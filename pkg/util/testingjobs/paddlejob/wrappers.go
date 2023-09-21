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

package paddlejob

import (
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/pkg/controller/constants"
)

// PaddleJobWrapper wraps a Job.
type PaddleJobWrapper struct{ kftraining.PaddleJob }

// MakePaddleJob creates a wrapper for a suspended job with a single container and parallelism=1.
func MakePaddleJob(name, ns string) *PaddleJobWrapper {
	return &PaddleJobWrapper{kftraining.PaddleJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Annotations: make(map[string]string, 1),
		},
		Spec: kftraining.PaddleJobSpec{
			RunPolicy: kftraining.RunPolicy{
				Suspend: ptr.To(true),
			},
			PaddleReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
				kftraining.PaddleJobReplicaTypeMaster: {
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
				kftraining.PaddleJobReplicaTypeWorker: {
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
func (j *PaddleJobWrapper) PriorityClass(pc string) *PaddleJobWrapper {
	if j.Spec.RunPolicy.SchedulingPolicy == nil {
		j.Spec.RunPolicy.SchedulingPolicy = &kftraining.SchedulingPolicy{}
	}
	j.Spec.RunPolicy.SchedulingPolicy.PriorityClass = pc
	return j
}

// Obj returns the inner Job.
func (j *PaddleJobWrapper) Obj() *kftraining.PaddleJob {
	return &j.PaddleJob
}

// Queue updates the queue name of the job.
func (j *PaddleJobWrapper) Queue(queue string) *PaddleJobWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[constants.QueueLabel] = queue
	return j
}

// Request adds a resource request to the default container.
func (j *PaddleJobWrapper) Request(replicaType kftraining.ReplicaType, r corev1.ResourceName, v string) *PaddleJobWrapper {
	j.Spec.PaddleReplicaSpecs[replicaType].Template.Spec.Containers[0].Resources.Requests[r] = resource.MustParse(v)
	return j
}

// Image updates images of the job.
func (j *PaddleJobWrapper) Image(image string) *PaddleJobWrapper {
	j.Spec.PaddleReplicaSpecs[kftraining.PaddleJobReplicaTypeMaster].Template.Spec.Containers[0].Image = image
	j.Spec.PaddleReplicaSpecs[kftraining.PaddleJobReplicaTypeWorker].Template.Spec.Containers[0].Image = image
	return j
}

// Args updates args of the job.
func (j *PaddleJobWrapper) Args(args []string) *PaddleJobWrapper {
	j.Spec.PaddleReplicaSpecs[kftraining.PaddleJobReplicaTypeMaster].Template.Spec.Containers[0].Args = args
	j.Spec.PaddleReplicaSpecs[kftraining.PaddleJobReplicaTypeWorker].Template.Spec.Containers[0].Args = args
	return j
}

// Parallelism updates job parallelism.
func (j *PaddleJobWrapper) Parallelism(p int32) *PaddleJobWrapper {
	j.Spec.PaddleReplicaSpecs[kftraining.PaddleJobReplicaTypeWorker].Replicas = ptr.To(p)
	return j
}

// Suspend updates the suspend status of the job.
func (j *PaddleJobWrapper) Suspend(s bool) *PaddleJobWrapper {
	j.Spec.RunPolicy.Suspend = &s
	return j
}

// UID updates the uid of the job.
func (j *PaddleJobWrapper) UID(uid string) *PaddleJobWrapper {
	j.ObjectMeta.UID = types.UID(uid)
	return j
}

// NodeSelector updates the nodeSelector of job.
func (j *PaddleJobWrapper) NodeSelector(k, v string) *PaddleJobWrapper {
	if j.Spec.PaddleReplicaSpecs[kftraining.PaddleJobReplicaTypeMaster].Template.Spec.NodeSelector == nil {
		j.Spec.PaddleReplicaSpecs[kftraining.PaddleJobReplicaTypeMaster].Template.Spec.NodeSelector = make(map[string]string)
	}
	if j.Spec.PaddleReplicaSpecs[kftraining.PaddleJobReplicaTypeWorker].Template.Spec.NodeSelector == nil {
		j.Spec.PaddleReplicaSpecs[kftraining.PaddleJobReplicaTypeWorker].Template.Spec.NodeSelector = make(map[string]string)
	}
	j.Spec.PaddleReplicaSpecs[kftraining.PaddleJobReplicaTypeMaster].Template.Spec.NodeSelector[k] = v
	j.Spec.PaddleReplicaSpecs[kftraining.PaddleJobReplicaTypeWorker].Template.Spec.NodeSelector[k] = v
	return j
}

// Active updates the replicaStatus for Active of job.
func (j *PaddleJobWrapper) Active(rType kftraining.ReplicaType, c int32) *PaddleJobWrapper {
	if j.Status.ReplicaStatuses == nil {
		j.Status.ReplicaStatuses = make(map[kftraining.ReplicaType]*kftraining.ReplicaStatus)
	}
	j.Status.ReplicaStatuses[rType] = &kftraining.ReplicaStatus{
		Active: c,
	}
	return j
}
