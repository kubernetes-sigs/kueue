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
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
)

// RayJobTemplateWrapper wraps a RayJobTemplate.
type RayJobTemplateWrapper struct{ v1alpha1.RayJobTemplate }

// MakeRayJobTemplate creates a wrapper for a RayJobTemplate
func MakeRayJobTemplate(name, ns string) *RayJobTemplateWrapper {
	return &RayJobTemplateWrapper{
		RayJobTemplate: v1alpha1.RayJobTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
		},
	}
}

// Obj returns the inner RayJobTemplate.
func (w *RayJobTemplateWrapper) Obj() *v1alpha1.RayJobTemplate {
	return &w.RayJobTemplate
}

// Clone RayJobTemplateWrapper.
func (w *RayJobTemplateWrapper) Clone() *RayJobTemplateWrapper {
	return &RayJobTemplateWrapper{
		RayJobTemplate: *w.RayJobTemplate.DeepCopy(),
	}
}

// Label sets the label key and value.
func (w *RayJobTemplateWrapper) Label(key, value string) *RayJobTemplateWrapper {
	if w.Template.ObjectMeta.Labels == nil {
		w.Template.ObjectMeta.Labels = make(map[string]string)
	}
	w.Template.ObjectMeta.Labels[key] = value
	return w
}

// Annotation sets the label key and value.
func (w *RayJobTemplateWrapper) Annotation(key, value string) *RayJobTemplateWrapper {
	if w.Template.ObjectMeta.Annotations == nil {
		w.Template.ObjectMeta.Annotations = make(map[string]string)
	}
	w.Template.ObjectMeta.Annotations[key] = value
	return w
}

// Entrypoint set entrypoint.
func (w *RayJobTemplateWrapper) Entrypoint(entrypoint string) *RayJobTemplateWrapper {
	w.Template.Spec.Entrypoint = entrypoint
	return w
}

// WithRayClusterSpec set entrypoint.
func (w *RayJobTemplateWrapper) WithRayClusterSpec(spec *rayv1.RayClusterSpec) *RayJobTemplateWrapper {
	w.Template.Spec.RayClusterSpec = spec
	return w
}
