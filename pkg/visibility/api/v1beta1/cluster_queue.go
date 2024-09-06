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

// CqREST type is used only to install clusterqueues/ resource, so we can install clusterqueues/pending_workloads subresource.
// It implements the necessary interfaces for genericapiserver but does not provide any actual functionalities.
type CqREST struct{}

// Those interfaces are necessary for genericapiserver to work properly
var _ rest.Storage = &CqREST{}
var _ rest.Scoper = &CqREST{}
var _ rest.SingularNameProvider = &CqREST{}

func NewCqREST() *CqREST {
	return &CqREST{}
}

// New implements rest.Storage interface
func (m *CqREST) New() runtime.Object {
	return &visibility.PendingWorkloadsSummary{}
}

// Destroy implements rest.Storage interface
func (m *CqREST) Destroy() {}

// NamespaceScoped implements rest.Scoper interface
func (m *CqREST) NamespaceScoped() bool {
	return false
}

// GetSingularName implements rest.SingularNameProvider interface
func (m *CqREST) GetSingularName() string {
	return "clusterqueue"
}
