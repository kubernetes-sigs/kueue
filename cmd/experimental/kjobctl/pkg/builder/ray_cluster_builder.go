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

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type rayClusterBuilder struct {
	*Builder
}

var _ builder = (*rayClusterBuilder)(nil)

func (b *rayClusterBuilder) build(ctx context.Context) (runtime.Object, []runtime.Object, error) {
	template, err := b.kjobctlClientset.KjobctlV1alpha1().RayClusterTemplates(b.profile.Namespace).
		Get(ctx, string(b.mode.Template), metav1.GetOptions{})
	if err != nil {
		return nil, nil, err
	}

	rayCluster := &rayv1.RayCluster{
		TypeMeta: metav1.TypeMeta{
			Kind:       "RayCluster",
			APIVersion: "ray.io/v1",
		},
		ObjectMeta: b.buildObjectMeta(template.Template.ObjectMeta, false),
		Spec:       template.Template.Spec,
	}

	b.buildRayClusterSpec(&rayCluster.Spec)

	return rayCluster, nil, nil
}

func newRayClusterBuilder(b *Builder) *rayClusterBuilder {
	return &rayClusterBuilder{Builder: b}
}
