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

package upstreamsync

import (
	"context"
	"fmt"
	"iter"
	"maps"
	"math/rand"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/features"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

/*

This file extracts additional scheduling logic from Scheduler for scheduling PodGroups and CompositePodGroups.

Extracted from kubernetes/kubernetes/pkg/scheduler/schedule_one_podgroup.go.

The upstream doc comments are kept verbatim so that each copy can be diffed against its origin.
Every intentional deviation is marked with the "UPSTREAM-DIFF:" prefix, either on the declaration
or on the exact line that differs. See pkg/upstreamsync/doc.go and CONTRIBUTING.md.

*/

// revertFns is an aggregator of functions that undo the in-memory changes (such
// as assuming pods and calls to Reserve plugins) performed during the pod group
// scheduling algorithm simulation.
//
// UPSTREAM-DIFF: none, copied verbatim.
type revertFns []func()

// Append registers additional revert functions.
//
// UPSTREAM-DIFF: none, copied verbatim.
func (r *revertFns) append(other revertFns) {
	*r = append(*r, other...)
}

// revert executes the underlying reverting functions in reverse order of their registration
// (Last In, First Out). Reverting in LIFO order ensures that sequential operations are unwound
// correctly, preserving state integrity since later operations might depend on the side-effects
// established by earlier ones (similar to how deferred execution works in Go).
//
// UPSTREAM-DIFF: none, copied verbatim.
func (r *revertFns) revert() {
	if r == nil {
		return
	}
	for i := len(*r) - 1; i >= 0; i-- {
		if (*r)[i] != nil {
			(*r)[i]()
			(*r)[i] = nil
		}
	}
	*r = nil
}

// PodGroupAlgorithmResult stores the pod scheduling results for a pod group
// and any information needed to act on these results.
//
// UPSTREAM-DIFF: replaces the unexported podGroupAlgorithmResult.
// Uses the library's exported AlgorithmResult; selectedPlacement and waitingOnPreemption are dropped
// as the library records no scheduling metrics and handles preemption synchronously.
type PodGroupAlgorithmResult struct {
	PodResults          []AlgorithmResult
	Status              *fwk.Status
	PlacementCycleState fwk.PlacementCycleState
	AnyScheduled        bool
}

// RunRootSchedulingAlgorithm orchestrates the scheduling attempt for a root pod group.
// It decides whether to evaluate a single group or recursively evaluate a composite group hierarchy.
// The returned map aggregates scheduling results across the entire pod group hierarchy.
//
// UPSTREAM-DIFF: adapted from Scheduler.runRootSchedulingAlgorithm.
// Unlike upstream (which unconditionally reverts results), scheduler library leaves
// successful reservations intact and returns the revert function for
// callers (e.g. ClusterSnapshot) to control the state.
func (sched *Scheduler) RunRootSchedulingAlgorithm(
	ctx context.Context,
	schedFwk framework.Framework,
	podGroupCycleState *framework.CycleState,
	rootPodGroupInfo *framework.PodGroupInfo,
) (results map[fwk.EntityKey]*PodGroupAlgorithmResult, revertFn func()) {
	var revertFns revertFns
	var result *PodGroupAlgorithmResult
	results = make(map[fwk.EntityKey]*PodGroupAlgorithmResult)

	defer func() {
		if revertFn == nil {
			revertFns.revert()
		}
	}()

	if rootPodGroupInfo.GetType() == fwk.CompositePodGroupKeyType {
		result, revertFns = sched.podGroupSchedulingRecursiveAlgorithm(ctx, schedFwk, podGroupCycleState, rootPodGroupInfo, rootPodGroupInfo, results)
	} else {
		result, revertFns = sched.podGroupSchedulingAlgorithm(ctx, schedFwk, podGroupCycleState, rootPodGroupInfo)
		results[getEntityKey(rootPodGroupInfo)] = result
	}

	if result.Status.IsSuccess() && !result.AnyScheduled {
		result.Status = fwk.NewStatus(fwk.Unschedulable, "no pods were schedulable")
	}

	if !result.Status.IsSuccess() {
		return results, nil
	}
	return results, revertFns.revert
}

// podGroupSchedulingRecursiveAlgorithm runs a recursive pod group scheduling algorithm.
// If the pod group info wraps a composite pod group, it will recursively invoke the algorithm on its children.
// Otherwise, the pod group info wraps a leaf pod group for which we invoke the standard pod group scheduling algorithm.
// The returned revertFns propagates revert functions from all child pod group evaluations up to the root level.
//
// UPSTREAM-DIFF: adapted from Scheduler.podGroupSchedulingRecursiveAlgorithm.
func (sched *Scheduler) podGroupSchedulingRecursiveAlgorithm(
	ctx context.Context,
	schedFwk framework.Framework,
	podGroupCycleState *framework.CycleState,
	root *framework.PodGroupInfo,
	podGroupInfo *framework.PodGroupInfo,
	results map[fwk.EntityKey]*PodGroupAlgorithmResult,
) (*PodGroupAlgorithmResult, revertFns) {
	if podGroupInfo.GetType() == fwk.PodGroupKeyType {
		algorithmResult, childRevertFns := sched.podGroupSchedulingAlgorithm(ctx, schedFwk, podGroupCycleState, podGroupInfo)
		results[getEntityKey(podGroupInfo)] = algorithmResult
		return algorithmResult, childRevertFns
	}

	return sched.compositePodGroupSchedulingAlgorithm(ctx, schedFwk, podGroupCycleState, root, podGroupInfo, results)
}

// UPSTREAM-DIFF: adapted from Scheduler.compositePodGroupSchedulingAlgorithm.
func (sched *Scheduler) compositePodGroupSchedulingAlgorithm(
	ctx context.Context,
	schedFwk framework.Framework,
	podGroupCycleState *framework.CycleState,
	root *framework.PodGroupInfo,
	podGroupInfo *framework.PodGroupInfo,
	results map[fwk.EntityKey]*PodGroupAlgorithmResult,
) (*PodGroupAlgorithmResult, revertFns) {
	podGroupCycleCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	if utilfeature.DefaultFeatureGate.Enabled(features.TopologyAwareWorkloadScheduling) {
		return sched.compositePodGroupSchedulingPlacementAlgorithm(podGroupCycleCtx, schedFwk, podGroupCycleState, root, podGroupInfo, results)
	}

	placementCycleState := framework.NewCycleState()
	placementCycleState.SetPodGroupSchedulingCycle(podGroupCycleState)
	return sched.compositePodGroupSchedulingDefaultAlgorithm(podGroupCycleCtx, schedFwk, placementCycleState, root, podGroupInfo, results)
}

// compositePodGroupSchedulingPlacementAlgorithm tries several different combinations for scheduling the child pod groups and selects the best one.
// First it runs placement generator plugins to create a list of placements.
// Placement is a set of nodes that will be considered when scheduling a pod group.
// Then for each placement it tries to schedule the pod group through compositePodGroupSchedulingDefaultAlgorithm.
// Finally, it runs placement scorer plugins to select the best placement.
//
// UPSTREAM-DIFF: adapted from Scheduler.compositePodGroupSchedulingPlacementAlgorithm.
func (sched *Scheduler) compositePodGroupSchedulingPlacementAlgorithm(
	ctx context.Context,
	schedFwk framework.Framework,
	podGroupCycleState *framework.CycleState,
	root *framework.PodGroupInfo,
	podGroupInfo *framework.PodGroupInfo,
	results map[fwk.EntityKey]*PodGroupAlgorithmResult,
) (finalResult *PodGroupAlgorithmResult, revertFns revertFns) {
	defer func() {
		results[getEntityKey(podGroupInfo)] = finalResult
	}()

	allNodes, err := sched.nodeInfoSnapshot.ListNodesInPlacement()
	if err != nil {
		return &PodGroupAlgorithmResult{
			Status: fwk.AsStatus(fmt.Errorf("failed to list node infos: %w", err)),
		}, nil
	}

	placements, status := schedFwk.RunPlacementGeneratePlugins(ctx, podGroupCycleState, podGroupInfo, allNodes)
	if !status.IsSuccess() {
		return &PodGroupAlgorithmResult{
			Status: status,
		}, nil
	}

	var anyResultSubtree map[fwk.EntityKey]*PodGroupAlgorithmResult
	successfulResults := make(map[*fwk.Placement]map[fwk.EntityKey]*PodGroupAlgorithmResult)

	parentPlacement := sched.nodeInfoSnapshot.GetPlacement()
	defer func() {
		sched.nodeInfoSnapshot.ForgetPlacement()
		err := sched.nodeInfoSnapshot.AssumePlacement(parentPlacement)
		if err != nil {
			finalResult.Status = fwk.AsStatus(fmt.Errorf("failed to restore parent pod group placement: %w", err))
			revertFns.revert()
		}
	}()

	for _, placement := range placements {
		err := sched.nodeInfoSnapshot.AssumePlacement(placement)
		if err != nil {
			return &PodGroupAlgorithmResult{
				Status: fwk.AsStatus(fmt.Errorf("failed to assume pod group placement: %w", err)),
			}, nil
		}
		placementCycleState := framework.NewCycleState()
		placementCycleState.SetPodGroupSchedulingCycle(podGroupCycleState)
		subtreeResult := make(map[fwk.EntityKey]*PodGroupAlgorithmResult)
		result, placementRevertFns := sched.compositePodGroupSchedulingDefaultAlgorithm(ctx, schedFwk, placementCycleState, root, podGroupInfo, subtreeResult)
		placementRevertFns.revert()
		sched.nodeInfoSnapshot.ForgetPlacement()

		if result.Status.IsError() {
			maps.Copy(results, subtreeResult)
			return result, nil
		}

		if anyResultSubtree == nil {
			anyResultSubtree = subtreeResult
		}

		if result.Status.IsSuccess() {
			successfulResults[placement] = subtreeResult
		}
	}

	if len(successfulResults) == 0 {
		anyResultRoot := anyResultSubtree[getEntityKey(podGroupInfo)]
		if anyResultRoot == nil {
			anyResultRoot = &PodGroupAlgorithmResult{
				Status: fwk.NewStatus(fwk.Unschedulable, fmt.Sprintf("0/%d placements are available", len(placements))),
			}
		}
		maps.Copy(results, anyResultSubtree)
		return anyResultRoot, nil
	}

	bestPlacement, status := sched.findBestCompositePodGroupPlacement(ctx, schedFwk, podGroupCycleState, podGroupInfo, successfulResults)
	if !status.IsSuccess() {
		return &PodGroupAlgorithmResult{Status: status}, nil
	}

	bestResult := successfulResults[bestPlacement]

	revertFns, err = sched.assumeSubtreeWithRevert(ctx, schedFwk, podGroupInfo, bestResult)
	if err != nil {
		return &PodGroupAlgorithmResult{
			Status: fwk.AsStatus(fmt.Errorf("failed to assume the subtree: %w", err)),
		}, nil
	}
	maps.Copy(results, bestResult)

	return bestResult[getEntityKey(podGroupInfo)], revertFns
}

// compositePodGroupSchedulingDefaultAlgorithm schedules a composite pod group by recursively scheduling
// its children.
//
// UPSTREAM-DIFF: adapted from Scheduler.compositePodGroupSchedulingDefaultAlgorithm.
func (sched *Scheduler) compositePodGroupSchedulingDefaultAlgorithm(
	ctx context.Context,
	schedFwk framework.Framework,
	placementCycleState *framework.CycleState,
	root *framework.PodGroupInfo,
	podGroupInfo *framework.PodGroupInfo,
	results map[fwk.EntityKey]*PodGroupAlgorithmResult,
) (result *PodGroupAlgorithmResult, revertFns revertFns) {
	defer func() {
		results[getEntityKey(podGroupInfo)] = result
		if !result.Status.IsSuccess() {
			revertFns.revert()
			result.AnyScheduled = false
		}
	}()

	anyScheduled := false
	for _, childPGInfo := range podGroupInfo.Children {
		childState := framework.NewCycleState()
		childState.SetPlacementCycleState(placementCycleState)
		childState.SetPodGroupSchedulingCycle(placementCycleState.GetPodGroupSchedulingCycle())
		childResult, childRevertFns := sched.podGroupSchedulingRecursiveAlgorithm(ctx, schedFwk, childState, root, childPGInfo, results)
		revertFns.append(childRevertFns)

		if childResult.Status.IsError() {
			return &PodGroupAlgorithmResult{
				Status:              fwk.AsStatus(fmt.Errorf("composite pod group evaluation failed due to child error: %w", childResult.Status.AsError())),
				PlacementCycleState: placementCycleState,
			}, revertFns
		}

		if !childResult.Status.IsSuccess() {
			return &PodGroupAlgorithmResult{
				Status:              fwk.NewStatus(fwk.Unschedulable, "composite pod group is unschedulable due to unschedulable child"),
				PlacementCycleState: placementCycleState,
			}, revertFns
		}
		anyScheduled = anyScheduled || childResult.AnyScheduled
	}

	result = &PodGroupAlgorithmResult{
		Status:              fwk.NewStatus(fwk.Success),
		PlacementCycleState: placementCycleState,
		AnyScheduled:        anyScheduled,
	}
	return result, revertFns
}

// PodGroupSchedulingAlgorithm attempts to schedule pods in the pod group according to the policy and constraints and returns the scheduling result for all evaluated pods in the pod group, not necessarily all pods in the pod group.
// The returned revertFns accumulates revert functions for all scheduled pods, allowing the caller to rollback tentative reservations if the pod group scheduling cycle fails.
//
// UPSTREAM-DIFF: adapted from Scheduler.podGroupSchedulingAlgorithm.
func (sched *Scheduler) podGroupSchedulingAlgorithm(
	ctx context.Context,
	schedFwk framework.Framework,
	podGroupCycleState *framework.CycleState,
	podGroupInfo *framework.PodGroupInfo,
) (*PodGroupAlgorithmResult, revertFns) {
	podGroupCycleCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	if utilfeature.DefaultFeatureGate.Enabled(features.TopologyAwareWorkloadScheduling) {
		return sched.podGroupSchedulingPlacementAlgorithm(podGroupCycleCtx, schedFwk, podGroupCycleState, podGroupInfo)
	}

	placementCycleState := framework.NewCycleState()
	placementCycleState.SetPodGroupSchedulingCycle(podGroupCycleState)
	return sched.podGroupSchedulingDefaultAlgorithm(podGroupCycleCtx, schedFwk, placementCycleState, podGroupInfo)
}

// podGroupSchedulingPlacementAlgorithm tries several different combinations for scheduling the pod group and selects the best one.
// First it runs placement generator plugins to create a list of placements.
// Placement is a set of nodes that will be considered when scheduling a pod group.
// Then for each placement it tries to schedule the pod group through podGroupSchedulingDefaultAlgorithm.
// Finally, it runs placement scorer plugins to select the best placement.
//
// UPSTREAM-DIFF: adapted from Scheduler.podGroupSchedulingPlacementAlgorithm.
// Uses ListNodesInPlacement to respect snapshot placement boundaries.
func (sched *Scheduler) podGroupSchedulingPlacementAlgorithm(
	ctx context.Context,
	schedFwk framework.Framework,
	podGroupCycleState *framework.CycleState,
	podGroupInfo *framework.PodGroupInfo,
) (*PodGroupAlgorithmResult, revertFns) {
	allNodes, err := sched.nodeInfoSnapshot.ListNodesInPlacement()
	if err != nil {
		return &PodGroupAlgorithmResult{
			Status: fwk.AsStatus(fmt.Errorf("failed to list node infos: %w", err)),
		}, nil
	}

	placements, status := schedFwk.RunPlacementGeneratePlugins(ctx, podGroupCycleState, podGroupInfo, allNodes)
	if !status.IsSuccess() {
		return &PodGroupAlgorithmResult{Status: status}, nil
	}

	successfulResults := make(map[*fwk.Placement]*PodGroupAlgorithmResult)
	var anyResult *PodGroupAlgorithmResult

	parentPlacement := sched.nodeInfoSnapshot.GetPlacement()
	defer func() {
		sched.nodeInfoSnapshot.ForgetPlacement()
		err := sched.nodeInfoSnapshot.AssumePlacement(parentPlacement)
		if err != nil {
			anyResult.Status = fwk.AsStatus(fmt.Errorf("failed to restore parent pod group placement: %w", err))
		}
	}()

	for _, placement := range placements {
		err := sched.nodeInfoSnapshot.AssumePlacement(placement)
		if err != nil {
			return &PodGroupAlgorithmResult{
				Status: fwk.AsStatus(fmt.Errorf("failed to assume placement: %w", err)),
			}, nil
		}

		placementCycleState := framework.NewCycleState()
		placementCycleState.SetPodGroupSchedulingCycle(podGroupCycleState)
		res, revertFns := sched.podGroupSchedulingDefaultAlgorithm(ctx, schedFwk, placementCycleState, podGroupInfo)
		sched.nodeInfoSnapshot.ForgetPlacement()

		if res.Status.IsError() {
			revertFns.revert()
			return res, nil
		}

		if anyResult == nil {
			anyResult = res
		}

		if res.Status.IsSuccess() {
			successfulResults[placement] = res
			revertFns.revert()
		} else {
			revertFns.revert()
		}
	}

	if len(successfulResults) == 0 {
		anyResult.Status = fwk.NewStatus(fwk.Unschedulable, fmt.Sprintf("0/%d placements are available", len(placements)))
		return anyResult, nil
	}

	bestPlacement, status := sched.findBestPodGroupPlacement(ctx, schedFwk, podGroupCycleState, podGroupInfo, successfulResults)
	if !status.IsSuccess() {
		return &PodGroupAlgorithmResult{Status: status}, nil
	}
	bestResult := successfulResults[bestPlacement]

	var revertFns revertFns
	if utilfeature.DefaultFeatureGate.Enabled(features.CompositePodGroup) {
		revertFns, err = sched.assumeSubtreeWithRevert(ctx, schedFwk, podGroupInfo, map[fwk.EntityKey]*PodGroupAlgorithmResult{getEntityKey(podGroupInfo): bestResult})
		if err != nil {
			return &PodGroupAlgorithmResult{
				Status: fwk.AsStatus(fmt.Errorf("failed to assume the subtree: %w", err)),
			}, nil
		}

		return bestResult, revertFns
	}
	return bestResult, nil
}

// assumeAndReserveWithRevert assumes and reserves the pod on the suggested host, returning a revert function to undo the reservation.
//
// UPSTREAM-DIFF: adapted from Scheduler.assumeAndReserveWithRevert.
func (sched *Scheduler) assumeAndReserveWithRevert(
	ctx context.Context,
	state fwk.CycleState,
	schedFramework framework.Framework,
	podInfo *framework.PodInfo,
	scheduleResult ScheduleResult,
) (*fwk.Status, func()) {
	assumedPodInfo, assumeStatus := sched.assumeAndReserve(ctx, state, schedFramework, podInfo, scheduleResult)
	if !assumeStatus.IsSuccess() {
		return assumeStatus, nil
	}
	return assumeStatus, func() {
		err := sched.unreserveAndForget(ctx, state, schedFramework, assumedPodInfo, scheduleResult.SuggestedHost)
		if err != nil {
			utilruntime.HandleErrorWithContext(ctx, err, "ForgetPod failed")
		}
	}
}

// podGroupSchedulingDefaultAlgorithm runs the default algorithm for scheduling a pod group.
// It tries to schedule each pod using standard filtering and scoring logic in a fixed order.
// If a pod requires preemption to be schedulable, subsequent pods in the algorithm
// treat that pod as already scheduled on that node with victims being already removed in memory.
// The returned revertFns accumulates revert functions for all scheduled pods, allowing the caller
// to rollback tentative reservations if the pod group scheduling cycle fails.
//
// UPSTREAM-DIFF: adapted from Scheduler.podGroupSchedulingDefaultAlgorithm.
// Uses the library's SchedulePod and tracks rollback functions via revertFns.
func (sched *Scheduler) podGroupSchedulingDefaultAlgorithm(
	ctx context.Context,
	schedFwk framework.Framework,
	placementCycleState *framework.CycleState,
	podGroupInfo *framework.PodGroupInfo,
) (result *PodGroupAlgorithmResult, revertFns revertFns) {
	defer func() {
		if !result.Status.IsSuccess() {
			revertFns.revert()
			result.AnyScheduled = false
		}
	}()

	unscheduledPods := podGroupInfo.GetUnscheduledPods()

	result = &PodGroupAlgorithmResult{
		PodResults:          make([]AlgorithmResult, 0, len(unscheduledPods)),
		Status:              nil,
		PlacementCycleState: placementCycleState,
	}

	anyScheduled := false
	for _, pod := range unscheduledPods {
		pInfo, _ := framework.NewPodInfo(pod)
		podCycleState := framework.NewCycleState()
		podCycleState.SetPodGroupSchedulingCycle(placementCycleState.GetPodGroupSchedulingCycle())
		podCycleState.SetPlacementCycleState(placementCycleState)

		pendingPod := &PendingPod{
			PodInfo:    pInfo,
			CycleState: podCycleState,
		}

		algRes, revertFn := sched.SchedulePod(ctx, schedFwk, pendingPod)
		result.PodResults = append(result.PodResults, algRes)

		if !algRes.status.IsSuccess() {
			result.Status = fwk.NewStatus(fwk.Unschedulable, "pod group is unschedulable")
			return result, revertFns
		}

		if revertFn != nil {
			revertFns = append(revertFns, revertFn)
		}

		// Run Permit plugins
		_, permitStatus := schedFwk.RunPermitPlugins(ctx, podCycleState, pod, algRes.scheduleResult.SuggestedHost)
		if !permitStatus.IsWait() && !permitStatus.IsSuccess() {
			result.Status = permitStatus
			return result, revertFns
		}
		anyScheduled = anyScheduled || algRes.status.IsSuccess()
	}

	result.AnyScheduled = anyScheduled
	result.Status = fwk.NewStatus(fwk.Success)
	return result, revertFns
}

// assumeSubtreeWithRevert runs assumeAndReserveWithRevert on all pods within the subtree.
// This is needed for placement-based algorithm, because after evaluating the results for all placements,
// the chosen result needs to be assumed for the other pods in the hierarchy to see the result.
//
// UPSTREAM-DIFF: adapted from Scheduler.assumeSubtreeWithRevert.
func (sched *Scheduler) assumeSubtreeWithRevert(
	ctx context.Context,
	schedFwk framework.Framework,
	pgi *framework.PodGroupInfo,
	results map[fwk.EntityKey]*PodGroupAlgorithmResult,
) (_ revertFns, err error) {
	if results == nil {
		return nil, fmt.Errorf("results for the subtree are missing")
	}

	var revertFns revertFns
	defer func() {
		if err != nil {
			revertFns.revert()
		}
	}()

	for leafResult := range successfulLeafResults(pgi, results) {
		for _, podResult := range leafResult.PodResults {
			if !podResult.status.IsSuccess() || podResult.GetNodeName() == "" {
				continue
			}
			status, revert := sched.assumeAndReserveWithRevert(ctx, podResult.cycleState, schedFwk, podResult.podInfo, podResult.scheduleResult)
			if revert != nil {
				revertFns = append(revertFns, revert)
			}
			if !status.IsSuccess() {
				return nil, status.AsError()
			}
		}
	}

	return revertFns, nil
}

// successfulLeafResults walks the tree down to the successful leafs.
// A leaf is only deemed successful if its ancestors are also successful.
// If the results are missing for a given subtree, that subtree is skipped.
//
// UPSTREAM-DIFF: none, copied verbatim.
func successfulLeafResults(root *framework.PodGroupInfo, results map[fwk.EntityKey]*PodGroupAlgorithmResult) iter.Seq[*PodGroupAlgorithmResult] {
	return func(yield func(*PodGroupAlgorithmResult) bool) {
		var walk func(pgi *framework.PodGroupInfo) bool
		walk = func(pgi *framework.PodGroupInfo) bool {
			result, ok := results[getEntityKey(pgi)]
			// Result may be missing because it may have been skipped due to PlacementFeasible status.
			// If the result for a given subtree is non-success (e.g. actualCount < minGroupCount), we treat all of its descendants as non-success with 0 pods scheduled.
			if !ok || !result.Status.IsSuccess() {
				return true
			}

			for _, child := range pgi.Children {
				if !walk(child) {
					return false
				}
			}

			if len(result.PodResults) > 0 {
				return yield(result)
			}

			return true
		}
		walk(root)
	}
}

// UPSTREAM-DIFF: adapted from Scheduler.findBestPodGroupPlacement.
func (sched *Scheduler) findBestPodGroupPlacement(
	ctx context.Context,
	schedFwk framework.Framework,
	podGroupCycleState fwk.PodGroupCycleState,
	podGroupInfo *framework.PodGroupInfo,
	successfulResults map[*fwk.Placement]*PodGroupAlgorithmResult,
) (*fwk.Placement, *fwk.Status) {
	if len(successfulResults) == 1 {
		for placement := range successfulResults {
			return placement, nil
		}
	}

	placementPodGroupAssignments, placementStates := makePodGroupAssignments(successfulResults)
	return sched.findBestPlacement(ctx, schedFwk, podGroupCycleState, podGroupInfo, placementPodGroupAssignments, placementStates)
}

// UPSTREAM-DIFF: adapted from Scheduler.findBestCompositePodGroupPlacement.
func (sched *Scheduler) findBestCompositePodGroupPlacement(
	ctx context.Context,
	schedFwk framework.Framework,
	podGroupCycleState fwk.PodGroupCycleState,
	podGroupInfo *framework.PodGroupInfo,
	successfulResults map[*fwk.Placement]map[fwk.EntityKey]*PodGroupAlgorithmResult,
) (*fwk.Placement, *fwk.Status) {
	if len(successfulResults) == 1 {
		for placement := range successfulResults {
			return placement, nil
		}
	}

	placementPodGroupAssignments, placementStates := makeCompositePodGroupAssignments(podGroupInfo, successfulResults)
	return sched.findBestPlacement(ctx, schedFwk, podGroupCycleState, podGroupInfo, placementPodGroupAssignments, placementStates)
}

// findBestPlacement uses PlacementScore plugins to determine the best placement based on the scheduling results.
//
// UPSTREAM-DIFF: adapted from Scheduler.findBestPlacement.
func (sched *Scheduler) findBestPlacement(
	ctx context.Context,
	schedFwk framework.Framework,
	podGroupCycleState fwk.PodGroupCycleState,
	podGroupInfo *framework.PodGroupInfo,
	placementPodGroupAssignments []*fwk.PodGroupAssignments,
	placementStates []fwk.PlacementCycleState,
) (*fwk.Placement, *fwk.Status) {
	scores, status := schedFwk.RunPlacementScorePlugins(ctx, podGroupCycleState, podGroupInfo, placementPodGroupAssignments, placementStates)
	if !status.IsSuccess() {
		return nil, status
	}

	for i := range scores {
		scores[i].Randomizer = rand.Int()
	}

	bestScore := &scores[0]
	for _, score := range scores[1:] {
		if score.TotalScore > bestScore.TotalScore ||
			(score.TotalScore == bestScore.TotalScore && score.Randomizer > bestScore.Randomizer) {
			bestScore = &score
		}
	}
	return bestScore.Placement, nil
}

// makePodGroupAssignments converts scheduling results for PodGroup from candidate placements into the format
// required by PlacementScore plugins to score and select the best placement for the pod group.
//
// UPSTREAM-DIFF: adapted from makePodGroupAssignments.
func makePodGroupAssignments(successfulResults map[*fwk.Placement]*PodGroupAlgorithmResult) ([]*fwk.PodGroupAssignments, []fwk.PlacementCycleState) {
	placementAssignments := make([]*fwk.PodGroupAssignments, 0, len(successfulResults))
	placementStates := make([]fwk.PlacementCycleState, 0, len(successfulResults))
	for placement, res := range successfulResults {
		proposedAssignments := make([]fwk.ProposedAssignment, 0, len(res.PodResults))
		for i := range res.PodResults {
			if res.PodResults[i].scheduleResult.SuggestedHost != "" {
				proposedAssignments = append(proposedAssignments, &res.PodResults[i])
			}
		}
		placementAssignments = append(placementAssignments, &fwk.PodGroupAssignments{
			Placement:           placement,
			ProposedAssignments: proposedAssignments,
		})
		placementStates = append(placementStates, res.PlacementCycleState)
	}
	return placementAssignments, placementStates
}

// makeCompositePodGroupAssignments converts scheduling results for CompositePodGroup from candidate placements into the format
// required by PlacementScore plugins to score and select the best placement for the composite pod group.
//
// UPSTREAM-DIFF: none, copied verbatim.
func makeCompositePodGroupAssignments(
	pgi *framework.PodGroupInfo,
	successfulResults map[*fwk.Placement]map[fwk.EntityKey]*PodGroupAlgorithmResult,
) ([]*fwk.PodGroupAssignments, []fwk.PlacementCycleState) {
	placementPodGroupAssignments := make([]*fwk.PodGroupAssignments, 0, len(successfulResults))
	placementStates := make([]fwk.PlacementCycleState, 0, len(successfulResults))
	for placement, subtreeResults := range successfulResults {
		var combinedProposedAssignments []fwk.ProposedAssignment
		for result := range successfulLeafResults(pgi, subtreeResults) {
			for i := range result.PodResults {
				if result.PodResults[i].scheduleResult.SuggestedHost != "" {
					combinedProposedAssignments = append(combinedProposedAssignments, &result.PodResults[i])
				}
			}
		}
		placementPodGroupAssignments = append(placementPodGroupAssignments, &fwk.PodGroupAssignments{
			Placement:           placement,
			ProposedAssignments: combinedProposedAssignments,
		})
		placementStates = append(placementStates, subtreeResults[getEntityKey(pgi)].PlacementCycleState)
	}
	return placementPodGroupAssignments, placementStates
}

func getEntityKey(pgi *framework.PodGroupInfo) fwk.EntityKey {
	if pgi.PodGroup != nil {
		return fwk.PodGroupKey(pgi.PodGroup.Namespace, pgi.PodGroup.Name)
	}
	return fwk.CompositePodGroupKey(pgi.CompositePodGroup.Namespace, pgi.CompositePodGroup.Name)
}
