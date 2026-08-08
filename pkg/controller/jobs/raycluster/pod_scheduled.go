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

package raycluster

import (
	"context"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	rayutils "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
)

// PodsScheduledForRayCluster reports whether each Ray PodSet has enough scheduled pods.
// When rayClusterName is empty, returns (false, nil) because pods cannot be verified yet.
func PodsScheduledForRayCluster(ctx context.Context, c client.Client, namespace, rayClusterName string, podSets []kueue.PodSet) (bool, error) {
	if rayClusterName == "" {
		return false, nil
	}
	for _, ps := range podSets {
		matchLabels := map[string]string{
			rayutils.RayClusterLabelKey: rayClusterName,
		}
		if string(ps.Name) == headGroupPodSetName {
			matchLabels[rayutils.RayNodeTypeLabelKey] = string(rayv1.HeadNode)
		} else {
			matchLabels[rayutils.RayNodeTypeLabelKey] = string(rayv1.WorkerNode)
			matchLabels[rayutils.RayNodeGroupLabelKey] = string(ps.Name)
		}
		scheduled, err := jobframework.PodsScheduledByLabels(ctx, c, namespace, matchLabels, int(ps.Count))
		if err != nil || !scheduled {
			return scheduled, err
		}
	}
	return true, nil
}
