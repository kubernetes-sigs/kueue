/*
Copyright The Kubernetes Authors.

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

package replicaset

import (
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// ReplicaSetWrapper wraps a ReplicaSet.
type ReplicaSetWrapper struct {
	appsv1.ReplicaSet
}

// MakeReplicaSet creates a wrapper for a ReplicaSet.
func MakeReplicaSet(name, ns string) *ReplicaSetWrapper {
	return &ReplicaSetWrapper{appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
	}}
}

// Obj returns the inner ReplicaSet.
func (r *ReplicaSetWrapper) Obj() *appsv1.ReplicaSet {
	return &r.ReplicaSet
}

// UID updates the uid of the ReplicaSet.
func (r *ReplicaSetWrapper) UID(uid string) *ReplicaSetWrapper {
	r.ObjectMeta.UID = types.UID(uid)
	return r
}

// ControllerOwnerReference sets a controller owner reference on the ReplicaSet.
func (r *ReplicaSetWrapper) ControllerOwnerReference(name, apiVersion, kind, uid string) *ReplicaSetWrapper {
	controller := true
	r.OwnerReferences = []metav1.OwnerReference{{
		Name:       name,
		APIVersion: apiVersion,
		Kind:       kind,
		UID:        types.UID(uid),
		Controller: &controller,
	}}
	return r
}
