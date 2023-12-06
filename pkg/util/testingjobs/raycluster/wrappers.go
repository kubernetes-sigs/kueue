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

package raycluster

import (
	rayclusterapi "github.com/ray-project/kuberay/ray-operator/apis/ray/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/pkg/controller/constants"
)

// JobWrapper wraps a RayCluster.
type JobWrapper struct{ rayclusterapi.RayCluster }

// MakeJob creates a wrapper for rayCluster
func MakeJob(name, ns string) *JobWrapper {
	return &JobWrapper{rayclusterapi.RayCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Annotations: make(map[string]string, 1),
		},
		Spec: rayclusterapi.RayClusterSpec{
			HeadGroupSpec: rayclusterapi.HeadGroupSpec{
				RayStartParams: map[string]string{"p1": "v1"},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "head-container",
							},
						},
					},
				},
			},
			WorkerGroupSpecs: []rayclusterapi.WorkerGroupSpec{
				{
					GroupName:      "workers-group-0",
					Replicas:       ptr.To[int32](1),
					MinReplicas:    ptr.To[int32](0),
					MaxReplicas:    ptr.To[int32](10),
					RayStartParams: map[string]string{"p1": "v1"},
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name: "worker-container",
								},
							},
						},
					},
				},
			},
		},
	}}
}

// Obj returns the inner Job.
func (j *JobWrapper) Obj() *rayclusterapi.RayCluster {
	return &j.RayCluster
}

// Suspend updates the suspend status of the job
func (j *JobWrapper) Suspend(s bool) *JobWrapper {
	//j.Spec.Suspend = s
	return j
}

// Queue updates the queue name of the job
func (j *JobWrapper) Queue(queue string) *JobWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[constants.QueueLabel] = queue
	return j
}

func (j *JobWrapper) RequestWorkerGroup(name corev1.ResourceName, quantity string) *JobWrapper {
	c := &j.Spec.WorkerGroupSpecs[0].Template.Spec.Containers[0]
	if c.Resources.Requests == nil {
		c.Resources.Requests = corev1.ResourceList{name: resource.MustParse(quantity)}
	} else {
		c.Resources.Requests[name] = resource.MustParse(quantity)
	}
	return j
}

func (j *JobWrapper) RequestHead(name corev1.ResourceName, quantity string) *JobWrapper {
	c := &j.Spec.HeadGroupSpec.Template.Spec.Containers[0]
	if c.Resources.Requests == nil {
		c.Resources.Requests = corev1.ResourceList{name: resource.MustParse(quantity)}
	} else {
		c.Resources.Requests[name] = resource.MustParse(quantity)
	}
	return j
}

// Clone returns deep copy of the Job.
func (j *JobWrapper) Clone() *JobWrapper {
	return &JobWrapper{RayCluster: *j.DeepCopy()}
}

func (j *JobWrapper) WithEnableAutoscaling(value *bool) *JobWrapper {
	j.Spec.EnableInTreeAutoscaling = value
	return j
}

func (j *JobWrapper) WithWorkerGroups(workers ...rayclusterapi.WorkerGroupSpec) *JobWrapper {
	j.Spec.WorkerGroupSpecs = workers
	return j
}

func (j *JobWrapper) WithHeadGroupSpec(value rayclusterapi.HeadGroupSpec) *JobWrapper {
	j.Spec.HeadGroupSpec = value
	return j
}

func (j *JobWrapper) WithPriorityClassName(value string) *JobWrapper {
	j.Spec.HeadGroupSpec.Template.Spec.PriorityClassName = value
	return j
}

func (j *JobWrapper) WithWorkerPriorityClassName(value string) *JobWrapper {
	j.Spec.WorkerGroupSpecs[0].Template.Spec.PriorityClassName = value
	return j
}

// WorkloadPriorityClass updates job workloadpriorityclass.
func (j *JobWrapper) WorkloadPriorityClass(wpc string) *JobWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[constants.WorkloadPriorityClassLabel] = wpc
	return j
}
