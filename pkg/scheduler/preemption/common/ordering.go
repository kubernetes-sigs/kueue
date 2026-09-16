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

package common

import (
	"cmp"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	cmputil "sigs.k8s.io/kueue/pkg/util/cmp"
	"sigs.k8s.io/kueue/pkg/util/priority"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
)

// CandidatesOrdering criteria:
// 0. Workloads already marked for preemption first.
// 1. Workloads whose current count is still above their reclaim target first.
// 2. Workloads from other ClusterQueues in the cohort before the ones in the
// same ClusterQueue as the preemptor.
// 3. (AdmissionFairSharing only) Workloads with higher LocalQueue's usage first.
// 4. Workloads with lower priority first.
// 5. Partial-preemptible workloads first (elastic jobs that can shed replicas
// towards minCount without failing the whole job - a lower-cost preemption).
// 6. Partial-preemptible workloads with higher effective usage first.
// 7. Workloads admitted more recently first.
func CandidatesOrdering(log logr.Logger, afsEnabled bool, a, b *workload.Info, cq kueue.ClusterQueueReference, now time.Time) int {
	return cmputil.LazyOr(
		func() int {
			return cmputil.CompareBool(
				workloadevict.IsEvicted(b.Obj),
				workloadevict.IsEvicted(a.Obj),
			)
		},
		func() int {
			return cmputil.CompareBool(
				workload.HasReclaimTargetCount(b.Obj),
				workload.HasReclaimTargetCount(a.Obj),
			)
		},
		func() int {
			return cmputil.CompareBool(
				a.ClusterQueue == cq,
				b.ClusterQueue == cq,
			)
		},
		func() int {
			if afsEnabled &&
				resourceUsagePreemptionEnabled(a, b) &&
				a.LocalQueueFSUsage != b.LocalQueueFSUsage {
				log.V(5).Info("Comparing workloads by LocalQueue fair sharing usage",
					"workloadA", klog.KObj(a.Obj), "queueA", klog.KRef(a.Obj.Namespace, string(a.Obj.Spec.QueueName)), "usageA", a.LocalQueueFSUsage,
					"workloadB", klog.KObj(b.Obj), "queueB", klog.KRef(b.Obj.Namespace, string(b.Obj.Spec.QueueName)), "usageB", b.LocalQueueFSUsage)
				return cmp.Compare(*b.LocalQueueFSUsage, *a.LocalQueueFSUsage)
			}
			return 0
		},
		func() int {
			return cmp.Compare(
				priority.EffectivePriority(log, a.Obj),
				priority.EffectivePriority(log, b.Obj),
			)
		},
		func() int {
			// Among equal-priority candidates, preempt partial-preemptible ones first: shedding
			// an elastic job's replicas down to minCount keeps it running, whereas evicting a
			// non-partial candidate fails the whole job. Placed after priority so it never
			// overrides priority protection. No-op when the PartialPreemption gate is off.
			aPartial, aUsedCount := partialPreemptionUsedCount(a)
			bPartial, bUsedCount := partialPreemptionUsedCount(b)
			return cmputil.LazyOr(
				func() int {
					return cmputil.CompareBool(bPartial, aPartial)
				},
				func() int {
					if aPartial && bPartial {
						return cmp.Compare(bUsedCount, aUsedCount)
					}
					return 0
				},
			)
		},
		func() int {
			return quotaReservationTime(b.Obj, now).Compare(quotaReservationTime(a.Obj, now))
		},
		func() int {
			// Arbitrary comparison for deterministic sorting.
			return cmp.Compare(
				a.Obj.UID,
				b.Obj.UID,
			)
		},
	)
}

func partialPreemptionUsedCount(info *workload.Info) (bool, int32) {
	if !workload.IsPartialPreemptionJob(info.Obj) {
		return false, 0
	}
	var usedCount int32
	var found bool
	for i := range info.Obj.Spec.PodSets {
		ps := &info.Obj.Spec.PodSets[i]
		if ps.MinCount == nil {
			continue
		}
		var used int32
		for _, podSetResources := range info.TotalRequests {
			if podSetResources.Name == ps.Name {
				used = podSetResources.Count
				break
			}
		}
		if target, ok := workload.ReclaimTargetCount(info.Obj, ps.Name); ok && target < used {
			used = target
		}
		if used <= *ps.MinCount {
			continue
		}
		usedCount += used
		found = true
	}
	return found, usedCount
}

func resourceUsagePreemptionEnabled(a, b *workload.Info) bool {
	// If both workloads are in the same ClusterQueue, but different LocalQueues,
	// we can compare their LocalQueue usage.
	// If the LocalQueueUsage is not nil for both Workloads, it means the feature gate has been enabled, and the
	// AdmissionScope of the ClusterQueue is set to UsageBasedFairSharing. We inherit this information from the snapshot initialization.
	return a.ClusterQueue == b.ClusterQueue && utilqueue.KeyFromWorkload(a.Obj) != utilqueue.KeyFromWorkload(b.Obj) && a.LocalQueueFSUsage != nil && b.LocalQueueFSUsage != nil
}

func quotaReservationTime(wl *kueue.Workload, now time.Time) time.Time {
	cond := meta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		// The condition wasn't populated yet, use the current time.
		return now
	}
	return cond.LastTransitionTime.Time
}
