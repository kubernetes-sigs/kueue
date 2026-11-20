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
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

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
)

// fsFallbackPreemptorCQ is the ClusterQueue of the incoming workload in the fallback fixtures.
const fsFallbackPreemptorCQ kueue.ClusterQueueReference = "p"

// fsFallbackWl describes an admitted workload of a fallback fixture.
type fsFallbackWl struct {
	name, cq, cpu string
	prio          int32
}

// fsFallbackTree returns the ClusterQueue tree the fallback cases share: p and s in org-a, x in org-b, all under root.
func fsFallbackTree(rootQuota, pQuota string, pDefaultWeight bool, withinClusterQueue kueue.PreemptionPolicy) ([]*kueue.ClusterQueue, []*kueue.Cohort) {
	p := utiltestingapi.MakeClusterQueue(string(fsFallbackPreemptorCQ)).
		Cohort("org-a").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, pQuota).Obj()).
		Preemption(kueue.ClusterQueuePreemption{
			WithinClusterQueue:  withinClusterQueue,
			ReclaimWithinCohort: kueue.PreemptionPolicyAny,
		})
	if !pDefaultWeight {
		p = p.FairWeight(resource.MustParse("5"))
	}
	clusterQueues := []*kueue.ClusterQueue{
		p.Obj(),
		utiltestingapi.MakeClusterQueue("s").
			Cohort("org-a").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "0").Obj()).
			Obj(),
		utiltestingapi.MakeClusterQueue("x").
			Cohort("org-b").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "0").Obj()).
			Obj(),
	}
	cohorts := []*kueue.Cohort{
		utiltestingapi.MakeCohort("root").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, rootQuota).Obj()).
			Obj(),
		utiltestingapi.MakeCohort("org-a").Parent("root").Obj(),
		utiltestingapi.MakeCohort("org-b").Parent("root").Obj(),
	}
	return clusterQueues, cohorts
}

// fsFallbackAdmitted builds the admitted workloads of a fallback fixture.
func fsFallbackAdmitted(now time.Time, wls []fsFallbackWl) []kueue.Workload {
	admitted := make([]kueue.Workload, 0, len(wls))
	for _, wl := range wls {
		admitted = append(admitted, *utiltestingapi.MakeWorkload(wl.name, "").
			Request(corev1.ResourceCPU, wl.cpu).
			Priority(wl.prio).
			SimpleReserveQuota(kueue.ClusterQueueReference(wl.cq), "default", now).
			Obj())
	}
	return admitted
}

// fsFallbackIncoming builds the incoming workload of a fallback fixture.
func fsFallbackIncoming(cpu string) *kueue.Workload {
	return utiltestingapi.MakeWorkload("p_high", "").Request(corev1.ResourceCPU, cpu).Priority(10).Obj()
}

// fsFallbackReproWls are the admitted workloads of the reproduced failure: two in p, one in s, two in x.
var fsFallbackReproWls = []fsFallbackWl{
	{name: "p_low", cq: "p", cpu: "4", prio: 1},
	{name: "p_mid", cq: "p", cpu: "4", prio: 2},
	{name: "s1", cq: "s", cpu: "3", prio: 0},
	{name: "x1", cq: "x", cpu: "4", prio: 0},
	{name: "x2", cq: "x", cpu: "9", prio: 1},
}

// fsRejectedPlanStrategies are the fair sharing strategies that reproduce the rejected plan.
var fsRejectedPlanStrategies = []config.PreemptionStrategy{config.LessThanInitialShare}

// fsDefaultsRejectedTree returns p and q in one flat cohort, where the default strategies pick a plan the final check rejects.
func fsDefaultsRejectedTree() ([]*kueue.ClusterQueue, []*kueue.Cohort) {
	makeCQ := func(name string) *kueue.ClusterQueue {
		return utiltestingapi.MakeClusterQueue(name).
			Cohort("all").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "50").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			Obj()
	}
	return []*kueue.ClusterQueue{makeCQ(string(fsFallbackPreemptorCQ)), makeCQ("q")},
		[]*kueue.Cohort{utiltestingapi.MakeCohort("all").Obj()}
}

// fsDefaultsRejectedWls are the admitted workloads of the default-strategies fixture: two in p and one borrowing in q.
var fsDefaultsRejectedWls = []fsFallbackWl{
	{name: "p1", cq: "p", cpu: "20", prio: 0},
	{name: "p2", cq: "p", cpu: "20", prio: 0},
	{name: "t", cq: "q", cpu: "60", prio: 0},
}

// fsFallbackFixture is the scheduling state of one fallback scenario, ready for a preemption search.
type fsFallbackFixture struct {
	clusterQueues []*kueue.ClusterQueue
	cohorts       []*kueue.Cohort
	admitted      []kueue.Workload
	incoming      *kueue.Workload
	strategies    []config.PreemptionStrategy
}

// fsFallbackEnv is a preemption search over a fallback fixture, repeatable as the search restores the snapshot.
type fsFallbackEnv struct {
	preemptor   *Preemptor
	snapshot    *schdcache.Snapshot
	preemptorWl *workload.Info
	assignment  flavorassigner.Assignment
}

func newFsFallbackEnv(ctx context.Context, tb testing.TB, now time.Time, fixture fsFallbackFixture) *fsFallbackEnv {
	tb.Helper()
	log := log.FromContext(ctx)
	admitted := make([]kueue.Workload, len(fixture.admitted))
	copy(admitted, fixture.admitted)
	for i := range admitted {
		// Set the name as the UID so that the candidates ordering is predictable.
		admitted[i].UID = types.UID(admitted[i].Name)
	}
	cl := utiltesting.NewClientBuilder().WithLists(&kueue.WorkloadList{Items: admitted}).Build()
	cqCache := schdcache.New(cl)
	cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
	for _, cq := range fixture.clusterQueues {
		if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
			tb.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
		}
	}
	for _, cohort := range fixture.cohorts {
		if err := cqCache.AddOrUpdateCohort(cohort); err != nil {
			tb.Fatalf("Couldn't add Cohort to cache: %v", err)
		}
	}
	snapshot, err := cqCache.Snapshot(ctx)
	if err != nil {
		tb.Fatalf("unexpected error while building snapshot: %v", err)
	}
	preemptorWl := workload.NewInfo(log, fixture.incoming)
	preemptorWl.ClusterQueue = fsFallbackPreemptorCQ
	return &fsFallbackEnv{
		preemptor: New(cl, workload.Ordering{}, &utiltesting.EventRecorder{}, &config.FairSharing{
			PreemptionStrategies: fixture.strategies,
		}, false, clocktesting.NewFakeClock(now), nil, preemptexpectations.New(), nil),
		snapshot:    snapshot,
		preemptorWl: preemptorWl,
		assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
			corev1.ResourceCPU: &flavorassigner.FlavorAssignment{Name: "default", Mode: flavorassigner.Preempt},
		}),
	}
}

// search runs the preemption search and returns the targets it found.
func (e *fsFallbackEnv) search(ctx context.Context) []*Target {
	strategies := e.preemptor.GetPreemptionStrategyIterator(ctx, *e.preemptorWl, e.snapshot, e.assignment)
	return e.preemptor.GetTargetsWithStrategy(ctx, strategies)
}

// fsFallbackSteps counts the fallback steps one preemption search logs.
type fsFallbackSteps struct {
	attempted           int
	skippedNoCandidates int
	skippedDoesNotFit   int
	guardRejected       int
	accepted            int
}

// TestFairPreemptionWithinClusterQueueFallbackSteps checks the logged fallback steps, since targets cannot show a skip.
func TestFairPreemptionWithinClusterQueueFallbackSteps(t *testing.T) {
	now := time.Now()
	reproCQs, reproCohorts := fsFallbackTree("24", "0", false, kueue.PreemptionPolicyLowerPriority)
	neverCQs, neverCohorts := fsFallbackTree("24", "0", false, kueue.PreemptionPolicyNever)
	borrowingCQs, borrowingCohorts := fsFallbackTree("118", "0", false, kueue.PreemptionPolicyLowerPriority)
	boundCQs, boundCohorts := fsFallbackTree("15", "0", false, kueue.PreemptionPolicyLowerPriority)
	withinNominalCQs, withinNominalCohorts := fsFallbackTree("25", "60", true, kueue.PreemptionPolicyLowerPriority)
	defaultsRejectedCQs, defaultsRejectedCohorts := fsDefaultsRejectedTree()

	cases := map[string]struct {
		fixture   fsFallbackFixture
		wantSteps fsFallbackSteps
	}{
		"own victim of the same size as the incoming workload: the fallback is attempted and accepted": {
			fixture: fsFallbackFixture{
				clusterQueues: reproCQs,
				cohorts:       reproCohorts,
				admitted:      fsFallbackAdmitted(now, fsFallbackReproWls),
				incoming:      fsFallbackIncoming("4"),
				strategies:    fsRejectedPlanStrategies,
			},
			wantSteps: fsFallbackSteps{attempted: 1, accepted: 1},
		},
		"own victim while borrowing: the fallback is attempted and rejected by the share guard": {
			fixture: fsFallbackFixture{
				clusterQueues: borrowingCQs,
				cohorts:       borrowingCohorts,
				admitted: fsFallbackAdmitted(now, []fsFallbackWl{
					{name: "p_low", cq: "p", cpu: "20", prio: 1},
					{name: "s1", cq: "s", cpu: "13", prio: 0},
					{name: "x1", cq: "x", cpu: "20", prio: 0},
					{name: "x2", cq: "x", cpu: "45", prio: 1},
				}),
				incoming:   fsFallbackIncoming("40"),
				strategies: fsRejectedPlanStrategies,
			},
			wantSteps: fsFallbackSteps{attempted: 1, guardRejected: 1},
		},
		"own victim within the ClusterQueue nominal but an ancestor cohort would borrow: the guard rejects": {
			fixture: fsFallbackFixture{
				clusterQueues: withinNominalCQs,
				cohorts:       withinNominalCohorts,
				admitted: fsFallbackAdmitted(now, []fsFallbackWl{
					{name: "p_low", cq: "p", cpu: "20", prio: 1},
					{name: "s1", cq: "s", cpu: "10", prio: 0},
					{name: "s2", cq: "s", cpu: "15", prio: 1},
					{name: "x1", cq: "x", cpu: "20", prio: 0},
				}),
				incoming:   fsFallbackIncoming("40"),
				strategies: fsRejectedPlanStrategies,
			},
			wantSteps: fsFallbackSteps{attempted: 1, guardRejected: 1},
		},
		"own workloads do not free enough for the incoming workload: the fallback is skipped": {
			fixture: fsFallbackFixture{
				clusterQueues: boundCQs,
				cohorts:       boundCohorts,
				admitted: fsFallbackAdmitted(now, []fsFallbackWl{
					{name: "p_low", cq: "p", cpu: "1", prio: 1},
					{name: "p_mid", cq: "p", cpu: "2", prio: 2},
					{name: "s1", cq: "s", cpu: "3", prio: 0},
					{name: "x1", cq: "x", cpu: "4", prio: 0},
					{name: "x2", cq: "x", cpu: "5", prio: 1},
				}),
				incoming:   fsFallbackIncoming("4"),
				strategies: fsRejectedPlanStrategies,
			},
			wantSteps: fsFallbackSteps{skippedDoesNotFit: 1},
		},
		"with the default strategies the fair search admits the workload on its own: the fallback never starts": {
			fixture: fsFallbackFixture{
				clusterQueues: reproCQs,
				cohorts:       reproCohorts,
				admitted:      fsFallbackAdmitted(now, fsFallbackReproWls),
				incoming:      fsFallbackIncoming("4"),
			},
			wantSteps: fsFallbackSteps{},
		},
		// The default strategies pick a plan the final check rejects, so the fallback runs and its targets pass the guard.
		"with the default strategies the plan is rejected by the final check: the fallback is attempted and accepted": {
			fixture: fsFallbackFixture{
				clusterQueues: defaultsRejectedCQs,
				cohorts:       defaultsRejectedCohorts,
				admitted:      fsFallbackAdmitted(now, fsDefaultsRejectedWls),
				incoming:      fsFallbackIncoming("30"),
			},
			wantSteps: fsFallbackSteps{attempted: 1, accepted: 1},
		},
		"the fair sharing strategies run out of candidates without fitting: the fallback never starts": {
			fixture: fsFallbackFixture{
				clusterQueues: reproCQs,
				cohorts:       reproCohorts,
				admitted:      fsFallbackAdmitted(now, fsFallbackReproWls),
				incoming:      fsFallbackIncoming("40"),
				strategies:    fsRejectedPlanStrategies,
			},
			wantSteps: fsFallbackSteps{},
		},
		"withinClusterQueue Never leaves the ClusterQueue without candidates: the fallback is skipped": {
			fixture: fsFallbackFixture{
				clusterQueues: neverCQs,
				cohorts:       neverCohorts,
				admitted:      fsFallbackAdmitted(now, fsFallbackReproWls),
				incoming:      fsFallbackIncoming("4"),
				strategies:    fsRejectedPlanStrategies,
			},
			wantSteps: fsFallbackSteps{skippedNoCandidates: 1},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.FairSharingPreemptWithinClusterQueueFallback, true)

			// Count the fallback steps from its V(4) log entries.
			var steps fsFallbackSteps
			log := funcr.New(func(_, args string) {
				switch {
				case strings.Contains(args, "Attempting within-ClusterQueue preemption fallback"):
					steps.attempted++
				case strings.Contains(args, "no candidates in the ClusterQueue"):
					steps.skippedNoCandidates++
				case strings.Contains(args, "does not fit even after preempting all its candidates"):
					steps.skippedDoesNotFit++
				case strings.Contains(args, "Discarding within-ClusterQueue preemption fallback targets"):
					steps.guardRejected++
				case strings.Contains(args, "Within-ClusterQueue preemption fallback targets accepted"):
					steps.accepted++
				}
			}, funcr.Options{Verbosity: 4})
			ctx := ctrl.LoggerInto(t.Context(), log)

			env := newFsFallbackEnv(ctx, t, now, tc.fixture)
			env.search(ctx)
			if steps != tc.wantSteps {
				t.Errorf("Fallback steps: got %+v, want %+v", steps, tc.wantSteps)
			}
		})
	}
}

// fsFallbackShrinkable builds an admitted own workload whose single PodSet of full Pods,
// one CPU each, can shrink to floor.
func fsFallbackShrinkable(now time.Time, name string, full, floor int32) kueue.Workload {
	return *utiltestingapi.MakeWorkload(name, "").
		Annotation(constants.PartialPreemptionAnnotation, "true").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, int(full)).
			Request(corev1.ResourceCPU, "1").
			PartialPreemptionMinRetainedCount(floor).
			Obj()).
		SimpleReserveQuota(fsFallbackPreemptorCQ, "default", now).
		Obj()
}

// partialTargetCtx returns the preemption context of a fallback fixture while the plan has
// taken from the victim: the snapshot holds it at keep Pods if keep > 0, or not at all, and
// the returned target describes that take, as the share guard sees it.
func partialTargetCtx(ctx context.Context, t *testing.T, now time.Time, fixture fsFallbackFixture, victim *kueue.Workload, keep int32) ([]*Target, *preemptionCtx) {
	t.Helper()
	env := newFsFallbackEnv(ctx, t, now, fixture)
	cq := env.snapshot.ClusterQueue(fsFallbackPreemptorCQ)
	full := cq.Workloads[workload.Key(victim)]
	if full == nil {
		t.Fatalf("The victim %q is not in the snapshot", victim.Name)
	}
	env.snapshot.RemoveWorkload(full)
	target := &Target{
		WorkloadInfo: full,
		Reason:       kueue.InClusterQueueReason,
		WorkloadCq:   cq,
	}
	if keep > 0 {
		reduced := reducedInfoForPartialPreemption(full, kueue.DefaultPodSetName, keep)
		env.snapshot.AddWorkload(reduced)
		target.PartialPreemption = &policy.PartialPreemptionTarget{
			PodSet:        kueue.DefaultPodSetName,
			RetainedCount: keep,
			ReducedInfo:   reduced,
		}
	}
	return []*Target{target}, env.preemptor.buildContext(ctx, *env.preemptorWl, env.assignment, env.snapshot)
}

// TestFairPreemptionWithinClusterQueueFallbackPartialTargetGuard checks the fallback share
// guard on a plan that takes from an own workload. While the plan runs, the snapshot holds a
// partially preempted victim at its reduced usage, so the guard has to restore the victim's
// full usage to compare against the state without the plan.
func TestFairPreemptionWithinClusterQueueFallbackPartialTargetGuard(t *testing.T) {
	now := time.Now()
	cases := map[string]struct {
		// others are the workloads of the other ClusterQueues.
		others []fsFallbackWl
		// full is the Pod count of the shrinkable own workload, one CPU each.
		full int32
		// keep is what the plan leaves of it, at least half of full, or 0 for a whole eviction.
		keep     int32
		incoming string
		want     bool
	}{
		"the shrink frees less than the incoming workload takes: the guard rejects": {
			others:   []fsFallbackWl{{name: "s1", cq: "s", cpu: "30"}, {name: "x1", cq: "x", cpu: "30"}},
			full:     40,
			keep:     28,
			incoming: "30",
			want:     false,
		},
		"the shrink frees more than the incoming workload takes: the guard accepts": {
			others:   []fsFallbackWl{{name: "s1", cq: "s", cpu: "44"}, {name: "x1", cq: "x", cpu: "44"}},
			full:     40,
			keep:     20,
			incoming: "10",
			want:     true,
		},
		"a whole eviction is compared with the workload's full usage: the guard accepts": {
			others:   []fsFallbackWl{{name: "s1", cq: "s", cpu: "30"}, {name: "x1", cq: "x", cpu: "30"}},
			full:     40,
			keep:     0,
			incoming: "30",
			want:     true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.PartialPreemption, true)
			features.SetFeatureGateDuringTest(t, features.FairSharingPreemptWithinClusterQueueFallback, true)
			features.SetFeatureGateDuringTest(t, features.FairSharingVerifyFinalTargets, true)

			ctx, _ := utiltesting.ContextWithLog(t)
			cqs, cohorts := fsFallbackTree("118", "0", false, kueue.PreemptionPolicyLowerPriority)
			victim := fsFallbackShrinkable(now, "p_low", tc.full, tc.full/2)
			targets, preemptionCtx := partialTargetCtx(ctx, t, now, fsFallbackFixture{
				clusterQueues: cqs,
				cohorts:       cohorts,
				admitted:      append(fsFallbackAdmitted(now, tc.others), victim),
				incoming:      fsFallbackIncoming(tc.incoming),
				strategies:    fsRejectedPlanStrategies,
			}, &victim, tc.keep)

			before := preemptionCtx.preemptorCQ.DominantResourceShare()
			got, reason := verifyWithinClusterQueueFallbackTargets(ctx, preemptionCtx, targets)
			if got != tc.want {
				t.Errorf("verifyWithinClusterQueueFallbackTargets() = %v (%q), want %v", got, reason, tc.want)
			}
			if !got && reason == "" {
				t.Error("verifyWithinClusterQueueFallbackTargets() rejected the targets without a reason")
			}
			if after := preemptionCtx.preemptorCQ.DominantResourceShare(); schdcache.CompareDRS(before, after) != 0 {
				t.Error("verifyWithinClusterQueueFallbackTargets() changed the usage of the ClusterQueue")
			}
		})
	}
}
