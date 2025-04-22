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

package leaderworkerset

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
)

// LeaderWorkerSetWrapper wraps a LeaderWorkerSet.
type LeaderWorkerSetWrapper struct {
	leaderworkersetv1.LeaderWorkerSet
}

// MakeLeaderWorkerSet creates a wrapper for a LeaderWorkerSet with a single container.
func MakeLeaderWorkerSet(name, ns string) *LeaderWorkerSetWrapper {
	return &LeaderWorkerSetWrapper{leaderworkersetv1.LeaderWorkerSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: leaderworkersetv1.LeaderWorkerSetSpec{
			Replicas:      ptr.To[int32](1),
			StartupPolicy: leaderworkersetv1.LeaderCreatedStartupPolicy,
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
func (w *LeaderWorkerSetWrapper) Obj() *leaderworkersetv1.LeaderWorkerSet {
	return &w.LeaderWorkerSet
}

// Label sets the label of the LeaderWorkerSet
func (w *LeaderWorkerSetWrapper) Label(k, v string) *LeaderWorkerSetWrapper {
	if w.Labels == nil {
		w.Labels = make(map[string]string)
	}
	w.Labels[k] = v
	return w
}

// Queue updates the queue name of the LeaderWorkerSet
func (w *LeaderWorkerSetWrapper) Queue(q string) *LeaderWorkerSetWrapper {
	return w.Label(constants.QueueLabel, q)
}

// Name updated the name of the LeaderWorkerSet
func (w *LeaderWorkerSetWrapper) Name(n string) *LeaderWorkerSetWrapper {
	w.ObjectMeta.Name = n
	return w
}

// UID updated the uid of the LeaderWorkerSet
func (w *LeaderWorkerSetWrapper) UID(uid string) *LeaderWorkerSetWrapper {
	w.ObjectMeta.UID = types.UID(uid)
	return w
}

func (w *LeaderWorkerSetWrapper) StartupPolicy(startupPolicyType leaderworkersetv1.StartupPolicyType) *LeaderWorkerSetWrapper {
	w.Spec.StartupPolicy = startupPolicyType
	return w
}

// WorkerTemplateSpecLabel sets the label of the pod template spec of the LeaderWorkerSet
func (w *LeaderWorkerSetWrapper) WorkerTemplateSpecLabel(k, v string) *LeaderWorkerSetWrapper {
	if w.Spec.LeaderWorkerTemplate.WorkerTemplate.Labels == nil {
		w.Spec.LeaderWorkerTemplate.WorkerTemplate.Labels = make(map[string]string, 1)
	}
	w.Spec.LeaderWorkerTemplate.WorkerTemplate.Labels[k] = v
	return w
}

// WorkerTemplateSpecAnnotation sets the annotation of the pod template spec of the LeaderWorkerSet
func (w *LeaderWorkerSetWrapper) WorkerTemplateSpecAnnotation(k, v string) *LeaderWorkerSetWrapper {
	if w.Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations == nil {
		w.Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations = make(map[string]string, 1)
	}
	w.Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations[k] = v
	return w
}

// WorkerTemplateSpecQueue updates the queue name of the pod template spec of the LeaderWorkerSet
func (w *LeaderWorkerSetWrapper) WorkerTemplateSpecQueue(q string) *LeaderWorkerSetWrapper {
	return w.WorkerTemplateSpecLabel(constants.QueueLabel, q)
}

// LeaderTemplateSpecLabel sets the label of the pod template spec of the LeaderLeaderSet
func (w *LeaderWorkerSetWrapper) LeaderTemplateSpecLabel(k, v string) *LeaderWorkerSetWrapper {
	if w.Spec.LeaderWorkerTemplate.LeaderTemplate.Labels == nil {
		w.Spec.LeaderWorkerTemplate.LeaderTemplate.Labels = make(map[string]string, 1)
	}
	w.Spec.LeaderWorkerTemplate.LeaderTemplate.Labels[k] = v
	return w
}

// LeaderTemplateSpecAnnotation sets the annotation of the pod template spec of the LeaderLeaderSet
func (w *LeaderWorkerSetWrapper) LeaderTemplateSpecAnnotation(k, v string) *LeaderWorkerSetWrapper {
	if w.Spec.LeaderWorkerTemplate.LeaderTemplate.Annotations == nil {
		w.Spec.LeaderWorkerTemplate.LeaderTemplate.Annotations = make(map[string]string, 1)
	}
	w.Spec.LeaderWorkerTemplate.LeaderTemplate.Annotations[k] = v
	return w
}

// Replicas sets the number of replicas of the LeaderWorkerSet.
func (w *LeaderWorkerSetWrapper) Replicas(n int32) *LeaderWorkerSetWrapper {
	w.Spec.Replicas = ptr.To[int32](n)
	return w
}

// Size sets the size of the LeaderWorkerSet.
func (w *LeaderWorkerSetWrapper) Size(n int32) *LeaderWorkerSetWrapper {
	w.Spec.LeaderWorkerTemplate.Size = ptr.To[int32](n)
	return w
}

func (w *LeaderWorkerSetWrapper) WorkerTemplateSpecPodGroupNameLabel(
	ownerName string, ownerUID types.UID, ownerGVK schema.GroupVersionKind,
) *LeaderWorkerSetWrapper {
	gvk := jobframework.GetWorkloadNameForOwnerWithGVK(ownerName, ownerUID, ownerGVK)
	return w.WorkerTemplateSpecLabel(podconstants.GroupNameLabel, gvk)
}

func (w *LeaderWorkerSetWrapper) WorkerTemplateSpecPodGroupTotalCountAnnotation(replicas int32) *LeaderWorkerSetWrapper {
	return w.WorkerTemplateSpecAnnotation(podconstants.GroupTotalCountAnnotation, fmt.Sprint(replicas))
}

func (w *LeaderWorkerSetWrapper) Image(image string, args []string) *LeaderWorkerSetWrapper {
	w.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Image = image
	w.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Args = args
	return w
}

// Request adds a resource request to the default container.
func (w *LeaderWorkerSetWrapper) Request(r corev1.ResourceName, v string) *LeaderWorkerSetWrapper {
	if w.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Resources.Requests == nil {
		w.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Resources.Requests = corev1.ResourceList{}
	}
	w.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Resources.Requests[r] = resource.MustParse(v)
	return w
}

// Limit adds a resource limit to the default container.
func (w *LeaderWorkerSetWrapper) Limit(r corev1.ResourceName, v string) *LeaderWorkerSetWrapper {
	if w.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Resources.Limits == nil {
		w.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Resources.Limits = corev1.ResourceList{}
	}
	w.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Resources.Limits[r] = resource.MustParse(v)
	return w
}

// RequestAndLimit adds a resource request and limit to the default container.
func (w *LeaderWorkerSetWrapper) RequestAndLimit(r corev1.ResourceName, v string) *LeaderWorkerSetWrapper {
	return w.Request(r, v).Limit(r, v)
}

// LeaderTemplate sets the leader template of the LeaderWorkerSet.
func (w *LeaderWorkerSetWrapper) LeaderTemplate(leader corev1.PodTemplateSpec) *LeaderWorkerSetWrapper {
	w.Spec.LeaderWorkerTemplate.LeaderTemplate = &leader
	return w
}

// WorkerTemplate sets the worker template of the LeaderWorkerSet.
func (w *LeaderWorkerSetWrapper) WorkerTemplate(worker corev1.PodTemplateSpec) *LeaderWorkerSetWrapper {
	w.Spec.LeaderWorkerTemplate.WorkerTemplate = worker
	return w
}

func (w *LeaderWorkerSetWrapper) TerminationGracePeriod(seconds int64) *LeaderWorkerSetWrapper {
	if w.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
		w.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.TerminationGracePeriodSeconds = &seconds
	}
	w.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.TerminationGracePeriodSeconds = &seconds
	return w
}

// WorkloadPriorityClass sets workloadpriorityclass.
func (w *LeaderWorkerSetWrapper) WorkloadPriorityClass(wpc string) *LeaderWorkerSetWrapper {
	return w.Label(constants.WorkloadPriorityClassLabel, wpc)
}
