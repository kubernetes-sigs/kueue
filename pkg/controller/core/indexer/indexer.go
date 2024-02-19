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

package indexer

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	return []string{wl.Spec.QueueName}
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
