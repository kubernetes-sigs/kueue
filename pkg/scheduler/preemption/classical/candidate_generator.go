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

package classical

import (
	"slices"
	"sort"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/clock"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
)

type candidateIterator struct {
	candidates                        []*candidateElem
	runIndex                          int
	frsNeedPreemption                 sets.Set[resources.FlavorResource]
	snapshot                          *schdcache.Snapshot
	NoCandidateFromOtherQueues        bool
	NoCandidateForHierarchicalReclaim bool
	hierarchicalReclaimCtx            *HierarchicalPreemptionCtx
}

type candidateElem struct {
	wl *workload.Info
	// lca of this queue and cq (queue to which the new workload is submitted)
	lca *schdcache.CohortSnapshot
	// candidates above priority threshold cannot be preempted if at the same time
	// cq would borrow from other queues/cohorts
	preemptionVariant preemptionVariant
}

func WorkloadUsesResources(wl *workload.Info, frsNeedPreemption sets.Set[resources.FlavorResource]) bool {
	for _, ps := range wl.TotalRequests {
		for res, flv := range ps.Flavors {
			if frsNeedPreemption.Has(resources.FlavorResource{Flavor: flv, Resource: res}) {
				return true
			}
		}
	}
	return false
}

// assume a prefix of the elements has condition WorkloadEvicted = true
func splitEvicted(workloads []*candidateElem) ([]*candidateElem, []*candidateElem) {
	firstFalse := sort.Search(len(workloads), func(i int) bool {
		return !workloadevict.IsEvicted(workloads[i].wl.Obj)
	})
	return workloads[:firstFalse], workloads[firstFalse:]
}

// quotaBand classifies where the cluster queue's usage lands after admitting the
// incoming workload, relative to its nominal quota and borrowing limit.
type quotaBand int

const (
	// usage + request <= nominal: fits within the guaranteed (nominal) quota.
	withinNominal quotaBand = iota
	// nominal < usage + request <= borrowingLimit: needs to borrow within the cohort.
	withinBorrowing
	// usage + request > borrowingLimit: exceeds the borrowing limit.
	exceedsBorrowing
)

// classifyQuotaBand computes the quotaBand for the preemptor cluster queue after
// admitting the incoming workload, and reports whether the queue's usage is at
// or above nominal for any flavor-resource needing preemption.
//
// The band is collapsed conjunctively: it is exceedsBorrowing only when EVERY
// flavor-resource needing preemption exceeds its borrowing limit AND the cohort's
// free quota already covers request - usage (the amount that must come from the
// cohort once the queue reclaims its own usage). Cross-queue preemption can only
// help by freeing cohort quota (a queue's own borrowing cap cannot be raised), so
// it is provably useless only when both conditions hold for all resources driving
// preemption. A single over-limit resource, or a cohort whose free quota is held
// by borrowing siblings, must not poison the classification — preempting other
// queues may still be required to make the workload fit.
func classifyQuotaBand(
	cq *schdcache.ClusterQueueSnapshot,
	requests resources.FlavorResourceQuantities,
	frsNeedPreemption sets.Set[resources.FlavorResource],
) (quotaBand, bool) {
	if len(frsNeedPreemption) == 0 {
		return withinNominal, false
	}
	band := exceedsBorrowing
	var usageAtOrAboveNominal bool
	for fr := range frsNeedPreemption {
		usage := cq.ResourceNode.Usage[fr]
		after := usage.Add(requests[fr])
		quota := cq.QuotaFor(fr)
		nominal := quota.Nominal

		// A nil BorrowingLimit means borrowing is unlimited, so usage can never
		// exceed the borrowing limit and only nominal is a meaningful boundary.
		hasBorrowingLimit := quota.BorrowingLimit != nil
		upper := nominal
		if hasBorrowingLimit {
			upper = upper.Add(*quota.BorrowingLimit)
		}
		switch {
		// needed = request - usage is the amount the cohort must supply from its
		// free capacity: same-queue preemption returns only local quota, so the
		// rest must come from the cohort. Using after - nominal instead understates
		// this by the queue's unused nominal (nominal - usage) and over-prunes when
		// a borrowing sibling holds that slack — cross-queue reclaim is then still
		// required. Verified: with q nominal=4/limit=4 usage=1 request=8, same-queue
		// preemption alone suffices only once cohort free >= 7 (= request - usage),
		// not >= 5 (= after - nominal).
		case hasBorrowingLimit && after.Cmp(upper) > 0 && cohortCanSupplyBorrowing(cq, fr, requests[fr].Sub(usage)):
			band = min(band, exceedsBorrowing)
		case after.Cmp(nominal) > 0:
			band = min(band, withinBorrowing)
		default: // after <= nominal
			band = min(band, withinNominal)
		}
		// Mirror queueUnderNominalInResourcesNeedingPreemption: the consumer
		// skips the no-borrowing run when usage >= nominal for a resource
		// needing preemption, making ReclaimWithoutBorrowing priority
		// candidates unusable.
		if !usageAtOrAboveNominal && usage.Cmp(nominal) >= 0 {
			usageAtOrAboveNominal = true
		}
	}
	return band, usageAtOrAboveNominal
}

// cohortCanSupplyBorrowing reports whether the cohort's currently free quota
// already covers needed. Callers pass needed = request - usage: reclaiming the
// queue's own workloads returns only local quota (it cannot raise the queue's
// borrowing cap), so request - usage is the amount that must instead come from
// the cohort's free capacity. When the cohort already has that much free,
// cross-queue preemption cannot help and is skipped; otherwise other queues may
// still need to be preempted to release cohort quota, so they must be kept.
func cohortCanSupplyBorrowing(cq *schdcache.ClusterQueueSnapshot, fr resources.FlavorResource, needed resources.Amount) bool {
	if !cq.HasParent() {
		// Without a cohort there are no cross-queue candidates to skip.
		return true
	}
	return cq.Parent().Available(fr).Cmp(needed) >= 0
}

// NewCandidateIterator creates a new iterator that yields candidate workloads for preemption
// The iterator can be used to perform two independent runs over the list of candidates:
// with and without borrowing. The runs are independent which means that the same candidates
// might be returned for both, but note that the candidates with borrowing are a subset of
// candidates without borrowing.
func NewCandidateIterator(
	hierarchicalReclaimCtx *HierarchicalPreemptionCtx,
	enabledAfs bool,
	frsNeedPreemption sets.Set[resources.FlavorResource],
	snapshot *schdcache.Snapshot,
	clock clock.Clock,
	ordering func(logr.Logger, bool, *workload.Info, *workload.Info, kueue.ClusterQueueReference, time.Time) int,
) *candidateIterator {
	cq := hierarchicalReclaimCtx.Cq
	band, usageAtOrAboveNominal := classifyQuotaBand(cq, hierarchicalReclaimCtx.Requests, frsNeedPreemption)

	sameQueueCandidates := collectSameQueueCandidates(hierarchicalReclaimCtx)
	var hierarchyCandidates, priorityCandidates []*candidateElem

	sortByOrdering := func(candidates []*candidateElem) {
		slices.SortFunc(candidates, func(a, b *candidateElem) int {
			return ordering(hierarchicalReclaimCtx.Log, enabledAfs, a.wl, b.wl, hierarchicalReclaimCtx.Cq.Name, clock.Now())
		})
	}
	// buildOrderedCandidates concatenates the candidate lists so that already
	// evicted workloads come first (they are the cheapest to preempt), keeping the
	// hierarchical-reclaim -> priority -> same-queue precedence within each group.
	buildOrderedCandidates := func(hierarchy, priority, sameQueue []*candidateElem) []*candidateElem {
		evictedHierarchy, nonEvictedHierarchy := splitEvicted(hierarchy)
		evictedPriority, nonEvictedPriority := splitEvicted(priority)
		evictedSameQueue, nonEvictedSameQueue := splitEvicted(sameQueue)
		out := make([]*candidateElem, 0, len(hierarchy)+len(priority)+len(sameQueue))
		out = append(out, evictedHierarchy...)
		out = append(out, evictedPriority...)
		out = append(out, evictedSameQueue...)
		out = append(out, nonEvictedHierarchy...)
		out = append(out, nonEvictedPriority...)
		out = append(out, nonEvictedSameQueue...)
		return out
	}

	var allCandidates []*candidateElem
	var noCandidateFromOtherQueues bool
	switch band {
	// exceedsBorrowing: every resource needing preemption exceeds the borrowing
	// limit and the cohort can already supply all the borrowing the queue would
	// need, so preempting workloads in other queues cannot free up anything
	// usable by this queue. Only same-queue candidates are worth considering —
	// we don't collect candidates from other ClusterQueues in the cohort, which
	// avoids the O(N) cohort scan on this path.
	//
	// By design this path can only ever preempt within the queue:
	// sameQueueCandidates is built by collectSameQueueCandidates, which scans
	// exactly ctx.Cq's own workloads. The hierarchy/priority pools — the only
	// source of other-queue candidates — are not collected here at all. That is
	// safe because a case that genuinely needs a cross-queue victim implies the
	// cohort cannot supply request-usage on its own, which makes
	// cohortCanSupplyBorrowing return false and routes the band to
	// withinBorrowing (where the cross-queue pools ARE collected) rather than
	// here. So the two conditions are mutually exclusive: if we reach this
	// branch, no other-queue preemption was ever required.
	case exceedsBorrowing:
		sortByOrdering(sameQueueCandidates)
		allCandidates = sameQueueCandidates
		noCandidateFromOtherQueues = true
	// withinBorrowing / withinNominal: both consider all candidate groups
	// (hierarchy, priority, same-queue) with identical collection and sort logic.
	// One shared special case: when hierarchy candidates are empty AND borrowing
	// within the cohort is forbidden AND the queue is at or above nominal in a
	// resource needing preemption, the consumer only runs the borrowing pass,
	// in which ReclaimWithoutBorrowing priority candidates are invalid anyway,
	// so they are dropped here. The guard mirrors
	// queueUnderNominalInResourcesNeedingPreemption used by the consumer, so
	// dropping the candidates cannot change the outcome.
	case withinBorrowing, withinNominal:
		hierarchyCandidates, priorityCandidates = collectCandidatesForHierarchicalReclaim(hierarchicalReclaimCtx)
		if len(hierarchyCandidates) == 0 {
			borrowWithinCohortForbidden, _ := IsBorrowingWithinCohortForbidden(cq)
			if borrowWithinCohortForbidden && usageAtOrAboveNominal {
				priorityCandidates = nil
			}
		}
		sortByOrdering(hierarchyCandidates)
		sortByOrdering(priorityCandidates)
		sortByOrdering(sameQueueCandidates)
		allCandidates = buildOrderedCandidates(hierarchyCandidates, priorityCandidates, sameQueueCandidates)
		noCandidateFromOtherQueues = len(hierarchyCandidates) == 0 && len(priorityCandidates) == 0
	}

	return &candidateIterator{
		runIndex:                          0,
		frsNeedPreemption:                 frsNeedPreemption,
		snapshot:                          snapshot,
		candidates:                        allCandidates,
		NoCandidateFromOtherQueues:        noCandidateFromOtherQueues,
		NoCandidateForHierarchicalReclaim: len(hierarchyCandidates) == 0,
		hierarchicalReclaimCtx:            hierarchicalReclaimCtx,
	}
}

// Next allows to iterate over the ordered sequence of candidates, with the reason
// for eviction returned together with a candidate.
func (c *candidateIterator) Next(borrow bool) (*workload.Info, string) {
	if c.runIndex >= len(c.candidates) {
		return nil, ""
	}
	candidate := c.candidates[c.runIndex]
	c.runIndex++
	if !c.candidateIsValid(candidate, borrow) {
		return c.Next(borrow)
	}
	return candidate.wl, candidate.preemptionVariant.PreemptionReason()
}

// candidateIsValid checks if candidate is valid,
// as eg. some candidates can only be considered without borrowing
// Also, preemption of candidates might invalidate other candidates
func (c *candidateIterator) candidateIsValid(candidate *candidateElem, borrow bool) bool {
	if c.hierarchicalReclaimCtx.Cq.Name == candidate.wl.ClusterQueue {
		return true
	}
	if borrow && candidate.preemptionVariant == ReclaimWithoutBorrowing {
		return false
	}
	cq := c.snapshot.ClusterQueue(candidate.wl.ClusterQueue)
	if schdcache.IsWithinNominalInResources(cq, c.frsNeedPreemption) {
		return false
	}
	// we don't go all the way to the root but only to the lca node
	for node := range cq.PathParentToRoot() {
		if node == candidate.lca {
			break
		}
		if schdcache.IsWithinNominalInResources(node, c.frsNeedPreemption) {
			return false
		}
	}
	return true
}

// Reset moves the candidate iterator back to the starting position.
// It is required to reset the iterator before each run.
func (c *candidateIterator) Reset() {
	c.runIndex = 0
}
