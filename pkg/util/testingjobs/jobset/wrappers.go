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

package rayjob

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/pointer"
	jobsetapi "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	jobsetutil "sigs.k8s.io/jobset/pkg/util/testing"

	"sigs.k8s.io/kueue/pkg/controller/constants"
)

// JobWrapper wraps a RayJob.
type JobWrapper struct{ jobsetapi.JobSet }

// TestPodSpec is the default pod spec used for testing.
var TestPodSpec = corev1.PodSpec{
	RestartPolicy: "Never",
	Containers: []corev1.Container{
		{
			Name:  "test-container",
			Image: "busybox:latest",
		},
	},
}

type ReplicatedJobRequirements struct {
	Name        string
	Replicas    int
	Parallelism int32
	Completions int32
}

// MakeJobSet creates a wrapper for a suspended rayJob
func MakeJobSet(name, ns string, replicatedJobs []ReplicatedJobRequirements) *JobWrapper {
	jobSetWrapper := *jobsetutil.MakeJobSet(name, ns)
	for index, replicatedJobRequirement := range replicatedJobs {
		jt := jobsetutil.MakeJobTemplate(fmt.Sprintf("test-job-%d", index), ns).PodSpec(TestPodSpec).Obj()
		jt.Spec.Parallelism = pointer.Int32(replicatedJobRequirement.Parallelism)
		jt.Spec.Completions = pointer.Int32(replicatedJobRequirement.Completions)
		jobSetWrapper.ReplicatedJob(jobsetutil.MakeReplicatedJob(replicatedJobRequirement.Name).
			Job(jt).
			Replicas(replicatedJobRequirement.Replicas).
			Obj())
	}
	jt1 := jobsetutil.MakeJobTemplate("test-job-1", ns).PodSpec(TestPodSpec).Obj()
	jt1.Spec.Parallelism = pointer.Int32(1)
	jt1.Spec.Completions = pointer.Int32(1)
	jt2 := jobsetutil.MakeJobTemplate("test-job-2", ns).PodSpec(TestPodSpec).Obj()
	jt2.Spec.Parallelism = pointer.Int32(1)
	jt2.Spec.Completions = pointer.Int32(1)
	return &JobWrapper{*jobSetWrapper.Suspend(true).Obj()}
}

// Obj returns the inner Job.
func (j *JobWrapper) Obj() *jobsetapi.JobSet {
	return &j.JobSet
}

// Suspend updates the suspend status of the job
func (j *JobWrapper) Suspend(s bool) *JobWrapper {
	j.Spec.Suspend = pointer.Bool(s)
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

// Request adds a resource request to the default container.
func (j *JobWrapper) Request(replicatedJobName string, r corev1.ResourceName, v string) *JobWrapper {
	for i, replicatedJob := range j.Spec.ReplicatedJobs {
		if replicatedJob.Name == replicatedJobName {
			_, ok := j.Spec.ReplicatedJobs[i].Template.Spec.Template.Spec.Containers[0].Resources.Requests[r]
			if !ok {
				j.Spec.ReplicatedJobs[i].Template.Spec.Template.Spec.Containers[0].Resources.Requests = map[corev1.ResourceName]resource.Quantity{}
			}
			j.Spec.ReplicatedJobs[i].Template.Spec.Template.Spec.Containers[0].Resources.Requests[r] = resource.MustParse(v)
		}
	}
	return j
}

// PriorityClass updates job priorityclass.
func (j *JobWrapper) PriorityClass(pc string) *JobWrapper {
	for i := range j.Spec.ReplicatedJobs {
		j.Spec.ReplicatedJobs[i].Template.Spec.Template.Spec.PriorityClassName = pc
	}
	return j
}
