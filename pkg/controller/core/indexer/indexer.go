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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
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
	WorkloadAdmissionCheckKey  = "status.admissionChecks"
	WorkloadPriorityClassKey   = "spec.priorityClassRef"
	// WorkloadSliceNameKey is an index for pods by their workload slice name annotation.
	// Used to find pods belonging to an elastic workload slice chain.
	WorkloadSliceNameKey = "metadata.workloadSliceName"

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
	ownerReferenceGroupKindFmt = ".metadata.ownerReferences[%s.%s]"
)

// OwnerReferenceIndexKey returns an index key based on the workload owner's GroupVersionKind and Name.
func OwnerReferenceIndexKey(ownerGVK schema.GroupVersionKind) string {
	return fmt.Sprintf(ownerReferenceGroupKindFmt, ownerGVK.Group, ownerGVK.Kind)
}

// OwnerReferenceIndexFieldMatcher returns a field matcher used to filter objects based on a specific OwnerReference.
func OwnerReferenceIndexFieldMatcher(gvk schema.GroupVersionKind, name string) client.MatchingFields {
	return client.MatchingFields{OwnerReferenceIndexKey(gvk): name}
}

// SetupWorkloadOwnerIndex registers a field index on kueue.Workload objects based on their OwnerReferences
// that match the specified GroupVersionKind.
func SetupWorkloadOwnerIndex(ctx context.Context, indexer client.FieldIndexer, gvk schema.GroupVersionKind) error {
	return indexer.IndexField(ctx, &kueue.Workload{}, OwnerReferenceIndexKey(gvk), WorkloadOwnerIndexFunc(gvk))
}

// WorkloadOwnerIndexFunc returns the field indexing function for kueue.Workload objects based on their OwnerReferences
// that match the specified GroupVersionKind.
func WorkloadOwnerIndexFunc(gvk schema.GroupVersionKind) client.IndexerFunc {
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

// IndexPodWorkloadSliceName indexes pods by their workload slice name annotation.
// Uses WorkloadSliceNameAnnotation if present, otherwise falls back to WorkloadAnnotation
// for non-elastic workloads.
func IndexPodWorkloadSliceName(obj client.Object) []string {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}
	// Prefer workload slice name annotation for elastic workloads
	if value, found := pod.Annotations[kueue.WorkloadSliceNameAnnotation]; found {
		return []string{value}
	}
	// Fall back to workload annotation for non-elastic workloads
	if value, found := pod.Annotations[kueue.WorkloadAnnotation]; found {
		return []string{value}
	}
	return nil
}

func IndexWorkloadAdmissionCheck(obj client.Object) []string {
	wl, ok := obj.(*kueue.Workload)
	if !ok || len(wl.Status.AdmissionChecks) == 0 {
		return nil
	}
	return slices.Map(wl.Status.AdmissionChecks, func(checkState *kueue.AdmissionCheckState) string {
		return string(checkState.Name)
	})
}

func IndexWorkloadPriorityClass(obj client.Object) []string {
	wl, ok := obj.(*kueue.Workload)
	if !ok || wl.Spec.PriorityClassRef == nil {
		return nil
	}
	if wl.Spec.PriorityClassRef.Kind != kueue.WorkloadPriorityClassKind ||
		wl.Spec.PriorityClassRef.Group != kueue.WorkloadPriorityClassGroup {
		return nil
	}
	return []string{wl.Spec.PriorityClassRef.Name}
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
	if err := indexer.IndexField(ctx, &kueue.Workload{}, WorkloadAdmissionCheckKey, IndexWorkloadAdmissionCheck); err != nil {
		return fmt.Errorf("setting index on admissionCheck for Workload: %w", err)
	}
	if err := indexer.IndexField(ctx, &kueue.Workload{}, WorkloadPriorityClassKey, IndexWorkloadPriorityClass); err != nil {
		return fmt.Errorf("setting index on priorityClass for Workload: %w", err)
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
	// Add pod indexes for elastic-jobs and TAS. Uses workload slice name annotation to support
	// JobSet and other workloads where pods are not immediate children of the job.
	if features.Enabled(features.ElasticJobsViaWorkloadSlices) || features.Enabled(features.TopologyAwareScheduling) {
		if err := indexer.IndexField(ctx, &corev1.Pod{}, WorkloadSliceNameKey, IndexPodWorkloadSliceName); err != nil {
			return fmt.Errorf("setting index on workloadSliceName for Pod: %w", err)
		}
		// OwnerReferenceUID index for Pod is for backwards compatibility only.
		// TODO(sohankunkerkar): remove in 0.18
		if err := indexer.IndexField(ctx, &corev1.Pod{}, OwnerReferenceUID, IndexOwnerUID); err != nil {
			return fmt.Errorf("setting index on ownerReferences.uid for Pod: %w", err)
		}
	}
	return nil
}
