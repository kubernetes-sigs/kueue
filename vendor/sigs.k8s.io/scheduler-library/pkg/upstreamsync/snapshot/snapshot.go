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
type ClusterSnapshot struct {
	profiles                  *upstreamsync.ProfileMap
	schedulerSnapshot         *cache.Snapshot
	undoLog                   undoLog
	transactionInProgress     bool
	stateVersionForPreemption uint64
}

type undoLog struct {
	undoOperations []func()
	stateVersion   uint64
}

func (ul *undoLog) registerOperation(undoOperation func()) {
	if undoOperation != nil {
		ul.undoOperations = append(ul.undoOperations, undoOperation)
		ul.stateVersion++
	}
}

func (ul *undoLog) restoreState(stateVersion uint64) {
	for ul.stateVersion != stateVersion {
		ul.undo()
	}
}

func (ul *undoLog) undo() {
	ops := ul.undoOperations
	ops, undoOp := ops[:len(ops)-1], ops[len(ops)-1]
	ul.undoOperations = ops
	undoOp()
	ul.stateVersion--
}

func (ul *undoLog) commit() {
	ul.undoOperations = nil
}

// New creates a new ClusterSnapshot stub wrapping the provided scheduler snapshot and frameworks.
func New(s *cache.Snapshot, profiles *upstreamsync.ProfileMap) *ClusterSnapshot {
	return &ClusterSnapshot{
		profiles:          profiles,
		schedulerSnapshot: s,
	}
}

// Transaction executes the provided function within a transaction.
// It rolls back operations if the function returns Revert or an error.
// Only a single active transaction is supported at any given time;
// attempting to start a nested transaction will return an error.
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
		s.undoLog.commit()
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
		if !s.transactionInProgress {
			s.undoLog.commit()
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
		if !s.transactionInProgress {
			s.undoLog.commit()
		}
	}()

	mutatingSnapshot := upstreamsync.NewMutatingSnapshot(s.schedulerSnapshot)

	unpreemptFns := []func(){}

	for _, pod := range pods {
		revertFn, err := removePodFromNode(ctx, mutatingSnapshot, pod)
		if err != nil {
			return nil, fmt.Errorf("failed to unreserve and forget pod %s: %w", klog.KObj(pod), err)
		}
		s.undoLog.registerOperation(revertFn)
		unpreemptFns = append(unpreemptFns, func() {
			revertFn()
			s.undoLog.registerOperation(func() {
				_, _ = removePodFromNode(ctx, mutatingSnapshot, pod)
			})
		})
	}

	unpreemptFn := func() {
		for _, revertFn := range slices.Backward(unpreemptFns) {
			revertFn()
		}
	}

	return &Unpreemption{
		pods:                   pods,
		revertFn:               unpreemptFn,
		validPreemptionVersion: s.stateVersionForPreemption,
	}, nil
}

// Unpreempt undos the preemption done by the PreemptPods.
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

	u.revertFn()
	u.reverted = true

	if !s.transactionInProgress {
		s.undoLog.commit()
	}

	return u.pods, nil
}
