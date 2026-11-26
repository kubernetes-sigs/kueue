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

package workloadslicing

import (
	"context"
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
)

// Manager holds the dependencies for managing workload slices.
type Manager struct {
	Client       client.Client
	Clock        clock.Clock
	Recorder     events.EventRecorder
	CustomLabels *metrics.CustomLabels
	RoleTracker  *roletracker.RoleTracker
}

// EnsureWorkloadSlices processes the Job object and returns the appropriate workload slice.
//
// Returns:
// - *Workload, true, nil: when a compatible workload exists or a new slice is needed.
// - nil, false, nil: when an incompatible workload exists and no update is performed.
// - error: on failure to fetch, update, or deactivate a workload slice.
func (r *Manager) EnsureWorkloadSlices(
	ctx context.Context,
	jobPodSets []kueue.PodSet,
	jobObject client.Object,
	jobObjectGVK schema.GroupVersionKind,
) (*kueue.Workload, bool, error) {
	jobPodSetsCounts := workload.ExtractPodSetCounts(jobPodSets)

	workloads, err := FindNotFinishedWorkloads(ctx, r.Client, jobObject, jobObjectGVK)
	if err != nil {
		return nil, true, fmt.Errorf("failed to find active workload slices: %w", err)
	}

	workloads, err = dropCancelledProbes(ctx, r.Client, workloads)
	if err != nil {
		return nil, true, err
	}

	// An evicted slice can still own running Pods. Return it to the job
	// reconciler until its reservation is released, unless an admitted
	// replacement has already taken ownership of those Pods.
	for i := range workloads {
		wl := &workloads[i]
		if !workloadevict.IsEvicted(wl) || !workload.HasQuotaReservation(wl) {
			continue
		}
		replaced := slices.ContainsFunc(workloads, func(candidate kueue.Workload) bool {
			key := ReplacementForKey(&candidate)
			return key != nil && *key == workload.Key(wl) && workload.IsAdmitted(&candidate) && !workloadevict.IsEvicted(&candidate)
		})
		if !replaced {
			return wl, true, nil
		}
	}

	switch len(workloads) {
	case 0:
		// No existing slices found — new slice should be created.
		return nil, true, nil

	case 1:
		// A single active workload was found.
		wl := &workloads[0]
		if !workload.IsActive(wl) {
			// A deactivated slice is not scaled up until it is reactivated.
			return wl, true, nil
		}
		wlPodSetsCounts := workload.ExtractPodSetCountsFromWorkload(wl)

		// Check if pod sets are structurally compatible (same number and names).
		if !jobPodSetsCounts.HasSamePodSetKeys(wlPodSetsCounts) {
			return nil, false, nil
		}

		// If counts match, return the existing workload slice or nil if the workload was partially admitted.
		if jobPodSetsCounts.EqualTo(wlPodSetsCounts) {
			if workload.IsAdmitted(wl) {
				for _, psa := range wl.Status.Admission.PodSetAssignments {
					if wlPodSetsCounts[psa.Name] > *psa.Count {
						if workload.HasPendingPreemptionRetainedCount(wl) {
							klog.FromContext(ctx).V(3).Info("Deferring scale-up probe while a partial-preemption request is pending", "workload", klog.KObj(wl))
							return wl, true, nil
						}
						// The workload was partially admitted, create the full scale up probe
						return nil, true, nil
					}
				}
			}
			return wl, true, nil
		}

		// Allow updating the existing slice if:
		// a. It hasn't been admitted (no quota reserved), or
		// b. It's a scale-down event.
		if !workload.HasQuotaReservation(wl) || ScaledDown(wlPodSetsCounts, jobPodSetsCounts) {
			workload.ApplyPodSetCounts(wl, jobPodSetsCounts)
			if err := r.Client.Update(ctx, wl); err != nil {
				return nil, true, fmt.Errorf("failed to update workload's pod sets counts: %w", err)
			}
			return wl, true, nil
		}

		// Scale-up on an admitted workload. A pending partial-preemption reclaim target means a
		// preemptor already asked this workload to scale down; keep the current slice until the
		// reclaim completes instead of creating a larger one.
		if workload.HasPendingPreemptionRetainedCount(wl) {
			klog.FromContext(ctx).V(3).Info("Deferring workload slice scale-up while a partial-preemption reclaim target is still pending", "workload", klog.KObj(wl))
			return wl, true, nil
		}

		// Scale-up on admitted workload → create a new slice.
		return nil, true, nil

	default:
		selectedWorkload, err := normalizeActiveSlices(ctx, r.Client, r.Clock, workloads)
		if err != nil {
			return nil, true, err
		}
		if selectedWorkload == nil {
			return nil, true, nil
		}
		if !workload.IsActive(selectedWorkload) {
			return selectedWorkload, true, nil
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
			if err := r.Client.Update(ctx, selectedWorkload); err != nil {
				return nil, true, fmt.Errorf("failed to update workload pod set counts: %w", err)
			}
			return selectedWorkload, true, nil
		}

		// Scale-up on an admitted workload. A pending partial-preemption reclaim target means a
		// preemptor already asked this workload to scale down; keep the current slice until the
		// reclaim completes instead of creating a larger one.
		if workload.HasPendingPreemptionRetainedCount(selectedWorkload) {
			klog.FromContext(ctx).V(3).Info("Deferring workload slice scale-up while a partial-preemption reclaim target is still pending", "workload", klog.KObj(selectedWorkload))
			return selectedWorkload, true, nil
		}

		// Scale-up on admitted selected workload — create a new slice.
		return nil, true, nil
	}
}

// dropCancelledProbes cancels pending scale-up probes and returns the other workloads.
// A pending probe that is already being deleted, by this function or by anyone else, has its finalizer removed.
// That finishes a deletion that deletePendingReplacement started with the finalizer kept.
// A pending probe of a deactivated slice is deleted, keeping its finalizer, see deletePendingReplacement.
// Left in the queue, it would be admitted into the quota that the deactivated slice releases, and replace that slice.
// The job would then hold quota against its deactivation, and reactivating the slice would have no effect.
// A new probe is created after the slice is reactivated and admitted again.
// Neither kind of probe is returned, so the job never takes a probe that is being deleted as its workload.
func dropCancelledProbes(ctx context.Context, clnt client.Client, workloads []kueue.Workload) ([]kueue.Workload, error) {
	deactivated := sets.New[workload.Reference]()
	for i := range workloads {
		if !workload.IsActive(&workloads[i]) {
			deactivated.Insert(workload.Key(&workloads[i]))
		}
	}
	log := klog.FromContext(ctx)
	remaining := make([]kueue.Workload, 0, len(workloads))
	for i := range workloads {
		wl := &workloads[i]
		switch {
		case IsPendingReplacementBeingDeleted(wl):
			log.V(4).Info("Removing the finalizer of a deleted scale-up probe", "workload", klog.KObj(wl))
			if err := workload.RemoveFinalizer(ctx, clnt, wl); client.IgnoreNotFound(err) != nil {
				return nil, fmt.Errorf("failed to remove the finalizer of a deleted scale-up probe: %w", err)
			}
		case IsPendingReplacement(wl) && deactivated.Has(*ReplacementForKey(wl)):
			log.V(4).Info("Deleting the scale-up probe of a deactivated workload slice", "workload", klog.KObj(wl), "replacementFor", *ReplacementForKey(wl))
			if err := deletePendingReplacement(ctx, clnt, wl); client.IgnoreNotFound(err) != nil {
				return nil, fmt.Errorf("failed to delete the scale-up probe of a deactivated workload slice: %w", err)
			}
		default:
			remaining = append(remaining, *wl)
		}
	}
	return remaining, nil
}
