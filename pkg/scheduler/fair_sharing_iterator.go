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
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/workload"
)

// fairSharingIterator orders candidates in a "fair" manner for
// consideration by scheduling when FairSharing is enabled. See
// runTournament for description of algorithm.
type fairSharingIterator struct {
	// cqToEntry tracks ClusterQueues which still have workloads
	// to schedule, and the corresponding workload entry.
	cqToEntry     map[*cache.ClusterQueueSnapshot]*entry
	entryComparer entryComparer
	log           logr.Logger
}

func makeFairSharingIterator(ctx context.Context, entries []entry, workloadOrdering workload.Ordering) *fairSharingIterator {
	f := fairSharingIterator{
		cqToEntry: make(map[*cache.ClusterQueueSnapshot]*entry, len(entries)),
		entryComparer: entryComparer{
			workloadOrdering: workloadOrdering,
		},
		log: ctrl.LoggerFrom(ctx),
	}
	for i := range entries {
		f.cqToEntry[entries[i].clusterQueueSnapshot] = &entries[i]
	}
	return &f
}

func (f *fairSharingIterator) hasNext() bool {
	return len(f.cqToEntry) > 0
}

func (f *fairSharingIterator) pop() *entry {
	cq := f.getCq()

	// CQ has no Cohort. We simply return its workload.
	if !cq.HasParent() {
		entry := f.cqToEntry[cq]
		f.log.V(3).Info("Returning workload from ClusterQueue without Cohort",
			"clusterQueue", klog.KRef("", string(cq.GetName())),
			"workload", klog.KObj(entry.Obj))
		delete(f.cqToEntry, cq)
		return entry
	}

	// CQ is part of a Cohort. We run a tournament, to select the
	// most fair workload at each level.
	root := cq.Parent().Root()
	log := f.log.WithValues("rootCohort", klog.KRef("", string(root.GetName())))

	log.V(5).Info("Computing DominantResourceShare for tournament")
	f.entryComparer.computeDRS(root, f.cqToEntry)

	log.V(3).Info("Running tournament to decide next workload to consider in scheduling cycle")
	entry := runTournament(root, f.entryComparer, f.cqToEntry)

	log = log.WithValues(
		"cohort", klog.KRef("", string(entry.clusterQueueSnapshot.Parent().GetName())),
		"clusterQueue", klog.KRef("", string(entry.clusterQueueSnapshot.GetName())),
		"winningWorkload", klog.KObj(entry.Obj))

	log.V(3).Info("Determined tournament winner")
	f.entryComparer.logDrsValuesWhenVerbose(log)

	delete(f.cqToEntry, entry.clusterQueueSnapshot)
	return entry
}

// getCq returns a CQ with a workload pending scheduling. This
// function is non-deterministic. We don't have any guarantees on
// scheduling order of workloads in different Cohort. Workload
// consideration is nearly deterministic within Cohort (only when DRS,
// Priority, and time are equal it is non-deterministic).
func (f *fairSharingIterator) getCq() *cache.ClusterQueueSnapshot {
	for cq := range f.cqToEntry {
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
func runTournament(cohort *cache.CohortSnapshot, ec entryComparer, cqToEntry map[*cache.ClusterQueueSnapshot]*entry) *entry {
	candidates := make([]*entry, 0, cohort.ChildCount())

	// Run algorithm recursively for each of the child Cohorts.
	for _, childCohort := range cohort.ChildCohorts() {
		// The tournament returns 0 nodes, when the child
		// Cohort has no workloads left to be scheduled this
		// cycle.
		if candidate := runTournament(childCohort, ec, cqToEntry); candidate != nil {
			candidates = append(candidates, candidate)
		}
	}

	// Collect entries from CQ. If an entry was returned during a
	// previous call to pop, it will not be in the cqToEntry map.
	for _, childCq := range cohort.ChildCQs() {
		if candidate, ok := cqToEntry[childCq]; ok {
			candidates = append(candidates, candidate)
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
	workloadKey  string
}

type entryComparer struct {
	drsValues        map[drsKey]int
	workloadOrdering workload.Ordering
}

func (e *entryComparer) less(a, b *entry, parentCohort kueue.CohortReference) bool {
	aDrs := e.drsValues[drsKey{parentCohort: parentCohort, workloadKey: workload.Key(a.Obj)}]
	bDrs := e.drsValues[drsKey{parentCohort: parentCohort, workloadKey: workload.Key(b.Obj)}]
	// 1: DRF
	if aDrs != bDrs {
		return aDrs < bDrs
	}

	// 2: Priority
	if features.Enabled(features.PrioritySortingWithinCohort) {
		p1 := priority.Priority(a.Obj)
		p2 := priority.Priority(b.Obj)
		if p1 != p2 {
			return p1 > p2
		}
	}

	// 3: FIFO
	aComparisonTimestamp := e.workloadOrdering.GetQueueOrderTimestamp(a.Obj)
	bComparisonTimestamp := e.workloadOrdering.GetQueueOrderTimestamp(b.Obj)
	return aComparisonTimestamp.Before(bComparisonTimestamp)
}

// computeDRS calculates DominantResourceShare (DRS) for each node
// after admission of workload, for all nodes on path from CQ to
// root-1.  During the tournament, these values are used to compare
// all children the parentCohort, to select the child with the lowest
// DRS after admission of its nominated workload.
func (e *entryComparer) computeDRS(rootCohort *cache.CohortSnapshot, cqToEntry map[*cache.ClusterQueueSnapshot]*entry) {
	e.drsValues = make(map[drsKey]int)
	for _, cq := range rootCohort.SubtreeClusterQueues() {
		entry, ok := cqToEntry[cq]
		if !ok {
			continue
		}
		// We add workload's usage to CQ, so that all
		// subsequent DRS include the admission of workload.
		revert := cq.SimulateUsageAddition(entry.assignmentUsage())

		// calculate DRS, with workload, for CQ.
		dominantResourceShare := cq.DominantResourceShare()

		// calculate DRS, with workload, for all Cohorts on
		// path to root.
		for ancestor := range cq.PathParentToRoot() {
			e.drsValues[drsKey{parentCohort: ancestor.GetName(), workloadKey: workload.Key(entry.Obj)}] = dominantResourceShare
			dominantResourceShare = ancestor.DominantResourceShare()
		}

		revert()
	}
}

func (e *entryComparer) logDrsValuesWhenVerbose(log logr.Logger) {
	if logV := log.V(5); logV.Enabled() {
		serializableDrs := make([]string, 0, len(e.drsValues))
		for k, v := range e.drsValues {
			serializableDrs = append(serializableDrs, fmt.Sprintf("{parentCohort: %s, workload %s, drs: %d}", k.parentCohort, k.workloadKey, v))
		}
		logV.Info("DominantResourceShare values used during tournament", "drsValues", serializableDrs)
	}
}
