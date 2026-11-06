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
	"iter"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	clocktesting "k8s.io/utils/clock/testing"

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
