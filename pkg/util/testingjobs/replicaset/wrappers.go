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

package replicaset

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/kueue/pkg/controller/constants"
)

// ReplicaSetWrapper wraps a ReplicaSet.
type ReplicaSetWrapper struct {
	appsv1.ReplicaSet
}

// MakeReplicaSet creates a wrapper for a ReplicaSet with a single container.
func MakeReplicaSet(name, ns string) *ReplicaSetWrapper {
	podLabels := map[string]string{
		"app": fmt.Sprintf("%s-pod", name),
	}
	return &ReplicaSetWrapper{appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Annotations: make(map[string]string, 1),
		},
		Spec: appsv1.ReplicaSetSpec{
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

// Obj returns the inner ReplicaSet.
func (d *ReplicaSetWrapper) Obj() *appsv1.ReplicaSet {
	return &d.ReplicaSet
}

// Label sets the label of the ReplicaSet
func (d *ReplicaSetWrapper) Label(k, v string) *ReplicaSetWrapper {
	if d.Labels == nil {
		d.Labels = make(map[string]string)
	}
	d.Labels[k] = v
	return d
}

// Queue updates the queue name of the ReplicaSet
func (d *ReplicaSetWrapper) Queue(q string) *ReplicaSetWrapper {
	return d.Label(constants.QueueLabel, q)
}

// Name updated the name of the ReplicaSet
func (d *ReplicaSetWrapper) Name(n string) *ReplicaSetWrapper {
	d.ObjectMeta.Name = n
	return d
}

// Image sets an image to the default container.
func (d *ReplicaSetWrapper) Image(image string, args []string) *ReplicaSetWrapper {
	d.Spec.Template.Spec.Containers[0].Image = image
	d.Spec.Template.Spec.Containers[0].Args = args
	return d
}

// Request adds a resource request to the default container.
func (d *ReplicaSetWrapper) Request(r corev1.ResourceName, v string) *ReplicaSetWrapper {
	if d.Spec.Template.Spec.Containers[0].Resources.Requests == nil {
		d.Spec.Template.Spec.Containers[0].Resources.Requests = corev1.ResourceList{}
	}
	d.Spec.Template.Spec.Containers[0].Resources.Requests[r] = resource.MustParse(v)
	return d
}

// Replicas updated the replicas of the ReplicaSet
func (d *ReplicaSetWrapper) Replicas(replicas int32) *ReplicaSetWrapper {
	d.Spec.Replicas = &replicas
	return d
}

// ReadyReplicas updated the readyReplicas of the ReplicaSet
func (d *ReplicaSetWrapper) ReadyReplicas(readyReplicas int32) *ReplicaSetWrapper {
	d.Status.ReadyReplicas = readyReplicas
	return d
}

// PodTemplateSpecLabel sets the label of the pod template spec of the ReplicaSet
func (d *ReplicaSetWrapper) PodTemplateSpecLabel(k, v string) *ReplicaSetWrapper {
	if d.Spec.Template.Labels == nil {
		d.Spec.Template.Labels = make(map[string]string, 1)
	}
	d.Spec.Template.Labels[k] = v
	return d
}

// PodTemplateSpecQueue updates the queue name of the pod template spec of the ReplicaSet
func (d *ReplicaSetWrapper) PodTemplateSpecQueue(q string) *ReplicaSetWrapper {
	return d.PodTemplateSpecLabel(constants.QueueLabel, q)
}
