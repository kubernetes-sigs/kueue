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

package jaxjob

import (
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/pkg/controller/constants"
)

// JAXJobWrapper wraps a Job.
type JAXJobWrapper struct{ kftraining.JAXJob }

// MakeJAXJob creates a wrapper for a suspended job with a single container and parallelism=1.
func MakeJAXJob(name, ns string) *JAXJobWrapper {
	return &JAXJobWrapper{kftraining.JAXJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Annotations: make(map[string]string, 1),
		},
		Spec: kftraining.JAXJobSpec{
			RunPolicy: kftraining.RunPolicy{
				Suspend: ptr.To(true),
			},
			JAXReplicaSpecs: make(map[kftraining.ReplicaType]*kftraining.ReplicaSpec),
		},
	}}
}

type JAXReplicaSpecRequirement struct {
	Image         string
	Args          []string
	ReplicaType   kftraining.ReplicaType
	Name          string
	ReplicaCount  int32
	Annotations   map[string]string
	RestartPolicy kftraining.RestartPolicy
}

func (j *JAXJobWrapper) JAXReplicaSpecs(replicaSpecs ...JAXReplicaSpecRequirement) *JAXJobWrapper {
	j.JAXReplicaSpecsDefault()
	for _, rs := range replicaSpecs {
		j.Spec.JAXReplicaSpecs[rs.ReplicaType].Replicas = ptr.To[int32](rs.ReplicaCount)
		j.Spec.JAXReplicaSpecs[rs.ReplicaType].Template.Name = rs.Name
		j.Spec.JAXReplicaSpecs[rs.ReplicaType].Template.Spec.RestartPolicy = corev1.RestartPolicy(rs.RestartPolicy)
		j.Spec.JAXReplicaSpecs[rs.ReplicaType].Template.Spec.Containers[0].Name = "jax"
		j.Spec.JAXReplicaSpecs[rs.ReplicaType].Template.Spec.Containers[0].Image = rs.Image
		j.Spec.JAXReplicaSpecs[rs.ReplicaType].Template.Spec.Containers[0].Args = rs.Args

		if rs.Annotations != nil {
			j.Spec.JAXReplicaSpecs[rs.ReplicaType].Template.Annotations = rs.Annotations
		}
	}

	return j
}

func (j *JAXJobWrapper) JAXReplicaSpecsDefault() *JAXJobWrapper {
	j.Spec.JAXReplicaSpecs[kftraining.JAXJobReplicaTypeWorker] = &kftraining.ReplicaSpec{
		Replicas: ptr.To[int32](1),
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				RestartPolicy: "Never",
				Containers: []corev1.Container{
					{
						Name:    "jax",
						Image:   "pause",
						Command: []string{},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{},
							Limits:   corev1.ResourceList{},
						},
					},
				},
				NodeSelector: map[string]string{},
			},
		},
	}

	return j
}

// Clone returns deep copy of the JAXJobWrapper.
func (j *JAXJobWrapper) Clone() *JAXJobWrapper {
	return &JAXJobWrapper{JAXJob: *j.DeepCopy()}
}

// Label sets the label key and value
func (j *JAXJobWrapper) Label(key, value string) *JAXJobWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[key] = value
	return j
}

// PriorityClass updates job priorityclass.
func (j *JAXJobWrapper) PriorityClass(pc string) *JAXJobWrapper {
	if j.Spec.RunPolicy.SchedulingPolicy == nil {
		j.Spec.RunPolicy.SchedulingPolicy = &kftraining.SchedulingPolicy{}
	}
	j.Spec.RunPolicy.SchedulingPolicy.PriorityClass = pc
	return j
}

// WorkloadPriorityClass updates job workloadpriorityclass.
func (j *JAXJobWrapper) WorkloadPriorityClass(wpc string) *JAXJobWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[constants.WorkloadPriorityClassLabel] = wpc
	return j
}

// Obj returns the inner Job.
func (j *JAXJobWrapper) Obj() *kftraining.JAXJob {
	return &j.JAXJob
}

// Queue updates the queue name of the job.
func (j *JAXJobWrapper) Queue(queue string) *JAXJobWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[constants.QueueLabel] = queue
	return j
}

// Request adds a resource request to the default container.
func (j *JAXJobWrapper) Request(replicaType kftraining.ReplicaType, r corev1.ResourceName, v string) *JAXJobWrapper {
	j.Spec.JAXReplicaSpecs[replicaType].Template.Spec.Containers[0].Resources.Requests[r] = resource.MustParse(v)
	return j
}

// Limit adds a resource request to the default container.
func (j *JAXJobWrapper) Limit(replicaType kftraining.ReplicaType, r corev1.ResourceName, v string) *JAXJobWrapper {
	j.Spec.JAXReplicaSpecs[replicaType].Template.Spec.Containers[0].Resources.Limits[r] = resource.MustParse(v)
	return j
}

// Parallelism updates job parallelism.
func (j *JAXJobWrapper) Parallelism(replicaType kftraining.ReplicaType, p int32) *JAXJobWrapper {
	j.Spec.JAXReplicaSpecs[replicaType].Replicas = ptr.To(p)
	return j
}

// Suspend updates the suspend status of the job.
func (j *JAXJobWrapper) Suspend(s bool) *JAXJobWrapper {
	j.Spec.RunPolicy.Suspend = &s
	return j
}

// UID updates the uid of the job.
func (j *JAXJobWrapper) UID(uid string) *JAXJobWrapper {
	j.ObjectMeta.UID = types.UID(uid)
	return j
}

// PodAnnotation sets annotation at the pod template level
func (j *JAXJobWrapper) PodAnnotation(replicaType kftraining.ReplicaType, k, v string) *JAXJobWrapper {
	if j.Spec.JAXReplicaSpecs[replicaType].Template.Annotations == nil {
		j.Spec.JAXReplicaSpecs[replicaType].Template.Annotations = make(map[string]string)
	}
	j.Spec.JAXReplicaSpecs[replicaType].Template.Annotations[k] = v
	return j
}

// PodLabel sets label at the pod template level
func (j *JAXJobWrapper) PodLabel(replicaType kftraining.ReplicaType, k, v string) *JAXJobWrapper {
	if j.Spec.JAXReplicaSpecs[replicaType].Template.Labels == nil {
		j.Spec.JAXReplicaSpecs[replicaType].Template.Labels = make(map[string]string)
	}
	j.Spec.JAXReplicaSpecs[replicaType].Template.Labels[k] = v
	return j
}

// StatusConditions adds a condition.
func (j *JAXJobWrapper) StatusConditions(c kftraining.JobCondition) *JAXJobWrapper {
	j.Status.Conditions = append(j.Status.Conditions, c)
	return j
}

func (j *JAXJobWrapper) Image(replicaType kftraining.ReplicaType, image string, args []string) *JAXJobWrapper {
	j.Spec.JAXReplicaSpecs[replicaType].Template.Spec.Containers[0].Image = image
	j.Spec.JAXReplicaSpecs[replicaType].Template.Spec.Containers[0].Args = args
	return j
}

// Command sets command for the default container of the specified ReplicaType.
func (j *JAXJobWrapper) Command(replicaType kftraining.ReplicaType, command []string) *JAXJobWrapper {
	j.Spec.JAXReplicaSpecs[replicaType].Template.Spec.Containers[0].Command = command
	return j
}

func (j *JAXJobWrapper) SetTypeMeta() *JAXJobWrapper {
	j.APIVersion = kftraining.GroupVersion.String()
	j.Kind = kftraining.JAXJobKind
	return j
}

// ManagedBy adds a ManagedBy.
func (j *JAXJobWrapper) ManagedBy(c string) *JAXJobWrapper {
	j.Spec.RunPolicy.ManagedBy = &c
	return j
}

// WithPriorityClassName sets the priority class name for the job.
func (j *JAXJobWrapper) WithPriorityClassName(pc string) *JAXJobWrapper {
	j.Spec.RunPolicy.SchedulingPolicy.PriorityClass = pc
	return j
}
