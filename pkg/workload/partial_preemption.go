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

package workload

import (
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/api"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
)

// IsPartialPreemptionJob reports the cheap eligibility gate for partial preemption:
//   - the PartialPreemption feature gate is enabled, and
//   - the job explicitly opted in via the partial-preemption annotation (propagated to the
//     Workload).
func IsPartialPreemptionJob(wl *kueue.Workload) bool {
	return features.Enabled(features.PartialPreemption) &&
		wl.GetAnnotations()[constants.PartialPreemptionAnnotation] == "true"
}

// PreemptionMaxReclaimedCount returns the most Pods that a round of a preemption
// search may take from the Workload, from its max-reclaimed-count annotation. It
// returns false if the annotation is absent or is not a positive integer.
func PreemptionMaxReclaimedCount(wl *kueue.Workload) (int32, bool) {
	value, found := wl.GetAnnotations()[constants.PartialPreemptionMaxReclaimedCountAnnotation]
	if !found {
		return 0, false
	}
	count, err := strconv.ParseInt(value, 10, 32)
	if err != nil || count <= 0 {
		return 0, false
	}
	return int32(count), true
}

// PartialPreemptionPodSet returns the single PodSet that can be shrunk.
func PartialPreemptionPodSet(wl *kueue.Workload, requests []PodSetResources) (kueue.PodSetReference, bool) {
	if !IsPartialPreemptionJob(wl) || workloadevict.IsEvicted(wl) {
		return "", false
	}

	var shrinkablePodSet kueue.PodSetReference
	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		if ps.PartialPreemption == nil || ps.PartialPreemption.MinRetainedCount == nil {
			continue
		}
		for j := range requests {
			psr := &requests[j]
			if psr.Name != ps.Name || psr.Count <= *ps.PartialPreemption.MinRetainedCount {
				continue
			}
			if psr.TopologyRequest != nil || shrinkablePodSet != "" {
				return "", false
			}
			shrinkablePodSet = ps.Name
		}
	}
	return shrinkablePodSet, shrinkablePodSet != ""
}

// HasPendingPreemptionRetainedCount reports whether any admitted PodSet still has an in-flight
// partial-preemption request above its current effective count.
func HasPendingPreemptionRetainedCount(wl *kueue.Workload) bool {
	if !IsPartialPreemptionJob(wl) || wl.Status.Admission == nil {
		return false
	}
	effectiveCounts := podSetsCountsAfterReclaim(wl)
	for i := range wl.Status.Admission.PodSetAssignments {
		psa := &wl.Status.Admission.PodSetAssignments[i]
		if psa.PartialPreemption == nil || psa.PartialPreemption.RetainedCount == nil {
			continue
		}
		effective := min(effectiveCounts[psa.Name], ptr.Deref(psa.Count, effectiveCounts[psa.Name]))
		if effective > *psa.PartialPreemption.RetainedCount {
			return true
		}
	}
	return false
}

// PreemptionRetainedCount returns the requested partial-preemption target count for the named PodSet.
func PreemptionRetainedCount(wl *kueue.Workload, podSetName kueue.PodSetReference) (int32, bool) {
	if wl.Status.Admission == nil {
		return 0, false
	}
	for i := range wl.Status.Admission.PodSetAssignments {
		psa := &wl.Status.Admission.PodSetAssignments[i]
		if psa.Name == podSetName && psa.PartialPreemption != nil && psa.PartialPreemption.RetainedCount != nil {
			return *psa.PartialPreemption.RetainedCount, true
		}
	}
	return 0, false
}

// PreemptionMinRetainedCount returns the configured partial-preemption floor for the named PodSet.
func PreemptionMinRetainedCount(wl *kueue.Workload, podSetName kueue.PodSetReference) (int32, bool) {
	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		if ps.Name == podSetName && ps.PartialPreemption != nil && ps.PartialPreemption.MinRetainedCount != nil {
			return *ps.PartialPreemption.MinRetainedCount, true
		}
	}
	return 0, false
}

func SetPartiallyPreemptedCondition(w *kueue.Workload, now time.Time, active bool, reason, message string) bool {
	status := metav1.ConditionFalse
	if active {
		status = metav1.ConditionTrue
	}
	condition := metav1.Condition{
		Type:               kueue.WorkloadPartiallyPreempted,
		Status:             status,
		LastTransitionTime: metav1.NewTime(now),
		Reason:             reason,
		Message:            api.TruncateConditionMessage(message),
		ObservedGeneration: w.Generation,
	}
	return meta.SetStatusCondition(&w.Status.Conditions, condition)
}

func MarkPartialPreemptionSatisfied(wl *kueue.Workload, now time.Time) bool {
	if !IsPartialPreemptionJob(wl) || wl.Status.Admission == nil || !meta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadPartiallyPreempted) {
		return false
	}
	if HasPendingPreemptionRetainedCount(wl) {
		return false
	}
	SetPartiallyPreemptedCondition(wl, now, false,
		kueue.WorkloadPartiallyPreemptedReasonQuotaReleased,
		"Partial-preemption request satisfied; quota released")
	return true
}
