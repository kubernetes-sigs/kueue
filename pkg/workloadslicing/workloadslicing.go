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

// Deactivate deactivates a given workload slice by setting its "Active" spec field to false.
// It also applies the specified status condition patches to the workload slice.
//
// This function performs the following steps:
//  1. It patches the spec of the workload slice to mark it as inactive (i.e., setting `Spec.Active` to false)
//     if the workload slice is currently active.
//  2. It applies any status conditions provided in the `conditions` parameter to the workload slice's status.
//
// Parameters:
//   - ctx: The context for the operation, used for cancellation and logging.
//   - clnt: The Kubernetes client used to interact with the API server.
//   - workloadSlice: The workload slice to deactivate.
//   - conditions: A variadic list of conditions to apply to the status of the workload slice.
//
// Returns:
//   - error: An error, if any occurred during the process. If no error occurred, nil is returned.
//
// This function is useful for deactivating a workload slice and updating its status conditions in one operation.
func Deactivate(ctx context.Context, clnt client.Client, workloadSlice *kueue.Workload, conditions ...metav1.Condition) error {
	if err := clientutil.Patch(ctx, clnt, workloadSlice, true, func() (bool, error) {
		// Patch spec to deactivate (if needed).
		if workload.IsActive(workloadSlice) {
			workloadSlice.Spec.Active = ptr.To(false)
			return true, nil
		}
		return false, nil
	}); err != nil {
		return fmt.Errorf("failed to patch workload slice: %w", err)
	}
	// Apply status condition patches.
	if err := clientutil.PatchStatus(ctx, clnt, workloadSlice, func() (bool, error) {
		updated := false
		for i := range conditions {
			updated = apimeta.SetStatusCondition(&workloadSlice.Status.Conditions, conditions[i]) || updated
		}
		return updated, nil
	}); err != nil {
		return fmt.Errorf("failed to patch workload slice status: %w", err)
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

		// There is also an edge-case when the old slice was preempted/evicted by the scheduler
		// to make the room for other (than new slice) workload, which would result in two
		// "pending" workloads. In such case - it is safe to deactivate the old slice.
		oldWorkload := workloads[0]
		if !workload.HasQuotaReservation(&oldWorkload) {
			if err := Deactivate(ctx, clnt, &oldWorkload, finishedOutOfSyncCondition()); err != nil {
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
