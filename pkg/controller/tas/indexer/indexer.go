/*
Copyright 2024 The Kubernetes Authors.

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

package indexer

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

const (
	WorkloadNameKey = "metadata.workload"
	ReadyNode       = "metadata.ready"
)

func indexPodWorkload(o client.Object) []string {
	pod, ok := o.(*corev1.Pod)
	if !ok {
		return nil
	}
	value, found := pod.Annotations[kueuealpha.WorkloadAnnotation]
	if !found {
		return nil
	}
	return []string{value}
}

func indexReadyNode(o client.Object) []string {
	node, ok := o.(*corev1.Node)
	if !ok || len(node.Status.Conditions) == 0 {
		return nil
	}

	if !utiltas.IsNodeStatusConditionTrue(node.Status.Conditions, corev1.NodeReady, corev1.ConditionTrue) {
		return nil
	}

	return []string{"true"}
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	if err := indexer.IndexField(ctx, &corev1.Pod{}, WorkloadNameKey, indexPodWorkload); err != nil {
		return fmt.Errorf("setting index pod workload: %w", err)
	}

	if err := indexer.IndexField(ctx, &corev1.Node{}, ReadyNode, indexReadyNode); err != nil {
		return fmt.Errorf("setting index node ready: %w", err)
	}

	return nil
}
