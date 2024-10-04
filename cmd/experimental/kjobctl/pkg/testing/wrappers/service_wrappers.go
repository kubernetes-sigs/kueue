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

// ServiceWrapper wraps a Service.
type ServiceWrapper struct{ corev1.Service }

// MakeService creates a wrapper for a Service
func MakeService(name, ns string) *ServiceWrapper {
	return &ServiceWrapper{
		corev1.Service{
			TypeMeta: metav1.TypeMeta{Kind: "Service", APIVersion: "v1"},
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
		},
	}
}

// Obj returns the inner Service.
func (w *ServiceWrapper) Obj() *corev1.Service {
	return &w.Service
}

// Profile sets the profile label.
func (w *ServiceWrapper) Profile(v string) *ServiceWrapper {
	return w.Label(constants.ProfileLabel, v)
}

// Mode sets the mode label.
func (w *ServiceWrapper) Mode(v v1alpha1.ApplicationProfileMode) *ServiceWrapper {
	return w.Label(constants.ModeLabel, string(v))
}

// LocalQueue sets the localqueue label.
func (w *ServiceWrapper) LocalQueue(v string) *ServiceWrapper {
	return w.Label(kueueconstants.QueueLabel, v)
}

// Label sets the label key and value.
func (w *ServiceWrapper) Label(key, value string) *ServiceWrapper {
	if w.Labels == nil {
		w.Labels = make(map[string]string)
	}
	w.ObjectMeta.Labels[key] = value
	return w
}

// ClusterIP sets clusterIP.
func (w *ServiceWrapper) ClusterIP(clusterIP string) *ServiceWrapper {
	w.Service.Spec.ClusterIP = clusterIP
	return w
}

// Selector sets the selector key and value.
func (w *ServiceWrapper) Selector(key, value string) *ServiceWrapper {
	if w.Service.Spec.Selector == nil {
		w.Service.Spec.Selector = make(map[string]string)
	}
	w.Service.Spec.Selector[key] = value
	return w
}

// WithOwnerReference adds the owner reference.
func (w *ServiceWrapper) WithOwnerReference(ref metav1.OwnerReference) *ServiceWrapper {
	w.OwnerReferences = append(w.OwnerReferences, ref)
	return w
}
