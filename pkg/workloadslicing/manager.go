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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
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

		// Scale-up on admitted selected workload — create a new slice.
		return nil, true, nil
	}
}

// FinishReplacedWorkloadSlices finishes predecessors referenced by status.replaces
// in workloads, including finished and evicted successors, and records each
// successful replacement event and metric. The caller supplies workloads
// belonging to the job or slice chain being reconciled.
func (r *Manager) FinishReplacedWorkloadSlices(ctx context.Context, workloads []kueue.Workload) error {
	byName := make(map[types.NamespacedName]*kueue.Workload, len(workloads))
	for i := range workloads {
		wl := &workloads[i]
		byName[client.ObjectKeyFromObject(wl)] = wl
	}
	for i := range workloads {
		newSlice := &workloads[i]
		replaces := newSlice.Status.Replaces
		if replaces == nil || replaces.Name == newSlice.Name {
			continue
		}
		oldSlice := byName[types.NamespacedName{Namespace: newSlice.Namespace, Name: replaces.Name}]
		if oldSlice == nil || workloadfinish.IsFinished(oldSlice) {
			continue
		}
		message := fmt.Sprintf("Replaced to accommodate a workload (UID: %s, JobUID: %s) due to workload slice aggregation", newSlice.UID, newSlice.Labels[controllerconsts.JobUIDLabel])
		if err := workloadfinish.Finish(ctx, r.Client, oldSlice, kueue.WorkloadSliceReplaced, message, r.Clock); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("finishing replaced workload slice: %w", err)
		}
		r.Recorder.Eventf(oldSlice, nil, corev1.EventTypeNormal, kueue.WorkloadSliceReplaced, "Replaced", message)
		if oldSlice.Status.Admission != nil {
			cq := oldSlice.Status.Admission.ClusterQueue
			metrics.ReportReplacedWorkloadSlices(cq, r.CustomLabels.CQGet(cq), r.RoleTracker)
		}
	}
	return nil
}
