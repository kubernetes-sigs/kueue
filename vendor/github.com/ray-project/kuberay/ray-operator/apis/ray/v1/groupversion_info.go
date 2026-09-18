// Package v1 contains API Schema definitions for the ray v1 API group
// +kubebuilder:object:generate=true
// +groupName=ray.io
package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is group version used to register these objects
	GroupVersion = schema.GroupVersion{Group: "ray.io", Version: "v1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion,
		&RayCluster{}, &RayClusterList{},
		&RayJob{}, &RayJobList{},
		&RayService{}, &RayServiceList{},
		&RayCronJob{}, &RayCronJobList{},
	)
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
