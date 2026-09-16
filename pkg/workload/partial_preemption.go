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
	"maps"
	"strconv"

	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
)

// minCountsByPodSet returns the spec minCount per PodSet, only for PodSets that set it.
func minCountsByPodSet(wl *kueue.Workload) map[kueue.PodSetReference]int32 {
	res := make(map[kueue.PodSetReference]int32, len(wl.Spec.PodSets))
	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		if ps.MinCount != nil {
			res[ps.Name] = *ps.MinCount
		}
	}
	return res
}

// IsPartialPreemptionJob reports the cheap eligibility gate for partial preemption:
//   - the PartialPreemption feature gate is enabled, and
//   - the job explicitly opted in via the partial-preemption annotation (propagated to the
//     Workload).
func IsPartialPreemptionJob(wl *kueue.Workload) bool {
	return features.Enabled(features.PartialPreemption) &&
		wl.GetAnnotations()[constants.PartialPreemptionAnnotation] == "true"
}

// HasReclaimTargetCount reports whether any admitted PodSet still has an in-flight
// partial-preemption reclaim target above its current count.
func HasReclaimTargetCount(wl *kueue.Workload) bool {
	specCounts := ExtractPodSetCountsFromWorkload(wl)
	for podSet, target := range ReclaimTargetCounts(wl) {
		if specCount, ok := specCounts[podSet]; ok && specCount > target {
			return true
		}
	}
	return false
}

// ReclaimTargetCounts returns the in-flight partial-preemption target counts by PodSet.
func ReclaimTargetCounts(wl *kueue.Workload) map[kueue.PodSetReference]int32 {
	if wl.Status.Admission == nil {
		return nil
	}
	res := make(map[kueue.PodSetReference]int32, len(wl.Status.Admission.PodSetAssignments))
	for i := range wl.Status.Admission.PodSetAssignments {
		psa := &wl.Status.Admission.PodSetAssignments[i]
		if psa.ReclaimTargetCount != nil {
			res[psa.Name] = *psa.ReclaimTargetCount
		}
	}
	return res
}

// MinCount returns the spec minCount for the named PodSet, if set. This is the target the PodSet is
// scaled down to during partial preemption.
func MinCount(wl *kueue.Workload, podSetName kueue.PodSetReference) (int32, bool) {
	mc, ok := minCountsByPodSet(wl)[podSetName]
	return mc, ok
}

// ReducedInfoForPartialPreemption returns a shallow copy of info whose per-PodSet resource
// usage is scaled down to the given target counts.
func ReducedInfoForPartialPreemption(info *Info, targetCounts map[kueue.PodSetReference]int32) *Info {
	reduced := *info
	reduced.TotalRequests = make([]PodSetResources, len(info.TotalRequests))
	for idx := range info.TotalRequests {
		psr := info.TotalRequests[idx]
		if tc, ok := targetCounts[psr.Name]; ok && psr.Count > 0 && tc >= 0 && tc < psr.Count {
			scaled := psr.Requests.Clone()
			scaled.Divide(int64(psr.Count))
			scaled.Mul(int64(tc))
			psr.Requests = scaled
			psr.Count = tc
			psr.Flavors = maps.Clone(psr.Flavors)
		}
		reduced.TotalRequests[idx] = psr
	}
	return &reduced
}

// SetReclaimTargetCount sets ReclaimTargetCount on the named PodSet's admission assignment and
// reports whether the stored value changed. It is a no-op returning false when the workload is not
// admitted or the PodSet is not found.
func SetReclaimTargetCount(wl *kueue.Workload, podSetName kueue.PodSetReference, count int32) bool {
	if wl.Status.Admission == nil {
		return false
	}
	for i := range wl.Status.Admission.PodSetAssignments {
		psa := &wl.Status.Admission.PodSetAssignments[i]
		if psa.Name == podSetName {
			if ptr.Deref(psa.ReclaimTargetCount, -1) == count {
				return false
			}
			psa.ReclaimTargetCount = new(count)
			return true
		}
	}
	return false
}

// PartialPreemptionMaxReclaimedCount returns the optional positive per-decision partial-preemption
// scale-down limit configured on the Workload.
func PartialPreemptionMaxReclaimedCount(wl *kueue.Workload) (int32, bool) {
	value := wl.GetAnnotations()[constants.PartialPreemptionMaxReclaimedCountAnnotation]
	count, err := strconv.ParseInt(value, 10, 32)
	if err != nil || count <= 0 {
		return 0, false
	}
	return int32(count), true
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
