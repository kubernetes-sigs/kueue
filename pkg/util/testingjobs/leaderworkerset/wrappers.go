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

package leaderworkerset

import (
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
)

// LeaderWorkerSetWrapper wraps a LeaderWorkerSet.
type LeaderWorkerSetWrapper struct {
	leaderworkersetv1.LeaderWorkerSet
}

// MakeLeaderWorkerSet creates a wrapper for a LeaderWorkerSet with a single container.
func MakeLeaderWorkerSet(name, ns string) *LeaderWorkerSetWrapper {
	return &LeaderWorkerSetWrapper{leaderworkersetv1.LeaderWorkerSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Annotations: make(map[string]string, 1),
		},
		Spec: leaderworkersetv1.LeaderWorkerSetSpec{
			Replicas:      ptr.To[int32](1),
			StartupPolicy: leaderworkersetv1.LeaderReadyStartupPolicy,
			LeaderWorkerTemplate: leaderworkersetv1.LeaderWorkerTemplate{
				WorkerTemplate: corev1.PodTemplateSpec{
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
				Size: ptr.To[int32](1),
			},
		},
	}}
}

// Obj returns the inner LeaderWorkerSet.
func (lws *LeaderWorkerSetWrapper) Obj() *leaderworkersetv1.LeaderWorkerSet {
	return &lws.LeaderWorkerSet
}

// Label sets the label of the LeaderWorkerSet
func (lws *LeaderWorkerSetWrapper) Label(k, v string) *LeaderWorkerSetWrapper {
	if lws.Labels == nil {
		lws.Labels = make(map[string]string)
	}
	lws.Labels[k] = v
	return lws
}

// Queue updates the queue name of the LeaderWorkerSet
func (lws *LeaderWorkerSetWrapper) Queue(q string) *LeaderWorkerSetWrapper {
	return lws.Label(constants.QueueLabel, q)
}

// Name updated the name of the LeaderWorkerSet
func (lws *LeaderWorkerSetWrapper) Name(n string) *LeaderWorkerSetWrapper {
	lws.ObjectMeta.Name = n
	return lws
}

// WorkerTemplateSpecLabel sets the label of the pod template spec of the LeaderWorkerSet
func (lws *LeaderWorkerSetWrapper) WorkerTemplateSpecLabel(k, v string) *LeaderWorkerSetWrapper {
	if lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Labels == nil {
		lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Labels = make(map[string]string, 1)
	}
	lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Labels[k] = v
	return lws
}

// WorkerTemplateSpecAnnotation sets the annotation of the pod template spec of the LeaderWorkerSet
func (lws *LeaderWorkerSetWrapper) WorkerTemplateSpecAnnotation(k, v string) *LeaderWorkerSetWrapper {
	if lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations == nil {
		lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations = make(map[string]string, 1)
	}
	lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations[k] = v
	return lws
}

// WorkerTemplateSpecQueue updates the queue name of the pod template spec of the LeaderWorkerSet
func (lws *LeaderWorkerSetWrapper) WorkerTemplateSpecQueue(q string) *LeaderWorkerSetWrapper {
	return lws.WorkerTemplateSpecLabel(constants.QueueLabel, q)
}

// Replicas sets the number of replicas of the LeaderWorkerSet.
func (lws *LeaderWorkerSetWrapper) Replicas(n int32) *LeaderWorkerSetWrapper {
	lws.Spec.Replicas = ptr.To[int32](n)
	return lws
}

// Size sets the size of the LeaderWorkerSet.
func (lws *LeaderWorkerSetWrapper) Size(n int32) *LeaderWorkerSetWrapper {
	lws.Spec.LeaderWorkerTemplate.Size = ptr.To[int32](n)
	return lws
}

func (lws *LeaderWorkerSetWrapper) WorkerTemplateSpecPodGroupNameLabel(
	ownerName string, ownerUID types.UID, ownerGVK schema.GroupVersionKind,
) *LeaderWorkerSetWrapper {
	gvk := jobframework.GetWorkloadNameForOwnerWithGVK(ownerName, ownerUID, ownerGVK)
	return lws.WorkerTemplateSpecLabel(pod.GroupNameLabel, gvk)
}

func (lws *LeaderWorkerSetWrapper) WorkerTemplateSpecPodGroupTotalCountAnnotation(replicas int32) *LeaderWorkerSetWrapper {
	return lws.WorkerTemplateSpecAnnotation(pod.GroupTotalCountAnnotation, fmt.Sprint(replicas))
}

func (lws *LeaderWorkerSetWrapper) WorkerTemplateSpecPodGroupFastAdmissionAnnotation(enabled bool) *LeaderWorkerSetWrapper {
	return lws.WorkerTemplateSpecAnnotation(pod.GroupFastAdmissionAnnotation, strconv.FormatBool(enabled))
}

func (lws *LeaderWorkerSetWrapper) Image(image string, args []string) *LeaderWorkerSetWrapper {
	lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Image = image
	lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Args = args
	return lws
}

// Request adds a resource request to the default container.
func (lws *LeaderWorkerSetWrapper) Request(r corev1.ResourceName, v string) *LeaderWorkerSetWrapper {
	if lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Resources.Requests == nil {
		lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Resources.Requests = corev1.ResourceList{}
	}
	lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Resources.Requests[r] = resource.MustParse(v)
	return lws
}

// Limit adds a resource limit to the default container.
func (lws *LeaderWorkerSetWrapper) Limit(r corev1.ResourceName, v string) *LeaderWorkerSetWrapper {
	if lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Resources.Limits == nil {
		lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Resources.Limits = corev1.ResourceList{}
	}
	lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Resources.Limits[r] = resource.MustParse(v)
	return lws
}

// LeaderTemplate sets the leader template of the LeaderWorkerSet.
func (lws *LeaderWorkerSetWrapper) LeaderTemplate(leader corev1.PodTemplateSpec) *LeaderWorkerSetWrapper {
	lws.Spec.LeaderWorkerTemplate.LeaderTemplate = &leader
	return lws
}
