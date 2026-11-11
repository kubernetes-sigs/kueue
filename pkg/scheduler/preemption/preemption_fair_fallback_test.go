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
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
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

// fsFallbackStrategies are the fair sharing strategies that reproduce the rejected plan.
var fsFallbackStrategies = []config.PreemptionStrategy{config.LessThanInitialShare}

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
				strategies:    fsFallbackStrategies,
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
				strategies: fsFallbackStrategies,
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
				strategies: fsFallbackStrategies,
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
				strategies: fsFallbackStrategies,
			},
			wantSteps: fsFallbackSteps{skippedDoesNotFit: 1},
		},
		"the fair sharing strategies run out of candidates without fitting: the fallback never starts": {
			fixture: fsFallbackFixture{
				clusterQueues: reproCQs,
				cohorts:       reproCohorts,
				admitted:      fsFallbackAdmitted(now, fsFallbackReproWls),
				incoming:      fsFallbackIncoming("40"),
				strategies:    fsFallbackStrategies,
			},
			wantSteps: fsFallbackSteps{},
		},
		"withinClusterQueue Never leaves the ClusterQueue without candidates: the fallback is skipped": {
			fixture: fsFallbackFixture{
				clusterQueues: neverCQs,
				cohorts:       neverCohorts,
				admitted:      fsFallbackAdmitted(now, fsFallbackReproWls),
				incoming:      fsFallbackIncoming("4"),
				strategies:    fsFallbackStrategies,
			},
			wantSteps: fsFallbackSteps{skippedNoCandidates: 1},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.FairSharingWithinClusterQueueFallback, true)

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
