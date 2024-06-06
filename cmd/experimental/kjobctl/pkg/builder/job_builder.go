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

package builder

import (
	"context"
	"slices"

	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/constants"
	kueueconstants "sigs.k8s.io/kueue/pkg/controller/constants"
)

type jobBuilder struct {
	*Builder
}

var _ builder = (*jobBuilder)(nil)

func (b *jobBuilder) build(ctx context.Context) (runtime.Object, error) {
	template, err := b.kjobctlClientset.KjobctlV1alpha1().JobTemplates(b.profile.Namespace).
		Get(ctx, string(b.mode.Template), metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	job := &batchv1.Job{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Job",
			APIVersion: "batch/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    b.profile.Namespace,
			GenerateName: b.profile.Name + "-",
			Labels:       map[string]string{},
		},
		Spec: template.Template.Spec,
	}

	if b.profile != nil {
		job.Labels[constants.ProfileLabel] = b.profile.Name
	}

	for _, vb := range b.volumeBundles {
		for _, volume := range vb.Spec.Volumes {
			index := slices.IndexFunc(job.Spec.Template.Spec.Volumes, func(v v1.Volume) bool {
				return v.Name == volume.Name
			})
			if index != -1 {
				job.Spec.Template.Spec.Volumes[index] = volume
			} else {
				job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, volume)
			}
		}
	}

	for i := range job.Spec.Template.Spec.Containers {
		container := &job.Spec.Template.Spec.Containers[i]

		if len(b.command) > 0 {
			container.Command = b.command
		}

		if len(b.requests) > 0 {
			container.Resources.Requests = b.requests
		}

		for _, vb := range b.volumeBundles {
			for _, volumeMount := range vb.Spec.ContainerVolumeMounts {
				index := slices.IndexFunc(container.VolumeMounts, func(vm v1.VolumeMount) bool {
					return vm.Name == volumeMount.Name
				})
				if index != -1 {
					container.VolumeMounts[index] = volumeMount
				} else {
					container.VolumeMounts = append(container.VolumeMounts, volumeMount)
				}
			}

			for _, envVar := range vb.Spec.EnvVars {
				index := slices.IndexFunc(container.Env, func(ev v1.EnvVar) bool {
					return ev.Name == envVar.Name
				})
				if index != -1 {
					container.Env[index] = envVar
				} else {
					container.Env = append(container.Env, envVar)
				}
			}
		}
	}

	if b.parallelism != nil {
		job.Spec.Parallelism = b.parallelism
	}

	if b.completions != nil {
		job.Spec.Completions = b.completions
	}

	if len(b.localQueue) > 0 {
		job.ObjectMeta.Labels[kueueconstants.QueueLabel] = b.localQueue
	}

	return job, nil
}

func newJobBuilder(b *Builder) *jobBuilder {
	return &jobBuilder{Builder: b}
}
