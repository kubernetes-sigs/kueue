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

package raycluster

import (
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/util/testing"
)

// ClusterWrapper wraps a RayCluster.
type ClusterWrapper struct{ rayv1.RayCluster }

// MakeCluster creates a wrapper for rayCluster
func MakeCluster(name, ns string) *ClusterWrapper {
	return &ClusterWrapper{rayv1.RayCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Annotations: make(map[string]string, 1),
		},
		Spec: rayv1.RayClusterSpec{
			RayVersion: testing.TestRayVersion(),
			HeadGroupSpec: rayv1.HeadGroupSpec{
				RayStartParams: map[string]string{},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						NodeSelector: map[string]string{},
						Containers: []corev1.Container{
							{
								Name:    "head-container",
								Command: []string{},
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{},
									Limits:   corev1.ResourceList{},
								},
							},
						},
					},
				},
			},
			WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
				{
					GroupName:      "workers-group-0",
					Replicas:       ptr.To[int32](1),
					MinReplicas:    ptr.To[int32](0),
					MaxReplicas:    ptr.To[int32](10),
					RayStartParams: map[string]string{},
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							NodeSelector: map[string]string{},
							Containers: []corev1.Container{
								{
									Name:    "worker-container",
									Command: []string{},
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{},
										Limits:   corev1.ResourceList{},
									},
								},
							},
						},
					},
				},
			},
			Suspend: ptr.To(true),
		},
	}}
}

// NodeSelectorHeadGroup adds a node selector to the job's head.
func (j *ClusterWrapper) NodeSelectorHeadGroup(k, v string) *ClusterWrapper {
	j.Spec.HeadGroupSpec.Template.Spec.NodeSelector[k] = v
	return j
}

// Obj returns the inner Job.
func (j *ClusterWrapper) Obj() *rayv1.RayCluster {
	return &j.RayCluster
}

// Suspend updates the suspend status of the job
func (j *ClusterWrapper) Suspend(s bool) *ClusterWrapper {
	j.Spec.Suspend = &s
	return j
}

func (j *ClusterWrapper) RequestWorkerGroup(name corev1.ResourceName, quantity string) *ClusterWrapper {
	c := &j.Spec.WorkerGroupSpecs[0].Template.Spec.Containers[0]
	if c.Resources.Requests == nil {
		c.Resources.Requests = corev1.ResourceList{name: resource.MustParse(quantity)}
	} else {
		c.Resources.Requests[name] = resource.MustParse(quantity)
	}
	return j
}

func (j *ClusterWrapper) RequestHead(name corev1.ResourceName, quantity string) *ClusterWrapper {
	c := &j.Spec.HeadGroupSpec.Template.Spec.Containers[0]
	if c.Resources.Requests == nil {
		c.Resources.Requests = corev1.ResourceList{name: resource.MustParse(quantity)}
	} else {
		c.Resources.Requests[name] = resource.MustParse(quantity)
	}
	return j
}

// Queue updates the queue name of the job
func (j *ClusterWrapper) Queue(queue string) *ClusterWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[constants.QueueLabel] = queue
	return j
}

// Clone returns deep copy of the Job.
func (j *ClusterWrapper) Clone() *ClusterWrapper {
	return &ClusterWrapper{RayCluster: *j.DeepCopy()}
}

func (j *ClusterWrapper) WithEnableAutoscaling(value *bool) *ClusterWrapper {
	j.Spec.EnableInTreeAutoscaling = value
	return j
}

func (j *ClusterWrapper) WithWorkerGroups(workers ...rayv1.WorkerGroupSpec) *ClusterWrapper {
	j.Spec.WorkerGroupSpecs = workers
	return j
}

func (j *ClusterWrapper) WithHeadGroupSpec(value rayv1.HeadGroupSpec) *ClusterWrapper {
	j.Spec.HeadGroupSpec = value
	return j
}

func (j *ClusterWrapper) WithPriorityClassName(value string) *ClusterWrapper {
	j.Spec.HeadGroupSpec.Template.Spec.PriorityClassName = value
	return j
}

func (j *ClusterWrapper) WithWorkerPriorityClassName(value string) *ClusterWrapper {
	j.Spec.WorkerGroupSpecs[0].Template.Spec.PriorityClassName = value
	return j
}

func (j *ClusterWrapper) WithNumOfHosts(groupName string, value int32) *ClusterWrapper {
	for index, group := range j.Spec.WorkerGroupSpecs {
		if group.GroupName == groupName {
			j.Spec.WorkerGroupSpecs[index].NumOfHosts = value
		}
	}
	return j
}

// WorkloadPriorityClass updates job workloadpriorityclass.
func (j *ClusterWrapper) WorkloadPriorityClass(wpc string) *ClusterWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[constants.WorkloadPriorityClassLabel] = wpc
	return j
}

// Label sets the label key and value
func (j *ClusterWrapper) Label(key, value string) *ClusterWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[key] = value
	return j
}

// StatusConditions adds a condition
func (j *ClusterWrapper) StatusConditions(c metav1.Condition) *ClusterWrapper {
	j.Status.Conditions = append(j.Status.Conditions, c)
	return j
}

// ManagedBy adds a managedby.
func (j *ClusterWrapper) ManagedBy(c string) *ClusterWrapper {
	j.Spec.ManagedBy = &c
	return j
}

// Request adds a resource request to the default container.
func (j *ClusterWrapper) Request(rayType rayv1.RayNodeType, r corev1.ResourceName, v string) *ClusterWrapper {
	if rayType == rayv1.HeadNode {
		j.Spec.HeadGroupSpec.Template.Spec.Containers[0].Resources.Requests[r] = resource.MustParse(v)
	} else if rayType == rayv1.WorkerNode {
		j.Spec.WorkerGroupSpecs[0].Template.Spec.Containers[0].Resources.Requests[r] = resource.MustParse(v)
	}
	return j
}

// Limit adds a resource limit to the default container.
func (j *ClusterWrapper) Limit(rayType rayv1.RayNodeType, r corev1.ResourceName, v string) *ClusterWrapper {
	if rayType == rayv1.HeadNode {
		j.Spec.HeadGroupSpec.Template.Spec.Containers[0].Resources.Limits[r] = resource.MustParse(v)
	} else if rayType == rayv1.WorkerNode {
		j.Spec.WorkerGroupSpecs[0].Template.Spec.Containers[0].Resources.Limits[r] = resource.MustParse(v)
	}
	return j
}

// RequestAndLimit adds a resource request and limit to the default container.
func (j *ClusterWrapper) RequestAndLimit(rayType rayv1.RayNodeType, r corev1.ResourceName, v string) *ClusterWrapper {
	return j.Request(rayType, r, v).Limit(rayType, r, v)
}

func (j *ClusterWrapper) Image(rayType rayv1.RayNodeType, image string, args []string) *ClusterWrapper {
	if rayType == rayv1.HeadNode {
		j.Spec.HeadGroupSpec.Template.Spec.Containers[0].Image = image
		j.Spec.HeadGroupSpec.Template.Spec.Containers[0].Args = args
	} else if rayType == rayv1.WorkerNode {
		j.Spec.WorkerGroupSpecs[0].Template.Spec.Containers[0].Image = image
		j.Spec.WorkerGroupSpecs[0].Template.Spec.Containers[0].Args = args
	}
	return j
}

func (j *ClusterWrapper) RayVersion(rv string) *ClusterWrapper {
	j.Spec.RayVersion = rv
	return j
}
