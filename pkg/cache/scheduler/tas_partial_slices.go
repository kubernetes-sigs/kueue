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

package scheduler

import (
	"math"
	"slices"

	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

// Placing a PodSet whose count is not a multiple of the slice size means
// placing k whole slices and one shorter, partial slice of tailSize pods.
// The partial slice is subject to the same constraint as the whole ones: it
// has to be held by a single domain at the slice level.
//
// The placement algorithm reasons in whole slices, so every domain carries a
// second capacity figure alongside sliceCount: the number of whole slices it
// can still hold once it also holds the partial slice. It is computed
// bottom-up in fillInCountsHelper, in the same pass and the same shape as the
// leader capacity:
//
//	at the slice level:  sliceCountWithTail(d) = (podCount(d) − tailSize) / sliceSize
//	above:               sliceCountWithTail(d) = sliceCount(d) − min_c tailPenalty(c)
//
// where tailPenalty(c) = sliceCount(c) − sliceCountWithTail(c) is what the
// child subtree c gives up by taking the partial slice, and the minimum runs
// over the children that can hold it at all. The domain selection then knows,
// before it commits to a set of domains, whether the partial slice has a
// home inside it, which is what neither reserving a whole slice for it nor
// placing it after the whole slices can know.

// noTailFit marks a domain that cannot hold the partial slice at all. It is
// negative so that it compares as "no room" against any number of slices,
// including zero.
const noTailFit int32 = -1

// hasTail reports whether the PodSet has a partial slice to place.
func (sh sliceShape) hasTail() bool {
	return sh.tailSize > 0
}

// newSliceShape describes how a PodSet of count pods is cut into slices of
// sliceSize.
//
// The tail is empty when the count divides evenly, when slices are not
// requested, and for multi-layer constraints, whose inner layers assume every
// layer divides cleanly - a partial tail generally does not divide by the inner
// sizes. Such requests are rejected at admission time when the feature is
// enabled, so this is only a safeguard for objects admitted earlier.
func newSliceShape(tr *kueue.PodSetTopologyRequest, count, sliceSize int32) sliceShape {
	shape := sliceShape{size: sliceSize}
	if !features.Enabled(features.TASPartialSlices) || sliceSize <= 1 {
		return shape
	}
	if len(utiltas.PodSetSliceRequiredTopologyConstraints(tr)) != 1 {
		return shape
	}
	shape.tailSize = count % sliceSize
	return shape
}

// sliceCountHostingTail returns the number of whole slices that still fit in a
// domain at the slice level once the partial slice is placed in it.
func sliceCountHostingTail(podCount int32, shape sliceShape) int32 {
	if podCount < shape.tailSize {
		return noTailFit
	}
	return (podCount - shape.tailSize) / shape.size
}

// fillTailCountsAtSliceLevel computes the tail capacities of a domain at the
// slice level, where the partial slice is charged directly against the
// domain's own pod count.
func fillTailCountsAtSliceLevel(ds *domainState, shape sliceShape) {
	ds.sliceCountWithTail = sliceCountHostingTail(ds.podCount, shape)
	ds.sliceCountWithLeaderAndTail = sliceCountHostingTail(ds.podCountWithLeader, shape)
}

// fillTailCounts computes the tail capacities of one domain, either directly
// when the domain is the one that has to hold the partial slice whole, or by
// charging the cheapest of its children for it.
//
// Child penalties are measured against the children's own slice counts, so they
// are subtracted from childrenSliceCapacity (the sum of child slice counts
// before any domain-level capacityBound) rather than from ds.sliceCount, which
// may already be clamped by capacityBound. The result is then bounded by what
// the domain's own pod count allows.
func fillTailCounts(ds *domainState, shape sliceShape, atSliceLevel bool, childrenSliceCapacity int32, penalties *tailPenaltyTracker) {
	if atSliceLevel {
		fillTailCountsAtSliceLevel(ds, shape)
		return
	}
	ds.sliceCountWithTail, ds.sliceCountWithLeaderAndTail = noTailFit, noTailFit
	if penalty, ok := penalties.tailPenalty(); ok {
		ds.sliceCountWithTail = min(childrenSliceCapacity-penalty, sliceCountHostingTail(ds.podCount, shape))
	}
	if penalty, ok := penalties.leaderAndTailPenalty(); ok {
		ds.sliceCountWithLeaderAndTail = min(childrenSliceCapacity-penalty, sliceCountHostingTail(ds.podCountWithLeader, shape))
	}
}

// cheapestChild tracks the two children with the lowest slice penalty
// (slices given up to host a partial slice or leader). Both are kept because
// the leader and partial slice may compete for the same cheapest child.
type cheapestChild struct {
	// bestIdx is the index of the child with the lowest penalty.
	bestIdx int
	// best is the lowest slice penalty seen so far.
	best int32
	// second is the runner-up slice penalty seen so far.
	second int32
	// hasBest reports whether at least one eligible child was seen.
	hasBest bool
	// hasSecond reports whether at least two eligible children were seen.
	hasSecond bool
}

func (c *cheapestChild) add(idx int, penalty int32) {
	switch {
	case !c.hasBest || penalty < c.best:
		c.second, c.hasSecond = c.best, c.hasBest
		c.bestIdx, c.best, c.hasBest = idx, penalty, true
	case !c.hasSecond || penalty < c.second:
		c.second, c.hasSecond = penalty, true
	}
}

// tailPenaltyTracker accumulates, over the children of one domain, the cost in
// whole slices of hosting the partial slice, and of hosting both the leader
// and the partial slice.
type tailPenaltyTracker struct {
	tail   cheapestChild
	leader cheapestChild
	// both is the cheapest single child holding the leader and the partial
	// slice together.
	both    int32
	hasBoth bool
}

// add folds in one child. leaderEligible mirrors the condition the caller uses
// for the leader capacity, so that a child which cannot hold the leader is not
// offered one.
func (t *tailPenaltyTracker) add(idx int, ds *domainState, leaderEligible bool) {
	if ds.sliceCountWithTail != noTailFit {
		t.tail.add(idx, ds.sliceCount-ds.sliceCountWithTail)
	}
	if !leaderEligible {
		return
	}
	t.leader.add(idx, ds.sliceCount-ds.sliceCountWithLeader)
	if ds.sliceCountWithLeaderAndTail != noTailFit {
		penalty := ds.sliceCount - ds.sliceCountWithLeaderAndTail
		if !t.hasBoth || penalty < t.both {
			t.both, t.hasBoth = penalty, true
		}
	}
}

// tailPenalty returns the slices the domain gives up by holding the partial
// slice somewhere among its children, and whether it can hold it at all.
func (t *tailPenaltyTracker) tailPenalty() (int32, bool) {
	return t.tail.best, t.tail.hasBest
}

// leaderAndTailPenalty returns the slices the domain gives up by holding both
// the leader and the partial slice, which may end up in the same child or in
// two different ones.
func (t *tailPenaltyTracker) leaderAndTailPenalty() (int32, bool) {
	best, found := t.both, t.hasBoth
	if t.leader.hasBest && t.tail.hasBest {
		if t.leader.bestIdx != t.tail.bestIdx {
			best, found = minPenalty(best, found, t.leader.best+t.tail.best, true)
		} else {
			best, found = minPenalty(best, found, t.leader.second+t.tail.best, t.leader.hasSecond)
			best, found = minPenalty(best, found, t.leader.best+t.tail.second, t.tail.hasSecond)
		}
	}
	return best, found
}

func minPenalty(best int32, found bool, candidate int32, ok bool) (int32, bool) {
	if ok && (!found || candidate < best) {
		return candidate, true
	}
	return best, found
}

// sliceCapacity returns the number of whole slices the domain can hold while
// also holding the obligations it is asked about. It returns noTailFit when the
// partial slice is asked for and does not fit.
func (s *TASFlavorSnapshot) sliceCapacity(d *domain, withLeader, withTail bool) int32 {
	return s.domainStateOf(d).sliceCapacity(withLeader, withTail)
}

// canHostTail reports whether the partial slice fits in the domain, with no
// whole slices of this PodSet alongside it.
func (s *TASFlavorSnapshot) canHostTail(d *domain) bool {
	return s.domainStateOf(d).sliceCountWithTail != noTailFit
}

// findBestFitDomainForSlicesWithTail is findBestFitDomainForSlices for the
// domain that also takes the partial slice, so the domains are ranked by the
// number of whole slices they hold next to it, breaking ties by their whole
// slice capacity because candidates are ordered whole-slice descending.
func (s *TASFlavorSnapshot) findBestFitDomainForSlicesWithTail(domains []*domain, sliceCount int32, leaderCount int32) *domain {
	candidates := s.topAffinityTierDomains(domains)
	bestDomain := candidates[0]
	bestTailCount := int32(math.MaxInt32)
	bestWholeCount := int32(math.MaxInt32)
	found := false
	withLeader := leaderCount > 0

	for _, domain := range candidates {
		if s.domainStateOf(domain).leaderCount < leaderCount {
			continue
		}
		tailCount := s.sliceCapacity(domain, withLeader, true)
		wholeCount := s.sliceCapacity(domain, withLeader, false)
		if tailCount >= sliceCount &&
			(tailCount < bestTailCount || (tailCount == bestTailCount && wholeCount < bestWholeCount)) {
			bestDomain = domain
			bestTailCount = tailCount
			bestWholeCount = wholeCount
			found = true
		}
	}
	if !found {
		return candidates[0]
	}
	return bestDomain
}

// cheapestTailDomains ranks the domains by the whole slices they give up by
// taking the partial slice. Two of them are kept, because the cheapest one
// may be the domain that ends up holding the leader.
func (s *TASFlavorSnapshot) cheapestTailDomains(domains []*domain) cheapestChild {
	var tail cheapestChild
	for i, d := range domains {
		ds := s.domainStateOf(d)
		if ds.sliceCountWithTail == noTailFit {
			continue
		}
		tail.add(i, ds.sliceCount-ds.sliceCountWithTail)
	}
	return tail
}

// leaderPenaltyWithTail returns the whole slices the domains give up when the
// leader goes to domains[idx] and the partial slice goes wherever it costs
// the least: to the same domain, or to the cheapest of the others. It reports
// false when putting the leader there leaves the partial slice no home.
//
// The two obligations are costed together because they compete for the same
// room, which is also how fillTailCounts costs them over the children of a
// domain. Costing the leader alone picks domains that the partial slice then
// has to be rejected for.
func (s *TASFlavorSnapshot) leaderPenaltyWithTail(domains []*domain, tailCosts *cheapestChild, idx int) (int32, bool) {
	ds := s.domainStateOf(domains[idx])
	penalty, found := int32(0), false
	if ds.sliceCountWithLeaderAndTail != noTailFit {
		penalty, found = ds.sliceCount-ds.sliceCountWithLeaderAndTail, true
	}
	if tailCosts.hasBest {
		tailPenalty, elsewhere := tailCosts.best, true
		if tailCosts.bestIdx == idx {
			tailPenalty, elsewhere = tailCosts.second, tailCosts.hasSecond
		}
		split := ds.sliceCount - ds.sliceCountWithLeader + tailPenalty
		if elsewhere && (!found || split < penalty) {
			penalty, found = split, true
		}
	}
	return penalty, found
}

// hostsTailWithAssignedSlices reports whether the domain can take the
// partial slice on top of the whole slices already assigned to it. The
// assigned count is read from sliceCount, which the descent trims to what the
// domain was given, while the tail capacities keep describing the room the
// domain started with.
func (s *TASFlavorSnapshot) hostsTailWithAssignedSlices(d *domain) bool {
	ds := s.domainStateOf(d)
	capacity := ds.sliceCountWithTail
	if ds.leaderCount > 0 {
		capacity = ds.sliceCountWithLeaderAndTail
	}
	return capacity != noTailFit && ds.sliceCount <= capacity
}

// placeTail gives the partial slice to one of the domains, and returns the
// domain it had to add to the assigned set to do so, if any.
//
// A domain that already holds whole slices of this PodSet is preferred, so that
// the partial slice does not widen the placement; only when none of them has
// the room for it is an unused domain opened.
func (s *TASFlavorSnapshot) placeTail(assigned, candidates []*domain, tailSize int32) (*domain, bool) {
	for _, d := range assigned {
		if s.hostsTailWithAssignedSlices(d) {
			s.domainStateOf(d).podCount += tailSize
			return nil, true
		}
	}
	// Best fit takes the closing domain out of order, so the candidates are
	// not disjoint from the assigned ones.
	d := s.firstDomainHostingTail(candidates, assigned)
	if d == nil {
		return nil, false
	}
	ds := s.domainStateOf(d)
	ds.sliceCount = 0
	ds.leaderCount = 0
	ds.podCount = tailSize
	return d, true
}

// finishSliceDistribution closes the assignment, giving the partial slice a
// home once the whole slices have been distributed.
func (s *TASFlavorSnapshot) finishSliceDistribution(used, all []*domain, shape sliceShape, count int32, tailPending bool) []*domain {
	if !tailPending {
		return used
	}
	return s.appendTailDomain(used, all, shape, count)
}

// appendTailDomain closes an assignment by giving the partial slice a home
// among the domains already used, or in one of those left unused, and returns
// the resulting set.
//
// Every candidate is offered, not only those past the domain that closed the
// count: best fit takes the closing domain out of order, leaving earlier ones
// unused. This mirrors findLevelWithFitDomains, which selected the domains
// knowing the partial slice has a home among them.
//
// It returns nil when no domain can take the partial slice, which the
// capacity roll-up is meant to have ruled out before the descent began.
func (s *TASFlavorSnapshot) appendTailDomain(used, candidates []*domain, shape sliceShape, count int32) []*domain {
	added, ok := s.placeTail(used, candidates, shape.tailSize)
	if !ok {
		// Error logs are not verbosity-gated; dumping leaves scales with cluster size.
		s.log.Error(errCodeAssumptionsViolated, "no domain left to hold the partial slice",
			"count", count,
			"tailSize", shape.tailSize,
			"sliceSize", shape.size,
			"topologyName", s.topologyName,
			"domainCount", len(candidates))
		return nil
	}
	if added != nil {
		used = append(used, added)
	}
	return used
}

// selectionHoldsTail reports whether one of the selected domains has room for
// the partial slice next to the whole slices it is expected to take.
//
// It runs before any count is assigned, so the expected amounts are passed in
// rather than read from the domains.
func (s *TASFlavorSnapshot) selectionHoldsTail(selected []*domain, assignedSlices []int32, takesLeader []bool) bool {
	for i, d := range selected {
		capacity := s.sliceCapacity(d, takesLeader[i], true)
		if capacity != noTailFit && assignedSlices[i] <= capacity {
			return true
		}
	}
	return false
}

// firstDomainHostingTail returns the first of the candidates that can hold the
// partial slice and is not selected yet, or nil when there is none.
func (s *TASFlavorSnapshot) firstDomainHostingTail(candidates, selected []*domain) *domain {
	taken := sets.New(selected...)
	for _, d := range candidates {
		if taken.Has(d) {
			continue
		}
		if !s.canHostTail(d) {
			continue
		}
		return d
	}
	return nil
}

// closingDomainWithTail checks whether the remaining whole slices, any
// remaining leaders, and the partial slice can all be closed in a single
// domain from candidates without stepping up to a larger whole-slice tier than
// the best-fit whole-slice choice.
func (s *TASFlavorSnapshot) closingDomainWithTail(
	candidates []*domain,
	remainingPrimary, remainingLeaderCount int32,
	shape sliceShape,
	unconstrained bool,
) (*domain, bool) {
	dom := candidates[0]
	withLeader := remainingLeaderCount > 0
	if s.sliceCapacity(dom, withLeader, false) < remainingPrimary ||
		s.domainStateOf(dom).leaderCount < remainingLeaderCount {
		return nil, false
	}
	tailDom := dom
	wholeFit := dom
	if useBestFitAlgorithm(unconstrained) {
		tailDom = s.findBestFitDomainForSlicesWithTail(candidates, remainingPrimary, remainingLeaderCount)
		wholeFit = s.findBestFitDomainForSlices(candidates, remainingPrimary, remainingLeaderCount)
	}
	if s.sliceCapacity(tailDom, withLeader, true) < remainingPrimary ||
		s.domainStateOf(tailDom).leaderCount < remainingLeaderCount ||
		s.sliceCapacity(tailDom, withLeader, false) > s.sliceCapacity(wholeFit, withLeader, false) {
		return nil, false
	}
	domainState := s.domainStateOf(tailDom)
	domainState.leaderCount = remainingLeaderCount
	domainState.sliceCountWithLeader = remainingPrimary
	domainState.sliceCount = remainingPrimary
	domainState.podCount = remainingPrimary*shape.size + shape.tailSize
	return tailDom, true
}

func (s *TASFlavorSnapshot) domainForAssignmentValues(levels, values []string) *domain {
	if len(levels) == 0 || len(values) == 0 {
		return nil
	}
	startIdx := slices.Index(s.levelKeys, levels[0])
	if startIdx == -1 {
		return nil
	}
	levelIdx := startIdx + len(values) - 1
	if levelIdx >= len(s.domainsPerLevel) {
		return nil
	}
	return s.domainsPerLevel[levelIdx][utiltas.DomainID(values)]
}

// sliceLevelDomain resolves an entry of a TopologyAssignment to its ancestor
// domain at sliceLevelIdx, or nil when the entry is not in the snapshot or does
// not reach sliceLevelIdx.
func (s *TASFlavorSnapshot) sliceLevelDomain(levels, values []string, sliceLevelIdx int) *domain {
	domain := s.domainForAssignmentValues(levels, values)
	for domain != nil && len(domain.levelValues) > sliceLevelIdx+1 {
		domain = domain.parent
	}
	if domain == nil || len(domain.levelValues) != sliceLevelIdx+1 {
		return nil
	}
	return domain
}

// normalizeTailLast moves the pods of the partial slice to the end of the
// domain ordering.
//
// The ungater ranks the pods of a PodSet by the order the domains appear in the
// published assignment (see rankToDomainID), so a slice occupies one domain in
// rank space only if the domain holding the trailing pods comes last. Domains
// are otherwise ordered by their level values, which says nothing about where
// the partial slice ends up, so the order is corrected here.
//
// Which domain holds the partial slice is recomputed from the assignment
// rather than remembered, which makes this idempotent and stable across the
// merges done to repair an assignment. It is a no-op when the pods divide
// evenly into slices.
func (s *TASFlavorSnapshot) normalizeTailLast(ta *utiltas.TopologyAssignment, tr *kueue.PodSetTopologyRequest, sliceSize int32) {
	if ta == nil || !features.Enabled(features.TASPartialSlices) || !slicesRequested(tr) || sliceSize <= 1 {
		return
	}
	sliceLevelIdx, found := s.resolveLevelIdx(s.sliceLevelKeyWithDefault(tr, s.lowestLevel()))
	if !found {
		return
	}
	remainder := utiltas.CountPodsInAssignment(ta) % sliceSize
	if remainder == 0 {
		return
	}

	usages := s.sliceLevelUsages(ta, sliceLevelIdx)
	tailIdx := slices.IndexFunc(usages, func(u sliceLevelUsage) bool {
		return u.count%sliceSize == remainder
	})
	if tailIdx < 0 || tailIdx == len(usages)-1 {
		return
	}
	tailDomainID := usages[tailIdx].domainID
	head := make([]utiltas.TopologyDomainAssignment, 0, len(ta.Domains))
	tail := make([]utiltas.TopologyDomainAssignment, 0, len(ta.Domains))
	for _, domainFromAssignment := range ta.Domains {
		domain := s.sliceLevelDomain(ta.Levels, domainFromAssignment.Values, sliceLevelIdx)
		if domain != nil && domain.id == tailDomainID {
			tail = append(tail, domainFromAssignment)
		} else {
			head = append(head, domainFromAssignment)
		}
	}
	ta.Domains = append(head, tail...)
}

// assignmentSliceAligned reports whether the assignment groups pods into whole
// slices: every domain at the slice level holds a multiple of sliceSize pods,
// except the last, which may hold the trailing pods of a partial slice.
func (s *TASFlavorSnapshot) assignmentSliceAligned(ta *utiltas.TopologyAssignment, tr *kueue.PodSetTopologyRequest, sliceSize int32) bool {
	if !features.Enabled(features.TASPartialSlices) || !slicesRequested(tr) || sliceSize <= 1 {
		return true
	}
	sliceLevelIdx, found := s.resolveLevelIdx(s.sliceLevelKeyWithDefault(tr, s.lowestLevel()))
	if !found {
		return true
	}
	usages := s.sliceLevelUsages(ta, sliceLevelIdx)
	total := int32(0)
	for _, usage := range usages {
		total += usage.count
	}
	if total != utiltas.CountPodsInAssignment(ta) {
		return false
	}
	for i, usage := range usages {
		expected := int32(0)
		if i == len(usages)-1 {
			expected = total % sliceSize
		}
		if usage.count%sliceSize != expected {
			return false
		}
	}
	return true
}
