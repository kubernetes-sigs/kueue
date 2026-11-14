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
	"errors"
	"fmt"
	"iter"
	"maps"
	"math/rand"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
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
	PodGroupInfo        *framework.PodGroupInfo
	PodResults          []AlgorithmResult
	Status              *fwk.Status
	PlacementCycleState fwk.PlacementCycleState
	AnyScheduled        bool
}

// podGroupFitError describes a fit error for a PodGroup or CompositePodGroup.
//
// UPSTREAM-DIFF: none, copied verbatim.
type podGroupFitError struct {
	// status is a PodGroup rejection status.
	status *fwk.Status
	// numPlacements is the number of generated placements.
	numPlacements int
	// unschedulablePlugins are plugins that returned Wait, Unschedulable or UnschedulableAndUnresolvable status.
	unschedulablePlugins sets.Set[string]
	// pendingPlugins are plugins that returned Pending status.
	pendingPlugins sets.Set[string]
}

// newPodGroupFitError creates a new PodGroupFitError without placement context.
//
// UPSTREAM-DIFF: none, copied verbatim.
func newPodGroupFitError(s *fwk.Status) *podGroupFitError {
	return newPodGroupPlacementFitError(s, 0)
}

// newPodGroupPlacementFitError creates a new PodGroupFitError within placement algorithm.
//
// UPSTREAM-DIFF: none, copied verbatim.
func newPodGroupPlacementFitError(s *fwk.Status, numPlacements int) *podGroupFitError {
	fe := &podGroupFitError{
		status:        s,
		numPlacements: numPlacements,
	}
	fe.addPluginStatus(s)
	return fe
}

// addPluginStatus updates the fit error with plugin status.
//
// UPSTREAM-DIFF: none, copied verbatim.
func (fe *podGroupFitError) addPluginStatus(s *fwk.Status) {
	if fitError, ok := errors.AsType[*podGroupFitError](s.AsError()); ok {
		// If the status holds podGroupFitError, it means that it already contains
		// the unschedulablePlugins and pendingPlugins inside.
		fe.unschedulablePlugins = fe.unschedulablePlugins.Union(fitError.unschedulablePlugins)
		fe.pendingPlugins = fe.pendingPlugins.Union(fitError.pendingPlugins)
		return
	}
	if s.Plugin() == "" {
		return
	}
	if s.IsRejected() || s.IsWait() {
		if fe.unschedulablePlugins == nil {
			fe.unschedulablePlugins = sets.New[string]()
		}
		fe.unschedulablePlugins.Insert(s.Plugin())
	}
	if s.Code() == fwk.Pending {
		if fe.pendingPlugins == nil {
			fe.pendingPlugins = sets.New[string]()
		}
		fe.pendingPlugins.Insert(s.Plugin())
	}
}

// Error returns the error message for the fit error.
// In case of non-placement algorithm's error, it will not contain any placement context.
//
// UPSTREAM-DIFF: none, copied verbatim.
func (fe *podGroupFitError) Error() string {
	reason := fe.status.Message()
	if fe.numPlacements == 0 {
		return reason
	}
	return fmt.Sprintf("0/%d placements are available, first placement status: %s", fe.numPlacements, reason)
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

// BuildPodGroupSchedulingFunc returns a PodGroupSchedulingFunc that runs the root
// pod group scheduling algorithm in a fresh CycleState and collects proposed
// assignments from successful leaf pod groups.
//
// UPSTREAM-DIFF: Adapted from Scheduler.buildPodGroupSchedulingFunc in
// pkg/scheduler/schedule_one_podgroup.go (kubernetes/kubernetes#142979), with one difference:
// Calls revertFn() immediately because RunRootSchedulingAlgorithm in upstreamsync
// returns revertFn to the caller instead of deferring it internally.
// Migration Plan (1.38): Replace with PodGroupSchedulingAlgorithm.BuildPodGroupSchedulingFunc
// once kubernetes/kubernetes#142979 and kubernetes/kubernetes#141852 merge.
func (sched *Scheduler) BuildPodGroupSchedulingFunc(
	schedFwk framework.Framework,
	podGroupInfo *framework.PodGroupInfo,
) fwk.PodGroupSchedulingFunc {
	rootKey := getEntityKey(podGroupInfo)
	return func(ctx context.Context) (*fwk.PodGroupAssignments, *fwk.Status) {
		results, revertFn := sched.RunRootSchedulingAlgorithm(ctx, schedFwk, framework.NewCycleState(), podGroupInfo)
		if revertFn != nil {
			revertFn()
		}
		rootResult := results[rootKey]
		if !rootResult.Status.IsSuccess() {
			return nil, rootResult.Status
		}
		var proposedAssignments []fwk.ProposedAssignment
		for res := range SuccessfulLeafResults(podGroupInfo, results) {
			proposedAssignments = append(proposedAssignments, makeProposedAssignments(res)...)
		}
		return &fwk.PodGroupAssignments{
			ProposedAssignments: proposedAssignments,
		}, rootResult.Status
	}
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
			PodGroupInfo: podGroupInfo,
			Status:       fwk.AsStatus(fmt.Errorf("failed to list node infos: %w", err)),
		}, nil
	}

	placements, status := schedFwk.RunPlacementGeneratePlugins(ctx, podGroupCycleState, podGroupInfo, allNodes)
	if !status.IsSuccess() {
		return &PodGroupAlgorithmResult{
			PodGroupInfo: podGroupInfo,
			Status:       status,
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
				PodGroupInfo: podGroupInfo,
				Status:       fwk.AsStatus(fmt.Errorf("failed to assume pod group placement: %w", err)),
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
				PodGroupInfo: podGroupInfo,
				Status:       fwk.NewStatus(fwk.Unschedulable, fmt.Sprintf("0/%d placements are available", len(placements))),
			}
		}
		maps.Copy(results, anyResultSubtree)
		return anyResultRoot, nil
	}

	bestPlacement, status := sched.findBestCompositePodGroupPlacement(ctx, schedFwk, podGroupCycleState, podGroupInfo, successfulResults)
	if !status.IsSuccess() {
		return &PodGroupAlgorithmResult{
			PodGroupInfo: podGroupInfo,
			Status:       status,
		}, nil
	}

	bestResult := successfulResults[bestPlacement]

	revertFns, err = sched.assumeSubtreeWithRevert(ctx, schedFwk, podGroupInfo, bestResult)
	if err != nil {
		return &PodGroupAlgorithmResult{
			PodGroupInfo: podGroupInfo,
			Status:       fwk.AsStatus(fmt.Errorf("failed to assume the subtree: %w", err)),
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
				PodGroupInfo:        podGroupInfo,
				Status:              fwk.AsStatus(fmt.Errorf("composite pod group evaluation failed due to child error: %w", childResult.Status.AsError())),
				PlacementCycleState: placementCycleState,
			}, revertFns
		}

		if !childResult.Status.IsSuccess() {
			return &PodGroupAlgorithmResult{
				PodGroupInfo:        podGroupInfo,
				Status:              fwk.NewStatus(fwk.Unschedulable, "composite pod group is unschedulable due to unschedulable child"),
				PlacementCycleState: placementCycleState,
			}, revertFns
		}
		anyScheduled = anyScheduled || childResult.AnyScheduled
	}

	result = &PodGroupAlgorithmResult{
		PodGroupInfo:        podGroupInfo,
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
			PodGroupInfo: podGroupInfo,
			Status:       fwk.AsStatus(fmt.Errorf("failed to list node infos: %w", err)),
		}, nil
	}

	placements, status := schedFwk.RunPlacementGeneratePlugins(ctx, podGroupCycleState, podGroupInfo, allNodes)
	if !status.IsSuccess() {
		return &PodGroupAlgorithmResult{
			PodGroupInfo: podGroupInfo,
			Status:       status,
		}, nil
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
				PodGroupInfo: podGroupInfo,
				Status:       fwk.AsStatus(fmt.Errorf("failed to assume placement: %w", err)),
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
		return &PodGroupAlgorithmResult{
			PodGroupInfo: podGroupInfo,
			Status:       status,
		}, nil
	}
	bestResult := successfulResults[bestPlacement]

	var revertFns revertFns
	if utilfeature.DefaultFeatureGate.Enabled(features.CompositePodGroup) {
		revertFns, err = sched.assumeSubtreeWithRevert(ctx, schedFwk, podGroupInfo, map[fwk.EntityKey]*PodGroupAlgorithmResult{getEntityKey(podGroupInfo): bestResult})
		if err != nil {
			return &PodGroupAlgorithmResult{
				PodGroupInfo: podGroupInfo,
				Status:       fwk.AsStatus(fmt.Errorf("failed to assume the subtree: %w", err)),
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
		PodGroupInfo:        podGroupInfo,
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

	for leafResult := range SuccessfulLeafResults(pgi, results) {
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

// CompletePodGroupAlgorithmResult ensures that the PodGroupAlgorithmResult contains the same number of PodResults as there are unscheduled pods in the pod group.
//
// UPSTREAM-DIFF: adapted from completePodGroupAlgorithmResult; exported for use by pkg/upstreamsync/snapshot.
// Uses PodGroupInfo.GetUnscheduledPods() instead of QueuedPodInfo (see PendingPod) and omits ctx and podSchedulingContext.
func CompletePodGroupAlgorithmResult(
	podGroupState *framework.CycleState,
	podGroupResult *PodGroupAlgorithmResult,
) *PodGroupAlgorithmResult {
	unscheduledPods := podGroupResult.PodGroupInfo.GetUnscheduledPods()
	numInResult := len(podGroupResult.PodResults)
	numInQueue := len(unscheduledPods)
	// UPSTREAM-DIFF: upstream currently overwrites successful pod statuses with the failed
	// pod group status in submitPodGroupResult; done here as the library has no submit phase.
	if !podGroupResult.Status.IsSuccess() {
		for i := range podGroupResult.PodResults {
			if podGroupResult.PodResults[i].status.IsSuccess() {
				podGroupResult.PodResults[i].status = podGroupResult.Status.Clone()
			}
		}
	}
	if numInResult == numInQueue {
		return podGroupResult
	}
	newResults := make([]AlgorithmResult, numInQueue)
	copy(newResults, podGroupResult.PodResults)
	for i := numInResult; i < numInQueue; i++ {
		pod := unscheduledPods[i]
		pInfo, _ := framework.NewPodInfo(pod)
		placementCycleState := framework.NewCycleState()
		placementCycleState.SetPodGroupSchedulingCycle(podGroupState)
		podCycleState := framework.NewCycleState()
		podCycleState.SetPodGroupSchedulingCycle(podGroupState)
		podCycleState.SetPlacementCycleState(placementCycleState)
		newResults[i] = AlgorithmResult{
			pod:        pod,
			podInfo:    pInfo,
			cycleState: podCycleState,
			status:     podGroupResult.Status.Clone(),
		}
	}
	podGroupResult.PodResults = newResults
	return podGroupResult
}

// CompleteCompositePodGroupAlgorithmResult post-processes scheduling results for a composite pod group.
// It ensures that every pod in every subgroup has a fully populated status and that failure statuses
// are propagated down the tree before finalizing the cycle.
//
// UPSTREAM-DIFF: adapted from completeCompositePodGroupAlgorithmResult; exported for use by
// pkg/upstreamsync/snapshot. Takes *framework.PodGroupInfo instead of *framework.QueuedPodGroupInfo
// and omits ctx.
func CompleteCompositePodGroupAlgorithmResult(
	rootPodGroupInfo *framework.PodGroupInfo,
	rootCycleState *framework.CycleState,
	pgResults map[fwk.EntityKey]*PodGroupAlgorithmResult,
) map[fwk.EntityKey]*PodGroupAlgorithmResult {
	completeCompositePodGroupAlgorithmResultMap(rootPodGroupInfo, pgResults, nil)
	for _, pgResult := range pgResults {
		if pgResult.PodGroupInfo.GetType() == fwk.PodGroupKeyType {
			// Ensure PodResults has an entry for each pod in the pod group with a status.
			CompletePodGroupAlgorithmResult(rootCycleState, pgResult)
		}
	}
	return pgResults
}

// completeCompositePodGroupAlgorithmResultMap propagates failures of composite pod groups to their
// descendants, so that no group is bound while one of its ancestors failed:
//   - A group that succeeded or was not evaluated gets a status derived from its nearest failed
//     ancestor (see buildAncestorFailureStatus).
//   - A group that failed on its own keeps its status, unless the ancestor failed with an Error
//     and the group did not, in which case the ancestor's Error takes precedence.
//   - A group that keeps its own failure becomes the nearest failed ancestor of its subtree.
//
// UPSTREAM-DIFF: none, copied verbatim.
func completeCompositePodGroupAlgorithmResultMap(
	podGroupInfo *framework.PodGroupInfo,
	pgResults map[fwk.EntityKey]*PodGroupAlgorithmResult,
	failedAncestor *PodGroupAlgorithmResult,
) {
	key := getEntityKey(podGroupInfo)
	result, evaluated := pgResults[key]
	if !evaluated {
		// Groups are only skipped after an ancestor failed.
		result = &PodGroupAlgorithmResult{PodGroupInfo: podGroupInfo}
		pgResults[key] = result
	}
	if failedAncestor != nil {
		if !evaluated || result.Status.IsSuccess() {
			// The group can't be bound while its ancestor failed.
			result.Status = buildAncestorFailureStatus(failedAncestor)
		} else if failedAncestor.Status.IsError() && !result.Status.IsError() {
			// An error takes precedence over the group's own non-error failure.
			result.Status = buildAncestorFailureStatus(failedAncestor)
		} else {
			// The group's own failure is the more specific reason, so its subtree inherits it.
			failedAncestor = result
		}
	} else if !result.Status.IsSuccess() {
		// The subtree inherits the group's own failure.
		failedAncestor = result
	}

	for _, child := range podGroupInfo.Children {
		completeCompositePodGroupAlgorithmResultMap(child, pgResults, failedAncestor)
	}
}

// buildAncestorFailureStatus returns the status for a pod group whose ancestor failed.
// The status only names the ancestor, without repeating the ancestor's failure reason,
// which is reported in the ancestor's own status.
// If the ancestor's status is an Error, it returns an Error.
// Otherwise, it returns ancestor's status code with a podGroupFitError that names the ancestor
// and carries its plugins.
//
// UPSTREAM-DIFF: none, copied verbatim.
func buildAncestorFailureStatus(failedAncestor *PodGroupAlgorithmResult) *fwk.Status {
	ancestorName := failedAncestor.PodGroupInfo.GetName()
	if failedAncestor.Status.IsError() {
		return fwk.AsStatus(fmt.Errorf("ancestor composite pod group %q encountered an error", ancestorName))
	}

	msg := fmt.Sprintf("ancestor composite pod group %q is unschedulable", ancestorName)
	fitError := newPodGroupFitError(fwk.NewStatus(fwk.Unschedulable, msg))
	fitError.addPluginStatus(failedAncestor.Status)
	return fwk.NewStatus(failedAncestor.Status.Code()).WithError(fitError)
}

// SuccessfulLeafResults walks the tree down to the successful leafs.
// A leaf is only deemed successful if its ancestors are also successful.
// If the results are missing for a given subtree, that subtree is skipped.
//
// UPSTREAM-DIFF: exported for use by pkg/upstreamsync/snapshot.
func SuccessfulLeafResults(root *framework.PodGroupInfo, results map[fwk.EntityKey]*PodGroupAlgorithmResult) iter.Seq[*PodGroupAlgorithmResult] {
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

// makeProposedAssignments extracts ProposedAssignment entries for pods that were assigned a host.
//
// UPSTREAM-DIFF: none, copied verbatim.
func makeProposedAssignments(res *PodGroupAlgorithmResult) []fwk.ProposedAssignment {
	proposedAssignments := make([]fwk.ProposedAssignment, 0, len(res.PodResults))
	for i := range res.PodResults {
		if res.PodResults[i].scheduleResult.SuggestedHost != "" {
			proposedAssignments = append(proposedAssignments, &res.PodResults[i])
		}
	}
	return proposedAssignments
}

// makePodGroupAssignments converts scheduling results for PodGroup from candidate placements into the format
// required by PlacementScore plugins to score and select the best placement for the pod group.
//
// UPSTREAM-DIFF: adapted from makePodGroupAssignments.
func makePodGroupAssignments(successfulResults map[*fwk.Placement]*PodGroupAlgorithmResult) ([]*fwk.PodGroupAssignments, []fwk.PlacementCycleState) {
	placementAssignments := make([]*fwk.PodGroupAssignments, 0, len(successfulResults))
	placementStates := make([]fwk.PlacementCycleState, 0, len(successfulResults))
	for placement, res := range successfulResults {
		placementAssignments = append(placementAssignments, &fwk.PodGroupAssignments{
			Placement:           placement,
			ProposedAssignments: makeProposedAssignments(res),
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
		for result := range SuccessfulLeafResults(pgi, subtreeResults) {
			combinedProposedAssignments = append(combinedProposedAssignments, makeProposedAssignments(result)...)
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
