/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package util

import (
	"context"
	"fmt"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	kuberayutils "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetRayClusterHeadPod returns the only head Pod associated with the RayCluster.
func GetRayClusterHeadPod(ctx context.Context, c client.Client, rayClusterKey client.ObjectKey) (*corev1.Pod, error) {
	pods := &corev1.PodList{}
	if err := c.List(ctx, pods,
		client.InNamespace(rayClusterKey.Namespace),
		client.MatchingLabels{
			kuberayutils.RayClusterLabelKey:  rayClusterKey.Name,
			kuberayutils.RayNodeTypeLabelKey: string(rayv1.HeadNode),
		},
	); err != nil {
		return nil, err
	}
	if len(pods.Items) != 1 {
		return nil, fmt.Errorf("expected exactly one head Pod for RayCluster %s, got %d", rayClusterKey, len(pods.Items))
	}
	return &pods.Items[0], nil
}

// GetRayClusterWorkerPods returns the worker Pods associated with the RayCluster
// whose phase matches the provided phase. If phase is empty, it returns all
// associated worker Pods.
func GetRayClusterWorkerPods(ctx context.Context, c client.Client, rayClusterKey client.ObjectKey, phase corev1.PodPhase) ([]corev1.Pod, error) {
	pods := &corev1.PodList{}
	if err := c.List(ctx, pods,
		client.InNamespace(rayClusterKey.Namespace),
		client.MatchingLabels{
			kuberayutils.RayClusterLabelKey:  rayClusterKey.Name,
			kuberayutils.RayNodeTypeLabelKey: string(rayv1.WorkerNode),
		},
	); err != nil {
		return nil, err
	}
	if phase == "" {
		return pods.Items, nil
	}
	filteredPods := make([]corev1.Pod, 0, len(pods.Items))
	for _, pod := range pods.Items {
		if pod.Status.Phase == phase {
			filteredPods = append(filteredPods, pod)
		}
	}
	return filteredPods, nil
}
