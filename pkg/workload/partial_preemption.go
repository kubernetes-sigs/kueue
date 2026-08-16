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

// HasPendingReclaimTargetCount reports whether any admitted PodSet still has an in-flight
// partial-preemption reclaim target above its current effective count.
func HasPendingReclaimTargetCount(wl *kueue.Workload) bool {
	if !IsPartialPreemptionJob(wl) || wl.Status.Admission == nil {
		return false
	}
	effectiveCounts := PodSetsCountsAfterReclaim(wl)
	for i := range wl.Status.Admission.PodSetAssignments {
		psa := &wl.Status.Admission.PodSetAssignments[i]
		if psa.ReclaimTargetCount == nil {
			continue
		}
		if effectiveCounts[psa.Name] > *psa.ReclaimTargetCount {
			return true
		}
	}
	return false
}

// PodSetSpecCount returns the spec replica count for the named PodSet. It scans in place so hot-path
// callers (e.g. the preemption sort comparator) stay allocation-free.
func PodSetSpecCount(wl *kueue.Workload, name kueue.PodSetReference) (int32, bool) {
	for i := range wl.Spec.PodSets {
		if wl.Spec.PodSets[i].Name == name {
			return wl.Spec.PodSets[i].Count, true
		}
	}
	return 0, false
}

// ReclaimTargetCount returns the requested partial-preemption target count for the named PodSet.
func ReclaimTargetCount(wl *kueue.Workload, podSetName kueue.PodSetReference) (int32, bool) {
	if wl.Status.Admission == nil {
		return 0, false
	}
	for i := range wl.Status.Admission.PodSetAssignments {
		psa := &wl.Status.Admission.PodSetAssignments[i]
		if psa.Name == podSetName && psa.ReclaimTargetCount != nil {
			return *psa.ReclaimTargetCount, true
		}
	}
	return 0, false
}
