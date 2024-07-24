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

// TFJobWrapper wraps a Job.
type TFJobWrapper struct{ kftraining.TFJob }

// MakeTFJob creates a wrapper for a suspended job with a single container and parallelism=1.
func MakeTFJob(name, ns string) *TFJobWrapper {
	return &TFJobWrapper{kftraining.TFJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Annotations: make(map[string]string, 1),
		},
		Spec: kftraining.TFJobSpec{
			RunPolicy: kftraining.RunPolicy{
				Suspend: ptr.To(true),
			},
			TFReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{},
		},
	}}
}

type TFReplicaSpecRequirement struct {
	ReplicaType   kftraining.ReplicaType
	Name          string
	ReplicaCount  int32
	Annotations   map[string]string
	RestartPolicy kftraining.RestartPolicy
}

func (j *TFJobWrapper) TFReplicaSpecs(replicaSpecs ...TFReplicaSpecRequirement) *TFJobWrapper {
	j = j.TFReplicaSpecsDefault()
	for _, rs := range replicaSpecs {
		j.Spec.TFReplicaSpecs[rs.ReplicaType].Replicas = ptr.To[int32](rs.ReplicaCount)
		j.Spec.TFReplicaSpecs[rs.ReplicaType].Template.Name = rs.Name
		j.Spec.TFReplicaSpecs[rs.ReplicaType].Template.Spec.RestartPolicy = corev1.RestartPolicy(rs.RestartPolicy)
		j.Spec.TFReplicaSpecs[rs.ReplicaType].Template.Spec.Containers[0].Name = "tensorflow"

		if rs.Annotations != nil {
			j.Spec.TFReplicaSpecs[rs.ReplicaType].Template.ObjectMeta.Annotations = rs.Annotations
		}
	}

	return j
}

func (j *TFJobWrapper) TFReplicaSpecsDefault() *TFJobWrapper {
	j.Spec.TFReplicaSpecs[kftraining.TFJobReplicaTypeChief] = &kftraining.ReplicaSpec{
		Replicas: ptr.To[int32](1),
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				RestartPolicy: "Never",
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
	}

	j.Spec.TFReplicaSpecs[kftraining.TFJobReplicaTypePS] = &kftraining.ReplicaSpec{
		Replicas: ptr.To[int32](1),
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				RestartPolicy: "Never",
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
	}

	j.Spec.TFReplicaSpecs[kftraining.TFJobReplicaTypeWorker] = &kftraining.ReplicaSpec{
		Replicas: ptr.To[int32](1),
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				RestartPolicy: "Never",
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
	}

	return j
}

// PriorityClass updates job priorityclass.
func (j *TFJobWrapper) PriorityClass(pc string) *TFJobWrapper {
	if j.Spec.RunPolicy.SchedulingPolicy == nil {
		j.Spec.RunPolicy.SchedulingPolicy = &kftraining.SchedulingPolicy{}
	}
	j.Spec.RunPolicy.SchedulingPolicy.PriorityClass = pc
	return j
}

// WorkloadPriorityClass updates job workloadpriorityclass.
func (j *TFJobWrapper) WorkloadPriorityClass(wpc string) *TFJobWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[constants.WorkloadPriorityClassLabel] = wpc
	return j
}

// Obj returns the inner Job.
func (j *TFJobWrapper) Obj() *kftraining.TFJob {
	return &j.TFJob
}

// Clone returns deep copy of the TFJobWrapper.
func (j *TFJobWrapper) Clone() *TFJobWrapper {
	return &TFJobWrapper{TFJob: *j.DeepCopy()}
}

// Label sets the label key and value
func (j *TFJobWrapper) Label(key, value string) *TFJobWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[key] = value
	return j
}

// Queue updates the queue name of the job.
func (j *TFJobWrapper) Queue(queue string) *TFJobWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[constants.QueueLabel] = queue
	return j
}

// Request adds a resource request to the default container.
func (j *TFJobWrapper) Request(replicaType kftraining.ReplicaType, r corev1.ResourceName, v string) *TFJobWrapper {
	j.Spec.TFReplicaSpecs[replicaType].Template.Spec.Containers[0].Resources.Requests[r] = resource.MustParse(v)
	return j
}

// Parallelism updates job parallelism.
func (j *TFJobWrapper) Parallelism(workerParallelism, psParallelism int32) *TFJobWrapper {
	j.Spec.TFReplicaSpecs[kftraining.TFJobReplicaTypeWorker].Replicas = ptr.To(workerParallelism)
	j.Spec.TFReplicaSpecs[kftraining.TFJobReplicaTypePS].Replicas = ptr.To(psParallelism)
	return j
}

// Suspend updates the suspend status of the job.
func (j *TFJobWrapper) Suspend(s bool) *TFJobWrapper {
	j.Spec.RunPolicy.Suspend = &s
	return j
}

// UID updates the uid of the job.
func (j *TFJobWrapper) UID(uid string) *TFJobWrapper {
	j.ObjectMeta.UID = types.UID(uid)
	return j
}

// Condition adds a condition
func (j *TFJobWrapper) StatusConditions(c kftraining.JobCondition) *TFJobWrapper {
	j.Status.Conditions = append(j.Status.Conditions, c)
	return j
}

func (j *TFJobWrapper) Image(replicaType kftraining.ReplicaType, image string, args []string) *TFJobWrapper {
	j.Spec.TFReplicaSpecs[replicaType].Template.Spec.Containers[0].Image = image
	j.Spec.TFReplicaSpecs[replicaType].Template.Spec.Containers[0].Args = args
	return j
}
