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

// Package v1alpha1 contains API Schema definitions for the multicluster.x-k8s.io v1alpha1 API group
// +kubebuilder:object:generate=true
// +groupName=multicluster.x-k8s.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

const (
	// Group is the API group.
	Group = "multicluster.x-k8s.io"
	// Version is the API version.
	Version = "v1alpha1"

	// ClusterProfile resource constants.
	// Kind is the resource kind for ClusterProfile.
	// Deprecated: Use ClusterProfileKind instead.
	Kind = ClusterProfileKind
	// ClusterProfileKind is the resource kind for ClusterProfile.
	ClusterProfileKind     = "ClusterProfile"
	clusterProfileResource = "clusterprofiles"

	// PlacementDecision resource constants.
	// PlacementDecisionKind is the resource kind for PlacementDecision.
	PlacementDecisionKind     = "PlacementDecision"
	placementDecisionResource = "placementdecisions"
)

var (
	// GroupVersion is group version used to register these objects
	GroupVersion = schema.GroupVersion{Group: Group, Version: Version}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// SchemeGroupVersion generated code relies on this name
	// Deprecated
	SchemeGroupVersion = GroupVersion

	// ClusterProfileSchemeGroupVersionKind is the group, version and kind for the ClusterProfile CR.
	ClusterProfileSchemeGroupVersionKind = schema.GroupVersionKind{
		Group:   Group,
		Version: Version,
		Kind:    ClusterProfileKind,
	}

	// SchemeGroupVersionKind is the group, version and kind for the ClusterProfile CR.
	// Deprecated: Use ClusterProfileSchemeGroupVersionKind instead.
	SchemeGroupVersionKind = ClusterProfileSchemeGroupVersionKind

	// ClusterProfileSchemeGroupVersionResource is the group, version and resource for the ClusterProfile CR.
	ClusterProfileSchemeGroupVersionResource = schema.GroupVersionResource{
		Group:    Group,
		Version:  Version,
		Resource: clusterProfileResource,
	}

	// SchemeGroupVersionResource is the group, version and resource for the ClusterProfile CR.
	// Deprecated: Use ClusterProfileSchemeGroupVersionResource instead.
	SchemeGroupVersionResource = ClusterProfileSchemeGroupVersionResource

	// PlacementDecisionSchemeGroupVersionKind is the group, version and kind for the PlacementDecision CR.
	PlacementDecisionSchemeGroupVersionKind = schema.GroupVersionKind{
		Group:   Group,
		Version: Version,
		Kind:    PlacementDecisionKind,
	}

	// PlacementDecisionSchemeGroupVersionResource is the group, version and resource for the PlacementDecision CR.
	PlacementDecisionSchemeGroupVersionResource = schema.GroupVersionResource{
		Group:    Group,
		Version:  Version,
		Resource: placementDecisionResource,
	}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

// Resource generated code relies on this being here, but it logically belongs to the group
// DEPRECATED
func Resource(resource string) schema.GroupResource {
	return schema.GroupResource{Group: GroupVersion.Group, Resource: resource}
}
