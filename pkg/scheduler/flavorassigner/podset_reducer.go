/*
Copyright 2023 The Kubernetes Authors.

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

	"k8s.io/utils/pointer"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

// PodSetReducer helper structure used to gradually walk down
// from PodSets[*].Count to *PodSets[*].MinimumCount.
type PodSetReducer[R any] struct {
	podSets    []kueue.PodSet
	fullCounts []int32
	deltas     []int32
	totalDelta int32
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

		d := ps.Count - pointer.Int32Deref(ps.MinCount, ps.Count)
		psr.deltas[i] = d
		psr.totalDelta += d
	}
	return psr
}

func fillPodSetSizesForSearchIndex(out, fullCounts, deltas []int32, upFactor int32, downFactor int32) {
	// this will panic if len(out) < len(deltas)
	for i, v := range deltas {
		tmp := int32(int64(v) * int64(upFactor) / int64(downFactor))
		out[i] = fullCounts[i] - tmp
	}
}

// Find the first biggest set of counts that pass fits(), it's using binary Search
// so the last call to fits() might not be a successful one
// Returns nil if no solution was found
func (psr *PodSetReducer[R]) Search() (R, bool) {
	var lastGoodIdx int
	var lastR R

	if psr.totalDelta == 0 {
		return lastR, false
	}

	current := make([]int32, len(psr.podSets))
	idx := sort.Search(int(psr.totalDelta)+1, func(i int) bool {
		fillPodSetSizesForSearchIndex(current, psr.fullCounts, psr.deltas, int32(i), psr.totalDelta)
		r, f := psr.fits(current)
		if f {
			lastGoodIdx = i
			lastR = r
		}
		return f
	})
	return lastR, idx == lastGoodIdx
}
