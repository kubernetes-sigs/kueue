/*
Copyright 2024 The Kubernetes Authors.

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

package wrappers

import (
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/constants"
	kueueconstants "sigs.k8s.io/kueue/pkg/controller/constants"
)

// JobWrapper wraps a Job.
type JobWrapper struct{ batchv1.Job }

// MakeJob creates a wrapper for a Job
func MakeJob(name, ns string) *JobWrapper {
	return &JobWrapper{
		batchv1.Job{
			TypeMeta: metav1.TypeMeta{Kind: "Job", APIVersion: "batch/v1"},
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
		},
	}
}

// Obj returns the inner Job.
func (j *JobWrapper) Obj() *batchv1.Job {
	return &j.Job
}

// GenerateName updates generateName.
func (j *JobWrapper) GenerateName(v string) *JobWrapper {
	j.ObjectMeta.GenerateName = v
	return j
}

// Completions updates job completions.
func (j *JobWrapper) Completions(v int32) *JobWrapper {
	j.Job.Spec.Completions = ptr.To(v)
	return j
}

// Parallelism updates job parallelism.
func (j *JobWrapper) Parallelism(v int32) *JobWrapper {
	j.Job.Spec.Parallelism = ptr.To(v)
	return j
}

// Profile sets the profile label.
func (j *JobWrapper) Profile(v string) *JobWrapper {
	return j.Label(constants.ProfileLabel, v)
}

// LocalQueue sets the localqueue label.
func (j *JobWrapper) LocalQueue(v string) *JobWrapper {
	return j.Label(kueueconstants.QueueLabel, v)
}

// Label sets the label key and value.
func (j *JobWrapper) Label(key, value string) *JobWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.ObjectMeta.Labels[key] = value
	return j
}

// WithContainer add container on the pod template.
func (j *JobWrapper) WithContainer(container corev1.Container) *JobWrapper {
	j.Job.Spec.Template.Spec.Containers = append(j.Job.Spec.Template.Spec.Containers, container)
	return j
}

// WithEnvVar add volume to the pod template.
func (j *JobWrapper) WithEnvVar(envVar corev1.EnvVar) *JobWrapper {
	for index := range j.Job.Spec.Template.Spec.InitContainers {
		j.Job.Spec.Template.Spec.InitContainers[index].Env =
			append(j.Job.Spec.Template.Spec.InitContainers[index].Env, envVar)
	}
	for index := range j.Job.Spec.Template.Spec.Containers {
		j.Job.Spec.Template.Spec.Containers[index].Env =
			append(j.Job.Spec.Template.Spec.Containers[index].Env, envVar)
	}
	return j
}

// RestartPolicy updates the restartPolicy on the pod template.
func (j *JobWrapper) RestartPolicy(restartPolicy corev1.RestartPolicy) *JobWrapper {
	j.Job.Spec.Template.Spec.RestartPolicy = restartPolicy
	return j
}

// CreationTimestamp sets the .metadata.creationTimestamp
func (j *JobWrapper) CreationTimestamp(t time.Time) *JobWrapper {
	j.ObjectMeta.CreationTimestamp = metav1.NewTime(t)
	return j
}

// StartTime sets the .status.startTime
func (j *JobWrapper) StartTime(t time.Time) *JobWrapper {
	j.Status.StartTime = &metav1.Time{Time: t}
	return j
}

// CompletionTime sets the .status.completionTime
func (j *JobWrapper) CompletionTime(t time.Time) *JobWrapper {
	j.Status.CompletionTime = &metav1.Time{Time: t}
	return j
}

// Succeeded sets the .status.succeeded
func (j *JobWrapper) Succeeded(value int32) *JobWrapper {
	j.Status.Succeeded = value
	return j
}

// Spec set job spec.
func (j *JobWrapper) Spec(spec batchv1.JobSpec) *JobWrapper {
	j.Job.Spec = spec
	return j
}
