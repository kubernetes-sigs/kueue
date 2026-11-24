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

package preemption

import (
	"context"
	"fmt"
	"iter"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/policy"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
)

// shrinkable builds a victim of the "standalone" ClusterQueue with count Pods
// that partial preemption may shrink to floor.
func shrinkable(name string, count, floor int32) *kueue.Workload {
	return partialPreemptionVictim(name, 0, count, floor, nil)
}

// shrinking builds a shrinkable victim that an earlier partial preemption
// already asked to shrink to target.
func shrinking(name string, count, floor, target int32) *kueue.Workload {
	return partialPreemptionVictim(name, 0, count, floor, &target)
}

// fixed builds a victim of the "standalone" ClusterQueue with count Pods that
// did not opt in to partial preemption. Its floor would let it shrink to no
// Pods, so only the missing opt-in keeps it whole.
func fixed(name string, count int32) *kueue.Workload {
	wl := shrinkable(name, count, 0)
	delete(wl.Annotations, constants.PartialPreemptionAnnotation)
	return wl
}

// beingEvicted marks the victim as already being evicted.
func beingEvicted(wl *kueue.Workload) *kueue.Workload {
	workloadevict.SetEvictedCondition(wl, time.Now(), kueue.WorkloadEvictedByPreemption, "already evicted")
	return wl
}

// planTarget is an expected preemption target: an eviction when retained is nil,
// a shrink to retained Pods otherwise.
type planTarget struct {
	name     string
	retained *int32
}

func evict(name string) planTarget {
	return planTarget{name: name}
}

func shrink(name string, retained int32) planTarget {
	return planTarget{name: name, retained: &retained}
}

var planTargetCmpOpts = cmp.Options{
	cmp.AllowUnexported(planTarget{}),
	cmpopts.EquateEmpty(),
}

// planFixture is a Preemptor with a snapshot of the given ClusterQueues and
// admitted workloads, for an incoming workload that needs preemption in the
// first of the ClusterQueues.
type planFixture struct {
	ctx       context.Context
	preemptor *Preemptor
	pCtx      *preemptionCtx
	// pristine is a second snapshot of the same state that no search touches.
	pristine *schdcache.Snapshot
}

func newPlanFixture(t *testing.T, fairSharing *config.FairSharing, cqs []*kueue.ClusterQueue, admitted []*kueue.Workload, incoming *kueue.Workload) planFixture {
	t.Helper()
	ctx, log := utiltesting.ContextWithLog(t)
	items := make([]kueue.Workload, len(admitted))
	for i := range admitted {
		items[i] = *admitted[i]
	}
	cl := utiltesting.NewClientBuilder().
		WithLists(&kueue.WorkloadList{Items: items}).
		Build()
	cqCache := schdcache.New(cl)
	cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
	for _, cq := range cqs {
		if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("Couldn't add ClusterQueue %q to cache: %v", cq.Name, err)
		}
	}
	preemptor := New(cl, workload.Ordering{}, &utiltesting.EventRecorder{}, fairSharing, false,
		clocktesting.NewFakeClock(time.Now()), nil, preemptexpectations.New(), nil)
	snapshot, err := cqCache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	pristine, err := cqCache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	wlInfo := workload.NewInfo(log, incoming)
	wlInfo.ClusterQueue = kueue.ClusterQueueReference(cqs[0].Name)
	pCtx := preemptor.buildContext(ctx, *wlInfo, singlePodSetAssignment(flavorassigner.ResourceAssignment{
		corev1.ResourceCPU: &flavorassigner.FlavorAssignment{Name: "default", Mode: flavorassigner.Preempt},
	}), snapshot)
	return planFixture{ctx: ctx, preemptor: preemptor, pCtx: pCtx, pristine: pristine}
}

// targets returns the targets that the search finds with the strategies. It
// checks that a partial target carries the victim as it is after the shrink,
// and that the search leaves the snapshot as it found it.
func (f planFixture) targets(t *testing.T, strategies iter.Seq[PreemptionStrategy]) []planTarget {
	t.Helper()
	targets := f.preemptor.getTargets(f.ctx, strategies)
	got := make([]planTarget, len(targets))
	for i, target := range targets {
		got[i].name = target.WorkloadInfo.Obj.Name
		if pp := target.PartialPreemption; pp != nil {
			got[i].retained = &pp.RetainedCount
			if count := podSetRequestCount(pp.ReducedInfo, pp.PodSet); count != pp.RetainedCount {
				t.Errorf("target %q: ReducedInfo keeps %d Pods, want %d", got[i].name, count, pp.RetainedCount)
			}
		}
	}
	if diff := cmp.Diff(f.pristine, f.pCtx.snapshot, snapCmpOpts); diff != "" {
		t.Errorf("The search changed the snapshot (-before,+after):\n%s", diff)
	}
	return got
}

// scriptedCandidates offers the given workloads in order, the way the strategies
// do: a candidate is removed from the snapshot before it is yielded, and the
// snapshot decides whether a later candidate is offered. A candidate in
// onlyWith is skipped unless the workload it names is in the snapshot, shrunk
// or not. It counts the candidates it yields.
func scriptedCandidates(pCtx *preemptionCtx, candidates []*kueue.Workload, reasons, onlyWith map[string]string, yielded *int) iter.Seq[*Target] {
	infos := make([]*workload.Info, len(candidates))
	keys := make(map[string]workload.Reference, len(candidates))
	for i, wl := range candidates {
		keys[wl.Name] = workload.Key(wl)
		infos[i] = pCtx.preemptorCQ.Workloads[keys[wl.Name]]
	}
	return func(yield func(*Target) bool) {
		yield = policy.YieldFromSnapshot(pCtx.snapshot, yield)
		for _, info := range infos {
			if other, found := onlyWith[info.Obj.Name]; found && pCtx.preemptorCQ.Workloads[keys[other]] == nil {
				continue
			}
			reason, found := reasons[info.Obj.Name]
			if !found {
				reason = kueue.InClusterQueueReason
			}
			*yielded++
			if !yield(&Target{WorkloadInfo: info, Reason: reason, WorkloadCq: pCtx.preemptorCQ}) {
				return
			}
		}
	}
}

// TestPartialPlan checks the search with a strategy that offers its candidates
// in the order given by the test case.
func TestPartialPlan(t *testing.T) {
	reject := func([]*Target) (bool, string) {
		return false, "rejected by the test"
	}
	rejectShrinks := func(targets []*Target) (bool, string) {
		for _, target := range targets {
			if target.PartialPreemption != nil {
				return false, "a shrink rejected by the test"
			}
		}
		return true, ""
	}
	cases := map[string]struct {
		partialPreemption bool
		quota             string
		// candidates are admitted and offered to the search in this order.
		candidates []*kueue.Workload
		// others are admitted but never offered.
		others []*kueue.Workload
		// reasons overrides the preemption reason of a candidate.
		reasons map[string]string
		// onlyWith makes the strategy offer a candidate only while the workload
		// it names is in the snapshot.
		onlyWith    map[string]string
		incomingCPU string
		verify      func([]*Target) (bool, string)

		wantTargets []planTarget
		// wantYielded is the number of candidates the search asks for, in the
		// shrinking walk and in the eviction walk together.
		wantYielded int
	}{
		"gate off: the walk stops at the first fit and unneeded victims are added back": {
			quota:       "7",
			candidates:  []*kueue.Workload{fixed("a", 2), fixed("b", 4), fixed("c", 1)},
			incomingCPU: "4",
			wantTargets: []planTarget{evict("b")},
			wantYielded: 2,
		},
		"gate off: a victim that opted in is evicted": {
			quota:       "6",
			candidates:  []*kueue.Workload{shrinkable("v", 5, 1)},
			incomingCPU: "4",
			wantTargets: []planTarget{evict("v")},
			wantYielded: 1,
		},
		"gate off: an in-flight shrink is ignored": {
			quota:       "6",
			candidates:  []*kueue.Workload{shrinking("v", 5, 1, 3)},
			incomingCPU: "3",
			wantTargets: []planTarget{evict("v")},
			wantYielded: 1,
		},
		"nobody opted in: same targets as with the gate off, after a shrinking walk over every candidate": {
			partialPreemption: true,
			quota:             "7",
			candidates:        []*kueue.Workload{fixed("a", 2), fixed("b", 4), fixed("c", 1)},
			incomingCPU:       "4",
			wantTargets:       []planTarget{evict("b")},
			wantYielded:       5,
		},
		"a victim shrinks only as far as the workload needs": {
			partialPreemption: true,
			quota:             "6",
			candidates:        []*kueue.Workload{shrinkable("v", 5, 1)},
			incomingCPU:       "4",
			wantTargets:       []planTarget{shrink("v", 2)},
			wantYielded:       1,
		},
		"a later candidate shrinks instead of an earlier one being evicted": {
			partialPreemption: true,
			quota:             "10",
			candidates:        []*kueue.Workload{fixed("a", 4), shrinkable("b", 6, 2)},
			incomingCPU:       "4",
			wantTargets:       []planTarget{shrink("b", 2)},
			wantYielded:       2,
		},
		"gate off: a candidate that is only offered while an earlier one is not evicted is not reached": {
			quota:       "10",
			candidates:  []*kueue.Workload{fixed("a", 4), shrinkable("b", 6, 2)},
			onlyWith:    map[string]string{"b": "a"},
			incomingCPU: "4",
			wantTargets: []planTarget{evict("a")},
			wantYielded: 1,
		},
		"a candidate that is only offered while an earlier one is not evicted shrinks": {
			partialPreemption: true,
			quota:             "10",
			candidates:        []*kueue.Workload{fixed("a", 4), shrinkable("b", 6, 2)},
			onlyWith:          map[string]string{"b": "a"},
			incomingCPU:       "4",
			wantTargets:       []planTarget{shrink("b", 2)},
			wantYielded:       2,
		},
		"a victim that is not offered once an earlier one is evicted is not evicted": {
			partialPreemption: true,
			quota:             "12",
			candidates:        []*kueue.Workload{shrinkable("a", 4, 3), fixed("b", 4), fixed("c", 4)},
			onlyWith:          map[string]string{"b": "a"},
			incomingCPU:       "8",
			wantTargets:       []planTarget{evict("a"), evict("c")},
			wantYielded:       5,
		},
		"gate off: a victim that the workload does not need is added back": {
			quota:       "16",
			candidates:  []*kueue.Workload{shrinkable("a", 6, 3), shrinkable("b", 8, 6)},
			incomingCPU: "9",
			wantTargets: []planTarget{evict("b")},
			wantYielded: 2,
		},
		"a victim that is added back without partial preemption is not evicted": {
			partialPreemption: true,
			quota:             "16",
			candidates:        []*kueue.Workload{shrinkable("a", 6, 3), shrinkable("b", 8, 6)},
			incomingCPU:       "9",
			wantTargets:       []planTarget{evict("b")},
			wantYielded:       4,
		},
		"an eviction is given back before a shrink": {
			partialPreemption: true,
			quota:             "10",
			candidates:        []*kueue.Workload{fixed("a", 1), fixed("b", 5), shrinkable("c", 3, 1)},
			incomingCPU:       "7",
			wantTargets:       []planTarget{evict("b"), shrink("c", 2)},
			wantYielded:       5,
		},
		"an eviction is given back before a shrink that is already in progress": {
			partialPreemption: true,
			quota:             "10",
			candidates:        []*kueue.Workload{fixed("a", 1), fixed("b", 6), shrinking("c", 3, 1, 2)},
			incomingCPU:       "7",
			wantTargets:       []planTarget{evict("b"), shrink("c", 2)},
			wantYielded:       5,
		},
		"evictions come on top of the shrinks, and an evicted victim that can shrink gets its floor back": {
			partialPreemption: true,
			quota:             "10",
			candidates:        []*kueue.Workload{shrinkable("a", 5, 1), fixed("b", 5)},
			incomingCPU:       "8",
			wantTargets:       []planTarget{shrink("a", 2), evict("b")},
			wantYielded:       4,
		},
		// Shrinking l1 and h1 to their floors is not enough. Evicting l1, the first
		// target of the search without partial preemption, is: l2 is left alone.
		"victims are evicted one by one, in the order of the walk": {
			partialPreemption: true,
			quota:             "11",
			candidates:        []*kueue.Workload{shrinkable("l1", 4, 2), fixed("l2", 3), shrinkable("h1", 4, 2)},
			incomingCPU:       "5",
			wantTargets:       []planTarget{evict("l1"), shrink("h1", 3)},
			wantYielded:       5,
		},
		// Without partial preemption a, b and c are removed, and a is added back.
		// With s shrunk, evicting b, the first of the two in the walk, is enough.
		"victims are evicted in the order of the walk, also when an earlier one is added back": {
			partialPreemption: true,
			quota:             "20",
			candidates:        []*kueue.Workload{fixed("a", 1), fixed("b", 5), fixed("c", 5), shrinkable("s", 9, 5)},
			incomingCPU:       "8",
			wantTargets:       []planTarget{evict("b"), shrink("s", 6)},
			wantYielded:       7,
		},
		"a shrunk victim that is not needed after an eviction is left alone": {
			partialPreemption: true,
			quota:             "10",
			candidates:        []*kueue.Workload{shrinkable("a", 4, 3), fixed("b", 6)},
			incomingCPU:       "6",
			wantTargets:       []planTarget{evict("b")},
			wantYielded:       4,
		},
		"a shrink that is already in progress counts as done": {
			partialPreemption: true,
			quota:             "6",
			candidates:        []*kueue.Workload{shrinking("v", 5, 1, 3)},
			incomingCPU:       "3",
			wantTargets:       []planTarget{shrink("v", 3)},
			wantYielded:       1,
		},
		"every in-flight shrink is used before any victim shrinks further": {
			partialPreemption: true,
			quota:             "10",
			candidates:        []*kueue.Workload{shrinking("a", 5, 1, 3), shrinking("b", 5, 1, 3)},
			incomingCPU:       "4",
			wantTargets:       []planTarget{shrink("a", 3), shrink("b", 3)},
			wantYielded:       2,
		},
		"an in-flight victim shrinks further when its in-flight target is not enough": {
			partialPreemption: true,
			quota:             "6",
			candidates:        []*kueue.Workload{shrinking("v", 5, 1, 3)},
			incomingCPU:       "4",
			wantTargets:       []planTarget{shrink("v", 2)},
			wantYielded:       1,
		},
		"an in-flight victim never gets more than its in-flight target": {
			partialPreemption: true,
			quota:             "6",
			candidates:        []*kueue.Workload{shrinking("v", 5, 1, 3)},
			incomingCPU:       "2",
			wantTargets:       []planTarget{shrink("v", 3)},
			wantYielded:       1,
		},
		"an in-flight shrink that the workload does not need is left out": {
			partialPreemption: true,
			quota:             "8",
			candidates:        []*kueue.Workload{shrinking("a", 3, 1, 2), shrinking("b", 5, 1, 2)},
			incomingCPU:       "3",
			wantTargets:       []planTarget{shrink("b", 2)},
			wantYielded:       2,
		},
		"an eviction that is already in progress counts as done": {
			partialPreemption: true,
			quota:             "10",
			candidates:        []*kueue.Workload{beingEvicted(fixed("e", 4)), shrinkable("a", 6, 2)},
			incomingCPU:       "4",
			wantTargets:       []planTarget{evict("e")},
			wantYielded:       2,
		},
		"a victim that is being evicted does not shrink": {
			partialPreemption: true,
			quota:             "6",
			candidates:        []*kueue.Workload{beingEvicted(shrinkable("v", 5, 1))},
			incomingCPU:       "4",
			wantTargets:       []planTarget{evict("v")},
			wantYielded:       2,
		},
		"a victim selected by a PreemptionConfig rule is evicted": {
			partialPreemption: true,
			quota:             "6",
			candidates:        []*kueue.Workload{shrinkable("v", 5, 1)},
			reasons:           map[string]string{"v": kueue.ConfigurablePreemptionReason},
			incomingCPU:       "4",
			wantTargets:       []planTarget{evict("v")},
			wantYielded:       2,
		},
		"no targets when the workload does not fit even without any candidate": {
			partialPreemption: true,
			quota:             "10",
			candidates:        []*kueue.Workload{shrinkable("train", 3, 2)},
			others:            []*kueue.Workload{fixed("serve", 6)},
			incomingCPU:       "6",
			wantYielded:       2,
		},
		"the eviction result is used when the result with a shrink is rejected": {
			partialPreemption: true,
			quota:             "6",
			candidates:        []*kueue.Workload{shrinkable("v", 5, 1)},
			incomingCPU:       "4",
			verify:            rejectShrinks,
			wantTargets:       []planTarget{evict("v")},
			wantYielded:       2,
		},
		"the eviction result is used when the result with a shrink and an eviction is rejected": {
			partialPreemption: true,
			quota:             "10",
			candidates:        []*kueue.Workload{shrinkable("a", 5, 1), fixed("b", 5)},
			incomingCPU:       "8",
			verify:            rejectShrinks,
			wantTargets:       []planTarget{evict("a"), evict("b")},
			wantYielded:       4,
		},
		"no targets when the eviction result is rejected too": {
			partialPreemption: true,
			quota:             "6",
			candidates:        []*kueue.Workload{shrinkable("v", 5, 1)},
			incomingCPU:       "4",
			verify:            reject,
			wantYielded:       2,
		},
		"gate off: no targets when the eviction result is rejected": {
			quota:       "6",
			candidates:  []*kueue.Workload{fixed("v", 5)},
			incomingCPU: "4",
			verify:      reject,
			wantYielded: 1,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.PartialPreemption, tc.partialPreemption)
			fixture := newPlanFixture(t, nil, []*kueue.ClusterQueue{partialPreemptionClusterQueue(tc.quota)},
				append(tc.candidates, tc.others...),
				utiltestingapi.MakeWorkload("in", "").Request(corev1.ResourceCPU, tc.incomingCPU).Obj())
			yielded := 0
			strategy := newPreemptionStrategy(
				scriptedCandidates(fixture.pCtx, tc.candidates, tc.reasons, tc.onlyWith, &yielded),
				false,
				fixture.pCtx,
				withVerify(tc.verify),
			)

			got := fixture.targets(t, func(yield func(PreemptionStrategy) bool) { yield(strategy) })

			if diff := cmp.Diff(tc.wantTargets, got, planTargetCmpOpts); diff != "" {
				t.Errorf("Unexpected targets (-want,+got):\n%s", diff)
			}
			if yielded != tc.wantYielded {
				t.Errorf("The search asked for %d candidates, want %d", yielded, tc.wantYielded)
			}
		})
	}
}

// TestPartialPreemptionPriorityOrder checks, through the classical strategy and the
// fair sharing strategy, the order in which the PartialPreemptionPriorityOrder
// feature gate takes from the victims: all of a lower priority before any of a
// higher one, and within one priority the victims shrink before any is evicted.
func TestPartialPreemptionPriorityOrder(t *testing.T) {
	victim := func(name string, priority, count, floor int32) *kueue.Workload {
		return partialPreemptionVictim(name, priority, count, floor, nil)
	}
	cases := map[string]struct {
		priorityOrder bool
		quota         string
		victims       []*kueue.Workload
		incomingCPU   string
		want          []planTarget
	}{
		"gate off: both victims shrink, the one of the higher priority too": {
			quota:       "20",
			victims:     []*kueue.Workload{victim("low", 0, 10, 5), victim("high", 10, 10, 5)},
			incomingCPU: "8",
			want:        []planTarget{shrink("low", 5), shrink("high", 7)},
		},
		"the victim of the lower priority is evicted, and the other is left alone": {
			priorityOrder: true,
			quota:         "20",
			victims:       []*kueue.Workload{victim("low", 0, 10, 5), victim("high", 10, 10, 5)},
			incomingCPU:   "8",
			want:          []planTarget{evict("low")},
		},
		"the victim of the lower priority shrinks when that is enough": {
			priorityOrder: true,
			quota:         "20",
			victims:       []*kueue.Workload{victim("low", 0, 10, 5), victim("high", 10, 10, 5)},
			incomingCPU:   "4",
			want:          []planTarget{shrink("low", 6)},
		},
		// Shrinking all three victims to their floors would free 15 Pods, enough for 12. The
		// two victims of the lower priority free only 10 that way, so one of them is evicted
		// and the other shrinks, and the victim of the higher priority is never touched.
		"the victims of the lower priority give all they can before the one of the higher priority is touched": {
			priorityOrder: true,
			quota:         "30",
			victims:       []*kueue.Workload{victim("low-a", 0, 10, 5), victim("low-b", 0, 10, 5), victim("high", 10, 10, 5)},
			incomingCPU:   "12",
			want:          []planTarget{evict("low-a"), shrink("low-b", 8)},
		},
		"victims of the same priority shrink together, and the fill-back gives the last one the most": {
			priorityOrder: true,
			quota:         "20",
			victims:       []*kueue.Workload{victim("a", 0, 10, 5), victim("b", 0, 10, 5)},
			incomingCPU:   "7",
			want:          []planTarget{shrink("a", 5), shrink("b", 8)},
		},
	}
	strategies := map[string]*config.FairSharing{
		"classical":    nil,
		"fair sharing": {},
	}
	for name, tc := range cases {
		for strategyName, fairSharing := range strategies {
			t.Run(name+"/"+strategyName, func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.PartialPreemption, true)
				features.SetFeatureGateDuringTest(t, features.PartialPreemptionPriorityOrder, tc.priorityOrder)
				incoming := utiltestingapi.MakeWorkload("in", "").UID("wl-in").Priority(20).Request(corev1.ResourceCPU, tc.incomingCPU).Obj()

				targets, _, _ := getPartialPreemptionTargets(t, time.Now().Truncate(time.Second), partialPreemptionClusterQueue(tc.quota), fairSharing, tc.victims, incoming)

				got := make([]planTarget, len(targets))
				for i, target := range targets {
					got[i].name = target.WorkloadInfo.Obj.Name
					if target.PartialPreemption != nil {
						got[i].retained = &target.PartialPreemption.RetainedCount
					}
				}
				if diff := cmp.Diff(tc.want, got, planTargetCmpOpts); diff != "" {
					t.Errorf("Unexpected targets (-want,+got):\n%s", diff)
				}
			})
		}
	}
}

// TestPartialPreemptionMaxReclaimedCount checks, with the classical and the fair
// sharing strategy, that victims with a max reclaimed count give up Pods in
// rounds, so that the victims of one bucket shrink evenly.
func TestPartialPreemptionMaxReclaimedCount(t *testing.T) {
	victim := func(name string, priority, count, floor int32, maxReclaimed string) *kueue.Workload {
		wl := partialPreemptionVictim(name, priority, count, floor, nil)
		if maxReclaimed != "" {
			wl.Annotations[constants.PartialPreemptionMaxReclaimedCountAnnotation] = maxReclaimed
		}
		return wl
	}
	cases := map[string]struct {
		priorityOrder bool
		quota         string
		victims       []*kueue.Workload
		incomingCPU   string
		want          []planTarget
	}{
		// Two rounds free 6 Pods, 2 from each victim, enough for 4. The fill-back
		// gives the whole of the last victim back, so the first two keep the 2 Pods
		// that each of them gave up.
		"victims of one bucket shrink by the same number of Pods": {
			priorityOrder: true,
			quota:         "30",
			victims:       []*kueue.Workload{victim("a", 0, 10, 0, "1"), victim("b", 0, 10, 0, "1"), victim("c", 0, 10, 0, "1")},
			incomingCPU:   "4",
			want:          []planTarget{shrink("a", 8), shrink("b", 8)},
		},
		"a victim without a max reclaimed count is at its floor after the first round": {
			priorityOrder: true,
			quota:         "20",
			victims:       []*kueue.Workload{victim("a", 0, 10, 0, "1"), victim("b", 0, 10, 0, "")},
			incomingCPU:   "12",
			want:          []planTarget{shrink("a", 8), shrink("b", 0)},
		},
		// One round takes 1 Pod from the capped victim and all that the uncapped one
		// may give up, which is enough. The fill-back gives back to the uncapped
		// victim as much as the workload does not need, leaving the capped victim
		// with the Pod of its first round.
		"every victim of the bucket gives up its round, even when one of them can give enough": {
			priorityOrder: true,
			quota:         "20",
			victims:       []*kueue.Workload{victim("a", 0, 10, 0, "1"), victim("b", 0, 10, 0, "")},
			incomingCPU:   "8",
			want:          []planTarget{shrink("a", 9), shrink("b", 3)},
		},
		"a max reclaimed count that is not less than what the victim may give up changes nothing": {
			priorityOrder: true,
			quota:         "20",
			victims:       []*kueue.Workload{victim("a", 0, 10, 5, "5"), victim("b", 0, 10, 5, "20")},
			incomingCPU:   "7",
			want:          []planTarget{shrink("a", 5), shrink("b", 8)},
		},
		// Two rounds take 4 Pods from each victim, the second stopping at the floor.
		// The fill-back then gives back to one victim as much as the workload does
		// not need.
		"the last round stops at the floor": {
			priorityOrder: true,
			quota:         "20",
			victims:       []*kueue.Workload{victim("a", 0, 10, 4, "4"), victim("b", 0, 10, 4, "4")},
			incomingCPU:   "9",
			want:          []planTarget{shrink("a", 4), shrink("b", 7)},
		},
		"a value that is not a positive integer is ignored": {
			priorityOrder: true,
			quota:         "20",
			victims:       []*kueue.Workload{victim("a", 0, 10, 5, "0"), victim("b", 0, 10, 5, "many")},
			incomingCPU:   "7",
			want:          []planTarget{shrink("a", 5), shrink("b", 8)},
		},
		"victims of a higher priority are not touched, and do not share the rounds": {
			priorityOrder: true,
			quota:         "30",
			victims:       []*kueue.Workload{victim("a", 0, 10, 0, "1"), victim("b", 0, 10, 0, "1"), victim("c", 10, 10, 0, "1")},
			incomingCPU:   "5",
			want:          []planTarget{shrink("a", 7), shrink("b", 8)},
		},
		"a victim that the rounds cannot spare is evicted": {
			priorityOrder: true,
			quota:         "20",
			victims:       []*kueue.Workload{victim("a", 0, 10, 5, "1"), victim("b", 0, 10, 5, "1")},
			incomingCPU:   "12",
			want:          []planTarget{evict("a"), shrink("b", 8)},
		},
		"without the priority order the maximum is not used": {
			quota:       "30",
			victims:     []*kueue.Workload{victim("a", 0, 10, 0, "1"), victim("b", 0, 10, 0, "1"), victim("c", 0, 10, 0, "1")},
			incomingCPU: "4",
			want:        []planTarget{shrink("a", 6)},
		},
	}
	strategies := map[string]*config.FairSharing{
		"classical":    nil,
		"fair sharing": {},
	}
	for name, tc := range cases {
		for strategyName, fairSharing := range strategies {
			t.Run(name+"/"+strategyName, func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.PartialPreemption, true)
				features.SetFeatureGateDuringTest(t, features.PartialPreemptionPriorityOrder, tc.priorityOrder)
				incoming := utiltestingapi.MakeWorkload("in", "").UID("wl-in").Priority(20).Request(corev1.ResourceCPU, tc.incomingCPU).Obj()

				targets, _, _ := getPartialPreemptionTargets(t, time.Now().Truncate(time.Second), partialPreemptionClusterQueue(tc.quota), fairSharing, tc.victims, incoming)

				got := make([]planTarget, len(targets))
				for i, target := range targets {
					got[i].name = target.WorkloadInfo.Obj.Name
					if target.PartialPreemption != nil {
						got[i].retained = &target.PartialPreemption.RetainedCount
					}
				}
				if diff := cmp.Diff(tc.want, got, planTargetCmpOpts); diff != "" {
					t.Errorf("Unexpected targets (-want,+got):\n%s", diff)
				}
			})
		}
	}
}

// TestSameBucketFunc checks which candidates the classical strategy puts in the
// same bucket.
func TestSameBucketFunc(t *testing.T) {
	now := time.Now()
	log := utiltesting.NewLogger(t)
	target := func(name string, cq kueue.ClusterQueueReference, priority int32, reason string) *Target {
		wl := utiltestingapi.MakeWorkload(name, "ns").
			UID(types.UID(name)).
			Priority(priority).
			ReserveQuotaAt(utiltestingapi.MakeAdmission(cq).Obj(), now).
			Obj()
		info := workload.NewInfo(log, wl)
		info.ClusterQueue = cq
		return &Target{WorkloadInfo: info, Reason: reason}
	}
	hierarchy := sets.New[kueue.ClusterQueueReference]("hier-1", "hier-2")
	sameBucket := sameBucketFunc(log, false, "preemptor", hierarchy)

	cases := map[string]struct {
		a, b *Target
		want bool
	}{
		"the same ClusterQueue and priority": {
			a:    target("a", "other", 1, kueue.InCohortReclamationReason),
			b:    target("b", "other", 1, kueue.InCohortReclamationReason),
			want: true,
		},
		"two ClusterQueues of the same kind and the same priority": {
			a:    target("a", "other-1", 1, kueue.InCohortReclamationReason),
			b:    target("b", "other-2", 1, kueue.InCohortReclamationReason),
			want: true,
		},
		"different priorities": {
			a:    target("a", "other", 1, kueue.InCohortReclamationReason),
			b:    target("b", "other", 2, kueue.InCohortReclamationReason),
			want: false,
		},
		"two ClusterQueues of the hierarchy, whatever the priority they have in common": {
			a:    target("a", "hier-1", 1, kueue.InCohortReclamationReason),
			b:    target("b", "hier-2", 1, kueue.InCohortReclamationReason),
			want: true,
		},
		"a ClusterQueue of the hierarchy and one that is not, at the same priority": {
			a:    target("a", "hier-1", 1, kueue.InCohortReclamationReason),
			b:    target("b", "other", 1, kueue.InCohortReclamationReason),
			want: false,
		},
		"the preemptor's ClusterQueue and another one, at the same priority": {
			a:    target("a", "other", 1, kueue.InCohortReclamationReason),
			b:    target("b", "preemptor", 1, kueue.InClusterQueueReason),
			want: false,
		},
		"a candidate of a PreemptionConfig rule and one that is not, at the same priority": {
			a:    target("a", "other", 1, kueue.ConfigurablePreemptionReason),
			b:    target("b", "other", 1, kueue.InCohortReclamationReason),
			want: false,
		},
		"two candidates of PreemptionConfig rules at the same priority": {
			a:    target("a", "other", 1, kueue.ConfigurablePreemptionReason),
			b:    target("b", "other", 1, kueue.ConfigurablePreemptionReason),
			want: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := sameBucket(tc.a, tc.b); got != tc.want {
				t.Errorf("sameBucket() = %v, want %v", got, tc.want)
			}
			if got := sameBucket(tc.b, tc.a); got != tc.want {
				t.Errorf("sameBucket() with the arguments swapped = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestFairSameBucketFunc checks which candidates the fair sharing strategy puts in
// the same bucket.
func TestFairSameBucketFunc(t *testing.T) {
	now := time.Now()
	log := utiltesting.NewLogger(t)
	target := func(name string, cq kueue.ClusterQueueReference, priority int32, reason string) *Target {
		wl := utiltestingapi.MakeWorkload(name, "ns").
			UID(types.UID(name)).
			Priority(priority).
			ReserveQuotaAt(utiltestingapi.MakeAdmission(cq).Obj(), now).
			Obj()
		info := workload.NewInfo(log, wl)
		info.ClusterQueue = cq
		return &Target{WorkloadInfo: info, Reason: reason}
	}
	sameBucket := fairSameBucketFunc(log, false, "preemptor")

	cases := map[string]struct {
		a, b *Target
		want bool
	}{
		"the same ClusterQueue and priority": {
			a:    target("a", "other", 1, kueue.InCohortFairSharingReason),
			b:    target("b", "other", 1, kueue.InCohortFairSharingReason),
			want: true,
		},
		"the same ClusterQueue, different priorities": {
			a:    target("a", "other", 1, kueue.InCohortFairSharingReason),
			b:    target("b", "other", 2, kueue.InCohortFairSharingReason),
			want: false,
		},
		"different ClusterQueues, the same priority": {
			a:    target("a", "other-1", 1, kueue.InCohortFairSharingReason),
			b:    target("b", "other-2", 1, kueue.InCohortFairSharingReason),
			want: false,
		},
		"a candidate of a PreemptionConfig rule and one that is not": {
			a:    target("a", "other", 1, kueue.ConfigurablePreemptionReason),
			b:    target("b", "other", 1, kueue.InCohortFairSharingReason),
			want: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := sameBucket(tc.a, tc.b); got != tc.want {
				t.Errorf("sameBucket() = %v, want %v", got, tc.want)
			}
			if got := sameBucket(tc.b, tc.a); got != tc.want {
				t.Errorf("sameBucket() with the arguments swapped = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPartialPreemptionGetTargets(t *testing.T) {
	type wantTarget struct {
		partial                 bool
		preemptionRetainedCount int32
		reducedCount            *int32
	}
	cases := map[string]struct {
		gateDisabled bool
		fairSharing  *config.FairSharing
		quota        string
		victims      []*kueue.Workload
		incoming     *kueue.Workload
		want         map[string]wantTarget
		wantOrder    []string
	}{
		"gate on: reduces the victim to free only what the incoming workload needs": {
			quota:    "6",
			victims:  []*kueue.Workload{partialPreemptionVictim("victim", 0, 5, 1, nil)},
			incoming: utiltestingapi.MakeWorkload("in", "").UID("wl-in").Priority(1).Request(corev1.ResourceCPU, "4").Obj(),
			want:     map[string]wantTarget{"victim": {partial: true, preemptionRetainedCount: 2}},
		},
		"gate off: whole eviction": {
			gateDisabled: true,
			quota:        "6",
			victims:      []*kueue.Workload{partialPreemptionVictim("victim", 0, 5, 1, nil)},
			incoming:     utiltestingapi.MakeWorkload("in", "").UID("wl-in").Priority(1).Request(corev1.ResourceCPU, "4").Obj(),
			want:         map[string]wantTarget{"victim": {partial: false}},
		},
		"no fit: do not return an in-flight partial target": {
			quota:    "6",
			victims:  []*kueue.Workload{partialPreemptionVictim("victim", 0, 5, 1, new(int32(2)))},
			incoming: utiltestingapi.MakeWorkload("no-fit", "").UID("wl-no-fit").Priority(1).Request(corev1.ResourceCPU, "7").Obj(),
			want:     nil,
		},
		"reclaims only enough": {
			quota:    "20",
			victims:  []*kueue.Workload{partialPreemptionVictim("victim", 0, 20, 5, nil)},
			incoming: utiltestingapi.MakeWorkload("incoming", "").UID("incoming").Priority(1).Request(corev1.ResourceCPU, "10").Obj(),
			want:     map[string]wantTarget{"victim": {partial: true, preemptionRetainedCount: 10}},
		},
		"multiple candidates each partially reduced": {
			quota: "10",
			victims: []*kueue.Workload{
				partialPreemptionVictim("v1", 0, 5, 4, nil),
				partialPreemptionVictim("v2", 0, 5, 4, nil),
			},
			incoming: utiltestingapi.MakeWorkload("in", "").UID("wl-in").Priority(1).Request(corev1.ResourceCPU, "2").Obj(),
			want: map[string]wantTarget{
				"v1": {partial: true, preemptionRetainedCount: 4},
				"v2": {partial: true, preemptionRetainedCount: 4},
			},
		},
		"round-two merge preserves priority for a whole-only candidate": {
			quota: "15",
			victims: []*kueue.Workload{
				partialPreemptionVictim("H1", 30, 5, 1, nil),
				partialPreemptionVictim("M1", 20, 5, 1, nil),
				partialPreemptionVictim("L1", 10, 5, 5, nil),
			},
			incoming: utiltestingapi.MakeWorkload("incoming", "").UID("incoming").Priority(40).Request(corev1.ResourceCPU, "12").Obj(),
			want: map[string]wantTarget{
				"H1": {partial: true, preemptionRetainedCount: 2},
				"M1": {partial: true, preemptionRetainedCount: 1},
				"L1": {partial: false},
			},
			wantOrder: []string{"L1", "M1", "H1"},
		},
		"multi-round progresses to full eviction": {
			quota: "10",
			victims: []*kueue.Workload{
				partialPreemptionVictim("victim-a", 0, 5, 1, nil),
				partialPreemptionVictim("victim-b", 0, 5, 5, nil),
			},
			incoming: utiltestingapi.MakeWorkload("incoming", "").UID("incoming").Priority(1).Request(corev1.ResourceCPU, "8").Obj(),
			want: map[string]wantTarget{
				"victim-a": {partial: true, preemptionRetainedCount: 2},
				"victim-b": {partial: false},
			},
		},
		"evicted partial-preemption victim is wholly evicted": {
			quota: "6",
			victims: []*kueue.Workload{func() *kueue.Workload {
				wl := partialPreemptionVictim("victim", 0, 5, 1, nil)
				workloadevict.SetEvictedCondition(wl, time.Now(), kueue.WorkloadEvictedByPreemption, "already evicted")
				return wl
			}()},
			incoming: utiltestingapi.MakeWorkload("in", "").UID("wl-in").Priority(1).Request(corev1.ResourceCPU, "4").Obj(),
			want:     map[string]wantTarget{"victim": {partial: false}},
		},
		"TAS victim is wholly evicted": {
			quota: "6",
			victims: []*kueue.Workload{func() *kueue.Workload {
				wl := partialPreemptionVictim("victim", 0, 5, 1, nil)
				wl.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{Required: new("rack")}
				wl.Status.Admission.PodSetAssignments[0].TopologyAssignment = utiltestingapi.MakeTopologyAssignment([]string{"rack"}).Obj()
				return wl
			}()},
			incoming: utiltestingapi.MakeWorkload("in", "").UID("wl-in").Priority(1).Request(corev1.ResourceCPU, "4").Obj(),
			want:     map[string]wantTarget{"victim": {partial: false}},
		},
		"multiple shrinkable PodSets are wholly evicted": {
			quota: "12",
			victims: []*kueue.Workload{func() *kueue.Workload {
				wl := partialPreemptionVictim("victim", 0, 5, 1, nil)
				second := utiltestingapi.MakePodSet("second", 5).Request(corev1.ResourceCPU, "1").SetPartialPreemptionMinRetainedCount(1).Obj()
				wl.Spec.PodSets = append(wl.Spec.PodSets, *second)
				assignment := utiltestingapi.MakePodSetAssignment("second").Assignment(corev1.ResourceCPU, "default", "5").Count(5).Obj()
				wl.Status.Admission.PodSetAssignments = append(wl.Status.Admission.PodSetAssignments, assignment)
				return wl
			}()},
			incoming: utiltestingapi.MakeWorkload("in", "").UID("wl-in").Priority(1).Request(corev1.ResourceCPU, "4").Obj(),
			want:     map[string]wantTarget{"victim": {partial: false}},
		},
		"fair sharing: evicts the victim whole when shrinking is not enough": {
			fairSharing: &config.FairSharing{},
			quota:       "5",
			victims:     []*kueue.Workload{partialPreemptionVictim("victim", 0, 3, 2, nil), fixed("fixed", 2)},
			incoming:    utiltestingapi.MakeWorkload("in", "").UID("wl-in").Priority(1).Request(corev1.ResourceCPU, "3").Obj(),
			want:        map[string]wantTarget{"victim": {partial: false}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.PartialPreemption, !tc.gateDisabled)
			now := time.Now().Truncate(time.Second)
			cq := partialPreemptionClusterQueue(tc.quota)
			targets, beforeSnapshot, snapshot := getPartialPreemptionTargets(t, now, cq, tc.fairSharing, tc.victims, tc.incoming)

			if tc.want == nil {
				if len(targets) != 0 {
					t.Fatalf("GetTargets() = %+v, want no targets when the preemptor cannot fit", targets)
				}
				if diff := cmp.Diff(beforeSnapshot, snapshot, snapCmpOpts); diff != "" {
					t.Errorf("Snapshot was not restored after the failed strategy attempt (-want,+got):\n%s", diff)
				}
				return
			}
			if len(targets) != len(tc.want) {
				t.Fatalf("GetTargets() returned %d targets, want %d: %+v", len(targets), len(tc.want), targets)
			}
			if tc.wantOrder != nil {
				gotOrder := make([]string, len(targets))
				for i := range targets {
					gotOrder[i] = targets[i].WorkloadInfo.Obj.Name
				}
				if diff := cmp.Diff(tc.wantOrder, gotOrder); diff != "" {
					t.Errorf("GetTargets() order mismatch (-want,+got):\n%s", diff)
				}
			}
			for _, target := range targets {
				victimName := target.WorkloadInfo.Obj.Name
				want, ok := tc.want[victimName]
				if !ok {
					t.Errorf("unexpected target %q", victimName)
					continue
				}
				if !want.partial {
					if target.PartialPreemption != nil {
						t.Errorf("target %q: PartialPreemption = %+v, want whole eviction (nil)", victimName, target.PartialPreemption)
					}
					continue
				}
				if target.PartialPreemption == nil {
					t.Errorf("target %q: PartialPreemption = nil, want partial scale-down to %d", victimName, want.preemptionRetainedCount)
					continue
				}
				if got, wantPS := target.PartialPreemption.PodSet, kueue.PodSetReference("executor"); got != wantPS {
					t.Errorf("target %q: PartialPreemption.PodSet = %q, want %q", victimName, got, wantPS)
				}
				if got := target.PartialPreemption.RetainedCount; got != want.preemptionRetainedCount {
					t.Errorf("target %q: PreemptionRetainedCount = %d, want %d", victimName, got, want.preemptionRetainedCount)
				}
				if want.reducedCount != nil {
					if target.PartialPreemption.ReducedInfo == nil {
						t.Errorf("target %q: ReducedInfo = nil, want count %d", victimName, *want.reducedCount)
					} else if got := target.PartialPreemption.ReducedInfo.TotalRequests[0].Count; got != *want.reducedCount {
						t.Errorf("target %q: ReducedInfo count = %d, want %d", victimName, got, *want.reducedCount)
					}
				}
			}
		})
	}
}

func TestPartialPreemptionIssuePreemptions(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.PartialPreemption, true)

	type victimWant struct {
		partial     bool
		targetCount int32
	}
	cases := map[string]struct {
		quota         string
		victims       []*kueue.Workload
		incoming      *kueue.Workload
		wantPreempted int
		wantFailed    int
		want          map[string]victimWant
	}{
		"partial scale-down keeps the victim running": {
			quota:         "6",
			victims:       []*kueue.Workload{partialPreemptionVictim("victim", 0, 5, 1, nil)},
			incoming:      utiltestingapi.MakeWorkload("in", "").UID("wl-in").Priority(1).Request(corev1.ResourceCPU, "4").Obj(),
			wantPreempted: 1,
			want:          map[string]victimWant{"victim": {partial: true, targetCount: 2}},
		},
		"non-shrinkable victim is wholly evicted": {
			// minCount equals the admitted count, so the victim cannot scale down and is evicted.
			quota:         "6",
			victims:       []*kueue.Workload{partialPreemptionVictim("victim", 0, 5, 5, nil)},
			incoming:      utiltestingapi.MakeWorkload("in", "").UID("wl-in").Priority(1).Request(corev1.ResourceCPU, "4").Obj(),
			wantPreempted: 1,
			want:          map[string]victimWant{"victim": {partial: false}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			now := time.Now().Truncate(time.Second)
			items := make([]kueue.Workload, len(tc.victims))
			for i := range tc.victims {
				items[i] = *tc.victims[i]
			}
			ctx, log := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewClientBuilder().
				WithLists(&kueue.WorkloadList{Items: items}).
				WithStatusSubresource(&kueue.Workload{}).
				WithInterceptorFuncs(interceptor.Funcs{SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration}).
				Build()
			cqCache := schdcache.New(cl)
			cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
			if err := cqCache.AddClusterQueue(ctx, partialPreemptionClusterQueue(tc.quota)); err != nil {
				t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
			}
			recorder := &utiltesting.EventRecorder{}
			preemptor := New(cl, workload.Ordering{}, recorder, nil, false, clocktesting.NewFakeClock(now), nil, preemptexpectations.New(), nil)
			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			wlInfo := workload.NewInfo(log, tc.incoming)
			wlInfo.ClusterQueue = "standalone"
			assignment := singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{Name: "default", Mode: flavorassigner.Preempt},
			})
			strategies := preemptor.GetPreemptionStrategyIterator(ctx, *wlInfo, snapshot, assignment)
			targets := preemptor.GetTargetsWithStrategy(ctx, strategies)

			preempted, failed, err := preemptor.IssuePreemptions(ctx, cqCache, wlInfo, targets, snapshot.ClusterQueue(wlInfo.ClusterQueue))
			if err != nil {
				t.Fatalf("IssuePreemptions failed: %v", err)
			}
			if preempted != tc.wantPreempted || failed != tc.wantFailed {
				t.Errorf("IssuePreemptions = (preempted=%d, failed=%d), want (%d, %d)", preempted, failed, tc.wantPreempted, tc.wantFailed)
			}

			for victimName, want := range tc.want {
				got := &kueue.Workload{}
				if err := cl.Get(ctx, types.NamespacedName{Name: victimName}, got); err != nil {
					t.Fatalf("failed to get victim %q: %v", victimName, err)
				}
				if !want.partial {
					if !workloadevict.IsEvicted(got) {
						t.Errorf("victim %q was not evicted, want whole eviction", victimName)
					}
					continue
				}
				// The scale-down request is persisted on the executor assignment...
				gotCount, ok := admittedTargetCount(got, "executor")
				if !ok || gotCount != want.targetCount {
					t.Errorf("victim %q executor targetCount = (%d, %t), want (%d, true)", victimName, gotCount, ok, want.targetCount)
				}
				// ...and the quota-accounted count is NOT changed here (released only as pods terminate)...
				if c := admittedCount(got, "executor"); c != 5 {
					t.Errorf("victim %q executor admitted count = %d, want 5 (unchanged; released as pods exit)", victimName, c)
				}
				// ...and the victim is NOT evicted (keeps running at reduced size).
				if workloadevict.IsEvicted(got) {
					t.Errorf("victim %q was evicted, want it running (partial preemption must not evict)", victimName)
				}
			}
		})
	}
}

func admittedTargetCount(wl *kueue.Workload, podSet kueue.PodSetReference) (int32, bool) {
	if wl.Status.Admission == nil {
		return 0, false
	}
	for i := range wl.Status.Admission.PodSetAssignments {
		psa := &wl.Status.Admission.PodSetAssignments[i]
		if psa.Name == podSet && psa.PartialPreemption.RetainedCount != nil {
			return *psa.PartialPreemption.RetainedCount, true
		}
	}
	return 0, false
}

func admittedCount(wl *kueue.Workload, podSet kueue.PodSetReference) int32 {
	if wl.Status.Admission == nil {
		return 0
	}
	for i := range wl.Status.Admission.PodSetAssignments {
		psa := &wl.Status.Admission.PodSetAssignments[i]
		if psa.Name == podSet {
			return ptr.Deref(psa.Count, 0)
		}
	}
	return 0
}

func partialPreemptionClusterQueue(cpuQuota string) *kueue.ClusterQueue {
	return utiltestingapi.MakeClusterQueue("standalone").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
			Resource(corev1.ResourceCPU, cpuQuota).Obj()).
		Preemption(kueue.ClusterQueuePreemption{
			WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
		}).
		Obj()
}

// partialPreemptionVictim builds an elastic victim admitted at usedCount replicas with
// the given minimum count. When preemptionRetainedCount is non-nil it seeds the executor
// assignment with an in-flight scale-down request (as a prior preemption round would).
func partialPreemptionVictim(name string, priorityValue, usedCount, minCount int32, preemptionRetainedCount *int32) *kueue.Workload {
	wl := utiltestingapi.MakeWorkload(name, "").
		UID(types.UID(name)).
		Annotation(constants.PartialPreemptionAnnotation, "true").
		Priority(priorityValue).
		PodSets(*utiltestingapi.MakePodSet("executor", int(usedCount)).
			Request(corev1.ResourceCPU, "1").
			SetPartialPreemptionMinRetainedCount(minCount).
			Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("standalone").
			PodSets(utiltestingapi.MakePodSetAssignment("executor").
				Assignment(corev1.ResourceCPU, "default", fmt.Sprintf("%d", usedCount)).
				Count(usedCount).
				Obj()).
			Obj(), time.Now()).
		Obj()
	if preemptionRetainedCount != nil {
		wl.Status.Admission.PodSetAssignments[0].PartialPreemption = &kueue.PodSetPartialPreemptionStatus{RetainedCount: preemptionRetainedCount}
	}
	return wl
}

func getPartialPreemptionTargets(
	t *testing.T,
	now time.Time,
	cq *kueue.ClusterQueue,
	fairSharing *config.FairSharing,
	victims []*kueue.Workload,
	incoming *kueue.Workload,
) (targets []*Target, beforeSnapshot, snapshot *schdcache.Snapshot) {
	t.Helper()
	ctx, log := utiltesting.ContextWithLog(t)
	items := make([]kueue.Workload, len(victims))
	for i := range victims {
		items[i] = *victims[i]
	}
	cl := utiltesting.NewClientBuilder().
		WithLists(&kueue.WorkloadList{Items: items}).
		WithStatusSubresource(&kueue.Workload{}).
		Build()
	cqCache := schdcache.New(cl)
	cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
	if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
	}
	preemptor := New(
		cl,
		workload.Ordering{},
		&utiltesting.EventRecorder{},
		fairSharing,
		false,
		clocktesting.NewFakeClock(now),
		nil,
		preemptexpectations.New(),
		nil,
	)
	beforeSnapshot, err := cqCache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	// make a working copy of the snapshot that preemption can temporarily modify
	snapshot, err = cqCache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	wlInfo := workload.NewInfo(log, incoming)
	wlInfo.ClusterQueue = kueue.ClusterQueueReference(cq.Name)
	strategies := preemptor.GetPreemptionStrategyIterator(ctx, *wlInfo, snapshot, singlePodSetAssignment(flavorassigner.ResourceAssignment{
		corev1.ResourceCPU: &flavorassigner.FlavorAssignment{Name: "default", Mode: flavorassigner.Preempt},
	}))
	targets = preemptor.GetTargetsWithStrategy(ctx, strategies)
	return targets, beforeSnapshot, snapshot
}

// partialPreemptionVictimInCQ is like partialPreemptionVictim, but admits the victim
// into cq instead of the fixed "standalone" ClusterQueue, for scenarios that need more
// than one ClusterQueue (e.g. a cohort with borrowing).
func partialPreemptionVictimInCQ(cq kueue.ClusterQueueReference, name string, priorityValue, usedCount, minCount int32) *kueue.Workload {
	return utiltestingapi.MakeWorkload(name, "").
		UID(types.UID(name)).
		Annotation(constants.PartialPreemptionAnnotation, "true").
		Priority(priorityValue).
		PodSets(*utiltestingapi.MakePodSet("executor", int(usedCount)).
			Request(corev1.ResourceCPU, "1").
			SetPartialPreemptionMinRetainedCount(minCount).
			Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission(cq).
			PodSets(utiltestingapi.MakePodSetAssignment("executor").
				Assignment(corev1.ResourceCPU, "default", fmt.Sprintf("%d", usedCount)).
				Count(usedCount).
				Obj()).
			Obj(), time.Now()).
		Obj()
}

func getPartialPreemptionTargetsForCohort(
	t *testing.T,
	now time.Time,
	cqs []*kueue.ClusterQueue,
	victims []*kueue.Workload,
	incoming *kueue.Workload,
	incomingCQ kueue.ClusterQueueReference,
) []*Target {
	t.Helper()
	ctx, log := utiltesting.ContextWithLog(t)
	items := make([]kueue.Workload, len(victims))
	for i := range victims {
		items[i] = *victims[i]
	}
	cl := utiltesting.NewClientBuilder().
		WithLists(&kueue.WorkloadList{Items: items}).
		WithStatusSubresource(&kueue.Workload{}).
		Build()
	cqCache := schdcache.New(cl)
	cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
	for _, cq := range cqs {
		if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("Couldn't add ClusterQueue %q to cache: %v", cq.Name, err)
		}
	}
	preemptor := New(
		cl,
		workload.Ordering{},
		&utiltesting.EventRecorder{},
		nil,
		false,
		clocktesting.NewFakeClock(now),
		nil,
		preemptexpectations.New(),
		nil,
	)
	snapshot, err := cqCache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	wlInfo := workload.NewInfo(log, incoming)
	wlInfo.ClusterQueue = incomingCQ
	strategies := preemptor.GetPreemptionStrategyIterator(ctx, *wlInfo, snapshot, singlePodSetAssignment(flavorassigner.ResourceAssignment{
		corev1.ResourceCPU: &flavorassigner.FlavorAssignment{Name: "default", Mode: flavorassigner.Preempt},
	}))
	return preemptor.GetTargetsWithStrategy(ctx, strategies)
}

// TestPartialPreemptionFillBackCohort1Regression covers cohort "coh1": P nominal
// 8, A nominal 2, B nominal 0 (10 total). A is used at 4 (floor 2), B at 4
// (floor 3). The 8 used leave 2 free, so a request of 7 needs 5 more. Shrinking
// both victims to their floors frees only 2+1=3, so the first candidate is
// evicted on top of that, and the other one stays at its floor.
func TestPartialPreemptionFillBackCohort1Regression(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.PartialPreemption, true)

	cqs := []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("P").Cohort("coh1").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "8").Obj()).
			Preemption(kueue.ClusterQueuePreemption{ReclaimWithinCohort: kueue.PreemptionPolicyAny}).
			Obj(),
		utiltestingapi.MakeClusterQueue("A").Cohort("coh1").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "2").Obj()).
			Obj(),
		utiltestingapi.MakeClusterQueue("B").Cohort("coh1").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "0").Obj()).
			Obj(),
	}
	victims := []*kueue.Workload{
		partialPreemptionVictimInCQ("A", "a1", 0, 4, 2),
		partialPreemptionVictimInCQ("B", "b1", 0, 4, 3),
	}
	incoming := utiltestingapi.MakeWorkload("p1", "").UID("p1").Request(corev1.ResourceCPU, "7").Obj()

	targets := getPartialPreemptionTargetsForCohort(t, time.Now(), cqs, victims, incoming, "P")

	byName := make(map[string]*Target, len(targets))
	for _, target := range targets {
		byName[target.WorkloadInfo.Obj.Name] = target
	}
	if a1 := byName["a1"]; a1 == nil || a1.PartialPreemption != nil {
		t.Errorf("a1: want a whole-eviction target, got %+v", a1)
	}
	b1 := byName["b1"]
	if b1 == nil || b1.PartialPreemption == nil {
		t.Fatalf("b1: want a partial target, got %+v", b1)
	}
	if got := b1.PartialPreemption.RetainedCount; got != 3 {
		t.Errorf("b1: RetainedCount = %d, want 3", got)
	}
}

// TestPartialPreemptionFillBackCohort2Regression covers cohort "coh2": P
// nominal 8, A nominal 5, borrowing limit 20 (13 total). A is used at 10
// (floor 5, equal to its nominal). P requests 11.
func TestPartialPreemptionFillBackCohort2Regression(t *testing.T) {
	now := time.Now()
	cqs := func() []*kueue.ClusterQueue {
		return []*kueue.ClusterQueue{
			utiltestingapi.MakeClusterQueue("P2").Cohort("coh2").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "8").Obj()).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					BorrowWithinCohort:  &kueue.BorrowWithinCohort{Policy: kueue.BorrowWithinCohortPolicyLowerPriority},
				}).
				Obj(),
			utiltestingapi.MakeClusterQueue("A2").Cohort("coh2").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "5", "20").Obj()).
				Obj(),
		}
	}
	victims := func() []*kueue.Workload {
		return []*kueue.Workload{partialPreemptionVictimInCQ("A2", "a2", 0, 10, 5)}
	}
	incoming := func() *kueue.Workload {
		return utiltestingapi.MakeWorkload("p2", "").UID("p2").Priority(10).Request(corev1.ResourceCPU, "11").Obj()
	}

	t.Run("gate off succeeds with a single whole eviction", func(t *testing.T) {
		features.SetFeatureGateDuringTest(t, features.PartialPreemption, false)
		targets := getPartialPreemptionTargetsForCohort(t, now, cqs(), victims(), incoming(), "P2")
		if len(targets) != 1 || targets[0].WorkloadInfo.Obj.Name != "a2" || targets[0].PartialPreemption != nil {
			t.Fatalf("want a single whole eviction of a2, got %+v", targets)
		}
	})

	t.Run("gate on evicts a2, because shrinking it to its floor is not enough", func(t *testing.T) {
		features.SetFeatureGateDuringTest(t, features.PartialPreemption, true)
		targets := getPartialPreemptionTargetsForCohort(t, now, cqs(), victims(), incoming(), "P2")
		if len(targets) != 1 || targets[0].WorkloadInfo.Obj.Name != "a2" || targets[0].PartialPreemption != nil {
			t.Errorf("want a single whole eviction of a2, got %+v", targets)
		}
	})
}
