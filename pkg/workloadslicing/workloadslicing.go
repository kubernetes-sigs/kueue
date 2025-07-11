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
	"fmt"
	"slices"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
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
	annotations := object.GetAnnotations()
	return len(annotations) > 0 && annotations[EnabledAnnotationKey] == EnabledAnnotationValue
}

const (
	// WorkloadPreemptibleSliceNameKey is the annotation key used to capture an "old" workload slice
	// that will be preempted by the "new" workload slice, i.e., this annotates a "new" workload slice.
	WorkloadPreemptibleSliceNameKey = "kueue.x-k8s.io/workload-preemptible-slice"
)

// PreemptibleSliceKey returns a preemptible workload slice key if this workload
// was annotated with such, otherwise, returns an empty string.
func PreemptibleSliceKey(wl *kueue.Workload) string {
	annotations := wl.GetAnnotations()
	if len(annotations) == 0 {
		return ""
	}
	return annotations[WorkloadPreemptibleSliceNameKey]
}

// Workload predicate definition to match workloads.
type predicate func(wlSlice kueue.Workload) bool

// not predicate inverts the provided predicate result, used in "exclusion" filters.
func not(predicate predicate) predicate {
	return func(wlSlice kueue.Workload) bool {
		return !predicate(wlSlice)
	}
}

// activeSlices predicate matches active non-deleted workload slices.
func activeSlices() predicate {
	return func(wlSlice kueue.Workload) bool {
		return workload.IsActive(&wlSlice) && wlSlice.DeletionTimestamp.IsZero()
	}
}

// FindSlices returns a sorted list of active workloads "owned by" the provided job object/gvk combination and
// matching the provided predicates.
func findSlices(ctx context.Context, clnt client.Client, jobObject client.Object, jobObjectGVK schema.GroupVersionKind, predicate predicate) ([]kueue.Workload, error) {
	list := &kueue.WorkloadList{}
	if err := clnt.List(ctx, list, client.InNamespace(jobObject.GetNamespace()), client.MatchingFields{fmt.Sprintf(".metadata.ownerReferences[%s.%s]", jobObjectGVK.Group, jobObjectGVK.Kind): jobObject.GetName()}); err != nil {
		return nil, err
	}

	// Sort workloads by creation timestamp - oldest first.
	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[i].CreationTimestamp.Before(&list.Items[j].CreationTimestamp)
	})

	// Filter out workloads not matching the provided predicates.
	// This is an exclusion match, i.e. it removes all entries that do not match the provided predicate.
	return slices.DeleteFunc(list.Items, not(predicate)), nil
}

// FindActiveSlices returns a list of active workloads for a given job object/gvk combination.
func FindActiveSlices(ctx context.Context, clnt client.Client, jobObject client.Object, jobObjectGVK schema.GroupVersionKind) ([]kueue.Workload, error) {
	return findSlices(ctx, clnt, jobObject, jobObjectGVK, activeSlices())
}

// finishedPreemptedCondition helper returns workload "Finished" condition for preempted workload slices.
func finishedPreemptedCondition() metav1.Condition {
	return metav1.Condition{
		Type:    kueue.WorkloadFinished,
		Status:  metav1.ConditionTrue,
		Reason:  kueue.WorkloadSlicePreemptionReason,
		Message: kueue.WorkloadEvictedByWorkloadSliceAggregation,
	}
}

// finishedOutOfSyncCondition helper returns workload "Finished" condition for updated, but
// not yet admitted workload slices.
func finishedOutOfSyncCondition() metav1.Condition {
	return metav1.Condition{
		Type:    kueue.WorkloadFinished,
		Status:  metav1.ConditionTrue,
		Reason:  kueue.WorkloadFinishedReasonOutOfSync,
		Message: "The workload slice is out of sync with its parent job",
	}
}

// FinishPreemptedSlice marks a preempted workload slice as finished by setting the appropriate status condition.
//
// This function updates the workload's status to reflect that it has been preempted and is now considered finished.
// It applies the 'Finished' condition to the workload's status.conditions field.
//
// Returns:
// - An error if the status update fails; otherwise, nil.
func FinishPreemptedSlice(ctx context.Context, clnt client.Client, workloadSlice *kueue.Workload) error {
	if err := clientutil.PatchStatus(ctx, clnt, workloadSlice, func() (bool, error) {
		return apimeta.SetStatusCondition(&workloadSlice.Status.Conditions, finishedPreemptedCondition()), nil
	}); err != nil {
		return fmt.Errorf("failed to mark workload slice as finished: %w", err)
	}
	return nil
}

// deactivateOutOfSyncSlice deactivates a workload slice and marks it as finished
// when its current state is out-of-sync with the associated job specification.
//
// This function performs two main actions:
//  1. Deactivates the workload slice by setting its 'Active' field to false.
//  2. Updates the workload's status conditions to reflect that it has been finished
//     due to being out-of-sync.
//
// Returns:
// - An error if any of the update operations fail; otherwise, nil.
func deactivateOutOfSyncSlice(ctx context.Context, clnt client.Client, workloadSlice *kueue.Workload) error {
	if err := clientutil.Patch(ctx, clnt, workloadSlice, true, func() (bool, error) {
		// Patch spec to deactivate (if needed).
		if workload.IsActive(workloadSlice) {
			workloadSlice.Spec.Active = ptr.To(false)
			return true, nil
		}
		return false, nil
	}); err != nil {
		return fmt.Errorf("failed to deactivate out-of-sync workload slice: %w", err)
	}

	// Patch status condition with "Finished" (if needed also).
	if err := clientutil.PatchStatus(ctx, clnt, workloadSlice, func() (bool, error) {
		return apimeta.SetStatusCondition(&workloadSlice.Status.Conditions, finishedOutOfSyncCondition()), nil
	}); err != nil {
		return fmt.Errorf("failed to update out-of-sync workload slice status: %w", err)
	}
	return nil
}

// EnsureWorkloadSlices processes the Job object and returns the appropriate workload slice.
//
// Returns:
// - *Workload, true, nil: when a compatible workload exists or a new slice is needed.
// - nil, false, nil: when an incompatible workload exists and no update is performed.
// - error: on failure to fetch, update, or deactivate a workload slice.
//
// Note: This function is intentionally designed to operate without requiring access to the old (preemptible) slice.
// Preemptible slices are expected to be handled by the scheduler's preemption logic, with some exceptions.
// EnsureWorkloadSlices processes the given Job's PodSets and returns a matching workload slice.
// It handles workload compatibility and updates or creates new slices as needed.
func EnsureWorkloadSlices(ctx context.Context, clnt client.Client, jobPodSets []kueue.PodSet, jobObject client.Object, jobObjectGVK schema.GroupVersionKind) (*kueue.Workload, bool, error) {
	jobPodSetsCounts := workload.ExtractPodSetCounts(jobPodSets)

	workloads, err := findSlices(ctx, clnt, jobObject, jobObjectGVK, activeSlices())
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
		if !workload.HasQuotaReservation(wl) || jobPodSetsCounts.HasFewerReplicasThan(wlPodSetsCounts) {
			workload.ApplyPodSetCounts(wl, jobPodSetsCounts)
			if err := clnt.Update(ctx, wl); err != nil {
				return nil, true, fmt.Errorf("failed to update workload pod set counts: %w", err)
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

		// We consider the new workload slice only when evaluating against the incoming job (pod sets).
		newWorkload := workloads[1]
		newCounts := workload.ExtractPodSetCountsFromWorkload(&newWorkload)

		// Check if new workload and job's pod sets are compatible (have the same keys and keys count)
		if !jobPodSetsCounts.HasSamePodSetKeys(newCounts) {
			return nil, false, nil
		}

		// Return the new workload if it's already admitted or if the pod set counts match.
		//
		// - If the workload is already admitted, we defer processing changes to allow the old slice
		//   to be deactivated, after which processing will continue under "case #1".
		// - If the pod set counts match, we treat this as a no-op, effectively achieving the same outcome.
		if workload.HasQuotaReservation(&newWorkload) || jobPodSetsCounts.EqualTo(newCounts) {
			return &newWorkload, true, nil
		}

		// Deactivating not admitted new slice as "out-of-sync".
		err := deactivateOutOfSyncSlice(ctx, clnt, &newWorkload)
		return &newWorkload, true, err

	default:
		// More than two matching slices is unexpected and considered an error.
		return nil, true, fmt.Errorf("unexpected number of matching workload slices: %d", len(workloads))
	}
}

// startWorkloadSlicePods identifies pods associated with the provided parent object
// that are gated by the "kueue.x-k8s.io/topology" scheduling gate and removes this gate,
// allowing them to be considered for scheduling.
//
// This function performs the following steps:
// 1. Lists all pods in the same namespace with an OwnerReference UID matching the parent object.
// 2. For each pod, removes the "kueue.x-k8s.io/topology" scheduling gate if present.
//
// Returns:
// - An error if any of the operations fail; otherwise, nil.
func startWorkloadSlicePods(ctx context.Context, clnt client.Client, object client.Object) error {
	list := &corev1.PodList{}
	if err := clnt.List(ctx, list, client.InNamespace(object.GetNamespace()), client.MatchingFields{indexer.OwnerReferenceUID: string(object.GetUID())}); err != nil {
		return fmt.Errorf("failed to list job pods: %w", err)
	}
	for i := range list.Items {
		if err := clientutil.Patch(ctx, clnt, &list.Items[i], true, func() (bool, error) {
			return pod.Ungate(&list.Items[i], kueue.WorkloadSliceSchedulingGate), nil
		}); err != nil {
			return fmt.Errorf("failed to patch pod: %w", err)
		}
	}
	return nil
}

// ReconcileWorkloadSlices clears pod's scheduling gates and finishes preempted workload slices
// in the Job reconciliation context.
func ReconcileWorkloadSlices(ctx context.Context, clnt client.Client, object client.Object) (reconcile.Result, error) {
	if err := startWorkloadSlicePods(ctx, clnt, object); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to start workload slice pods: %w", err)
	}
	return ctrl.Result{}, nil
}
