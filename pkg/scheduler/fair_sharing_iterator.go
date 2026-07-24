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
	"slices"
	"strings"

	"github.com/go-logr/logr"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/workload"
)

// fairSharingIterator orders candidates in a "fair" manner for
// consideration by scheduling when FairSharing is enabled. See
// runTournament for description of algorithm.
type fairSharingIterator struct {
	// cqToEntries tracks ClusterQueues which still have workloads to
	// schedule, and the corresponding workload entries. A ClusterQueue
	// may have several entries in a cycle (e.g. second-pass workloads
	// in addition to the queue head); the slices are never empty and
	// are ordered by compareEntries, so the first element is the
	// ClusterQueue's current nominee.
	cqToEntries   map[*schdcache.ClusterQueueSnapshot][]*entry
	entryComparer entryComparer
	log           logr.Logger
}

func makeFairSharingIterator(ctx context.Context, entries []entry, workloadOrdering workload.Ordering) *fairSharingIterator {
	log := ctrl.LoggerFrom(ctx)
	f := fairSharingIterator{
		cqToEntries: make(map[*schdcache.ClusterQueueSnapshot][]*entry, len(entries)),
		entryComparer: entryComparer{
			log:              log,
			workloadOrdering: workloadOrdering,
		},
		log: log,
	}
	for i := range entries {
		cq := entries[i].clusterQueueSnapshot
		f.cqToEntries[cq] = append(f.cqToEntries[cq], &entries[i])
	}
	for _, cqEntries := range f.cqToEntries {
		slices.SortFunc(cqEntries, func(a, b *entry) int {
			return compareEntries(log, workloadOrdering, a, b)
		})
	}
	return &f
}

func (f *fairSharingIterator) hasNext() bool {
	return len(f.cqToEntries) > 0
}

// popEntryForCq removes and returns the first pending entry of the
// ClusterQueue, dropping the ClusterQueue from the iterator once it has
// no entries left.
func (f *fairSharingIterator) popEntryForCq(cq *schdcache.ClusterQueueSnapshot) *entry {
	cqEntries := f.cqToEntries[cq]
	head := cqEntries[0]
	if len(cqEntries) == 1 {
		delete(f.cqToEntries, cq)
	} else {
		f.cqToEntries[cq] = cqEntries[1:]
	}
	return head
}

func (f *fairSharingIterator) pop() *entry {
	cq := f.getCq()

	// CQ has no Cohort. We simply return its nominated workload.
	if !cq.HasParent() {
		entry := f.popEntryForCq(cq)
		f.log.V(3).Info("Returning workload from ClusterQueue without Cohort",
			"clusterQueue", klog.KRef("", string(cq.GetName())),
			"workload", klog.KObj(entry.Obj))
		return entry
	}

	// CQ is part of a Cohort. We run a tournament, to select the
	// most fair workload at each level.
	root := cq.Parent().Root()
	log := f.log.WithValues("rootCohort", klog.KRef("", string(root.GetName())))

	log.V(5).Info("Computing DominantResourceShare for tournament")
	f.entryComparer.computeDRS(root, f.cqToEntries)

	log.V(3).Info("Running tournament to decide next workload to consider in scheduling cycle")
	entry := runTournament(root, f.entryComparer, f.cqToEntries)

	log = log.WithValues(
		"cohort", klog.KRef("", string(entry.clusterQueueSnapshot.Parent().GetName())),
		"clusterQueue", klog.KRef("", string(entry.clusterQueueSnapshot.GetName())),
		"winningWorkload", klog.KObj(entry.Obj))

	log.V(3).Info("Determined tournament winner")
	f.entryComparer.logDrsValuesWhenVerbose(log)

	// The winner is always the first entry of its ClusterQueue, as only
	// the first entry of each ClusterQueue takes part in the tournament.
	f.popEntryForCq(entry.clusterQueueSnapshot)
	return entry
}

// getCq returns a CQ with a workload pending scheduling. This
// function is non-deterministic. We don't have any guarantees on
// scheduling order of workloads in different Cohort. Workload
// consideration is nearly deterministic within Cohort (only when DRS,
// Priority, and time are equal it is non-deterministic).
func (f *fairSharingIterator) getCq() *schdcache.ClusterQueueSnapshot {
	for cq := range f.cqToEntries {
		return cq
	}
	return nil
}

// runTournament is a recursive algorithm which nominates one workload
// for each Cohort.  It compares the DominantResourceShare (DRS) value
// of each of the Cohort's children nodes (CQs or Cohorts), including
// in this DRS value the workload which that child node is
// nominating. The node with the lowest DRS wins, with some additional
// tiebreaks (see entryComparer.less).
//
// This process results in one workload (or zero if Cohort has no
// remaining workloads to schedule this cycle) being bubbled up per
// node, until exactly one workload remains at the root.
func runTournament(cohort *schdcache.CohortSnapshot, ec entryComparer, cqToEntries map[*schdcache.ClusterQueueSnapshot][]*entry) *entry {
	candidates := make([]*entry, 0, cohort.ChildCount())

	// Run algorithm recursively for each of the child Cohorts.
	for _, childCohort := range cohort.ChildCohorts() {
		// The tournament returns 0 nodes, when the child
		// Cohort has no workloads left to be scheduled this
		// cycle.
		if candidate := runTournament(childCohort, ec, cqToEntries); candidate != nil {
			candidates = append(candidates, candidate)
		}
	}

	// Collect the first entry of each CQ. Once all of a CQ's entries
	// were returned during previous calls to pop, the CQ is no longer
	// in the cqToEntries map.
	for _, childCq := range cohort.ChildCQs() {
		if cqEntries, ok := cqToEntries[childCq]; ok {
			candidates = append(candidates, cqEntries[0])
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	// Compare DRS values for each workload, for each of the
	// children of the current Cohort.
	best := candidates[0]
	for _, current := range candidates[1:] {
		if ec.less(current, best, cohort.GetName()) {
			best = current
		}
	}
	return best
}

type drsKey struct {
	parentCohort kueue.CohortReference
	workloadKey  workload.Reference
}

type entryComparer struct {
	log       logr.Logger
	drsValues map[drsKey]schdcache.DRS
	// requestedFRs stores the FlavorResources each workload's
	// assignment uses. Bounded by the number of ClusterQueues
	// (one head-of-line workload per CQ), not total workloads.
	requestedFRs     map[workload.Reference]resources.FlavorResourceQuantities
	workloadOrdering workload.Ordering
}

func (e *entryComparer) less(a, b *entry, parentCohort kueue.CohortReference) bool {
	aDrs := e.drsValues[drsKey{parentCohort: parentCohort, workloadKey: workload.Key(a.Obj)}]
	bDrs := e.drsValues[drsKey{parentCohort: parentCohort, workloadKey: workload.Key(b.Obj)}]

	if features.Enabled(features.FairSharingPrioritizeNonBorrowing) {
		// 1: Nominal first — prefer workloads whose subtree at this
		// tournament level is not borrowing from the parent cohort on
		// the workload's requested flavors. A subtree borrowing on an
		// unrelated flavor does not penalize workloads for flavors
		// where the subtree has ample quota. The recursive tournament
		// applies this check at every hierarchy level, covering the
		// entire CQ-to-root path.
		aBorrowing := aDrs.IsBorrowingOn(e.requestedFRs[workload.Key(a.Obj)])
		bBorrowing := bDrs.IsBorrowingOn(e.requestedFRs[workload.Key(b.Obj)])
		if aBorrowing != bBorrowing {
			return !aBorrowing
		}
	}

	// 2: DRF
	if cmp := schdcache.CompareDRS(aDrs, bDrs); cmp != 0 {
		return cmp == -1
	}

	// 3: Effective priority
	if features.Enabled(features.PrioritySortingWithinCohort) {
		p1 := priority.EffectivePriority(e.log, a.Obj)
		p2 := priority.EffectivePriority(e.log, b.Obj)
		if p1 != p2 {
			return p1 > p2
		}
	}

	// 4: FIFO
	aComparisonTimestamp := e.workloadOrdering.GetQueueOrderTimestamp(a.Obj)
	bComparisonTimestamp := e.workloadOrdering.GetQueueOrderTimestamp(b.Obj)
	return aComparisonTimestamp.Before(bComparisonTimestamp)
}

// computeDRS calculates DominantResourceShare (DRS) for each node
// after admission of workload, for all nodes on path from CQ to
// root-1.  During the tournament, these values are used to compare
// all children the parentCohort, to select the child with the lowest
// DRS after admission of its nominated workload.
func (e *entryComparer) computeDRS(rootCohort *schdcache.CohortSnapshot, cqToEntries map[*schdcache.ClusterQueueSnapshot][]*entry) {
	e.drsValues = make(map[drsKey]schdcache.DRS)
	if features.Enabled(features.FairSharingPrioritizeNonBorrowing) {
		e.requestedFRs = make(map[workload.Reference]resources.FlavorResourceQuantities)
	}
	for _, cq := range rootCohort.SubtreeClusterQueues() {
		cqEntries, ok := cqToEntries[cq]
		if !ok {
			continue
		}
		// Only the first entry of each CQ takes part in the tournament.
		entry := cqEntries[0]
		log := e.log.WithValues(
			"workload", klog.KObj(entry.Obj),
			"clusterQueue", klog.KRef("", string(cq.Name)),
		)
		usage := entry.assignmentUsage(log)
		// We add workload's usage to CQ, so that all
		// subsequent DRS include the admission of workload.
		revert := cq.SimulateUsageAddition(usage)

		wlKey := workload.Key(entry.Obj)
		if e.requestedFRs != nil {
			e.requestedFRs[wlKey] = usage.Quota
		}

		// calculate DRS, with workload, for CQ.
		dominantResourceShare := cq.DominantResourceShare()

		// calculate DRS, with workload, for all Cohorts on
		// path to root.
		for ancestor := range cq.PathParentToRoot() {
			e.drsValues[drsKey{parentCohort: ancestor.GetName(), workloadKey: wlKey}] = dominantResourceShare
			dominantResourceShare = ancestor.DominantResourceShare()
		}

		revert()
	}
}

type drsLogEntry struct {
	ParentCohort string `json:"parentCohort"`
	Workload     string `json:"workload"`
	DRS          string `json:"drs"`
}

func (e *entryComparer) logDrsValuesWhenVerbose(log logr.Logger) {
	if logV := log.V(4); logV.Enabled() {
		entries := make([]drsLogEntry, 0, len(e.drsValues))
		for k, v := range e.drsValues {
			drs := v.PreciseWeightedShareSerialized()
			entries = append(entries, drsLogEntry{
				ParentCohort: string(k.parentCohort),
				Workload:     string(k.workloadKey),
				DRS:          drs,
			})
		}
		slices.SortFunc(entries, func(a, b drsLogEntry) int {
			if c := strings.Compare(a.ParentCohort, b.ParentCohort); c != 0 {
				return c
			}
			return strings.Compare(a.Workload, b.Workload)
		})
		logV.Info(
			"DominantResourceShare values used during tournament",
			"drsValues", entries,
		)
	}
}
