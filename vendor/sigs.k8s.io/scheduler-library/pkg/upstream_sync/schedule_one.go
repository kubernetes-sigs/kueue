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
	"sync/atomic"
	"time"

	v1 "k8s.io/api/core/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/klog/v2"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/features"
	"k8s.io/kubernetes/pkg/scheduler"
	"k8s.io/kubernetes/pkg/scheduler/backend/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework/parallelize"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/dynamicresources"
	"k8s.io/kubernetes/pkg/scheduler/metrics"
)

var clearNominatedNode = &fwk.NominatingInfo{NominatingMode: fwk.ModeOverride, NominatedNodeName: ""}

// AlgorithmResult is copied from k8s.io/kubernetes/pkg/scheduler/schedule_one.go.
// LIBRARY CHANGE: Exported to allow usage from the snapshot package.
type AlgorithmResult struct {
	// pod is the pod the result applies to.
	Pod *v1.Pod
	// scheduleResult is a scheduling algorithm result.
	ScheduleResult ScheduleResult
	// podCtx is a specific pod scheduling context used for the scheduling algorithm.
	podCtx *podSchedulingContext
	// schedulingDuration is a pod scheduling duration used for metrics recording.
	schedulingDuration time.Duration
	// requiresPreemption determines whether this pod requires a preemption to proceed or not.
	requiresPreemption bool
	// status is a scheduling algorithm status.
	Status *fwk.Status
}

// podSchedulingContext is copied from k8s.io/kubernetes/pkg/scheduler/schedule_one.go.
// It holds the precomputed data needed to handle the pod scheduling.
// Each scheduling attempt in the same pod group scheduling cycle for the same pod
// should use a new podSchedulingContext.
type podSchedulingContext struct {
	logger         klog.Logger
	state          *framework.CycleState
	podsToActivate *framework.PodsToActivate
}

// ScheduleResult wraps the upstream k8s.io/kubernetes/pkg/scheduler.ScheduleResult struct
// as the upstream ScheduleResult does not export its nominatingInfo property.
type ScheduleResult struct {
	scheduler.ScheduleResult
	nominatingInfo *fwk.NominatingInfo
}

// Scheduler wraps the upstream k8s.io/kubernetes/pkg/scheduler.Scheduler. Since the upstream
// Scheduler does not export nextStartNodeIndex or nodeInfoSnapshot, we implement
// local functions for the Scheduler receiver to duplicate functionality from the upstream.
// LIBRARY CHANGE: Created custom wrapper to track snapshot and start index state.
type Scheduler struct {
	*scheduler.Scheduler
	nextStartNodeIndex       int
	nodeInfoSnapshot         *cache.Snapshot
	percentageOfNodesToScore int32
}

// NewScheduler creates a new wrapper around the upstream Scheduler.
func NewScheduler(sched *scheduler.Scheduler, snapshot *cache.Snapshot) *Scheduler {
	return &Scheduler{
		Scheduler:                sched,
		nodeInfoSnapshot:         snapshot,
		percentageOfNodesToScore: int32(100),
	}
}

// schedulingAlgorithm is copied from k8s.io/kubernetes/pkg/scheduler/schedule_one.go
// because it is not exported upstream.
func (sched *Scheduler) schedulingAlgorithm(
	ctx context.Context,
	state fwk.CycleState,
	schedFramework framework.Framework,
	podInfo *framework.QueuedPodInfo,
	start time.Time,
) (ScheduleResult, *fwk.Status) {
	defer func() {
		metrics.SchedulingAlgorithmLatency.Observe(metrics.SinceInSeconds(start))
	}()

	pod := podInfo.Pod

	logger := klog.FromContext(ctx)
	scheduleResult, err := sched.SchedulePod(ctx, schedFramework, state, podInfo)
	localScheduleRes := ScheduleResult{
		ScheduleResult: scheduleResult,
	}

	if err != nil {
		if err == scheduler.ErrNoNodesAvailable {
			status := fwk.NewStatus(fwk.UnschedulableAndUnresolvable).WithError(err)
			return ScheduleResult{nominatingInfo: clearNominatedNode}, status
		}

		fitError, ok := err.(*framework.FitError)
		if !ok {
			logger.Error(err, "Error selecting node for pod", "pod", klog.KObj(pod))
			return ScheduleResult{nominatingInfo: clearNominatedNode}, fwk.AsStatus(err)
		}

		// SchedulePod() may have failed because the pod would not fit on any host, so we try to
		// preempt, with the expectation that the next time the pod is tried for scheduling it
		// will fit due to the preemption. It is also possible that a different pod will schedule
		// into the resources that were preempted, but this is harmless.

		if !schedFramework.HasPostFilterPlugins() {
			logger.V(3).Info("No PostFilter plugins are registered, so no preemption will be performed")
			return ScheduleResult{nominatingInfo: clearNominatedNode}, fwk.NewStatus(fwk.Unschedulable).WithError(err)
		}

		// Run PostFilter plugins to attempt to make the pod schedulable in a future scheduling cycle.
		result, status := schedFramework.RunPostFilterPlugins(ctx, state, pod, fitError.Diagnosis.NodeToStatus)
		msg := status.Message()
		fitError.Diagnosis.PostFilterMsg = msg
		if status.Code() == fwk.Error {
			utilruntime.HandleErrorWithContext(ctx, nil, "Status after running PostFilter plugins for pod", "pod", klog.KObj(pod), "status", msg)
		} else {
			logger.V(5).Info("Status after running PostFilter plugins for pod", "pod", klog.KObj(pod), "status", msg)
		}

		var nominatingInfo *fwk.NominatingInfo
		if result != nil {
			nominatingInfo = result.NominatingInfo
		}
		return ScheduleResult{nominatingInfo: nominatingInfo}, fwk.NewStatus(fwk.Unschedulable).WithError(err)
	}
	return localScheduleRes, nil
}

// FrameworkForPod is copied from k8s.io/kubernetes/pkg/scheduler/scheduler.go
// because it is not exported upstream.
// LIBRARY CHANGE: Exported to be accessible from other packages.
func (sched *Scheduler) FrameworkForPod(pod *v1.Pod) (framework.Framework, error) {
	schedulerName := pod.Spec.SchedulerName
	if schedulerName == "" {
		schedulerName = v1.DefaultSchedulerName
	}
	fwk, ok := sched.Profiles[schedulerName]
	if !ok {
		return nil, fmt.Errorf("profile not found for scheduler name %q", schedulerName)
	}
	return fwk, nil
}

// assumeAndReserve is copied from k8s.io/kubernetes/pkg/scheduler/schedule_one.go
// because it is not exported upstream.
func (sched *Scheduler) assumeAndReserve(
	ctx context.Context,
	state fwk.CycleState,
	schedFramework framework.Framework,
	podInfo *framework.QueuedPodInfo,
	scheduleResult scheduler.ScheduleResult,
) (*framework.QueuedPodInfo, *fwk.Status) {
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

// assume is copied from k8s.io/kubernetes/pkg/scheduler/schedule_one.go
// because it is not exported upstream.
func (sched *Scheduler) assume(logger klog.Logger, state fwk.CycleState, assumedPodInfo *framework.QueuedPodInfo, host string) error {
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

	if state.IsPodGroupSchedulingCycle() {
		err := sched.nodeInfoSnapshot.AssumePod(assumedPodInfo.PodInfo)
		if err != nil {
			logger.Error(err, "Scheduler snapshot AssumePod failed")
			return err
		}
	} else {
		if err := sched.Cache.AssumePod(logger, assumedPodInfo.Pod); err != nil {
			logger.Error(err, "Scheduler cache AssumePod failed")
			return err
		}
	}
	// if "assumed" is a nominated pod, we should remove it from internal cache
	if sched.SchedulingQueue != nil {
		sched.SchedulingQueue.DeleteNominatedPodIfExists(assumedPodInfo.Pod)
	}

	return nil
}

// unreserveAndForget is copied from k8s.io/kubernetes/pkg/scheduler/schedule_one.go
// because it is not exported upstream.
func (sched *Scheduler) unreserveAndForget(
	ctx context.Context,
	state fwk.CycleState,
	schedFramework framework.Framework,
	assumedPodInfo *framework.QueuedPodInfo,
	nodeName string,
) error {
	logger := klog.FromContext(ctx)

	schedFramework.RunReservePluginsUnreserve(ctx, state, assumedPodInfo.Pod, nodeName)
	if state.IsPodGroupSchedulingCycle() {
		err := sched.nodeInfoSnapshot.ForgetPod(logger, assumedPodInfo.Pod)
		if err != nil {
			return err
		}
		if assumedPodInfo.Pod.Status.NominatedNodeName != "" {
			// Assume method removed the nomination, but since we are reverting that stage for pod groups,
			// we need to revert that operation as well.
			if sched.SchedulingQueue != nil {
				nominatingInfo := &fwk.NominatingInfo{
					NominatedNodeName: assumedPodInfo.Pod.Status.NominatedNodeName,
					NominatingMode:    fwk.ModeOverride,
				}
				// AssumedPodInfo can be used here, because the whole pod object is not stored in the nominator.
				sched.SchedulingQueue.AddNominatedPod(logger, assumedPodInfo.PodInfo, nominatingInfo)
			}
		}
		return nil
	}
	return sched.Cache.ForgetPod(logger, assumedPodInfo.Pod)
}

// Filters the nodes to find the ones that fit the pod based on the framework
// filter plugins and filter extenders.
func (sched *Scheduler) FindNodesThatFitPod(ctx context.Context, schedFramework framework.Framework, state fwk.CycleState, podInfo *framework.QueuedPodInfo) ([]fwk.NodeInfo, framework.Diagnosis, string, error) {
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
		nodeHint = schedFramework.GetNodeHint(ctx, pod, podInfo.PodSignature, state, sched.CurrentCycle())
	}

	// "NominatedNodeName" can potentially be set in a previous scheduling cycle as a result of preemption.
	// This node is likely the only candidate that will fit the pod, and hence we try it first before iterating over all nodes.
	// We take the same tack for hinted nodes from the batch module.
	if len(pod.Status.NominatedNodeName) > 0 || len(nodeHint) > 0 {
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
	feasibleNodes, err := sched.findNodesThatPassFilters(ctx, schedFramework, state, pod, &diagnosis, nodes)
	// always try to update the sched.nextStartNodeIndex regardless of whether an error has occurred
	// this is helpful to make sure that all the nodes have a chance to be searched
	if len(allNodes) > 0 {
		processedNodes := len(feasibleNodes) + diagnosis.NodeToStatus.Len()
		sched.nextStartNodeIndex = (sched.nextStartNodeIndex + processedNodes) % len(allNodes)
	}
	if err != nil {
		return nil, diagnosis, "", err
	}

	feasibleNodesAfterExtender, err := findNodesThatPassExtenders(ctx, sched.Extenders, pod, feasibleNodes, diagnosis.NodeToStatus)
	if err != nil {
		return nil, diagnosis, "", err
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

	return feasibleNodesAfterExtender, diagnosis, "", nil
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
	feasibleNodes, err := sched.findNodesThatPassFilters(ctx, schedFramework, state, pod, &diagnosis, node)
	if err != nil {
		return nil, err
	}

	feasibleNodes, err = findNodesThatPassExtenders(ctx, sched.Extenders, pod, feasibleNodes, diagnosis.NodeToStatus)
	if err != nil {
		return nil, err
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

// findNodesThatPassFilters finds the nodes that fit the filter plugins.
func (sched *Scheduler) findNodesThatPassFilters(
	ctx context.Context,
	schedFramework framework.Framework,
	state fwk.CycleState,
	pod *v1.Pod,
	diagnosis *framework.Diagnosis,
	nodes []fwk.NodeInfo) ([]fwk.NodeInfo, error) {
	numAllNodes := len(nodes)
	//LIBRARY CHANGE: For simplicty ignored limitation logic from k/k
	numNodesToFind := int32(numAllNodes)

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
