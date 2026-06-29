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

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	upstreamsync "sigs.k8s.io/scheduler-library/pkg/upstream_sync"

	"k8s.io/kubernetes/pkg/scheduler/backend/cache"
	fwk "k8s.io/kubernetes/pkg/scheduler/framework"
)

type txMutation struct {
	revertFn func()
	active   bool
}

// ClusterSnapshot wraps a scheduler snapshot and its associated frameworks.
// All ClusterSnapshot instances created from the same ClusterState share the
// same underlying cache.Snapshot. Creating a new snapshot via ClusterState.Snapshot
// updates that shared snapshot in-place, which invalidates any previously returned
// ClusterSnapshot instance — callers must not use a prior snapshot after requesting a new one.
type ClusterSnapshot struct {
	sched             *upstreamsync.Scheduler
	schedulerSnapshot *cache.Snapshot
	txCompensations   []*txMutation
	stateVersion      uint64
}

// New creates a new ClusterSnapshot stub wrapping the provided scheduler snapshot and frameworks.
func New(s *cache.Snapshot, sched *upstreamsync.Scheduler) *ClusterSnapshot {
	return &ClusterSnapshot{
		sched:             sched,
		schedulerSnapshot: s,
	}
}

// Transaction executes the provided function within a transaction.
// It rolls back operations if the function returns Revert or an error.
// Only a single active transaction is supported at any given time;
// attempting to start a nested transaction will return an error.
func (s *ClusterSnapshot) Transaction(ctx context.Context, logger klog.Logger, transactionFn func() (TransactionResult, error)) error {
	if s.txCompensations != nil {
		return fmt.Errorf("a transaction is already in progress")
	}
	s.txCompensations = []*txMutation{}

	result, err := transactionFn()

	if err != nil || result == Revert {
		for i := len(s.txCompensations) - 1; i >= 0; i-- {
			m := s.txCompensations[i]
			if m.active {
				m.revertFn()
				m.active = false
			}
		}
	} else if len(s.txCompensations) > 0 {
		s.stateVersion++
	}

	s.txCompensations = nil

	if err != nil {
		return fmt.Errorf("transaction failed: %w", err)
	}
	return nil
}

func (s *ClusterSnapshot) registerMutation(revertFn func(), dryRun bool, currTx *[]func()) {
	if dryRun {
		if currTx != nil {
			*currTx = append(*currTx, revertFn)
		}
	} else if s.txCompensations != nil {
		s.txCompensations = append(s.txCompensations, &txMutation{
			revertFn: revertFn,
			active:   true,
		})
	}
}

// CanSchedulePod checks feasibility of a single pod on the specified nodes by running
// PreFilter and Filter plugins. Returns the names of nodes on which the pod can be scheduled,
// the framework.Diagnosis for rejected nodes, and any error.
func (s *ClusterSnapshot) CanSchedulePod(ctx context.Context, logger klog.Logger, pod SchedulablePod) ([]string, *fwk.Diagnosis, error) {
	framework, err := s.sched.FrameworkForPod(pod.Pod)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get framework: %w", err)
	}
	state := fwk.NewCycleState()
	podInfo, err := fwk.NewPodInfo(pod.Pod)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create pod info: %w", err)
	}

	feasibleNodes := make([]string, 0)
	var diagnosis fwk.Diagnosis
	findNodesThatFitPods := func() error {
		nodes, diag, _, err := s.sched.FindNodesThatFitPod(ctx, framework, state, &fwk.QueuedPodInfo{PodInfo: podInfo})
		diagnosis = diag
		for _, node := range nodes {
			feasibleNodes = append(feasibleNodes, node.Node().Name)
		}
		return err
	}

	err = withPlacement(pod.CandidateNodeNames, s.schedulerSnapshot, findNodesThatFitPods)
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

// SchedulePods schedules the given pods onto their candidate nodes using PreFilter and Filter plugins.
// StopOnFailure controls whether the first unschedulable pod stops the loop. Note that
// node-not-found errors always propagate immediately regardless of StopOnFailure, as they
// indicate a programming error rather than a scheduling failure.
func (s *ClusterSnapshot) SchedulePods(ctx context.Context, logger klog.Logger, pods []SchedulablePod, opts SchedulePodsOptions) ([]SchedulingResult, error) {
	if !opts.DryRun {
		if s.txCompensations == nil {
			s.stateVersion++
		}
	}

	var currTx []func()
	if opts.DryRun {
		currTx = []func(){}
		defer func() {
			for i := len(currTx) - 1; i >= 0; i-- {
				currTx[i]()
			}
		}()
	}

	result := make([]SchedulingResult, 0)
	if len(pods) == 0 {
		return result, nil
	}

	for _, p := range pods {
		res, revertFn, err := scheduleOnePod(ctx, s.sched, s.schedulerSnapshot, p.Pod, p.CandidateNodeNames)
		if err != nil {
			return result, err
		}

		if !res.Status.IsSuccess() {
			if opts.StopOnFailure {
				return result, fmt.Errorf("simulation failed: %w", res.Status.AsError())
			}
			result = append(result, schedulingResult(res))
			continue
		}

		result = append(result, schedulingResult(res))

		s.registerMutation(revertFn, opts.DryRun, &currTx)
	}

	return result, nil
}

// SchedulePodsByTemplate attempts to schedule as many pods matching the template as possible.
// It assumes candidate nodes are feasible and moves to the next node only if the pod is unschedulable on the current node.
//
// Note: Each simulated pod gets its own PodGroup. For SchedulePodsByTemplate where N pods are generated
// from one template, they do not share a PodGroup. Consequently, PodTopologySpread and InterPodAffinity
// will not see these pods as siblings during simulation. This is a known limitation that may affect
// scoring accuracy, but is sufficient for filter-only feasibility.
func (s *ClusterSnapshot) SchedulePodsByTemplate(ctx context.Context, logger klog.Logger, template *v1.PodTemplateSpec, candidateNodes []string, maxPods int, opts SchedulePodsByTemplateOptions) ([]SchedulingResult, error) {
	if !opts.DryRun {
		if s.txCompensations == nil {
			s.stateVersion++
		}
	}

	var currTx []func()
	if opts.DryRun {
		currTx = []func(){}
		defer func() {
			for i := len(currTx) - 1; i >= 0; i-- {
				currTx[i]()
			}
		}()
	}

	result := make([]SchedulingResult, 0)
	if maxPods <= 0 || len(candidateNodes) == 0 {
		return result, nil
	}

	nodeIdx := 0
	for i := range maxPods {

		pod := createPodFromTemplate(template, i)
		scheduled := false

		for nodeIdx < len(candidateNodes) {
			res, revertFn, err := scheduleOnePod(ctx, s.sched, s.schedulerSnapshot, pod, []string{candidateNodes[nodeIdx]})
			if err != nil {
				return result, err
			}

			if res.Status.IsSuccess() {
				result = append(result, schedulingResult(res))

				s.registerMutation(revertFn, opts.DryRun, &currTx)

				scheduled = true
				break // Successfully scheduled, move to next pod using the same nodeIdx
			}

			// Unschedulable on this node, try next node
			nodeIdx++
		}

		if !scheduled {
			// No more nodes can fit this pod template, stop scheduling
			break
		}
	}

	return result, nil
}

// PreemptPods removes pods from the snapshot.
// It supports transaction rollbacks if called inside a transaction.
// If any pod fails to be preempted, all previously preempted pods in this call
// are automatically restored and an error is returned.
func (s *ClusterSnapshot) PreemptPods(ctx context.Context, sched *upstreamsync.Scheduler, pods []*v1.Pod) (*Unpreemption, error) {
	// Validate all pods before making any changes.
	for _, pod := range pods {
		if pod.Spec.NodeName == "" {
			return nil, fmt.Errorf("pod %s has no node name", klog.KObj(pod))
		}
	}

	var revertFns []func()

	for _, pod := range pods {
		nodeName := pod.Spec.NodeName

		revertFn, err := removePodFromNode(ctx, s.schedulerSnapshot, pod, nodeName)
		if err != nil {
			// Roll back all already-preempted pods.
			for i := len(revertFns) - 1; i >= 0; i-- {
				revertFns[i]()
			}
			return nil, fmt.Errorf("failed to unreserve and forget pod %s: %w", klog.KObj(pod), err)
		}

		revertFns = append(revertFns, revertFn)
	}

	if s.txCompensations == nil {
		s.stateVersion++
		return nil, nil
	}

	revertAllFn := func() {
		for i := len(revertFns) - 1; i >= 0; i-- {
			revertFns[i]()
		}
	}

	mutation := &txMutation{
		revertFn: revertAllFn,
		active:   true,
	}
	s.txCompensations = append(s.txCompensations, mutation)

	unpreemptRevertFn := func() {
		if mutation != nil {
			mutation.active = true
		}
		for _, pod := range pods {
			_, _ = removePodFromNode(ctx, s.schedulerSnapshot, pod, pod.Spec.NodeName)
		}
	}

	return &Unpreemption{
		RevertFn:     unpreemptRevertFn,
		pods:         pods,
		mutation:     mutation,
		stateVersion: s.stateVersion,
	}, nil
}

// Unpreempt undos the preemption done by the PreemptPods.
func (s *ClusterSnapshot) Unpreempt(u *Unpreemption) ([]*v1.Pod, error) {
	if u == nil {
		return nil, fmt.Errorf("preemption handle is nil")
	}
	if s.txCompensations == nil {
		return nil, fmt.Errorf("no active transaction")
	}
	if s.stateVersion != u.stateVersion {
		return nil, fmt.Errorf("preemption handle is invalid: snapshot has been permanently mutated since preemption")
	}

	if u.mutation == nil {
		return nil, fmt.Errorf("mutation not found")
	}

	if !u.mutation.active {
		return nil, fmt.Errorf("preemption handle is invalid: mutation is already inactive")
	}

	u.mutation.active = false

	// Run revertFn to restore pods to nodes
	u.mutation.revertFn()

	return u.pods, nil
}
