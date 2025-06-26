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

package jobframework

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

// OwnerReferenceGroupKindFmt defines the format string used to construct a field path
// for indexing or matching against a specific owner Group and Kind in a Kubernetes object's metadata.
//
// The format expects two placeholders: the owner's Group and Kind, and produces a path like:
// ".metadata.ownerReferences[<group>.<kind>]"
// which can be used as an index key in field selectors.
//
// Example:
//
//	fmt.Sprintf(OwnerReferenceGroupKindFmt, "batch", "Job")
//	=> ".metadata.ownerReferences[batch.Job]"
const OwnerReferenceGroupKindFmt = ".metadata.ownerReferences[%s.%s]"

// OwnerReferenceIndexKey returns an index key based on the workload owner's GroupVersionKind and Name.
func OwnerReferenceIndexKey(ownerGVK schema.GroupVersionKind) string {
	return fmt.Sprintf(OwnerReferenceGroupKindFmt, ownerGVK.Group, ownerGVK.Kind)
}

// OwnerReferenceIndexFieldMatcher returns a field matcher used to filter objects based on a specific OwnerReference.
//
// It constructs a MatchingFields map using the provided GroupVersionKind and owner name,
// which can be used in client.List or client.MatchingFields queries to retrieve objects
// owned by a specific controller.
//
// Example usage:
//
//	matcher := OwnerReferenceIndexFieldMatcher(gvk, "my-owner-name")
//	cl.List(ctx, &objList, matcher)
//
// The index key is derived using OwnerReferenceIndexKey(gvk).
func OwnerReferenceIndexFieldMatcher(gvk schema.GroupVersionKind, name string) client.MatchingFields {
	return client.MatchingFields{OwnerReferenceIndexKey(gvk): name}
}

// SetupWorkloadOwnerIndex registers a field index on kueue.Workload objects based on their OwnerReferences
// that match the specified GroupVersionKind.
//
// This enables lookups of Workloads by the name of their controller (e.g., Job, CronJob, etc.)
// using a field selector constructed with OwnerReferenceIndexFieldMatcher.
//
// Parameters:
// - ctx: context for the indexing operation.
// - indexer: the client.FieldIndexer used to register the index function.
// - gvk: the GroupVersionKind of the controller to match against Workload OwnerReferences.
//
// The index function extracts the names of all OwnerReferences from the Workload whose Kind and APIVersion
// match the given GroupVersionKind. These names are used as index keys.
//
// Example:
//
//	err := SetupWorkloadOwnerIndex(ctx, mgr.GetFieldIndexer(), schema.GroupVersionKind{
//	    Group:   "batch",
//	    Version: "v1",
//	    Kind:    "Job",
//	})
//
// Once registered, you can query Workloads owned by a specific Job name using:
//
//	matcher := OwnerReferenceIndexFieldMatcher(gvk, "my-job-name")
//	cl.List(ctx, &workloadList, matcher)
func SetupWorkloadOwnerIndex(ctx context.Context, indexer client.FieldIndexer, gvk schema.GroupVersionKind) error {
	return indexer.IndexField(ctx, &kueue.Workload{}, OwnerReferenceIndexKey(gvk), func(object client.Object) []string {
		wl, ok := object.(*kueue.Workload)
		if !ok || len(wl.OwnerReferences) == 0 {
			return nil
		}
		owners := make([]string, 0, len(wl.OwnerReferences))
		for i := range wl.OwnerReferences {
			owner := &wl.OwnerReferences[i]
			if owner.Kind == gvk.Kind && owner.APIVersion == gvk.GroupVersion().String() {
				owners = append(owners, owner.Name)
			}
		}
		return owners
	})
}
