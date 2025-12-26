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

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	cmputil "sigs.k8s.io/kueue/pkg/util/cmp"
	"sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
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

	// Sort workloads by creation timestamp, oldest first.
	// In the rare case that two workload slices have identical creationTimestamp values
	// (due to RFC3339 second-level precision), use WorkloadSliceReplacementFor
	// as a tiebreaker. This edge case is uncommon in production but can occur in
	// integration or e2e tests where the original and scaled-up workloads are created
	// in rapid succession.
	slices.SortFunc(list.Items, func(a, b kueue.Workload) int {
		return cmputil.LazyOr(
			func() int {
				return a.CreationTimestamp.Compare(b.CreationTimestamp.Time)
			},
			func() int {
				if b.Annotations[WorkloadSliceReplacementFor] == string(workload.Key(&a)) {
					return -1
				}
				return 1
			},
		)
	})

	// Filter out workloads with activated "Finished" condition.
	return slices.DeleteFunc(list.Items, func(w kueue.Workload) bool {
		return workload.IsFinished(&w)
	}), nil
}

// FindLatestActiveWorkload returns the newest non-finished workload from the list of workloads
// owned by the provided job. Returns nil if no active workload exists.
func FindLatestActiveWorkload(ctx context.Context, clnt client.Client, jobObject client.Object, jobObjectGVK schema.GroupVersionKind) (*kueue.Workload, error) {
	workloads, err := FindNotFinishedWorkloads(ctx, clnt, jobObject, jobObjectGVK)
	if err != nil {
		return nil, err
	}
	if len(workloads) == 0 {
		return nil, nil
	}
	// FindNotFinishedWorkloads returns oldest first, so the latest is at the end
	return &workloads[len(workloads)-1], nil
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
	tracker *roletracker.RoleTracker,
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

	case 2:
		// Two active workload slices detected.
		//
		// This transient state typically occurs when a new slice has been created,
		// and the old slice is pending deactivation. The system should resolve this
		// by deactivating the old slice, after which processing will continue under "case #1".
		oldWorkload := workloads[0]
		newWorkload := workloads[1]

		// Finish the old workload slice if:
		// a. It lost its quota reservation, or
		// b. It was explicitly evicted, or
		// c. The new workload has been admitted (has quota reservation) AND has a replacement
		//    annotation pointing to the old workload. This handles the case where the scheduler
		//    admitted the new slice but failed to finish the old slice.
		replacementKey := ReplacementForKey(&newWorkload)
		oldWorkloadKey := workload.Key(&oldWorkload)
		newWorkloadAdmittedAsReplacement := workload.HasQuotaReservation(&newWorkload) &&
			replacementKey != nil && *replacementKey == oldWorkloadKey
		shouldFinishOldSlice := !workload.HasQuotaReservation(&oldWorkload) ||
			workload.IsEvicted(&oldWorkload) ||
			newWorkloadAdmittedAsReplacement
		if shouldFinishOldSlice {
			// Finish the old workload slice as out of sync.
			reason := kueue.WorkloadFinishedReasonOutOfSync
			message := "The workload slice is out of sync with its parent job"
			if err := workload.Finish(ctx, clnt, &oldWorkload, reason, message, clk, tracker); err != nil {
				return nil, true, err
			}
		}

		// We consider the new workload slice only when evaluating against the incoming job (pod sets).
		newCounts := workload.ExtractPodSetCountsFromWorkload(&newWorkload)

		// Check if new workload and job's pod sets are compatible (have the same keys and keys count)
		if !jobPodSetsCounts.HasSamePodSetKeys(newCounts) {
			return nil, false, nil
		}

		// Return an error if the new workload has a reserved quota and we didn't just finish
		// the old workload due to the replacement annotation.
		//
		// A combination of a new workload with a reserved quota and an active old workload is considered an anomaly.
		// This condition may indicate a race condition or external interference. Specifically, a new workload slice
		// should never gain a quota reservation without the prior finalization of the old slice.
		// However, if the new workload was admitted as a replacement (and we just finished the old slice above),
		// this is the expected cleanup path and not an error.
		if workload.HasQuotaReservation(&newWorkload) && !newWorkloadAdmittedAsReplacement {
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

// StartWorkloadSlicePods identifies pods associated with the provided workload
// that are gated by the ElasticJobSchedulingGate scheduling gate and removes this gate,
// allowing them to be considered for scheduling.
//
// This function performs the following steps:
//  1. Lists all pods in the same namespace with the WorkloadSliceNameAnnotation matching
//     the workload slice name, falling back to OwnerReference UID for backwards compatibility.
//  2. For each pod, removes the ElasticJobSchedulingGate scheduling gate if present.
//
// Returns:
// - An error if any of the operations fail; otherwise, nil.
func StartWorkloadSlicePods(ctx context.Context, clnt client.Client, wl *kueue.Workload) error {
	log := ctrl.LoggerFrom(ctx)
	list := &corev1.PodList{}
	sliceName := SliceName(wl)

	// First try annotation-based lookup (supports JobSet and other workloads where pods
	// are not immediate children of the job).
	if err := clnt.List(ctx, list, client.InNamespace(wl.Namespace), client.MatchingFields{indexer.WorkloadSliceNameKey: sliceName}); err != nil {
		return fmt.Errorf("failed to list workload slice pods: %w", err)
	}

	// Fallback to owner reference lookup for backwards compatibility with pods created
	// before the annotation was introduced.
	// TODO(sohankunkerkar): remove in 0.18
	if len(list.Items) == 0 && len(wl.OwnerReferences) > 0 {
		ownerUID := string(wl.OwnerReferences[0].UID)
		log.V(4).Info("No pods found with annotation, falling back to owner reference lookup", "ownerUID", ownerUID)
		if err := clnt.List(ctx, list, client.InNamespace(wl.Namespace), client.MatchingFields{indexer.OwnerReferenceUID: ownerUID}); err != nil {
			return fmt.Errorf("failed to list job pods by owner reference: %w", err)
		}
	}

	for i := range list.Items {
		log.V(4).Info("Patching pod to remove elastic job scheduling gate", "podName", list.Items[i].Name, "workloadSliceName", sliceName)
		if err := clientutil.Patch(ctx, clnt, &list.Items[i], func() (bool, error) {
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
