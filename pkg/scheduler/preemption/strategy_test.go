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
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"

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

// The tests in this file exercise the preemption *plans*, not the preemption
// outcome. A plan is a lazily evaluated sequence of strategies, where each
// strategy is a sequence of candidate targets together with the
// StrategyParams the targets were generated for. Which of those targets is
// finally preempted is decided by classicalPreemptions/fairPreemptions and is
// covered by TestPreemption, TestFairPreemptions and TestHierarchicalPreemptions.

// wantTarget is a comparable projection of a *Target.
//
// Targets are not diffed directly. cmpopts.IgnoreUnexported cannot cover them
// on its own, because cmp reaches resources.resourceEntry, whose type is
// unexported and therefore cannot be listed. It only works together with the
// snapCmpOpts bundle (cmp.Comparer(resources.Equal) plus AllowUnexported for
// the ClusterQueueSnapshot and IgnoreFields to cut the cohort cycle), and even
// then a single wrong expectation prints tens of lines of Workload and
// ClusterQueueSnapshot internals. The projection keeps a failure down to the
// three fields a plan actually decides.
type wantTarget struct {
	Workload workload.Reference
	Reason   string
	CQ       kueue.ClusterQueueReference
}

// wantStrategy is a comparable projection of a single strategy of a plan,
// including the StrategyParams it was yielded with.
type wantStrategy struct {
	Borrowing bool
	Targets   []wantTarget
}

// planConsumption describes a consumer which abandons the plan early. The
// zero value consumes the plan in full, which is what most cases do in order
// to observe the whole candidate sequence.
type planConsumption struct {
	// stopAfterStrategies, when positive, stops the consumer from requesting
	// further strategies once that many have been yielded.
	stopAfterStrategies int
	// stopAfterFirstStrategyTargets, when positive, abandons the first
	// strategy after that many targets. Later strategies are consumed fully.
	stopAfterFirstStrategyTargets int
}

// consumePlan drains the plan and returns a comparable projection of what was
// yielded.
//
// Both plans mutate the snapshot while they iterate: a candidate is removed
// from it before being yielded, so that the following candidates are picked
// against the state the previous ones left behind. Those removals are undone
// by Cleanup, which every production consumer calls once an attempt is over -
// classicalPreemptions between the borrowing and the non-borrowing attempt,
// fairPreemptions after its single strategy. This consumer does the same, so
// that a later attempt starts from the snapshot the first one saw, and so
// that the caller can assert the snapshot was left as it was found.
func consumePlan(plan *PreemptionPlan, consumption planConsumption) []wantStrategy {
	gotStrategies := []wantStrategy{}
	for strategy := range plan.Strategies {
		targets := []wantTarget{}
		for candidate := range strategy.Candidates {
			targets = append(targets, wantTarget{
				Workload: workload.Key(candidate.WorkloadInfo.Obj),
				Reason:   candidate.Reason,
				CQ:       candidate.WorkloadCq.Name,
			})
			if len(gotStrategies) == 0 && consumption.stopAfterFirstStrategyTargets > 0 &&
				len(targets) >= consumption.stopAfterFirstStrategyTargets {
				break
			}
		}
		plan.Cleanup()
		gotStrategies = append(gotStrategies, wantStrategy{Borrowing: strategy.Borrowing, Targets: targets})
		if consumption.stopAfterStrategies > 0 && len(gotStrategies) >= consumption.stopAfterStrategies {
			break
		}
	}
	return gotStrategies
}

type planFixtureCfg struct {
	flavors          []*kueue.ResourceFlavor
	clusterQueues    []*kueue.ClusterQueue
	cohorts          []*kueue.Cohort
	admitted         []kueue.Workload
	incoming         *kueue.Workload
	targetCQ         kueue.ClusterQueueReference
	assignmentFlavor kueue.ResourceFlavorReference
	fairSharing      *config.FairSharing
	now              time.Time
}

type planFixture struct {
	preemptor *Preemptor
	pCtx      *preemptionCtx
	snapshot  *schdcache.Snapshot
	// pristine is an independent snapshot of the same cache, used to assert
	// that the plan left the working snapshot untouched.
	pristine *schdcache.Snapshot
}

// newPlanFixture builds the inputs of the plan functions the same way the
// scheduler does: a cache snapshot, a Preemptor and a preemptionCtx built by
// Preemptor.buildContext, so that frsNeedPreemption and workloadUsage are
// derived from a real flavor assignment.
func newPlanFixture(ctx context.Context, t *testing.T, log logr.Logger, cfg planFixtureCfg) planFixture {
	t.Helper()

	// Set the name as UID so that candidate sorting is deterministic.
	admitted := make([]kueue.Workload, len(cfg.admitted))
	copy(admitted, cfg.admitted)
	for i := range admitted {
		admitted[i].UID = types.UID(admitted[i].Name)
	}

	cl := utiltesting.NewClientBuilder().
		WithLists(&kueue.WorkloadList{Items: admitted}).
		Build()
	cqCache := schdcache.New(cl)
	for _, flv := range cfg.flavors {
		cqCache.AddOrUpdateResourceFlavor(log, flv)
	}
	for _, cq := range cfg.clusterQueues {
		if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
		}
	}
	for _, cohort := range cfg.cohorts {
		if err := cqCache.AddOrUpdateCohort(cohort); err != nil {
			t.Fatalf("Couldn't add Cohort to cache: %v", err)
		}
	}

	snapshot, err := cqCache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	pristine, err := cqCache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}

	preemptor := New(cl, workload.Ordering{}, &utiltesting.EventRecorder{}, cfg.fairSharing, false,
		clocktesting.NewFakeClock(cfg.now), nil, preemptexpectations.New(), nil)

	flavorName := cfg.assignmentFlavor
	if flavorName == "" {
		flavorName = "default"
	}
	wlInfo := workload.NewInfo(log, cfg.incoming)
	wlInfo.ClusterQueue = cfg.targetCQ
	assignment := singlePodSetAssignment(flavorassigner.ResourceAssignment{
		corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
			Name: flavorName,
			Mode: flavorassigner.Preempt,
		},
	})

	return planFixture{
		preemptor: preemptor,
		pCtx:      preemptor.buildContext(ctx, *wlInfo, assignment, snapshot),
		snapshot:  snapshot,
		pristine:  pristine,
	}
}

func TestClassicalPreemptionPlan(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	flavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("default").Obj(),
		utiltestingapi.MakeResourceFlavor("alternative").Obj(),
	}
	// Reclaiming from the cohort is allowed regardless of priority, so that
	// the cases below can vary a single dimension at a time.
	basePreemption := kueue.ClusterQueuePreemption{
		WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
		ReclaimWithinCohort: kueue.PreemptionPolicyAny,
	}
	makeCQ := func(name string, cohort kueue.CohortReference, nominal string) *utiltestingapi.ClusterQueueWrapper {
		wrapper := utiltestingapi.MakeClusterQueue(name).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, nominal).Obj()).
			Preemption(basePreemption)
		if cohort != "" {
			wrapper = wrapper.Cohort(cohort)
		}
		return wrapper
	}
	admittedWl := func(name string, cq kueue.ClusterQueueReference, cpu string, priority int32) kueue.Workload {
		return *utiltestingapi.MakeWorkload(name, "").
			Request(corev1.ResourceCPU, cpu).
			Priority(priority).
			SimpleReserveQuota(cq, "default", now).
			Obj()
	}
	incomingWl := func(cpu string, priority int32) *kueue.Workload {
		return utiltestingapi.MakeWorkload("in", "").
			Request(corev1.ResourceCPU, cpu).
			Priority(priority).
			Obj()
	}

	cases := map[string]struct {
		clusterQueues    []*kueue.ClusterQueue
		cohorts          []*kueue.Cohort
		admitted         []kueue.Workload
		incoming         *kueue.Workload
		targetCQ         kueue.ClusterQueueReference
		assignmentFlavor kueue.ResourceFlavorReference
		featureGates     map[featuregate.Feature]bool
		consumption      planConsumption

		wantStrategies []wantStrategy
	}{
		// C1: nothing to preempt anywhere; the plan still offers a single
		// borrowing attempt, so that a workload which only needs the quota
		// freed by admission bookkeeping is evaluated once.
		"no candidates yields a single borrowing strategy with no targets": {
			clusterQueues: []*kueue.ClusterQueue{makeCQ("a", "", "3").Obj()},
			incoming:      incomingWl("1", 10),
			targetCQ:      "a",
			wantStrategies: []wantStrategy{
				{Borrowing: true, Targets: []wantTarget{}},
			},
		},
		// C2: NoCandidateFromOtherQueues short-circuits to one attempt, and
		// same-queue candidates are ordered by priority, lowest first.
		"only same ClusterQueue candidates are offered in a single attempt": {
			clusterQueues: []*kueue.ClusterQueue{makeCQ("a", "", "3").Obj()},
			admitted: []kueue.Workload{
				admittedWl("a1", "a", "1", 0),
				admittedWl("a2", "a", "1", -1),
				admittedWl("a3", "a", "1", -2),
			},
			incoming: incomingWl("1", 10),
			targetCQ: "a",
			wantStrategies: []wantStrategy{
				{Borrowing: true, Targets: []wantTarget{
					{Workload: "/a3", Reason: kueue.InClusterQueueReason, CQ: "a"},
					{Workload: "/a2", Reason: kueue.InClusterQueueReason, CQ: "a"},
					{Workload: "/a1", Reason: kueue.InClusterQueueReason, CQ: "a"},
				}},
			},
		},
		// C3: the incoming workload fits in the preemptor's own quota, so it
		// has a hierarchical advantage over the borrowing ClusterQueue. Such
		// candidates are valid with and without borrowing, hence both
		// attempts list them, borrowing first.
		"hierarchical reclaim candidates are offered with and without borrowing": {
			clusterQueues: []*kueue.ClusterQueue{
				makeCQ("a", "all", "3").Obj(),
				makeCQ("c", "all", "0").Obj(),
			},
			admitted: []kueue.Workload{
				admittedWl("c1", "c", "1", 0),
				admittedWl("c2", "c", "1", 0),
				admittedWl("c3", "c", "1", 0),
			},
			incoming: incomingWl("2", 10),
			targetCQ: "a",
			wantStrategies: []wantStrategy{
				{Borrowing: true, Targets: []wantTarget{
					{Workload: "/c1", Reason: kueue.InCohortReclamationReason, CQ: "c"},
					{Workload: "/c2", Reason: kueue.InCohortReclamationReason, CQ: "c"},
					{Workload: "/c3", Reason: kueue.InCohortReclamationReason, CQ: "c"},
				}},
				{Borrowing: false, Targets: []wantTarget{
					{Workload: "/c1", Reason: kueue.InCohortReclamationReason, CQ: "c"},
					{Workload: "/c2", Reason: kueue.InCohortReclamationReason, CQ: "c"},
					{Workload: "/c3", Reason: kueue.InCohortReclamationReason, CQ: "c"},
				}},
			},
		},
		// C4: BorrowWithinCohort is forbidden and the preemptor is already at
		// its nominal quota, so only the borrowing attempt is planned. The
		// cohort candidates are ReclaimWithoutBorrowing and are therefore
		// filtered out of that attempt, leaving the same-queue candidate.
		"borrowing forbidden and preemptor at nominal plans only the borrowing attempt": {
			clusterQueues: []*kueue.ClusterQueue{
				makeCQ("a", "all", "3").Obj(),
				makeCQ("c", "all", "0").Obj(),
			},
			admitted: []kueue.Workload{
				admittedWl("a1", "a", "3", 0),
				admittedWl("c1", "c", "1", 0),
				admittedWl("c2", "c", "1", 0),
			},
			incoming: incomingWl("1", 10),
			targetCQ: "a",
			wantStrategies: []wantStrategy{
				{Borrowing: true, Targets: []wantTarget{
					{Workload: "/a1", Reason: kueue.InClusterQueueReason, CQ: "a"},
				}},
			},
		},
		// C5: no hierarchical advantage and borrowing within the cohort is
		// forbidden, so the non-borrowing attempt is tried first; the same
		// candidates are invalid once borrowing is allowed.
		"borrowing forbidden with only priority candidates tries the non-borrowing attempt first": {
			clusterQueues: []*kueue.ClusterQueue{
				makeCQ("a", "all", "3").Obj(),
				makeCQ("c", "all", "0").Obj(),
			},
			admitted: []kueue.Workload{
				admittedWl("c1", "c", "1", 0),
				admittedWl("c2", "c", "1", 0),
			},
			incoming: incomingWl("5", 10),
			targetCQ: "a",
			wantStrategies: []wantStrategy{
				{Borrowing: false, Targets: []wantTarget{
					{Workload: "/c1", Reason: kueue.InCohortReclamationReason, CQ: "c"},
					{Workload: "/c2", Reason: kueue.InCohortReclamationReason, CQ: "c"},
				}},
				{Borrowing: true, Targets: []wantTarget{}},
			},
		},
		// C6: with BorrowWithinCohort enabled, candidates above the priority
		// threshold can only be preempted when the preemptor does not borrow,
		// and the reason differs between the two variants.
		"BorrowWithinCohort splits candidates by the priority threshold": {
			clusterQueues: []*kueue.ClusterQueue{
				makeCQ("a", "all", "3").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
							MaxPriorityThreshold: ptr.To[int32](-3),
						},
					}).Obj(),
				makeCQ("c", "all", "0").Obj(),
			},
			admitted: []kueue.Workload{
				admittedWl("c-low", "c", "1", -5),
				admittedWl("c-high", "c", "1", 0),
			},
			incoming: incomingWl("5", 1),
			targetCQ: "a",
			wantStrategies: []wantStrategy{
				{Borrowing: true, Targets: []wantTarget{
					{Workload: "/c-low", Reason: kueue.InCohortReclaimWhileBorrowingReason, CQ: "c"},
				}},
				{Borrowing: false, Targets: []wantTarget{
					{Workload: "/c-low", Reason: kueue.InCohortReclaimWhileBorrowingReason, CQ: "c"},
					{Workload: "/c-high", Reason: kueue.InCohortReclamationReason, CQ: "c"},
				}},
			},
		},
		// C7: a ClusterQueue which stays within its nominal quota is not
		// lending anything to the preemptor, so its workloads are never
		// candidates.
		"candidates in ClusterQueues within nominal quota are never offered": {
			clusterQueues: []*kueue.ClusterQueue{
				makeCQ("a", "all", "3").Obj(),
				makeCQ("b", "all", "3").Obj(),
				makeCQ("c", "all", "0").Obj(),
			},
			admitted: []kueue.Workload{
				admittedWl("b1", "b", "2", 0),
				admittedWl("c1", "c", "1", 0),
			},
			incoming: incomingWl("2", 10),
			targetCQ: "a",
			wantStrategies: []wantStrategy{
				{Borrowing: true, Targets: []wantTarget{
					{Workload: "/c1", Reason: kueue.InCohortReclamationReason, CQ: "c"},
				}},
				{Borrowing: false, Targets: []wantTarget{
					{Workload: "/c1", Reason: kueue.InCohortReclamationReason, CQ: "c"},
				}},
			},
		},
		// C8: a candidate which is already being evicted is offered first,
		// even when another candidate has a lower priority, because
		// preempting it costs nothing.
		"already evicted candidates are offered first": {
			clusterQueues: []*kueue.ClusterQueue{
				makeCQ("a", "all", "3").Obj(),
				makeCQ("c", "all", "0").Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("c-evicted", "").
					Request(corev1.ResourceCPU, "1").
					Priority(0).
					SimpleReserveQuota("c", "default", now).
					EvictedAt(now).
					Obj(),
				admittedWl("c-low", "c", "1", -5),
			},
			incoming: incomingWl("2", 10),
			targetCQ: "a",
			wantStrategies: []wantStrategy{
				{Borrowing: true, Targets: []wantTarget{
					{Workload: "/c-evicted", Reason: kueue.InCohortReclamationReason, CQ: "c"},
					{Workload: "/c-low", Reason: kueue.InCohortReclamationReason, CQ: "c"},
				}},
				{Borrowing: false, Targets: []wantTarget{
					{Workload: "/c-evicted", Reason: kueue.InCohortReclamationReason, CQ: "c"},
					{Workload: "/c-low", Reason: kueue.InCohortReclamationReason, CQ: "c"},
				}},
			},
		},
		// C9: candidates from the cohort are offered before the preemptor's
		// own workloads, so that the plan prefers reclaiming lent quota over
		// preempting a tenant of the same ClusterQueue.
		"cohort candidates precede same ClusterQueue candidates": {
			clusterQueues: []*kueue.ClusterQueue{
				makeCQ("a", "all", "3").Obj(),
				makeCQ("c", "all", "0").Obj(),
			},
			admitted: []kueue.Workload{
				admittedWl("a1", "a", "1", 0),
				admittedWl("c1", "c", "1", 0),
			},
			incoming: incomingWl("5", 5),
			targetCQ: "a",
			wantStrategies: []wantStrategy{
				{Borrowing: false, Targets: []wantTarget{
					{Workload: "/c1", Reason: kueue.InCohortReclamationReason, CQ: "c"},
					{Workload: "/a1", Reason: kueue.InClusterQueueReason, CQ: "a"},
				}},
				{Borrowing: true, Targets: []wantTarget{
					{Workload: "/a1", Reason: kueue.InClusterQueueReason, CQ: "a"},
				}},
			},
		},
		// C10: the incoming workload does not fit in "a", but it does fit in
		// the "left" subtree, so the preemptor has a hierarchical advantage
		// over "c", which borrows quota from "root". It has no such advantage
		// over "b", which borrows within "left".
		"hierarchy candidates precede priority candidates": {
			clusterQueues: []*kueue.ClusterQueue{
				makeCQ("a", "left", "3").Obj(),
				makeCQ("b", "left", "0").Obj(),
				makeCQ("c", "right", "0").Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("left").Parent("root").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").Obj()).Obj(),
				utiltestingapi.MakeCohort("right").Parent("root").Obj(),
				utiltestingapi.MakeCohort("root").Obj(),
			},
			admitted: []kueue.Workload{
				admittedWl("b1", "b", "1", 0),
				admittedWl("c1", "c", "1", 0),
			},
			incoming: incomingWl("5", 10),
			targetCQ: "a",
			wantStrategies: []wantStrategy{
				{Borrowing: true, Targets: []wantTarget{
					{Workload: "/c1", Reason: kueue.InCohortReclamationReason, CQ: "c"},
				}},
				{Borrowing: false, Targets: []wantTarget{
					{Workload: "/c1", Reason: kueue.InCohortReclamationReason, CQ: "c"},
					{Workload: "/b1", Reason: kueue.InCohortReclamationReason, CQ: "b"},
				}},
			},
		},
		// C11: preemption policies set to Never remove every candidate, in
		// the ClusterQueue and in the cohort.
		"preemption policies set to Never produce no candidates": {
			clusterQueues: []*kueue.ClusterQueue{
				makeCQ("a", "all", "3").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyNever,
						ReclaimWithinCohort: kueue.PreemptionPolicyNever,
					}).Obj(),
				makeCQ("c", "all", "0").Obj(),
			},
			admitted: []kueue.Workload{
				admittedWl("a1", "a", "1", -5),
				admittedWl("c1", "c", "1", -5),
			},
			incoming: incomingWl("2", 10),
			targetCQ: "a",
			wantStrategies: []wantStrategy{
				{Borrowing: true, Targets: []wantTarget{}},
			},
		},
		// C12: a candidate which does not use any of the flavor-resources
		// needing preemption cannot free the contested quota.
		"candidates not using the contested flavor are skipped": {
			clusterQueues: []*kueue.ClusterQueue{
				makeCQ("a", "all", "3").Obj(),
				utiltestingapi.MakeClusterQueue("c").
					Cohort("all").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "0").Obj(),
						*utiltestingapi.MakeFlavorQuotas("alternative").
							Resource(corev1.ResourceCPU, "0").Obj(),
					).
					Preemption(basePreemption).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("c-alternative", "").
					Request(corev1.ResourceCPU, "1").
					Priority(0).
					SimpleReserveQuota("c", "alternative", now).
					Obj(),
				admittedWl("c-default", "c", "1", 0),
			},
			incoming: incomingWl("2", 10),
			targetCQ: "a",
			wantStrategies: []wantStrategy{
				{Borrowing: true, Targets: []wantTarget{
					{Workload: "/c-default", Reason: kueue.InCohortReclamationReason, CQ: "c"},
				}},
				{Borrowing: false, Targets: []wantTarget{
					{Workload: "/c-default", Reason: kueue.InCohortReclamationReason, CQ: "c"},
				}},
			},
		},
		// C13: both attempts share a single candidate iterator, which must be
		// reset between them, so a consumer which abandons the first attempt
		// still sees the full sequence in the second one.
		"the second attempt restarts the shared candidate iterator": {
			clusterQueues: []*kueue.ClusterQueue{
				makeCQ("a", "all", "3").Obj(),
				makeCQ("c", "all", "0").Obj(),
			},
			admitted: []kueue.Workload{
				admittedWl("c1", "c", "1", 0),
				admittedWl("c2", "c", "1", 0),
				admittedWl("c3", "c", "1", 0),
			},
			incoming:    incomingWl("2", 10),
			targetCQ:    "a",
			consumption: planConsumption{stopAfterFirstStrategyTargets: 1},
			wantStrategies: []wantStrategy{
				{Borrowing: true, Targets: []wantTarget{
					{Workload: "/c1", Reason: kueue.InCohortReclamationReason, CQ: "c"},
				}},
				{Borrowing: false, Targets: []wantTarget{
					{Workload: "/c1", Reason: kueue.InCohortReclamationReason, CQ: "c"},
					{Workload: "/c2", Reason: kueue.InCohortReclamationReason, CQ: "c"},
					{Workload: "/c3", Reason: kueue.InCohortReclamationReason, CQ: "c"},
				}},
			},
		},
		// C14: a consumer which found its targets in the first attempt stops
		// the plan, and the second attempt is never generated.
		"the plan stops when the consumer stops": {
			clusterQueues: []*kueue.ClusterQueue{
				makeCQ("a", "all", "3").Obj(),
				makeCQ("c", "all", "0").Obj(),
			},
			admitted: []kueue.Workload{
				admittedWl("c1", "c", "1", 0),
			},
			incoming:    incomingWl("2", 10),
			targetCQ:    "a",
			consumption: planConsumption{stopAfterStrategies: 1},
			wantStrategies: []wantStrategy{
				{Borrowing: true, Targets: []wantTarget{
					{Workload: "/c1", Reason: kueue.InCohortReclamationReason, CQ: "c"},
				}},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, log := utiltesting.ContextWithLog(t)
			fixture := newPlanFixture(ctx, t, log, planFixtureCfg{
				flavors:          flavors,
				clusterQueues:    tc.clusterQueues,
				cohorts:          tc.cohorts,
				admitted:         tc.admitted,
				incoming:         tc.incoming,
				targetCQ:         tc.targetCQ,
				assignmentFlavor: tc.assignmentFlavor,
				now:              now,
			})

			plan := ClassicalPreemptionPlan(ctx, fixture.preemptor, fixture.pCtx)
			gotStrategies := consumePlan(plan, tc.consumption)
			if diff := cmp.Diff(tc.wantStrategies, gotStrategies, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected strategies (-want,+got):\n%s", diff)
			}
			// The plan removes every candidate it yields from the snapshot,
			// which is what makes an attempt see the effect of its earlier
			// candidates. Cleanup, which consumePlan calls once an attempt is
			// over, must put all of them back.
			if diff := cmp.Diff(fixture.pristine, fixture.snapshot, snapCmpOpts); diff != "" {
				t.Errorf("Snapshot was not restored after the plan was consumed (-initial,+end):\n%s", diff)
			}
		})
	}
}

func TestFairSharingPreemptionPlan(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	flavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("default").Obj(),
	}
	basePreemption := kueue.ClusterQueuePreemption{
		WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
		ReclaimWithinCohort: kueue.PreemptionPolicyAny,
	}
	makeCQ := func(name string, nominal string) *utiltestingapi.ClusterQueueWrapper {
		return utiltestingapi.MakeClusterQueue(name).
			Cohort("all").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, nominal).Obj()).
			Preemption(basePreemption)
	}
	admittedWl := func(name string, cq kueue.ClusterQueueReference, cpu string, priority int32) kueue.Workload {
		return *utiltestingapi.MakeWorkload(name, "").
			Request(corev1.ResourceCPU, cpu).
			Priority(priority).
			SimpleReserveQuota(cq, "default", now).
			Obj()
	}
	incomingWl := func(cpu string, priority int32) *kueue.Workload {
		return utiltestingapi.MakeWorkload("in", "").
			Request(corev1.ResourceCPU, cpu).
			Priority(priority).
			Obj()
	}

	cases := map[string]struct {
		clusterQueues []*kueue.ClusterQueue
		cohorts       []*kueue.Cohort
		admitted      []kueue.Workload
		incoming      *kueue.Workload
		targetCQ      kueue.ClusterQueueReference
		strategies    []config.PreemptionStrategy
		featureGates  map[featuregate.Feature]bool
		// stopAfterTargets, when positive, abandons the plan after that many
		// targets. A fair sharing plan yields a single strategy, so this is
		// the only way a consumer can leave it early.
		stopAfterTargets int

		wantStrategies []wantStrategy
	}{
		// F1: without candidates the plan is empty, so the caller never even
		// simulates the first strategy.
		"no candidates yields an empty plan": {
			clusterQueues: []*kueue.ClusterQueue{
				makeCQ("a", "3").Obj(),
				makeCQ("b", "3").Obj(),
			},
			incoming:       incomingWl("1", 0),
			targetCQ:       "a",
			wantStrategies: []wantStrategy{},
		},
		// F2: only one configured strategy means rule S2-b is never planned,
		// even though the candidates failed rule S2-a.
		"a single configured strategy yields no targets from the second rule": {
			clusterQueues: []*kueue.ClusterQueue{
				makeCQ("a", "3").Obj(),
				makeCQ("b", "3").Obj(),
			},
			admitted: []kueue.Workload{
				admittedWl("b1", "b", "3", 0),
				admittedWl("b2", "b", "3", 0),
			},
			incoming:   incomingWl("4", 0),
			targetCQ:   "a",
			strategies: []config.PreemptionStrategy{config.LessThanOrEqualToFinalShare},
			wantStrategies: []wantStrategy{
				{Borrowing: true, Targets: []wantTarget{}},
			},
		},
		// F3: candidates from the preemptor's own ClusterQueue are yielded
		// without evaluating any fair sharing strategy.
		"in ClusterQueue candidates are yielded unconditionally": {
			clusterQueues: []*kueue.ClusterQueue{
				makeCQ("a", "3").Obj(),
				makeCQ("b", "3").Obj(),
			},
			admitted: []kueue.Workload{
				admittedWl("a1", "a", "1", -1),
				admittedWl("a2", "a", "1", -1),
			},
			incoming: incomingWl("1", 5),
			targetCQ: "a",
			wantStrategies: []wantStrategy{
				{Borrowing: true, Targets: []wantTarget{
					{Workload: "/a1", Reason: kueue.InClusterQueueReason, CQ: "a"},
					{Workload: "/a2", Reason: kueue.InClusterQueueReason, CQ: "a"},
				}},
			},
		},
		// F4: FairSharingPreemptWithinNominal lets a preemptor which stays
		// within its nominal quota reclaim every borrowed candidate, without
		// consulting the strategies, so rule S2-b has nothing left to do.
		"preemptor within nominal reclaims every candidate": {
			clusterQueues: []*kueue.ClusterQueue{
				makeCQ("a", "3").Obj(),
				makeCQ("preemptible", "0").Obj(),
			},
			admitted: []kueue.Workload{
				admittedWl("p1", "preemptible", "1", 0),
				admittedWl("p2", "preemptible", "1", 0),
			},
			incoming:     incomingWl("3", 0),
			targetCQ:     "a",
			featureGates: map[featuregate.Feature]bool{features.FairSharingPreemptWithinNominal: true},
			wantStrategies: []wantStrategy{
				{Borrowing: true, Targets: []wantTarget{
					{Workload: "/p1", Reason: kueue.InCohortReclamationReason, CQ: "preemptible"},
					{Workload: "/p2", Reason: kueue.InCohortReclamationReason, CQ: "preemptible"},
				}},
			},
		},
		// F5: the same topology with the gate disabled goes through the
		// strategy evaluation, which changes the preemption reason.
		"preemptor within nominal follows the strategy path when the gate is disabled": {
			clusterQueues: []*kueue.ClusterQueue{
				makeCQ("a", "3").Obj(),
				makeCQ("preemptible", "0").Obj(),
			},
			admitted: []kueue.Workload{
				admittedWl("p1", "preemptible", "1", 0),
				admittedWl("p2", "preemptible", "1", 0),
			},
			incoming:     incomingWl("3", 0),
			targetCQ:     "a",
			featureGates: map[featuregate.Feature]bool{features.FairSharingPreemptWithinNominal: false},
			wantStrategies: []wantStrategy{
				{Borrowing: true, Targets: []wantTarget{
					{Workload: "/p1", Reason: kueue.InCohortFairSharingReason, CQ: "preemptible"},
					{Workload: "/p2", Reason: kueue.InCohortFairSharingReason, CQ: "preemptible"},
				}},
			},
		},
		// F6: a borrowing preemptor takes one candidate from the ClusterQueue
		// with the highest DominantResourceShare, and then re-evaluates the
		// ordering, which no longer allows preempting anything.
		"a borrowing preemptor takes from the ClusterQueue with the highest share": {
			clusterQueues: []*kueue.ClusterQueue{
				makeCQ("a", "3").Obj(),
				makeCQ("b", "3").Obj(),
				makeCQ("c", "3").Obj(),
			},
			admitted: []kueue.Workload{
				admittedWl("b1", "b", "1", 0),
				admittedWl("b2", "b", "1", 0),
				admittedWl("b3", "b", "1", 0),
				admittedWl("b4", "b", "1", 0),
				admittedWl("b5", "b", "1", 0),
				admittedWl("c1", "c", "1", 0),
				admittedWl("c2", "c", "1", 0),
				admittedWl("c3", "c", "1", 0),
				admittedWl("c4", "c", "1", 0),
			},
			incoming: incomingWl("4", 0),
			targetCQ: "a",
			wantStrategies: []wantStrategy{
				{Borrowing: true, Targets: []wantTarget{
					{Workload: "/b1", Reason: kueue.InCohortFairSharingReason, CQ: "b"},
				}},
			},
		},
		// F7: every candidate is big enough to push the target below the
		// preemptor's share, so rule S2-a rejects all of them and rule S2-b
		// picks a single candidate per ClusterQueue.
		"candidates rejected by the first strategy are retried by the second": {
			clusterQueues: []*kueue.ClusterQueue{
				makeCQ("a", "3").Obj(),
				makeCQ("b", "3").Obj(),
			},
			admitted: []kueue.Workload{
				admittedWl("b1", "b", "3", 0),
				admittedWl("b2", "b", "3", 0),
			},
			incoming: incomingWl("4", 0),
			targetCQ: "a",
			wantStrategies: []wantStrategy{
				{Borrowing: true, Targets: []wantTarget{
					{Workload: "/b1", Reason: kueue.InCohortFairSharingReason, CQ: "b"},
				}},
			},
		},
		// F8: a borrowing preemptor with a zero fair weight has an infinite
		// share, so no candidate can win either strategy. The tournament is
		// skipped without simulating the candidates, and the iteration still
		// terminates.
		"an unsatisfiable tournament yields nothing and terminates": {
			clusterQueues: []*kueue.ClusterQueue{
				makeCQ("a", "3").FairWeight(resource.MustParse("0")).Obj(),
				makeCQ("b", "3").Obj(),
			},
			admitted: []kueue.Workload{
				admittedWl("b1", "b", "1", 0),
				admittedWl("b2", "b", "1", 0),
				admittedWl("b3", "b", "1", 0),
				admittedWl("b4", "b", "1", 0),
			},
			incoming: incomingWl("4", 0),
			targetCQ: "a",
			wantStrategies: []wantStrategy{
				{Borrowing: true, Targets: []wantTarget{}},
			},
		},
		// F9: preempting within the preemptor ClusterQueue frees its own
		// quota, so FairSharingReevaluatePreemptionCandidates plans a second
		// pass over the candidates rejected by the first one. Here the three
		// in-ClusterQueue preemptions bring the preemptor back within its
		// nominal quota, so the second pass takes the
		// FairSharingPreemptWithinNominal shortcut and reclaims the remaining
		// candidates instead of putting them through the strategies.
		"the reevaluation gate adds a pass when a target came from the preemptor ClusterQueue": {
			clusterQueues: []*kueue.ClusterQueue{
				makeCQ("a", "3").Obj(),
				makeCQ("b", "0").Obj(),
				makeCQ("c", "3").Obj(),
			},
			admitted: []kueue.Workload{
				admittedWl("a1", "a", "1", -1),
				admittedWl("a2", "a", "1", -1),
				admittedWl("a3", "a", "1", -1),
				admittedWl("b1", "b", "1", 0),
				admittedWl("b2", "b", "1", 0),
			},
			incoming:     incomingWl("3", 5),
			targetCQ:     "a",
			featureGates: map[featuregate.Feature]bool{features.FairSharingReevaluatePreemptionCandidates: true},
			wantStrategies: []wantStrategy{
				{Borrowing: true, Targets: []wantTarget{
					{Workload: "/a1", Reason: kueue.InClusterQueueReason, CQ: "a"},
					{Workload: "/a2", Reason: kueue.InClusterQueueReason, CQ: "a"},
					{Workload: "/a3", Reason: kueue.InClusterQueueReason, CQ: "a"},
					{Workload: "/b1", Reason: kueue.InCohortReclamationReason, CQ: "b"},
					{Workload: "/b2", Reason: kueue.InCohortReclamationReason, CQ: "b"},
				}},
			},
		},
		// F9b: the same gate, but the preemptor still borrows after the
		// in-ClusterQueue preemptions. Its share dropped below the target's
		// though, so candidates which lost the first tournament now win it.
		"the reevaluation pass re-runs the strategy for a still borrowing preemptor": {
			clusterQueues: []*kueue.ClusterQueue{
				makeCQ("a", "3").Obj(),
				makeCQ("b", "0").Obj(),
			},
			admitted: []kueue.Workload{
				admittedWl("a1", "a", "1", -1),
				admittedWl("a2", "a", "1", -1),
				admittedWl("b1", "b", "1", 0),
				admittedWl("b2", "b", "1", 0),
				admittedWl("b3", "b", "1", 0),
			},
			incoming:     incomingWl("4", 5),
			targetCQ:     "a",
			featureGates: map[featuregate.Feature]bool{features.FairSharingReevaluatePreemptionCandidates: true},
			wantStrategies: []wantStrategy{
				{Borrowing: true, Targets: []wantTarget{
					{Workload: "/a1", Reason: kueue.InClusterQueueReason, CQ: "a"},
					{Workload: "/a2", Reason: kueue.InClusterQueueReason, CQ: "a"},
					{Workload: "/b1", Reason: kueue.InCohortFairSharingReason, CQ: "b"},
					{Workload: "/b2", Reason: kueue.InCohortFairSharingReason, CQ: "b"},
				}},
			},
		},
		// F10: without a target in the preemptor ClusterQueue there is
		// nothing to reevaluate, so the gate does not add a pass.
		"the reevaluation gate adds nothing without a target in the preemptor ClusterQueue": {
			clusterQueues: []*kueue.ClusterQueue{
				makeCQ("a", "3").Obj(),
				makeCQ("b", "0").Obj(),
			},
			admitted: []kueue.Workload{
				admittedWl("b1", "b", "1", 0),
				admittedWl("b2", "b", "1", 0),
			},
			incoming:     incomingWl("3", 5),
			targetCQ:     "a",
			featureGates: map[featuregate.Feature]bool{features.FairSharingReevaluatePreemptionCandidates: true},
			wantStrategies: []wantStrategy{
				{Borrowing: true, Targets: []wantTarget{
					{Workload: "/b1", Reason: kueue.InCohortReclamationReason, CQ: "b"},
					{Workload: "/b2", Reason: kueue.InCohortReclamationReason, CQ: "b"},
				}},
			},
		},
		// F11: candidates are sorted before the ordering is built, so within
		// a ClusterQueue the lowest priority is offered first, and equal
		// priorities are broken by the most recent admission.
		"candidates within a ClusterQueue follow the candidates ordering": {
			clusterQueues: []*kueue.ClusterQueue{
				makeCQ("a", "3").Obj(),
				makeCQ("b", "0").Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("b-old", "").
					Request(corev1.ResourceCPU, "1").Priority(0).
					SimpleReserveQuota("b", "default", now.Add(-time.Hour)).Obj(),
				*utiltestingapi.MakeWorkload("b-new", "").
					Request(corev1.ResourceCPU, "1").Priority(0).
					SimpleReserveQuota("b", "default", now).Obj(),
				admittedWl("b-low", "b", "1", -5),
			},
			incoming: incomingWl("3", 5),
			targetCQ: "a",
			wantStrategies: []wantStrategy{
				{Borrowing: true, Targets: []wantTarget{
					{Workload: "/b-low", Reason: kueue.InCohortReclamationReason, CQ: "b"},
					{Workload: "/b-new", Reason: kueue.InCohortReclamationReason, CQ: "b"},
					{Workload: "/b-old", Reason: kueue.InCohortReclamationReason, CQ: "b"},
				}},
			},
		},
		// F12: a consumer which found enough targets abandons the plan, and
		// the remaining candidates are never generated.
		"the plan stops when the consumer stops": {
			clusterQueues: []*kueue.ClusterQueue{
				makeCQ("a", "3").Obj(),
				makeCQ("b", "0").Obj(),
			},
			admitted: []kueue.Workload{
				admittedWl("b1", "b", "1", 0),
				admittedWl("b2", "b", "1", 0),
			},
			incoming:         incomingWl("3", 5),
			targetCQ:         "a",
			stopAfterTargets: 1,
			wantStrategies: []wantStrategy{
				{Borrowing: true, Targets: []wantTarget{
					{Workload: "/b1", Reason: kueue.InCohortReclamationReason, CQ: "b"},
				}},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, log := utiltesting.ContextWithLog(t)
			fixture := newPlanFixture(ctx, t, log, planFixtureCfg{
				flavors:       flavors,
				clusterQueues: tc.clusterQueues,
				cohorts:       tc.cohorts,
				admitted:      tc.admitted,
				incoming:      tc.incoming,
				targetCQ:      tc.targetCQ,
				fairSharing:   &config.FairSharing{PreemptionStrategies: tc.strategies},
				now:           now,
			})

			plan := FairPreemptionPlan(ctx, fixture.preemptor, fixture.pCtx, fixture.preemptor.fsStrategies)
			// The plan simulates the incoming workload's usage itself, so that
			// the shares account for it while the strategies are evaluated.
			// The plan yields a single strategy, so capping the first one
			// caps the whole plan.
			gotStrategies := consumePlan(plan, planConsumption{stopAfterFirstStrategyTargets: tc.stopAfterTargets})

			if diff := cmp.Diff(tc.wantStrategies, gotStrategies, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected strategies (-want,+got):\n%s", diff)
			}

			// A fair sharing plan removes the workloads it yields from the
			// snapshot, and nothing else; the Cleanup consumePlan ran must
			// have added all of them back.
			if diff := cmp.Diff(fixture.pristine, fixture.snapshot, snapCmpOpts); diff != "" {
				t.Errorf("Snapshot was modified beyond the yielded targets (-initial,+end):\n%s", diff)
			}
		})
	}
}
