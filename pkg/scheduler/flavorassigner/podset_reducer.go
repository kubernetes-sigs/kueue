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
	"sort"

	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// distributeFunc spends a shrink budget of amount (out of totalDelta) across
// fullCounts, writing the resulting per-PodSet counts into out; deltas caps
// how much each PodSet can individually give up. Every out[i] must be
// monotonically non-increasing as amount grows, or the binary search in
// Reduce breaks.
type distributeFunc func(out, fullCounts, deltas []int32, amount, totalDelta int64)

// PodSetReducer helper structure used to find the largest counts between
// PodSets[*].MinCount and PodSets[*].Count that fit.
type PodSetReducer[R any] struct {
	podSets    []kueue.PodSet
	fullCounts []int32
	deltas     []int32
	totalDelta int64
	// fits reports whether the given counts can be admitted. The slice is scratch,
	// overwritten on every probe, so an implementation must copy anything it keeps.
	fits       func([]int32) (R, bool)
	distribute distributeFunc
	// refine optionally grows back counts the shrink gave up needlessly. Nil for a
	// strategy whose shrink is already exact.
	refine func(counts []int32, best R) (R, bool)
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

// NewOrderedPodSetReducer shrinks PodSets from the last towards the first, moving on only
// once the current one is at its minimum count, then gives back what that order cut
// needlessly. The budget is a single number, but PodSets tied to different node groups draw
// on separate capacity, so spending from the back can drain a PodSet whose own capacity was
// never the constraint.
func NewOrderedPodSetReducer[R any](podSets []kueue.PodSet, fits func([]int32) (R, bool)) *PodSetReducer[R] {
	psr := newPodSetReducer(podSets, fits, distributeOrderBased)
	psr.refine = psr.giveBack
	return psr
}

func distributeOrderBased(out, fullCounts, deltas []int32, amount, _ int64) {
	remaining := amount
	for i, d := range slices.Backward(deltas) {
		cut := min(int64(d), remaining)
		out[i] = fullCounts[i] - int32(cut)
		remaining -= cut
	}
}

// Reduce returns the fits() result for the largest counts the reduction strategy can admit,
// and false when no combination fits. A strategy may favour some PodSets over others, so the
// total is not necessarily the largest one possible.
func (psr *PodSetReducer[R]) Reduce() (R, bool) {
	var best R

	if psr.totalDelta == 0 {
		return best, false
	}

	current := make([]int32, len(psr.podSets))
	// current holds the budget probed last, usually a failing one, so the winning counts are
	// kept separately for refine to start from.
	bestCounts := make([]int32, len(psr.podSets))
	idx := sort.Search(int(psr.totalDelta)+1, func(i int) bool {
		psr.distribute(current, psr.fullCounts, psr.deltas, int64(i), psr.totalDelta)
		r, f := psr.fits(current)
		if f {
			best = r
			copy(bestCounts, current)
		}
		return f
	})

	// Not even the full reduction fit.
	if idx > int(psr.totalDelta) {
		return best, false
	}
	if psr.refine != nil {
		return psr.refine(bestCounts, best)
	}
	return best, true
}

// giveBack grows back the counts that the shrink cut more than it had to.
// It goes through the PodSets from first to last and grows each one as far as
// still fits before moving on - so a later PodSet only sees the capacity the earlier ones
// left. It grows counts in place and returns what fits() gave for the final counts.
func (psr *PodSetReducer[R]) giveBack(counts []int32, best R) (R, bool) {
	reduced := 0
	for i := range counts {
		if counts[i] < psr.fullCounts[i] {
			reduced++
		}
	}
	// With one reduced PodSet there is nothing to redistribute: growing it means a budget the
	// shrink already rejected. This also keeps the pass away from classic partial admission,
	// which allows at most one minCount PodSet per Workload.
	if reduced < 2 {
		return best, true
	}

	trial := make([]int32, len(counts))
	copy(trial, counts)

	for i := range counts {
		if counts[i] == psr.fullCounts[i] {
			continue
		}

		// Restoring the full count is the common case, and one call settles it.
		trial[i] = psr.fullCounts[i]
		if r, f := psr.fits(trial); f {
			counts[i], best = psr.fullCounts[i], r
			continue
		}

		// Otherwise find the largest count that still fits, between the count the shrink
		// settled on and the full count, both exclusive.
		lo, hi := counts[i]+1, psr.fullCounts[i]-1
		grownTo, grownBest := counts[i], best
		sort.Search(int(hi-lo+1), func(k int) bool {
			trial[i] = lo + int32(k)
			r, f := psr.fits(trial)
			if f {
				// sort.Search only raises its lower bound past a count that fits, so the
				// last one seen to fit is the largest one that does.
				grownTo, grownBest = trial[i], r
			}
			return !f
		})
		counts[i], trial[i], best = grownTo, grownTo, grownBest
	}
	return best, true
}
