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
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/pkg/controller/constants"
)

// PyTorchJobWrapper wraps a Job.
type PyTorchJobWrapper struct{ kftraining.PyTorchJob }

// MakePyTorchJob creates a wrapper for a suspended job with a single container and parallelism=1.
func MakePyTorchJob(name, ns string) *PyTorchJobWrapper {
	return &PyTorchJobWrapper{kftraining.PyTorchJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Annotations: make(map[string]string, 1),
		},
		Spec: kftraining.PyTorchJobSpec{
			RunPolicy: kftraining.RunPolicy{
				Suspend: ptr.To(true),
			},
			PyTorchReplicaSpecs: make(map[kftraining.ReplicaType]*kftraining.ReplicaSpec),
		},
	}}
}

type PyTorchReplicaSpecRequirement struct {
	Image         string
	Args          []string
	ReplicaType   kftraining.ReplicaType
	Name          string
	ReplicaCount  int32
	Annotations   map[string]string
	RestartPolicy kftraining.RestartPolicy
}

func (j *PyTorchJobWrapper) PyTorchReplicaSpecs(replicaSpecs ...PyTorchReplicaSpecRequirement) *PyTorchJobWrapper {
	j = j.PyTorchReplicaSpecsDefault()
	for _, rs := range replicaSpecs {
		j.Spec.PyTorchReplicaSpecs[rs.ReplicaType].Replicas = ptr.To[int32](rs.ReplicaCount)
		j.Spec.PyTorchReplicaSpecs[rs.ReplicaType].Template.Name = rs.Name
		j.Spec.PyTorchReplicaSpecs[rs.ReplicaType].Template.Spec.RestartPolicy = corev1.RestartPolicy(rs.RestartPolicy)
		j.Spec.PyTorchReplicaSpecs[rs.ReplicaType].Template.Spec.Containers[0].Name = "pytorch"
		j.Spec.PyTorchReplicaSpecs[rs.ReplicaType].Template.Spec.Containers[0].Image = rs.Image
		j.Spec.PyTorchReplicaSpecs[rs.ReplicaType].Template.Spec.Containers[0].Args = rs.Args

		if rs.Annotations != nil {
			j.Spec.PyTorchReplicaSpecs[rs.ReplicaType].Template.ObjectMeta.Annotations = rs.Annotations
		}
	}

	return j
}

func (j *PyTorchJobWrapper) PyTorchReplicaSpecsDefault() *PyTorchJobWrapper {
	j.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeMaster] = &kftraining.ReplicaSpec{
		Replicas: ptr.To[int32](1),
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				RestartPolicy: "Never",
				Containers: []corev1.Container{
					{
						Name:    "c",
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

	j.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeWorker] = &kftraining.ReplicaSpec{
		Replicas: ptr.To[int32](1),
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				RestartPolicy: "Never",
				Containers: []corev1.Container{
					{
						Name:    "c",
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

// Clone returns deep copy of the PyTorchJobWrapper.
func (j *PyTorchJobWrapper) Clone() *PyTorchJobWrapper {
	return &PyTorchJobWrapper{PyTorchJob: *j.DeepCopy()}
}

// Label sets the label key and value
func (j *PyTorchJobWrapper) Label(key, value string) *PyTorchJobWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[key] = value
	return j
}

// PriorityClass updates job priorityclass.
func (j *PyTorchJobWrapper) PriorityClass(pc string) *PyTorchJobWrapper {
	if j.Spec.RunPolicy.SchedulingPolicy == nil {
		j.Spec.RunPolicy.SchedulingPolicy = &kftraining.SchedulingPolicy{}
	}
	j.Spec.RunPolicy.SchedulingPolicy.PriorityClass = pc
	return j
}

// WorkloadPriorityClass updates job workloadpriorityclass.
func (j *PyTorchJobWrapper) WorkloadPriorityClass(wpc string) *PyTorchJobWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[constants.WorkloadPriorityClassLabel] = wpc
	return j
}

// Obj returns the inner Job.
func (j *PyTorchJobWrapper) Obj() *kftraining.PyTorchJob {
	return &j.PyTorchJob
}

// Queue updates the queue name of the job.
func (j *PyTorchJobWrapper) Queue(queue string) *PyTorchJobWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[constants.QueueLabel] = queue
	return j
}

// Request adds a resource request to the default container.
func (j *PyTorchJobWrapper) Request(replicaType kftraining.ReplicaType, r corev1.ResourceName, v string) *PyTorchJobWrapper {
	j.Spec.PyTorchReplicaSpecs[replicaType].Template.Spec.Containers[0].Resources.Requests[r] = resource.MustParse(v)
	return j
}

// Limit adds a resource request to the default container.
func (j *PyTorchJobWrapper) Limit(replicaType kftraining.ReplicaType, r corev1.ResourceName, v string) *PyTorchJobWrapper {
	j.Spec.PyTorchReplicaSpecs[replicaType].Template.Spec.Containers[0].Resources.Limits[r] = resource.MustParse(v)
	return j
}

// Parallelism updates job parallelism.
func (j *PyTorchJobWrapper) Parallelism(p int32) *PyTorchJobWrapper {
	j.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeWorker].Replicas = ptr.To(p)
	return j
}

// Suspend updates the suspend status of the job.
func (j *PyTorchJobWrapper) Suspend(s bool) *PyTorchJobWrapper {
	j.Spec.RunPolicy.Suspend = &s
	return j
}

// UID updates the uid of the job.
func (j *PyTorchJobWrapper) UID(uid string) *PyTorchJobWrapper {
	j.ObjectMeta.UID = types.UID(uid)
	return j
}

// PodAnnotation sets annotation at the pod template level
func (j *PyTorchJobWrapper) PodAnnotation(replicaType kftraining.ReplicaType, k, v string) *PyTorchJobWrapper {
	if j.Spec.PyTorchReplicaSpecs[replicaType].Template.Annotations == nil {
		j.Spec.PyTorchReplicaSpecs[replicaType].Template.Annotations = make(map[string]string)
	}
	j.Spec.PyTorchReplicaSpecs[replicaType].Template.Annotations[k] = v
	return j
}

// PodLabel sets label at the pod template level
func (j *PyTorchJobWrapper) PodLabel(replicaType kftraining.ReplicaType, k, v string) *PyTorchJobWrapper {
	if j.Spec.PyTorchReplicaSpecs[replicaType].Template.Labels == nil {
		j.Spec.PyTorchReplicaSpecs[replicaType].Template.Labels = make(map[string]string)
	}
	j.Spec.PyTorchReplicaSpecs[replicaType].Template.Labels[k] = v
	return j
}

// Condition adds a condition
func (j *PyTorchJobWrapper) StatusConditions(c kftraining.JobCondition) *PyTorchJobWrapper {
	j.Status.Conditions = append(j.Status.Conditions, c)
	return j
}

func (j *PyTorchJobWrapper) Image(replicaType kftraining.ReplicaType, image string, args []string) *PyTorchJobWrapper {
	j.Spec.PyTorchReplicaSpecs[replicaType].Template.Spec.Containers[0].Image = image
	j.Spec.PyTorchReplicaSpecs[replicaType].Template.Spec.Containers[0].Args = args
	return j
}
