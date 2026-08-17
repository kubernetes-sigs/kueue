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
	"sigs.k8s.io/kueue/pkg/workload/concurrentadmission"
)

// defaultRefillBudget keeps cycle length and time-to-fresh-snapshot predictable
// under a large backlog. The allowance is global and per-cycle, not a
// per-ClusterQueue or per-cohort quota, so one queue can spend all of it.
// TODO(#14190): make this configurable via the Kueue Configuration API.
const defaultRefillBudget = 8

// refillPass exists because a cohort that frees several units of capacity in
// one cycle otherwise serves the poorest ClusterQueue's single head and lets
// over-share siblings pick up the rest. See Kueue#9345.
// A nil *refillPass is the disabled case, and every method tolerates it.
type refillPass struct {
	scheduler *Scheduler
	iterator  *fairSharingIterator
	snapshot  *schdcache.Snapshot

	// budget is how many more workloads this cycle may pull in. Spent on every
	// workload pulled in, not only the ones that get admitted, so the extra
	// work per cycle stays bounded regardless of how the nominations turn out.
	budget int

	// Mid-cycle pops are not in the slices nomination built, so the cycle's
	// terminal steps reach them only through these.
	pushed []*entry
	parked []*entry
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
	refillContinue                  refillStopReason = ""
	refillStopNotAdmitted           refillStopReason = "EntryNotAdmitted"
	refillStopSecondPass            refillStopReason = "SecondPassAdmission"
	refillStopVariantAdmitted       refillStopReason = "VariantAdmitted"
	refillStopBudget                refillStopReason = "BudgetExhausted"
	refillStopQueueEmpty            refillStopReason = "QueueEmpty"
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
	// The sibling scan reads the snapshot, which lacks this cycle's admissions,
	// so a refill here could admit a second variant of the same parent.
	if features.Enabled(features.ConcurrentAdmission) && concurrentadmission.IsVariant(e.Obj) {
		return refillStopVariantAdmitted, nil
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
	log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(wl.Obj), "clusterQueue", klog.KRef("", string(wl.ClusterQueue)))
	if r.scheduler.dropIfAlreadyAccounted(log, *wl) {
		// The drop released the checkout, so there is nothing to park.
		return refillStopSuccessorNotNominated, &entry{Head: *wl}
	}
	refilled, nominated := r.scheduler.nominateWorkload(ctx, log, *wl, r.snapshot)
	refilled.refilled = true
	if nominated {
		r.pushed = append(r.pushed, &refilled)
		r.iterator.push(&refilled)
		return refillContinue, &refilled
	}
	r.parked = append(r.parked, &refilled)
	return refillStopSuccessorNotNominated, &refilled
}

// refilledEntries returns the entries refill pushed into the tournament.
func (r *refillPass) refilledEntries() []*entry {
	if r == nil {
		return nil
	}
	return r.pushed
}

// refilledInadmissible returns the entries refill popped but could not nominate.
func (r *refillPass) refilledInadmissible() []*entry {
	if r == nil {
		return nil
	}
	return r.parked
}
