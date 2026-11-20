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

package jobframework

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	cmputil "sigs.k8s.io/kueue/pkg/util/cmp"
	"sigs.k8s.io/kueue/pkg/util/parallelize"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

// reconcilePartialPreemption evicts the Pods above the retained count of an admitted elastic job
// whose Workload carries a pending partial-preemption request, so each PodSet drains to its
// retained count. The integration chooses which Pods leave (see JobWithPartialPreemption);
// without the hook nothing is evicted.
// Eviction goes through the Eviction API so PodDisruptionBudgets are honored.
func (r *JobReconciler) reconcilePartialPreemption(ctx context.Context, job GenericJob, wl *kueue.Workload) error {
	if wl == nil || wl.Status.Admission == nil ||
		!features.Enabled(features.PartialPreemption) ||
		!workload.HasPendingPreemptionRetainedCount(wl) {
		return nil
	}
	targets := make(map[kueue.PodSetReference]int32)
	for i := range wl.Status.Admission.PodSetAssignments {
		psa := &wl.Status.Admission.PodSetAssignments[i]
		if psa.PartialPreemption != nil && psa.PartialPreemption.RetainedCount != nil {
			targets[psa.Name] = *psa.PartialPreemption.RetainedCount
		}
	}
	if len(targets) == 0 {
		return nil
	}
	pods, err := r.reclaimCandidates(ctx, job, wl, targets)
	if err != nil || len(pods) == 0 {
		return err
	}
	return parallelize.Until(ctx, len(pods), func(i int) error {
		return evictPodForReclaim(ctx, r.client, ctrl.LoggerFrom(ctx), pods[i])
	})
}

// reclaimCandidates returns the Pods the integration chooses to evict. Only integrations that
// implement JobWithPartialPreemption reclaim Pods; others return none.
func (r *JobReconciler) reclaimCandidates(ctx context.Context, job GenericJob, wl *kueue.Workload, targets map[kueue.PodSetReference]int32) ([]*corev1.Pod, error) {
	ppJob, ok := job.(JobWithPartialPreemption)
	if !ok {
		return nil, nil
	}
	return ppJob.PodsToReclaim(ctx, r.client, wl, targets)
}

// PropagatePartialPreemptionAnnotation copies the partial-preemption opt-in annotation from
// obj to wl in memory, without persisting the change.
func PropagatePartialPreemptionAnnotation(obj client.Object, wl *kueue.Workload) bool {
	jobEnabled := obj.GetAnnotations()[constants.PartialPreemptionAnnotation] == "true"
	_, wlHasAnnotation := wl.Annotations[constants.PartialPreemptionAnnotation]
	if jobEnabled == wlHasAnnotation {
		return false
	}
	if jobEnabled {
		metav1.SetMetaDataAnnotation(&wl.ObjectMeta, constants.PartialPreemptionAnnotation, "true")
	} else {
		delete(wl.Annotations, constants.PartialPreemptionAnnotation)
	}
	return true
}

// UpdatePartialPreemptionAnnotation patches each workload's partial-preemption opt-in
// annotation to match obj, skipping the request when the value is already up to date.
func UpdatePartialPreemptionAnnotation(ctx context.Context, c client.Client, obj client.Object, wls ...*kueue.Workload) error {
	if !features.Enabled(features.PartialPreemption) {
		return nil
	}
	for _, wl := range wls {
		if wl == nil {
			continue
		}
		if err := clientutil.Patch(ctx, c, wl, func() (bool, error) {
			return PropagatePartialPreemptionAnnotation(obj, wl), nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// evictPodForReclaim evicts one Pod through the Eviction API.
func evictPodForReclaim(ctx context.Context, c client.Client, log logr.Logger, pod *corev1.Pod) error {
	eviction := &policyv1.Eviction{
		Name:      pod.Name,
		Namespace: pod.Namespace,
	}
	if err := c.SubResource("eviction").Create(ctx, pod, eviction); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		if apierrors.IsTooManyRequests(err) {
			log.V(3).Info("partial-preemption eviction blocked by PodDisruptionBudget, will retry", "pod", klog.KObj(pod))
			return err
		}
		return fmt.Errorf("evicting pod %s: %w", klog.KObj(pod), err)
	}
	log.V(3).Info("evicted elastic pod above partial-preemption target", "pod", klog.KObj(pod))
	return nil
}

// ParsePartialPreemptionMinRetainedCount returns the partial-preemption minimum set by the
// kueue.x-k8s.io/podset-partial-preemption-min-retained-count annotation, or nil if the annotation is
// absent. It returns an error if the value is not a non-negative 32-bit integer.
func ParsePartialPreemptionMinRetainedCount(annotations map[string]string) (*int32, error) {
	value, found := annotations[constants.PodSetPartialPreemptionMinRetainedCountAnnotation]
	if !found {
		return nil, nil
	}
	minRetainedCount, err := strconv.ParseInt(value, 10, 32)
	if err != nil || minRetainedCount < 0 {
		return nil, errors.New("must be a non-negative integer")
	}
	return new(int32(minRetainedCount)), nil
}

// PodsAboveTargets returns the Pods to evict so that each PodSet keeps at most its target of
// ungated, non-terminating Pods. Unscheduled Pods go first, then the lowest pod-deletion-cost,
// then the newest.
func PodsAboveTargets(ctx context.Context, c client.Client, wl *kueue.Workload, targets map[kueue.PodSetReference]int32) ([]*corev1.Pod, error) {
	pods, err := workloadslicing.ListPodsForWorkloadSlice(ctx, c, wl.Namespace, workloadslicing.SliceName(wl))
	if err != nil {
		return nil, err
	}
	counts := make(map[kueue.PodSetReference]int32, len(targets))
	var candidates []*corev1.Pod
	for _, p := range pods {
		ps := kueue.PodSetReference(p.Labels[constants.PodSetLabel])
		if _, ok := targets[ps]; !ok || utilpod.IsTerminated(p) || p.DeletionTimestamp != nil || utilpod.HasGate(p, kueue.ElasticJobSchedulingGate) {
			continue
		}
		counts[ps]++
		candidates = append(candidates, p)
	}
	slices.SortFunc(candidates, compareForEviction)
	var toEvict []*corev1.Pod
	for _, p := range candidates {
		ps := kueue.PodSetReference(p.Labels[constants.PodSetLabel])
		if counts[ps] > targets[ps] {
			toEvict = append(toEvict, p)
			counts[ps]--
		}
	}
	return toEvict, nil
}

// compareForEviction orders Pods so that the ones to evict first come first.
func compareForEviction(a, b *corev1.Pod) int {
	return cmp.Or(
		cmputil.CompareBool(utilpod.IsScheduled(a), utilpod.IsScheduled(b)),
		cmp.Compare(podDeletionCost(a), podDeletionCost(b)),
		b.CreationTimestamp.Compare(a.CreationTimestamp.Time),
		cmp.Compare(a.Name, b.Name),
	)
}

// podDeletionCost returns the Pod's deletion cost, or 0 if it is unset or invalid, as the
// ReplicaSet controller does.
func podDeletionCost(p *corev1.Pod) int32 {
	cost, err := strconv.ParseInt(p.Annotations[corev1.PodDeletionCost], 10, 32)
	if err != nil {
		return 0
	}
	return int32(cost)
}
