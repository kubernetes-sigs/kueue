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
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
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

// PartialPreemptionPodSet returns the single PodSet that can be shrunk.
func PartialPreemptionPodSet(wl *kueue.Workload, requests []PodSetResources) (kueue.PodSetReference, bool) {
	if !IsPartialPreemptionJob(wl) || workloadevict.IsEvicted(wl) {
		return "", false
	}

	var shrinkablePodSet kueue.PodSetReference
	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		if ps.PartialPreemption == nil || ps.PartialPreemption.MinCount == nil {
			continue
		}
		for j := range requests {
			psr := &requests[j]
			if psr.Name != ps.Name || psr.Count <= *ps.PartialPreemption.MinCount {
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

// HasPendingPreemptionTargetCount reports whether any admitted PodSet still has an in-flight
// partial-preemption reclaim target above its current effective count.
func HasPendingPreemptionTargetCount(wl *kueue.Workload) bool {
	if !IsPartialPreemptionJob(wl) || wl.Status.Admission == nil {
		return false
	}
	effectiveCounts := podSetsCountsAfterReclaim(wl)
	for i := range wl.Status.Admission.PodSetAssignments {
		psa := &wl.Status.Admission.PodSetAssignments[i]
		if psa.PartialPreemption == nil || psa.PartialPreemption.TargetCount == nil {
			continue
		}
		if effectiveCounts[psa.Name] > *psa.PartialPreemption.TargetCount {
			return true
		}
	}
	return false
}

// PreemptionTargetCount returns the requested partial-preemption target count for the named PodSet.
func PreemptionTargetCount(wl *kueue.Workload, podSetName kueue.PodSetReference) (int32, bool) {
	if wl.Status.Admission == nil {
		return 0, false
	}
	for i := range wl.Status.Admission.PodSetAssignments {
		psa := &wl.Status.Admission.PodSetAssignments[i]
		if psa.Name == podSetName && psa.PartialPreemption != nil && psa.PartialPreemption.TargetCount != nil {
			return *psa.PartialPreemption.TargetCount, true
		}
	}
	return 0, false
}

// PreemptionMinCount returns the configured partial-preemption floor for the named PodSet.
func PreemptionMinCount(wl *kueue.Workload, podSetName kueue.PodSetReference) (int32, bool) {
	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		if ps.Name == podSetName && ps.PartialPreemption != nil && ps.PartialPreemption.MinCount != nil {
			return *ps.PartialPreemption.MinCount, true
		}
	}
	return 0, false
}

// ReleaseSatisfiedPreemptionTargets clears fulfilled reclaim requests and updates
// the partial-preemption condition after quota has been released.
func ReleaseSatisfiedPreemptionTargets(wl *kueue.Workload, now time.Time) bool {
	if !IsPartialPreemptionJob(wl) || wl.Status.Admission == nil || !meta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadPartiallyPreempted) {
		return false
	}
	updated := false
	effectiveCounts := podSetsCountsAfterReclaim(wl)
	for i := range wl.Status.Admission.PodSetAssignments {
		psa := &wl.Status.Admission.PodSetAssignments[i]
		if psa.PartialPreemption == nil || psa.PartialPreemption.TargetCount == nil {
			continue
		}
		effective := min(effectiveCounts[psa.Name], ptr.Deref(psa.Count, effectiveCounts[psa.Name]))
		if effective <= *psa.PartialPreemption.TargetCount {
			psa.PartialPreemption = nil
			updated = true
		}
	}
	if updated && !HasPendingPreemptionTargetCount(wl) {
		SetPartiallyPreemptedCondition(wl, now, false,
			kueue.WorkloadPartiallyPreemptedReasonQuotaReleased,
			"Partial-preemption reclaim target satisfied; quota released")
	}
	return updated
}
