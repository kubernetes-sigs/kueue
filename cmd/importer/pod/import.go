/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package pod

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/cmd/importer/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
)

var realClock = clock.RealClock{}

func Import(ctx context.Context, c client.Client, importCache *cache.ImportCache, jobs uint) error {
	ch := make(chan corev1.Pod)
	go func() {
		err := ListPods(ctx, c, importCache.Namespaces, ch)
		if err != nil {
			ctrl.LoggerFrom(ctx).Error(err, "Listing pods")
		}
	}()
	summary := ProcessConcurrently(ch, jobs, func(p *corev1.Pod) (bool, error) {
		log := ctrl.LoggerFrom(ctx).WithValues("pod", klog.KObj(p))
		log.V(3).Info("Importing")

		lq, cq, skip, err := resolveQueues(importCache, p)
		if skip || err != nil {
			return skip, err
		}

		// Import shares its Pod/ClusterQueue validation and Workload construction
		// with Check, so the two commands cannot disagree on what is importable.
		checked, err := checkPodWorkload(ctx, c, importCache, p, lq.Name, cq)
		if err != nil {
			return false, err
		}
		wl := checked.workload

		// checkPodWorkload already rejects a conflicting pre-existing queue label,
		// so a Pod reaching here either has none or one that already matches lq.Name.
		// It may still be missing the managed-by label or importCache.AddLabels,
		// e.g. on a re-run with a new --add-labels value.
		if podNeedsLabels(p, lq.Name, importCache.AddLabels) {
			if err := addLabels(ctx, c, p, lq.Name, importCache.AddLabels); err != nil {
				return false, fmt.Errorf("cannot add queue label: %w", err)
			}
		}

		if err := createWorkload(ctx, c, wl); err != nil {
			return false, fmt.Errorf("creating workload: %w", err)
		}

		if err := admitWorkload(ctx, c, wl, cq, checked.flavors, importCache.WorkloadInfoOptions()); err != nil {
			return false, err
		}
		log.V(2).Info("Successfully imported", "pod", klog.KObj(p), "workload", klog.KObj(wl))
		return false, nil
	})

	log := ctrl.LoggerFrom(ctx)
	log.Info("Import done", "checked", summary.TotalPods, "skipped", summary.SkippedPods, "failed", summary.FailedPods)
	for e, pods := range summary.ErrorsForPods {
		log.Info("Import failed for Pods", "err", e, "occurrences", len(pods), "observedFirstIn", pods[0])
	}
	return errors.Join(summary.Errors...)
}

func checkError(err error) (retry, reload bool, timeout time.Duration) {
	retrySeconds, retry := apierrors.SuggestsClientDelay(err)
	if retry {
		return true, false, time.Duration(retrySeconds) * time.Second
	}

	if apierrors.IsConflict(err) {
		return true, true, 0
	}
	return false, false, 0
}

// waitForRetry blocks for timeout, or returns early with an error if ctx is
// done first. A non-positive timeout returns immediately.
func waitForRetry(ctx context.Context, timeout time.Duration) error {
	if timeout <= 0 {
		return nil
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return errors.New("context canceled")
	case <-t.C:
		return nil
	}
}

// importLabels merges queue, the managed-by label, and the configured extra
// labels into the full label set a Pod must carry after import.
func importLabels(queue string, addLabels map[string]string) map[string]string {
	labels := make(map[string]string, len(addLabels)+2)
	maps.Copy(labels, addLabels)
	labels[controllerconstants.QueueLabel] = queue
	labels[constants.ManagedByKueueLabelKey] = constants.ManagedByKueueLabelValue
	return labels
}

// podNeedsLabels reports whether p is missing, or has a stale value for, any label from importLabels.
func podNeedsLabels(p *corev1.Pod, queue string, addLabels map[string]string) bool {
	for k, v := range importLabels(queue, addLabels) {
		if p.Labels[k] != v {
			return true
		}
	}
	return false
}

func addLabels(ctx context.Context, c client.Client, p *corev1.Pod, queue string, addLabels map[string]string) error {
	applyLabels := func() {
		if p.Labels == nil {
			p.Labels = make(map[string]string)
		}
		maps.Copy(p.Labels, importLabels(queue, addLabels))
	}

	applyLabels()
	err := c.Update(ctx, p)
	retry, reload, timeout := checkError(err)

	for retry {
		if err := waitForRetry(ctx, timeout); err != nil {
			return err
		}
		if reload {
			err = c.Get(ctx, client.ObjectKeyFromObject(p), p)
			if err != nil {
				retry, reload, timeout = checkError(err)
				continue
			}
			applyLabels()
		}
		err = c.Update(ctx, p)
		retry, reload, timeout = checkError(err)
	}
	return err
}

func createWorkload(ctx context.Context, c client.Client, wl *kueue.Workload) error {
	err := c.Create(ctx, wl)
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	retry, _, timeout := checkError(err)
	for retry {
		if err := waitForRetry(ctx, timeout); err != nil {
			return err
		}
		err = c.Create(ctx, wl)
		retry, _, timeout = checkError(err)
	}
	return err
}

func admitWorkload(
	ctx context.Context,
	c client.Client,
	wl *kueue.Workload,
	cq *kueue.ClusterQueue,
	flavors map[corev1.ResourceName]kueue.ResourceFlavorReference,
	workloadInfoOptions []workload.InfoOption,
) error {
	resourceFormatter := resources.NewResourceFormatter()
	update := func(wl *kueue.Workload) (bool, error) {
		info := workload.NewInfo(ctrl.LoggerFrom(ctx), wl, workloadInfoOptions...)
		admission := kueue.Admission{
			ClusterQueue: kueue.ClusterQueueReference(cq.Name),
			PodSetAssignments: []kueue.PodSetAssignment{
				{
					Name:          info.TotalRequests[0].Name,
					Flavors:       flavors,
					ResourceUsage: info.TotalRequests[0].Requests.ToResourceList(resourceFormatter),
					Count:         new(int32(1)),
				},
			},
		}
		msg := fmt.Sprintf("Imported into ClusterQueue %s", cq.Name)
		wl.Status.Admission = &admission
		apimeta.SetStatusCondition(&wl.Status.Conditions, metav1.Condition{
			Type:    kueue.WorkloadQuotaReserved,
			Status:  metav1.ConditionTrue,
			Reason:  "Imported",
			Message: msg,
		})
		apimeta.SetStatusCondition(&wl.Status.Conditions, metav1.Condition{
			Type:    kueue.WorkloadAdmitted,
			Status:  metav1.ConditionTrue,
			Reason:  "Imported",
			Message: msg,
		})
		return true, nil
	}

	const maxAttempts = 5
	for range maxAttempts {
		err := workloadpatching.PatchAdmissionStatus(ctx, c, wl, realClock, update, workloadpatching.WithForceApply())
		if err == nil {
			return nil
		}
		retry, reload, timeout := checkError(err)
		if !retry {
			return err
		}
		if waitErr := waitForRetry(ctx, timeout); waitErr != nil {
			return waitErr
		}
		if reload {
			if getErr := c.Get(ctx, client.ObjectKeyFromObject(wl), wl); getErr != nil {
				return getErr
			}
		}
	}
	return fmt.Errorf("admitting workload %s: too many conflicts", klog.KObj(wl))
}
