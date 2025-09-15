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

package preemptioncommon

import (
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/workload"
)

// CandidatesOrdering criteria:
// 0. Workloads already marked for preemption first.
// 1. Workloads from other ClusterQueues in the cohort before the ones in the
// same ClusterQueue as the preemptor.
// 2. (AdmissionFairSharing only) Workloads with lower LocalQueue's usage first
// 3. Workloads with lower priority first.
// 4. Workloads admitted more recently first.
func CandidatesOrdering(log logr.Logger, afsEnabled bool, a, b *workload.Info, cq kueue.ClusterQueueReference, now time.Time) bool {
	aEvicted := meta.IsStatusConditionTrue(a.Obj.Status.Conditions, kueue.WorkloadEvicted)
	bEvicted := meta.IsStatusConditionTrue(b.Obj.Status.Conditions, kueue.WorkloadEvicted)
	if aEvicted != bEvicted {
		return aEvicted
	}
	aInCQ := a.ClusterQueue == cq
	bInCQ := b.ClusterQueue == cq
	if aInCQ != bInCQ {
		return !aInCQ
	}

	if afsEnabled && resourceUsagePreemptionEnabled(a, b) {
		if a.LocalQueueFSUsage != b.LocalQueueFSUsage {
			log.V(5).Info("Comparing workloads by LocalQueue fair sharing usage",
				"workloadA", klog.KObj(a.Obj), "queueA", a.Obj.Spec.QueueName, "usageA", a.LocalQueueFSUsage,
				"workloadB", klog.KObj(b.Obj), "queueB", b.Obj.Spec.QueueName, "usageB", b.LocalQueueFSUsage)
			return *a.LocalQueueFSUsage > *b.LocalQueueFSUsage
		}
	}
	pa := priority.Priority(a.Obj)
	pb := priority.Priority(b.Obj)
	if pa != pb {
		return pa < pb
	}
	timeA := quotaReservationTime(a.Obj, now)
	timeB := quotaReservationTime(b.Obj, now)
	if !timeA.Equal(timeB) {
		return timeA.After(timeB)
	}
	// Arbitrary comparison for deterministic sorting.
	return a.Obj.UID < b.Obj.UID
}

func resourceUsagePreemptionEnabled(a, b *workload.Info) bool {
	// If both workloads are in the same ClusterQueue, but different LocalQueues,
	// we can compare their LocalQueue usage.
	// If the LocalQueueUsage is not nil for both Workloads, it means the feature gate has been enabled, and the
	// AdmissionScope of the ClusterQueue is set to UsageBasedFairSharing. We inherit this information from the snapshot initialization.
	return a.ClusterQueue == b.ClusterQueue && a.Obj.Spec.QueueName != b.Obj.Spec.QueueName && a.LocalQueueFSUsage != nil && b.LocalQueueFSUsage != nil
}

func quotaReservationTime(wl *kueue.Workload, now time.Time) time.Time {
	cond := meta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		// The condition wasn't populated yet, use the current time.
		return now
	}
	return cond.LastTransitionTime.Time
}
