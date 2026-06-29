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
	"math/rand"
	"time"

	v1 "k8s.io/api/core/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

const (
	// Percentage of plugin metrics to be sampled.
	pluginMetricsSamplePercent = 10
)

// podGroupPostFilterMode defines how the pod group algorithm should run post filters plugins.
type podGroupPostFilterMode int

const (
	// The pod group algorithm should try to run all post filters in pod-by-pod cycle.
	runAllPostFilters podGroupPostFilterMode = iota
	// The pod group algorithm should not try post filter at all. This can be used
	// by workload aware preemption that tries to check if after removing some
	// pods the pod group can be scheduled.
	RunWithoutPostFilters
)

// podGroupPodSchedulingAlgorithm runs a scheduling algorithm for individual pod from a pod group.
// It returns the algorithm result together with the revert function.
func (sched *Scheduler) PodGroupPodSchedulingAlgorithm(ctx context.Context, schedFwk framework.Framework, placementCycleState *framework.CycleState, podGroupInfo *framework.QueuedPodGroupInfo, podInfo *framework.QueuedPodInfo, postFilterMode podGroupPostFilterMode) (AlgorithmResult, func()) {
	pod := podInfo.Pod
	podCtx := initPodSchedulingContext(ctx, pod, placementCycleState, postFilterMode)
	logger := podCtx.logger
	ctx = klog.NewContext(ctx, logger)
	start := time.Now()

	logger.V(4).Info("Attempting to schedule a pod belonging to a pod group", "podGroup", klog.KObj(podGroupInfo), "pod", klog.KObj(pod))

	requiresPreemption := false
	scheduleResult, status := sched.schedulingAlgorithm(ctx, podCtx.state, schedFwk, podInfo, start)
	if !status.IsSuccess() {
		if scheduleResult.nominatingInfo != nil && scheduleResult.nominatingInfo.NominatedNodeName != "" {
			// If the NominatedNodeName is set, the preemption is required.
			// Continue with assuming and reserving, because the subsequent pods from this group
			// have to see this one as already scheduled on its nominated place.
			// Set SuggestedHost to NominatedNodeName to handle the pod similarly to one that is feasible.
			scheduleResult.SuggestedHost = scheduleResult.nominatingInfo.NominatedNodeName
			requiresPreemption = true
		} else {
			// In case of pod being just unschedulable or having an error, just return now.
			return AlgorithmResult{
				Pod:                pod,
				ScheduleResult:     scheduleResult,
				podCtx:             podCtx,
				schedulingDuration: time.Since(start),
				Status:             status,
			}, nil
		}
	}
	assumedPodInfo, assumeStatus := sched.assumeAndReserve(ctx, podCtx.state, schedFwk, podInfo, scheduleResult.ScheduleResult)
	if !assumeStatus.IsSuccess() {
		return AlgorithmResult{
			Pod:                pod,
			ScheduleResult:     ScheduleResult{nominatingInfo: clearNominatedNode},
			podCtx:             podCtx,
			schedulingDuration: time.Since(start),
			Status:             assumeStatus,
		}, nil
	}

	revertFn := func() {
		err := sched.unreserveAndForget(ctx, podCtx.state, schedFwk, assumedPodInfo, scheduleResult.SuggestedHost)
		if err != nil {
			utilruntime.HandleErrorWithContext(ctx, err, "ForgetPod failed")
		}
	}

	return AlgorithmResult{
		Pod:                pod,
		ScheduleResult:     scheduleResult,
		podCtx:             podCtx,
		schedulingDuration: time.Since(start),
		Status:             status,
		requiresPreemption: requiresPreemption,
	}, revertFn
}

// initPodSchedulingContext initializes the scheduling context of a single pod for pod group scheduling cycle.
func initPodSchedulingContext(ctx context.Context, pod *v1.Pod, placementCycleState *framework.CycleState, postFilterMode podGroupPostFilterMode) *podSchedulingContext {
	logger := klog.FromContext(ctx)
	// TODO(knelasevero): Remove duplicated keys from log entry calls
	// When contextualized logging hits GA
	// https://github.com/kubernetes/kubernetes/issues/111672
	logger = klog.LoggerWithValues(logger, "pod", klog.KObj(pod))

	// Synchronously attempt to find a fit for the pod.
	state := framework.NewCycleState()
	// For the sake of performance, scheduler does not measure and export the scheduler_plugin_execution_duration metric
	// for every plugin execution in each scheduling cycle. Instead it samples a portion of scheduling cycles - percentage
	// determined by pluginMetricsSamplePercent. The line below helps to randomly pick appropriate scheduling cycles.
	state.SetRecordPluginMetrics(rand.Intn(100) < pluginMetricsSamplePercent)

	// Initialize an empty podsToActivate struct, which will be filled up by plugins or stay empty.
	podsToActivate := framework.NewPodsToActivate()
	state.Write(framework.PodsToActivateKey, podsToActivate)

	podGroupCycleState := placementCycleState.GetPodGroupSchedulingCycle()
	// Marks this cycle as a pod group scheduling cycle.
	state.SetPodGroupSchedulingCycle(podGroupCycleState)

	// Skip post filters if requested.
	switch postFilterMode {
	case RunWithoutPostFilters:
		state.SetSkipAllPostFilterPlugins(true)
	case runAllPostFilters:
		// Default podCtx state will run all post filters.
	}

	return &podSchedulingContext{
		logger:         logger,
		state:          state,
		podsToActivate: podsToActivate,
	}
}
