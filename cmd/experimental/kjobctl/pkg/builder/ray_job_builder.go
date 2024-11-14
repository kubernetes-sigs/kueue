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
	"strings"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	rayutil "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kueueconstants "sigs.k8s.io/kueue/pkg/controller/constants"
)

type rayJobBuilder struct {
	*Builder
}

var _ builder = (*rayJobBuilder)(nil)

func (b *rayJobBuilder) build(ctx context.Context) (runtime.Object, []runtime.Object, error) {
	template, err := b.kjobctlClientset.KjobctlV1alpha1().RayJobTemplates(b.profile.Namespace).
		Get(ctx, string(b.mode.Template), metav1.GetOptions{})
	if err != nil {
		return nil, nil, err
	}

	objectMeta, err := b.buildObjectMeta(template.Template.ObjectMeta, false)
	if err != nil {
		return nil, nil, err
	}

	rayJob := &rayv1.RayJob{
		TypeMeta: metav1.TypeMeta{
			Kind:       "RayJob",
			APIVersion: "ray.io/v1",
		},
		ObjectMeta: objectMeta,
		Spec:       template.Template.Spec,
	}

	if b.command != nil {
		rayJob.Spec.Entrypoint = strings.Join(b.command, " ")
	}

	if len(b.rayCluster) != 0 {
		delete(rayJob.ObjectMeta.Labels, kueueconstants.QueueLabel)
		rayJob.Spec.RayClusterSpec = nil
		if rayJob.Spec.ClusterSelector == nil {
			rayJob.Spec.ClusterSelector = map[string]string{}
		}
		rayJob.Spec.ClusterSelector[rayutil.RayClusterLabelKey] = b.rayCluster
	} else if rayJob.Spec.RayClusterSpec != nil {
		b.buildRayClusterSpec(rayJob.Spec.RayClusterSpec)
	}

	return rayJob, nil, nil
}

func newRayJobBuilder(b *Builder) *rayJobBuilder {
	return &rayJobBuilder{Builder: b}
}
