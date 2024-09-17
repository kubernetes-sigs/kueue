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
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kueueconstants "sigs.k8s.io/kueue/pkg/controller/constants"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/constants"
)

// RoleBindingWrapper wraps a RoleBinding.
type RoleBindingWrapper struct{ rbacv1.RoleBinding }

// MakeRoleBinding creates a wrapper for a RoleBinding
func MakeRoleBinding(name, ns string) *RoleBindingWrapper {
	return &RoleBindingWrapper{
		rbacv1.RoleBinding{
			TypeMeta: metav1.TypeMeta{Kind: "RoleBinding", APIVersion: "rbac.authorization.k8s.io/v1"},
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
		},
	}
}

// Obj returns the inner RoleBinding.
func (w *RoleBindingWrapper) Obj() *rbacv1.RoleBinding {
	return &w.RoleBinding
}

// Profile sets the profile label.
func (w *RoleBindingWrapper) Profile(v string) *RoleBindingWrapper {
	return w.Label(constants.ProfileLabel, v)
}

// Mode sets the mode label.
func (w *RoleBindingWrapper) Mode(v v1alpha1.ApplicationProfileMode) *RoleBindingWrapper {
	return w.Label(constants.ModeLabel, string(v))
}

// LocalQueue sets the localqueue label.
func (w *RoleBindingWrapper) LocalQueue(v string) *RoleBindingWrapper {
	return w.Label(kueueconstants.QueueLabel, v)
}

// Label sets the label key and value.
func (w *RoleBindingWrapper) Label(key, value string) *RoleBindingWrapper {
	if w.Labels == nil {
		w.Labels = make(map[string]string)
	}
	w.ObjectMeta.Labels[key] = value
	return w
}

// WithOwnerReference adds the owner reference.
func (w *RoleBindingWrapper) WithOwnerReference(ref metav1.OwnerReference) *RoleBindingWrapper {
	w.OwnerReferences = append(w.OwnerReferences, ref)
	return w
}

// WithSubject adds the subject.
func (w *RoleBindingWrapper) WithSubject(subject rbacv1.Subject) *RoleBindingWrapper {
	w.Subjects = append(w.Subjects, subject)
	return w
}

// RoleRef adds the roleRef.
func (w *RoleBindingWrapper) RoleRef(roleRef rbacv1.RoleRef) *RoleBindingWrapper {
	w.RoleBinding.RoleRef = roleRef
	return w
}
