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

package jobset

import (
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	jobsetapi "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	jobsetutil "sigs.k8s.io/jobset/pkg/util/testing"

	"sigs.k8s.io/kueue/pkg/controller/constants"
)

// JobSetWrapper wraps a JobSet.
type JobSetWrapper struct{ jobsetapi.JobSet }

// TestPodSpec is the default pod spec used for testing.
var TestPodSpec = corev1.PodSpec{
	RestartPolicy: corev1.RestartPolicyNever,
	Containers: []corev1.Container{
		{
			Name:  "test-container",
			Image: "busybox:latest",
		},
	},
}

type ReplicatedJobRequirements struct {
	Name           string
	Replicas       int32
	Parallelism    int32
	Completions    int32
	Annotations    map[string]string
	PodAnnotations map[string]string
	Image          string
	Args           []string
}

// MakeJobSet creates a wrapper for a suspended JobSet
func MakeJobSet(name, ns string) *JobSetWrapper {
	jobSetWrapper := *jobsetutil.MakeJobSet(name, ns)
	return &JobSetWrapper{*jobSetWrapper.Suspend(true).Obj()}
}

// Obj returns the inner JobSet.
func (j *JobSetWrapper) Obj() *jobsetapi.JobSet {
	return &j.JobSet
}

// DeepCopy returns a DeepCopy of j.
func (j *JobSetWrapper) DeepCopy() *JobSetWrapper {
	return &JobSetWrapper{JobSet: *j.JobSet.DeepCopy()}
}

// ReplicatedJobs sets a new set of ReplicatedJobs in the inner JobSet.
func (j *JobSetWrapper) ReplicatedJobs(replicatedJobs ...ReplicatedJobRequirements) *JobSetWrapper {
	j.Spec.ReplicatedJobs = make([]jobsetapi.ReplicatedJob, len(replicatedJobs))
	for index, req := range replicatedJobs {
		jt := jobsetutil.MakeJobTemplate("", "").PodSpec(TestPodSpec).Obj()
		jt.Annotations = req.Annotations
		jt.Spec.Parallelism = ptr.To(req.Parallelism)
		jt.Spec.Completions = ptr.To(req.Completions)
		jt.Spec.Template.Annotations = req.PodAnnotations
		if len(req.Image) > 0 {
			jt.Spec.BackoffLimit = ptr.To[int32](0)
			spec := &jt.Spec.Template.Spec
			spec.RestartPolicy = corev1.RestartPolicyNever
			spec.TerminationGracePeriodSeconds = ptr.To[int64](0)
			spec.Containers = []corev1.Container{
				{
					Name:  "c",
					Image: req.Image,
					Args:  req.Args,
				},
			}
		}
		j.Spec.ReplicatedJobs[index] = jobsetutil.MakeReplicatedJob(req.Name).Job(jt).Replicas(req.Replicas).Obj()
	}
	return j
}

// Suspend updates the suspend status of the JobSet.
func (j *JobSetWrapper) Suspend(s bool) *JobSetWrapper {
	j.Spec.Suspend = ptr.To(s)
	return j
}

// Label sets a label to the JobSet.
func (j *JobSetWrapper) Label(k, v string) *JobSetWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[k] = v
	return j
}

// Annotations sets annotations to the JobSet.
func (j *JobSetWrapper) Annotations(annotations map[string]string) *JobSetWrapper {
	j.ObjectMeta.Annotations = annotations
	return j
}

// Queue updates the queue name of the JobSet.
func (j *JobSetWrapper) Queue(queue string) *JobSetWrapper {
	return j.Label(constants.QueueLabel, queue)
}

// Request adds a resource request to the first container of the target replicatedJob.
func (j *JobSetWrapper) Request(replicatedJobName string, r corev1.ResourceName, v string) *JobSetWrapper {
	for i, replicatedJob := range j.Spec.ReplicatedJobs {
		if replicatedJob.Name == replicatedJobName {
			if j.Spec.ReplicatedJobs[i].Template.Spec.Template.Spec.Containers[0].Resources.Requests == nil {
				j.Spec.ReplicatedJobs[i].Template.Spec.Template.Spec.Containers[0].Resources.Requests = map[corev1.ResourceName]resource.Quantity{}
			}
			j.Spec.ReplicatedJobs[i].Template.Spec.Template.Spec.Containers[0].Resources.Requests[r] = resource.MustParse(v)
		}
	}
	return j
}

// Limit adds a resource limit to the first container of the target replicatedJob.
func (j *JobSetWrapper) Limit(replicatedJobName string, r corev1.ResourceName, v string) *JobSetWrapper {
	for i, replicatedJob := range j.Spec.ReplicatedJobs {
		if replicatedJob.Name == replicatedJobName {
			if j.Spec.ReplicatedJobs[i].Template.Spec.Template.Spec.Containers[0].Resources.Limits == nil {
				j.Spec.ReplicatedJobs[i].Template.Spec.Template.Spec.Containers[0].Resources.Limits = map[corev1.ResourceName]resource.Quantity{}
			}
			j.Spec.ReplicatedJobs[i].Template.Spec.Template.Spec.Containers[0].Resources.Limits[r] = resource.MustParse(v)
		}
	}
	return j
}

// PriorityClass updates JobSet priorityclass.
func (j *JobSetWrapper) PriorityClass(pc string) *JobSetWrapper {
	for i := range j.Spec.ReplicatedJobs {
		j.Spec.ReplicatedJobs[i].Template.Spec.Template.Spec.PriorityClassName = pc
	}
	return j
}

// WorkloadPriorityClass updates JobSet workloadpriorityclass.
func (j *JobSetWrapper) WorkloadPriorityClass(wpc string) *JobSetWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[constants.WorkloadPriorityClassLabel] = wpc
	return j
}

// JobsStatus updates JobSet status.
func (j *JobSetWrapper) JobsStatus(statuses ...jobsetapi.ReplicatedJobStatus) *JobSetWrapper {
	j.Status.ReplicatedJobsStatus = statuses
	return j
}

// Condition adds a condition
func (j *JobSetWrapper) Condition(c metav1.Condition) *JobSetWrapper {
	apimeta.SetStatusCondition(&j.Status.Conditions, c)
	return j
}

// ManagedBy adds a managedby.
func (j *JobSetWrapper) ManagedBy(c string) *JobSetWrapper {
	j.Spec.ManagedBy = &c
	return j
}
