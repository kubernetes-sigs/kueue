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
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/util/routine"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

// TestScheduleForFairSharingRefill exercises the FairSharingRefill feature
// gate: when a workload is admitted, its ClusterQueue's next workload joins
// the running scheduling cycle instead of waiting for the next one.
//
// The shared fixture is the shape of issue #9345: ClusterQueues refill-poor
// (nominal 8) and refill-rich (nominal 2) share cohort refill, and refill-rich
// already borrows well beyond its nominal quota, so its DRS is positive while
// refill-poor's DRS stays 0 for the workloads at hand. The rich pending
// workload is the oldest, which makes the first case's outcome depend on DRS
// ordering rather than FIFO; the budget-focused cases have FIFO-equivalent
// terminal states by design, and the "consecutive refills" case pins the
// per-pop reranking directly.
func TestScheduleForFairSharingRefill(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(now)

	resourceFlavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("default").Obj(),
	}
	clusterQueues := []kueue.ClusterQueue{
		*utiltestingapi.MakeClusterQueue("refill-poor").
			Cohort("refill").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "8", "0").Obj()).
			Obj(),
		*utiltestingapi.MakeClusterQueue("refill-rich").
			Cohort("refill").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "2", "8").Obj()).
			Obj(),
	}
	queues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("poor-lq", "default").ClusterQueue("refill-poor").Obj(),
		*utiltestingapi.MakeLocalQueue("rich-lq", "default").ClusterQueue("refill-rich").Obj(),
	}

	// richActive occupies rich capacity so refill-rich borrows `borrowed` CPU
	// beyond its nominal quota of 2, leaving 10-count CPU free in the cohort.
	richActive := func(count int, quantity string) *utiltestingapi.WorkloadWrapper {
		return utiltestingapi.MakeWorkload("rich-active", "default").
			Queue("rich-lq").
			PodSets(*utiltestingapi.MakePodSet("one", count).
				Request(corev1.ResourceCPU, "1").
				Obj()).
			ReserveQuotaAt(utiltestingapi.MakeAdmission("refill-rich").PodSets(
				utiltestingapi.MakePodSetAssignment("one").
					Assignment(corev1.ResourceCPU, "default", quantity).Count(int32(count)).Obj(),
			).Obj(), now)
	}
	pendingWl := func(name, lq string, creation time.Time) *utiltestingapi.WorkloadWrapper {
		return utiltestingapi.MakeWorkload(name, "default").
			Queue(kueue.LocalQueueName(lq)).
			Creation(creation).
			PodSets(*utiltestingapi.MakePodSet("one", 1).
				Request(corev1.ResourceCPU, "1").
				Obj())
	}
	singleCPUAdmission := func(cq kueue.ClusterQueueReference) *kueue.Admission {
		return utiltestingapi.MakeAdmission(cq).PodSets(
			utiltestingapi.MakePodSetAssignment("one").
				Assignment(corev1.ResourceCPU, "default", "1").Count(1).Obj(),
		).Obj()
	}
	admittedWl := func(w *utiltestingapi.WorkloadWrapper, cq kueue.ClusterQueueReference) *utiltestingapi.WorkloadWrapper {
		return w.Clone().
			Condition(metav1.Condition{
				Type:               kueue.WorkloadQuotaReserved,
				Status:             metav1.ConditionTrue,
				Reason:             "QuotaReserved",
				Message:            "Quota reserved in ClusterQueue " + string(cq),
				LastTransitionTime: metav1.NewTime(now),
			}).
			Condition(metav1.Condition{
				Type:               kueue.WorkloadAdmitted,
				Status:             metav1.ConditionTrue,
				Reason:             "Admitted",
				Message:            "The workload is admitted",
				LastTransitionTime: metav1.NewTime(now),
			}).
			Admission(singleCPUAdmission(cq))
	}
	unadmittedWl := func(w *utiltestingapi.WorkloadWrapper, reason, message, cpu string) *utiltestingapi.WorkloadWrapper {
		return w.Clone().
			Condition(metav1.Condition{
				Type:               kueue.WorkloadQuotaReserved,
				Status:             metav1.ConditionFalse,
				Reason:             reason,
				Message:            message,
				LastTransitionTime: metav1.NewTime(now),
			}).
			Condition(metav1.Condition{
				Type:               kueue.WorkloadAdmitted,
				Status:             metav1.ConditionFalse,
				Reason:             kueue.WorkloadAdmittedReasonNoReservation,
				Message:            "The workload has no reservation",
				LastTransitionTime: metav1.NewTime(now),
			}).
			ResourceRequests(kueue.PodSetRequest{
				Name: "one",
				Resources: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse(cpu),
				},
			})
	}
	skippedWl := func(w *utiltestingapi.WorkloadWrapper) *utiltestingapi.WorkloadWrapper {
		return unadmittedWl(w, kueue.WorkloadQuotaReservedReasonWaitingForQuota,
			"Workload no longer fits after processing another workload", "1")
	}

	poorA := pendingWl("poor-a", "poor-lq", now.Add(-2*time.Minute))
	poorB := pendingWl("poor-b", "poor-lq", now.Add(-time.Minute))
	poorC := pendingWl("poor-c", "poor-lq", now.Add(-30*time.Second))
	richPending := pendingWl("rich-pending", "rich-lq", now.Add(-3*time.Minute))
	richNext := pendingWl("rich-next", "rich-lq", now.Add(-30*time.Second))
	poorBig := utiltestingapi.MakeWorkload("poor-big", "default").
		Queue("poor-lq").
		Creation(now.Add(-time.Minute)).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			Request(corev1.ResourceCPU, "10").
			Obj())
	poorRetry := pendingWl("poor-retry", "poor-lq", now.Add(-time.Minute)).
		AdmissionCheck(kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStateRetry})

	cases := map[string]scheduleTestCase{
		// Two CPUs are free. The poorest CQ's head wins the first admission;
		// refill immediately brings its next workload into the cycle, which
		// wins the second admission on DRS, so the over-share CQ gets nothing.
		"refill admits the poor ClusterQueue's backlog within one cycle": {
			enableFairSharing: true,
			featureGates:      map[featuregate.Feature]bool{features.FairSharingRefill: true},
			workloads: []kueue.Workload{
				*richActive(8, "8").Obj(),
				*poorA.Clone().Obj(),
				*poorB.Clone().Obj(),
				*richPending.Clone().Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*admittedWl(poorA, "refill-poor").Obj(),
				*admittedWl(poorB, "refill-poor").Obj(),
				*richActive(8, "8").Obj(),
				*skippedWl(richPending).Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/rich-active": *utiltestingapi.MakeAdmission("refill-rich").PodSets(
					utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "default", "8").Count(8).Obj(),
				).Obj(),
				"default/poor-a": *singleCPUAdmission("refill-poor"),
				"default/poor-b": *singleCPUAdmission("refill-poor"),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"refill-rich": {"default/rich-pending"},
			},
		},
		// Same fixture with the gate off: the poor CQ only has its single
		// head in the room, so after that head is admitted the over-share CQ
		// picks up the remaining capacity -- the issue #9345 shape.
		"without refill the over-share ClusterQueue takes the freed capacity": {
			enableFairSharing: true,
			featureGates:      map[featuregate.Feature]bool{features.FairSharingRefill: false},
			workloads: []kueue.Workload{
				*richActive(8, "8").Obj(),
				*poorA.Clone().Obj(),
				*poorB.Clone().Obj(),
				*richPending.Clone().Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*admittedWl(poorA, "refill-poor").Obj(),
				*poorB.Clone().Obj(),
				*richActive(8, "8").Obj(),
				*admittedWl(richPending, "refill-rich").Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/rich-active": *utiltestingapi.MakeAdmission("refill-rich").PodSets(
					utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "default", "8").Count(8).Obj(),
				).Obj(),
				"default/poor-a":       *singleCPUAdmission("refill-poor"),
				"default/rich-pending": *singleCPUAdmission("refill-rich"),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"refill-poor": {"default/poor-b"},
			},
		},
		// Three CPUs are free and the budget allows one extra pop. The poor
		// CQ admits its head plus one refilled workload; once the budget is
		// exhausted its next workload stays in the heap (it is never popped),
		// and the over-share CQ's head takes the remaining capacity.
		"budget exhaustion stops refilling and the cycle continues": {
			enableFairSharing: true,
			featureGates:      map[featuregate.Feature]bool{features.FairSharingRefill: true},
			refillBudget:      new(1),
			workloads: []kueue.Workload{
				*richActive(7, "7").Obj(),
				*poorA.Clone().Obj(),
				*poorB.Clone().Obj(),
				*poorC.Clone().Obj(),
				*richPending.Clone().Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*admittedWl(poorA, "refill-poor").Obj(),
				*admittedWl(poorB, "refill-poor").Obj(),
				*poorC.Clone().Obj(),
				*richActive(7, "7").Obj(),
				*admittedWl(richPending, "refill-rich").Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/rich-active": *utiltestingapi.MakeAdmission("refill-rich").PodSets(
					utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "default", "7").Count(7).Obj(),
				).Obj(),
				"default/poor-a":       *singleCPUAdmission("refill-poor"),
				"default/poor-b":       *singleCPUAdmission("refill-poor"),
				"default/rich-pending": *singleCPUAdmission("refill-rich"),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"refill-poor": {"default/poor-c"},
			},
		},
		// Two CPUs are free and the budget allows one extra pop. The refilled
		// workload needs ten CPUs -- more than its ClusterQueue could ever
		// get -- so its nomination is NoFit and it is requeued as
		// inadmissible. Its pop already consumed the budget, so the
		// over-share CQ's later admission cannot refill either: rich-next
		// stays in the heap, never popped.
		"a refilled workload that no longer fits consumes budget and is requeued": {
			enableFairSharing: true,
			featureGates:      map[featuregate.Feature]bool{features.FairSharingRefill: true},
			refillBudget:      new(1),
			workloads: []kueue.Workload{
				*richActive(8, "8").Obj(),
				*poorA.Clone().Obj(),
				*poorBig.Clone().Obj(),
				*richPending.Clone().Obj(),
				*richNext.Clone().Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*admittedWl(poorA, "refill-poor").Obj(),
				*unadmittedWl(poorBig, kueue.WorkloadQuotaReservedReasonExceedsMaxQuota,
					"couldn't assign flavors to pod set one: insufficient quota for cpu in flavor default, previously considered podsets requests (0) + current podset request (10) > maximum capacity (8)", "10").Obj(),
				*richActive(8, "8").Obj(),
				*richNext.Clone().Obj(),
				*admittedWl(richPending, "refill-rich").Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/rich-active": *utiltestingapi.MakeAdmission("refill-rich").PodSets(
					utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "default", "8").Count(8).Obj(),
				).Obj(),
				"default/poor-a":       *singleCPUAdmission("refill-poor"),
				"default/rich-pending": *singleCPUAdmission("refill-rich"),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"refill-rich": {"default/rich-next"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"refill-poor": {"default/poor-big"},
			},
		},
		// The refilled workload fails nomination (retrying admission checks),
		// so it never joins the room and must still reach the requeue path.
		"a refilled workload that fails nomination is requeued as inadmissible": {
			enableFairSharing: true,
			featureGates:      map[featuregate.Feature]bool{features.FairSharingRefill: true},
			workloads: []kueue.Workload{
				*richActive(8, "8").Obj(),
				*poorA.Clone().Obj(),
				*poorRetry.Clone().Obj(),
				*richPending.Clone().Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*admittedWl(poorA, "refill-poor").Obj(),
				*unadmittedWl(poorRetry, kueue.WorkloadQuotaReservedReasonPendingEvaluation,
					"The workload has failed admission checks", "1").Obj(),
				*richActive(8, "8").Obj(),
				*admittedWl(richPending, "refill-rich").Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/rich-active": *utiltestingapi.MakeAdmission("refill-rich").PodSets(
					utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "default", "8").Count(8).Obj(),
				).Obj(),
				"default/poor-a":       *singleCPUAdmission("refill-poor"),
				"default/rich-pending": *singleCPUAdmission("refill-rich"),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"refill-poor": {"default/poor-retry"},
			},
		},
		// Chain of two refills from the same CQ, where the per-pop reranking
		// is decisive: after chain-a1 is admitted, refill-a's next workload
		// competes while borrowing, so chain-b1 (within nominal) wins the
		// second admission even though it is the newest workload. A ranking
		// computed once at cycle start would keep refill-a at zero borrowing
		// and let chain-a2 and chain-a3 win on FIFO instead.
		"consecutive refills rerank against fresh usage on every pop": {
			enableFairSharing: true,
			featureGates:      map[featuregate.Feature]bool{features.FairSharingRefill: true},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("refill-a").
					Cohort("refill-chain").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1", "8").Obj()).
					Obj(),
				*utiltestingapi.MakeClusterQueue("refill-b").
					Cohort("refill-chain").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2", "0").Obj()).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("a-lq", "default").ClusterQueue("refill-a").Obj(),
				*utiltestingapi.MakeLocalQueue("b-lq", "default").ClusterQueue("refill-b").Obj(),
			},
			workloads: []kueue.Workload{
				*pendingWl("chain-a1", "a-lq", now.Add(-4*time.Minute)).Obj(),
				*pendingWl("chain-a2", "a-lq", now.Add(-3*time.Minute)).Obj(),
				*pendingWl("chain-a3", "a-lq", now.Add(-2*time.Minute)).Obj(),
				*pendingWl("chain-b1", "b-lq", now.Add(-time.Minute)).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*admittedWl(pendingWl("chain-a1", "a-lq", now.Add(-4*time.Minute)), "refill-a").Obj(),
				*admittedWl(pendingWl("chain-a2", "a-lq", now.Add(-3*time.Minute)), "refill-a").Obj(),
				*unadmittedWl(pendingWl("chain-a3", "a-lq", now.Add(-2*time.Minute)),
					kueue.WorkloadQuotaReservedReasonWaitingForQuota,
					"couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor default, 1 more needed", "1").Obj(),
				*admittedWl(pendingWl("chain-b1", "b-lq", now.Add(-time.Minute)), "refill-b").Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/chain-a1": *singleCPUAdmission("refill-a"),
				"default/chain-a2": *singleCPUAdmission("refill-a"),
				"default/chain-b1": *singleCPUAdmission("refill-b"),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"refill-a": {"default/chain-a3"},
			},
		},
		// Budget zero with the gate on must behave exactly like the gate off.
		"budget zero disables refill while the gate is on": {
			enableFairSharing: true,
			featureGates:      map[featuregate.Feature]bool{features.FairSharingRefill: true},
			refillBudget:      new(0),
			workloads: []kueue.Workload{
				*richActive(8, "8").Obj(),
				*poorA.Clone().Obj(),
				*poorB.Clone().Obj(),
				*richPending.Clone().Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*admittedWl(poorA, "refill-poor").Obj(),
				*poorB.Clone().Obj(),
				*richActive(8, "8").Obj(),
				*admittedWl(richPending, "refill-rich").Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/rich-active": *utiltestingapi.MakeAdmission("refill-rich").PodSets(
					utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "default", "8").Count(8).Obj(),
				).Obj(),
				"default/poor-a":       *singleCPUAdmission("refill-poor"),
				"default/rich-pending": *singleCPUAdmission("refill-rich"),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"refill-poor": {"default/poor-b"},
			},
		},
	}
	runScheduleTestCases(t, scheduleTestConfig{
		queues:          queues,
		clusterQueues:   clusterQueues,
		resourceFlavors: resourceFlavors,
		fakeClock:       fakeClock,
	}, cases)
}

// TestRefillReleasesWorkloadAlreadyAccountedInCache covers the one exit where a
// refilled workload leaves the cycle without being requeued or deleted: its
// nomination is dropped because the scheduler cache already accounts for it.
// The queue's copy is stale in that case, which is reachable whenever a
// workload is admitted between the cycle's snapshot and the refill pop.
//
// This cannot be expressed as a scheduleTestCase because the harness derives
// the queues and the scheduler cache from one workload list, so no workload can
// be pending in the queue and admitted in the cache at the same time.
func TestRefillReleasesWorkloadAlreadyAccountedInCache(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{features.FairSharingRefill: true})

	now := time.Now().Truncate(time.Second)
	flavor := utiltestingapi.MakeResourceFlavor("default").Obj()
	cq := utiltestingapi.MakeClusterQueue("refill-poor").
		Cohort("refill").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
			Resource(corev1.ResourceCPU, "8", "0").Obj()).
		Obj()
	lq := utiltestingapi.MakeLocalQueue("poor-lq", "default").ClusterQueue("refill-poor").Obj()

	pending := func(name string, creation time.Time) *utiltestingapi.WorkloadWrapper {
		return utiltestingapi.MakeWorkload(name, "default").
			Queue("poor-lq").
			Creation(creation).
			PodSets(*utiltestingapi.MakePodSet("one", 1).
				Request(corev1.ResourceCPU, "1").
				Obj())
	}
	// head is admitted by the cycle, which triggers the refill pop of stale.
	head := pending("head", now.Add(-2*time.Minute)).Obj()
	staleWrapper := pending("stale", now.Add(-time.Minute))
	stale := staleWrapper.Obj()

	cl := utiltesting.NewClientBuilder().
		WithLists(
			&kueue.WorkloadList{Items: []kueue.Workload{*head, *stale}},
			&kueue.LocalQueueList{Items: []kueue.LocalQueue{*lq}},
		).
		WithObjects(utiltesting.MakeNamespaceWrapper("default").Obj()).
		WithStatusSubresource(&kueue.Workload{}).
		Build()

	cqCache := schdcache.New(cl)
	qManager := qcache.NewManagerForUnitTests(cl, cqCache,
		qcache.WithPreemptionExpectations(preemptexpectations.New()))
	cqCache.AddOrUpdateResourceFlavor(log, flavor)
	if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Inserting clusterQueue in cache: %v", err)
	}
	if err := qManager.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Inserting clusterQueue in manager: %v", err)
	}
	if err := qManager.AddLocalQueue(ctx, lq); err != nil {
		t.Fatalf("Inserting localQueue in manager: %v", err)
	}

	// The queue keeps the pending copy it was loaded with, which is the state
	// a refill pop can observe.
	admittedStale := staleWrapper.Clone().
		ReserveQuotaAt(utiltestingapi.MakeAdmission("refill-poor").PodSets(
			utiltestingapi.MakePodSetAssignment("one").
				Assignment(corev1.ResourceCPU, "default", "1").Count(1).Obj(),
		).Obj(), now).
		Obj()
	if !cqCache.AddOrUpdateWorkload(log, admittedStale) {
		t.Fatal("Failed to account the workload in the scheduler cache")
	}
	if !cqCache.IsAdded(*workload.NewInfo(admittedStale)) {
		t.Fatal("The workload is not accounted in the scheduler cache")
	}

	scheduler := New(qManager, cqCache, cl, &utiltesting.EventRecorder{},
		WithFairSharing(&config.FairSharing{}),
		WithClock(t, testingclock.NewFakeClock(now)),
		WithPreemptionExpectations(preemptexpectations.New()),
	)
	wg := sync.WaitGroup{}
	scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
		func() { wg.Add(1) },
		func() { wg.Done() },
	))
	ctx, cancel := context.WithTimeout(ctx, queueingTimeout)
	defer cancel()
	go qManager.CleanUpOnContext(ctx)

	scheduler.schedule(ctx)
	wg.Wait()

	// Dropped means neither requeued nor deleted, so nothing returns to the heap.
	if got := qManager.Dump()["refill-poor"]; len(got) != 0 {
		t.Errorf("Workloads left on the heap after the dropped nomination: %v", got)
	}

	// The workload controller re-adds the workload once it is pending again.
	// A claim left behind by the drop would make this a no-op.
	if err := qManager.AddOrUpdateWorkload(log, stale.DeepCopy()); err != nil {
		t.Fatalf("Re-adding the workload: %v", err)
	}
	want := []workload.Reference{"default/stale"}
	if diff := cmp.Diff(want, qManager.Dump()["refill-poor"], cmpDump); diff != "" {
		t.Errorf("The workload did not return to the queue; the drop kept its inflight claim (-want,+got):\n%s", diff)
	}
}
