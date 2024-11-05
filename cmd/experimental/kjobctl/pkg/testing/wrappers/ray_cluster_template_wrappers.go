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

// RayClusterTemplateWrapper wraps a RayClusterTemplate.
type RayClusterTemplateWrapper struct{ v1alpha1.RayClusterTemplate }

// MakeRayClusterTemplate creates a wrapper for a RayClusterTemplate
func MakeRayClusterTemplate(name, ns string) *RayClusterTemplateWrapper {
	return &RayClusterTemplateWrapper{
		RayClusterTemplate: v1alpha1.RayClusterTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
		},
	}
}

// Obj returns the inner RayClusterTemplate.
func (w *RayClusterTemplateWrapper) Obj() *v1alpha1.RayClusterTemplate {
	return &w.RayClusterTemplate
}

// Clone RayClusterTemplateWrapper.
func (w *RayClusterTemplateWrapper) Clone() *RayClusterTemplateWrapper {
	return &RayClusterTemplateWrapper{
		RayClusterTemplate: *w.RayClusterTemplate.DeepCopy(),
	}
}

// Label sets the label key and value.
func (w *RayClusterTemplateWrapper) Label(key, value string) *RayClusterTemplateWrapper {
	if w.Template.ObjectMeta.Labels == nil {
		w.Template.ObjectMeta.Labels = make(map[string]string)
	}
	w.Template.ObjectMeta.Labels[key] = value
	return w
}

// Annotation sets the label key and value.
func (w *RayClusterTemplateWrapper) Annotation(key, value string) *RayClusterTemplateWrapper {
	if w.Template.ObjectMeta.Annotations == nil {
		w.Template.ObjectMeta.Annotations = make(map[string]string)
	}
	w.Template.ObjectMeta.Annotations[key] = value
	return w
}

// Spec set entrypoint.
func (w *RayClusterTemplateWrapper) Spec(spec rayv1.RayClusterSpec) *RayClusterTemplateWrapper {
	w.Template.Spec = spec
	return w
}
