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

package podset

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
)

var (
	ErrInvalidPodsetInfo   = errors.New("invalid podset infos")
	ErrInvalidPodSetUpdate = errors.New("invalid admission check PodSetUpdate")
)

type PodSetInfo struct {
	Name            kueue.PodSetReference
	Count           int32
	Annotations     map[string]string
	Labels          map[string]string
	NodeSelector    map[string]string
	Tolerations     []corev1.Toleration
	SchedulingGates []corev1.PodSchedulingGate
}

// FromAssignment returns a PodSetInfo based on the provided assignment and an error if unable
// to get any of the referenced flavors.
func FromAssignment(ctx context.Context, client client.Client, assignment *kueue.PodSetAssignment, defaultCount int32) (PodSetInfo, error) {
	processedFlvs := sets.New[kueue.ResourceFlavorReference]()
	info := PodSetInfo{
		Name:         assignment.Name,
		NodeSelector: make(map[string]string),
		Count:        ptr.Deref(assignment.Count, defaultCount),
		Labels:       make(map[string]string),
		Annotations:  make(map[string]string),
	}
	if features.Enabled(features.TopologyAwareScheduling) && assignment.TopologyAssignment != nil {
		info.Labels[kueuealpha.TASLabel] = "true"
		info.SchedulingGates = append(info.SchedulingGates, corev1.PodSchedulingGate{
			Name: kueuealpha.TopologySchedulingGate,
		})
	}
	for _, flvRef := range assignment.Flavors {
		if processedFlvs.Has(flvRef) {
			continue
		}
		// Lookup the ResourceFlavors to fetch the node affinity labels and toleration to apply on the job.
		flv := kueue.ResourceFlavor{}
		if err := client.Get(ctx, types.NamespacedName{Name: string(flvRef)}, &flv); err != nil {
			return info, err
		}
		utilmaps.Copy(&info.NodeSelector, flv.Spec.NodeLabels)
		info.Tolerations = append(info.Tolerations, flv.Spec.Tolerations...)

		processedFlvs.Insert(flvRef)
	}
	return info, nil
}

// FromUpdate returns a PodSetInfo based on the provided PodSetUpdate
func FromUpdate(update *kueue.PodSetUpdate) PodSetInfo {
	return PodSetInfo{
		Annotations:  update.Annotations,
		Labels:       update.Labels,
		NodeSelector: update.NodeSelector,
		Tolerations:  update.Tolerations,
	}
}

// FromPodSet returns a PodSetInfo based on the provided PodSet
func FromPodSet(ps *kueue.PodSet) PodSetInfo {
	return PodSetInfo{
		Name:            ps.Name,
		Count:           ps.Count,
		Annotations:     maps.Clone(ps.Template.Annotations),
		Labels:          maps.Clone(ps.Template.Labels),
		NodeSelector:    maps.Clone(ps.Template.Spec.NodeSelector),
		Tolerations:     slices.Clone(ps.Template.Spec.Tolerations),
		SchedulingGates: slices.Clone(ps.Template.Spec.SchedulingGates),
	}
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
	utilmaps.Copy(&podSetInfo.Annotations, o.Annotations)
	utilmaps.Copy(&podSetInfo.Labels, o.Labels)
	utilmaps.Copy(&podSetInfo.NodeSelector, o.NodeSelector)

	// make sure we don't duplicate tolerations
	for _, t := range o.Tolerations {
		if slices.Index(podSetInfo.Tolerations, t) == -1 {
			podSetInfo.Tolerations = append(podSetInfo.Tolerations, t)
		}
	}
	// make sure we don't duplicate schedulingGates
	for _, t := range o.SchedulingGates {
		if slices.Index(podSetInfo.SchedulingGates, t) == -1 {
			podSetInfo.SchedulingGates = append(podSetInfo.SchedulingGates, t)
		}
	}
	return nil
}

// AddOrUpdateLabel adds or updates the label identified by k with value v
// allocating a new Labels nap if nil
func (podSetInfo *PodSetInfo) AddOrUpdateLabel(k, v string) {
	if podSetInfo.Labels == nil {
		podSetInfo.Labels = map[string]string{k: v}
	} else {
		podSetInfo.Labels[k] = v
	}
}

// Merge updates or appends the replica metadata & spec fields based on PodSetInfo.
// It returns error if there is a conflict.
func Merge(meta *metav1.ObjectMeta, spec *corev1.PodSpec, info PodSetInfo) error {
	tmp := PodSetInfo{
		Annotations:     meta.Annotations,
		Labels:          meta.Labels,
		NodeSelector:    spec.NodeSelector,
		Tolerations:     spec.Tolerations,
		SchedulingGates: spec.SchedulingGates,
	}
	if err := tmp.Merge(info); err != nil {
		return err
	}
	meta.Annotations = tmp.Annotations
	meta.Labels = tmp.Labels
	spec.NodeSelector = tmp.NodeSelector
	spec.Tolerations = tmp.Tolerations
	spec.SchedulingGates = tmp.SchedulingGates
	return nil
}

// RestorePodSpec sets replica metadata and spec fields based on PodSetInfo.
// It returns true if there is any change.
func RestorePodSpec(meta *metav1.ObjectMeta, spec *corev1.PodSpec, info PodSetInfo) bool {
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
	if !slices.Equal(spec.SchedulingGates, info.SchedulingGates) {
		spec.SchedulingGates = slices.Clone(info.SchedulingGates)
		changed = true
	}
	return changed
}

func BadPodSetsInfoLenError(want, got int) error {
	return fmt.Errorf("%w: expecting %d podset, got %d", ErrInvalidPodsetInfo, got, want)
}

func BadPodSetsUpdateError(update string, err error) error {
	return fmt.Errorf("%w: conflict for %v: %v", ErrInvalidPodSetUpdate, update, err)
}

func IsPermanent(e error) bool {
	return errors.Is(e, ErrInvalidPodsetInfo) || errors.Is(e, ErrInvalidPodSetUpdate)
}
