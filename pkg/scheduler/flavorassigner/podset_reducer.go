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
	"slices"

	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// distributeFunc spends a shrink budget of amount (out of totalDelta) across
// fullCounts, writing the resulting per-PodSet counts into out; deltas caps
// how much each PodSet can individually give up. Every out[i] must be
// monotonically non-increasing as amount grows, or the binary search in
// Search breaks.
type distributeFunc func(out, fullCounts, deltas []int32, amount, totalDelta int64)

// PodSetReducer helper structure used to gradually walk down
// from PodSets[*].Count to *PodSets[*].MinimumCount.
type PodSetReducer[R any] struct {
	podSets    []kueue.PodSet
	fullCounts []int32
	deltas     []int32
	totalDelta int64
	fits       func([]int32) (R, bool)
	distribute distributeFunc
}

func newPodSetReducer[R any](podSets []kueue.PodSet, fits func([]int32) (R, bool), distribute distributeFunc) *PodSetReducer[R] {
	psr := &PodSetReducer[R]{
		podSets:    podSets,
		deltas:     make([]int32, len(podSets)),
		fullCounts: make([]int32, len(podSets)),
		fits:       fits,
		distribute: distribute,
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

// NewOrderedPodSetReducer shrinks PodSets sequentially, starting from the
// last one in podSets and moving towards the first only once the current one
// has been shrunk down to its minimum count.
func NewOrderedPodSetReducer[R any](podSets []kueue.PodSet, fits func([]int32) (R, bool)) *PodSetReducer[R] {
	return newPodSetReducer(podSets, fits, distributeOrderBased)
}

func distributeOrderBased(out, fullCounts, deltas []int32, amount, _ int64) {
	remaining := amount
	for i, d := range slices.Backward(deltas) {
		cut := min(int64(d), remaining)
		out[i] = fullCounts[i] - int32(cut)
		remaining -= cut
	}
}

// Search find the first biggest set of counts that pass fits(), it's using
// binary Search so the last call to fits() might not be a successful one
// Returns nil if no solution was found
func (psr *PodSetReducer[R]) Search() (R, bool) {
	var lastGoodIdx int64
	var lastR R

	if psr.totalDelta == 0 {
		return lastR, false
	}

	current := make([]int32, len(psr.podSets))
	idx := searchInt64(psr.totalDelta+1, func(i int64) bool {
		psr.distribute(current, psr.fullCounts, psr.deltas, i, psr.totalDelta)
		r, f := psr.fits(current)
		if f {
			lastGoodIdx = i
			lastR = r
		}
		return f
	})
	return lastR, idx == lastGoodIdx
}

func searchInt64(n int64, f func(int64) bool) int64 {
	var i int64
	j := n

	for i < j {
		h := i + (j-i)/2
		if !f(h) {
			i = h + 1
		} else {
			j = h
		}
	}
	return i
}
