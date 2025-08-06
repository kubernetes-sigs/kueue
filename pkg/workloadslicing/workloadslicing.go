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
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	"sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/workload"
)

// Workload slicing refers to a specialized Kueue feature designed to support workload scaling up.
// A workload slice represents a logical grouping of two workloads:
// the "old slice" (preemptible workload) and the "new slice" (preemptor workload).

// Workload slicing can be enabled for a specific integration job instance,
// provided that the integration supports workload slicing.
const (
	// EnabledAnnotationKey refers to the annotation key present on Job's that support
	// workload slicing.
	EnabledAnnotationKey = "kueue.x-k8s.io/elastic-job"
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
	return object.GetAnnotations()[EnabledAnnotationKey] == EnabledAnnotationValue
}

const (
	// WorkloadSliceReplacementFor is the annotation key used to capture an "old" workload slice key
	// that will be preempted by the "new", e.g., this workload slice with annotation.
	WorkloadSliceReplacementFor = "kueue.x-k8s.io/workload-slice-replacement-for"
)

// ReplacementForKey returns a value for workload "WorkloadSliceReplacementFor" annotation
// key if this workload was annotated with such, otherwise, returns an empty string.
func ReplacementForKey(wl *kueue.Workload) *workload.Reference {
	annotations := wl.GetAnnotations()
	if len(annotations) == 0 {
		return nil
	}
	key, found := annotations[WorkloadSliceReplacementFor]
	if !found {
		return nil
	}
	ref := workload.Reference(key)
	return &ref
}

// Finish updates the status of a workload slice by applying the "Finished" condition.
// The function checks if the "Finished" condition is already applied, and if so, does nothing (NOOP).
// If the "Finished" condition is not present, it applies the condition with the provided `reason` and `message`.
//
// This function performs the following:
// 1. It checks if the "Finished" condition is already applied. If true, it returns immediately, doing nothing.
// 2. If the "Finished" condition is not set, it patches the workload slice's status to add the "Finished" condition.
// 3. If the patch fails, it returns an error.
func Finish(ctx context.Context, clnt client.Client, workloadSlice *kueue.Workload, reason, message string) error {
	// NOOP if the workload already has "Finished" condition (irrespective of reason and message values).
	if apimeta.IsStatusConditionTrue(workloadSlice.Status.Conditions, kueue.WorkloadFinished) {
		return nil
	}
	if err := clientutil.PatchStatus(ctx, clnt, workloadSlice, func() (bool, error) {
		return apimeta.SetStatusCondition(&workloadSlice.Status.Conditions, metav1.Condition{
			Type:    kueue.WorkloadFinished,
			Status:  metav1.ConditionTrue,
			Reason:  reason,
			Message: message,
		}), nil
	}); err != nil {
		return fmt.Errorf("failed to patch workload slice status: %w", err)
	}
	return nil
}

// FindNotFinishedWorkloads returns a sorted list of workloads "owned by" the provided job object/gvk combination and
// without "Finished" condition with status = "True".
func FindNotFinishedWorkloads(ctx context.Context, clnt client.Client, jobObject client.Object, jobObjectGVK schema.GroupVersionKind) ([]kueue.Workload, error) {
	list := &kueue.WorkloadList{}
	if err := clnt.List(ctx, list, client.InNamespace(jobObject.GetNamespace()), indexer.OwnerReferenceIndexFieldMatcher(jobObjectGVK, jobObject.GetName())); err != nil {
		return nil, err
	}

	// Sort workloads by creation timestamp, oldest first.
	// In the rare case that two workload slices have identical creationTimestamp values
	// (due to RFC3339 second-level precision), use WorkloadSliceReplacementFor
	// as a tiebreaker. This edge case is uncommon in production but can occur in
	// integration or e2e tests where the original and scaled-up workloads are created
	// in rapid succession.
	sort.Slice(list.Items, func(i, j int) bool {
		a, b := list.Items[i], list.Items[j]
		if a.CreationTimestamp.Equal(&b.CreationTimestamp) {
			return b.Annotations[WorkloadSliceReplacementFor] == string(workload.Key(&a))
		}
		return a.CreationTimestamp.Before(&b.CreationTimestamp)
	})

	// Filter out workloads with activated "Finished" condition.
	return slices.DeleteFunc(list.Items, func(w kueue.Workload) bool {
		return apimeta.IsStatusConditionTrue(w.Status.Conditions, kueue.WorkloadFinished)
	}), nil
}

// ScaledDown returns true if the new pod sets represent a scale-down operation.
// This is determined by checking whether at least one new pod set has fewer replicas
// than its corresponding old pod set, and none of the old pod sets have fewer replicas
// than their corresponding new pod sets.
func ScaledDown(oldCounts, newCounts workload.PodSetsCounts) bool {
	return newCounts.HasFewerReplicasThan(oldCounts) && !oldCounts.HasFewerReplicasThan(newCounts)
}

// EnsureWorkloadSlices processes the Job object and returns the appropriate workload slice.
//
// Returns:
// - *Workload, true, nil: when a compatible workload exists or a new slice is needed.
// - nil, false, nil: when an incompatible workload exists and no update is performed.
// - error: on failure to fetch, update, or deactivate a workload slice.
func EnsureWorkloadSlices(ctx context.Context, clnt client.Client, jobPodSets []kueue.PodSet, jobObject client.Object, jobObjectGVK schema.GroupVersionKind) (*kueue.Workload, bool, error) {
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

	case 2:
		// Two active workload slices detected.
		//
		// This transient state typically occurs when a new slice has been created,
		// and the old slice is pending deactivation. The system should resolve this
		// by deactivating the old slice, after which processing will continue under "case #1".
		oldWorkload := workloads[0]

		// Finish the old workload slice if it lost its quota reservation or if it was
		// explicitly evicted.
		if evictedCondition := apimeta.FindStatusCondition(oldWorkload.Status.Conditions, kueue.WorkloadEvicted); !workload.HasQuotaReservation(&oldWorkload) || evictedCondition != nil {
			// Finish the old workload slice as out of sync.
			if err := Finish(ctx, clnt, &oldWorkload, kueue.WorkloadFinishedReasonOutOfSync, "The workload slice is out of sync with its parent job"); err != nil {
				return nil, true, err
			}
		}

		// We consider the new workload slice only when evaluating against the incoming job (pod sets).
		newWorkload := workloads[1]
		newCounts := workload.ExtractPodSetCountsFromWorkload(&newWorkload)

		// Check if new workload and job's pod sets are compatible (have the same keys and keys count)
		if !jobPodSetsCounts.HasSamePodSetKeys(newCounts) {
			return nil, false, nil
		}

		// Return an error if the new workload has a reserved quota.
		//
		// A combination of a new workload with a reserved quota and an active old workload is considered an anomaly.
		// This condition may indicate a race condition or external interference. Specifically, a new workload slice
		// should never gain a quota reservation without the prior finalization of the old slice.
		if workload.HasQuotaReservation(&newWorkload) {
			return nil, true, errors.New("unexpected combination of old and new workload slices with reserved quota")
		}

		// If the pod set counts match, return new workload slice.
		if jobPodSetsCounts.EqualTo(newCounts) {
			return &newWorkload, true, nil
		}

		// Update new workload slice.
		workload.ApplyPodSetCounts(&newWorkload, jobPodSetsCounts)
		if err := clnt.Update(ctx, &newWorkload); err != nil {
			return nil, true, fmt.Errorf("failed to update workload pod set counts: %w", err)
		}
		return &newWorkload, true, nil
	default:
		// More than two matching slices is unexpected and considered an error.
		return nil, true, fmt.Errorf("unexpected number of matching workload slices: %d", len(workloads))
	}
}

// StartWorkloadSlicePods identifies pods associated with the provided parent object
// that are gated by the ElasticJobSchedulingGate scheduling gate and removes this gate,
// allowing them to be considered for scheduling.
//
// This function performs the following steps:
// 1. Lists all pods in the same namespace with an OwnerReference UID matching the parent object.
// 2. For each pod, removes the ElasticJobSchedulingGate scheduling gate if present.
//
// Returns:
// - An error if any of the operations fail; otherwise, nil.
func StartWorkloadSlicePods(ctx context.Context, clnt client.Client, object client.Object) error {
	list := &corev1.PodList{}
	if err := clnt.List(ctx, list, client.InNamespace(object.GetNamespace()), client.MatchingFields{indexer.OwnerReferenceUID: string(object.GetUID())}); err != nil {
		return fmt.Errorf("failed to list job pods: %w", err)
	}
	for i := range list.Items {
		if err := clientutil.Patch(ctx, clnt, &list.Items[i], true, func() (bool, error) {
			return pod.Ungate(&list.Items[i], kueue.ElasticJobSchedulingGate), nil
		}); err != nil {
			return fmt.Errorf("failed to patch pod: %w", err)
		}
	}
	return nil
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
func ReplacedWorkloadSlice(wl *workload.Info, snap *cache.Snapshot) ([]*preemption.Target, *workload.Info) {
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

	return []*preemption.Target{{WorkloadInfo: replaced}}, replaced
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
