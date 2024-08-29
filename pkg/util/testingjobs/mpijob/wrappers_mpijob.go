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
			MPIReplicaSpecs: make(map[kubeflow.MPIReplicaType]*kubeflow.ReplicaSpec),
		},
	},
	}
}

type MPIJobReplicaSpecRequirement struct {
	ReplicaType   kubeflow.MPIReplicaType
	Name          string
	ReplicaCount  int32
	Annotations   map[string]string
	RestartPolicy corev1.RestartPolicy
}

func (j *MPIJobWrapper) MPIJobReplicaSpecs(replicaSpecs ...MPIJobReplicaSpecRequirement) *MPIJobWrapper {
	j = j.MPIJobReplicaSpecsDefault()
	for _, rs := range replicaSpecs {
		j.Spec.MPIReplicaSpecs[rs.ReplicaType].Replicas = ptr.To[int32](rs.ReplicaCount)
		j.Spec.MPIReplicaSpecs[rs.ReplicaType].Template.Name = rs.Name
		j.Spec.MPIReplicaSpecs[rs.ReplicaType].Template.Spec.RestartPolicy = rs.RestartPolicy

		if rs.Annotations != nil {
			j.Spec.MPIReplicaSpecs[rs.ReplicaType].Template.ObjectMeta.Annotations = rs.Annotations
		}
	}

	return j
}

func (j *MPIJobWrapper) MPIJobReplicaSpecsDefault() *MPIJobWrapper {
	j.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeLauncher] = &kubeflow.ReplicaSpec{
		Replicas: ptr.To[int32](1),
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				RestartPolicy: "Never",
				Containers: []corev1.Container{
					{
						Name:      "mpijob",
						Image:     "pause",
						Command:   []string{},
						Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
					},
				},
				NodeSelector: map[string]string{},
			},
		},
	}

	j.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeWorker] = &kubeflow.ReplicaSpec{
		Replicas: ptr.To[int32](1),
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				RestartPolicy: "Never",
				Containers: []corev1.Container{
					{
						Name:      "mpijob",
						Image:     "pause",
						Command:   []string{},
						Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
					},
				},
				NodeSelector: map[string]string{},
			},
		},
	}

	return j
}

// Clone returns deep copy of the PaddleJobWrapper.
func (j *MPIJobWrapper) Clone() *MPIJobWrapper {
	return &MPIJobWrapper{MPIJob: *j.DeepCopy()}
}

// Label sets the label key and value
func (j *MPIJobWrapper) Label(key, value string) *MPIJobWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[key] = value
	return j
}

// PriorityClass updates job priorityclass.
func (j *MPIJobWrapper) PriorityClass(pc string) *MPIJobWrapper {
	if j.Spec.RunPolicy.SchedulingPolicy == nil {
		j.Spec.RunPolicy.SchedulingPolicy = &kubeflow.SchedulingPolicy{}
	}
	j.Spec.RunPolicy.SchedulingPolicy.PriorityClass = pc
	return j
}

// WorkloadPriorityClass updates job workloadpriorityclass.
func (j *MPIJobWrapper) WorkloadPriorityClass(wpc string) *MPIJobWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[constants.WorkloadPriorityClassLabel] = wpc
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

// PodAnnotation sets annotation at the pod template level
func (j *MPIJobWrapper) PodAnnotation(replicaType kubeflow.MPIReplicaType, k, v string) *MPIJobWrapper {
	if j.Spec.MPIReplicaSpecs[replicaType].Template.Annotations == nil {
		j.Spec.MPIReplicaSpecs[replicaType].Template.Annotations = make(map[string]string)
	}
	j.Spec.MPIReplicaSpecs[replicaType].Template.Annotations[k] = v
	return j
}

// PodLabel sets label at the pod template level
func (j *MPIJobWrapper) PodLabel(replicaType kubeflow.MPIReplicaType, k, v string) *MPIJobWrapper {
	if j.Spec.MPIReplicaSpecs[replicaType].Template.Labels == nil {
		j.Spec.MPIReplicaSpecs[replicaType].Template.Labels = make(map[string]string)
	}
	j.Spec.MPIReplicaSpecs[replicaType].Template.Labels[k] = v
	return j
}

// Generation sets the generation of the job.
func (j *MPIJobWrapper) Generation(num int64) *MPIJobWrapper {
	j.ObjectMeta.Generation = num
	return j
}

// StatusConditions adds a condition
func (j *MPIJobWrapper) StatusConditions(c kubeflow.JobCondition) *MPIJobWrapper {
	j.Status.Conditions = append(j.Status.Conditions, c)
	return j
}

func (j *MPIJobWrapper) Image(replicaType kubeflow.MPIReplicaType, image string, args []string) *MPIJobWrapper {
	j.Spec.MPIReplicaSpecs[replicaType].Template.Spec.Containers[0].Image = image
	j.Spec.MPIReplicaSpecs[replicaType].Template.Spec.Containers[0].Args = args
	return j
}
