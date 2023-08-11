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

package testing

import (
	common "github.com/kubeflow/common/pkg/apis/common/v1"
	kubeflow "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/pkg/controller/constants"
)

// MPIJobWrapper wraps a Job.
type MPIJobWrapper struct{ kubeflow.MPIJob }

// MakeMPIJob creates a wrapper for a suspended job with a single container and parallelism=1.
func MakeMPIJob(name, ns string) *MPIJobWrapper {
	return &MPIJobWrapper{kubeflow.MPIJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Annotations: make(map[string]string, 1),
		},
		Spec: kubeflow.MPIJobSpec{
			RunPolicy: kubeflow.RunPolicy{
				Suspend: ptr.To(true),
			},
			MPIReplicaSpecs: map[kubeflow.MPIReplicaType]*common.ReplicaSpec{
				kubeflow.MPIReplicaTypeLauncher: {
					Replicas: ptr.To[int32](1),
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							RestartPolicy: corev1.RestartPolicyNever,
							Containers: []corev1.Container{
								{
									Name:      "c",
									Image:     "pause",
									Command:   []string{},
									Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
								},
							},
							NodeSelector: map[string]string{},
						},
					},
				},
				kubeflow.MPIReplicaTypeWorker: {
					Replicas: ptr.To[int32](1),
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							RestartPolicy: corev1.RestartPolicyNever,
							Containers: []corev1.Container{
								{
									Name:      "c",
									Image:     "pause",
									Command:   []string{},
									Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
								},
							},
							NodeSelector: map[string]string{},
						},
					},
				},
			},
		},
	}}
}

// PriorityClass updates job priorityclass.
func (j *MPIJobWrapper) PriorityClass(pc string) *MPIJobWrapper {
	if j.Spec.RunPolicy.SchedulingPolicy == nil {
		j.Spec.RunPolicy.SchedulingPolicy = &kubeflow.SchedulingPolicy{}
	}
	j.Spec.RunPolicy.SchedulingPolicy.PriorityClass = pc
	return j
}

// Obj returns the inner Job.
func (j *MPIJobWrapper) Obj() *kubeflow.MPIJob {
	return &j.MPIJob
}

// Queue updates the queue name of the job.
func (j *MPIJobWrapper) Queue(queue string) *MPIJobWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[constants.QueueLabel] = queue
	return j
}

// Request adds a resource request to the default container.
func (j *MPIJobWrapper) Request(replicaType kubeflow.MPIReplicaType, r corev1.ResourceName, v string) *MPIJobWrapper {
	j.Spec.MPIReplicaSpecs[replicaType].Template.Spec.Containers[0].Resources.Requests[r] = resource.MustParse(v)
	return j
}

// Parallelism updates job parallelism.
func (j *MPIJobWrapper) Parallelism(p int32) *MPIJobWrapper {
	j.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeWorker].Replicas = ptr.To(p)
	return j
}

// Suspend updates the suspend status of the job.
func (j *MPIJobWrapper) Suspend(s bool) *MPIJobWrapper {
	j.Spec.RunPolicy.Suspend = &s
	return j
}

// UID updates the uid of the job.
func (j *MPIJobWrapper) UID(uid string) *MPIJobWrapper {
	j.ObjectMeta.UID = types.UID(uid)
	return j
}
