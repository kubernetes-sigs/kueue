/*
Copyright 2023 The Kubernetes Authors.

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

package v1beta1

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/registry/rest"

	visibility "sigs.k8s.io/kueue/apis/visibility/v1beta1"
)

// LqREST type is used only to install localqueues/ resource, so we can install localqueues/pending_workloads subresource.
// It implements the necessary interfaces for genericapiserver but does not provide any actual functionalities.
type LqREST struct{}

// Those interfaces are necessary for genericapiserver to work properly
var _ rest.Storage = &LqREST{}
var _ rest.Scoper = &LqREST{}
var _ rest.SingularNameProvider = &LqREST{}

func NewLqREST() *LqREST {
	return &LqREST{}
}

// New implements rest.Storage interface
func (m *LqREST) New() runtime.Object {
	return &visibility.PendingWorkloadsSummary{}
}

// Destroy implements rest.Storage interface
func (m *LqREST) Destroy() {}

// NamespaceScoped implements rest.Scoper interface
func (m *LqREST) NamespaceScoped() bool {
	return true
}

// GetSingularName implements rest.SingularNameProvider interface
func (m *LqREST) GetSingularName() string {
	return "localqueue"
}
