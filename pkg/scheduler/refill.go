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
	"context"

	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/workload"
)

// defaultRefillBudget bounds the number of extra workloads a scheduling cycle
// may pop after the initial ClusterQueue heads, keeping cycle length and
// time-to-fresh-snapshot predictable under a large backlog. The allowance is
// global and per-cycle, not a per-ClusterQueue or per-cohort quota, so one
// queue can spend all of it.
// TODO(#14190): make this configurable via the Kueue Configuration API.
const defaultRefillBudget = 8

// refillPass implements refill for fair sharing. Without it, a cohort that
// frees several units of capacity in one cycle serves the poorest
// ClusterQueue's single head and over-share siblings pick up the rest.
// A refilled workload acts on its assignment only when the mode is Fit; see
// processEntry. See Kueue#9345.
type refillPass struct {
	scheduler *Scheduler
	iterator  *fairSharingIterator
	snapshot  *schdcache.Snapshot

	// budget is how many more workloads this cycle may pull in. Spent on every
	// workload pulled in, not only the ones that get admitted, so the extra
	// work per cycle stays bounded regardless of how the nominations turn out.
	budget int

	// pushed and parked hold the workloads popped mid-cycle so the cycle's
	// requeue step reaches them: they are not in the entries slice built
	// during nomination.
	pushed []*entry
	parked []*entry
}

// newRefillPass returns the refill hook for this cycle, or nil (a no-op) when
// refill is disabled. Only the fair-sharing iterator is supported: refill
// relies on the tournament re-ranking the remaining entries on every pop, and
// ordering for the classical iterator is an open question.
//
// WaitForPodsReady with blockAdmission disables refill outright, because that
// configuration already serializes the cycle: a refilled successor would make
// the current cycle longer without being admitted any sooner. The cache's
// pods-ready tracking stands in for the setting, which the manager turns on
// for exactly that configuration.
func (s *Scheduler) newRefillPass(iterator entryIterator, snapshot *schdcache.Snapshot) *refillPass {
	if !features.Enabled(features.FairSharingRefill) {
		return nil
	}
	if s.cache.PodsReadyTracking() {
		return nil
	}
	fsIterator, ok := iterator.(*fairSharingIterator)
	if !ok {
		return nil
	}
	return &refillPass{
		scheduler: s,
		iterator:  fsIterator,
		snapshot:  snapshot,
		budget:    s.refillBudget,
	}
}

// refillStopReason names the point at which a refill chain stopped, so a future
// refill_stops_total{reason} has the vocabulary ready. Unlike a
// qcache.RequeueReason, which selects what happens next to the workload it
// describes, these decide nothing.
type refillStopReason string

const (
	// refillContinue means a successor joined the cycle, so nothing stopped.
	refillContinue refillStopReason = ""
	// The entry was not admitted, so it freed no head slot.
	refillStopNotAdmitted refillStopReason = "EntryNotAdmitted"
	// The entry already held quota before this cycle, so its admission freed
	// no head slot either.
	refillStopSecondPass refillStopReason = "SecondPassAdmission"
	refillStopBudget     refillStopReason = "BudgetExhausted"
	refillStopQueueEmpty refillStopReason = "QueueEmpty"
	// The successor was popped but got no assignment: it either parks as
	// inadmissible or is already accounted in the cache.
	refillStopSuccessorNotNominated refillStopReason = "SuccessorNotNominated"
)

// afterEntryProcessed pulls a ClusterQueue's next workload into the running
// cycle when one of its workloads is admitted, so a queue that is still the
// poorest can win again this cycle instead of waiting for the next one.
// Called after processEntry for every popped entry; safe on a nil receiver.
// A refill is logged, and so is where a chain stopped instead — except for the
// entries that were never admitted: they are the common case and would bury
// the real reasons.
func (r *refillPass) afterEntryProcessed(ctx context.Context, e *entry) {
	if r == nil {
		return
	}
	reason, refilled := r.tryRefill(ctx, e)
	if reason == refillStopNotAdmitted {
		return
	}
	log := ctrl.LoggerFrom(ctx)
	if refilled != nil {
		log = log.WithValues("workload", klog.KObj(refilled.Obj))
	}
	if reason == refillContinue {
		log.V(3).Info("Refilled the ClusterQueue's next workload into the running cycle",
			"clusterQueue", klog.KRef("", string(e.ClusterQueue)),
			"remainingBudget", r.budget)
		return
	}
	log.V(3).Info("Refill stopped after an admission",
		"reason", reason,
		"clusterQueue", klog.KRef("", string(e.ClusterQueue)),
		"remainingBudget", r.budget)
}

// tryRefill pops the ClusterQueue's next workload into the running cycle, or
// reports where the chain stopped. The second value is the popped entry.
func (r *refillPass) tryRefill(ctx context.Context, e *entry) (refillStopReason, *entry) {
	// Only a fresh admission frees a head slot for its ClusterQueue.
	if e.status != assumed {
		return refillStopNotAdmitted, nil
	}
	// Second-pass workloads already held quota before this cycle, so their
	// admission does not free a slot the successor could take.
	if workload.HasQuotaReservation(e.Obj) {
		return refillStopSecondPass, nil
	}
	if r.budget <= 0 {
		// Kept apart so BudgetExhausted measures how often the budget
		// actually binds, not how often it is merely spent.
		if !r.scheduler.queues.HasQueuedWorkloads(e.ClusterQueue) {
			return refillStopQueueEmpty, nil
		}
		return refillStopBudget, nil
	}
	wl := r.scheduler.queues.PopFrom(e.ClusterQueue)
	if wl == nil {
		return refillStopQueueEmpty, nil
	}
	r.budget--
	refilled, outcome := r.scheduler.nominateWorkload(ctx, *wl, r.snapshot)
	refilled.refilled = true
	switch outcome {
	case nominationOK:
		// The tournament reranks it against the remaining entries.
		r.pushed = append(r.pushed, &refilled)
		r.iterator.push(&refilled)
		return refillContinue, &refilled
	case nominationInadmissible:
		// The cycle's requeue step reaches it through refilledInadmissible.
		r.parked = append(r.parked, &refilled)
	case nominationDropped:
		// Already accounted in the cache, so it leaves the cycle without
		// being requeued; nominateWorkload drops its inflight claim.
	}
	return refillStopSuccessorNotNominated, &refilled
}

// refilledEntries returns the refilled entries that competed for admission
// this cycle. Safe to call on a nil receiver.
func (r *refillPass) refilledEntries() []*entry {
	if r == nil {
		return nil
	}
	return r.pushed
}

// refilledInadmissible returns the refilled entries that could not be
// nominated. Safe to call on a nil receiver.
func (r *refillPass) refilledInadmissible() []*entry {
	if r == nil {
		return nil
	}
	return r.parked
}
