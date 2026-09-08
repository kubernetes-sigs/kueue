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

package flavorassigner

import (
	"sort"

	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// PodSetReducer helper structure used to gradually walk down
// from PodSets[*].Count to *PodSets[*].MinimumCount.
type PodSetReducer[R any] struct {
	podSets    []kueue.PodSet
	fullCounts []int32
	deltas     []int32
	totalDelta int64
	fits       func([]int32) (R, bool)
}

func NewPodSetReducer[R any](podSets []kueue.PodSet, fits func([]int32) (R, bool)) *PodSetReducer[R] {
	psr := &PodSetReducer[R]{
		podSets:    podSets,
		deltas:     make([]int32, len(podSets)),
		fullCounts: make([]int32, len(podSets)),
		fits:       fits,
	}

	for i := range psr.podSets {
		ps := &psr.podSets[i]
		psr.fullCounts[i] = ps.Count

		d := ps.Count - ptr.Deref(ps.MinCount, ps.Count)
		psr.deltas[i] = d
		psr.totalDelta += int64(d)
	}
	return psr
}

func fillPodSetSizesForSearchIndex(out, fullCounts, deltas []int32, upFactor, downFactor int64) {
	// this will panic if len(out) < len(deltas)
	for i, v := range deltas {
		tmp := int32(int64(v) * upFactor / downFactor)
		out[i] = fullCounts[i] - tmp
	}
}

// Search find the first biggest set of counts that pass fits(), it's using
// binary Search so the last call to fits() might not be a successful one
// Returns nil if no solution was found
func (psr *PodSetReducer[R]) Search() (R, bool) {
	var lastR R

	if psr.totalDelta == 0 {
		return lastR, false
	}

	current := make([]int32, len(psr.podSets))
	idx := sort.Search(int(psr.totalDelta), func(i int) bool {
		fillPodSetSizesForSearchIndex(current, psr.fullCounts, psr.deltas, int64(i), psr.totalDelta)
		r, f := psr.fits(current)
		if f {
			lastR = r
		}
		return f
	})

	if idx < int(psr.totalDelta) {
		return lastR, true
	}

	// sort.Search searches [0, totalDelta), so check totalDelta separately.
	fillPodSetSizesForSearchIndex(current, psr.fullCounts, psr.deltas, psr.totalDelta, psr.totalDelta)
	return psr.fits(current)
}
