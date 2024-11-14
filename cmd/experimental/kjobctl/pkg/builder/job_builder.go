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

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type jobBuilder struct {
	*Builder
}

var _ builder = (*jobBuilder)(nil)

func (b *jobBuilder) build(ctx context.Context) (runtime.Object, []runtime.Object, error) {
	template, err := b.kjobctlClientset.KjobctlV1alpha1().JobTemplates(b.profile.Namespace).
		Get(ctx, string(b.mode.Template), metav1.GetOptions{})
	if err != nil {
		return nil, nil, err
	}

	objectMeta, err := b.buildObjectMeta(template.Template.ObjectMeta, false)
	if err != nil {
		return nil, nil, err
	}

	job := &batchv1.Job{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Job",
			APIVersion: "batch/v1",
		},
		ObjectMeta: objectMeta,
		Spec:       template.Template.Spec,
	}

	job.Spec.Template.Spec = b.buildPodSpec(job.Spec.Template.Spec)

	if b.parallelism != nil {
		job.Spec.Parallelism = b.parallelism
	}

	if b.completions != nil {
		job.Spec.Completions = b.completions
	}

	return job, nil, nil
}

func newJobBuilder(b *Builder) *jobBuilder {
	return &jobBuilder{Builder: b}
}
