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
	// Status reports the outcome of scheduling without waiting for preemption, or an error.
	// If status is not [fwk.Success] or dry-run flag is set, the results won't be saved to snapshot.
	//
	// Possible status codes:
	//   - [fwk.Success]: the workload fits on Pass 1 without waiting for any preemption (with nodes occupied by
	//     [WorkloadPreemptionOptions.CommittedVictims] filtered out and CommittedVictims temporarily excluded from
	//     [PreemptionFilter]), or the input pod slice is empty.
	//   - [fwk.Unschedulable]: the workload does not fit without waiting for preemption (either on Pass 1's
	//     scheduling evaluation, such as pod filtering or PlacementFeasible/PlacementGenerate checks,
	//     or according to [PreemptionFilter.MayFit]).
	//     If the workload fits either by waiting for [WorkloadPreemptionOptions.CommittedVictims] alone
	//     (Pass 2, with [WorkloadSchedulingResult.PreemptionVictims] empty) or by preempting additional victims
	//     from [WorkloadPreemptionOptions.PotentialVictims] (Pass 3, with [WorkloadSchedulingResult.PreemptionVictims]
	//     non-empty), Status has code [fwk.Unschedulable], [WorkloadSchedulingResult.WaitingForPreemption] is true,
	//     and [WorkloadSchedulingResult.PodResults] contains the proposed pod assignments.
	//     Otherwise (neither CommittedVictims nor PotentialVictims sufficed, or neither was provided),
	//     [WorkloadSchedulingResult.WaitingForPreemption] is false and [WorkloadSchedulingResult.PodResults] contains
	//     per-pod failure statuses from the last without-preemption attempt that ran: Pass 2 across all nodes when
	//     [WorkloadPreemptionOptions.CommittedVictims] is non-empty, or Pass 1 otherwise (or is empty if that attempt
	//     was skipped by [PreemptionFilter.MayFit]).
	//   - [fwk.UnschedulableAndUnresolvable]: a PlacementGenerate plugin rejected the root group
	//     as unresolvable during Pass 1 or Pass 2 (remaining passes are skipped).
	//   - [fwk.Error]: hierarchy validation, framework resolution, plugin execution, or preemption failed with an error.
	Status *fwk.Status

	// PodResults stores per-pod scheduling outcomes.
	// When the workload fits immediately (Status is [fwk.Success]) or via preemption
	// ([WorkloadSchedulingResult.WaitingForPreemption] is true), each entry carries the assigned SelectedNodeName
	// and a nil (Success) Status. In these cases, it can be a subset of pods specified in the input when the
	// hierarchy satisfies the gang quorum without needing to schedule every pod.
	// When the workload cannot be scheduled even with preemption, it contains the pods from the last
	// without-preemption attempt that ran (Pass 2 across all nodes when [WorkloadPreemptionOptions.CommittedVictims]
	// is non-empty and Pass 1 returned [fwk.Unschedulable], or Pass 1 otherwise), with empty SelectedNodeName and
	// Status explaining why each pod or its pod group could not be scheduled (or is empty when that attempt was
	// skipped by [PreemptionFilter.MayFit] or when Status has code [fwk.Error]).
	PodResults []SchedulingResult

	// WaitingForPreemption specifies whether the workload fits only after ongoing or newly required preemptions finish.
	// When true, Status has code [fwk.Unschedulable] and either [WorkloadSchedulingResult.PreemptionVictims] is empty
	// (Pass 2: the workload fits by waiting on [WorkloadPreemptionOptions.CommittedVictims] alone) or non-empty
	// (Pass 3: the workload additionally requires preempting those new victims from [WorkloadPreemptionOptions.PotentialVictims]).
	WaitingForPreemption bool

	// PreemptionVictims is the final set of new victims determined by the scheduling algorithm to be required for the workload to fit.
	// It is a subset of victims specified in [WorkloadPreemptionOptions.PotentialVictims] (excluding [WorkloadPreemptionOptions.CommittedVictims]),
	// and is empty when the workload fits without preemption (Pass 1) or by waiting only on CommittedVictims (Pass 2).
	PreemptionVictims []PreemptionVictim
}
