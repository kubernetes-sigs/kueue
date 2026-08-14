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
// may pop after the initial ClusterQueue heads. The bound keeps cycle length
// and time-to-fresh-snapshot predictable under a large backlog.
// TODO: make this configurable via the Kueue Configuration API.
const defaultRefillBudget = 8

// refillPass implements refill for fair sharing: when a workload is admitted,
// its ClusterQueue's next workload immediately joins the running scheduling
// cycle instead of waiting for the next one. Without refill, a cohort that
// frees several units of capacity in one cycle serves the poorest
// ClusterQueue's single head and over-share siblings pick up the rest; the
// refilled workload is nominated against the snapshot whose usage already
// accounts for the admission. Only usage is current: the snapshot's workload
// membership stays frozen for the cycle, and other entries may have reserved
// capacity for workloads that are not admitted yet. A refilled workload
// therefore acts on its assignment only when the mode is Fit: Preempt and
// DeferredFit are requeued for the next cycle, while the structural NoFit
// parks as usual (see processEntry). See Kueue#9345.
//
// When the budget runs out, the cycle keeps processing the entries already in
// the room; the remaining backlog waits for the next cycle.
//
// The mid-cycle insertion primitive lives in fairSharingIterator.push so
// other mechanisms can re-enter entries mid-cycle independently of the
// refill-specific "pop next + budget" logic below.
type refillPass struct {
	scheduler *Scheduler
	iterator  *fairSharingIterator
	snapshot  *schdcache.Snapshot

	// budget is the remaining number of extra workloads this cycle may pop
	// after the initial ClusterQueue heads.
	budget int

	// entries and inadmissibleEntries track the workloads popped mid-cycle so
	// the cycle's requeue step reaches them; they are not part of the entries
	// slice built during initial nomination.
	entries             []*entry
	inadmissibleEntries []*entry
}

// newRefillPass returns nil when refill cannot run. Refill relies on the
// tournament re-ranking the remaining entries on every pop, so only the
// fair-sharing iterator qualifies; ordering for the classical one is unanswered.
// WaitForPodsReady with blockAdmission already serializes the cycle, so a
// refilled successor would only lengthen it; the cache's pods-ready tracking
// stands in for that setting, which the manager turns on for exactly that
// configuration.
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

// refillStopReason values are the labels a future refill_stops_total{reason}
// would inherit. Unlike a qcache.RequeueReason they decide nothing.
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

// afterEntryProcessed is the refill hook, called after processEntry for
// every popped entry. Safe to call on a nil receiver.
func (r *refillPass) afterEntryProcessed(ctx context.Context, e *entry) {
	if r == nil {
		return
	}
	reason, refilled := r.tryRefill(ctx, e)
	// Entries that were never admitted say nothing about refill and are the
	// common case; logging them would bury the real stop reasons.
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

// tryRefill pops the ClusterQueue's next workload into the cycle, or reports
// where the chain stopped and which entry it stopped on.
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
	// A popped workload consumes budget regardless of its nomination outcome,
	// so the total per-cycle work stays bounded.
	r.budget--
	log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(wl.Obj), "clusterQueue", klog.KRef("", string(wl.ClusterQueue)))
	if r.scheduler.dropIfAlreadyAccounted(log, *wl) {
		// Already accounted in the cache, so it leaves the cycle without
		// being requeued; the drop released its inflight claim.
		return refillStopSuccessorNotNominated, &entry{Head: *wl}
	}
	refilled, nominated := r.scheduler.nominateWorkload(ctx, log, *wl, r.snapshot)
	refilled.refilled = true
	if nominated {
		// The tournament reranks it against the remaining entries on the
		// next pop.
		r.entries = append(r.entries, &refilled)
		r.iterator.push(&refilled)
		return refillContinue, &refilled
	}
	// Parks with its inadmissibleMsg; the cycle's requeue step reaches
	// it through refilledInadmissible.
	r.inadmissibleEntries = append(r.inadmissibleEntries, &refilled)
	return refillStopSuccessorNotNominated, &refilled
}

// nominatedEntries returns the refilled entries that competed for admission
// this cycle. Safe to call on a nil receiver.
func (r *refillPass) nominatedEntries() []*entry {
	if r == nil {
		return nil
	}
	return r.entries
}

// inadmissible returns the refilled entries that could not be nominated. Safe
// to call on a nil receiver.
func (r *refillPass) inadmissible() []*entry {
	if r == nil {
		return nil
	}
	return r.inadmissibleEntries
}
