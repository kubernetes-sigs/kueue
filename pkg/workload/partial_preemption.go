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

// IsPartialPreemptible reports whether the workload tolerates partial preemption: it is opted in
// (see IsPartialPreemptionJob) and at least one PodSet is used above its minCount, so some
// replicas can be shed without evicting the whole workload.
func IsPartialPreemptible(wl *kueue.Workload) bool {
	return IsPartialPreemptionJob(wl) && len(PartialPreemptibleCounts(wl)) > 0
}

// AdmittedPodSetCounts returns the admitted (accounted) count per PodSet, falling back to the spec
// count when the admission does not record a count.
func AdmittedPodSetCounts(wl *kueue.Workload) map[kueue.PodSetReference]int32 {
	if wl.Status.Admission == nil {
		return nil
	}
	specCounts := ExtractPodSetCountsFromWorkload(wl)
	counts := make(map[kueue.PodSetReference]int32, len(wl.Status.Admission.PodSetAssignments))
	for i := range wl.Status.Admission.PodSetAssignments {
		psa := &wl.Status.Admission.PodSetAssignments[i]
		counts[psa.Name] = ptr.Deref(psa.Count, specCounts[psa.Name])
	}
	return counts
}

// PartialPreemptibleCounts returns, per PodSet, the maximum number of pods that partial
// preemption may reclaim.
func PartialPreemptibleCounts(wl *kueue.Workload) map[kueue.PodSetReference]int32 {
	mins := minCountsByPodSet(wl)
	if len(mins) == 0 {
		return nil
	}
	admitted := AdmittedPodSetCounts(wl)
	afterReclaim := podSetsCountsAfterReclaim(wl)
	res := make(map[kueue.PodSetReference]int32, len(mins))
	for name, minCount := range mins {
		a, ok := admitted[name]
		if !ok {
			continue
		}
		used := a
		if r, ok := afterReclaim[name]; ok && r < used {
			used = r
		}
		if used > minCount {
			res[name] = used - minCount
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
			// Clone the Flavors map too: it is a reference type and the reduced Info is mutated in
			// the scheduling snapshot; sharing it with the original workload Info would risk
			// corrupting the live cache. Mirrors PodSetResources.ScaledTo.
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
