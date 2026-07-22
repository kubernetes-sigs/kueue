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
	"container/heap"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	v1 "k8s.io/api/core/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/klog/v2"
	extenderv1 "k8s.io/kube-scheduler/extender/v1"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/features"
	"k8s.io/kubernetes/pkg/scheduler"
	"k8s.io/kubernetes/pkg/scheduler/backend/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework/parallelize"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/dynamicresources"
	"k8s.io/kubernetes/pkg/scheduler/metrics"
	utiltrace "k8s.io/utils/trace"
)

/*

This is a proposal for extracting common logic from Scheduler for scheduling pods.

This component would be used in PG scheduling cycle, pod-by-pod scheduling cycle and the simulations.

In PG scheduling cycle, the caller would additionally run the placement feasible extensions etc.
In pod-by-pod scheduling cycle, the caller would additionally run post filter extensions.

Extracted from kubernetes/kubernetes/pkg/upstreamsync/scheduler/schedule_one.go and schedule_one_podgroup.go

*/

type ScheduleResult = scheduler.ScheduleResult

type Scheduler struct {
	nodeInfoSnapshot   *cache.Snapshot
	currentCycle       int64
	nextStartNodeIndex int
	numNodesToFind     int32
}

func NewScheduler(nodeInfoSnapshot *cache.Snapshot,
	currentCycle int64,
	nextStartNodeIndex int,
	numNodesToFind int32) *Scheduler {
	return &Scheduler{
		nodeInfoSnapshot:   nodeInfoSnapshot,
		currentCycle:       currentCycle,
		nextStartNodeIndex: nextStartNodeIndex,
		numNodesToFind:     numNodesToFind,
	}
}

type PendingPod struct {
	*framework.PodInfo
	PodSignature fwk.PodSignature
	CycleState   fwk.CycleState
}

type AlgorithmResult struct {
	ScheduleResult ScheduleResult
	Pod            *v1.Pod
	Status         *fwk.Status
	CycleState     fwk.CycleState
}

func (sched *Scheduler) SchedulePod(ctx context.Context, schedFwk framework.Framework, podInfo *PendingPod) (AlgorithmResult, func()) {
	pod := podInfo.Pod
	state := podInfo.CycleState

	scheduleResult, err := sched.schedulePod(ctx, schedFwk, podInfo)
	if err != nil {
		var status *fwk.Status
		if err == scheduler.ErrNoNodesAvailable {
			status = fwk.NewStatus(fwk.UnschedulableAndUnresolvable).WithError(err)
		} else if _, ok := err.(*framework.FitError); !ok {
			status = fwk.AsStatus(err)
		} else {
			status = fwk.NewStatus(fwk.Unschedulable).WithError(err)
		}

		return AlgorithmResult{
			Pod:            pod,
			ScheduleResult: scheduleResult,
			Status:         status,
		}, nil
	}
	assumedPodInfo, assumeStatus := sched.assumeAndReserve(ctx, state, schedFwk, podInfo.PodInfo, scheduleResult)
	if !assumeStatus.IsSuccess() {
		return AlgorithmResult{
			Pod:            pod,
			ScheduleResult: ScheduleResult{},
			Status:         assumeStatus,
		}, nil
	}

	revertFn := func() {
		err := sched.unreserveAndForget(ctx, state, schedFwk, assumedPodInfo, scheduleResult.SuggestedHost)
		if err != nil {
			utilruntime.HandleErrorWithContext(ctx, err, "ForgetPod failed")
		}
	}

	return AlgorithmResult{
		Pod:            pod,
		ScheduleResult: scheduleResult,
		Status:         nil,
	}, revertFn
}

// assumeAndReserve assumes and reserves the pod in scheduler's memory.
func (sched *Scheduler) assumeAndReserve(
	ctx context.Context,
	state fwk.CycleState,
	schedFramework framework.Framework,
	podInfo *framework.PodInfo,
	scheduleResult ScheduleResult,
) (*framework.PodInfo, *fwk.Status) {
	logger := klog.FromContext(ctx)
	// Tell the cache to assume that a pod now is running on a given node, even though it hasn't been bound yet.
	// This allows us to keep scheduling without waiting on binding to occur.
	assumedPodInfo := podInfo.DeepCopy()
	assumedPod := assumedPodInfo.Pod
	// assume modifies `assumedPod` by setting NodeName=scheduleResult.SuggestedHost
	err := sched.assume(logger, state, assumedPodInfo, scheduleResult.SuggestedHost)
	if err != nil {
		// This is most probably result of a BUG in retrying logic.
		// We report an error here so that pod scheduling can be retried.
		// This relies on the fact that Error will check if the pod has been bound
		// to a node and if so will not add it back to the unscheduled pods queue
		// (otherwise this would cause an infinite loop).
		return assumedPodInfo, fwk.AsStatus(err)
	}

	// Run the Reserve method of reserve plugins.
	if sts := schedFramework.RunReservePluginsReserve(ctx, state, assumedPod, scheduleResult.SuggestedHost); !sts.IsSuccess() {
		// trigger un-reserve to clean up state associated with the reserved Pod
		err := sched.unreserveAndForget(ctx, state, schedFramework, assumedPodInfo, scheduleResult.SuggestedHost)
		if err != nil {
			utilruntime.HandleErrorWithContext(ctx, err, "ForgetPod failed")
		}

		if sts.IsRejected() {
			fitErr := &framework.FitError{
				NumAllNodes: 1,
				Pod:         podInfo.Pod,
				Diagnosis: framework.Diagnosis{
					NodeToStatus: framework.NewDefaultNodeToStatus(),
				},
			}
			fitErr.Diagnosis.NodeToStatus.Set(scheduleResult.SuggestedHost, sts)
			fitErr.Diagnosis.AddPluginStatus(sts)
			return assumedPodInfo, fwk.NewStatus(sts.Code()).WithError(fitErr)
		}
		return assumedPodInfo, sts
	}
	return assumedPodInfo, nil
}

// unreserveAndForget unreserves and forgets the pod from scheduler's memory.
// This function shouldn't be called during binding cycle with a state, where IsPodGroupSchedulingCycle is set to true,
// but this shouldn't happen, because such pods with such state cannot reach binding.
func (sched *Scheduler) unreserveAndForget(
	ctx context.Context,
	state fwk.CycleState,
	schedFramework framework.Framework,
	assumedPodInfo *framework.PodInfo,
	nodeName string,
) error {
	logger := klog.FromContext(ctx)

	schedFramework.RunReservePluginsUnreserve(ctx, state, assumedPodInfo.Pod, nodeName)
	return sched.nodeInfoSnapshot.ForgetPod(logger, assumedPodInfo.Pod)
}

// assume signals to the cache that a pod is already in the cache, so that binding can be asynchronous.
// When called during pod group scheduling cycle, pod is assumed in the snapshot instead.
func (sched *Scheduler) assume(logger klog.Logger, state fwk.CycleState, assumedPodInfo *framework.PodInfo, host string) error {
	// Optimistically assume that the binding will succeed and send it to apiserver
	// in the background.
	// If the binding fails, scheduler will release resources allocated to assumed pod
	// immediately.
	assumedPodInfo.Pod.Spec.NodeName = host
	if utilfeature.DefaultFeatureGate.Enabled(features.DRANodeAllocatableResources) {
		// If DRANodeAllocatableResources is enabled, copy the calculated node allocatable resource claim status
		// from the cycle state to the assumed pod's status. This ensures that the scheduler's
		// cached version of the pod reflects the node allocatable resources allocated by the DRA plugin
		// for this scheduling cycle, making this information available for NodeInfo cache update.
		// Any potential NodeAllocatableResourceClaimStatuses from a previously failed scheduling attempt is overwritten.
		// This field is not explicitly cleared as the Pod object is reconstructed in handleSchedulingFailure()
		// before re-queueing.
		assumedPodInfo.Pod.Status.NodeAllocatableResourceClaimStatuses = dynamicresources.ExtractPodNodeAllocatableResourceClaimStatus(logger, state, host)
	}

	err := sched.nodeInfoSnapshot.AssumePod(assumedPodInfo)
	if err != nil {
		logger.Error(err, "Scheduler snapshot AssumePod failed")
		return err
	}
	return nil
}

// schedulePod tries to schedule the given pod to one of the nodes in the node list.
// If it succeeds, it will return the name of the node.
// If it fails, it will return a FitError with reasons.
func (sched *Scheduler) schedulePod(ctx context.Context, fwk framework.Framework, podInfo *PendingPod) (result ScheduleResult, err error) {
	pod := podInfo.Pod
	trace := utiltrace.New("Scheduling", utiltrace.Field{Key: "namespace", Value: pod.Namespace}, utiltrace.Field{Key: "name", Value: pod.Name})
	defer trace.LogIfLong(100 * time.Millisecond)

	if sched.nodeInfoSnapshot.NumNodesInPlacement() == 0 {
		return result, scheduler.ErrNoNodesAvailable
	}

	feasibleNodes, diagnosis, nodeHint, err := sched.findNodesThatFitPod(ctx, fwk, podInfo, false)
	if err != nil {
		return result, err
	}
	trace.Step("Computing predicates done")

	if len(feasibleNodes) == 0 {
		return result, &framework.FitError{
			Pod:         pod,
			NumAllNodes: sched.nodeInfoSnapshot.NumNodesInPlacement(),
			Diagnosis:   diagnosis,
		}
	}

	// When only one node after predicate, just use it.
	if len(feasibleNodes) == 1 {
		node := feasibleNodes[0].Node().Name
		if utilfeature.DefaultFeatureGate.Enabled(features.OpportunisticBatching) {
			fwk.StoreScheduleResults(ctx, podInfo.PodSignature, nodeHint, node, nil, sched.currentCycle)
		}
		return ScheduleResult{
			SuggestedHost:  node,
			EvaluatedNodes: 1 + diagnosis.NodeToStatus.Len(),
			FeasibleNodes:  1,
		}, nil
	}

	priorityList, err := prioritizeNodes(ctx, fwk.Extenders(), fwk, podInfo.CycleState, pod, feasibleNodes)
	if err != nil {
		return result, err
	}

	sortedPrioritizedNodes := newSortedNodeScores(priorityList)
	node := sortedPrioritizedNodes.Pop()
	trace.Step("Prioritizing done")

	if utilfeature.DefaultFeatureGate.Enabled(features.OpportunisticBatching) {
		fwk.StoreScheduleResults(ctx, podInfo.PodSignature, nodeHint, node, sortedPrioritizedNodes, sched.currentCycle)
	}

	return ScheduleResult{
		SuggestedHost:  node,
		EvaluatedNodes: len(feasibleNodes) + diagnosis.NodeToStatus.Len(),
		FeasibleNodes:  len(feasibleNodes),
	}, err
}

func (sched *Scheduler) FindAllNodesThatFitPod(ctx context.Context, schedFramework framework.Framework, podInfo *PendingPod) ([]fwk.NodeInfo, framework.Diagnosis, string, error) {
	return sched.findNodesThatFitPod(ctx, schedFramework, podInfo, true)
}

// Filters the nodes to find the ones that fit the pod based on the framework
// filter plugins and filter extenders.
func (sched *Scheduler) findNodesThatFitPod(ctx context.Context, schedFramework framework.Framework, podInfo *PendingPod, findAll bool) ([]fwk.NodeInfo, framework.Diagnosis, string, error) {
	state := podInfo.CycleState
	logger := klog.FromContext(ctx)
	diagnosis := framework.Diagnosis{
		NodeToStatus: framework.NewDefaultNodeToStatus(),
	}
	allNodes, err := sched.nodeInfoSnapshot.ListNodesInPlacement()
	if err != nil {
		return nil, diagnosis, "", err
	}
	// Run "prefilter" plugins.
	pod := podInfo.Pod
	preRes, s, unscheduledPlugins := schedFramework.RunPreFilterPlugins(ctx, state, pod)
	diagnosis.UnschedulablePlugins = unscheduledPlugins
	if !s.IsSuccess() {
		if !s.IsRejected() {
			return nil, diagnosis, "", s.AsError()
		}
		// All nodes in NodeToStatus will have the same status so that they can be handled in the preemption.
		diagnosis.NodeToStatus.SetAbsentNodesStatus(s)

		// Record the messages from PreFilter in Diagnosis.PreFilterMsg.
		msg := s.Message()
		diagnosis.PreFilterMsg = msg
		logger.V(5).Info("Status after running PreFilter plugins for pod", "pod", klog.KObj(pod), "status", msg)
		diagnosis.AddPluginStatus(s)
		return nil, diagnosis, "", nil
	}

	var nodeHint string
	if utilfeature.DefaultFeatureGate.Enabled(features.OpportunisticBatching) {
		// We get the node hint even if we have a nominated name for simplicity, but we could potentially avoid it
		// in this scenario in the future.
		nodeHint = schedFramework.GetNodeHint(ctx, pod, podInfo.PodSignature, state, sched.currentCycle)
	}

	// "NominatedNodeName" can potentially be set in a previous scheduling cycle as a result of preemption.
	// This node is likely the only candidate that will fit the pod, and hence we try it first before iterating over all nodes.
	// We take the same tack for hinted nodes from the batch module.
	if !findAll && (len(pod.Status.NominatedNodeName) > 0 || len(nodeHint) > 0) {
		feasibleNodes, err := sched.evaluateNominatedNode(ctx, pod, schedFramework, state, nodeHint, diagnosis)
		if err != nil {
			utilruntime.HandleErrorWithContext(ctx, err, "Evaluation failed on nominated node", "pod", klog.KObj(pod), "node", pod.Status.NominatedNodeName)
		}
		// Nominated node passes all the filters, scheduler is good to assign this node to the pod.
		if len(feasibleNodes) != 0 {
			return feasibleNodes, diagnosis, nodeHint, nil
		}
	}

	nodes := allNodes
	if !preRes.AllNodes() {
		nodes = make([]fwk.NodeInfo, 0, len(preRes.NodeNames))
		for nodeName := range preRes.NodeNames {
			// PreRes may return nodeName(s) which do not exist; we verify
			// node exists in the Snapshot within the selected placement.
			if nodeInfo, err := sched.nodeInfoSnapshot.GetNodeInPlacement(nodeName); err == nil {
				nodes = append(nodes, nodeInfo)
			}
		}
		diagnosis.NodeToStatus.SetAbsentNodesStatus(fwk.NewStatus(fwk.UnschedulableAndUnresolvable, fmt.Sprintf("node(s) didn't satisfy plugin(s) %v", sets.List(unscheduledPlugins))))
	}
	feasibleNodes, err := sched.findNodesThatPassFilters(ctx, schedFramework, state, pod, &diagnosis, nodes, findAll)
	// always try to update the sched.nextStartNodeIndex regardless of whether an error has occurred
	// this is helpful to make sure that all the nodes have a chance to be searched
	processedNodes := len(feasibleNodes) + diagnosis.NodeToStatus.Len()
	sched.nextStartNodeIndex = (sched.nextStartNodeIndex + processedNodes) % len(allNodes)
	if err != nil {
		return nil, diagnosis, nodeHint, err
	}

	feasibleNodesAfterExtender, err := findNodesThatPassExtenders(ctx, schedFramework.Extenders(), pod, feasibleNodes, diagnosis.NodeToStatus)
	if err != nil {
		return nil, diagnosis, nodeHint, err
	}
	if len(feasibleNodesAfterExtender) != len(feasibleNodes) {
		// Extenders filtered out some nodes.
		//
		// Extender doesn't support any kind of requeueing feature like EnqueueExtensions in the scheduling framework.
		// When Extenders reject some Nodes and the pod ends up being unschedulable,
		// we put fwk.ExtenderName to pInfo.UnschedulablePlugins.
		// This Pod will be requeued from unschedulable pod pool to activeQ/backoffQ
		// by any kind of cluster events.
		// https://github.com/kubernetes/kubernetes/issues/122019
		if diagnosis.UnschedulablePlugins == nil {
			diagnosis.UnschedulablePlugins = sets.New[string]()
		}
		diagnosis.UnschedulablePlugins.Insert(framework.ExtenderName)
	}

	return feasibleNodesAfterExtender, diagnosis, nodeHint, nil
}

func (sched *Scheduler) evaluateNominatedNode(ctx context.Context, pod *v1.Pod, schedFramework framework.Framework, state fwk.CycleState, nodeHint string, diagnosis framework.Diagnosis) ([]fwk.NodeInfo, error) {
	// In the future we could potentially use the hint if the nominated node failed.
	// https://github.com/kubernetes/kubernetes/issues/135163
	nnn := pod.Status.NominatedNodeName
	if len(nnn) == 0 {
		nnn = nodeHint
	}

	nodeInfo, err := sched.nodeInfoSnapshot.GetNodeInPlacement(nnn)
	if err != nil {
		if _, err := sched.nodeInfoSnapshot.Get(nnn); err != nil {
			return nil, err
		}
		// It's not an error if NNN is in the cluster but not in the placement.
		// This can happen during the pod group placement scheduling cycle, where we simulate multiple potential placements.
		logger := klog.FromContext(ctx)
		logger.V(4).Info("Pod's nominated node is present in the cluster but not available in the current placement", "pod", klog.KObj(pod), "node", pod.Status.NominatedNodeName)
		return nil, nil
	}
	node := []fwk.NodeInfo{nodeInfo}
	feasibleNodes, err := sched.findNodesThatPassFilters(ctx, schedFramework, state, pod, &diagnosis, node, false /* doesn't matter as there's only 1 node anyway */)
	if err != nil {
		return nil, err
	}

	feasibleNodes, err = findNodesThatPassExtenders(ctx, schedFramework.Extenders(), pod, feasibleNodes, diagnosis.NodeToStatus)
	if err != nil {
		return nil, err
	}

	return feasibleNodes, nil
}

// hasScoring checks if scoring nodes is configured.
func hasScoring(fwk framework.Framework) bool {
	if fwk.HasScorePlugins() {
		return true
	}
	for _, extender := range fwk.Extenders() {
		if extender.IsPrioritizer() {
			return true
		}
	}
	return false
}

// hasExtenderFilters checks if any extenders filter nodes.
func hasExtenderFilters(fwk framework.Framework) bool {
	for _, extender := range fwk.Extenders() {
		if extender.IsFilter() {
			return true
		}
	}
	return false
}

// findNodesThatPassFilters finds the nodes that fit the filter plugins.
func (sched *Scheduler) findNodesThatPassFilters(
	ctx context.Context,
	schedFramework framework.Framework,
	state fwk.CycleState,
	pod *v1.Pod,
	diagnosis *framework.Diagnosis,
	nodes []fwk.NodeInfo,
	findAll bool) ([]fwk.NodeInfo, error) {
	numAllNodes := len(nodes)
	numNodesToFind := int32(numAllNodes)
	if !findAll {
		numNodesToFind = sched.numNodesToFind
		if !hasExtenderFilters(schedFramework) && !hasScoring(schedFramework) {
			numNodesToFind = 1
		}
	}

	// Create feasible list with enough space to avoid growing it
	// and allow assigning.
	feasibleNodes := make([]fwk.NodeInfo, numNodesToFind)

	if !schedFramework.HasFilterPlugins() {
		for i := range feasibleNodes {
			feasibleNodes[i] = nodes[(sched.nextStartNodeIndex+i)%numAllNodes]
		}
		return feasibleNodes, nil
	}

	errCh := parallelize.NewResultChannel[error]()
	var feasibleNodesLen int32
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(errors.New("findNodesThatPassFilters has completed"))

	type nodeStatus struct {
		node   string
		status *fwk.Status
	}
	result := make([]*nodeStatus, numAllNodes)
	checkNode := func(i int) {
		// We check the nodes starting from where we left off in the previous scheduling cycle,
		// this is to make sure all nodes have the same chance of being examined across pods.
		nodeInfo := nodes[(sched.nextStartNodeIndex+i)%numAllNodes]
		status := schedFramework.RunFilterPluginsWithNominatedPods(ctx, state, pod, nodeInfo)
		if status.Code() == fwk.Error {
			errCh.SendWithCancel(status.AsError(), func() {
				cancel(errors.New("some other Filter operation failed"))
			})
			return
		}
		if status.IsSuccess() {
			length := atomic.AddInt32(&feasibleNodesLen, 1)
			if length > numNodesToFind {
				cancel(errors.New("findNodesThatPassFilters has found enough nodes"))
				atomic.AddInt32(&feasibleNodesLen, -1)
			} else {
				feasibleNodes[length-1] = nodeInfo
			}
		} else {
			result[i] = &nodeStatus{node: nodeInfo.Node().Name, status: status}
		}
	}

	beginCheckNode := time.Now()
	statusCode := fwk.Success
	defer func() {
		// We record Filter extension point latency here instead of in framework.go because framework.RunFilterPlugins
		// function is called for each node, whereas we want to have an overall latency for all nodes per scheduling cycle.
		// Note that this latency also includes latency for `addNominatedPods`, which calls framework.RunPreFilterAddPod.
		metrics.FrameworkExtensionPointDuration.WithLabelValues(metrics.Filter, statusCode.String(), schedFramework.ProfileName()).Observe(metrics.SinceInSeconds(beginCheckNode))
	}()

	// Stops searching for more nodes once the configured number of feasible nodes
	// are found.
	schedFramework.Parallelizer().Until(ctx, numAllNodes, checkNode, metrics.Filter)
	feasibleNodes = feasibleNodes[:feasibleNodesLen]
	for _, item := range result {
		if item == nil {
			continue
		}
		diagnosis.NodeToStatus.Set(item.node, item.status)
		diagnosis.AddPluginStatus(item.status)
	}
	if err := errCh.Receive(); err != nil {
		statusCode = fwk.Error
		return feasibleNodes, err
	}
	return feasibleNodes, nil
}

func findNodesThatPassExtenders(ctx context.Context, extenders []fwk.Extender, pod *v1.Pod, feasibleNodes []fwk.NodeInfo, statuses *framework.NodeToStatus) ([]fwk.NodeInfo, error) {
	logger := klog.FromContext(ctx)

	// Extenders are called sequentially.
	// Nodes in original feasibleNodes can be excluded in one extender, and pass on to the next
	// extender in a decreasing manner.
	for _, extender := range extenders {
		if len(feasibleNodes) == 0 {
			break
		}
		if !extender.IsInterested(pod) {
			continue
		}

		// Status of failed nodes in failedAndUnresolvableMap will be added to <statuses>,
		// so that the scheduler framework can respect the UnschedulableAndUnresolvable status for
		// particular nodes, and this may eventually improve preemption efficiency.
		// Note: users are recommended to configure the extenders that may return UnschedulableAndUnresolvable
		// status ahead of others.
		feasibleList, failedMap, failedAndUnresolvableMap, err := extender.Filter(pod, feasibleNodes)
		if err != nil {
			if extender.IsIgnorable() {
				logger.Info("Skipping extender as it returned error and has ignorable flag set", "extender", extender, "err", err)
				continue
			}
			return nil, err
		}

		for failedNodeName, failedMsg := range failedAndUnresolvableMap {
			statuses.Set(failedNodeName, fwk.NewStatus(fwk.UnschedulableAndUnresolvable, failedMsg))
		}

		for failedNodeName, failedMsg := range failedMap {
			if _, found := failedAndUnresolvableMap[failedNodeName]; found {
				// failedAndUnresolvableMap takes precedence over failedMap
				// note that this only happens if the extender returns the node in both maps
				continue
			}
			statuses.Set(failedNodeName, fwk.NewStatus(fwk.Unschedulable, failedMsg))
		}

		feasibleNodes = feasibleList
	}
	return feasibleNodes, nil
}

// prioritizeNodes prioritizes the nodes by running the score plugins,
// which return a score for each node from the call to RunScorePlugins().
// The scores from each plugin are added together to make the score for that node, then
// any extenders are run as well.
// All scores are finally combined (added) to get the total weighted scores of all nodes
func prioritizeNodes(
	ctx context.Context,
	extenders []fwk.Extender,
	schedFramework framework.Framework,
	state fwk.CycleState,
	pod *v1.Pod,
	nodes []fwk.NodeInfo,
) ([]fwk.NodePluginScores, error) {
	logger := klog.FromContext(ctx)
	// If no priority configs are provided, then all nodes will have a score of one.
	// This is required to generate the priority list in the required format
	if len(extenders) == 0 && !schedFramework.HasScorePlugins() {
		result := make([]fwk.NodePluginScores, 0, len(nodes))
		for i := range nodes {
			result = append(result, fwk.NodePluginScores{
				Name:       nodes[i].Node().Name,
				TotalScore: 1,
			})
		}
		return result, nil
	}

	// Run PreScore plugins.
	preScoreStatus := schedFramework.RunPreScorePlugins(ctx, state, pod, nodes)
	if !preScoreStatus.IsSuccess() {
		return nil, preScoreStatus.AsError()
	}

	// Run the Score plugins.
	nodesScores, scoreStatus := schedFramework.RunScorePlugins(ctx, state, pod, nodes)
	if !scoreStatus.IsSuccess() {
		return nil, scoreStatus.AsError()
	}

	// Additional details logged at level 10 if enabled.
	loggerVTen := logger.V(10)
	if loggerVTen.Enabled() {
		for _, nodeScore := range nodesScores {
			for _, pluginScore := range nodeScore.Scores {
				loggerVTen.Info("Plugin scored node for pod", "pod", klog.KObj(pod), "plugin", pluginScore.Name, "node", nodeScore.Name, "score", pluginScore.Score)
			}
		}
	}

	if len(extenders) != 0 && nodes != nil {
		// allNodeExtendersScores has all extenders scores for all nodes.
		// It is keyed with node name.
		allNodeExtendersScores := make(map[string]*fwk.NodePluginScores, len(nodes))
		var mu sync.Mutex
		var wg sync.WaitGroup
		for i := range extenders {
			if !extenders[i].IsInterested(pod) {
				continue
			}
			wg.Add(1)
			go func(extIndex int) {
				metrics.Goroutines.WithLabelValues(metrics.PrioritizingExtender).Inc()
				defer func() {
					metrics.Goroutines.WithLabelValues(metrics.PrioritizingExtender).Dec()
					wg.Done()
				}()
				prioritizedList, weight, err := extenders[extIndex].Prioritize(pod, nodes)
				if err != nil {
					// Prioritization errors from extender can be ignored, let k8s/other extenders determine the priorities
					logger.V(5).Info("Failed to run extender's priority function. No score given by this extender.", "error", err, "pod", klog.KObj(pod), "extender", extenders[extIndex].Name())
					return
				}
				mu.Lock()
				defer mu.Unlock()
				for i := range *prioritizedList {
					nodename := (*prioritizedList)[i].Host
					score := (*prioritizedList)[i].Score
					if loggerVTen.Enabled() {
						loggerVTen.Info("Extender scored node for pod", "pod", klog.KObj(pod), "extender", extenders[extIndex].Name(), "node", nodename, "score", score)
					}

					// MaxExtenderPriority may diverge from the max priority used in the scheduler and defined by MaxNodeScore,
					// therefore we need to scale the score returned by extenders to the score range used by the scheduler.
					finalscore := score * weight * (fwk.MaxScore / extenderv1.MaxExtenderPriority)

					if allNodeExtendersScores[nodename] == nil {
						allNodeExtendersScores[nodename] = &fwk.NodePluginScores{
							Name:   nodename,
							Scores: make([]fwk.PluginScore, 0, len(extenders)),
						}
					}
					allNodeExtendersScores[nodename].Scores = append(allNodeExtendersScores[nodename].Scores, fwk.PluginScore{
						Name:  extenders[extIndex].Name(),
						Score: finalscore,
					})
					allNodeExtendersScores[nodename].TotalScore += finalscore
				}
			}(i)
		}
		// wait for all go routines to finish
		wg.Wait()
		for i := range nodesScores {
			if score, ok := allNodeExtendersScores[nodes[i].Node().Name]; ok {
				nodesScores[i].Scores = append(nodesScores[i].Scores, score.Scores...)
				nodesScores[i].TotalScore += score.TotalScore
				nodesScores[i].Randomizer = rand.Int()
			}
		}
	}

	if loggerVTen.Enabled() {
		for i := range nodesScores {
			loggerVTen.Info("Calculated node's final score for pod", "pod", klog.KObj(pod), "node", nodesScores[i].Name, "score", nodesScores[i].TotalScore)
		}
	}
	return nodesScores, nil
}

type sortedNodeScores struct {
	nodes nodeScoreHeap
}

func newSortedNodeScores(nodeScoreList []fwk.NodePluginScores) *sortedNodeScores {
	var h nodeScoreHeap = nodeScoreList
	heap.Init(&h)
	return &sortedNodeScores{nodes: h}
}

func (s *sortedNodeScores) Pop() string {
	ent := heap.Pop(&s.nodes).(fwk.NodePluginScores)
	return ent.Name
}

func (s *sortedNodeScores) Len() int {
	return s.nodes.Len()
}

// nodeScoreHeap is a heap of fwk.NodePluginScores.
type nodeScoreHeap []fwk.NodePluginScores

// nodeScoreHeap implements heap.Interface.
var _ heap.Interface = &nodeScoreHeap{}

func (h nodeScoreHeap) Len() int { return len(h) }
func (h nodeScoreHeap) Less(i, j int) bool {
	return (h[i].TotalScore > h[j].TotalScore ||
		(h[i].TotalScore == h[j].TotalScore && h[i].Randomizer > h[j].Randomizer))
}
func (h nodeScoreHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *nodeScoreHeap) Push(x interface{}) {
	*h = append(*h, x.(fwk.NodePluginScores))
}

func (h *nodeScoreHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}
