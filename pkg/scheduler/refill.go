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
// refilled workload is nominated against the snapshot that already accounts
// for the admission, so same-CQ state is never stale. See Kueue#9345.
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

// newRefillPass returns the refill hook for this cycle, or nil (a no-op) when
// refill is disabled. Only the fair-sharing iterator is supported: refill relies
// on the tournament re-ranking the remaining entries on every pop, and ordering
// for the classical iterator is an open question.
func (s *Scheduler) newRefillPass(iterator entryIterator, snapshot *schdcache.Snapshot) *refillPass {
	if !features.Enabled(features.FairSharingRefill) {
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

// afterEntryProcessed is the refill hook, called after processEntry for
// every popped entry. Safe to call on a nil receiver.
func (r *refillPass) afterEntryProcessed(ctx context.Context, e *entry) {
	if r == nil || e.status != assumed {
		return
	}
	// Second-pass workloads already held quota before this cycle; their
	// admission does not free up a head slot for their ClusterQueue.
	if workload.HasQuotaReservation(e.Obj) {
		return
	}
	log := ctrl.LoggerFrom(ctx)
	// With WaitForPodsReady blockAdmission, admitting the refilled workload
	// would block the scheduler goroutine until all admitted workloads
	// (including the one just assumed) become ready. Leave the backlog for
	// the next cycle instead.
	if !r.scheduler.cache.PodsReadyForAllAdmittedWorkloads(log) {
		return
	}
	if r.budget <= 0 {
		// Log only when a successor actually exists in the heap, so the
		// count measures how often the budget binds rather than how often
		// admissions happen after exhaustion.
		if r.scheduler.queues.HasQueuedWorkloads(e.ClusterQueue) {
			log.V(3).Info("Refill budget exhausted; the ClusterQueue's next workload waits for the next cycle",
				"clusterQueue", klog.KRef("", string(e.ClusterQueue)))
		}
		return
	}
	wl := r.scheduler.queues.PopFrom(e.ClusterQueue)
	if wl == nil {
		return
	}
	// A popped workload consumes budget regardless of its nomination outcome,
	// so the total per-cycle work stays bounded.
	r.budget--
	wlLog := log.WithValues("workload", klog.KObj(wl.Obj), "clusterQueue", klog.KRef("", string(wl.ClusterQueue)))
	if r.scheduler.dropIfAlreadyAccounted(wlLog, *wl) {
		return
	}
	ne, nominated := r.scheduler.nominateWorkload(ctx, wlLog, *wl, r.snapshot)
	refilled := &ne
	if !nominated {
		r.inadmissibleEntries = append(r.inadmissibleEntries, refilled)
		log.V(3).Info("Refilled workload cannot be nominated in this cycle",
			"workload", klog.KObj(refilled.Obj),
			"clusterQueue", klog.KRef("", string(refilled.ClusterQueue)),
			"reason", refilled.inadmissibleMsg,
			"remainingBudget", r.budget)
		return
	}
	r.entries = append(r.entries, refilled)
	r.iterator.push(refilled)
	log.V(3).Info("Refilled the ClusterQueue's next workload into the running cycle",
		"workload", klog.KObj(refilled.Obj),
		"clusterQueue", klog.KRef("", string(refilled.ClusterQueue)),
		"remainingBudget", r.budget)
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
