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

package statefulset

import (
	"fmt"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
)

// StatefulSetWrapper wraps a StatefulSet.
type StatefulSetWrapper struct {
	appsv1.StatefulSet
}

// MakeStatefulSet creates a wrapper for a StatefulSet with a single container.
func MakeStatefulSet(name, ns string) *StatefulSetWrapper {
	podLabels := map[string]string{
		"app": fmt.Sprintf("%s-pod", name),
	}
	return &StatefulSetWrapper{appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Annotations: make(map[string]string, 1),
		},
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: podLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: podLabels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:      "c",
							Image:     "pause",
							Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
						},
					},
					NodeSelector: map[string]string{},
				},
			},
		},
	}}
}

// Obj returns the inner StatefulSet.
func (ss *StatefulSetWrapper) Obj() *appsv1.StatefulSet {
	return &ss.StatefulSet
}

// Label sets the label of the StatefulSet
func (ss *StatefulSetWrapper) Label(k, v string) *StatefulSetWrapper {
	if ss.Labels == nil {
		ss.Labels = make(map[string]string)
	}
	ss.Labels[k] = v
	return ss
}

// Queue updates the queue name of the StatefulSet
func (ss *StatefulSetWrapper) Queue(q string) *StatefulSetWrapper {
	return ss.Label(constants.QueueLabel, q)
}

// Name updated the name of the StatefulSet
func (ss *StatefulSetWrapper) Name(n string) *StatefulSetWrapper {
	ss.ObjectMeta.Name = n
	return ss
}

// PodTemplateSpecLabel sets the label of the pod template spec of the StatefulSet
func (ss *StatefulSetWrapper) PodTemplateSpecLabel(k, v string) *StatefulSetWrapper {
	if ss.Spec.Template.Labels == nil {
		ss.Spec.Template.Labels = make(map[string]string, 1)
	}
	ss.Spec.Template.Labels[k] = v
	return ss
}

// PodTemplateSpecAnnotation sets the annotation of the pod template spec of the StatefulSet
func (ss *StatefulSetWrapper) PodTemplateSpecAnnotation(k, v string) *StatefulSetWrapper {
	if ss.Spec.Template.Annotations == nil {
		ss.Spec.Template.Annotations = make(map[string]string, 1)
	}
	ss.Spec.Template.Annotations[k] = v
	return ss
}

// PodTemplateSpecQueue updates the queue name of the pod template spec of the StatefulSet
func (ss *StatefulSetWrapper) PodTemplateSpecQueue(q string) *StatefulSetWrapper {
	return ss.PodTemplateSpecLabel(constants.QueueLabel, q)
}

func (ss *StatefulSetWrapper) Replicas(r int32) *StatefulSetWrapper {
	ss.Spec.Replicas = &r
	return ss
}

func (ss *StatefulSetWrapper) PodTemplateSpecPodGroupNameLabel(
	ownerName string, ownerUID types.UID, ownerGVK schema.GroupVersionKind,
) *StatefulSetWrapper {
	gvk := jobframework.GetWorkloadNameForOwnerWithGVK(ownerName, ownerUID, ownerGVK)
	return ss.PodTemplateSpecLabel(pod.GroupNameLabel, gvk)
}

func (ss *StatefulSetWrapper) PodTemplateSpecPodGroupTotalCountAnnotation(replicas int32) *StatefulSetWrapper {
	return ss.PodTemplateSpecAnnotation(pod.GroupTotalCountAnnotation, fmt.Sprint(replicas))
}

func (ss *StatefulSetWrapper) PodTemplateSpecPodGroupFastAdmissionAnnotation(enabled bool) *StatefulSetWrapper {
	return ss.PodTemplateSpecAnnotation(pod.GroupFastAdmissionAnnotation, strconv.FormatBool(enabled))
}

func (ss *StatefulSetWrapper) PodTemplateSpecPodGroupServingAnnotation(enabled bool) *StatefulSetWrapper {
	return ss.PodTemplateSpecAnnotation(pod.GroupServingAnnotation, strconv.FormatBool(enabled))
}

func (ss *StatefulSetWrapper) Image(image string, args []string) *StatefulSetWrapper {
	ss.Spec.Template.Spec.Containers[0].Image = image
	ss.Spec.Template.Spec.Containers[0].Args = args
	return ss
}

// Request adds a resource request to the default container.
func (ss *StatefulSetWrapper) Request(r corev1.ResourceName, v string) *StatefulSetWrapper {
	if ss.Spec.Template.Spec.Containers[0].Resources.Requests == nil {
		ss.Spec.Template.Spec.Containers[0].Resources.Requests = corev1.ResourceList{}
	}
	ss.Spec.Template.Spec.Containers[0].Resources.Requests[r] = resource.MustParse(v)
	return ss
}

// Limit adds a resource limit to the default container.
func (ss *StatefulSetWrapper) Limit(r corev1.ResourceName, v string) *StatefulSetWrapper {
	if ss.Spec.Template.Spec.Containers[0].Resources.Limits == nil {
		ss.Spec.Template.Spec.Containers[0].Resources.Limits = corev1.ResourceList{}
	}
	ss.Spec.Template.Spec.Containers[0].Resources.Limits[r] = resource.MustParse(v)
	return ss
}
