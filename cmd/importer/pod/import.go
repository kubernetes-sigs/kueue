/*
Copyright 2024 The Kubernetes Authors.

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
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/cmd/importer/util"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
)

func Import(ctx context.Context, c client.Client, cache *util.ImportCache, jobs uint) error {
	ch := make(chan corev1.Pod)
	go func() {
		err := util.PushPods(ctx, c, cache.Namespaces, ch)
		if err != nil {
			ctrl.LoggerFrom(ctx).Error(err, "Listing pods")
		}
	}()
	summary := util.ConcurrentProcessPod(ch, jobs, func(p *corev1.Pod) (bool, error) {
		log := ctrl.LoggerFrom(ctx).WithValues("pod", klog.KObj(p))
		log.V(3).Info("Importing")

		lq, skip, err := cache.LocalQueue(p)
		if skip || err != nil {
			return skip, err
		}

		oldLq, found := p.Labels[controllerconstants.QueueLabel]
		if !found {
			if err := addLabels(ctx, c, p, lq.Name, cache.AddLabels); err != nil {
				return false, fmt.Errorf("cannot add queue label: %w", err)
			}
		} else if oldLq != lq.Name {
			return false, fmt.Errorf("another local queue name is set %q expecting %q", oldLq, lq.Name)
		}

		kp := pod.FromObject(p)
		// Note: the recorder is not used for single pods, we can just pass nil for now.
		wl, err := kp.ConstructComposableWorkload(ctx, c, nil, nil)
		if err != nil {
			return false, fmt.Errorf("construct workload: %w", err)
		}

		maps.Copy(wl.Labels, cache.AddLabels)

		if pc, found := cache.PriorityClasses[p.Spec.PriorityClassName]; found {
			wl.Spec.PriorityClassName = pc.Name
			wl.Spec.Priority = &pc.Value
			wl.Spec.PriorityClassSource = constants.PodPriorityClassSource
		}

		if err := createWorkload(ctx, c, wl); err != nil {
			return false, fmt.Errorf("creating workload: %w", err)
		}

		// make its admission and update its status
		info := workload.NewInfo(wl)
		cq := cache.ClusterQueues[string(lq.Spec.ClusterQueue)]
		admission := kueue.Admission{
			ClusterQueue: kueue.ClusterQueueReference(cq.Name),
			PodSetAssignments: []kueue.PodSetAssignment{
				{
					Name:          info.TotalRequests[0].Name,
					Flavors:       make(map[corev1.ResourceName]kueue.ResourceFlavorReference),
					ResourceUsage: info.TotalRequests[0].Requests.ToResourceList(),
					Count:         ptr.To[int32](1),
				},
			},
		}
		flv := cq.Spec.ResourceGroups[0].Flavors[0].Name
		for r := range info.TotalRequests[0].Requests {
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
		if err := admitWorkload(ctx, c, wl); err != nil {
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
	p.Labels[controllerconstants.QueueLabel] = queue
	p.Labels[pod.ManagedLabelKey] = pod.ManagedLabelValue
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
			p.Labels[controllerconstants.QueueLabel] = queue
			p.Labels[pod.ManagedLabelKey] = pod.ManagedLabelValue
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

func admitWorkload(ctx context.Context, c client.Client, wl *kueue.Workload) error {
	err := workload.ApplyAdmissionStatus(ctx, c, wl, false)
	retry, _, timeout := checkError(err)
	for retry {
		if timeout >= 0 {
			select {
			case <-ctx.Done():
				return errors.New("context canceled")
			case <-time.After(timeout):
			}
		}
		err = workload.ApplyAdmissionStatus(ctx, c, wl, false)
		retry, _, timeout = checkError(err)
	}
	return err
}
