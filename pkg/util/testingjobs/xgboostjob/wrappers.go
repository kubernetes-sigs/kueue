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

// XGBoostJobWrapper wraps a Job.
type XGBoostJobWrapper struct{ kftraining.XGBoostJob }

// MakeXGBoostJob creates a wrapper for a suspended job with a single container and parallelism=1.
func MakeXGBoostJob(name, ns string) *XGBoostJobWrapper {
	return &XGBoostJobWrapper{kftraining.XGBoostJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Annotations: make(map[string]string, 1),
		},
		Spec: kftraining.XGBoostJobSpec{
			RunPolicy: kftraining.RunPolicy{
				Suspend: ptr.To(true),
			},
			XGBReplicaSpecs: make(map[kftraining.ReplicaType]*kftraining.ReplicaSpec),
		},
	}}
}

type XGBReplicaSpecRequirement struct {
	ReplicaType   kftraining.ReplicaType
	Name          string
	ReplicaCount  int32
	Annotations   map[string]string
	RestartPolicy kftraining.RestartPolicy
}

func (j *XGBoostJobWrapper) XGBReplicaSpecs(replicaSpecs ...XGBReplicaSpecRequirement) *XGBoostJobWrapper {
	j = j.XGBReplicaSpecsDefault()
	for _, rs := range replicaSpecs {
		j.Spec.XGBReplicaSpecs[rs.ReplicaType].Replicas = ptr.To[int32](rs.ReplicaCount)
		j.Spec.XGBReplicaSpecs[rs.ReplicaType].Template.Name = rs.Name
		j.Spec.XGBReplicaSpecs[rs.ReplicaType].Template.Spec.RestartPolicy = corev1.RestartPolicy(rs.RestartPolicy)
		j.Spec.XGBReplicaSpecs[rs.ReplicaType].Template.Spec.Containers[0].Name = "xgboost"

		if rs.Annotations != nil {
			j.Spec.XGBReplicaSpecs[rs.ReplicaType].Template.ObjectMeta.Annotations = rs.Annotations
		}
	}

	return j
}

func (j *XGBoostJobWrapper) XGBReplicaSpecsDefault() *XGBoostJobWrapper {
	j.Spec.XGBReplicaSpecs[kftraining.XGBoostJobReplicaTypeMaster] = &kftraining.ReplicaSpec{
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

	j.Spec.XGBReplicaSpecs[kftraining.XGBoostJobReplicaTypeWorker] = &kftraining.ReplicaSpec{
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

// Clone returns deep copy of the XGBoostJobWrapper.
func (j *XGBoostJobWrapper) Clone() *XGBoostJobWrapper {
	return &XGBoostJobWrapper{XGBoostJob: *j.DeepCopy()}
}

// Label sets the label key and value
func (j *XGBoostJobWrapper) Label(key, value string) *XGBoostJobWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[key] = value
	return j
}

// PriorityClass updates job priorityclass.
func (j *XGBoostJobWrapper) PriorityClass(pc string) *XGBoostJobWrapper {
	if j.Spec.RunPolicy.SchedulingPolicy == nil {
		j.Spec.RunPolicy.SchedulingPolicy = &kftraining.SchedulingPolicy{}
	}
	j.Spec.RunPolicy.SchedulingPolicy.PriorityClass = pc
	return j
}

// Obj returns the inner Job.
func (j *XGBoostJobWrapper) Obj() *kftraining.XGBoostJob {
	return &j.XGBoostJob
}

// Queue updates the queue name of the job.
func (j *XGBoostJobWrapper) Queue(queue string) *XGBoostJobWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[constants.QueueLabel] = queue
	return j
}

// Request updates a resource request to the default container.
func (j *XGBoostJobWrapper) Request(replicaType kftraining.ReplicaType, r corev1.ResourceName, v string) *XGBoostJobWrapper {
	j.Spec.XGBReplicaSpecs[replicaType].Template.Spec.Containers[0].Resources.Requests[r] = resource.MustParse(v)
	return j
}

// Image updates images of the job.
func (j *XGBoostJobWrapper) Image(image string) *XGBoostJobWrapper {
	j.Spec.XGBReplicaSpecs[kftraining.XGBoostJobReplicaTypeMaster].Template.Spec.Containers[0].Image = image
	j.Spec.XGBReplicaSpecs[kftraining.XGBoostJobReplicaTypeWorker].Template.Spec.Containers[0].Image = image
	return j
}

// Args updates args of the job.
func (j *XGBoostJobWrapper) Args(args []string) *XGBoostJobWrapper {
	j.Spec.XGBReplicaSpecs[kftraining.XGBoostJobReplicaTypeMaster].Template.Spec.Containers[0].Args = args
	j.Spec.XGBReplicaSpecs[kftraining.XGBoostJobReplicaTypeWorker].Template.Spec.Containers[0].Args = args
	return j
}

// Parallelism updates job parallelism.
func (j *XGBoostJobWrapper) Parallelism(p int32) *XGBoostJobWrapper {
	j.Spec.XGBReplicaSpecs[kftraining.XGBoostJobReplicaTypeWorker].Replicas = ptr.To(p)
	return j
}

// Suspend updates the suspend status of the job.
func (j *XGBoostJobWrapper) Suspend(s bool) *XGBoostJobWrapper {
	j.Spec.RunPolicy.Suspend = &s
	return j
}

// UID updates the uid of the job.
func (j *XGBoostJobWrapper) UID(uid string) *XGBoostJobWrapper {
	j.ObjectMeta.UID = types.UID(uid)
	return j
}

// NodeSelector updates the nodeSelector of job.
func (j *XGBoostJobWrapper) NodeSelector(k, v string) *XGBoostJobWrapper {
	if j.Spec.XGBReplicaSpecs[kftraining.XGBoostJobReplicaTypeMaster].Template.Spec.NodeSelector == nil {
		j.Spec.XGBReplicaSpecs[kftraining.XGBoostJobReplicaTypeMaster].Template.Spec.NodeSelector = make(map[string]string)
	}
	if j.Spec.XGBReplicaSpecs[kftraining.XGBoostJobReplicaTypeWorker].Template.Spec.NodeSelector == nil {
		j.Spec.XGBReplicaSpecs[kftraining.XGBoostJobReplicaTypeWorker].Template.Spec.NodeSelector = make(map[string]string)
	}
	j.Spec.XGBReplicaSpecs[kftraining.XGBoostJobReplicaTypeMaster].Template.Spec.NodeSelector[k] = v
	j.Spec.XGBReplicaSpecs[kftraining.XGBoostJobReplicaTypeWorker].Template.Spec.NodeSelector[k] = v
	return j
}

// Active updates the replicaStatus for Active of job.
func (j *XGBoostJobWrapper) Active(rType kftraining.ReplicaType, c int32) *XGBoostJobWrapper {
	if j.Status.ReplicaStatuses == nil {
		j.Status.ReplicaStatuses = make(map[kftraining.ReplicaType]*kftraining.ReplicaStatus)
	}
	j.Status.ReplicaStatuses[rType] = &kftraining.ReplicaStatus{
		Active: c,
	}
	return j
}

// StatusConditions updates status conditions of the XGBoostJob.
func (j *XGBoostJobWrapper) StatusConditions(conditions ...kftraining.JobCondition) *XGBoostJobWrapper {
	j.Status.Conditions = conditions
	return j
}
