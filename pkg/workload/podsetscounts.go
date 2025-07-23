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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
)

// PodSetsCounts represents a mapping between PodSet references and their desired replica counts.
//
// Each key in the map corresponds to a unique kueue.PodSetReference, and the associated value
// is the number of replicas (int32) requested for that PodSet. This type is used to facilitate
// comparisons, updates, and compatibility checks between different sets of PodSet definitions
// within jobs and workloads.
type PodSetsCounts map[kueue.PodSetReference]int32

// HasSamePodSetKeys returns true if both PodSetsCounts have exactly the same PodSet keys,
// ignoring their replica count values.
func (c PodSetsCounts) HasSamePodSetKeys(in PodSetsCounts) bool {
	return maps.EqualFunc(c, in, func(_ int32, _ int32) bool { return true })
}

// EqualTo returns true if this PodSetsCount is identical (on keys and values) to
// the incoming PodSetsCount.
func (c PodSetsCounts) EqualTo(in PodSetsCounts) bool {
	return maps.Equal(c, in)
}

// HasFewerReplicasThan returns true if there is at least one pod set in the current set (`c`)
// whose count is lower than the corresponding entry in the input set (`in`).
//
// Note: This function does not consider keys that exist in current set (`c`) but not the other (`in`).
func (c PodSetsCounts) HasFewerReplicasThan(in PodSetsCounts) bool {
	for key, cCount := range c {
		if inCount, ok := in[key]; ok && cCount < inCount {
			return true
		}
	}
	return false
}

// ExtractPodSetCounts builds a PodSetsCounts map from a list of PodSets.
// Each entry maps PodSet name to its replica count.
func ExtractPodSetCounts(podSets []kueue.PodSet) PodSetsCounts {
	return utilslices.ToMap(podSets, func(i int) (kueue.PodSetReference, int32) {
		return podSets[i].Name, podSets[i].Count
	})
}

// ExtractPodSetCountsFromWorkload returns a PodSetsCounts map derived from the provided Workload.
//
// Important: This function assumes the Workload is not nil. It does not perform a nil check,
// and calling it with a nil Workload will result in a panic.
func ExtractPodSetCountsFromWorkload(wl *kueue.Workload) PodSetsCounts {
	return ExtractPodSetCounts(wl.Spec.PodSets)
}

// ApplyPodSetCounts updates the count values of a Workload's PodSets based on the provided counts.
//
// This function only updates existing PodSets within the Workload. It ignores:
//   - PodSet names present in the counts map but not found in the Workload.
//   - PodSets in the Workload that are not present in the counts map.
//
// Important: This function assumes the Workload is not nil. It does not perform a nil check,
// and calling it with a nil Workload will result in a panic.
func ApplyPodSetCounts(wl *kueue.Workload, counts PodSetsCounts) {
	for i := range wl.Spec.PodSets {
		if count, found := counts[wl.Spec.PodSets[i].Name]; found {
			wl.Spec.PodSets[i].Count = count
		}
	}
}
