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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
func shrinkable(name string, count, floor int32) *utiltestingapi.WorkloadWrapper {
	return partialPreemptionVictim(name, count, floor, nil)
}

// shrinking builds a shrinkable victim that an earlier partial preemption
// already asked to shrink to target.
func shrinking(name string, count, floor, target int32) *utiltestingapi.WorkloadWrapper {
	return partialPreemptionVictim(name, count, floor, &target)
}

// fixed builds a victim of the "standalone" ClusterQueue with count Pods that
// did not opt in to partial preemption. Its floor would let it shrink to no
// Pods, so only the missing opt-in keeps it whole.
func fixed(name string, count int32) *utiltestingapi.WorkloadWrapper {
	wl := partialPreemptionVictim(name, count, 0, nil)
	delete(wl.Workload.Annotations, constants.PartialPreemptionAnnotation)
	return wl
}

// beingEvicted marks the victim as already being evicted.
func beingEvicted(wl *utiltestingapi.WorkloadWrapper) *utiltestingapi.WorkloadWrapper {
	workloadevict.SetEvictedCondition(wl.Obj(), time.Now(), kueue.WorkloadEvictedByPreemption, "already evicted")
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
	client    client.Client
	cache     *schdcache.Cache
	preemptor *Preemptor
	pCtx      *preemptionCtx
	// pristine is a snapshot of the same state that no search touches. It comes
	// from a cache of its own, so it shares no workload with the snapshot that
	// the search works on.
	pristine *schdcache.Snapshot
}

func newPlanFixture(t *testing.T, fairSharing *config.FairSharing, cqs []*kueue.ClusterQueue, admitted []*kueue.Workload, incoming *kueue.Workload) planFixture {
	t.Helper()
	ctx, log := utiltesting.ContextWithLog(t)
	items := make([]kueue.Workload, len(admitted))
	for i := range admitted {
		items[i] = *admitted[i]
		if items[i].UID == "" {
			// Set name as UID so that candidates sorting is predictable.
			items[i].UID = types.UID(items[i].Name)
		}
	}
	cl := utiltesting.NewClientBuilder().
		WithLists(&kueue.WorkloadList{Items: items}).
		WithStatusSubresource(&kueue.Workload{}).
		WithInterceptorFuncs(interceptor.Funcs{SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration}).
		Build()
	// newCache returns a cache of the ClusterQueues, with the admitted
	// workloads as it lists them from the client.
	newCache := func() *schdcache.Cache {
		cqCache := schdcache.New(cl)
		cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
		for _, cq := range cqs {
			if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("Couldn't add ClusterQueue %q to cache: %v", cq.Name, err)
			}
		}
		return cqCache
	}
	cqCache := newCache()
	preemptor := New(cl, workload.Ordering{}, &utiltesting.EventRecorder{}, fairSharing, false,
		clocktesting.NewFakeClock(time.Now()), nil, preemptexpectations.New(), nil, nil)
	snapshot, err := cqCache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	// The snapshots of a cache share its workloads, so they would all see a
	// workload that a search changes in place.
	pristine, err := newCache().Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	wlInfo := workload.NewInfo(log, incoming)
	wlInfo.ClusterQueue = kueue.ClusterQueueReference(cqs[0].Name)
	pCtx := preemptor.buildContext(ctx, *wlInfo, singlePodSetAssignment(flavorassigner.ResourceAssignment{
		corev1.ResourceCPU: &flavorassigner.FlavorAssignment{Name: "default", Mode: flavorassigner.Preempt},
	}), snapshot)
	return planFixture{ctx: ctx, client: cl, cache: cqCache, preemptor: preemptor, pCtx: pCtx, pristine: pristine}
}

// search returns the targets that the search finds with the strategies. It
// checks that the search leaves the snapshot, and the workloads in it, as it
// found them.
func (f planFixture) search(t *testing.T, strategies iter.Seq[PreemptionStrategy]) []*Target {
	t.Helper()
	targets := f.preemptor.getTargets(f.ctx, strategies)
	if diff := cmp.Diff(f.pristine, f.pCtx.snapshot, snapCmpOpts); diff != "" {
		t.Errorf("The search changed the snapshot (-before,+after):\n%s", diff)
	}
	return targets
}

// targets returns the targets that the search finds with the strategies, the
// way the test cases expect them (see search and planTargets).
func (f planFixture) targets(t *testing.T, strategies iter.Seq[PreemptionStrategy]) []planTarget {
	t.Helper()
	return planTargets(t, f.search(t, strategies))
}

// planTargets returns the targets the way the test cases expect them. It
// checks that a partial target carries the victim as it is after the shrink.
func planTargets(t *testing.T, targets []*Target) []planTarget {
	t.Helper()
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
	return got
}

// issue issues the targets, and checks what that does to the victims. An
// evicted victim is evicted. A shrunk victim is marked as partially preempted
// and asked to keep the count of its target, and nothing else changes in its
// status: it stays admitted and charged for all its Pods, and it releases the
// quota when the Pods terminate.
//
// The victims named in deleted are deleted before the targets are issued. Each
// of them counts as preempted, and stays deleted.
func (f planFixture) issue(t *testing.T, targets []*Target, deleted ...string) {
	t.Helper()
	for _, name := range deleted {
		if err := f.client.Delete(f.ctx, utiltestingapi.MakeWorkload(name, "").Obj()); err != nil {
			t.Fatalf("Failed to delete victim %q: %v", name, err)
		}
	}
	// The victims as they are before the targets are issued. A target shares
	// its victim with the cache, so issuing it could change the victim in place.
	victims := make([]*kueue.Workload, len(targets))
	for i, target := range targets {
		victims[i] = target.WorkloadInfo.Obj.DeepCopy()
	}
	preempted, failed, err := f.preemptor.IssuePreemptions(f.ctx, f.cache, &f.pCtx.preemptor, targets, f.pCtx.preemptorCQ)
	if err != nil {
		t.Fatalf("IssuePreemptions failed: %v", err)
	}
	if preempted != len(targets) || failed != 0 {
		t.Errorf("IssuePreemptions = (preempted=%d, failed=%d), want (%d, 0)", preempted, failed, len(targets))
	}
	for i, target := range targets {
		before := victims[i]
		got := &kueue.Workload{}
		err := f.client.Get(f.ctx, client.ObjectKeyFromObject(before), got)
		if slices.Contains(deleted, before.Name) {
			if !apierrors.IsNotFound(err) {
				t.Errorf("Victim %q: got error %v, want NotFound", before.Name, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("Failed to get victim %q: %v", before.Name, err)
		}
		pp := target.PartialPreemption
		if pp == nil {
			if !workloadevict.IsEvicted(got) {
				t.Errorf("Victim %q is not evicted", before.Name)
			}
			continue
		}
		if workloadevict.IsEvicted(got) {
			t.Errorf("Victim %q is evicted, want it to stay admitted", before.Name)
		}
		if retained, found := workload.PreemptionRetainedCount(got, pp.PodSet); !found || retained != pp.RetainedCount {
			t.Errorf("Victim %q is asked to keep (%d, %t) Pods of PodSet %q, want (%d, true)", before.Name, retained, found, pp.PodSet, pp.RetainedCount)
		}
		if !apimeta.IsStatusConditionTrue(got.Status.Conditions, kueue.WorkloadPartiallyPreempted) {
			t.Errorf("Victim %q is not marked as partially preempted", before.Name)
		}
		// The shrink sets the count to retain, and the PartiallyPreempted
		// condition. The rest of the status stays as it was: the victim keeps its
		// other conditions and its quota reservation.
		shrinkOnly := cmp.Options{
			cmpopts.IgnoreFields(kueue.PodSetAssignment{}, "PartialPreemption"),
			cmpopts.IgnoreSliceElements(func(condition metav1.Condition) bool {
				return condition.Type == kueue.WorkloadPartiallyPreempted
			}),
		}
		if diff := cmp.Diff(before.Status, got.Status, shrinkOnly); diff != "" {
			t.Errorf("Victim %q changed beyond the shrink (-before,+after):\n%s", before.Name, diff)
		}
	}
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
			candidates:  []*kueue.Workload{fixed("a", 2).Obj(), fixed("b", 4).Obj(), fixed("c", 1).Obj()},
			incomingCPU: "4",
			wantTargets: []planTarget{evict("b")},
			wantYielded: 2,
		},
		"gate off: a victim that opted in is evicted": {
			quota:       "6",
			candidates:  []*kueue.Workload{shrinkable("v", 5, 1).Obj()},
			incomingCPU: "4",
			wantTargets: []planTarget{evict("v")},
			wantYielded: 1,
		},
		"gate off: an in-flight shrink is ignored": {
			quota:       "6",
			candidates:  []*kueue.Workload{shrinking("v", 5, 1, 3).Obj()},
			incomingCPU: "3",
			wantTargets: []planTarget{evict("v")},
			wantYielded: 1,
		},
		"nobody opted in: same targets as with the gate off, after a shrinking walk over every candidate": {
			partialPreemption: true,
			quota:             "7",
			candidates:        []*kueue.Workload{fixed("a", 2).Obj(), fixed("b", 4).Obj(), fixed("c", 1).Obj()},
			incomingCPU:       "4",
			wantTargets:       []planTarget{evict("b")},
			wantYielded:       5,
		},
		"a victim shrinks only as far as the workload needs": {
			partialPreemption: true,
			quota:             "6",
			candidates:        []*kueue.Workload{shrinkable("v", 5, 1).Obj()},
			incomingCPU:       "4",
			wantTargets:       []planTarget{shrink("v", 2)},
			wantYielded:       1,
		},
		"a later candidate shrinks instead of an earlier one being evicted": {
			partialPreemption: true,
			quota:             "10",
			candidates:        []*kueue.Workload{fixed("a", 4).Obj(), shrinkable("b", 6, 2).Obj()},
			incomingCPU:       "4",
			wantTargets:       []planTarget{shrink("b", 2)},
			wantYielded:       2,
		},
		"gate off: a candidate that is only offered while an earlier one is not evicted is not reached": {
			quota:       "10",
			candidates:  []*kueue.Workload{fixed("a", 4).Obj(), shrinkable("b", 6, 2).Obj()},
			onlyWith:    map[string]string{"b": "a"},
			incomingCPU: "4",
			wantTargets: []planTarget{evict("a")},
			wantYielded: 1,
		},
		"a candidate that is only offered while an earlier one is not evicted shrinks": {
			partialPreemption: true,
			quota:             "10",
			candidates:        []*kueue.Workload{fixed("a", 4).Obj(), shrinkable("b", 6, 2).Obj()},
			onlyWith:          map[string]string{"b": "a"},
			incomingCPU:       "4",
			wantTargets:       []planTarget{shrink("b", 2)},
			wantYielded:       2,
		},
		"a victim that is not offered once an earlier one is evicted is not evicted": {
			partialPreemption: true,
			quota:             "12",
			candidates:        []*kueue.Workload{shrinkable("a", 4, 3).Obj(), fixed("b", 4).Obj(), fixed("c", 4).Obj()},
			onlyWith:          map[string]string{"b": "a"},
			incomingCPU:       "8",
			wantTargets:       []planTarget{evict("a"), evict("c")},
			wantYielded:       5,
		},
		"gate off: a victim that the workload does not need is added back": {
			quota:       "16",
			candidates:  []*kueue.Workload{shrinkable("a", 6, 3).Obj(), shrinkable("b", 8, 6).Obj()},
			incomingCPU: "9",
			wantTargets: []planTarget{evict("b")},
			wantYielded: 2,
		},
		"a victim that is added back without partial preemption is not evicted": {
			partialPreemption: true,
			quota:             "16",
			candidates:        []*kueue.Workload{shrinkable("a", 6, 3).Obj(), shrinkable("b", 8, 6).Obj()},
			incomingCPU:       "9",
			wantTargets:       []planTarget{evict("b")},
			wantYielded:       4,
		},
		"an eviction is given back before a shrink": {
			partialPreemption: true,
			quota:             "10",
			candidates:        []*kueue.Workload{fixed("a", 1).Obj(), fixed("b", 5).Obj(), shrinkable("c", 3, 1).Obj()},
			incomingCPU:       "7",
			wantTargets:       []planTarget{evict("b"), shrink("c", 2)},
			wantYielded:       5,
		},
		"an eviction is given back before a shrink that is already in progress": {
			partialPreemption: true,
			quota:             "10",
			candidates:        []*kueue.Workload{fixed("a", 1).Obj(), fixed("b", 6).Obj(), shrinking("c", 3, 1, 2).Obj()},
			incomingCPU:       "7",
			wantTargets:       []planTarget{evict("b"), shrink("c", 2)},
			wantYielded:       5,
		},
		"evictions come on top of the shrinks, and an evicted victim that can shrink gets its floor back": {
			partialPreemption: true,
			quota:             "10",
			candidates:        []*kueue.Workload{shrinkable("a", 5, 1).Obj(), fixed("b", 5).Obj()},
			incomingCPU:       "8",
			wantTargets:       []planTarget{shrink("a", 2), evict("b")},
			wantYielded:       4,
		},
		// Shrinking l1 and h1 to their floors is not enough. Evicting l1, the first
		// target of the search without partial preemption, is: l2 is left alone.
		"victims are evicted one by one, in the order of the walk": {
			partialPreemption: true,
			quota:             "11",
			candidates:        []*kueue.Workload{shrinkable("l1", 4, 2).Obj(), fixed("l2", 3).Obj(), shrinkable("h1", 4, 2).Obj()},
			incomingCPU:       "5",
			wantTargets:       []planTarget{evict("l1"), shrink("h1", 3)},
			wantYielded:       5,
		},
		// Without partial preemption a, b and c are removed, and a is added back.
		// With s shrunk, evicting b, the first of the two in the walk, is enough.
		"victims are evicted in the order of the walk, also when an earlier one is added back": {
			partialPreemption: true,
			quota:             "20",
			candidates:        []*kueue.Workload{fixed("a", 1).Obj(), fixed("b", 5).Obj(), fixed("c", 5).Obj(), shrinkable("s", 9, 5).Obj()},
			incomingCPU:       "8",
			wantTargets:       []planTarget{evict("b"), shrink("s", 6)},
			wantYielded:       7,
		},
		"a shrunk victim that is not needed after an eviction is left alone": {
			partialPreemption: true,
			quota:             "10",
			candidates:        []*kueue.Workload{shrinkable("a", 4, 3).Obj(), fixed("b", 6).Obj()},
			incomingCPU:       "6",
			wantTargets:       []planTarget{evict("b")},
			wantYielded:       4,
		},
		"a shrink that is already in progress counts as done": {
			partialPreemption: true,
			quota:             "6",
			candidates:        []*kueue.Workload{shrinking("v", 5, 1, 3).Obj()},
			incomingCPU:       "3",
			wantTargets:       []planTarget{shrink("v", 3)},
			wantYielded:       1,
		},
		"every in-flight shrink is used before any victim shrinks further": {
			partialPreemption: true,
			quota:             "10",
			candidates:        []*kueue.Workload{shrinking("a", 5, 1, 3).Obj(), shrinking("b", 5, 1, 3).Obj()},
			incomingCPU:       "4",
			wantTargets:       []planTarget{shrink("a", 3), shrink("b", 3)},
			wantYielded:       2,
		},
		"an in-flight victim shrinks further when its in-flight target is not enough": {
			partialPreemption: true,
			quota:             "6",
			candidates:        []*kueue.Workload{shrinking("v", 5, 1, 3).Obj()},
			incomingCPU:       "4",
			wantTargets:       []planTarget{shrink("v", 2)},
			wantYielded:       1,
		},
		"an in-flight victim never gets more than its in-flight target": {
			partialPreemption: true,
			quota:             "6",
			candidates:        []*kueue.Workload{shrinking("v", 5, 1, 3).Obj()},
			incomingCPU:       "2",
			wantTargets:       []planTarget{shrink("v", 3)},
			wantYielded:       1,
		},
		"an in-flight shrink that the workload does not need is left out": {
			partialPreemption: true,
			quota:             "8",
			candidates:        []*kueue.Workload{shrinking("a", 3, 1, 2).Obj(), shrinking("b", 5, 1, 2).Obj()},
			incomingCPU:       "3",
			wantTargets:       []planTarget{shrink("b", 2)},
			wantYielded:       2,
		},
		"an eviction that is already in progress counts as done": {
			partialPreemption: true,
			quota:             "10",
			candidates:        []*kueue.Workload{beingEvicted(fixed("e", 4)).Obj(), shrinkable("a", 6, 2).Obj()},
			incomingCPU:       "4",
			wantTargets:       []planTarget{evict("e")},
			wantYielded:       2,
		},
		"a victim that is being evicted does not shrink": {
			partialPreemption: true,
			quota:             "6",
			candidates:        []*kueue.Workload{beingEvicted(shrinkable("v", 5, 1)).Obj()},
			incomingCPU:       "4",
			wantTargets:       []planTarget{evict("v")},
			wantYielded:       2,
		},
		"a victim that has not reached readiness is evicted": {
			partialPreemption: true,
			quota:             "6",
			candidates: []*kueue.Workload{shrinkable("v", 5, 1).
				Condition(metav1.Condition{Type: kueue.WorkloadPodsReady, Status: metav1.ConditionFalse, Reason: kueue.WorkloadWaitForStart}).
				Obj()},
			incomingCPU: "4",
			wantTargets: []planTarget{evict("v")},
			wantYielded: 2,
		},
		"a victim that MultiKueue sent to a worker is evicted": {
			partialPreemption: true,
			quota:             "6",
			candidates:        []*kueue.Workload{shrinkable("v", 5, 1).Label(kueue.MultiKueueOriginLabel, "manager").Obj()},
			incomingCPU:       "4",
			wantTargets:       []planTarget{evict("v")},
			wantYielded:       2,
		},
		"a victim waiting for recovery still shrinks": {
			partialPreemption: true,
			quota:             "6",
			candidates: []*kueue.Workload{shrinkable("v", 5, 1).
				Condition(metav1.Condition{Type: kueue.WorkloadPodsReady, Status: metav1.ConditionFalse, Reason: kueue.WorkloadWaitForRecovery}).
				Obj()},
			incomingCPU: "4",
			wantTargets: []planTarget{shrink("v", 2)},
			wantYielded: 1,
		},
		"a victim that reached readiness still shrinks": {
			partialPreemption: true,
			quota:             "6",
			candidates: []*kueue.Workload{shrinkable("v", 5, 1).
				Condition(metav1.Condition{Type: kueue.WorkloadPodsReady, Status: metav1.ConditionTrue, Reason: kueue.WorkloadStarted}).
				Obj()},
			incomingCPU: "4",
			wantTargets: []planTarget{shrink("v", 2)},
			wantYielded: 1,
		},
		"a victim with an undetermined readiness is evicted": {
			partialPreemption: true,
			quota:             "6",
			candidates: []*kueue.Workload{shrinkable("v", 5, 1).
				Condition(metav1.Condition{Type: kueue.WorkloadPodsReady, Status: metav1.ConditionUnknown, Reason: kueue.WorkloadWaitForStart}).
				Obj()},
			incomingCPU: "4",
			wantTargets: []planTarget{evict("v")},
			wantYielded: 2,
		},
		"a victim selected by a PreemptionConfig rule is evicted": {
			partialPreemption: true,
			quota:             "6",
			candidates:        []*kueue.Workload{shrinkable("v", 5, 1).Obj()},
			reasons:           map[string]string{"v": kueue.ConfigurablePreemptionReason},
			incomingCPU:       "4",
			wantTargets:       []planTarget{evict("v")},
			wantYielded:       2,
		},
		"no targets when the workload does not fit even without any candidate": {
			partialPreemption: true,
			quota:             "10",
			candidates:        []*kueue.Workload{shrinkable("train", 3, 2).Obj()},
			others:            []*kueue.Workload{fixed("serve", 6).Obj()},
			incomingCPU:       "6",
			wantYielded:       2,
		},
		"the eviction result is used when the result with a shrink is rejected": {
			partialPreemption: true,
			quota:             "6",
			candidates:        []*kueue.Workload{shrinkable("v", 5, 1).Obj()},
			incomingCPU:       "4",
			verify:            rejectShrinks,
			wantTargets:       []planTarget{evict("v")},
			wantYielded:       2,
		},
		"the eviction result is used when the result with a shrink and an eviction is rejected": {
			partialPreemption: true,
			quota:             "10",
			candidates:        []*kueue.Workload{shrinkable("a", 5, 1).Obj(), fixed("b", 5).Obj()},
			incomingCPU:       "8",
			verify:            rejectShrinks,
			wantTargets:       []planTarget{evict("a"), evict("b")},
			wantYielded:       4,
		},
		"no targets when the eviction result is rejected too": {
			partialPreemption: true,
			quota:             "6",
			candidates:        []*kueue.Workload{shrinkable("v", 5, 1).Obj()},
			incomingCPU:       "4",
			verify:            reject,
			wantYielded:       2,
		},
		"gate off: no targets when the eviction result is rejected": {
			quota:       "6",
			candidates:  []*kueue.Workload{fixed("v", 5).Obj()},
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

// TestPartialPreemption checks the targets that the search finds with the
// strategies of the Preemptor, and what issuing them does to the victims.
//
// The incoming workload needs preemption in the first of the ClusterQueues.
// Every Pod requests one CPU.
//
// Some cases draw the cohort as it is before the preemption. A row is a
// ClusterQueue: its name, its nominal quota in brackets, and one letter for
// each Pod of its workloads, which is the first letter of the name of the
// workload:
//
//	a [2]  ..          <- in: 3 CPUs
//	b [2]  PPp(p) FF
//
//	.    a CPU of the nominal quota that no workload uses
//	P    a Pod of p below its floor, which partial preemption does not take
//	p    a Pod of p above its floor
//	(p)  a Pod that p was already asked to release
//	FF   a workload that cannot shrink: all its Pods are below its floor
//	in   the incoming workload, with the CPUs that it requests
func TestPartialPreemption(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	// The Fair Sharing configurations: the default strategies, and each of the
	// two strategies alone.
	defaultStrategies := &config.FairSharing{}
	finalShare := &config.FairSharing{PreemptionStrategies: []config.PreemptionStrategy{config.LessThanOrEqualToFinalShare}}
	initialShare := &config.FairSharing{PreemptionStrategies: []config.PreemptionStrategy{config.LessThanInitialShare}}
	// cohortPreemption lets a ClusterQueue preempt workloads of a lower priority
	// of its own, and reclaim from its cohort.
	cohortPreemption := kueue.ClusterQueuePreemption{
		WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
		ReclaimWithinCohort: kueue.PreemptionPolicyAny,
	}
	// alreadyShrinking is the condition of a victim that an earlier scheduling
	// cycle asked to shrink.
	alreadyShrinking := metav1.Condition{
		Type:               kueue.WorkloadPartiallyPreempted,
		Status:             metav1.ConditionTrue,
		Reason:             kueue.InCohortFairSharingReason,
		Message:            "Requested scale-down by an earlier scheduling cycle",
		LastTransitionTime: metav1.NewTime(now),
	}

	cases := map[string]struct {
		featureGates map[featuregate.Feature]bool
		// fairSharing are the Fair Sharing configurations to run the case with:
		// the targets are the same with each of them. Without any, the case runs
		// once, with classical preemption.
		fairSharing   []*config.FairSharing
		clusterQueues []*kueue.ClusterQueue
		admitted      []*kueue.Workload
		incoming      *kueue.Workload
		wantTargets   []planTarget
		// deletedAfterSearch are victims deleted after the search, before the
		// targets are issued.
		deletedAfterSearch []string
	}{
		"gate on: a victim deleted after the search counts as preempted": {
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("standalone").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "6").Obj()).
					Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
					Obj(),
			},
			admitted: []*kueue.Workload{
				utiltestingapi.MakeWorkload("victim", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 5).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(1).
						Obj()).
					SimpleReserveQuota("standalone", "default", now).
					AdmittedAt(true, now).
					Obj(),
			},
			incoming:           utiltestingapi.MakeWorkload("in", "").Priority(1).Request(corev1.ResourceCPU, "4").Obj(),
			wantTargets:        []planTarget{shrink("victim", 2)},
			deletedAfterSearch: []string{"victim"},
		},
		"gate on: reduces the victim to free only what the incoming workload needs": {
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("standalone").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "6").Obj()).
					Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
					Obj(),
			},
			admitted: []*kueue.Workload{
				utiltestingapi.MakeWorkload("victim", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 5).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(1).
						Obj()).
					SimpleReserveQuota("standalone", "default", now).
					AdmittedAt(true, now).
					Obj(),
			},
			incoming:    utiltestingapi.MakeWorkload("in", "").Priority(1).Request(corev1.ResourceCPU, "4").Obj(),
			wantTargets: []planTarget{shrink("victim", 2)},
		},
		"gate off: whole eviction": {
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: false},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("standalone").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "6").Obj()).
					Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
					Obj(),
			},
			admitted: []*kueue.Workload{
				utiltestingapi.MakeWorkload("victim", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 5).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(1).
						Obj()).
					SimpleReserveQuota("standalone", "default", now).
					Obj(),
			},
			incoming:    utiltestingapi.MakeWorkload("in", "").Priority(1).Request(corev1.ResourceCPU, "4").Obj(),
			wantTargets: []planTarget{evict("victim")},
		},
		"no fit: do not return an in-flight partial target": {
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("standalone").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "6").Obj()).
					Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
					Obj(),
			},
			admitted: []*kueue.Workload{
				utiltestingapi.MakeWorkload("victim", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 5).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(1).
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("standalone").
						PodSets(utiltestingapi.MakePodSetAssignment("main").
							Assignment(corev1.ResourceCPU, "default", "5").
							Count(5).
							PartialPreemptionRetainedCount(2).
							Obj()).
						Obj(), now).
					Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("in", "").Priority(1).Request(corev1.ResourceCPU, "7").Obj(),
		},
		"reclaims only enough": {
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("standalone").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "20").Obj()).
					Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
					Obj(),
			},
			admitted: []*kueue.Workload{
				utiltestingapi.MakeWorkload("victim", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 20).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(5).
						Obj()).
					SimpleReserveQuota("standalone", "default", now).
					Obj(),
			},
			incoming:    utiltestingapi.MakeWorkload("in", "").Priority(1).Request(corev1.ResourceCPU, "10").Obj(),
			wantTargets: []planTarget{shrink("victim", 10)},
		},
		"multiple candidates each partially reduced": {
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("standalone").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10").Obj()).
					Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
					Obj(),
			},
			admitted: []*kueue.Workload{
				utiltestingapi.MakeWorkload("v1", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 5).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(4).
						Obj()).
					SimpleReserveQuota("standalone", "default", now).
					Obj(),
				utiltestingapi.MakeWorkload("v2", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 5).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(4).
						Obj()).
					SimpleReserveQuota("standalone", "default", now).
					Obj(),
			},
			incoming:    utiltestingapi.MakeWorkload("in", "").Priority(1).Request(corev1.ResourceCPU, "2").Obj(),
			wantTargets: []planTarget{shrink("v1", 4), shrink("v2", 4)},
		},
		"round-two merge preserves priority for a whole-only candidate": {
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("standalone").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "15").Obj()).
					Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
					Obj(),
			},
			admitted: []*kueue.Workload{
				utiltestingapi.MakeWorkload("H1", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(30).
					PodSets(*utiltestingapi.MakePodSet("main", 5).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(1).
						Obj()).
					SimpleReserveQuota("standalone", "default", now).
					Obj(),
				utiltestingapi.MakeWorkload("M1", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(20).
					PodSets(*utiltestingapi.MakePodSet("main", 5).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(1).
						Obj()).
					SimpleReserveQuota("standalone", "default", now).
					Obj(),
				utiltestingapi.MakeWorkload("L1", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(10).
					PodSets(*utiltestingapi.MakePodSet("main", 5).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(5).
						Obj()).
					SimpleReserveQuota("standalone", "default", now).
					Obj(),
			},
			incoming:    utiltestingapi.MakeWorkload("in", "").Priority(40).Request(corev1.ResourceCPU, "12").Obj(),
			wantTargets: []planTarget{evict("L1"), shrink("M1", 1), shrink("H1", 2)},
		},
		"multi-round progresses to full eviction": {
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("standalone").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10").Obj()).
					Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
					Obj(),
			},
			admitted: []*kueue.Workload{
				utiltestingapi.MakeWorkload("victim-a", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 5).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(1).
						Obj()).
					SimpleReserveQuota("standalone", "default", now).
					Obj(),
				utiltestingapi.MakeWorkload("victim-b", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 5).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(5).
						Obj()).
					SimpleReserveQuota("standalone", "default", now).
					Obj(),
			},
			incoming:    utiltestingapi.MakeWorkload("in", "").Priority(1).Request(corev1.ResourceCPU, "8").Obj(),
			wantTargets: []planTarget{shrink("victim-a", 2), evict("victim-b")},
		},
		"evicted partial-preemption victim is wholly evicted": {
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("standalone").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "6").Obj()).
					Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
					Obj(),
			},
			admitted: []*kueue.Workload{
				utiltestingapi.MakeWorkload("victim", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 5).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(1).
						Obj()).
					SimpleReserveQuota("standalone", "default", now).
					EvictedAt(now).
					Obj(),
			},
			incoming:    utiltestingapi.MakeWorkload("in", "").Priority(1).Request(corev1.ResourceCPU, "4").Obj(),
			wantTargets: []planTarget{evict("victim")},
		},
		"TAS victim is wholly evicted": {
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("standalone").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "6").Obj()).
					Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
					Obj(),
			},
			admitted: []*kueue.Workload{
				utiltestingapi.MakeWorkload("victim", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 5).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(1).
						RequiredTopologyRequest("rack").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("standalone").
						PodSets(utiltestingapi.MakePodSetAssignment("main").
							Assignment(corev1.ResourceCPU, "default", "5").
							Count(5).
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{"rack"}).Obj()).
							Obj()).
						Obj(), now).
					Obj(),
			},
			incoming:    utiltestingapi.MakeWorkload("in", "").Priority(1).Request(corev1.ResourceCPU, "4").Obj(),
			wantTargets: []planTarget{evict("victim")},
		},
		"multiple shrinkable PodSets are wholly evicted": {
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("standalone").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "12").Obj()).
					Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
					Obj(),
			},
			admitted: []*kueue.Workload{
				utiltestingapi.MakeWorkload("victim", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(
						*utiltestingapi.MakePodSet("main", 5).
							Request(corev1.ResourceCPU, "1").
							PartialPreemptionMinRetainedCount(1).
							Obj(),
						*utiltestingapi.MakePodSet("second", 5).
							Request(corev1.ResourceCPU, "1").
							PartialPreemptionMinRetainedCount(1).
							Obj(),
					).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("standalone").
						PodSets(
							utiltestingapi.MakePodSetAssignment("main").
								Assignment(corev1.ResourceCPU, "default", "5").
								Count(5).
								Obj(),
							utiltestingapi.MakePodSetAssignment("second").
								Assignment(corev1.ResourceCPU, "default", "5").
								Count(5).
								Obj(),
						).
						Obj(), now).
					Obj(),
			},
			incoming:    utiltestingapi.MakeWorkload("in", "").Priority(1).Request(corev1.ResourceCPU, "4").Obj(),
			wantTargets: []planTarget{evict("victim")},
		},
		// The floor of the victim equals its admitted count, so it cannot shrink.
		"non-shrinkable victim is wholly evicted": {
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("standalone").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "6").Obj()).
					Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
					Obj(),
			},
			admitted: []*kueue.Workload{
				utiltestingapi.MakeWorkload("victim", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 5).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(5).
						Obj()).
					SimpleReserveQuota("standalone", "default", now).
					Obj(),
			},
			incoming:    utiltestingapi.MakeWorkload("in", "").Priority(1).Request(corev1.ResourceCPU, "4").Obj(),
			wantTargets: []planTarget{evict("victim")},
		},
		// The other workload did not opt in to partial preemption.
		"fair sharing: evicts the victim whole when shrinking is not enough": {
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			fairSharing:  []*config.FairSharing{defaultStrategies},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("standalone").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "5").Obj()).
					Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
					Obj(),
			},
			admitted: []*kueue.Workload{
				utiltestingapi.MakeWorkload("victim", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 3).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(2).
						Obj()).
					SimpleReserveQuota("standalone", "default", now).
					Obj(),
				utiltestingapi.MakeWorkload("fixed", "").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 2).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(0).
						Obj()).
					SimpleReserveQuota("standalone", "default", now).
					Obj(),
			},
			incoming:    utiltestingapi.MakeWorkload("in", "").Priority(1).Request(corev1.ResourceCPU, "3").Obj(),
			wantTargets: []planTarget{evict("victim")},
		},
		// Cohort "coh1" has 10 CPUs: 8 of P, 2 of A, none of B. A uses 4 (floor 2),
		// and B uses 4 (floor 3). The 8 used leave 2 free, so a request of 7 needs
		// 5 more. Shrinking both victims to their floors frees only 2+1=3, so the
		// first candidate is evicted on top of that, and the other one stays at
		// its floor.
		"cohort: a victim is evicted on top of the shrinks, and the other one stays at its floor": {
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("P").
					Cohort("coh1").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "8").Obj()).
					Preemption(kueue.ClusterQueuePreemption{ReclaimWithinCohort: kueue.PreemptionPolicyAny}).
					Obj(),
				utiltestingapi.MakeClusterQueue("A").
					Cohort("coh1").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "2").Obj()).
					Obj(),
				utiltestingapi.MakeClusterQueue("B").
					Cohort("coh1").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "0").Obj()).
					Obj(),
			},
			admitted: []*kueue.Workload{
				utiltestingapi.MakeWorkload("a1", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 4).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(2).
						Obj()).
					SimpleReserveQuota("A", "default", now).
					Obj(),
				utiltestingapi.MakeWorkload("b1", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 4).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(3).
						Obj()).
					SimpleReserveQuota("B", "default", now).
					Obj(),
			},
			incoming:    utiltestingapi.MakeWorkload("in", "").Priority(0).Request(corev1.ResourceCPU, "7").Obj(),
			wantTargets: []planTarget{evict("a1"), shrink("b1", 3)},
		},
		// Cohort "coh2" has 13 CPUs: 8 of P2, and 5 of A2, which uses 10 (floor 5,
		// equal to its quota). P2 requests 11.
		"cohort, gate off: a single whole eviction": {
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: false},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("P2").
					Cohort("coh2").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "8").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						BorrowWithinCohort:  &kueue.BorrowWithinCohort{Policy: kueue.BorrowWithinCohortPolicyLowerPriority},
					}).
					Obj(),
				utiltestingapi.MakeClusterQueue("A2").
					Cohort("coh2").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "5", "20").Obj()).
					Obj(),
			},
			admitted: []*kueue.Workload{
				utiltestingapi.MakeWorkload("a2", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 10).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(5).
						Obj()).
					SimpleReserveQuota("A2", "default", now).
					Obj(),
			},
			incoming:    utiltestingapi.MakeWorkload("in", "").Priority(10).Request(corev1.ResourceCPU, "11").Obj(),
			wantTargets: []planTarget{evict("a2")},
		},
		"cohort: the victim is evicted, because shrinking it to its floor is not enough": {
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("P2").
					Cohort("coh2").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "8").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						BorrowWithinCohort:  &kueue.BorrowWithinCohort{Policy: kueue.BorrowWithinCohortPolicyLowerPriority},
					}).
					Obj(),
				utiltestingapi.MakeClusterQueue("A2").
					Cohort("coh2").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "5", "20").Obj()).
					Obj(),
			},
			admitted: []*kueue.Workload{
				utiltestingapi.MakeWorkload("a2", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 10).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(5).
						Obj()).
					SimpleReserveQuota("A2", "default", now).
					Obj(),
			},
			incoming:    utiltestingapi.MakeWorkload("in", "").Priority(10).Request(corev1.ResourceCPU, "11").Obj(),
			wantTargets: []planTarget{evict("a2")},
		},
		// ClusterQueue b uses all 6 CPUs of the cohort. The workload makes a borrow
		// 2, and b gives them only as long as it borrows more than a: the second
		// strategy offers v.
		"fair sharing: a victim shrinks only as far as the workload needs": {
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			fairSharing:  []*config.FairSharing{defaultStrategies, initialShare},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "2").Obj()).
					Preemption(cohortPreemption).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "2").Obj()).
					Preemption(cohortPreemption).
					Obj(),
				utiltestingapi.MakeClusterQueue("c").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "2").Obj()).
					Preemption(cohortPreemption).
					Obj(),
			},
			admitted: []*kueue.Workload{
				utiltestingapi.MakeWorkload("v", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 6).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(1).
						Obj()).
					SimpleReserveQuota("b", "default", now).
					Obj(),
			},
			incoming:    utiltestingapi.MakeWorkload("in", "").Priority(10).Request(corev1.ResourceCPU, "4").Obj(),
			wantTargets: []planTarget{shrink("v", 2)},
		},
		// ClusterQueue b uses the 4 CPUs of a, and the workload needs them back.
		// The shrink of p to 3 is not enough, and p is the first to be evicted.
		"fair sharing: a victim that is already shrinking is evicted when its shrink is not enough": {
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			fairSharing:  []*config.FairSharing{defaultStrategies, finalShare, initialShare},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "4").Obj()).
					Preemption(cohortPreemption).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "4").Obj()).
					Preemption(cohortPreemption).
					Obj(),
			},
			admitted: []*kueue.Workload{
				utiltestingapi.MakeWorkload("p", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 4).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(3).
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("b").
						PodSets(utiltestingapi.MakePodSetAssignment("main").
							Assignment(corev1.ResourceCPU, "default", "4").
							Count(4).
							PartialPreemptionRetainedCount(3).
							Obj()).
						Obj(), now).
					Obj(),
				utiltestingapi.MakeWorkload("fixed", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("main", 4).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(4).
						Obj()).
					SimpleReserveQuota("b", "default", now).
					Obj(),
			},
			incoming:    utiltestingapi.MakeWorkload("in", "").Priority(10).Request(corev1.ResourceCPU, "4").Obj(),
			wantTargets: []planTarget{evict("p")},
		},
		// ClusterQueue b uses all 10 CPUs of the cohort, and the workload needs 6.
		// With p and q at 2 Pods each, b borrows 2. Undoing the shrink of p makes b
		// borrow 4, as much as a borrows, so no strategy allows that shrink, and
		// there are no targets.
		"fair sharing: a shrink that the verification rejects is not a target": {
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			fairSharing:  []*config.FairSharing{defaultStrategies},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "2").Obj()).
					Preemption(cohortPreemption).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "2").Obj()).
					Preemption(cohortPreemption).
					Obj(),
				utiltestingapi.MakeClusterQueue("c").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "6").Obj()).
					Preemption(cohortPreemption).
					Obj(),
			},
			admitted: []*kueue.Workload{
				utiltestingapi.MakeWorkload("p", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 4).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(2).
						Obj()).
					SimpleReserveQuota("b", "default", now).
					Obj(),
				utiltestingapi.MakeWorkload("q", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("main", 6).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(0).
						Obj()).
					SimpleReserveQuota("b", "default", now).
					Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("in", "").Priority(10).Request(corev1.ResourceCPU, "6").Obj(),
		},
		"fair sharing: two victims of one ClusterQueue shrink, and neither is evicted": {
			// The workload needs 2 of the 3 CPUs that b borrows. p and q can release
			// one Pod each, so both shrink and neither is evicted. Afterwards a and b
			// borrow 1 CPU each.
			//
			//   a [1]  .          <- in: 2 CPUs
			//   b [3]  PPp QQq
			//   c [2]  ..
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			fairSharing:  []*config.FairSharing{defaultStrategies, initialShare},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj()).
					Preemption(cohortPreemption).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "3").Obj()).
					Preemption(cohortPreemption).
					Obj(),
				utiltestingapi.MakeClusterQueue("c").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "2").Obj()).
					Preemption(cohortPreemption).
					Obj(),
			},
			admitted: []*kueue.Workload{
				utiltestingapi.MakeWorkload("p", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 3).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(2).
						Obj()).
					SimpleReserveQuota("b", "default", now).
					Obj(),
				utiltestingapi.MakeWorkload("q", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("main", 3).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(2).
						Obj()).
					SimpleReserveQuota("b", "default", now).
					Obj(),
			},
			incoming:    utiltestingapi.MakeWorkload("in", "").Priority(10).Request(corev1.ResourceCPU, "2").Obj(),
			wantTargets: []planTarget{shrink("p", 2), shrink("q", 2)},
		},
		"fair sharing: a second victim shrinks when the one already shrinking does not free enough": {
			// The workload needs 2 of the 3 CPUs that b borrows. p was already asked to
			// shrink to 2 Pods, which frees one of them. q shrinks for the other one,
			// and p is not evicted.
			//
			//   a [1]  .            <- in: 2 CPUs
			//   b [3]  PP(p) QQq
			//   c [2]  ..
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			fairSharing:  []*config.FairSharing{defaultStrategies, initialShare},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj()).
					Preemption(cohortPreemption).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "3").Obj()).
					Preemption(cohortPreemption).
					Obj(),
				utiltestingapi.MakeClusterQueue("c").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "2").Obj()).
					Preemption(cohortPreemption).
					Obj(),
			},
			admitted: []*kueue.Workload{
				utiltestingapi.MakeWorkload("p", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 3).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(2).
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("b").
						PodSets(utiltestingapi.MakePodSetAssignment("main").
							Assignment(corev1.ResourceCPU, "default", "3").
							Count(3).
							PartialPreemptionRetainedCount(2).
							Obj()).
						Obj(), now).
					Condition(alreadyShrinking).
					Obj(),
				utiltestingapi.MakeWorkload("q", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("main", 3).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(2).
						Obj()).
					SimpleReserveQuota("b", "default", now).
					Obj(),
			},
			incoming:    utiltestingapi.MakeWorkload("in", "").Priority(10).Request(corev1.ResourceCPU, "2").Obj(),
			wantTargets: []planTarget{shrink("p", 2), shrink("q", 2)},
		},
		"fair sharing: a victim that cannot shrink is passed over, and the next one shrinks": {
			// The workload needs the 2 CPUs that b borrows. fixed is offered first and
			// cannot shrink, so it stays as it is. elastic releases 2 Pods.
			//
			//   a [2]  ..        <- in: 3 CPUs
			//   b [3]  FF Eee
			//   c [1]  .
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			fairSharing:  []*config.FairSharing{defaultStrategies, initialShare},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "2").Obj()).
					Preemption(cohortPreemption).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "3").Obj()).
					Preemption(cohortPreemption).
					Obj(),
				utiltestingapi.MakeClusterQueue("c").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj()).
					Preemption(cohortPreemption).
					Obj(),
			},
			admitted: []*kueue.Workload{
				utiltestingapi.MakeWorkload("fixed", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 2).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(2).
						Obj()).
					SimpleReserveQuota("b", "default", now).
					Obj(),
				utiltestingapi.MakeWorkload("elastic", "").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("main", 3).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(1).
						Obj()).
					SimpleReserveQuota("b", "default", now).
					Obj(),
			},
			incoming:    utiltestingapi.MakeWorkload("in", "").Priority(10).Request(corev1.ResourceCPU, "3").Obj(),
			wantTargets: []planTarget{shrink("elastic", 1)},
		},
	}
	for name, tc := range cases {
		fairSharing := tc.fairSharing
		if len(fairSharing) == 0 {
			// Classical preemption.
			fairSharing = []*config.FairSharing{nil}
		}
		for _, fs := range fairSharing {
			testName := name
			if fs != nil {
				testName = fmt.Sprintf("%s with %s", name, strategiesName(fs))
			}
			t.Run(testName, func(t *testing.T) {
				features.SetFeatureGatesDuringTest(t, tc.featureGates)
				fixture := newPlanFixture(t, fs, tc.clusterQueues, tc.admitted, tc.incoming)

				targets := fixture.search(t, fixture.preemptor.getPreemptionStrategyIterator(fixture.ctx, fixture.pCtx))

				if diff := cmp.Diff(tc.wantTargets, planTargets(t, targets), planTargetCmpOpts); diff != "" {
					t.Errorf("Unexpected targets (-want,+got):\n%s", diff)
				}
				fixture.issue(t, targets, tc.deletedAfterSearch...)
			})
		}
	}
}

// strategiesName names the Fair Sharing configuration after its strategies.
func strategiesName(fairSharing *config.FairSharing) string {
	if len(fairSharing.PreemptionStrategies) == 0 {
		return "the default strategies"
	}
	names := make([]string, len(fairSharing.PreemptionStrategies))
	for i, strategy := range fairSharing.PreemptionStrategies {
		names[i] = string(strategy)
	}
	return strings.Join(names, " and ")
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

// partialPreemptionVictim builds a victim of the "standalone" ClusterQueue that
// opted in to partial preemption. It is admitted with count Pods in its
// "executor" PodSet, which partial preemption may shrink to floor. With a
// non-nil retained, an earlier partial preemption already asked the victim to
// keep that many Pods.
func partialPreemptionVictim(name string, count, floor int32, retained *int32) *utiltestingapi.WorkloadWrapper {
	wl := utiltestingapi.MakeWorkload(name, "").
		UID(types.UID(name)).
		Annotation(constants.PartialPreemptionAnnotation, "true").
		Priority(0).
		PodSets(*utiltestingapi.MakePodSet("executor", int(count)).
			Request(corev1.ResourceCPU, "1").
			PartialPreemptionMinRetainedCount(floor).
			Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("standalone").
			PodSets(utiltestingapi.MakePodSetAssignment("executor").
				Assignment(corev1.ResourceCPU, "default", fmt.Sprintf("%d", count)).
				Count(count).
				Obj()).
			Obj(), time.Now())
	if retained != nil {
		wl.Status.Admission.PodSetAssignments[0].PartialPreemption = &kueue.PodSetPartialPreemptionStatus{RetainedCount: retained}
	}
	return wl
}
