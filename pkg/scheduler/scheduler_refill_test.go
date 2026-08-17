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
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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
	// refill-rich can borrow, so a request above its nominal quota but within
	// its borrowing limit is short of used capacity, not of quota.
	richBorrow := utiltestingapi.MakeWorkload("rich-borrow", "default").
		Queue("rich-lq").
		Creation(now.Add(-30 * time.Second)).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			Request(corev1.ResourceCPU, "5").
			Obj())

	// Preemption-case fixture: high-priority prio-head can only enter by
	// preempting the low-priority prio-victim; prio-next waits behind it.
	prioVictim := utiltestingapi.MakeWorkload("prio-victim", "default").
		Queue("prio-lq").
		Priority(0).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			Request(corev1.ResourceCPU, "2").
			Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("refill-prio").PodSets(
			utiltestingapi.MakePodSetAssignment("one").
				Assignment(corev1.ResourceCPU, "default", "2").Count(1).Obj(),
		).Obj(), now)
	prioHead := utiltestingapi.MakeWorkload("prio-head", "default").
		Queue("prio-lq").
		UID("wl-prio-head").
		JobUID("job-prio-head").
		Priority(100).
		Creation(now.Add(-2 * time.Minute)).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			Request(corev1.ResourceCPU, "2").
			Obj())
	prioNext := utiltestingapi.MakeWorkload("prio-next", "default").
		Queue("prio-lq").
		Priority(100).
		Creation(now.Add(-time.Minute)).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			Request(corev1.ResourceCPU, "1").
			Obj())

	// Budget-case fixture: bgt-solo admits with an empty backlog, then the
	// bgt-poor pair is the refill that must still happen.
	bgtSolo := pendingWl("bgt-solo", "bgt-solo-lq", now.Add(-4*time.Minute))
	bgtPoorA := pendingWl("bgt-poor-a", "bgt-poor-lq", now.Add(-3*time.Minute))
	bgtPoorB := pendingWl("bgt-poor-b", "bgt-poor-lq", now.Add(-2*time.Minute))

	// Fit-only-rule reservation fixture (resv-*): resv-rich borrows all the
	// cohort's spare CPU except one, so resv-blocked's head cannot fit, has no
	// reclaim candidates (ReclaimWithinCohort defaults to Never), and reserves
	// its request mid-cycle. resv-work admits its head on memory, and the
	// refilled resv-work-b finds the single free CPU consumed by that
	// reservation. The scenario is load-bearing on the DRS=0 FIFO tie:
	// resv-blocked-head must be processed (and reserve) before resv-work's
	// head admits and triggers the refill pop.
	resvClusterQueues := []kueue.ClusterQueue{
		*utiltestingapi.MakeClusterQueue("resv-blocked").
			Cohort("resv").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "3", "0").Obj()).
			Obj(),
		*utiltestingapi.MakeClusterQueue("resv-work").
			Cohort("resv").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "2", "0").
				Resource(corev1.ResourceMemory, "2Gi", "0").Obj()).
			Obj(),
		*utiltestingapi.MakeClusterQueue("resv-rich").
			Cohort("resv").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "0", "4").Obj()).
			Obj(),
	}
	resvLocalQueues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("resv-blocked-lq", "default").ClusterQueue("resv-blocked").Obj(),
		*utiltestingapi.MakeLocalQueue("resv-work-lq", "default").ClusterQueue("resv-work").Obj(),
		*utiltestingapi.MakeLocalQueue("resv-rich-lq", "default").ClusterQueue("resv-rich").Obj(),
	}
	resvRichActive := utiltestingapi.MakeWorkload("resv-rich-active", "default").
		Queue("resv-rich-lq").
		PodSets(*utiltestingapi.MakePodSet("one", 4).
			Request(corev1.ResourceCPU, "1").
			Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("resv-rich").PodSets(
			utiltestingapi.MakePodSetAssignment("one").
				Assignment(corev1.ResourceCPU, "default", "4").Count(4).Obj(),
		).Obj(), now)
	resvBlockedHead := utiltestingapi.MakeWorkload("resv-blocked-head", "default").
		Queue("resv-blocked-lq").
		Creation(now.Add(-4 * time.Minute)).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			Request(corev1.ResourceCPU, "2").
			Obj())
	resvWorkA := utiltestingapi.MakeWorkload("resv-work-a", "default").
		Queue("resv-work-lq").
		Creation(now.Add(-3 * time.Minute)).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			Request(corev1.ResourceMemory, "1Gi").
			Obj())
	resvWorkB := utiltestingapi.MakeWorkload("resv-work-b", "default").
		Queue("resv-work-lq").
		Creation(now.Add(-2 * time.Minute)).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			Request(corev1.ResourceCPU, "1").
			Obj())
	resvWorkAAdmission := utiltestingapi.MakeAdmission("resv-work").PodSets(
		utiltestingapi.MakePodSetAssignment("one").
			Assignment(corev1.ResourceMemory, "default", "1Gi").Count(1).Obj(),
	).Obj()
	resvWorkAAdmitted := resvWorkA.Clone().
		Condition(metav1.Condition{
			Type:               kueue.WorkloadQuotaReserved,
			Status:             metav1.ConditionTrue,
			Reason:             "QuotaReserved",
			Message:            "Quota reserved in ClusterQueue resv-work",
			LastTransitionTime: metav1.NewTime(now),
		}).
		Condition(metav1.Condition{
			Type:               kueue.WorkloadAdmitted,
			Status:             metav1.ConditionTrue,
			Reason:             "Admitted",
			Message:            "The workload is admitted",
			LastTransitionTime: metav1.NewTime(now),
		}).
		Admission(resvWorkAAdmission)

	// DeferredFit fixture (dfit-*): dfit-work admits its head and refills
	// dfit-work-b, whose nomination targets the borrowing dfit-victim-active;
	// dfit-preempt's head preempts that victim first, so dfit-work-b's
	// recomputed assignment lands on DeferredFit.
	dfitClusterQueues := []kueue.ClusterQueue{
		*utiltestingapi.MakeClusterQueue("dfit-preempt").
			Cohort("dfit").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "2", "0").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			Obj(),
		*utiltestingapi.MakeClusterQueue("dfit-work").
			Cohort("dfit").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "4", "0").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			Obj(),
		*utiltestingapi.MakeClusterQueue("dfit-victim").
			Cohort("dfit").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "0", "5").Obj()).
			Obj(),
	}
	dfitLocalQueues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("dfit-preempt-lq", "default").ClusterQueue("dfit-preempt").Obj(),
		*utiltestingapi.MakeLocalQueue("dfit-work-lq", "default").ClusterQueue("dfit-work").Obj(),
		*utiltestingapi.MakeLocalQueue("dfit-victim-lq", "default").ClusterQueue("dfit-victim").Obj(),
	}
	dfitVictimActive := utiltestingapi.MakeWorkload("dfit-victim-active", "default").
		Queue("dfit-victim-lq").
		PodSets(*utiltestingapi.MakePodSet("one", 5).
			Request(corev1.ResourceCPU, "1").
			Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("dfit-victim").PodSets(
			utiltestingapi.MakePodSetAssignment("one").
				Assignment(corev1.ResourceCPU, "default", "5").Count(5).Obj(),
		).Obj(), now)
	dfitWorkA := pendingWl("dfit-work-a", "dfit-work-lq", now.Add(-5*time.Minute))
	dfitHead := utiltestingapi.MakeWorkload("dfit-head", "default").
		Queue("dfit-preempt-lq").
		UID("wl-dfit-head").
		JobUID("job-dfit-head").
		Creation(now.Add(-4 * time.Minute)).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			Request(corev1.ResourceCPU, "2").
			Obj())
	dfitWorkB := utiltestingapi.MakeWorkload("dfit-work-b", "default").
		Queue("dfit-work-lq").
		Creation(now.Add(-time.Minute)).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			Request(corev1.ResourceCPU, "2").
			Obj())

	// Real-preemption fixture (fitonly-*): the head admits and refills
	// fitonly-next, whose nomination finds a genuine lower-priority candidate.
	fitonlyClusterQueue := utiltestingapi.MakeClusterQueue("fitonly-prio").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
			Resource(corev1.ResourceCPU, "3", "0").Obj()).
		Preemption(kueue.ClusterQueuePreemption{
			WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
		}).
		Obj()
	fitonlyVictim := utiltestingapi.MakeWorkload("fitonly-victim", "default").
		Queue("fitonly-lq").
		Priority(0).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			Request(corev1.ResourceCPU, "2").
			Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("fitonly-prio").PodSets(
			utiltestingapi.MakePodSetAssignment("one").
				Assignment(corev1.ResourceCPU, "default", "2").Count(1).Obj(),
		).Obj(), now)
	fitonlyHead := utiltestingapi.MakeWorkload("fitonly-head", "default").
		Queue("fitonly-lq").
		Priority(100).
		Creation(now.Add(-3 * time.Minute)).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			Request(corev1.ResourceCPU, "1").
			Obj())
	fitonlyNext := utiltestingapi.MakeWorkload("fitonly-next", "default").
		Queue("fitonly-lq").
		Priority(100).
		Creation(now.Add(-time.Minute)).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			Request(corev1.ResourceCPU, "2").
			Obj())

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
		// get -- so it is deferred like any other non-Fit refill and waits on
		// the heap. It parks with its precise reason one cycle later, when it
		// comes round as its ClusterQueue's head. Its pop already consumed the
		// budget, so the over-share CQ's later admission cannot refill either:
		// rich-next stays in the heap, never popped.
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
				*unadmittedWl(poorBig, kueue.WorkloadQuotaReservedReasonWaitingForQuota,
					"Workload was evaluated mid-cycle and is deferred to the next scheduling cycle: couldn't assign flavors to pod set one: insufficient quota for cpu in flavor default, previously considered podsets requests (0) + current podset request (10) > maximum capacity (8)", "10").Obj(),
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
				"refill-poor": {"default/poor-big"},
				"refill-rich": {"default/rich-next"},
			},
		},
		// The mirror of the case above: rich-borrow is short of used capacity
		// rather than of quota, which is the shortfall a mid-cycle reservation
		// could have produced. It is deferred just the same, and lands back on
		// the heap to compete against the next cycle's fresh snapshot.
		"a refilled workload short of used capacity is deferred, not parked": {
			enableFairSharing: true,
			featureGates:      map[featuregate.Feature]bool{features.FairSharingRefill: true},
			workloads: []kueue.Workload{
				*richActive(6, "6").Obj(),
				*richPending.Clone().Obj(),
				*richBorrow.Clone().Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*richActive(6, "6").Obj(),
				*unadmittedWl(richBorrow, kueue.WorkloadQuotaReservedReasonWaitingForQuota,
					"Workload was evaluated mid-cycle and is deferred to the next scheduling cycle: couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor default, 2 more needed", "5").Obj(),
				*admittedWl(richPending, "refill-rich").Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/rich-active": *utiltestingapi.MakeAdmission("refill-rich").PodSets(
					utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "default", "6").Count(6).Obj(),
				).Obj(),
				"default/rich-pending": *singleCPUAdmission("refill-rich"),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"refill-rich": {"default/rich-borrow"},
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
		// and let chain-a2 and chain-a3 win on FIFO instead. The refilled
		// chain-a3 no longer fits, so the Fit-only rule requeues it back to
		// the heap immediately.
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
					"Workload was evaluated mid-cycle and is deferred to the next scheduling cycle: couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor default, 1 more needed", "1").Obj(),
				*admittedWl(pendingWl("chain-b1", "b-lq", now.Add(-time.Minute)), "refill-b").Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/chain-a1": *singleCPUAdmission("refill-a"),
				"default/chain-a2": *singleCPUAdmission("refill-a"),
				"default/chain-b1": *singleCPUAdmission("refill-b"),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"refill-a": {"default/chain-a3"},
			},
		},
		// An entry that issues preemptions is not assumed and so ends its
		// ClusterQueue's refill chain: the successor stays in the heap even
		// though the head slot is vacated.
		"a preempting workload does not refill its successor": {
			enableFairSharing: true,
			featureGates:      map[featuregate.Feature]bool{features.FairSharingRefill: true},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("refill-prio").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3", "0").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("prio-lq", "default").ClusterQueue("refill-prio").Obj(),
			},
			workloads: []kueue.Workload{
				*prioVictim.Clone().Obj(),
				*prioHead.Clone().Obj(),
				*prioNext.Clone().Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*prioHead.Clone().
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor default, 1 more needed. Pending the preemption of 1 workload(s)",
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
							corev1.ResourceCPU: resource.MustParse("2"),
						},
					}).
					Obj(),
				*prioNext.Clone().Obj(),
				*prioVictim.Clone().
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-prio-head, JobUID: job-prio-head) due to prioritization in the ClusterQueue; preemptor path: /refill-prio; preemptee path: /refill-prio",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-prio-head, JobUID: job-prio-head) due to prioritization in the ClusterQueue; preemptor path: /refill-prio; preemptee path: /refill-prio",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/prio-victim": *utiltestingapi.MakeAdmission("refill-prio").PodSets(
					utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "default", "2").Count(1).Obj(),
				).Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"refill-prio": {"default/prio-head", "default/prio-next"},
			},
			// A wrongly popped successor would collide with the preemptor's
			// targets and count a skip.
			wantSkippedPreemptions: map[string]int{"refill-prio": 0},
		},
		// An admission whose ClusterQueue has an empty backlog pops nothing
		// and must not consume budget: after bgt-solo's admission (first by
		// FIFO, all DRS zero), the single budget unit must remain available
		// for bgt-poor's refill.
		"an admission with an empty backlog does not consume the budget": {
			enableFairSharing: true,
			featureGates:      map[featuregate.Feature]bool{features.FairSharingRefill: true},
			refillBudget:      new(1),
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("bgt-solo").
					Cohort("refill-budget").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1", "0").Obj()).
					Obj(),
				*utiltestingapi.MakeClusterQueue("bgt-poor").
					Cohort("refill-budget").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2", "0").Obj()).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("bgt-solo-lq", "default").ClusterQueue("bgt-solo").Obj(),
				*utiltestingapi.MakeLocalQueue("bgt-poor-lq", "default").ClusterQueue("bgt-poor").Obj(),
			},
			workloads: []kueue.Workload{
				*bgtSolo.Clone().Obj(),
				*bgtPoorA.Clone().Obj(),
				*bgtPoorB.Clone().Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*admittedWl(bgtPoorA, "bgt-poor").Obj(),
				*admittedWl(bgtPoorB, "bgt-poor").Obj(),
				*admittedWl(bgtSolo, "bgt-solo").Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/bgt-solo":   *singleCPUAdmission("bgt-solo"),
				"default/bgt-poor-a": *singleCPUAdmission("bgt-poor"),
				"default/bgt-poor-b": *singleCPUAdmission("bgt-poor"),
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
		// Fit-only rule, reservation source #1: resv-blocked's head reserves
		// its request (no reclaim candidates), which consumes the last free
		// CPU. The refilled resv-work-b would fit on a clean snapshot; against
		// the reservation its mode is Preempt with no candidates, so it is
		// requeued back to the heap immediately -- it must neither reserve
		// capacity itself nor park as inadmissible with no wakeup event.
		"a reservation for an unreclaimable preemption defers the refilled workload": {
			enableFairSharing:       true,
			featureGates:            map[featuregate.Feature]bool{features.FairSharingRefill: true},
			additionalClusterQueues: resvClusterQueues,
			additionalLocalQueues:   resvLocalQueues,
			workloads: []kueue.Workload{
				*resvRichActive.Clone().Obj(),
				*resvBlockedHead.Clone().Obj(),
				*resvWorkA.Clone().Obj(),
				*resvWorkB.Clone().Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*unadmittedWl(resvBlockedHead, kueue.WorkloadQuotaReservedReasonWaitingForQuota,
					"couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor default, 1 more needed", "2").Obj(),
				*resvRichActive.Clone().Obj(),
				*resvWorkAAdmitted.Clone().Obj(),
				*resvWorkB.Clone().
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            "Workload was evaluated mid-cycle and is deferred to the next scheduling cycle: couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor default, 1 more needed",
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
							corev1.ResourceCPU: resource.MustParse("1"),
						},
					}).Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/resv-rich-active": *utiltestingapi.MakeAdmission("resv-rich").PodSets(
					utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "default", "4").Count(4).Obj(),
				).Obj(),
				"default/resv-work-a": *resvWorkAAdmission,
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"resv-work": {"default/resv-work-b"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"resv-blocked": {"default/resv-blocked-head"},
			},
			// The deferral returns before the skipped-preemption accounting.
			wantSkippedPreemptions: map[string]int{"resv-blocked": 0, "resv-work": 0, "resv-rich": 0},
		},
		// Control for the reservation case: the same fixture without the
		// reserving head admits the refilled workload on the free CPU, so the
		// deferral above is caused by the reservation, not by the fixture.
		"without the reservation the same refilled workload is admitted": {
			enableFairSharing:       true,
			featureGates:            map[featuregate.Feature]bool{features.FairSharingRefill: true},
			additionalClusterQueues: resvClusterQueues,
			additionalLocalQueues:   resvLocalQueues,
			workloads: []kueue.Workload{
				*resvRichActive.Clone().Obj(),
				*resvWorkA.Clone().Obj(),
				*resvWorkB.Clone().Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*resvRichActive.Clone().Obj(),
				*resvWorkAAdmitted.Clone().Obj(),
				*resvWorkB.Clone().
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue resv-work",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(singleCPUAdmission("resv-work")).Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/resv-rich-active": *utiltestingapi.MakeAdmission("resv-rich").PodSets(
					utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "default", "4").Count(4).Obj(),
				).Obj(),
				"default/resv-work-a": *resvWorkAAdmission,
				"default/resv-work-b": *singleCPUAdmission("resv-work"),
			},
		},
		// Fit-only rule, reservation source #2 (the #13863 interaction): the
		// refilled dfit-work-b's nomination targets the same victim as
		// dfit-preempt's head, so its recomputed assignment is DeferredFit.
		// The refilled entry must not take the DeferredFit branch (which would
		// reserve capacity mid-cycle); it is requeued back to the heap.
		"a refilled workload whose recomputed assignment is DeferredFit is requeued": {
			enableFairSharing:       true,
			featureGates:            map[featuregate.Feature]bool{features.FairSharingRefill: true},
			additionalClusterQueues: dfitClusterQueues,
			additionalLocalQueues:   dfitLocalQueues,
			workloads: []kueue.Workload{
				*dfitVictimActive.Clone().Obj(),
				*dfitWorkA.Clone().Obj(),
				*dfitHead.Clone().Obj(),
				*dfitWorkB.Clone().Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*dfitHead.Clone().
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor default, 1 more needed. Pending the preemption of 1 workload(s)",
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
							corev1.ResourceCPU: resource.MustParse("2"),
						},
					}).Obj(),
				*dfitVictimActive.Clone().
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-dfit-head, JobUID: job-dfit-head) due to reclamation within the cohort; preemptor path: /dfit/dfit-preempt; preemptee path: /dfit/dfit-victim",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-dfit-head, JobUID: job-dfit-head) due to reclamation within the cohort; preemptor path: /dfit/dfit-preempt; preemptee path: /dfit/dfit-victim",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*admittedWl(dfitWorkA, "dfit-work").Obj(),
				*dfitWorkB.Clone().
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            "Workload was evaluated mid-cycle and is deferred to the next scheduling cycle",
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
							corev1.ResourceCPU: resource.MustParse("2"),
						},
					}).Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/dfit-victim-active": *utiltestingapi.MakeAdmission("dfit-victim").PodSets(
					utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "default", "5").Count(5).Obj(),
				).Obj(),
				"default/dfit-work-a": *singleCPUAdmission("dfit-work"),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"dfit-preempt": {"default/dfit-head"},
				"dfit-work":    {"default/dfit-work-b"},
			},
			// The overlapping targets defer via the Fit-only rule, not via
			// the skipped-preemption path.
			wantSkippedPreemptions: map[string]int{"dfit-preempt": 0, "dfit-work": 0, "dfit-victim": 0},
		},
		// Fit-only rule with genuine scarcity: the refilled fitonly-next has a
		// real lower-priority preemption candidate, but a refilled workload
		// never preempts. The victim keeps its admission and fitonly-next
		// competes as the head of the next cycle, where it may preempt
		// normally.
		"a refilled workload does not preempt even with real candidates": {
			enableFairSharing:       true,
			featureGates:            map[featuregate.Feature]bool{features.FairSharingRefill: true},
			additionalClusterQueues: []kueue.ClusterQueue{*fitonlyClusterQueue},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("fitonly-lq", "default").ClusterQueue("fitonly-prio").Obj(),
			},
			workloads: []kueue.Workload{
				*fitonlyVictim.Clone().Obj(),
				*fitonlyHead.Clone().Obj(),
				*fitonlyNext.Clone().Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*admittedWl(fitonlyHead, "fitonly-prio").Obj(),
				*fitonlyNext.Clone().
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            "Workload was evaluated mid-cycle and is deferred to the next scheduling cycle: couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor default, 2 more needed",
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
							corev1.ResourceCPU: resource.MustParse("2"),
						},
					}).Obj(),
				*fitonlyVictim.Clone().Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/fitonly-victim": *utiltestingapi.MakeAdmission("fitonly-prio").PodSets(
					utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "default", "2").Count(1).Obj(),
				).Obj(),
				"default/fitonly-head": *singleCPUAdmission("fitonly-prio"),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"fitonly-prio": {"default/fitonly-next"},
			},
			wantSkippedPreemptions: map[string]int{"fitonly-prio": 0},
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

// TestRefillFitOnlyDeferralIsPerCycle covers the Fit-only rule across two
// cycles: in the first cycle a mid-cycle reservation defers the refilled
// workload, and in the second cycle -- where the reserving head is parked as
// inadmissible and the reservation is gone with its snapshot -- both the
// deferred workload and a fresh refill pop must proceed normally. This guards
// the rule's state being scoped to the entry, not to the Scheduler: a deferral
// signal that outlives the cycle would suppress the second cycle's refill.
//
// Not expressible as a scheduleTestCase because the harness runs a single
// schedule() call.
func TestRefillFitOnlyDeferralIsPerCycle(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{features.FairSharingRefill: true})

	now := time.Now().Truncate(time.Second)
	flavor := utiltestingapi.MakeResourceFlavor("default").Obj()
	// Same shape as the resv-* fixture of TestScheduleForFairSharingRefill:
	// cycle-blocked's head reserves the cohort's last free CPU mid-cycle.
	clusterQueues := []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("cycle-blocked").
			Cohort("cycle").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "3", "0").Obj()).
			Obj(),
		utiltestingapi.MakeClusterQueue("cycle-work").
			Cohort("cycle").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "2", "0").
				Resource(corev1.ResourceMemory, "2Gi", "0").Obj()).
			Obj(),
		utiltestingapi.MakeClusterQueue("cycle-rich").
			Cohort("cycle").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "0", "4").Obj()).
			Obj(),
	}
	localQueues := []*kueue.LocalQueue{
		utiltestingapi.MakeLocalQueue("cycle-blocked-lq", "default").ClusterQueue("cycle-blocked").Obj(),
		utiltestingapi.MakeLocalQueue("cycle-work-lq", "default").ClusterQueue("cycle-work").Obj(),
		utiltestingapi.MakeLocalQueue("cycle-rich-lq", "default").ClusterQueue("cycle-rich").Obj(),
	}
	richActive := utiltestingapi.MakeWorkload("rich-active", "default").
		Queue("cycle-rich-lq").
		PodSets(*utiltestingapi.MakePodSet("one", 4).
			Request(corev1.ResourceCPU, "1").
			Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cycle-rich").PodSets(
			utiltestingapi.MakePodSetAssignment("one").
				Assignment(corev1.ResourceCPU, "default", "4").Count(4).Obj(),
		).Obj(), now).
		Obj()
	blockedHead := utiltestingapi.MakeWorkload("blocked-head", "default").
		Queue("cycle-blocked-lq").
		Creation(now.Add(-5 * time.Minute)).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			Request(corev1.ResourceCPU, "2").
			Obj()).
		Obj()
	workA := utiltestingapi.MakeWorkload("work-a", "default").
		Queue("cycle-work-lq").
		Creation(now.Add(-4 * time.Minute)).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			Request(corev1.ResourceMemory, "1Gi").
			Obj()).
		Obj()
	workB := utiltestingapi.MakeWorkload("work-b", "default").
		Queue("cycle-work-lq").
		Creation(now.Add(-3 * time.Minute)).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			Request(corev1.ResourceCPU, "1").
			Obj()).
		Obj()
	workC := utiltestingapi.MakeWorkload("work-c", "default").
		Queue("cycle-work-lq").
		Creation(now.Add(-2 * time.Minute)).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			Request(corev1.ResourceMemory, "1Gi").
			Obj()).
		Obj()

	cl := utiltesting.NewClientBuilder().
		WithLists(
			&kueue.WorkloadList{Items: []kueue.Workload{*richActive, *blockedHead, *workA, *workB, *workC}},
			&kueue.LocalQueueList{Items: []kueue.LocalQueue{*localQueues[0], *localQueues[1], *localQueues[2]}},
		).
		WithObjects(utiltesting.MakeNamespaceWrapper("default").Obj()).
		WithStatusSubresource(&kueue.Workload{}).
		Build()

	cqCache := schdcache.New(cl)
	qManager := qcache.NewManagerForUnitTests(cl, cqCache,
		qcache.WithPreemptionExpectations(preemptexpectations.New()))
	cqCache.AddOrUpdateResourceFlavor(log, flavor)
	for _, cq := range clusterQueues {
		if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("Inserting clusterQueue %s in cache: %v", cq.Name, err)
		}
		if err := qManager.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("Inserting clusterQueue %s in manager: %v", cq.Name, err)
		}
	}
	for _, lq := range localQueues {
		if err := qManager.AddLocalQueue(ctx, lq); err != nil {
			t.Fatalf("Inserting localQueue %s in manager: %v", lq.Name, err)
		}
	}
	if !cqCache.AddOrUpdateWorkload(log, richActive.DeepCopy()) {
		t.Fatal("Failed to account the admitted workload in the scheduler cache")
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

	// Cycle 1: blocked-head reserves the free CPU and parks as inadmissible;
	// work-a admits on memory; the refilled work-b is deferred back to the
	// heap. work-c is never popped because the deferred work-b ends the chain.
	scheduler.schedule(ctx)
	wg.Wait()

	wantHeap := map[kueue.ClusterQueueReference][]workload.Reference{
		"cycle-work": {"default/work-b", "default/work-c"},
	}
	if diff := cmp.Diff(wantHeap, qManager.Dump(), cmpDump...); diff != "" {
		t.Errorf("Unexpected heap after the first cycle (-want,+got):\n%s", diff)
	}
	wantInadmissible := map[kueue.ClusterQueueReference][]workload.Reference{
		"cycle-blocked": {"default/blocked-head"},
	}
	if diff := cmp.Diff(wantInadmissible, qManager.DumpInadmissible(), cmpDump...); diff != "" {
		t.Errorf("Unexpected inadmissible workloads after the first cycle (-want,+got):\n%s", diff)
	}

	// Cycle 2: the reservation is gone with its snapshot, so work-b admits as
	// its ClusterQueue's head and refill pops and admits work-c.
	scheduler.schedule(ctx)
	wg.Wait()

	if got := qManager.Dump(); len(got) != 0 {
		t.Errorf("Workloads left on the heap after the second cycle: %v", got)
	}
	for _, name := range []string{"work-b", "work-c"} {
		var wl kueue.Workload
		if err := cl.Get(ctx, client.ObjectKey{Namespace: "default", Name: name}, &wl); err != nil {
			t.Fatalf("Getting workload %s: %v", name, err)
		}
		if !workload.HasQuotaReservation(&wl) {
			t.Errorf("Workload %s has no quota reservation after the second cycle", name)
		}
	}
}

// TestRefillDeferralClearsFlavorScanProgress guards the deferral's
// LastAssignment clearing against FlavorFungibilityPreserveScanProgress: with
// whenCanPreempt: MayStopSearch the refilled flv-next's first-cycle scan stops
// at f1, where it has a genuine preemption candidate, and records that
// position. In the second cycle flv-next is the head and may preempt; only a
// cleared assignment rescans f1 and evicts the candidate there -- preserved
// progress would resume at f2, where the equal-priority filler leaves no
// candidates, and park the workload instead.
//
// Not expressible as a scheduleTestCase because the harness runs a single
// schedule() call.
func TestRefillDeferralClearsFlavorScanProgress(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
		features.FairSharingRefill:                     true,
		features.FlavorFungibilityPreserveScanProgress: true,
	})

	now := time.Now().Truncate(time.Second)
	flavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("f1").Obj(),
		utiltestingapi.MakeResourceFlavor("f2").Obj(),
	}
	cq := utiltestingapi.MakeClusterQueue("flv-prio").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("f1").
				Resource(corev1.ResourceCPU, "3", "0").Obj(),
			*utiltestingapi.MakeFlavorQuotas("f2").
				Resource(corev1.ResourceCPU, "2", "0").Obj()).
		Preemption(kueue.ClusterQueuePreemption{
			WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
		}).
		FlavorFungibility(kueue.FlavorFungibility{WhenCanPreempt: kueue.MayStopSearch}).
		Obj()
	lq := utiltestingapi.MakeLocalQueue("flv-lq", "default").ClusterQueue("flv-prio").Obj()

	victimF1 := utiltestingapi.MakeWorkload("victim-f1", "default").
		Queue("flv-lq").
		Priority(0).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			Request(corev1.ResourceCPU, "2").
			Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("flv-prio").PodSets(
			utiltestingapi.MakePodSetAssignment("one").
				Assignment(corev1.ResourceCPU, "f1", "2").Count(1).Obj(),
		).Obj(), now).
		Obj()
	fillerF2 := utiltestingapi.MakeWorkload("filler-f2", "default").
		Queue("flv-lq").
		Priority(100).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			Request(corev1.ResourceCPU, "2").
			Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("flv-prio").PodSets(
			utiltestingapi.MakePodSetAssignment("one").
				Assignment(corev1.ResourceCPU, "f2", "2").Count(1).Obj(),
		).Obj(), now).
		Obj()
	head := utiltestingapi.MakeWorkload("flv-head", "default").
		Queue("flv-lq").
		Priority(100).
		Creation(now.Add(-4 * time.Minute)).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			Request(corev1.ResourceCPU, "1").
			Obj()).
		Obj()
	next := utiltestingapi.MakeWorkload("flv-next", "default").
		Queue("flv-lq").
		UID("wl-flv-next").
		Priority(100).
		Creation(now.Add(-2 * time.Minute)).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			Request(corev1.ResourceCPU, "2").
			Obj()).
		Obj()

	cl := utiltesting.NewClientBuilder().
		WithLists(
			&kueue.WorkloadList{Items: []kueue.Workload{*victimF1, *fillerF2, *head, *next}},
			&kueue.LocalQueueList{Items: []kueue.LocalQueue{*lq}},
		).
		WithObjects(utiltesting.MakeNamespaceWrapper("default").Obj()).
		WithStatusSubresource(&kueue.Workload{}).
		Build()

	cqCache := schdcache.New(cl)
	qManager := qcache.NewManagerForUnitTests(cl, cqCache,
		qcache.WithPreemptionExpectations(preemptexpectations.New()))
	for _, flavor := range flavors {
		cqCache.AddOrUpdateResourceFlavor(log, flavor)
	}
	if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Inserting clusterQueue in cache: %v", err)
	}
	if err := qManager.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Inserting clusterQueue in manager: %v", err)
	}
	if err := qManager.AddLocalQueue(ctx, lq); err != nil {
		t.Fatalf("Inserting localQueue in manager: %v", err)
	}
	for _, wl := range []*kueue.Workload{victimF1, fillerF2} {
		if !cqCache.AddOrUpdateWorkload(log, wl.DeepCopy()) {
			t.Fatalf("Failed to account the admitted workload %s in the scheduler cache", wl.Name)
		}
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

	// Cycle 1: flv-head admits on f1's last free CPU; the refilled flv-next
	// stops its scan at f1 with victim-f1 as a preemption candidate and is
	// deferred without preempting.
	scheduler.schedule(ctx)
	wg.Wait()

	var victim kueue.Workload
	if err := cl.Get(ctx, client.ObjectKey{Namespace: "default", Name: "victim-f1"}, &victim); err != nil {
		t.Fatalf("Getting workload victim-f1: %v", err)
	}
	if meta.IsStatusConditionTrue(victim.Status.Conditions, kueue.WorkloadEvicted) {
		t.Fatal("The refilled workload preempted the victim in the first cycle")
	}

	// Cycle 2: flv-next is the head and may preempt. The cleared assignment
	// rescans from f1 and evicts victim-f1 there; preserved scan progress
	// would resume at f2 and find no candidates.
	scheduler.schedule(ctx)
	wg.Wait()

	if err := cl.Get(ctx, client.ObjectKey{Namespace: "default", Name: "victim-f1"}, &victim); err != nil {
		t.Fatalf("Getting workload victim-f1: %v", err)
	}
	if !meta.IsStatusConditionTrue(victim.Status.Conditions, kueue.WorkloadEvicted) {
		t.Error("The deferred workload did not preempt on f1 in the second cycle; its flavor scan did not restart")
	}
}

// TestTryRefillStopsBeforePopping covers the stop reasons reachable without
// popping a successor, including every entryStatus that must not start a chain.
// BudgetExhausted must mean the budget actually bound, not merely that it was
// spent. The reasons needing a snapshot are covered by
// TestScheduleForFairSharingRefill.
func TestTryRefillStopsBeforePopping(t *testing.T) {
	cases := map[string]struct {
		entryStatus   entryStatus
		quotaReserved bool
		// variant makes the admitted workload a concurrent admission variant;
		// concurrentAdmission enables the gate, so the two can be set apart.
		variant             bool
		concurrentAdmission bool
		queuedSuccessor     bool
		want                refillStopReason
		// Spelled out rather than derived from want, so renaming a constant
		// cannot move the expectation with it.
		wantLabel string
	}{
		// One row per entryStatus that must not start a chain. A successor is
		// queued throughout, so a leak reports a different reason.
		"the processed entry was never nominated": {
			entryStatus:     notNominated,
			queuedSuccessor: true,
			want:            refillStopNotAdmitted,
		},
		"the processed entry was nominated but not admitted": {
			entryStatus:     nominated,
			queuedSuccessor: true,
			want:            refillStopNotAdmitted,
		},
		"the processed entry was skipped": {
			entryStatus:     skipped,
			queuedSuccessor: true,
			want:            refillStopNotAdmitted,
		},
		// Only gated: no capacity freed, and no quota reservation for the
		// second-pass guard to catch.
		"the processed entry is waiting on preemption gates": {
			entryStatus:     preemptionGated,
			queuedSuccessor: true,
			want:            refillStopNotAdmitted,
		},
		"the processed entry was evicted": {
			entryStatus:     evicted,
			queuedSuccessor: true,
			want:            refillStopNotAdmitted,
		},
		"the admission was a second pass": {
			entryStatus:   assumed,
			quotaReserved: true,
			want:          refillStopSecondPass,
			wantLabel:     "SecondPassAdmission",
		},
		"the admission was a concurrent admission variant": {
			entryStatus:         assumed,
			variant:             true,
			concurrentAdmission: true,
			queuedSuccessor:     true,
			want:                refillStopVariantAdmitted,
			wantLabel:           "VariantAdmitted",
		},
		// The guard reads the workload only when the gate is on: with it off,
		// a Workload-kind owner reference is not refill's business.
		"a variant is admitted with concurrent admission disabled": {
			entryStatus:     assumed,
			variant:         true,
			queuedSuccessor: true,
			want:            refillStopBudget,
			wantLabel:       "BudgetExhausted",
		},
		"the budget is spent and a successor is waiting": {
			entryStatus:     assumed,
			queuedSuccessor: true,
			want:            refillStopBudget,
			wantLabel:       "BudgetExhausted",
		},
		"the budget is spent but nothing is waiting": {
			entryStatus: assumed,
			want:        refillStopQueueEmpty,
			wantLabel:   "QueueEmpty",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ConcurrentAdmission, tc.concurrentAdmission)
			ctx, log := utiltesting.ContextWithLog(t)
			now := time.Now().Truncate(time.Second)

			cq := utiltestingapi.MakeClusterQueue("stop-cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "8", "0").Obj()).
				Obj()
			lq := utiltestingapi.MakeLocalQueue("stop-lq", "default").ClusterQueue("stop-cq").Obj()

			admitted := utiltestingapi.MakeWorkload("admitted", "default").Queue("stop-lq").
				PodSets(*utiltestingapi.MakePodSet("one", 1).Request(corev1.ResourceCPU, "1").Obj())
			if tc.quotaReserved {
				admitted.ReserveQuotaAt(utiltestingapi.MakeAdmission("stop-cq").Obj(), now)
			}
			if tc.variant {
				admitted.ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent", "parent-uid")
			}
			var pending []kueue.Workload
			if tc.queuedSuccessor {
				pending = append(pending, *utiltestingapi.MakeWorkload("successor", "default").
					Queue("stop-lq").
					PodSets(*utiltestingapi.MakePodSet("one", 1).Request(corev1.ResourceCPU, "1").Obj()).
					Obj())
			}

			cl := utiltesting.NewClientBuilder().
				WithLists(
					&kueue.WorkloadList{Items: pending},
					&kueue.LocalQueueList{Items: []kueue.LocalQueue{*lq}},
				).
				WithObjects(utiltesting.MakeNamespaceWrapper("default").Obj()).
				Build()
			cqCache := schdcache.New(cl)
			qManager := qcache.NewManagerForUnitTests(cl, cqCache,
				qcache.WithPreemptionExpectations(preemptexpectations.New()))
			cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
			if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("Inserting clusterQueue in cache: %v", err)
			}
			if err := qManager.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("Inserting clusterQueue in manager: %v", err)
			}
			if err := qManager.AddLocalQueue(ctx, lq); err != nil {
				t.Fatalf("Inserting localQueue in manager: %v", err)
			}
			if got := qManager.HasQueuedWorkloads("stop-cq"); got != tc.queuedSuccessor {
				t.Fatalf("The fixture has a queued successor: %t, want %t", got, tc.queuedSuccessor)
			}

			// budget 0 stops on the budget branch, so no snapshot is needed.
			r := &refillPass{
				scheduler: &Scheduler{queues: qManager, cache: cqCache},
				budget:    0,
			}
			e := &entry{
				Info:   workload.Info{Obj: admitted.Obj(), ClusterQueue: "stop-cq"},
				status: tc.entryStatus,
			}

			got, refilled := r.tryRefill(ctx, e)
			if got != tc.want {
				t.Errorf("tryRefill() reason = %q, want %q", got, tc.want)
			}
			if refilled != nil {
				t.Errorf("tryRefill() popped %q, want no successor", workload.Key(refilled.Obj))
			}

			// The same decision through the hook, to pin the emitted string
			// and that EntryNotAdmitted stays silent.
			capture, logger := newRefillLogCapture()
			hook := &refillPass{
				scheduler: &Scheduler{queues: qManager, cache: cqCache},
				budget:    0,
			}
			hook.afterEntryProcessed(ctrl.LoggerInto(ctx, logger), e)
			capture.verifyStop(t, tc.want, tc.wantLabel)
		})
	}
}

// TestRefillStopReasonLabels pins the label strings a future
// refill_stops_total{reason} would inherit. Two of them are out of reach of
// TestTryRefillStopsBeforePopping, and a pair collapsing onto one label would
// make that metric under-report: as constant keys, that is a compile error here.
func TestRefillStopReasonLabels(t *testing.T) {
	want := map[refillStopReason]string{
		refillContinue:                  "",
		refillStopNotAdmitted:           "EntryNotAdmitted",
		refillStopSecondPass:            "SecondPassAdmission",
		refillStopVariantAdmitted:       "VariantAdmitted",
		refillStopBudget:                "BudgetExhausted",
		refillStopQueueEmpty:            "QueueEmpty",
		refillStopSuccessorNotNominated: "SuccessorNotNominated",
	}
	for reason, label := range want {
		if string(reason) != label {
			t.Errorf("Stop reason label = %q, want %q", string(reason), label)
		}
	}
}

// newRefillLogCapture returns a logger verbose enough for refill's V(3) lines
// and a capture of what reaches it.
func newRefillLogCapture() (*refillLogCapture, logr.Logger) {
	capture := &refillLogCapture{}
	logger := funcr.NewJSON(func(obj string) {
		entry := make(map[string]any)
		if err := json.Unmarshal([]byte(obj), &entry); err != nil {
			capture.parseErr = err
			return
		}
		capture.entries = append(capture.entries, entry)
	}, funcr.Options{Verbosity: 3})
	return capture, logger
}

type refillLogCapture struct {
	entries  []map[string]any
	parseErr error
}

// verifyStop asserts the single line afterEntryProcessed emits for a stop, or
// that it stayed silent when the chain never started.
func (c *refillLogCapture) verifyStop(t *testing.T, want refillStopReason, wantLabel string) {
	t.Helper()
	if c.parseErr != nil {
		t.Fatalf("Parsing the captured log: %v", c.parseErr)
	}
	if want == refillStopNotAdmitted {
		if len(c.entries) != 0 {
			t.Errorf("afterEntryProcessed logged %v, want silence for %q", c.entries, want)
		}
		return
	}
	if len(c.entries) != 1 {
		t.Fatalf("afterEntryProcessed logged %d lines, want exactly 1: %v", len(c.entries), c.entries)
	}
	entry := c.entries[0]
	if got := entry["msg"]; got != "Refill stopped after an admission" {
		t.Errorf("The logged message is %q, want the stop message", got)
	}
	if got := entry["reason"]; got != wantLabel {
		t.Errorf("The logged reason is %q, want %q", got, wantLabel)
	}
}

// TestNewRefillPass pins the conditions under which a cycle gets a refill hook
// at all, including that WaitForPodsReady with blockAdmission is settled here
// rather than re-asked on every admission.
func TestNewRefillPass(t *testing.T) {
	cases := map[string]struct {
		refillEnabled     bool
		podsReadyTracking bool
		fairSharing       bool
		wantHook          bool
	}{
		"fair sharing, gate on, no pods-ready tracking": {
			refillEnabled: true,
			fairSharing:   true,
			wantHook:      true,
		},
		"the feature gate is off": {
			fairSharing: true,
		},
		"pods-ready tracking blocks admission": {
			refillEnabled:     true,
			podsReadyTracking: true,
			fairSharing:       true,
		},
		"the classical iterator is not supported": {
			refillEnabled: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.FairSharingRefill: tc.refillEnabled,
			})

			cqCache := schdcache.New(utiltesting.NewFakeClient(),
				schdcache.WithPodsReadyTracking(tc.podsReadyTracking))
			// Not defaultRefillBudget, so a constructor ignoring the
			// configured budget is caught.
			s := &Scheduler{cache: cqCache, refillBudget: defaultRefillBudget + 1}
			iterator := makeIterator(ctx, nil, workload.Ordering{}, tc.fairSharing)
			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("Building the snapshot: %v", err)
			}

			got := s.newRefillPass(iterator, snapshot)
			if (got != nil) != tc.wantHook {
				t.Fatalf("newRefillPass() returned a hook: %t, want %t", got != nil, tc.wantHook)
			}
			if got == nil {
				return
			}
			// The hook is useless unless the cycle's own state reached it.
			if got.iterator != iterator {
				t.Error("The hook does not hold the cycle's iterator")
			}
			if got.snapshot != snapshot {
				t.Error("The hook does not hold the cycle's snapshot")
			}
			if got.budget != s.refillBudget {
				t.Errorf("The hook's budget = %d, want %d", got.budget, s.refillBudget)
			}
			if got.scheduler != s {
				t.Error("The hook does not hold the scheduler")
			}
		})
	}
}

// TestRefillNotTriggeredWhenPodsReadyBlocksAdmission covers the end-to-end
// consequence of the constructor's pods-ready condition: the cycle has no
// refill hook, so the successor stays in the heap and its status is never
// touched.
//
// Not expressible as a scheduleTestCase: the harness does not enable
// pods-ready tracking on the scheduler cache.
func TestRefillNotTriggeredWhenPodsReadyBlocksAdmission(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{features.FairSharingRefill: true})

	now := time.Now().Truncate(time.Second)
	cq := utiltestingapi.MakeClusterQueue("refill-poor").
		Cohort("refill").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
			Resource(corev1.ResourceCPU, "8", "0").Obj()).
		Obj()
	lq := utiltestingapi.MakeLocalQueue("poor-lq", "default").ClusterQueue("refill-poor").Obj()

	pending := func(name string, creation time.Time) *kueue.Workload {
		return utiltestingapi.MakeWorkload(name, "default").
			Queue("poor-lq").
			Creation(creation).
			PodSets(*utiltestingapi.MakePodSet("one", 1).
				Request(corev1.ResourceCPU, "1").
				Obj()).
			Obj()
	}
	head := pending("head", now.Add(-2*time.Minute))
	next := pending("next", now.Add(-time.Minute))

	cl := utiltesting.NewClientBuilder().
		WithLists(
			&kueue.WorkloadList{Items: []kueue.Workload{*head, *next}},
			&kueue.LocalQueueList{Items: []kueue.LocalQueue{*lq}},
		).
		WithObjects(utiltesting.MakeNamespaceWrapper("default").Obj()).
		WithStatusSubresource(&kueue.Workload{}).
		Build()

	cqCache := schdcache.New(cl, schdcache.WithPodsReadyTracking(true))
	qManager := qcache.NewManagerForUnitTests(cl, cqCache,
		qcache.WithPreemptionExpectations(preemptexpectations.New()))
	cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
	if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Inserting clusterQueue in cache: %v", err)
	}
	if err := qManager.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Inserting clusterQueue in manager: %v", err)
	}
	if err := qManager.AddLocalQueue(ctx, lq); err != nil {
		t.Fatalf("Inserting localQueue in manager: %v", err)
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
	go cqCache.CleanUpOnContext(ctx)
	go qManager.CleanUpOnContext(ctx)

	scheduler.schedule(ctx)
	wg.Wait()

	// Nothing was admitted before head, so the block does not apply to it.
	var gotHead kueue.Workload
	if err := cl.Get(ctx, client.ObjectKeyFromObject(head), &gotHead); err != nil {
		t.Fatalf("Getting the head workload: %v", err)
	}
	if !workload.HasQuotaReservation(&gotHead) {
		t.Error("The head workload was not admitted")
	}

	want := []workload.Reference{"default/next"}
	if diff := cmp.Diff(want, qManager.Dump()["refill-poor"], cmpDump); diff != "" {
		t.Errorf("The successor did not stay in the heap (-want,+got):\n%s", diff)
	}

	// Entering the admission block would have patched a WaitingForPodsReady
	// condition on the successor.
	var gotNext kueue.Workload
	if err := cl.Get(ctx, client.ObjectKeyFromObject(next), &gotNext); err != nil {
		t.Fatalf("Getting the successor workload: %v", err)
	}
	if len(gotNext.Status.Conditions) != 0 {
		t.Errorf("The successor's status was patched during the cycle: %v", gotNext.Status.Conditions)
	}
}

// TestRefillPopKeepsMidCycleRequeueSignal covers the mid-cycle pop's effect on
// the epoch counter end to end. A workload that cannot be admitted goes back to
// the active heap, rather than being parked, when a requeue event arrived after
// the cycle's heads were popped. That event is edge-triggered, so a refilled
// workload whose pop hides it waits for the next one instead of for the next
// cycle. TestPopMidCycleDoesNotConsumeRequeueSignal pins this on the queue
// primitive, but nothing showed the scheduler holding on to the signal.
//
// The namespaceSelector turns the refilled workload away because the Fit-only
// rule leaves no other way in: it requeues every refilled entry that got an
// assignment with a reason that never reads the counter, so only a failed
// nomination reaches it.
//
// Not expressible as a scheduleTestCase because the harness runs a single
// schedule() call and cannot fire an event mid-cycle.
func TestRefillPopKeepsMidCycleRequeueSignal(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{features.FairSharingRefill: true})

	now := time.Now().Truncate(time.Second)
	flavor := utiltestingapi.MakeResourceFlavor("default").Obj()
	clusterQueue := utiltestingapi.MakeClusterQueue("signal-cq").
		Cohort("signal").
		NamespaceSelector(&metav1.LabelSelector{MatchLabels: map[string]string{"signal": "admit"}}).
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
			Resource(corev1.ResourceCPU, "2", "0").Obj()).
		Obj()
	// Two LocalQueues in different namespaces feed the one ClusterQueue, so the
	// head passes the selector while its successor does not.
	headQueue := utiltestingapi.MakeLocalQueue("signal-head-lq", "signal-admit").ClusterQueue("signal-cq").Obj()
	nextQueue := utiltestingapi.MakeLocalQueue("signal-next-lq", "signal-hold").ClusterQueue("signal-cq").Obj()
	head := utiltestingapi.MakeWorkload("signal-head", "signal-admit").
		Queue("signal-head-lq").
		Creation(now.Add(-2 * time.Minute)).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			Request(corev1.ResourceCPU, "1").
			Obj()).
		Obj()
	next := utiltestingapi.MakeWorkload("signal-next", "signal-hold").
		Queue("signal-next-lq").
		Creation(now.Add(-1 * time.Minute)).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			Request(corev1.ResourceCPU, "1").
			Obj()).
		Obj()

	// The requeue event has to land between the heads pop and the refill pop,
	// which is the window the counter is about. A nomination that reaches
	// admissibility validation reads its namespace, so the head's validation is
	// a hook into that window which costs the production code nothing.
	var fired bool
	fireOnce := func() {}
	cl := utiltesting.NewClientBuilder().
		WithLists(
			&kueue.WorkloadList{Items: []kueue.Workload{*head, *next}},
			&kueue.LocalQueueList{Items: []kueue.LocalQueue{*headQueue, *nextQueue}},
		).
		WithObjects(
			utiltesting.MakeNamespaceWrapper("signal-admit").Label("signal", "admit").Obj(),
			utiltesting.MakeNamespaceWrapper("signal-hold").Obj(),
		).
		WithStatusSubresource(&kueue.Workload{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.Namespace); ok && !fired {
					fired = true
					fireOnce()
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).
		Build()

	cqCache := schdcache.New(cl)
	qManager, requeuer := qcache.NewManagerForUnitTestsWithRequeuer(cl, cqCache,
		qcache.WithPreemptionExpectations(preemptexpectations.New()))
	fireOnce = func() {
		qcache.NotifyRetryInadmissible(qManager, sets.New[kueue.ClusterQueueReference]("signal-cq"))
		requeuer.ProcessRequeues(ctx)
	}
	cqCache.AddOrUpdateResourceFlavor(log, flavor)
	if err := cqCache.AddClusterQueue(ctx, clusterQueue); err != nil {
		t.Fatalf("Inserting clusterQueue %s in cache: %v", clusterQueue.Name, err)
	}
	if err := qManager.AddClusterQueue(ctx, clusterQueue); err != nil {
		t.Fatalf("Inserting clusterQueue %s in manager: %v", clusterQueue.Name, err)
	}
	for _, lq := range []*kueue.LocalQueue{headQueue, nextQueue} {
		if err := qManager.AddLocalQueue(ctx, lq); err != nil {
			t.Fatalf("Inserting localQueue %s in manager: %v", lq.Name, err)
		}
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

	// Cycle 1: signal-head admits and the refilled signal-next fails the
	// selector. The requeue event fired while the cycle was running, so
	// signal-next belongs back on the active heap.
	scheduler.schedule(ctx)
	wg.Wait()

	if !fired {
		t.Fatal("The mid-cycle requeue event never fired; the fixture no longer hooks a nomination")
	}
	wantHeap := map[kueue.ClusterQueueReference][]workload.Reference{
		"signal-cq": {"signal-hold/signal-next"},
	}
	if diff := cmp.Diff(wantHeap, qManager.Dump(), cmpDump...); diff != "" {
		t.Errorf("Unexpected heap after the first cycle (-want,+got):\n%s", diff)
	}
	if got := qManager.DumpInadmissible(); len(got) != 0 {
		t.Errorf("Refilled workload parked as inadmissible, so the mid-cycle event was lost: %v", got)
	}

	// Cycle 2: the namespace now matches, so a signal-next that stayed on the
	// heap admits. One that had been parked would still be waiting for an event
	// that already happened.
	ns := utiltesting.MakeNamespaceWrapper("signal-hold").Label("signal", "admit").Obj()
	if err := cl.Update(ctx, ns); err != nil {
		t.Fatalf("Labelling the successor's namespace: %v", err)
	}
	scheduler.schedule(ctx)
	wg.Wait()

	var gotNext kueue.Workload
	if err := cl.Get(ctx, client.ObjectKeyFromObject(next), &gotNext); err != nil {
		t.Fatalf("Getting the successor workload: %v", err)
	}
	if !workload.HasQuotaReservation(&gotNext) {
		t.Errorf("Workload %s has no quota reservation after the second cycle", next.Name)
	}
}
