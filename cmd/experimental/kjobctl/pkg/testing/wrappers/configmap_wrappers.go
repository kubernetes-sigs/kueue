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
	kueueconstants "sigs.k8s.io/kueue/pkg/controller/constants"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/constants"
)

// ConfigMapWrapper wraps a ConfigMap.
type ConfigMapWrapper struct{ corev1.ConfigMap }

// MakeConfigMap creates a wrapper for a ConfigMap
func MakeConfigMap(name, ns string) *ConfigMapWrapper {
	return &ConfigMapWrapper{
		corev1.ConfigMap{
			TypeMeta: metav1.TypeMeta{Kind: "ConfigMap", APIVersion: "v1"},
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
		},
	}
}

// Obj returns the inner ConfigMap.
func (w *ConfigMapWrapper) Obj() *corev1.ConfigMap {
	return &w.ConfigMap
}

// WithOwnerReference adds the owner reference.
func (w *ConfigMapWrapper) WithOwnerReference(ref metav1.OwnerReference) *ConfigMapWrapper {
	w.OwnerReferences = append(w.OwnerReferences, ref)
	return w
}

// Profile sets the profile label.
func (w *ConfigMapWrapper) Profile(v string) *ConfigMapWrapper {
	return w.Label(constants.ProfileLabel, v)
}

// Mode sets the profile label.
func (w *ConfigMapWrapper) Mode(v v1alpha1.ApplicationProfileMode) *ConfigMapWrapper {
	return w.Label(constants.ModeLabel, string(v))
}

// LocalQueue sets the localqueue label.
func (w *ConfigMapWrapper) LocalQueue(v string) *ConfigMapWrapper {
	return w.Label(kueueconstants.QueueLabel, v)
}

// Label sets the label key and value.
func (w *ConfigMapWrapper) Label(key, value string) *ConfigMapWrapper {
	if w.Labels == nil {
		w.Labels = make(map[string]string)
	}
	w.ObjectMeta.Labels[key] = value
	return w
}

// Data sets data.
func (w *ConfigMapWrapper) Data(data map[string]string) *ConfigMapWrapper {
	w.ConfigMap.Data = data
	return w
}
