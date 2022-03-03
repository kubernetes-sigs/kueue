/*
Copyright 2022 The Kubernetes Authors.

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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"

	kueue "sigs.k8s.io/kueue/api/v1alpha1"
	"sigs.k8s.io/kueue/pkg/constants"
)

// JobWrapper wraps a Job.
type JobWrapper struct{ batchv1.Job }

// MakeJob creates a wrapper for a suspended job with a single container and parallelism=1.
func MakeJob(name, ns string) *JobWrapper {
	return &JobWrapper{batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Annotations: make(map[string]string, 1),
		},
		Spec: batchv1.JobSpec{
			Parallelism: pointer.Int32Ptr(1),
			Suspend:     pointer.BoolPtr(true),
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
	}}
}

// Obj returns the inner Job.
func (j *JobWrapper) Obj() *batchv1.Job {
	return &j.Job
}

// Suspend updates the suspend status of the job
func (j *JobWrapper) Suspend(s bool) *JobWrapper {
	j.Spec.Suspend = pointer.BoolPtr(s)
	return j
}

// Parallelism updates job parallelism.
func (j *JobWrapper) Parallelism(p int32) *JobWrapper {
	j.Spec.Parallelism = pointer.Int32Ptr(p)
	return j
}

// Queue updates the queue name of the job
func (j *JobWrapper) Queue(queue string) *JobWrapper {
	j.Annotations[constants.QueueAnnotation] = queue
	return j
}

// Toleration adds a toleration to the job.
func (j *JobWrapper) Toleration(t corev1.Toleration) *JobWrapper {
	j.Spec.Template.Spec.Tolerations = append(j.Spec.Template.Spec.Tolerations, t)
	return j
}

// NodeSelector adds a node selector to the job.
func (j *JobWrapper) NodeSelector(k, v string) *JobWrapper {
	j.Spec.Template.Spec.NodeSelector[k] = v
	return j
}

// Request adds a resource request to the default container.
func (j *JobWrapper) Request(r corev1.ResourceName, v string) *JobWrapper {
	j.Spec.Template.Spec.Containers[0].Resources.Requests[r] = resource.MustParse(v)
	return j
}

type QueuedWorkloadWrapper struct{ kueue.QueuedWorkload }

// MakeQueuedWorkload creates a wrapper for a QueuedWorkload with a single
// pod with a single container.
func MakeQueuedWorkload(name, ns string) *QueuedWorkloadWrapper {
	return &QueuedWorkloadWrapper{kueue.QueuedWorkload{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: kueue.QueuedWorkloadSpec{
			Pods: []kueue.PodSet{
				{
					Count: 1,
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "c",
								Resources: corev1.ResourceRequirements{
									Requests: make(corev1.ResourceList),
								},
							},
						},
					},
					AssignedFlavors: make(map[corev1.ResourceName]string),
				},
			},
		},
	}}
}

func (w *QueuedWorkloadWrapper) Obj() *kueue.QueuedWorkload {
	return &w.QueuedWorkload
}

func (w *QueuedWorkloadWrapper) Request(r corev1.ResourceName, q string) *QueuedWorkloadWrapper {
	w.Spec.Pods[0].Spec.Containers[0].Resources.Requests[r] = resource.MustParse(q)
	return w
}

func (w *QueuedWorkloadWrapper) AssignFlavor(r corev1.ResourceName, f string) *QueuedWorkloadWrapper {
	w.Spec.Pods[0].AssignedFlavors[r] = f
	return w
}

// QueueWrapper wraps a Queue.
type QueueWrapper struct{ kueue.Queue }

// MakeQueue creates a wrapper for a Queue.
func MakeQueue(name, ns string) *QueueWrapper {
	return &QueueWrapper{kueue.Queue{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
	}}
}

// Obj returns the inner Queue.
func (q *QueueWrapper) Obj() *kueue.Queue {
	return &q.Queue
}

// Capacity updates the capacity the queue points to.
func (q *QueueWrapper) Capacity(c string) *QueueWrapper {
	q.Spec.Capacity = kueue.CapacityReference(c)
	return q
}

// CapacityWrapper wraps a Capacity.
type CapacityWrapper struct{ kueue.Capacity }

// MakeCapacity creates a wrapper for a Capacity.
func MakeCapacity(name string) *CapacityWrapper {
	return &CapacityWrapper{kueue.Capacity{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}}
}

// Obj returns the inner Capacity.
func (c *CapacityWrapper) Obj() *kueue.Capacity {
	return &c.Capacity
}

// Cohort sets the borrowing cohort.
func (c *CapacityWrapper) Cohort(cohort string) *CapacityWrapper {
	c.Spec.Cohort = cohort
	return c
}

// QueueingStrategy sets the the queueing strategy in this Capacity.
func (c *CapacityWrapper) QueueingStrategy(strategy kueue.QueueingStrategy) *CapacityWrapper {
	c.Spec.QueueingStrategy = strategy
	return c
}

// Resource adds a resource with flavors to the capacity.
func (c *CapacityWrapper) Resource(r *kueue.Resource) *CapacityWrapper {
	c.Spec.RequestableResources = append(c.Spec.RequestableResources, *r)
	return c
}

// ResourceWrapper wraps a requestable resource.
type ResourceWrapper struct{ kueue.Resource }

// MakeResource creates a wrapper for a requestable resource.
func MakeResource(name corev1.ResourceName) *ResourceWrapper {
	return &ResourceWrapper{kueue.Resource{
		Name: name,
	}}
}

// Obj returns the inner resource.
func (r *ResourceWrapper) Obj() *kueue.Resource {
	return &r.Resource
}

// Flavor appends a flavor.
func (r *ResourceWrapper) Flavor(f *kueue.ResourceFlavor) *ResourceWrapper {
	r.Flavors = append(r.Flavors, *f)
	return r
}

// FlavorWrapper wraps a resource flavor.
type FlavorWrapper struct{ kueue.ResourceFlavor }

// MakeFlavor creates a wrapper for a resource flavor.
func MakeFlavor(name, guaranteed string) *FlavorWrapper {
	return &FlavorWrapper{kueue.ResourceFlavor{
		Name: name,
		Quota: kueue.Quota{
			Guaranteed: resource.MustParse(guaranteed),
			Ceiling:    resource.MustParse(guaranteed),
		},
		Labels: map[string]string{},
	}}
}

// Obj returns the inner resource flavor.
func (f *FlavorWrapper) Obj() *kueue.ResourceFlavor {
	return &f.ResourceFlavor
}

// Ceiling updates the flavor ceiling.
func (f *FlavorWrapper) Ceiling(c string) *FlavorWrapper {
	f.Quota.Ceiling = resource.MustParse(c)
	return f
}

// Label adds a label to the flavor.
func (f *FlavorWrapper) Label(k, v string) *FlavorWrapper {
	f.Labels[k] = v
	return f
}

// Taint adds a taint to the flavor.
func (f *FlavorWrapper) Taint(t corev1.Taint) *FlavorWrapper {
	f.Taints = append(f.Taints, t)
	return f
}
