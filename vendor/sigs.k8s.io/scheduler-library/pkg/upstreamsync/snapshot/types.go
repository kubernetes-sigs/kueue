// Copyright The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package snapshot

import (
	v1 "k8s.io/api/core/v1"
	fwk "k8s.io/kube-scheduler/framework"
	"sigs.k8s.io/scheduler-library/pkg/preemption"
)

// CommonSchedulingOptions contains options shared across different scheduling simulation methods.
type CommonSchedulingOptions struct {
	// DryRun determines if the scheduling attempt should be a dry run.
	// When true, the simulation only tests feasibility and returns the results
	// without updating the cluster snapshot state (any state updates are automatically restored).
	DryRun bool
}

// SchedulePodsOptions are the options of ClusterSnapshot.SchedulePods.
type SchedulePodsOptions struct {
	CommonSchedulingOptions
	// StopOnFailure determines whether the first pod that cannot be scheduled ends the whole
	// call. When false, the remaining pods are still attempted and the returned results hold one
	// entry per attempted pod. Unexpected execution errors always end the call regardless of
	// this option, as they indicate a programming error rather than a scheduling failure.
	StopOnFailure bool
}

// SchedulePodsByTemplateOptions are the options of ClusterSnapshot.SchedulePodsByTemplate.
// The method always stops at the first pod that does not fit, as the pods it schedules are
// identical and the next one would not fit either.
type SchedulePodsByTemplateOptions struct {
	CommonSchedulingOptions
}

// NewSchedulePodsOptions builds the SchedulePodsOptions out of its individual fields.
func NewSchedulePodsOptions(dryRun bool, stopOnFailure bool) SchedulePodsOptions {
	return SchedulePodsOptions{
		CommonSchedulingOptions: CommonSchedulingOptions{DryRun: dryRun},
		StopOnFailure:           stopOnFailure,
	}
}

// NewSchedulePodsByTemplateOptions builds the SchedulePodsByTemplateOptions out of its individual fields.
func NewSchedulePodsByTemplateOptions(dryRun bool) SchedulePodsByTemplateOptions {
	return SchedulePodsByTemplateOptions{
		CommonSchedulingOptions: CommonSchedulingOptions{DryRun: dryRun},
	}
}

// TransactionResult is what the function passed to ClusterSnapshot.Transaction returns to decide
// the fate of the mutations it made.
type TransactionResult int

const (
	// Commit keeps the mutations made within the transaction.
	Commit TransactionResult = iota
	// Revert undoes all the mutations made within the transaction.
	Revert
)

// SchedulingResult is the outcome of a single pod scheduling attempt.
type SchedulingResult struct {
	// Pod is the pod the attempt was made for, carrying the selected node when it was scheduled.
	// For the pods passed to SchedulePods it is the library's own copy; for the pods created from a
	// template it is the generated pod, which is the only way for the caller to learn what was
	// scheduled.
	// On a failed attempt Spec.NodeName is left as it came in, so it is empty unless the caller, or
	// the template, already set one.
	Pod *v1.Pod
	// Status is the outcome of the scheduling cycle: success, or the reason the pod was rejected.
	Status *fwk.Status
	// SelectedNodeName is the node the pod was scheduled on, empty if it was not scheduled.
	SelectedNodeName string
	// CycleState is the state of the scheduling cycle.
	CycleState fwk.CycleState
}

// Unpreemption is the handle returned by ClusterSnapshot.PreemptPods, allowing the preempted pods
// to be put back with ClusterSnapshot.Unpreempt. It is single-use and is tied to the state of the
// snapshot it was taken from: any permanent mutation of that snapshot invalidates it, since the
// pods it holds could no longer be restored to the state they were removed from.
type Unpreemption struct {
	// pods are the pods that were preempted, returned to the caller by Unpreempt.
	pods []*v1.Pod
	// revertFn puts the pods back into the snapshot, registering the undo of each addition.
	revertFn func() error
	// reverted marks the handle as consumed, so that it cannot be applied twice.
	reverted bool
	// validPreemptionVersion is the snapshot's preemption state version at the time of the
	// preemption. Unpreempt refuses to run once the snapshot has moved past it.
	validPreemptionVersion uint64
}

// ScheduleWorkloadOptions contains options for scheduling a workload.
type ScheduleWorkloadOptions struct {
	CommonSchedulingOptions
	WorkloadPreemptionOptions
}

// NewScheduleWorkloadOptions builds the ScheduleWorkloadOptions used by ScheduleWorkload.
func NewScheduleWorkloadOptions(dryRun bool) ScheduleWorkloadOptions {
	return ScheduleWorkloadOptions{
		CommonSchedulingOptions: CommonSchedulingOptions{DryRun: dryRun},
	}
}

// TODO(https://github.com/kubernetes-sigs/scheduler-library/issues/54): Remove these type aliases
// once ClusterSnapshot and its public API types move out of pkg/upstreamsync/snapshot.

// PreemptionVictim is an atomic unit of preemption, which can contain 1 or more pods.
type PreemptionVictim = preemption.PreemptionVictim

// PreemptionFilter provides a fast pre-check to skip expensive scheduling evaluations
// when the current set of preempted victims is known to be insufficient.
type PreemptionFilter = preemption.PreemptionFilter

// WorkloadPreemptionOptions configures victim selection and filtering for workload preemption.
type WorkloadPreemptionOptions = preemption.WorkloadPreemptionOptions

// WorkloadSchedulingResult is the result of the ScheduleWorkload operation.
type WorkloadSchedulingResult struct {
	// Status is status of the initial scheduling attempt (without preemption) or error.
	// If status is not successful or dry-run flag is set, the results won't be saved to snapshot.
	// If preemption was successful, status will be Unschedulable and [WorkloadSchedulingResult.PodResults]
	// and [WorkloadSchedulingResult.PreemptionVictims] will be set.
	Status *fwk.Status

	// PodResults stores assignments from pods to nodes.
	// If both scheduling and preemption were unsuccessful, this will be empty.
	// It is a subset of pods specified in the input.
	// In particular, it can contain fewer pods than provided when the hierarchy satisfies the gang quorum without needing to schedule every pod.
	PodResults []SchedulingResult

	// PreemptionVictims is the final set of victims determined by the scheduling algorithm to be required for the workload to fit.
	// If preemption was not needed or was unsuccessful, this will be empty.
	// It is a subset of all victims specified in [WorkloadPreemptionOptions].
	PreemptionVictims []PreemptionVictim
}
