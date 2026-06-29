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
	"k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/backend/cache"
	fwk "k8s.io/kubernetes/pkg/scheduler/framework"
	upstreamsync "sigs.k8s.io/scheduler-library/pkg/upstream_sync"
)

// addPodToNode adds a new pod to the specific node and returns the corresponding revert function
func addPodToNode(ctx context.Context, schedulerSnapshot *cache.Snapshot, pod *v1.Pod, nodeName string) (func(), error) {
	nodeInfo, err := schedulerSnapshot.Get(nodeName)
	if err != nil {
		return nil, fmt.Errorf("failed to get node info: %w", err)
	}

	podInfo, err := fwk.NewPodInfo(pod.DeepCopy())
	if err != nil {
		return nil, fmt.Errorf("failed to create pod info: %w", err)

	}

	nodeInfo.AddPodInfo(podInfo)

	revertFn := func() {
		if _, err := removePodFromNode(ctx, schedulerSnapshot, pod, nodeName); err != nil {
			logger := klog.FromContext(ctx)
			logger.Error(err, "revert addPodToNode failed")
		}
	}

	return revertFn, nil
}

// removePodFromNode removes a pod from a specific node and returns the corresponding revert function.
func removePodFromNode(ctx context.Context, schedulerSnapshot *cache.Snapshot, pod *v1.Pod, nodeName string) (func(), error) {
	logger := klog.FromContext(ctx)
	nodeInfo, err := schedulerSnapshot.Get(nodeName)
	if err != nil {
		return nil, fmt.Errorf("failed to get node info: %w", err)
	}

	if err := nodeInfo.RemovePod(logger, pod); err != nil {
		return nil, fmt.Errorf("failed to remove pod: %w", err)
	}

	revertFn := func() {
		if _, err := addPodToNode(ctx, schedulerSnapshot, pod, nodeName); err != nil {
			logger.Error(err, "revert removePodFromNode failed")
		}
	}

	return revertFn, nil
}

// createPodFromTemplate creates a new v1.Pod based on the provided pod template spec and index.
// The pod's name is constructed as "<template-name>-<index>", and it is assigned a random UUID.
func createPodFromTemplate(template *v1.PodTemplateSpec, index int) *v1.Pod {
	podNamePrefix := template.Name
	if podNamePrefix == "" {
		podNamePrefix = "templated-pod"
	}
	ns := template.Namespace
	if ns == "" {
		ns = "default"
	}
	uid := types.UID(uuid.New().String())

	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d-%s", podNamePrefix, index, uid),
			Namespace: ns,
			UID:       uid,
			Labels:    template.Labels,
		},
		Spec: template.Spec,
	}
}

func withPlacement(candidates []string, nodeInfoSnapshot *cache.Snapshot, fn func() error) error {
	nodes := make([]framework.NodeInfo, 0, len(candidates))
	for _, name := range candidates {
		ni, err := nodeInfoSnapshot.NodeInfos().Get(name)
		if err != nil {
			return fmt.Errorf("node %s not in snapshot: %w", name, err)
		}
		nodes = append(nodes, ni)
	}
	if err := nodeInfoSnapshot.AssumePlacement(&framework.Placement{Nodes: nodes}); err != nil {
		return err
	}
	defer nodeInfoSnapshot.ForgetPlacement()
	return fn()
}

// ScheduleOnePod simulates a single scheduling cycle for a pod against the given candidate nodes.
func scheduleOnePod(ctx context.Context, sched *upstreamsync.Scheduler, nodeInfoSnapshot *cache.Snapshot, pod *v1.Pod, candidateNodes []string) (*upstreamsync.AlgorithmResult, func(), error) {
	framework, err := sched.FrameworkForPod(pod)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get framework for pod %s: %w", klog.KObj(pod), err)
	}

	cycleState := fwk.NewCycleState()
	cycleState.SetPodGroupSchedulingCycle(fwk.NewCycleState())
	podInfo, err := fwk.NewPodInfo(pod)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create pod info for pod %s: %w", klog.KObj(pod), err)
	}

	var algRes upstreamsync.AlgorithmResult
	var revertFn func()

	err = withPlacement(candidateNodes, nodeInfoSnapshot, func() error {
		queuedPodInfo := &fwk.QueuedPodInfo{PodInfo: podInfo}
		queuedPodGroupInfo := &fwk.QueuedPodGroupInfo{
			PodGroupInfo: &fwk.PodGroupInfo{
				Namespace:       pod.Namespace,
				Name:            pod.Name + "-dummy-podgroup",
				UnscheduledPods: []*v1.Pod{pod},
			},
			QueuedPodInfos: []*fwk.QueuedPodInfo{
				queuedPodInfo,
			},
		}
		algRes, revertFn = sched.PodGroupPodSchedulingAlgorithm(ctx, framework, cycleState, queuedPodGroupInfo, queuedPodInfo, upstreamsync.RunWithoutPostFilters)
		return nil
	})

	if err != nil {
		return nil, nil, fmt.Errorf("failed to simulate scheduling for pod %s: %w", klog.KObj(pod), err)
	}

	if !algRes.Status.IsSuccess() {
		return &algRes, nil, nil
	}

	pod.Spec.NodeName = algRes.ScheduleResult.SuggestedHost
	return &algRes, revertFn, nil
}
