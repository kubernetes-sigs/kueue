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

// Package workloadslicing contains definitions to support workload slicing functionality.
package workloadslicing

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
)

// Workload slicing refers to a specialized Kueue feature designed to support workload scaling up.
// A workload slice represents a logical grouping of two workloads:
// the "old slice" (preemptible workload) and the "new slice" (preemptor workload).

// Workload slicing can be enabled for a specific integration job instance,
// provided that the integration supports workload slicing.
const (
	// EnabledAnnotationKey refers to the annotation key present on Job's that support
	// workload slicing.
	EnabledAnnotationKey = constants.ElasticJobAnnotation
	// EnabledAnnotationValue refers to the annotation value. To enable
	// workload slicing for a given job, we match both annotation key and value.
	EnabledAnnotationValue = "true"
)

// Enabled returns true if a provided job has slicing annotation indicator.
// Indicator matched on both annotation key and value.
func Enabled(object metav1.Object) bool {
	if object == nil {
		return false
	}
	return features.Enabled(features.ElasticJobsViaWorkloadSlices) && object.GetAnnotations()[EnabledAnnotationKey] == EnabledAnnotationValue
}

// IsElasticWorkload returns true if ElasticJobsViaWorkloadSlices feature gate is enabled
// and the given Workload is marked as elastic (e.g., via annotations or other criteria).
func IsElasticWorkload(workload *kueue.Workload) bool {
	if workload == nil {
		return false
	}
	return Enabled(workload)
}

const (
	// WorkloadSliceReplacementFor is the annotation key set on a new workload slice to indicate
	// the key of the workload slice it is intended to replace (i.e., the "old" slice being preempted).
	WorkloadSliceReplacementFor = "kueue.x-k8s.io/workload-slice-replacement-for"
)

// ReplacementForKey returns a value for workload "WorkloadSliceReplacementFor" annotation
func ReplacementForKey(wl *kueue.Workload) *workload.Reference {
	key, found := wl.GetAnnotations()[WorkloadSliceReplacementFor]
	if !found {
		return nil
	}
	ref := workload.Reference(key)
	return &ref
}

// SliceName returns the workload slice name for the given workload.
// This is the original workload name in the slice chain, used to identify pods
// across workload slice replacements. If the workload has the WorkloadSliceNameAnnotation,
// that value is returned; otherwise the workload's own name is returned.
func SliceName(wl *kueue.Workload) string {
	if sliceName, found := wl.Annotations[kueue.WorkloadSliceNameAnnotation]; found {
		return sliceName
	}
	return wl.Name
}

// FindNotFinishedWorkloads returns a sorted list of workloads "owned by" the provided job object/gvk combination and
// without "Finished" condition with status = "True".
func FindNotFinishedWorkloads(ctx context.Context, clnt client.Client, jobObject client.Object, jobObjectGVK schema.GroupVersionKind) ([]kueue.Workload, error) {
	list := &kueue.WorkloadList{}
	if err := clnt.List(ctx, list, client.InNamespace(jobObject.GetNamespace()), indexer.OwnerReferenceIndexFieldMatcher(jobObjectGVK, jobObject.GetName())); err != nil {
		return nil, err
	}

	// Sort oldest-first; break same-second ties by UID for stable ordering.
	slices.SortFunc(list.Items, func(a, b kueue.Workload) int {
		if c := a.CreationTimestamp.Compare(b.CreationTimestamp.Time); c != 0 {
			return c
		}
		return cmp.Compare(a.UID, b.UID)
	})

	// Filter out workloads with activated "Finished" condition.
	return slices.DeleteFunc(list.Items, func(w kueue.Workload) bool {
		return workloadfinish.IsFinished(&w)
	}), nil
}

// FindLatestActiveWorkload returns the newest non-finished workload slice owned
// by the provided job object/gvk that holds a quota reservation, or nil if none
// qualifies. This is the chain's "active" slice: its granted PodSet counts
// define the admitted capacity.
func FindLatestActiveWorkload(ctx context.Context, clnt client.Client, jobObject client.Object, jobObjectGVK schema.GroupVersionKind) (*kueue.Workload, error) {
	workloads, err := FindNotFinishedWorkloads(ctx, clnt, jobObject, jobObjectGVK)
	if err != nil {
		return nil, err
	}
	for i := range slices.Backward(workloads) {
		if workload.HasQuotaReservation(&workloads[i]) {
			return &workloads[i], nil
		}
	}
	return nil, nil
}

// ScaledDown returns true if the new pod sets represent a scale-down operation.
// This is determined by checking whether at least one new pod set has fewer replicas
// than its corresponding old pod set, and none of the old pod sets have fewer replicas
// than their corresponding new pod sets.
func ScaledDown(oldCounts, newCounts workload.PodSetsCounts) bool {
	return newCounts.HasFewerReplicasThan(oldCounts) && !oldCounts.HasFewerReplicasThan(newCounts)
}

// ScaledUp returns true if the given workload has the
// WorkloadSliceReplacementFor annotation, indicating that
// this workload is a scaled-up replacement for another.
func ScaledUp(workload *kueue.Workload) bool {
	return ReplacementForKey(workload) != nil
}

// EnsureWorkloadSlices processes the Job object and returns the appropriate workload slice.
//
// Returns:
// - *Workload, true, nil: when a compatible workload exists or a new slice is needed.
// - nil, false, nil: when an incompatible workload exists and no update is performed.
// - error: on failure to fetch, update, or deactivate a workload slice.
func EnsureWorkloadSlices(
	ctx context.Context,
	clnt client.Client,
	clk clock.Clock,
	jobPodSets []kueue.PodSet,
	jobObject client.Object,
	jobObjectGVK schema.GroupVersionKind,
) (*kueue.Workload, bool, error) {
	jobPodSetsCounts := workload.ExtractPodSetCounts(jobPodSets)

	workloads, err := FindNotFinishedWorkloads(ctx, clnt, jobObject, jobObjectGVK)
	if err != nil {
		return nil, true, fmt.Errorf("failed to find active workload slices: %w", err)
	}

	switch len(workloads) {
	case 0:
		// No existing slices found — new slice should be created.
		return nil, true, nil

	case 1:
		// A single active workload was found.
		wl := &workloads[0]
		wlPodSetsCounts := workload.ExtractPodSetCountsFromWorkload(wl)

		// Check if pod sets are structurally compatible (same number and names).
		if !jobPodSetsCounts.HasSamePodSetKeys(wlPodSetsCounts) {
			return nil, false, nil
		}

		// If counts match, return the existing workload slice.
		if jobPodSetsCounts.EqualTo(wlPodSetsCounts) {
			return wl, true, nil
		}

		// Allow updating the existing slice if:
		// a. It hasn't been admitted (no quota reserved), or
		// b. It's a scale-down event.
		if !workload.HasQuotaReservation(wl) || ScaledDown(wlPodSetsCounts, jobPodSetsCounts) {
			workload.ApplyPodSetCounts(wl, jobPodSetsCounts)
			if err := clnt.Update(ctx, wl); err != nil {
				return nil, true, fmt.Errorf("failed to update workload's pod sets counts: %w", err)
			}
			return wl, true, nil
		}

		// Scale-up on admitted workload → create a new slice.
		return nil, true, nil

	default:
		selectedWorkload, err := normalizeActiveSlices(ctx, clnt, clk, workloads)
		if err != nil {
			return nil, true, err
		}
		if selectedWorkload == nil {
			return nil, true, nil
		}

		selectedCounts := workload.ExtractPodSetCountsFromWorkload(selectedWorkload)

		if !jobPodSetsCounts.HasSamePodSetKeys(selectedCounts) {
			return nil, false, nil
		}

		if jobPodSetsCounts.EqualTo(selectedCounts) {
			return selectedWorkload, true, nil
		}

		if !workload.HasQuotaReservation(selectedWorkload) || ScaledDown(selectedCounts, jobPodSetsCounts) {
			workload.ApplyPodSetCounts(selectedWorkload, jobPodSetsCounts)
			if err := clnt.Update(ctx, selectedWorkload); err != nil {
				return nil, true, fmt.Errorf("failed to update workload pod set counts: %w", err)
			}
			return selectedWorkload, true, nil
		}

		// Scale-up on admitted selected workload — create a new slice.
		return nil, true, nil
	}
}

// normalizeActiveSlices enforces the workload slice invariant:
//   - One non-evicted admitted workload (latestWithQuotaReservation)
//   - At most one non-evicted pending replacement that directly replaces it
//   - When no non-evicted admitted workload exists, the newest non-evicted
//     workload is kept
//   - Evicted workloads are always finished (they hold quota that must be released)
func normalizeActiveSlices(
	ctx context.Context,
	clnt client.Client,
	clk clock.Clock,
	workloads []kueue.Workload,
) (*kueue.Workload, error) {
	log := ctrl.LoggerFrom(ctx)

	// Index replacements by the workload they replace. On duplicate claims
	// (race-created forks), prefer the admitted one.
	replacements := make(map[workload.Reference]*kueue.Workload)
	for i := range workloads {
		wl := &workloads[i]
		if replKey := ReplacementForKey(wl); replKey != nil && !workloadevict.IsEvicted(wl) {
			if existing, ok := replacements[*replKey]; !ok || (!workload.HasQuotaReservation(existing) && workload.HasQuotaReservation(wl)) {
				replacements[*replKey] = wl
			}
		}
	}

	// Find the admitted workload at the head of the replacement chain: the one
	// whose replacement (if any) is not itself admitted.
	var latestWithQuotaReservation, pendingReplacement, latestNonEvicted *kueue.Workload
	for i := range workloads {
		wl := &workloads[i]
		if workloadevict.IsEvicted(wl) {
			continue
		}
		if latestNonEvicted == nil || wl.CreationTimestamp.After(latestNonEvicted.CreationTimestamp.Time) {
			latestNonEvicted = wl
		}
		if !workload.HasQuotaReservation(wl) {
			continue
		}
		// Skip if replaced by another admitted workload.
		if repl, ok := replacements[workload.Key(wl)]; ok && workload.HasQuotaReservation(repl) {
			continue
		}
		latestWithQuotaReservation = wl
	}

	if latestWithQuotaReservation != nil {
		if repl, ok := replacements[workload.Key(latestWithQuotaReservation)]; ok {
			if !workload.HasQuotaReservation(repl) {
				pendingReplacement = repl
			}
		}
	}

	log.V(3).Info("Classified workload slices",
		"total", len(workloads),
		"latestWithQuotaReservation", klog.KObj(latestWithQuotaReservation),
		"pendingReplacement", klog.KObj(pendingReplacement),
		"latestNonEvicted", klog.KObj(latestNonEvicted))

	var selectedWorkload *kueue.Workload
	switch {
	case pendingReplacement != nil:
		selectedWorkload = pendingReplacement
	case latestWithQuotaReservation != nil:
		selectedWorkload = latestWithQuotaReservation
	default:
		selectedWorkload = latestNonEvicted
	}

	for i := range workloads {
		wl := &workloads[i]
		if wl == selectedWorkload || wl == latestWithQuotaReservation {
			continue
		}
		log.V(2).Info("Finishing out-of-sync workload slice", "workload", workload.Key(wl))
		if err := workloadfinish.Finish(ctx, clnt, wl, kueue.WorkloadFinishedReasonOutOfSync,
			"The workload slice is out of sync with its parent job", clk); err != nil {
			return nil, err
		}
	}

	return selectedWorkload, nil
}

// ReplacedWorkloadSlice returns the replacement workload slice for the given workload `wl`
// if it has been marked as replaced by another slice. This is determined using the
// `ReplacementForKey` annotation or metadata.
//
// It returns a slice containing the corresponding preemption.Target for the replacement,
// as well as a pointer to the replacement workload.Info object.
//
// Returns nil if:
// - The ElasticJobsViaWorkloadSlices feature gate is not enabled
// - The input workload or snapshot is nil
// - The workload has no replacement slice key
// - The referenced replacement workload is not found in the ClusterQueue snapshot
func ReplacedWorkloadSlice(wl *workload.Info, snap *schdcache.Snapshot) ([]*preemption.Target, *workload.Info) {
	if !features.Enabled(features.ElasticJobsViaWorkloadSlices) || wl == nil || snap == nil {
		return nil, nil
	}

	sliceKey := ReplacementForKey(wl.Obj)
	if sliceKey == nil {
		return nil, nil
	}

	// Retrieve cluster queue.
	queue := snap.ClusterQueue(wl.ClusterQueue)
	if queue == nil {
		return nil, nil
	}

	replaced, found := queue.Workloads[*sliceKey]
	if !found {
		return nil, nil
	}

	if replaced.Obj.Namespace != wl.Obj.Namespace {
		return nil, nil
	}

	return []*preemption.Target{{WorkloadInfo: replaced}}, replaced
}

// IsReplaced returns true if the workload status contains active WorkloadFinish condition
// with WorkloadSliceReplaced reason.
func IsReplaced(status kueue.WorkloadStatus) bool {
	finishedCondition := apimeta.FindStatusCondition(status.Conditions, kueue.WorkloadFinished)
	return finishedCondition != nil && finishedCondition.Status == metav1.ConditionTrue &&
		finishedCondition.Reason == kueue.WorkloadSliceReplaced
}

// FindReplacedSliceTarget identifies and removes a preempted workload slice target from the given list of targets.
// The function checks if Elastic Jobs via Workload Slices feature is enabled and if so, attempts to find a matching
// workload slice in the target list for the provided preemptor. If a matching slice is found, it is removed from the list
// and returned alongside the original target list.
//
// This function performs the following:
// 1. It checks if the feature `ElasticJobsViaWorkloadSlices` is enabled. If not, it returns the target list unchanged with `nil` as the second return value.
// 2. It generates a replacement key for the preemptor workload slice. If no replacement key is found, it returns the original target list unchanged with `nil`.
// 3. It iterates over the list of preemption targets, looking for a target that matches the replacement key.
// 4. If a matching target is found, it removes it from the list and returns the updated list and the removed target.
// 5. If no matching target is found, it returns the original target list and `nil`.
func FindReplacedSliceTarget(preemptor *kueue.Workload, targets []*preemption.Target) ([]*preemption.Target, *preemption.Target) {
	if !features.Enabled(features.ElasticJobsViaWorkloadSlices) {
		return targets, nil
	}
	sliceKey := ReplacementForKey(preemptor)
	if sliceKey == nil {
		return targets, nil
	}
	for i := range targets {
		if *sliceKey == workload.Key(targets[i].WorkloadInfo.Obj) {
			return append(targets[:i], targets[i+1:]...), targets[i]
		}
	}
	return targets, nil
}
