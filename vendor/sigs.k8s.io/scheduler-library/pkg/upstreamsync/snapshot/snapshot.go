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
	"context"
	"errors"
	"fmt"
	"iter"
	"math"
	"slices"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/scheduler-library/pkg/upstreamsync"

	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/backend/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

// ClusterSnapshot wraps a scheduler snapshot and its associated frameworks.
// All ClusterSnapshot instances created from the same ClusterState share the
// same underlying cache.Snapshot. Creating a new snapshot via ClusterState.Snapshot
// updates that shared snapshot in-place, which invalidates any previously returned
// ClusterSnapshot instance — callers must not use a prior snapshot after requesting a new one.
// A ClusterSnapshot is not safe for concurrent use.
type ClusterSnapshot struct {
	// profiles holds the scheduling framework per scheduler name. All of them share
	// schedulerSnapshot as their SnapshotSharedLister, so the plugins always see the mutations
	// performed here.
	profiles *upstreamsync.ProfileMap
	// schedulerSnapshot is the upstream snapshot holding the actual node and pod data.
	schedulerSnapshot *cache.Snapshot
	// undoLog records how to undo every mutation applied to schedulerSnapshot, so that a dry run
	// or a reverted transaction can restore the state it started from.
	undoLog undoLog
	// transactionInProgress guards against nested transactions and tells the mutating methods to
	// leave the undo log alone, as the enclosing transaction owns it.
	transactionInProgress bool
	// stateVersionForPreemption is bumped whenever a mutation makes the outstanding Unpreemption
	// handles unusable, i.e. whenever the state they would be restoring into is no longer the one
	// they were taken from. Unpreempt compares it with the value recorded in the handle.
	stateVersionForPreemption uint64
}

// undoLog is a stack of the operations reverting the mutations applied to the snapshot, most
// recent last. Every mutation pushes its revert function, and rolling back means popping and
// running them until the recorded state version is reached again.
type undoLog struct {
	// undoOperations are the revert functions, in the order their mutations were applied.
	undoOperations []func()
	// stateVersion is incremented by every registered operation and decremented by every undone
	// one. A caller records it before mutating and passes it to restoreState afterwards to undo
	// exactly its own mutations.
	stateVersion uint64
}

// registerOperation pushes the revert function of a mutation that has just been applied.
// A nil undoOperation is ignored, so that callers can pass the result of an operation that
// did not change anything.
func (ul *undoLog) registerOperation(undoOperation func()) {
	if undoOperation != nil {
		ul.undoOperations = append(ul.undoOperations, undoOperation)
		ul.stateVersion++
	}
}

// restoreState undoes the operations registered after the given state version was observed,
// in the reverse order of their registration.
func (ul *undoLog) restoreState(stateVersion uint64) {
	for ul.stateVersion != stateVersion {
		ul.undo()
	}
}

// undo pops the most recently registered operation and runs it.
func (ul *undoLog) undo() {
	ops := ul.undoOperations
	ops, undoOp := ops[:len(ops)-1], ops[len(ops)-1]
	ul.undoOperations = ops
	undoOp()
	ul.stateVersion--
}

// New creates a new ClusterSnapshot stub wrapping the provided scheduler snapshot and frameworks.
//
// Consumers should obtain a ClusterSnapshot from simulator.SchedulingSimulator instead, either via
// NewClusterSnapshot or via NewClusterState followed by state.ClusterState.Snapshot: those build
// the full plugin chain out of the KubeSchedulerConfiguration and initialize the scheduler metrics,
// which this constructor expects to have been done already.
func New(s *cache.Snapshot, profiles *upstreamsync.ProfileMap) *ClusterSnapshot {
	return &ClusterSnapshot{
		profiles:          profiles,
		schedulerSnapshot: s,
	}
}

// ResetMutations restores the snapshot to its state prior to any mutations,
// executing all accumulated undo operations in reverse order.
func (s *ClusterSnapshot) ResetMutations() error {
	if s.transactionInProgress {
		return fmt.Errorf("transaction is in progress, cannot reset mutations")
	}
	if s.undoLog.stateVersion > 0 {
		s.undoLog.restoreState(0)
		s.stateVersionForPreemption++
	}
	return nil
}

// Transaction executes the provided function within a transaction.
// It rolls back operations if the function returns Revert or an error.
// Only a single active transaction is supported at any given time;
// attempting to start a nested transaction will return an error.
// Committed operations or operations made outside of transaction scope
// can only be reverted by [ClusterSnapshot.ResetMutations].
func (s *ClusterSnapshot) Transaction(ctx context.Context, transactionFn func() (TransactionResult, error)) error {
	if s.transactionInProgress {
		return fmt.Errorf("a transaction is already in progress")
	}

	s.transactionInProgress = true
	defer func() { s.transactionInProgress = false }()

	initialStateVersion := s.undoLog.stateVersion
	initialStateVersionForPreemption := s.stateVersionForPreemption
	s.stateVersionForPreemption++

	result, err := transactionFn()

	if err != nil || result == Revert {
		s.undoLog.restoreState(initialStateVersion)
		s.stateVersionForPreemption = initialStateVersionForPreemption
	} else {
		// invalidate preemptions done within the transaction
		s.stateVersionForPreemption++
	}

	if err != nil {
		return fmt.Errorf("transaction failed: %w", err)
	}
	return nil
}

// CanSchedulePod checks feasibility of a single pod on the specified nodes by running
// PreFilter and Filter plugins. Returns the names of nodes on which the pod can be scheduled,
// the framework.Diagnosis for rejected nodes, and any error.
func (s *ClusterSnapshot) CanSchedulePod(ctx context.Context, pod *v1.Pod, placement *fwk.Placement) ([]string, *framework.Diagnosis, error) {
	if placement == nil || len(placement.Nodes) == 0 {
		return nil, nil, nil
	}
	schedFramework, err := s.profiles.FrameworkForPod(pod)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get framework: %w", err)
	}
	state := framework.NewCycleState()
	podInfo, err := framework.NewPodInfo(pod)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create pod info: %w", err)
	}
	pendingPod := &upstreamsync.PendingPod{
		PodInfo:    podInfo,
		CycleState: state,
	}

	feasibleNodes := make([]string, 0)
	var diagnosis framework.Diagnosis
	sched := upstreamsync.NewScheduler(s.schedulerSnapshot, 0, 0, math.MaxInt32)
	err = s.schedulerSnapshot.AssumePlacement(placement)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to assume placement: %w", err)
	}
	defer s.schedulerSnapshot.ForgetPlacement()
	nodes, diag, _, err := sched.FindAllNodesThatFitPod(ctx, schedFramework, pendingPod)
	diagnosis = diag
	for _, node := range nodes {
		feasibleNodes = append(feasibleNodes, node.Node().Name)
	}
	if err != nil {
		return nil, &diagnosis, fmt.Errorf("failed to find nodes that fit pod: %w", err)
	}

	return feasibleNodes, &diagnosis, nil
}

func schedulingResult(algRes *upstreamsync.AlgorithmResult) SchedulingResult {
	return SchedulingResult{
		Pod:              algRes.Pod,
		Status:           algRes.Status,
		SelectedNodeName: algRes.ScheduleResult.SuggestedHost,
	}
}

// SchedulePods schedules the given pods onto the specified placement using PreFilter and Filter plugins.
// StopOnFailure controls whether the first unschedulable pod stops the loop. Note that
// All unexpected execution errors always propagate immediately regardless of StopOnFailure, as they
// indicate a programming error rather than a scheduling failure.
// Every pod that is scheduled gets its Spec.NodeName set to the selected node, which is also
// reported by the corresponding SchedulingResult.
func (s *ClusterSnapshot) SchedulePods(ctx context.Context, pods []*v1.Pod, placement *fwk.Placement, opts SchedulePodsOptions) ([]SchedulingResult, error) {
	return s.schedulePods(ctx, slices.Values(pods), placement, opts)
}

// SchedulePodsByTemplate attempts to schedule as many pods matching the template as possible.
// It assumes nodes in the placement are feasible and moves to the next node only if the pod is unschedulable on the current node.
func (s *ClusterSnapshot) SchedulePodsByTemplate(ctx context.Context, template *v1.PodTemplateSpec, placement *fwk.Placement, maxPods int, opts SchedulePodsByTemplateOptions) ([]SchedulingResult, error) {
	if maxPods <= 0 {
		return nil, nil
	}

	podIterator := func(yield func(*v1.Pod) bool) {
		for i := 0; i < maxPods; i++ {
			pod := createPodFromTemplate(template, i)
			if !yield(pod) {
				return
			}
		}
	}

	scheduleOptions := SchedulePodsOptions{
		CommonSchedulingOptions: opts.CommonSchedulingOptions,
		StopOnFailure:           true,
	}

	return s.schedulePods(ctx, podIterator, placement, scheduleOptions)
}

func (s *ClusterSnapshot) schedulePods(ctx context.Context, pods iter.Seq[*v1.Pod], placement *fwk.Placement, opts SchedulePodsOptions) (_ []SchedulingResult, err error) {
	if placement == nil || len(placement.Nodes) == 0 {
		return nil, nil
	}

	initialStateVersion := s.undoLog.stateVersion

	defer func() {
		if err != nil || opts.DryRun {
			s.undoLog.restoreState(initialStateVersion)
		}
		if initialStateVersion != s.undoLog.stateVersion {
			s.stateVersionForPreemption++
		}
	}()

	result := make([]SchedulingResult, 0)

	currentCycle := int64(0)

	err = s.schedulerSnapshot.AssumePlacement(placement)
	if err != nil {
		return nil, fmt.Errorf("error assuming placement: %w", err)
	}
	defer s.schedulerSnapshot.ForgetPlacement()
	for pod := range pods {
		sched := upstreamsync.NewScheduler(s.schedulerSnapshot, currentCycle, 0, 1)

		res, revertFn, err := scheduleOnePod(ctx, s.profiles, sched, pod)

		if err != nil {
			return result, err
		}

		if res.Status.IsSuccess() {
			// Reflect the simulated placement on the caller's pod, so that a pod scheduled in this
			// loop is seen as assigned by whoever inspects it, including the SchedulingResult below.
			pod.Spec.NodeName = res.ScheduleResult.SuggestedHost
		}

		if revertFn != nil {
			s.undoLog.registerOperation(revertFn)
		}
		result = append(result, schedulingResult(res))

		if !res.Status.IsSuccess() {
			if opts.StopOnFailure {
				return result, nil
			}
		}

		currentCycle++
	}

	return result, nil
}

// MakePlacement creates a framework.Placement containing NodeInfo structures for each candidate node name.
func (s *ClusterSnapshot) MakePlacement(candidateNodeNames []string) (*fwk.Placement, error) {
	nodes := make([]fwk.NodeInfo, 0, len(candidateNodeNames))
	for _, name := range candidateNodeNames {
		ni, err := s.schedulerSnapshot.NodeInfos().Get(name)
		if err != nil {
			return nil, fmt.Errorf("error getting %s from snapshot: %w", name, err)
		}
		nodes = append(nodes, ni)
	}
	return &fwk.Placement{Nodes: nodes}, nil
}

// PreemptPods removes pods from the snapshot.
// It supports transaction rollbacks if called inside a transaction.
// If any pod fails to be preempted, all previously preempted pods in this call
// are automatically restored and an error is returned.
func (s *ClusterSnapshot) PreemptPods(ctx context.Context, pods []*v1.Pod) (_ *Unpreemption, err error) {
	// Validate all pods before making any changes.
	for _, pod := range pods {
		if pod.Spec.NodeName == "" {
			return nil, fmt.Errorf("pod %s has no node name", klog.KObj(pod))
		}
	}

	initialStateVersion := s.undoLog.stateVersion

	defer func() {
		if err != nil {
			s.undoLog.restoreState(initialStateVersion)
		}
	}()

	mutatingSnapshot := upstreamsync.NewMutatingSnapshot(s.schedulerSnapshot)

	unpreemptFns := []func() error{}

	for _, pod := range pods {
		revertFn, err := removePodFromNode(ctx, mutatingSnapshot, pod)
		if err != nil {
			return nil, fmt.Errorf("failed to unreserve and forget pod %s: %w", klog.KObj(pod), err)
		}
		s.undoLog.registerOperation(revertFn)
		// Putting the pod back is a snapshot mutation like any other, so it goes through
		// addPodToNode and registers its own revert function, undoing it re-preempts the pod.
		unpreemptFns = append(unpreemptFns, func() error {
			repreemptFn, err := addPodToNode(ctx, mutatingSnapshot, pod, pod.Spec.NodeName)
			if err != nil {
				return fmt.Errorf("failed to unpreempt pod %s: %w", klog.KObj(pod), err)
			}
			s.undoLog.registerOperation(repreemptFn)
			return nil
		})
	}

	unpreemptFn := func() error {
		var errs []error
		for _, unpreempt := range slices.Backward(unpreemptFns) {
			// Keep going on failure, so that as many pods as possible are put back.
			if err := unpreempt(); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}

	return &Unpreemption{
		pods:                   pods,
		revertFn:               unpreemptFn,
		validPreemptionVersion: s.stateVersionForPreemption,
	}, nil
}

// Unpreempt undos the preemption done by the PreemptPods.
// The handle is consumed even if putting some of the pods back fails, in which case the pods that
// were restored are still registered in the undo log and are rolled back with the transaction.
func (s *ClusterSnapshot) Unpreempt(u *Unpreemption) ([]*v1.Pod, error) {
	if u == nil {
		return nil, fmt.Errorf("preemption handle is nil")
	}
	if s.stateVersionForPreemption != u.validPreemptionVersion {
		return nil, fmt.Errorf("preemption handle is invalid: snapshot has been permanently mutated since preemption")
	}
	if u.reverted {
		return nil, fmt.Errorf("preemption handle is invalid: already unpreempted")
	}

	defer func() {
		u.reverted = true
	}()

	err := u.revertFn()
	if err != nil {
		return nil, err
	}

	return u.pods, nil
}
