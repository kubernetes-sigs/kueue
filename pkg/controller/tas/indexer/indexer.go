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

package indexer

import (
	"context"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	TASKey                        = "metadata.tas"
	WorkloadNameKey               = "metadata.workload"
	PodNodeSelectorHostnameKey    = "spec.nodeSelector.hostname"
	PodNodeNameKey                = "spec.nodeName"
	ResourceFlavorTopologyNameKey = "spec.topologyName"
	AdmittedWorkloadNodesKey      = "metadata.admittedWorkloadNodes"
)

func indexPodTAS(o client.Object) []string {
	pod, ok := o.(*corev1.Pod)
	if !ok {
		return nil
	}
	return []string{strconv.FormatBool(utiltas.IsTAS(pod))}
}

func indexPodWorkload(o client.Object) []string {
	pod, ok := o.(*corev1.Pod)
	if !ok {
		return nil
	}
	value, found := pod.Annotations[kueue.WorkloadAnnotation]
	if !found {
		return nil
	}
	return []string{value}
}

func indexPodNodeSelectorHostname(o client.Object) []string {
	pod, ok := o.(*corev1.Pod)
	if !ok || !utiltas.IsTAS(pod) {
		return nil
	}
	if pod.Spec.NodeSelector != nil {
		if nodeName, ok := pod.Spec.NodeSelector[corev1.LabelHostname]; ok {
			return []string{nodeName}
		}
	}
	return nil
}

func indexPodNodeName(o client.Object) []string {
	pod, ok := o.(*corev1.Pod)
	if !ok {
		return nil
	}
	return []string{pod.Spec.NodeName}
}

func indexResourceFlavorTopologyName(o client.Object) []string {
	flavor, ok := o.(*kueue.ResourceFlavor)
	if !ok || flavor.Spec.TopologyName == nil {
		return nil
	}
	return []string{string(*flavor.Spec.TopologyName)}
}

func indexAdmittedWorkloadNodes(o client.Object) []string {
	wl, ok := o.(*kueue.Workload)
	if !ok || workload.IsFinished(wl) || workload.IsEvicted(wl) {
		return nil
	}

	return workload.TASAssignedNodeNames(wl)
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	if err := indexer.IndexField(ctx, &corev1.Pod{}, TASKey, indexPodTAS); err != nil {
		return fmt.Errorf("setting index pod TAS: %w", err)
	}

	if err := indexer.IndexField(ctx, &corev1.Pod{}, WorkloadNameKey, indexPodWorkload); err != nil {
		return fmt.Errorf("setting index pod workload: %w", err)
	}

	if err := indexer.IndexField(ctx, &corev1.Pod{}, PodNodeSelectorHostnameKey, indexPodNodeSelectorHostname); err != nil {
		return fmt.Errorf("setting index pod node selector hostname: %w", err)
	}
	if err := indexer.IndexField(ctx, &corev1.Pod{}, PodNodeNameKey, indexPodNodeName); err != nil {
		return fmt.Errorf("setting index on %s for Pod: %w", PodNodeNameKey, err)
	}

	if err := indexer.IndexField(ctx, &kueue.ResourceFlavor{}, ResourceFlavorTopologyNameKey, indexResourceFlavorTopologyName); err != nil {
		return fmt.Errorf("setting index resource flavor topology name: %w", err)
	}
	if err := indexer.IndexField(ctx, &kueue.Workload{}, AdmittedWorkloadNodesKey, indexAdmittedWorkloadNodes); err != nil {
		return fmt.Errorf("setting index admitted workload nodes: %w", err)
	}
	return nil
}
