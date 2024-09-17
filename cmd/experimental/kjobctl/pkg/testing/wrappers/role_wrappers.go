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

// RoleWrapper wraps a Role.
type RoleWrapper struct{ rbacv1.Role }

// MakeRole creates a wrapper for a Role
func MakeRole(name, ns string) *RoleWrapper {
	return &RoleWrapper{
		rbacv1.Role{
			TypeMeta: metav1.TypeMeta{Kind: "Role", APIVersion: "rbac.authorization.k8s.io/v1"},
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
		},
	}
}

// Obj returns the inner Role.
func (w *RoleWrapper) Obj() *rbacv1.Role {
	return &w.Role
}

// Profile sets the profile label.
func (w *RoleWrapper) Profile(v string) *RoleWrapper {
	return w.Label(constants.ProfileLabel, v)
}

// Mode sets the mode label.
func (w *RoleWrapper) Mode(v v1alpha1.ApplicationProfileMode) *RoleWrapper {
	return w.Label(constants.ModeLabel, string(v))
}

// LocalQueue sets the localqueue label.
func (w *RoleWrapper) LocalQueue(v string) *RoleWrapper {
	return w.Label(kueueconstants.QueueLabel, v)
}

// Label sets the label key and value.
func (w *RoleWrapper) Label(key, value string) *RoleWrapper {
	if w.Labels == nil {
		w.Labels = make(map[string]string)
	}
	w.ObjectMeta.Labels[key] = value
	return w
}

// WithOwnerReference adds the owner reference.
func (w *RoleWrapper) WithOwnerReference(ref metav1.OwnerReference) *RoleWrapper {
	w.OwnerReferences = append(w.OwnerReferences, ref)
	return w
}

// WithRule adds the rule.
func (w *RoleWrapper) WithRule(rule rbacv1.PolicyRule) *RoleWrapper {
	w.Rules = append(w.Rules, rule)
	return w
}
