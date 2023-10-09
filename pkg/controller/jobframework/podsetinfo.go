/*
Copyright 2023 The Kubernetes Authors.
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

package jobframework

import (
	"maps"
	"slices"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
)

type PodSetInfo struct {
	Name         string
	Count        int32
	Annotations  map[string]string
	Labels       map[string]string
	NodeSelector map[string]string
	Tolerations  []corev1.Toleration
}

func (podSetInfo *PodSetInfo) Merge(o PodSetInfo) error {
	if err := utilmaps.HaveConflict(podSetInfo.Annotations, o.Annotations); err != nil {
		return BadPodSetsUpdateError("annotations", err)
	}
	if err := utilmaps.HaveConflict(podSetInfo.Labels, o.Labels); err != nil {
		return BadPodSetsUpdateError("labels", err)
	}
	if err := utilmaps.HaveConflict(podSetInfo.NodeSelector, o.NodeSelector); err != nil {
		return BadPodSetsUpdateError("nodeSelector", err)
	}
	podSetInfo.Annotations = utilmaps.MergeKeepFirst(podSetInfo.Annotations, o.Annotations)
	podSetInfo.Labels = utilmaps.MergeKeepFirst(podSetInfo.Labels, o.Labels)
	podSetInfo.NodeSelector = utilmaps.MergeKeepFirst(podSetInfo.NodeSelector, o.NodeSelector)
	podSetInfo.Tolerations = append(podSetInfo.Tolerations, o.Tolerations...)
	return nil
}

// Merge updates or appends the replica metadata & spec fields based on PodSetInfo.
// If returns error if there is a conflict.
func Merge(meta *metav1.ObjectMeta, spec *v1.PodSpec, info PodSetInfo) error {
	if err := info.Merge(PodSetInfo{
		Annotations:  meta.Annotations,
		Labels:       meta.Labels,
		NodeSelector: spec.NodeSelector,
		Tolerations:  spec.Tolerations,
	}); err != nil {
		return err
	}
	meta.Annotations = info.Annotations
	meta.Labels = info.Labels
	spec.NodeSelector = info.NodeSelector
	spec.Tolerations = info.Tolerations
	return nil
}

// Restore sets replica metadata and spec fields based on PodSetInfo.
// It returns true if there is any change.
func Restore(meta *metav1.ObjectMeta, spec *v1.PodSpec, info PodSetInfo) bool {
	changed := false
	if !maps.Equal(meta.Annotations, info.Annotations) {
		meta.Annotations = maps.Clone(info.Annotations)
		changed = true
	}
	if !maps.Equal(meta.Labels, info.Labels) {
		meta.Labels = maps.Clone(info.Labels)
		changed = true
	}
	if !maps.Equal(spec.NodeSelector, info.NodeSelector) {
		spec.NodeSelector = maps.Clone(info.NodeSelector)
		changed = true
	}
	if !slices.Equal(spec.Tolerations, info.Tolerations) {
		spec.Tolerations = slices.Clone(info.Tolerations)
		changed = true
	}
	return changed
}
