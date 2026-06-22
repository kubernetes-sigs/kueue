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

	"github.com/google/uuid"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	"k8s.io/kubernetes/pkg/scheduler/backend/cache"
	schedulerframework "k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/profile"
)

// ClusterSnapshot wraps a scheduler snapshot and its associated frameworks.
// All ClusterSnapshot instances created from the same ClusterState share the
// same underlying cache.Snapshot. Creating a new snapshot via ClusterState.Snapshot
// updates that shared snapshot in-place, which invalidates any previously returned
// ClusterSnapshot instance — callers must not use a prior snapshot after requesting a new one.
type ClusterSnapshot struct {
	schedulerSnapshot *cache.Snapshot
	frameworks        profile.Map
	transactions      []string
	lastCommittedTx   string
	txCompensation    map[string][]func() error
}

// NewClusterSnapshot creates a new ClusterSnapshot stub wrapping the provided scheduler snapshot and frameworks.
func NewClusterSnapshot(s *cache.Snapshot, frameworks profile.Map) *ClusterSnapshot {
	return &ClusterSnapshot{
		schedulerSnapshot: s,
		frameworks:        frameworks,
		txCompensation:    make(map[string][]func() error),
	}
}

// Transaction executes the provided function within a transaction.
// It rolls back operations if the function returns Revert or an error.
func (s *ClusterSnapshot) Transaction(ctx context.Context, logger klog.Logger, transactionFn func() (TransactionResult, error)) error {
	txId := uuid.New().String()
	s.transactions = append(s.transactions, txId)
	s.txCompensation[txId] = []func() error{}

	defer func() {
		delete(s.txCompensation, txId)
		s.transactions = s.transactions[:len(s.transactions)-1]
	}()

	result, err := transactionFn()

	if err != nil || result == Revert {
		operations := s.txCompensation[txId]
		for i := len(operations) - 1; i >= 0; i-- {
			if rErr := operations[i](); rErr != nil {
				if err != nil {
					return fmt.Errorf("transaction failed (%w) and revert failed at index %d: %v", err, i, rErr)
				}
				return fmt.Errorf("failed to revert operation at index %d: %w", i, rErr)
			}
		}
	} else {
		s.lastCommittedTx = txId
	}

	if err != nil {
		return fmt.Errorf("transaction failed: %w", err)
	}
	return nil
}

// CanSchedulePod checks feasibility of a single pod on the specified nodes by running
// PreFilter and Filter plugins. Returns the names of nodes on which the pod can be scheduled.
func (s *ClusterSnapshot) CanSchedulePod(ctx context.Context, logger klog.Logger, pod SchedulablePod) ([]string, error) {
	fwk, err := s.getFramework(pod.Pod.Spec.SchedulerName)
	if err != nil {
		return nil, fmt.Errorf("failed to get framework: %w", err)
	}
	state := schedulerframework.NewCycleState()

	preFilterResult, status, _ := fwk.RunPreFilterPlugins(ctx, state, pod.Pod)
	if !status.IsSuccess() {
		if status.IsRejected() {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to run prefilter plugins: %w", status.AsError())
	}

	var feasibleNodes []string
	for _, nodeName := range pod.CandidateNodeNames {
		if preFilterResult != nil && !preFilterResult.AllNodes() && !preFilterResult.NodeNames.Has(nodeName) {
			continue
		}
		nodeInfo, err := fwk.SnapshotSharedLister().NodeInfos().Get(nodeName)
		if err != nil {
			return nil, fmt.Errorf("failed to get node %s: %w", nodeName, err)
		}
		if fwk.RunFilterPlugins(ctx, state, pod.Pod, nodeInfo).IsSuccess() {
			feasibleNodes = append(feasibleNodes, nodeName)
		}
	}

	return feasibleNodes, nil
}

// SchedulePods schedules the given pods onto their candidate nodes using PreFilter and Filter plugins.
// StopOnFailure controls whether the first unschedulable pod stops the loop. Note that
// node-not-found errors always propagate immediately regardless of StopOnFailure, as they
// indicate a programming error rather than a scheduling failure.
func (s *ClusterSnapshot) SchedulePods(ctx context.Context, logger klog.Logger, pods []SchedulablePod, opts SchedulePodsOptions) ([]SchedulingResult, error) {
	var currTx []func() error
	if opts.DryRun {
		currTx = []func() error{}
		defer func() {
			for i := len(currTx) - 1; i >= 0; i-- {
				if err := currTx[i](); err != nil {
					logger.Error(err, "Failed to revert operation", "index", i)
				}
			}
		}()
	}

	result := make([]SchedulingResult, 0)
	if len(pods) == 0 {
		return result, nil
	}

	for _, pod := range pods {
		framework, err := s.getFramework(pod.Pod.Spec.SchedulerName)
		if err != nil {
			return nil, fmt.Errorf("failed to get framework for pod %s: %w", klog.KObj(pod.Pod), err)
		}

		cycleState := schedulerframework.NewCycleState()

		prefilterResult, status, _ := framework.RunPreFilterPlugins(ctx, cycleState, pod.Pod)
		if !status.IsSuccess() {
			if opts.StopOnFailure {
				return result, fmt.Errorf("failed to run prefilter plugins: %w", status.AsError())
			}
			continue
		}

		success := false
		for _, nodeName := range pod.CandidateNodeNames {
			if prefilterResult != nil && !prefilterResult.AllNodes() && !prefilterResult.NodeNames.Has(nodeName) {
				continue
			}

			node, err := framework.SnapshotSharedLister().NodeInfos().Get(nodeName)
			if err != nil {
				return nil, fmt.Errorf("failed to get node: %w", err)
			}
			status := framework.RunFilterPlugins(ctx, cycleState, pod.Pod, node)
			if status.IsSuccess() {
				result = append(result, SchedulingResult{
					Pod:              pod.Pod,
					Status:           status,
					SelectedNodeName: nodeName,
				})
				err := s.assumeAndReserve(ctx, logger, pod.Pod, nodeName, cycleState)
				if err != nil {
					unreserveErr := s.unreserveAndForget(ctx, logger, pod.Pod, nodeName, cycleState)
					if unreserveErr != nil {
						return result, fmt.Errorf("failed to unreserve and forget pod: %w", unreserveErr)
					}
					return result, fmt.Errorf("failed to assume and reserve pod: %w", err)
				}
				if opts.DryRun {
					currTx = append(currTx, func() error {
						return s.unreserveAndForget(ctx, logger, pod.Pod, nodeName, cycleState)
					})
				} else if len(s.transactions) > 0 {
					txId := s.transactions[len(s.transactions)-1]
					s.txCompensation[txId] = append(s.txCompensation[txId], func() error {
						return s.unreserveAndForget(ctx, logger, pod.Pod, nodeName, cycleState)
					})
				}
				success = true
				break
			}
		}
		if !success {
			if opts.StopOnFailure {
				return result, fmt.Errorf("pod %s could not be scheduled on any candidate node", klog.KObj(pod.Pod))
			}
		}
	}

	return result, nil
}

// SchedulePodsByTemplate attempts to schedule as many pods matching the template as possible.
// It assumes candidate nodes are feasible and moves to the next node only if the pod is unschedulable on the current node.
func (s *ClusterSnapshot) SchedulePodsByTemplate(ctx context.Context, logger klog.Logger, template *v1.PodTemplateSpec, candidateNodes []string, maxPods int, opts SchedulePodsByTemplateOptions) ([]SchedulingResult, error) {
	var currTx []func() error
	if opts.DryRun {
		currTx = []func() error{}
		defer func() {
			for i := len(currTx) - 1; i >= 0; i-- {
				if err := currTx[i](); err != nil {
					logger.Error(err, "Failed to revert operation", "index", i)
				}
			}
		}()
	}

	result := make([]SchedulingResult, 0)
	if maxPods <= 0 || len(candidateNodes) == 0 {
		return result, nil
	}

	framework, err := s.getFramework(template.Spec.SchedulerName)
	if err != nil {
		return nil, fmt.Errorf("failed to get framework: %w", err)
	}

	podNamePrefix := template.Name
	if podNamePrefix == "" {
		podNamePrefix = "templated-pod"
	}
	ns := template.Namespace
	if ns == "" {
		ns = "default"
	}

	nodeIdx := 0
	for i := 0; i < maxPods; i++ {
		pod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-%d", podNamePrefix, i),
				Namespace: ns,
				UID:       types.UID(uuid.New().String()),
				Labels:    template.Labels,
			},
			Spec: template.Spec,
		}

		cycleState := schedulerframework.NewCycleState()

		prefilterResult, status, _ := framework.RunPreFilterPlugins(ctx, cycleState, pod)
		if !status.IsSuccess() {
			// PreFilter failure applies to all pods from this template; stop scheduling.
			break
		}

		scheduled := false

		for nodeIdx < len(candidateNodes) {
			nodeName := candidateNodes[nodeIdx]
			if prefilterResult != nil && !prefilterResult.AllNodes() && !prefilterResult.NodeNames.Has(nodeName) {
				nodeIdx++
				continue
			}
			node, err := framework.SnapshotSharedLister().NodeInfos().Get(nodeName)
			if err != nil {
				return nil, fmt.Errorf("failed to get node: %w", err)
			}

			status := framework.RunFilterPlugins(ctx, cycleState, pod, node)
			if status.IsSuccess() {
				result = append(result, SchedulingResult{
					Pod:              pod,
					Status:           status,
					SelectedNodeName: nodeName,
				})

				err := s.assumeAndReserve(ctx, logger, pod, nodeName, cycleState)
				if err != nil {
					unreserveErr := s.unreserveAndForget(ctx, logger, pod, nodeName, cycleState)
					if unreserveErr != nil {
						return result, fmt.Errorf("failed to unreserve and forget pod: %w", unreserveErr)
					}
					return result, fmt.Errorf("failed to assume and reserve pod: %w", err)
				}

				if opts.DryRun {
					currTx = append(currTx, func() error {
						return s.unreserveAndForget(ctx, logger, pod, nodeName, cycleState)
					})
				} else if len(s.transactions) > 0 {
					txId := s.transactions[len(s.transactions)-1]
					s.txCompensation[txId] = append(s.txCompensation[txId], func() error {
						return s.unreserveAndForget(ctx, logger, pod, nodeName, cycleState)
					})
				}

				scheduled = true
				break // Move to next pod
			} else {
				// Move to next node if pod is unschedulable on current node
				nodeIdx++
			}
		}

		if !scheduled {
			// Could not schedule this pod on any remaining nodes, stop.
			break
		}
	}

	return result, nil
}

// PreemptPods removes pods from the snapshot.
// It supports transaction rollbacks if called inside a transaction.
// If any pod fails to be preempted, all previously preempted pods in this call
// are automatically restored and an error is returned.
func (s *ClusterSnapshot) PreemptPods(ctx context.Context, logger klog.Logger, pods []*v1.Pod) (*PreemptionSnapshot, error) {
	// Validate all pods before making any changes.
	for _, pod := range pods {
		if pod.Spec.NodeName == "" {
			return nil, fmt.Errorf("pod %s has no node name", klog.KObj(pod))
		}
	}

	var preemptedPods []*v1.Pod
	var nodeNames []string

	var txId string
	insideTx := len(s.transactions) > 0
	if insideTx {
		txId = s.transactions[len(s.transactions)-1]
	}

	for _, pod := range pods {
		nodeName := pod.Spec.NodeName
		cycleState := schedulerframework.NewCycleState()

		if err := s.unreserveAndForget(ctx, logger, pod, nodeName, cycleState); err != nil {
			// Roll back all already-preempted pods.
			for i := len(preemptedPods) - 1; i >= 0; i-- {
				cs := schedulerframework.NewCycleState()
				if rollbackErr := s.assumeAndReserve(ctx, logger, preemptedPods[i], nodeNames[i], cs); rollbackErr != nil {
					return nil, fmt.Errorf("preemption of %s failed (%w), rollback of %s also failed: %v",
						klog.KObj(pod), err, klog.KObj(preemptedPods[i]), rollbackErr)
				}
			}
			// Remove the compensation callbacks added for the already-preempted pods,
			// since we just manually restored them. Without this, a later transaction
			// revert would try to re-add them again and double-add the pods.
			if insideTx {
				n := len(preemptedPods)
				comps := s.txCompensation[txId]
				s.txCompensation[txId] = comps[:len(comps)-n]
			}
			return nil, fmt.Errorf("failed to unreserve and forget pod %s: %w", klog.KObj(pod), err)
		}

		preemptedPods = append(preemptedPods, pod)
		nodeNames = append(nodeNames, nodeName)

		if insideTx {
			s.txCompensation[txId] = append(s.txCompensation[txId], func() error {
				return s.assumeAndReserve(ctx, logger, pod, nodeName, cycleState)
			})
		}
	}

	return newPreemptionSnapshot(s, preemptedPods, nodeNames), nil
}

type PreemptionSnapshot struct {
	snapshot           *ClusterSnapshot
	pods               []*v1.Pod
	nodeNames          []string
	currentTx          string
	currentTxMutations int
	lastCommittedTx    string
}

func newPreemptionSnapshot(s *ClusterSnapshot, pods []*v1.Pod, nodeNames []string) *PreemptionSnapshot {
	var currentTx string
	if len(s.transactions) > 0 {
		currentTx = s.transactions[len(s.transactions)-1]
	}
	var currentTxMutations int
	if currentTx != "" {
		currentTxMutations = len(s.txCompensation[currentTx])
	}

	return &PreemptionSnapshot{
		snapshot:           s,
		pods:               pods,
		nodeNames:          nodeNames,
		currentTx:          currentTx,
		currentTxMutations: currentTxMutations,
		lastCommittedTx:    s.lastCommittedTx,
	}
}

// Unpreempt undos the preemption done by the PreemptPods.
func (ps *PreemptionSnapshot) Unpreempt(ctx context.Context, logger klog.Logger) error {
	newTxWasCommitedAfterPreemption := ps.lastCommittedTx != ps.snapshot.lastCommittedTx

	var currentSnapshotTx string
	if len(ps.snapshot.transactions) > 0 {
		currentSnapshotTx = ps.snapshot.transactions[len(ps.snapshot.transactions)-1]
	}
	newTxStarted := ps.currentTx != currentSnapshotTx

	newMutationForCurrentTx := !newTxStarted && ps.currentTxMutations != len(ps.snapshot.txCompensation[ps.currentTx])

	if newTxWasCommitedAfterPreemption || newTxStarted || newMutationForCurrentTx {
		return fmt.Errorf("snapshot was mutated after preemption")
	}

	for i, pod := range ps.pods {
		nodeName := ps.nodeNames[i]
		cycleState := schedulerframework.NewCycleState()
		if err := ps.snapshot.assumeAndReserve(ctx, logger, pod, nodeName, cycleState); err != nil {
			return fmt.Errorf("failed to assume and reserve pod %s during unpreempt: %w", klog.KObj(pod), err)
		}
	}

	// Non transaction scope, nothing to revert
	if ps.currentTx == "" {
		return nil
	}

	txId := ps.currentTx
	// Number of pods being manually re-added right now
	numPods := len(ps.pods)

	compensationFuncs := ps.snapshot.txCompensation[txId]
	if len(compensationFuncs) < numPods {
		return fmt.Errorf("unexpected number of mutations in transaction compensation list")
	}

	ps.snapshot.txCompensation[txId] = compensationFuncs[:len(compensationFuncs)-numPods]

	return nil
}

func (c *ClusterSnapshot) getFramework(schedulerName string) (schedulerframework.Framework, error) {
	if schedulerName == "" {
		schedulerName = v1.DefaultSchedulerName
	}

	framework, ok := c.frameworks[schedulerName]
	if !ok {
		return nil, fmt.Errorf("no framework found for scheduler: %q", schedulerName)
	}

	return framework, nil
}

func (c *ClusterSnapshot) assumeAndReserve(ctx context.Context, logger klog.Logger, pod *v1.Pod, nodeName string, cycleState *schedulerframework.CycleState) error {
	framework, err := c.getFramework(pod.Spec.SchedulerName)
	if err != nil {
		return fmt.Errorf("failed to get framework: %w", err)
	}

	podInfo, err := schedulerframework.NewPodInfo(pod.DeepCopy())
	if err != nil {
		return fmt.Errorf("failed to create pod info: %w", err)
	}
	podInfo.Pod.Spec.NodeName = nodeName

	err = c.schedulerSnapshot.AssumePod(podInfo)
	if err != nil {
		return fmt.Errorf("failed to assume pod: %w", err)
	}

	status := framework.RunReservePluginsReserve(ctx, cycleState, pod, nodeName)
	if !status.IsSuccess() {
		return fmt.Errorf("failed to reserve pod: %w", status.AsError())
	}
	return nil
}

func (c *ClusterSnapshot) unreserveAndForget(ctx context.Context, logger klog.Logger, pod *v1.Pod, nodeName string, cycleState *schedulerframework.CycleState) error {
	framework, err := c.getFramework(pod.Spec.SchedulerName)
	if err != nil {
		return fmt.Errorf("failed to get framework: %w", err)
	}

	framework.RunReservePluginsUnreserve(ctx, cycleState, pod, nodeName)
	podInfo, err := schedulerframework.NewPodInfo(pod.DeepCopy())
	if err != nil {
		return fmt.Errorf("failed to create pod info: %w", err)
	}
	podInfo.Pod.Spec.NodeName = nodeName
	if err := c.removePod(ctx, podInfo); err != nil {
		return fmt.Errorf("failed to remove pod: %w", err)
	}
	return nil
}

func (c *ClusterSnapshot) removePod(ctx context.Context, podInfo *schedulerframework.PodInfo) error {
	logger := klog.FromContext(ctx)

	// ForgetPod removes an assumed pod from the snapshot's assumption map.
	// If the pod was never assumed (e.g., it was a real scheduled pod removed via
	// preemption), ForgetPod returns an error and we fall back to removing it
	// directly from the node's info list.
	err := c.schedulerSnapshot.ForgetPod(logger, podInfo.Pod)
	if err != nil {
		nodeName := podInfo.Pod.Spec.NodeName
		nodeInfo, getErr := c.schedulerSnapshot.NodeInfos().Get(nodeName)
		if getErr != nil {
			return fmt.Errorf("failed to get node: %w", getErr)
		}
		if removeErr := nodeInfo.RemovePod(logger, podInfo.Pod); removeErr != nil {
			return fmt.Errorf("failed to remove pod from NodeInfo: %w", removeErr)
		}
	}

	return nil
}
