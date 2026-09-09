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
	// fits reports whether the given counts can be admitted. The slice is scratch that is
	// overwritten on every probe, including long after a successful one, so an implementation
	// must not retain it - copy anything it needs to keep.
	fits       func([]int32) (R, bool)
	distribute distributeFunc
	// refine optionally improves the counts the shrink settled on, for a shrink that can give
	// up more than the constraints required. Only the ordered strategy sets it today; a
	// strategy whose shrink is already exact leaves it nil.
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

// NewOrderedPodSetReducer shrinks PodSets sequentially, starting from the
// last one in podSets and moving towards the first only once the current one
// has been shrunk down to its minimum count. A second pass then grows back
// whatever that order cut beyond what the constraints required, so the counts
// Reduce returns are not simply the result of one ordered shrink.
func NewOrderedPodSetReducer[R any](podSets []kueue.PodSet, fits func([]int32) (R, bool)) *PodSetReducer[R] {
	psr := newPodSetReducer(podSets, fits, distributeOrderBased)
	// The budget is a single number, but PodSets can draw on separate capacity - different node
	// selectors tied to different node groups, and so different flavors. Spending strictly from
	// the back can therefore drain a PodSet whose own capacity was never the constraint, which
	// the second pass gives back.
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

// Reduce returns the fits() result for the largest counts that fit, and false when no
// combination does. It gives up as little of PodSets[*].Count as the reduction strategy allows
// - which is not necessarily the smallest possible total, since a strategy may deliberately
// favour some PodSets over others.
func (psr *PodSetReducer[R]) Reduce() (R, bool) {
	var best R

	if psr.totalDelta == 0 {
		return best, false
	}

	// The searched range is [0, totalDelta], inclusive of totalDelta: cutting every PodSet down
	// to its minimum count is a candidate like any other, and is the only one left when nothing
	// smaller fits. sort.Search takes a half-open range, hence the +1.
	current := make([]int32, len(psr.podSets))
	// current is scratch for the budget being probed, and the binary search usually probes a
	// failing budget last. bestCounts is copied only on success, so it always describes the
	// same attempt as best - which refine needs as its starting point.
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

// giveBack grows back the PodSets that the ordered shrink cut further than the constraints
// required. It walks the PodSets from the first to the last, since those are the ones the
// order-based policy protects, and commits each grown count before moving on so that later
// PodSets are measured against the quota the earlier ones just took.
//
// It grows counts in place and returns the fits() result matching them.
//
// Two preconditions: counts must be a combination that fits, with best its fits() result; and a
// count that fits must imply every smaller count fits, holding the other PodSets steady, since
// the binary search below relies on it. The latter holds for a workload slice because it is
// pinned to the assignment of the slice it replaces, so a PodSet's assignment cannot change
// underneath the search. Were it ever violated the result would be a smaller admission, never
// an invalid one, because every count committed here came back from a successful fits().
func (psr *PodSetReducer[R]) giveBack(counts []int32, best R) (R, bool) {
	reduced := 0
	for i := range counts {
		if counts[i] < psr.fullCounts[i] {
			reduced++
		}
	}
	// A single reduced PodSet has nothing to redistribute: the shrink already found the
	// smallest budget that fits, and giving any of it back means a budget it has rejected.
	// This also keeps the second pass away from classic partial admission, where a Workload
	// may carry at most one minCount PodSet, so it only ever runs where the PodSets can draw
	// on separate capacity.
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

		// Otherwise look for the largest count that still fits, between the count the shrink
		// settled on (exclusive) and the full count (exclusive, having just failed).
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
