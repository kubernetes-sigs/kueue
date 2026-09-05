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
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/cmd/importer/cache"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/workload"
)

// checkedWorkload is the outcome of validating a Pod against its target
// ClusterQueue: the Workload it would produce, the flavor assigned to each
// requested resource, and the resolved priority.
type checkedWorkload struct {
	workload *kueue.Workload
	flavors  map[corev1.ResourceName]kueue.ResourceFlavorReference
	priority int32
}

func Check(ctx context.Context, c client.Client, importCache *cache.ImportCache, jobs uint) error {
	ch := make(chan corev1.Pod)
	go func() {
		err := ListPods(ctx, c, importCache.Namespaces, ch)
		if err != nil {
			ctrl.LoggerFrom(ctx).Error(err, "Listing pods")
		}
	}()
	summary := ProcessConcurrently(ch, jobs, func(p *corev1.Pod) (bool, error) {
		log := ctrl.LoggerFrom(ctx).WithValues("pod", klog.KObj(p))
		log.V(3).Info("Checking")

		lq, cq, skip, err := resolveQueues(importCache, p)
		if skip || err != nil {
			return skip, err
		}

		checked, err := checkPodWorkload(ctx, c, importCache, p, lq.Name, cq)
		if err != nil {
			return false, err
		}

		// flavors reflects the per-resource assignments validated against the workload's requests.
		log.V(2).Info("Successfully checked", "clusterQueue", klog.KObj(cq), "priority", checked.priority, "flavors", checked.flavors)
		return false, nil
	})

	log := ctrl.LoggerFrom(ctx)
	log.Info("Check done", "checked", summary.TotalPods, "skipped", summary.SkippedPods, "failed", summary.FailedPods)
	for e, pods := range summary.ErrorsForPods {
		log.Info("Validation failed for Pods", "err", e, "occurrences", len(pods), "observedFirstIn", pods[0])
	}
	return errors.Join(summary.Errors...)
}

// resolveQueues resolves the Pod to its LocalQueue and ClusterQueue.
// It returns skip=true when mapping says this Pod should be skipped.
func resolveQueues(importCache *cache.ImportCache, p *corev1.Pod) (*kueue.LocalQueue, *kueue.ClusterQueue, bool, error) {
	lq, skip, err := importCache.LocalQueueForPod(p)
	if skip || err != nil {
		return nil, nil, skip, err
	}
	cq, ok := importCache.ClusterQueues[string(lq.Spec.ClusterQueue)]
	if !ok {
		return nil, nil, false, fmt.Errorf("cluster queue not found in cache: %s: %w", lq.Spec.ClusterQueue, cache.ErrCQNotFound)
	}
	return lq, cq, false, nil
}

// checkPodWorkload validates p against the target ClusterQueue and returns the
// Workload that would be created, its resource-flavor assignments, and the
// resolved priority.
func checkPodWorkload(ctx context.Context, c client.Client, importCache *cache.ImportCache, p *corev1.Pod, lqName string, cq *kueue.ClusterQueue) (*checkedWorkload, error) {
	if oldLq, found := p.Labels[controllerconstants.QueueLabel]; found && oldLq != lqName {
		return nil, &queueLabelConflictError{CurrentQueue: oldLq, ExpectedQueue: lqName}
	}
	if len(cq.Spec.ResourceGroups) == 0 {
		return nil, fmt.Errorf("%q has no resource groups: %w", cq.Name, cache.ErrCQInvalid)
	}
	if err := importCache.FlavorValidationForClusterQueue(kueue.ClusterQueueReference(cq.Name)); err != nil {
		return nil, err
	}

	// Workload construction derives queue, name, and some spec fields from Pod labels.
	// Build it from a copy that includes importer-added labels, while leaving the
	// real Pod untouched until validation succeeds and those labels can be persisted.
	podForWorkload := preparePodForWorkload(p, lqName, importCache.AddLabels)

	kp := pod.FromObject(podForWorkload)
	// Note: the recorder is not used for single pods, we can just pass nil for now.
	wl, err := kp.ConstructComposableWorkload(ctx, c, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("construct workload: %w", err)
	}
	if prebuiltWorkloadName := jobframework.PrebuiltWorkloadNameFor(podForWorkload); prebuiltWorkloadName != "" {
		wl.Name = prebuiltWorkloadName
	}
	// Keep the resolved queue authoritative even if the real Pod has not been labeled
	// yet or still carries a conflicting queue label that Import rejects separately.
	wl.Spec.QueueName = kueue.LocalQueueName(lqName)
	// Generic label copying is disabled in this importer path, so preserve the
	// importer-added labels on the constructed Workload metadata explicitly.
	if wl.Labels == nil {
		wl.Labels = make(map[string]string)
	}
	maps.Copy(wl.Labels, importCache.AddLabels)

	info := workload.NewInfo(ctrl.LoggerFrom(ctx), wl, importCache.WorkloadInfoOptions()...)
	flavors, err := flavorAssignmentsForRequests(importCache.FlavorsByResourceForClusterQueue(kueue.ClusterQueueReference(cq.Name)), cq.Name, info.TotalRequests[0].Requests)
	if err != nil {
		return nil, err
	}

	var pv int32
	if pc, found := importCache.PriorityClasses[p.Spec.PriorityClassName]; found {
		pv = pc.Value
		wl.Spec.PriorityClassRef = kueue.NewPodPriorityClassRef(pc.Name)
		wl.Spec.Priority = &pc.Value
	} else if p.Spec.PriorityClassName != "" {
		return nil, fmt.Errorf("%q: %w", p.Spec.PriorityClassName, cache.ErrPCNotFound)
	}

	return &checkedWorkload{workload: wl, flavors: flavors, priority: pv}, nil
}

// flavorAssignmentsForRequests assigns a flavor to each non-zero requested
// resource, using the ClusterQueue's precomputed resource-to-flavor map.
func flavorAssignmentsForRequests(
	flavorsByResource map[corev1.ResourceName]kueue.ResourceFlavorReference,
	cqName string,
	requests resources.Requests,
) (map[corev1.ResourceName]kueue.ResourceFlavorReference, error) {
	type rq struct {
		name corev1.ResourceName
		qty  int64
	}
	pairs := make([]rq, 0, requests.Len())
	requests.ForEach(func(name corev1.ResourceName, quantity int64) {
		pairs = append(pairs, rq{name, quantity})
	})
	slices.SortFunc(pairs, func(a, b rq) int { return strings.Compare(string(a.name), string(b.name)) })

	flavors := make(map[corev1.ResourceName]kueue.ResourceFlavorReference)
	for _, p := range pairs {
		if p.qty == 0 {
			continue
		}
		flv, ok := flavorsByResource[p.name]
		if !ok {
			return nil, &resourceNotCoveredError{Resource: p.name, ClusterQueue: cqName}
		}
		flavors[p.name] = flv
	}

	return flavors, nil
}

// preparePodForWorkload returns a labeled copy of p for Workload construction.
// Callers must have already confirmed p has no conflicting queue label.
func preparePodForWorkload(p *corev1.Pod, queue string, addLabels map[string]string) *corev1.Pod {
	podForWorkload := p.DeepCopy()
	if podForWorkload.Labels == nil {
		podForWorkload.Labels = make(map[string]string)
	}
	maps.Copy(podForWorkload.Labels, addLabels)
	podForWorkload.Labels[controllerconstants.QueueLabel] = queue
	return podForWorkload
}
