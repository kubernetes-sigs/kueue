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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
)

// JobTemplateWrapper wraps a JobTemplate.
type JobTemplateWrapper struct{ v1alpha1.JobTemplate }

// MakeJobTemplate creates a wrapper for a JobTemplate
func MakeJobTemplate(name, ns string) *JobTemplateWrapper {
	return &JobTemplateWrapper{
		JobTemplate: v1alpha1.JobTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
		},
	}
}

// Obj returns the inner JobTemplate.
func (j *JobTemplateWrapper) Obj() *v1alpha1.JobTemplate {
	return &j.JobTemplate
}

// Completions updates the completions on the job template.
func (j *JobTemplateWrapper) Completions(completion int32) *JobTemplateWrapper {
	j.Template.Spec.Completions = ptr.To(completion)
	return j
}

// Parallelism updates the parallelism on the job template.
func (j *JobTemplateWrapper) Parallelism(parallelism int32) *JobTemplateWrapper {
	j.Template.Spec.Parallelism = ptr.To(parallelism)
	return j
}

// RestartPolicy updates the restartPolicy on the pod template.
func (j *JobTemplateWrapper) RestartPolicy(restartPolicy corev1.RestartPolicy) *JobTemplateWrapper {
	j.Template.Spec.Template.Spec.RestartPolicy = restartPolicy
	return j
}

// WithContainer add container to the pod template.
func (j *JobTemplateWrapper) WithContainer(container corev1.Container) *JobTemplateWrapper {
	j.Template.Spec.Template.Spec.Containers = append(j.Template.Spec.Template.Spec.Containers, container)
	return j
}
