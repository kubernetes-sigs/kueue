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

package indexer

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/slices"
)

const (
	WorkloadQueueKey           = "spec.queueName"
	WorkloadClusterQueueKey    = "status.admission.clusterQueue"
	QueueClusterQueueKey       = "spec.clusterQueue"
	LimitRangeHasContainerType = "spec.hasContainerType"
	WorkloadQuotaReservedKey   = "status.quotaReserved"
	WorkloadRuntimeClassKey    = "spec.runtimeClass"
	OwnerReferenceUID          = "metadata.ownerReferences.uid"

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
	OwnerReferenceGroupKindFmt = ".metadata.ownerReferences[%s.%s]"
)

func IndexQueueClusterQueue(obj client.Object) []string {
	q, ok := obj.(*kueue.LocalQueue)
	if !ok {
		return nil
	}
	return []string{string(q.Spec.ClusterQueue)}
}

func IndexWorkloadQueue(obj client.Object) []string {
	wl, ok := obj.(*kueue.Workload)
	if !ok {
		return nil
	}
	return []string{string(wl.Spec.QueueName)}
}

func IndexWorkloadClusterQueue(obj client.Object) []string {
	wl, ok := obj.(*kueue.Workload)
	if !ok {
		return nil
	}
	if wl.Status.Admission == nil {
		return nil
	}
	return []string{string(wl.Status.Admission.ClusterQueue)}
}

func IndexLimitRangeHasContainerType(obj client.Object) []string {
	lr, ok := obj.(*corev1.LimitRange)
	if !ok {
		return nil
	}

	for i := range lr.Spec.Limits {
		if lr.Spec.Limits[i].Type == corev1.LimitTypeContainer {
			return []string{"true"}
		}
	}
	return nil
}

func IndexWorkloadQuotaReserved(obj client.Object) []string {
	wl, ok := obj.(*kueue.Workload)
	if !ok {
		return nil
	}

	cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
	if cond == nil {
		return []string{string(metav1.ConditionFalse)}
	}

	return []string{string(cond.Status)}
}

func IndexWorkloadRuntimeClass(obj client.Object) []string {
	wl, ok := obj.(*kueue.Workload)
	if !ok {
		return nil
	}
	set := sets.New[string]()
	for _, ps := range wl.Spec.PodSets {
		if ps.Template.Spec.RuntimeClassName != nil {
			set.Insert(*ps.Template.Spec.RuntimeClassName)
		}
	}
	if set.Len() > 0 {
		return set.UnsortedList()
	}
	return nil
}

func IndexOwnerUID(obj client.Object) []string {
	return slices.Map(obj.GetOwnerReferences(), func(o *metav1.OwnerReference) string { return string(o.UID) })
}

// Setup sets the index with the given fields for core apis.
func Setup(ctx context.Context, indexer client.FieldIndexer) error {
	if err := indexer.IndexField(ctx, &kueue.Workload{}, WorkloadQueueKey, IndexWorkloadQueue); err != nil {
		return fmt.Errorf("setting index on queue for Workload: %w", err)
	}
	if err := indexer.IndexField(ctx, &kueue.Workload{}, WorkloadClusterQueueKey, IndexWorkloadClusterQueue); err != nil {
		return fmt.Errorf("setting index on clusterQueue for Workload: %w", err)
	}
	if err := indexer.IndexField(ctx, &kueue.Workload{}, WorkloadQuotaReservedKey, IndexWorkloadQuotaReserved); err != nil {
		return fmt.Errorf("setting index on admitted for Workload: %w", err)
	}
	if err := indexer.IndexField(ctx, &kueue.Workload{}, WorkloadRuntimeClassKey, IndexWorkloadRuntimeClass); err != nil {
		return fmt.Errorf("setting index on runtimeClass for Workload: %w", err)
	}
	if err := indexer.IndexField(ctx, &kueue.LocalQueue{}, QueueClusterQueueKey, IndexQueueClusterQueue); err != nil {
		return fmt.Errorf("setting index on clusterQueue for localQueue: %w", err)
	}
	if err := indexer.IndexField(ctx, &corev1.LimitRange{}, LimitRangeHasContainerType, IndexLimitRangeHasContainerType); err != nil {
		return fmt.Errorf("setting index on hasContainerType for limitRange: %w", err)
	}
	if err := indexer.IndexField(ctx, &kueue.Workload{}, OwnerReferenceUID, IndexOwnerUID); err != nil {
		return fmt.Errorf("setting index on ownerReferences.uid for Workload: %w", err)
	}
	return nil
}

// WorkloadOwnerIndexKey returns an index key based on the workload owner's GroupVersionKind and Name.
func WorkloadOwnerIndexKey(ownerGVK schema.GroupVersionKind) string {
	return fmt.Sprintf(OwnerReferenceGroupKindFmt, ownerGVK.Group, ownerGVK.Kind)
}

// WorkloadOwnerReferenceIndexFieldMatcher returns a field matcher used to filter objects based on a specific OwnerReference.
//
// It constructs a MatchingFields map using the provided GroupVersionKind and owner name,
// which can be used in client.List or client.MatchingFields queries to retrieve objects
// owned by a specific controller.
//
// Example usage:
//
//	matcher := WorkloadOwnerReferenceIndexFieldMatcher(gvk, "my-owner-name")
//	cl.List(ctx, &objList, matcher)
//
// The index key is derived using WorkloadOwnerIndexKey(gvk).
func WorkloadOwnerReferenceIndexFieldMatcher(gvk schema.GroupVersionKind, name string) client.MatchingFields {
	return client.MatchingFields{WorkloadOwnerIndexKey(gvk): name}
}

// WorkloadOwnerIndex returns an IndexerFunc that extracts the names of workload owners
// matching the provided GroupVersionKind (GVK).
func WorkloadOwnerIndex(gvk schema.GroupVersionKind) client.IndexerFunc {
	return func(object client.Object) []string {
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
	}
}
