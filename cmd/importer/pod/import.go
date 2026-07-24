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
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/cmd/importer/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
)

var realClock = clock.RealClock{}

type resourceNotCoveredError struct {
	Resource     corev1.ResourceName
	ClusterQueue string
}

func (e *resourceNotCoveredError) Error() string {
	return fmt.Sprintf("resource %q is not covered by ClusterQueue %q", e.Resource, e.ClusterQueue)
}

func (e *resourceNotCoveredError) Is(target error) bool {
	t, ok := target.(*resourceNotCoveredError)
	if !ok {
		return false
	}
	return e.Resource == t.Resource && e.ClusterQueue == t.ClusterQueue
}

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

		lq, skip, err := importCache.LocalQueue(p)
		if skip || err != nil {
			return skip, err
		}

		oldLq, found := p.Labels[controllerconstants.QueueLabel]
		if !found {
			if err := addLabels(ctx, c, p, lq.Name, importCache.AddLabels); err != nil {
				return false, fmt.Errorf("cannot add queue label: %w", err)
			}
		} else if oldLq != lq.Name {
			return false, fmt.Errorf("another local queue name is set %q expecting %q", oldLq, lq.Name)
		}

		kp := pod.FromObject(p)
		// Note: the recorder is not used for single pods, we can just pass nil for now.
		wl, err := kp.ConstructComposableWorkload(ctx, c, nil, nil, nil)
		if err != nil {
			return false, fmt.Errorf("construct workload: %w", err)
		}

		maps.Copy(wl.Labels, importCache.AddLabels)

		if pc, found := importCache.PriorityClasses[p.Spec.PriorityClassName]; found {
			wl.Spec.PriorityClassRef = kueue.NewPodPriorityClassRef(pc.Name)
			wl.Spec.Priority = &pc.Value
		}

		if err := createWorkload(ctx, c, wl); err != nil {
			return false, fmt.Errorf("creating workload: %w", err)
		}

		if err := admitWorkload(ctx, c, wl, importCache.ClusterQueues[string(lq.Spec.ClusterQueue)]); err != nil {
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

func addLabels(ctx context.Context, c client.Client, p *corev1.Pod, queue string, addLabels map[string]string) error {
	if p.Labels == nil {
		p.Labels = make(map[string]string)
	}
	p.Labels[controllerconstants.QueueLabel] = queue
	p.Labels[constants.ManagedByKueueLabelKey] = constants.ManagedByKueueLabelValue
	maps.Copy(p.Labels, addLabels)

	err := c.Update(ctx, p)
	retry, reload, timeout := checkError(err)

	for retry {
		if timeout >= 0 {
			select {
			case <-ctx.Done():
				return errors.New("context canceled")
			case <-time.After(timeout):
			}
		}
		if reload {
			err = c.Get(ctx, client.ObjectKeyFromObject(p), p)
			if err != nil {
				retry, reload, timeout = checkError(err)
				continue
			}
			if p.Labels == nil {
				p.Labels = make(map[string]string)
			}
			p.Labels[controllerconstants.QueueLabel] = queue
			p.Labels[constants.ManagedByKueueLabelKey] = constants.ManagedByKueueLabelValue
			maps.Copy(p.Labels, addLabels)
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
		if timeout >= 0 {
			select {
			case <-ctx.Done():
				return errors.New("context canceled")
			case <-time.After(timeout):
			}
		}
		err = c.Create(ctx, wl)
		retry, _, timeout = checkError(err)
	}
	return err
}

func admitWorkload(ctx context.Context, c client.Client, wl *kueue.Workload, cq *kueue.ClusterQueue) error {
	resourceFormatter := resources.NewResourceFormatter()
	update := func(wl *kueue.Workload) (bool, error) {
		// make its admission and update its status
		info := workload.NewInfo(wl)

		admission := kueue.Admission{
			ClusterQueue: kueue.ClusterQueueReference(cq.Name),
			PodSetAssignments: []kueue.PodSetAssignment{
				{
					Name:          info.TotalRequests[0].Name,
					Flavors:       make(map[corev1.ResourceName]kueue.ResourceFlavorReference),
					ResourceUsage: info.TotalRequests[0].Requests.ToResourceList(resourceFormatter),
					Count:         ptr.To[int32](1),
				},
			},
		}

		// sort requestedResources for deterministic handling order. This does not affect flavor assignments
		// (the Flavors map is order-independent), but it makes uncovered-resource errors deterministic
		// when multiple resources are missing coverage.
		requestedResources := make([]corev1.ResourceName, 0, len(info.TotalRequests[0].Requests))
		for r := range info.TotalRequests[0].Requests {
			requestedResources = append(requestedResources, r)
		}
		slices.Sort(requestedResources)

		for _, r := range requestedResources {
			flv := resourceFlavorForResource(cq, r)
			if flv == "" {
				return false, &resourceNotCoveredError{Resource: r, ClusterQueue: cq.Name}
			}
			admission.PodSetAssignments[0].Flavors[r] = flv
		}

		wl.Status.Admission = &admission
		reservedCond := metav1.Condition{
			Type:    kueue.WorkloadQuotaReserved,
			Status:  metav1.ConditionTrue,
			Reason:  "Imported",
			Message: fmt.Sprintf("Imported into ClusterQueue %s", cq.Name),
		}
		apimeta.SetStatusCondition(&wl.Status.Conditions, reservedCond)
		admittedCond := metav1.Condition{
			Type:    kueue.WorkloadAdmitted,
			Status:  metav1.ConditionTrue,
			Reason:  "Imported",
			Message: fmt.Sprintf("Imported into ClusterQueue %s", cq.Name),
		}
		apimeta.SetStatusCondition(&wl.Status.Conditions, admittedCond)
		return true, nil
	}

	for {
		err := workloadpatching.PatchAdmissionStatus(ctx, c, wl, realClock, update, workloadpatching.WithForceApply())
		retry, _, timeout := checkError(err)
		if !retry {
			if err != nil {
				return err
			}
			break
		}
		if timeout >= 0 {
			select {
			case <-ctx.Done():
				return errors.New("context canceled")
			case <-time.After(timeout):
			}
		}
	}

	return nil
}

// resourceFlavorForResource returns the first flavor from the first resource group
// that covers the requested resource. It skips groups with no flavors and returns
// an empty string when no matching group exists.
func resourceFlavorForResource(cq *kueue.ClusterQueue, resource corev1.ResourceName) kueue.ResourceFlavorReference {
	for _, rg := range cq.Spec.ResourceGroups {
		if len(rg.Flavors) == 0 {
			continue
		}

		if slices.Contains(rg.CoveredResources, resource) {
			return rg.Flavors[0].Name
		}
	}
	return ""
}
