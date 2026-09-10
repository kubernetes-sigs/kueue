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

package workloadslicing

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
)

// nameForPod returns the annotated Workload name, preferring a present slice-chain annotation.
func nameForPod(pod *corev1.Pod) string {
	if name, found := pod.Annotations[kueue.WorkloadSliceNameAnnotation]; found {
		return name
	}
	return pod.Annotations[kueue.WorkloadAnnotation]
}

// KeyForPod returns the annotated Workload key, or nil if the name is empty.
func KeyForPod(pod *corev1.Pod) *types.NamespacedName {
	if name := nameForPod(pod); name != "" {
		return &types.NamespacedName{Namespace: pod.Namespace, Name: name}
	}
	return nil
}

// ListPodsForWorkloadSlice lists pods belonging to a workload slice by querying
// the WorkloadSliceNameKey index. Additional list options (e.g., label selectors)
// can be passed for filtering. Returns pointers to avoid copying large Pod structs.
func ListPodsForWorkloadSlice(ctx context.Context, c client.Client, namespace, workloadSliceName string, opts ...client.ListOption) ([]*corev1.Pod, error) {
	var podList corev1.PodList
	listOpts := append([]client.ListOption{
		client.InNamespace(namespace),
		client.MatchingFields{indexer.WorkloadSliceNameKey: workloadSliceName},
	}, opts...)
	if err := c.List(ctx, &podList, listOpts...); err != nil {
		return nil, err
	}
	result := make([]*corev1.Pod, len(podList.Items))
	for i := range podList.Items {
		result[i] = &podList.Items[i]
	}
	return result, nil
}
