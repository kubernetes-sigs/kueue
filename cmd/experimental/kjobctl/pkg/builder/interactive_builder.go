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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type interactiveBuilder struct {
	*Builder
}

var _ builder = (*interactiveBuilder)(nil)

func (b *interactiveBuilder) build(ctx context.Context) (runtime.Object, []runtime.Object, error) {
	template, err := b.k8sClientset.CoreV1().PodTemplates(b.profile.Namespace).Get(ctx, string(b.mode.Template), metav1.GetOptions{})
	if err != nil {
		return nil, nil, err
	}

	pod := &corev1.Pod{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Pod",
			APIVersion: "v1",
		},
		ObjectMeta: b.buildObjectMeta(template.Template.ObjectMeta, false),
		Spec:       template.Template.Spec,
	}

	pod.Spec = b.buildPodSpec(pod.Spec)

	if len(pod.Spec.Containers) > 0 {
		pod.Spec.Containers[0].TTY = true
		pod.Spec.Containers[0].Stdin = true
	}

	return pod, nil, nil
}

func newInteractiveBuilder(b *Builder) *interactiveBuilder {
	return &interactiveBuilder{Builder: b}
}
