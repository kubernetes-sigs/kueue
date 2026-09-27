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
	"slices"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
)

// UngatedPodNames returns the sorted names of Pods in the namespace that don't
// have the specified scheduling gate.
func UngatedPodNames(ctx context.Context, k8sClient client.Client, namespace, gateName string) ([]string, error) {
	pods := &corev1.PodList{}
	if err := k8sClient.List(ctx, pods, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	ungated := make([]string, 0, len(pods.Items))
	for i := range pods.Items {
		if !utilpod.HasGate(&pods.Items[i], gateName) {
			ungated = append(ungated, pods.Items[i].Name)
		}
	}
	slices.Sort(ungated)
	return ungated, nil
}
