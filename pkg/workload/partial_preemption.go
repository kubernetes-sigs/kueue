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
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
)

// IsPartialPreemptionJob reports the cheap eligibility gate for partial preemption:
//   - the PartialPreemption feature gate is enabled, and
//   - the job explicitly opted in via the partial-preemption annotation (propagated to the
//     Workload).
func IsPartialPreemptionJob(wl *kueue.Workload) bool {
	return features.Enabled(features.PartialPreemption) &&
		wl.GetAnnotations()[constants.PartialPreemptionAnnotation] == "true"
}

// HasPendingPreemptionTargetCount reports whether any admitted PodSet still has an in-flight
// partial-preemption reclaim target above its current effective count.
func HasPendingPreemptionTargetCount(wl *kueue.Workload) bool {
	if !IsPartialPreemptionJob(wl) || wl.Status.Admission == nil {
		return false
	}
	effectiveCounts := PodSetsCountsAfterReclaim(wl)
	for i := range wl.Status.Admission.PodSetAssignments {
		psa := &wl.Status.Admission.PodSetAssignments[i]
		if psa.PreemptionTargetCount == nil {
			continue
		}
		if effectiveCounts[psa.Name] > *psa.PreemptionTargetCount {
			return true
		}
	}
	return false
}

// PartialPreemptionUsedCount returns how many replicas a partial-preemption job can shed above
// its PodSets' PreemptionMinCount (capped by any pending reclaim target). The caller must have
// already confirmed the workload is a partial-preemption job. Cached on Info (see NewInfo) so the
// preemption comparator can use it as an allocation-free tie-breaker.
func PartialPreemptionUsedCount(wl *kueue.Workload) int32 {
	effective := PodSetsCountsAfterReclaim(wl)
	var usedCount int32
	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		if ps.PreemptionMinCount != nil {
			used := effective[ps.Name]
			if target, ok := PreemptionTargetCount(wl, ps.Name); ok && target < used {
				used = target
			}
			if used > *ps.PreemptionMinCount {
				usedCount += used - *ps.PreemptionMinCount
			}
		}
	}
	return usedCount
}

// PreemptionTargetCount returns the requested partial-preemption target count for the named PodSet.
func PreemptionTargetCount(wl *kueue.Workload, podSetName kueue.PodSetReference) (int32, bool) {
	if wl.Status.Admission == nil {
		return 0, false
	}
	for i := range wl.Status.Admission.PodSetAssignments {
		psa := &wl.Status.Admission.PodSetAssignments[i]
		if psa.Name == podSetName && psa.PreemptionTargetCount != nil {
			return *psa.PreemptionTargetCount, true
		}
	}
	return 0, false
}
