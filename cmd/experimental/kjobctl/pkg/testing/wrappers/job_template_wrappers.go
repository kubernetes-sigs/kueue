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
	"k8s.io/apimachinery/pkg/api/resource"
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

// WithInitContainer add init container to the pod template.
func (j *JobTemplateWrapper) WithInitContainer(container corev1.Container) *JobTemplateWrapper {
	j.Template.Spec.Template.Spec.InitContainers = append(j.Template.Spec.Template.Spec.InitContainers, container)
	return j
}

// WithContainer add container to the pod template.
func (j *JobTemplateWrapper) WithContainer(container corev1.Container) *JobTemplateWrapper {
	j.Template.Spec.Template.Spec.Containers = append(j.Template.Spec.Template.Spec.Containers, container)
	return j
}

// Clone clone JobTemplateWrapper.
func (j *JobTemplateWrapper) Clone() *JobTemplateWrapper {
	return &JobTemplateWrapper{
		JobTemplate: *j.JobTemplate.DeepCopy(),
	}
}

// WithVolume add volume to the job template.
func (j *JobTemplateWrapper) WithVolume(name, localObjectReferenceName string) *JobTemplateWrapper {
	j.Template.Spec.Template.Spec.Volumes = append(j.Template.Spec.Template.Spec.Volumes, corev1.Volume{
		Name: name,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: localObjectReferenceName,
				},
			},
		},
	})
	return j
}

// WithEnvVar add volume to the job template.
func (j *JobTemplateWrapper) WithEnvVar(envVar corev1.EnvVar) *JobTemplateWrapper {
	for index := range j.Template.Spec.Template.Spec.InitContainers {
		j.Template.Spec.Template.Spec.InitContainers[index].Env =
			append(j.Template.Spec.Template.Spec.InitContainers[index].Env, envVar)
	}
	for index := range j.Template.Spec.Template.Spec.Containers {
		j.Template.Spec.Template.Spec.Containers[index].Env =
			append(j.Template.Spec.Template.Spec.Containers[index].Env, envVar)
	}
	return j
}

// WithVolumeMount add volume mount to pod templates.
func (j *JobTemplateWrapper) WithVolumeMount(volumeMount corev1.VolumeMount) *JobTemplateWrapper {
	for index := range j.Template.Spec.Template.Spec.InitContainers {
		j.Template.Spec.Template.Spec.InitContainers[index].VolumeMounts =
			append(j.Template.Spec.Template.Spec.InitContainers[index].VolumeMounts, volumeMount)
	}
	for index := range j.Template.Spec.Template.Spec.Containers {
		j.Template.Spec.Template.Spec.Containers[index].VolumeMounts =
			append(j.Template.Spec.Template.Spec.Containers[index].VolumeMounts, volumeMount)
	}
	return j
}

// Command set command to primary pod templates.
func (j *JobTemplateWrapper) Command(command []string) *JobTemplateWrapper {
	if len(j.Template.Spec.Template.Spec.Containers) > 0 {
		j.Template.Spec.Template.Spec.Containers[0].Command = command
	}
	return j
}

// WithRequest set command to primary pod templates.
func (j *JobTemplateWrapper) WithRequest(key corev1.ResourceName, value resource.Quantity) *JobTemplateWrapper {
	if len(j.Template.Spec.Template.Spec.Containers) > 0 {
		if j.Template.Spec.Template.Spec.Containers[0].Resources.Requests == nil {
			j.Template.Spec.Template.Spec.Containers[0].Resources.Requests = make(corev1.ResourceList)
		}
		j.Template.Spec.Template.Spec.Containers[0].Resources.Requests[key] = value
	}
	return j
}
