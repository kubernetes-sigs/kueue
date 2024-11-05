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

package mxjob

import (
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/pkg/controller/constants"
)

// MXJobWrapper wraps a Job.
type MXJobWrapper struct{ kftraining.MXJob }

// MakeMXJob creates a wrapper for a suspended job with a single container and parallelism=1.
func MakeMXJob(name, ns string) *MXJobWrapper {
	return &MXJobWrapper{kftraining.MXJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Annotations: make(map[string]string, 1),
		},
		Spec: kftraining.MXJobSpec{
			JobMode: kftraining.MXTrain,
			RunPolicy: kftraining.RunPolicy{
				Suspend: ptr.To(true),
			},
			MXReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
				kftraining.MXJobReplicaTypeScheduler: {
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
				},
				kftraining.MXJobReplicaTypeServer: {
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
				},
				kftraining.MXJobReplicaTypeWorker: {
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
				},
			},
		},
	}}
}

// PriorityClass updates job priorityclass.
func (j *MXJobWrapper) PriorityClass(pc string) *MXJobWrapper {
	if j.Spec.RunPolicy.SchedulingPolicy == nil {
		j.Spec.RunPolicy.SchedulingPolicy = &kftraining.SchedulingPolicy{}
	}
	j.Spec.RunPolicy.SchedulingPolicy.PriorityClass = pc
	return j
}

// WorkloadPriorityClass updates job workloadpriorityclass.
func (j *MXJobWrapper) WorkloadPriorityClass(wpc string) *MXJobWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[constants.WorkloadPriorityClassLabel] = wpc
	return j
}

// Obj returns the inner Job.
func (j *MXJobWrapper) Obj() *kftraining.MXJob {
	return &j.MXJob
}

// Queue updates the queue name of the job.
func (j *MXJobWrapper) Queue(queue string) *MXJobWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[constants.QueueLabel] = queue
	return j
}

// Annotations updates annotations of the job.
func (j *MXJobWrapper) Annotations(annotations map[string]string) *MXJobWrapper {
	j.ObjectMeta.Annotations = annotations
	return j
}

// PodAnnotation sets annotation at the pod template level
func (j *MXJobWrapper) PodAnnotation(replicaType kftraining.ReplicaType, k, v string) *MXJobWrapper {
	if j.Spec.MXReplicaSpecs[replicaType].Template.Annotations == nil {
		j.Spec.MXReplicaSpecs[replicaType].Template.Annotations = make(map[string]string)
	}
	j.Spec.MXReplicaSpecs[replicaType].Template.Annotations[k] = v
	return j
}

// Request adds a resource request to the default container.
func (j *MXJobWrapper) Request(replicaType kftraining.ReplicaType, r corev1.ResourceName, v string) *MXJobWrapper {
	j.Spec.MXReplicaSpecs[replicaType].Template.Spec.Containers[0].Resources.Requests[r] = resource.MustParse(v)
	return j
}

// Image updates images of the job.
func (j *MXJobWrapper) Image(image string) *MXJobWrapper {
	j.Spec.MXReplicaSpecs[kftraining.MXJobReplicaTypeScheduler].Template.Spec.Containers[0].Image = image
	j.Spec.MXReplicaSpecs[kftraining.MXJobReplicaTypeServer].Template.Spec.Containers[0].Image = image
	j.Spec.MXReplicaSpecs[kftraining.MXJobReplicaTypeWorker].Template.Spec.Containers[0].Image = image
	return j
}

// Args updates args of the job.
func (j *MXJobWrapper) Args(args []string) *MXJobWrapper {
	j.Spec.MXReplicaSpecs[kftraining.MXJobReplicaTypeScheduler].Template.Spec.Containers[0].Args = args
	j.Spec.MXReplicaSpecs[kftraining.MXJobReplicaTypeServer].Template.Spec.Containers[0].Args = args
	j.Spec.MXReplicaSpecs[kftraining.MXJobReplicaTypeWorker].Template.Spec.Containers[0].Args = args
	return j
}

// Parallelism updates job parallelism.
func (j *MXJobWrapper) Parallelism(workerParallelism, psParallelism int32) *MXJobWrapper {
	j.Spec.MXReplicaSpecs[kftraining.MXJobReplicaTypeWorker].Replicas = ptr.To(workerParallelism)
	j.Spec.MXReplicaSpecs[kftraining.MXJobReplicaTypeServer].Replicas = ptr.To(psParallelism)
	return j
}

// Suspend updates the suspend status of the job.
func (j *MXJobWrapper) Suspend(s bool) *MXJobWrapper {
	j.Spec.RunPolicy.Suspend = &s
	return j
}

// UID updates the uid of the job.
func (j *MXJobWrapper) UID(uid string) *MXJobWrapper {
	j.ObjectMeta.UID = types.UID(uid)
	return j
}

// NodeSelector updates the nodeSelector of job.
func (j *MXJobWrapper) NodeSelector(k, v string) *MXJobWrapper {
	return j.RoleNodeSelector(kftraining.MXJobReplicaTypeServer, k, v).
		RoleNodeSelector(kftraining.MXJobReplicaTypeWorker, k, v)
}

// RoleNodeSelector updates the nodeSelector of job.
func (j *MXJobWrapper) RoleNodeSelector(role kftraining.ReplicaType, k, v string) *MXJobWrapper {
	if j.Spec.MXReplicaSpecs[role].Template.Spec.NodeSelector == nil {
		j.Spec.MXReplicaSpecs[role].Template.Spec.NodeSelector = make(map[string]string)
	}
	j.Spec.MXReplicaSpecs[role].Template.Spec.NodeSelector[k] = v
	return j
}

// Active updates the replicaStatus for Active of job.
func (j *MXJobWrapper) Active(rType kftraining.ReplicaType, c int32) *MXJobWrapper {
	if j.Status.ReplicaStatuses == nil {
		j.Status.ReplicaStatuses = make(map[kftraining.ReplicaType]*kftraining.ReplicaStatus)
	}
	j.Status.ReplicaStatuses[rType] = &kftraining.ReplicaStatus{
		Active: c,
	}
	return j
}

// StatusConditions updates status conditions of the MXJob.
func (j *MXJobWrapper) StatusConditions(conditions ...kftraining.JobCondition) *MXJobWrapper {
	j.Status.Conditions = conditions
	return j
}
