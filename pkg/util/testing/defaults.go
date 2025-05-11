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

package testing

import (
	"os"

	corev1 "k8s.io/api/core/v1"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
)

const (
	DefaultRackTopologyLevel  = "cloud.provider.com/topology-rack"
	DefaultBlockTopologyLevel = "cloud.provider.com/topology-block"
)

// MakeDefaultOneLevelTopology creates a default topology with hostname level.
func MakeDefaultOneLevelTopology(name string) *kueuealpha.Topology {
	return MakeTopology(name).
		Levels(corev1.LabelHostname).
		Obj()
}

// MakeDefaultTwoLevelTopology creates a default topology with block and rack levels.
func MakeDefaultTwoLevelTopology(name string) *kueuealpha.Topology {
	return MakeTopology(name).
		Levels(DefaultBlockTopologyLevel, DefaultRackTopologyLevel).
		Obj()
}

// MakeDefaultThreeLevelTopology creates a default topology with block, rack and hostname levels.
func MakeDefaultThreeLevelTopology(name string) *kueuealpha.Topology {
	return MakeTopology(name).
		Levels(DefaultBlockTopologyLevel, DefaultRackTopologyLevel, corev1.LabelHostname).
		Obj()
}

// MakeNamespace creates a default namespace with name.
func MakeNamespace(name string) *corev1.Namespace {
	return MakeNamespaceWrapper(name).Obj()
}

// MakeNamespaceWithGenerateName creates a default namespace with generate name.
func MakeNamespaceWithGenerateName(prefix string) *corev1.Namespace {
	return MakeNamespaceWrapper("").GenerateName(prefix).Obj()
}

// TestRayVersion retrieves the Ray version from the "RAY_VERSION" environment variable.
// If the environment variable is not set, it returns the default Ray version ("2.41.0").
func TestRayVersion() string {
	const defaultVersion = "2.41.0" // Default Ray version
	ver, found := os.LookupEnv("RAY_VERSION")
	if found {
		return ver
	}
	return defaultVersion
}
