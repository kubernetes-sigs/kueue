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
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
)

func defaultCohorts() []kueue.Cohort {
	// Cohort hierarchy for this test suite:
	//
	//                     root-cohort (nominal: 3005)
	//                   /              |              \
	//         parent-cohort-a   parent-cohort-b   parent-cohort-c
	//           (nominal: 0)     (nominal: 0)      (nominal: 0)
	//            /        \            |                 |
	//         cq-hero   cq-noisy    cq-tiny           cq-rest
	//       (nom: 3000) (nom: 0)   (nom: 50)         (nom: 0)
	return []kueue.Cohort{
		*utiltestingapi.MakeCohort("root-cohort").ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "3005").Obj(),
		).Obj(),
		*utiltestingapi.MakeCohort("parent-cohort-a").Parent("root-cohort").ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "0").Obj(),
		).Obj(),
		*utiltestingapi.MakeCohort("parent-cohort-b").Parent("root-cohort").Obj(),
		*utiltestingapi.MakeCohort("parent-cohort-c").Parent("root-cohort").Obj(),
	}
}

func TestScheduleRecomputePreemptionTargets(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(now)

	resourceFlavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("default").Obj(),
		utiltestingapi.MakeResourceFlavor("on-demand").Obj(),
	}

	clusterQueues := []kueue.ClusterQueue{
		*utiltestingapi.MakeClusterQueue("cq-hero").
			Cohort("parent-cohort-a").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "3000").Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
			}).Obj(),
		*utiltestingapi.MakeClusterQueue("cq-noisy").
			Cohort("parent-cohort-a").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "0").Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
			}).Obj(),
		*utiltestingapi.MakeClusterQueue("cq-tiny").
			Cohort("parent-cohort-b").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "50").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
			}).Obj(),
		*utiltestingapi.MakeClusterQueue("cq-rest").
			Cohort("parent-cohort-c").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "0").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
			}).Obj(),
	}

	queues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("lq-hero", "eng-alpha").ClusterQueue("cq-hero").Obj(),
		*utiltestingapi.MakeLocalQueue("lq-noisy", "eng-alpha").ClusterQueue("cq-noisy").Obj(),
		*utiltestingapi.MakeLocalQueue("lq-tiny", "eng-alpha").ClusterQueue("cq-tiny").Obj(),
		*utiltestingapi.MakeLocalQueue("lq-rest", "eng-alpha").ClusterQueue("cq-rest").Obj(),
	}

	cases := map[string]scheduleTestCase{
		"with fair sharing: hierarchical nominal-first prefers non-borrowing leaf CQ": {
			// Admitted state:
			// - wl-noisy-admitted (cq-noisy): uses 3000 CPU (borrowed from cq-hero)
			// - wl-tiny-admitted (cq-tiny): uses 60 CPU (borrows 10 from root-cohort)
			// - wl-rest-admitted (cq-rest): uses 2995 CPU (borrowed from root-cohort)
			//
			// Pending:
			// - wl-hero (cq-hero): requests 2990 CPU (fits within its nominal 3000 CPU, so cq-hero is not borrowing)
			// - wl-tiny-pending (cq-tiny): requests 10 CPU (already borrowing, needs to borrow even more)
			//
			// Since cq-rest is borrowing a lot of quota, both cq-hero and cq-tiny initially choose
			// the same candidate for preemption (wl-rest-admitted from cq-rest).
			// Within the scheduling cycle, wl-tiny-pending is considered before wl-hero (due to
			// Fair Sharing prioritization). So, wl-hero would normally be requeued due to
			// overlapping preemption targets. But with RecomputePreemptionTargetsUponOverlap
			// featuregate, it refreshes its targets, finds wl-noisy-admitted from cq-noisy
			// and preempts it in the same scheduling cycle.
			enableFairSharing: true,
			cohorts:           defaultCohorts(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-noisy-admitted", "eng-alpha").
					UID("wl-noisy-admitted").
					Queue("lq-noisy").
					Request(corev1.ResourceCPU, "3000").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-noisy").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3000").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-tiny-admitted", "eng-alpha").
					UID("wl-tiny-admitted").
					Queue("lq-tiny").
					Request(corev1.ResourceCPU, "60").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-tiny").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "60").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-rest-admitted", "eng-alpha").
					UID("wl-rest-admitted").
					Queue("lq-rest").
					Request(corev1.ResourceCPU, "2995").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-rest").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2995").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-hero", "eng-alpha").
					UID("wl-hero").
					JobUID("job-h-uid").
					Queue("lq-hero").
					Request(corev1.ResourceCPU, "2990").
					Creation(now.Add(time.Second)).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-tiny-pending", "eng-alpha").
					UID("wl-tiny-pending").
					JobUID("job-t-uid").
					Queue("lq-tiny").
					Request(corev1.ResourceCPU, "10").
					Creation(now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-hero", "eng-alpha").
					UID("wl-hero").
					JobUID("job-h-uid").
					Queue("lq-hero").
					Request(corev1.ResourceCPU, "2990").
					Creation(now.Add(time.Second)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 5 more needed. Pending the preemption of 1 workload(s)",
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
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2990"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-noisy-admitted", "eng-alpha").
					UID("wl-noisy-admitted").
					Queue("lq-noisy").
					Request(corev1.ResourceCPU, "3000").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-noisy").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3000").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-hero, JobUID: job-h-uid) due to reclamation within the cohort; preemptor path: /root-cohort/parent-cohort-a/cq-hero; preemptee path: /root-cohort/parent-cohort-a/cq-noisy",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-hero, JobUID: job-h-uid) due to reclamation within the cohort; preemptor path: /root-cohort/parent-cohort-a/cq-hero; preemptee path: /root-cohort/parent-cohort-a/cq-noisy",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-rest-admitted", "eng-alpha").
					UID("wl-rest-admitted").
					Queue("lq-rest").
					Request(corev1.ResourceCPU, "2995").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-rest").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2995").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-tiny-pending, JobUID: job-t-uid) due to Fair Sharing within the cohort; preemptor path: /root-cohort/parent-cohort-b/cq-tiny; preemptee path: /root-cohort/parent-cohort-c/cq-rest",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortFairSharing",
						Message:            "Preempted to accommodate a workload (UID: wl-tiny-pending, JobUID: job-t-uid) due to Fair Sharing within the cohort; preemptor path: /root-cohort/parent-cohort-b/cq-tiny; preemptee path: /root-cohort/parent-cohort-c/cq-rest",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-tiny-admitted", "eng-alpha").
					UID("wl-tiny-admitted").
					Queue("lq-tiny").
					Request(corev1.ResourceCPU, "60").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-tiny").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "60").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-tiny-pending", "eng-alpha").
					UID("wl-tiny-pending").
					JobUID("job-t-uid").
					Queue("lq-tiny").
					Request(corev1.ResourceCPU, "10").
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 10 more needed. Pending the preemption of 1 workload(s)",
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
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("10"),
						},
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/wl-noisy-admitted": *utiltestingapi.MakeAdmission("cq-noisy").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "3000").Obj()).Obj(),
				"eng-alpha/wl-tiny-admitted":  *utiltestingapi.MakeAdmission("cq-tiny").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "60").Obj()).Obj(),
				"eng-alpha/wl-rest-admitted":  *utiltestingapi.MakeAdmission("cq-rest").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "2995").Obj()).Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-hero": {"eng-alpha/wl-hero"},
				"cq-tiny": {"eng-alpha/wl-tiny-pending"},
			},
			// wl-hero initially targeted wl-rest-admitted (same as wl-tiny-pending), then
			// recomputed and found wl-noisy-admitted as a non-overlapping alternative.
			wantPreemptionTargetRecomputations: map[string]map[string]int{
				"cq-hero": {"new_targets": 1},
			},
		},
		"with fair sharing: two workloads reclaim nominal quota; RecomputePreemptionTargetsUponOverlap enabled": {
			enableFairSharing: true,
			cohorts:           defaultCohorts(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-rest-admitted", "eng-alpha").
					UID("wl-rest-admitted").
					Queue("lq-rest").
					Request(corev1.ResourceCPU, "6055").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-rest").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "6055").Obj()).
						Obj(), now).
					Priority(0).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-hero", "eng-alpha").
					UID("wl-hero").
					JobUID("job-h-uid").
					Queue("lq-hero").
					Request(corev1.ResourceCPU, "2950").
					Creation(now.Add(time.Second)).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-tiny-pending", "eng-alpha").
					UID("wl-tiny-pending").
					JobUID("job-t-uid").
					Queue("lq-tiny").
					Request(corev1.ResourceCPU, "50").
					Creation(now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-rest-pending", "eng-alpha").
					UID("wl-rest-pending").
					Queue("lq-rest").
					Request(corev1.ResourceCPU, "4000").
					Priority(10).
					Creation(now.Add(2 * time.Second)).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-hero", "eng-alpha").
					UID("wl-hero").
					JobUID("job-h-uid").
					Queue("lq-hero").
					Request(corev1.ResourceCPU, "2950").
					Creation(now.Add(time.Second)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "Workload has overlapping preemption targets with another workload, but will fit after these preemptions complete",
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
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2950"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-rest-admitted", "eng-alpha").
					UID("wl-rest-admitted").
					Queue("lq-rest").
					Request(corev1.ResourceCPU, "6055").
					Priority(0).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-rest").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "6055").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-tiny-pending, JobUID: job-t-uid) due to reclamation within the cohort; preemptor path: /root-cohort/parent-cohort-b/cq-tiny; preemptee path: /root-cohort/parent-cohort-c/cq-rest",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-tiny-pending, JobUID: job-t-uid) due to reclamation within the cohort; preemptor path: /root-cohort/parent-cohort-b/cq-tiny; preemptee path: /root-cohort/parent-cohort-c/cq-rest",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-rest-pending", "eng-alpha").
					UID("wl-rest-pending").
					Queue("lq-rest").
					Request(corev1.ResourceCPU, "4000").
					Creation(now.Add(2 * time.Second)).
					Priority(10).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 945 more needed",
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
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("4000"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-tiny-pending", "eng-alpha").
					UID("wl-tiny-pending").
					JobUID("job-t-uid").
					Queue("lq-tiny").
					Request(corev1.ResourceCPU, "50").
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 50 more needed. Pending the preemption of 1 workload(s)",
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
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("50"),
						},
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/wl-rest-admitted": *utiltestingapi.MakeAdmission("cq-rest").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "6055").Obj()).Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-hero": {"eng-alpha/wl-hero"},
				"cq-tiny": {"eng-alpha/wl-tiny-pending"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-rest": {"eng-alpha/wl-rest-pending"},
			},
			// wl-hero overlapped with wl-tiny-pending on wl-rest-admitted, recomputed, but
			// can only fit after the earlier preemptions (from wl-tiny-pending) complete.
			wantPreemptionTargetRecomputations: map[string]map[string]int{
				"cq-hero": {"deferred_fit": 1},
			},
		},
		"two workloads reclaim nominal quota; RecomputePreemptionTargetsUponOverlap enabled": {
			enableFairSharing: false,
			cohorts:           defaultCohorts(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-rest-admitted", "eng-alpha").
					UID("wl-rest-admitted").
					Queue("lq-rest").
					Request(corev1.ResourceCPU, "6055").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-rest").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "6055").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-hero", "eng-alpha").
					UID("wl-hero").
					JobUID("job-h-uid").
					Queue("lq-hero").
					Request(corev1.ResourceCPU, "2950").
					Creation(now.Add(time.Second)).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-tiny-pending", "eng-alpha").
					UID("wl-tiny-pending").
					JobUID("job-t-uid").
					Queue("lq-tiny").
					Request(corev1.ResourceCPU, "50").
					Creation(now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-hero", "eng-alpha").
					UID("wl-hero").
					JobUID("job-h-uid").
					Queue("lq-hero").
					Request(corev1.ResourceCPU, "2950").
					Creation(now.Add(time.Second)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "Workload has overlapping preemption targets with another workload, but will fit after these preemptions complete",
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
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2950"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-rest-admitted", "eng-alpha").
					UID("wl-rest-admitted").
					Queue("lq-rest").
					Request(corev1.ResourceCPU, "6055").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-rest").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "6055").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-tiny-pending, JobUID: job-t-uid) due to reclamation within the cohort; preemptor path: /root-cohort/parent-cohort-b/cq-tiny; preemptee path: /root-cohort/parent-cohort-c/cq-rest",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-tiny-pending, JobUID: job-t-uid) due to reclamation within the cohort; preemptor path: /root-cohort/parent-cohort-b/cq-tiny; preemptee path: /root-cohort/parent-cohort-c/cq-rest",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-tiny-pending", "eng-alpha").
					UID("wl-tiny-pending").
					JobUID("job-t-uid").
					Queue("lq-tiny").
					Request(corev1.ResourceCPU, "50").
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 50 more needed. Pending the preemption of 1 workload(s)",
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
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("50"),
						},
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/wl-rest-admitted": *utiltestingapi.MakeAdmission("cq-rest").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "6055").Obj()).Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-hero": {"eng-alpha/wl-hero"},
				"cq-tiny": {"eng-alpha/wl-tiny-pending"},
			},
			// wl-hero overlapped with wl-tiny-pending on wl-rest-admitted, recomputed, but
			// can only fit after the earlier preemptions (from wl-tiny-pending) complete.
			wantPreemptionTargetRecomputations: map[string]map[string]int{
				"cq-hero": {"deferred_fit": 1},
			},
		},

		"flavor stickiness with RecomputePreemptionTargetsUponOverlap": {
			// Admitted state:
			// - wl-admitted-default (cq-1): uses 10 CPU on default flavor (borrowing 5)
			// - wl-admitted-on-demand (cq-1): uses 10 CPU on on-demand flavor (borrowing 5)
			//
			// Pending:
			// - wl-pending-high-prio-1 (cq-1): priority 11, requests 10 CPU (can fit by preempting its own admitted workloads)
			// - wl-pending-high-prio-2 (cq-2): priority 10, requests 10 CPU (needs to borrow, can fit by reclaiming from cq-1)
			//
			// Since cq-1 has much higher usage (20 CPU) than cq-2 (0 CPU), under Fair Sharing,
			// cq-2's pending workload (wl-pending-high-prio-2) is prioritized and processed first
			// despite having a lower priority than wl-pending-high-prio-1.
			//
			// wl-pending-high-prio-2 chooses to preempt wl-admitted-default on the default flavor.
			// When wl-pending-high-prio-1 is processed next, it is skipped due to overlapping targets
			// on the default flavor rather than violating flavor stickiness to switch to the on-demand flavor.
			enableFairSharing: true,
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("root").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cq-1").
					Cohort("root").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "5").Obj(),
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "5").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("cq-2").
					Cohort("root").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "5").Obj(),
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "5").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq-1", "default").ClusterQueue("cq-1").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-2", "default").ClusterQueue("cq-2").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-admitted-default", "default").
					UID("wl-admitted-default-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-admitted-on-demand", "default").
					UID("wl-admitted-on-demand-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-1", "default").
					UID("wl-pending-high-prio-1-uid").
					JobUID("job-high-prio-1-uid").
					Queue("lq-1").
					Priority(11).
					Request(corev1.ResourceCPU, "10").
					Creation(now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-2", "default").
					UID("wl-pending-high-prio-2-uid").
					JobUID("job-high-prio-2-uid").
					Queue("lq-2").
					Priority(10).
					Request(corev1.ResourceCPU, "10").
					Creation(now.Add(time.Second)).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-admitted-default", "default").
					UID("wl-admitted-default-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-pending-high-prio-2-uid, JobUID: job-high-prio-2-uid) due to Fair Sharing within the cohort; preemptor path: /root/cq-2; preemptee path: /root/cq-1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortFairSharing",
						Message:            "Preempted to accommodate a workload (UID: wl-pending-high-prio-2-uid, JobUID: job-high-prio-2-uid) due to Fair Sharing within the cohort; preemptor path: /root/cq-2; preemptee path: /root/cq-1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-admitted-on-demand", "default").
					UID("wl-admitted-on-demand-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-1", "default").
					UID("wl-pending-high-prio-1-uid").
					JobUID("job-high-prio-1-uid").
					Queue("lq-1").
					Priority(11).
					Request(corev1.ResourceCPU, "10").
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 10 more needed, skipping flavor on-demand as it is not found in the nomination mapping for resource cpu",
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
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("10"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-2", "default").
					UID("wl-pending-high-prio-2-uid").
					JobUID("job-high-prio-2-uid").
					Queue("lq-2").
					Priority(10).
					Request(corev1.ResourceCPU, "10").
					Creation(now.Add(time.Second)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 10 more needed, insufficient unused quota for cpu in flavor on-demand, 10 more needed. Pending the preemption of 1 workload(s)",
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
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("10"),
						},
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/wl-admitted-default":   *utiltestingapi.MakeAdmission("cq-1").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "10").Obj()).Obj(),
				"default/wl-admitted-on-demand": *utiltestingapi.MakeAdmission("cq-1").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-2": {"default/wl-pending-high-prio-2"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-1": {"default/wl-pending-high-prio-1"},
			},
			// wl-pending-high-prio-1 overlapped on the default flavor with wl-pending-high-prio-2's
			// preemption target, recomputed with flavor stickiness enabled, but still couldn't
			// resolve the overlap — skipped.
			wantPreemptionTargetRecomputations: map[string]map[string]int{
				"cq-1": {"skipped": 1},
			},
		},

		"flavor stickiness with RecomputePreemptionTargetsUponOverlap and TAS disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.RecomputeAssignmentUponPreemptionTargetsOverlap: true,
				features.TopologyAwareScheduling:                         false,
				features.TASRecomputeAssignmentWithinSchedulingCycle:     false,
			},
			// Admitted state:
			// - wl-admitted-default (cq-1): uses 10 CPU on default flavor (borrowing 5)
			// - wl-admitted-on-demand (cq-1): uses 10 CPU on on-demand flavor (borrowing 5)
			//
			// Pending:
			// - wl-pending-high-prio-1 (cq-1): priority 11, requests 10 CPU (can fit by preempting its own admitted workloads)
			// - wl-pending-high-prio-2 (cq-2): priority 10, requests 10 CPU (needs to borrow, can fit by reclaiming from cq-1)
			//
			// Since cq-1 has much higher usage (20 CPU) than cq-2 (0 CPU), under Fair Sharing,
			// cq-2's pending workload (wl-pending-high-prio-2) is prioritized and processed first
			// despite having a lower priority than wl-pending-high-prio-1.
			//
			// wl-pending-high-prio-2 chooses to preempt wl-admitted-default on the default flavor.
			// When wl-pending-high-prio-1 is processed next, it is skipped due to overlapping targets
			// on the default flavor rather than violating flavor stickiness to switch to the on-demand flavor.
			enableFairSharing: true,
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("root").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cq-1").
					Cohort("root").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "5").Obj(),
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "5").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("cq-2").
					Cohort("root").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "5").Obj(),
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "5").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq-1", "default").ClusterQueue("cq-1").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-2", "default").ClusterQueue("cq-2").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-admitted-default", "default").
					UID("wl-admitted-default-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-admitted-on-demand", "default").
					UID("wl-admitted-on-demand-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-1", "default").
					UID("wl-pending-high-prio-1-uid").
					JobUID("job-high-prio-1-uid").
					Queue("lq-1").
					Priority(11).
					Request(corev1.ResourceCPU, "10").
					Creation(now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-2", "default").
					UID("wl-pending-high-prio-2-uid").
					JobUID("job-high-prio-2-uid").
					Queue("lq-2").
					Priority(10).
					Request(corev1.ResourceCPU, "10").
					Creation(now.Add(time.Second)).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-admitted-default", "default").
					UID("wl-admitted-default-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-pending-high-prio-2-uid, JobUID: job-high-prio-2-uid) due to Fair Sharing within the cohort; preemptor path: /root/cq-2; preemptee path: /root/cq-1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortFairSharing",
						Message:            "Preempted to accommodate a workload (UID: wl-pending-high-prio-2-uid, JobUID: job-high-prio-2-uid) due to Fair Sharing within the cohort; preemptor path: /root/cq-2; preemptee path: /root/cq-1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-admitted-on-demand", "default").
					UID("wl-admitted-on-demand-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-1", "default").
					UID("wl-pending-high-prio-1-uid").
					JobUID("job-high-prio-1-uid").
					Queue("lq-1").
					Priority(11).
					Request(corev1.ResourceCPU, "10").
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 10 more needed, skipping flavor on-demand as it is not found in the nomination mapping for resource cpu",
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
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("10"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-2", "default").
					UID("wl-pending-high-prio-2-uid").
					JobUID("job-high-prio-2-uid").
					Queue("lq-2").
					Priority(10).
					Request(corev1.ResourceCPU, "10").
					Creation(now.Add(time.Second)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 10 more needed, insufficient unused quota for cpu in flavor on-demand, 10 more needed. Pending the preemption of 1 workload(s)",
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
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("10"),
						},
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/wl-admitted-default":   *utiltestingapi.MakeAdmission("cq-1").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "10").Obj()).Obj(),
				"default/wl-admitted-on-demand": *utiltestingapi.MakeAdmission("cq-1").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-2": {"default/wl-pending-high-prio-2"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-1": {"default/wl-pending-high-prio-1"},
			},
		},

		"flavor stickiness with RecomputePreemptionTargetsUponOverlap and TAS recomputation disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.RecomputeAssignmentUponPreemptionTargetsOverlap: true,
				features.TopologyAwareScheduling:                         true,
				features.TASRecomputeAssignmentWithinSchedulingCycle:     false,
			},
			enableFairSharing: true,
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("root").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cq-1").
					Cohort("root").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "5").Obj(),
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "5").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("cq-2").
					Cohort("root").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "5").Obj(),
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "5").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq-1", "default").ClusterQueue("cq-1").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-2", "default").ClusterQueue("cq-2").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-admitted-default", "default").
					UID("wl-admitted-default-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-admitted-on-demand", "default").
					UID("wl-admitted-on-demand-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-1", "default").
					UID("wl-pending-high-prio-1-uid").
					JobUID("job-high-prio-1-uid").
					Queue("lq-1").
					Priority(11).
					Request(corev1.ResourceCPU, "10").
					Creation(now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-2", "default").
					UID("wl-pending-high-prio-2-uid").
					JobUID("job-high-prio-2-uid").
					Queue("lq-2").
					Priority(10).
					Request(corev1.ResourceCPU, "10").
					Creation(now.Add(time.Second)).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-admitted-default", "default").
					UID("wl-admitted-default-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-pending-high-prio-2-uid, JobUID: job-high-prio-2-uid) due to Fair Sharing within the cohort; preemptor path: /root/cq-2; preemptee path: /root/cq-1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortFairSharing",
						Message:            "Preempted to accommodate a workload (UID: wl-pending-high-prio-2-uid, JobUID: job-high-prio-2-uid) due to Fair Sharing within the cohort; preemptor path: /root/cq-2; preemptee path: /root/cq-1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-admitted-on-demand", "default").
					UID("wl-admitted-on-demand-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-1", "default").
					UID("wl-pending-high-prio-1-uid").
					JobUID("job-high-prio-1-uid").
					Queue("lq-1").
					Priority(11).
					Request(corev1.ResourceCPU, "10").
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 10 more needed, skipping flavor on-demand as it is not found in the nomination mapping for resource cpu",
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
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("10"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-2", "default").
					UID("wl-pending-high-prio-2-uid").
					JobUID("job-high-prio-2-uid").
					Queue("lq-2").
					Priority(10).
					Request(corev1.ResourceCPU, "10").
					Creation(now.Add(time.Second)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 10 more needed, insufficient unused quota for cpu in flavor on-demand, 10 more needed. Pending the preemption of 1 workload(s)",
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
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("10"),
						},
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/wl-admitted-default":   *utiltestingapi.MakeAdmission("cq-1").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "10").Obj()).Obj(),
				"default/wl-admitted-on-demand": *utiltestingapi.MakeAdmission("cq-1").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-2": {"default/wl-pending-high-prio-2"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-1": {"default/wl-pending-high-prio-1"},
			},
		},

		"legacy overlap skip with RecomputePreemptionTargetsUponOverlap disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.RecomputeAssignmentUponPreemptionTargetsOverlap: false,
				features.TopologyAwareScheduling:                         false,
				features.TASRecomputeAssignmentWithinSchedulingCycle:     false,
			},
			enableFairSharing: true,
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("root").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("cq-1").
					Cohort("root").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "5").Obj(),
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "5").Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("cq-2").
					Cohort("root").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "5").Obj(),
						*utiltestingapi.MakeFlavorQuotas("on-demand").
							Resource(corev1.ResourceCPU, "5").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lq-1", "default").ClusterQueue("cq-1").Obj(),
				*utiltestingapi.MakeLocalQueue("lq-2", "default").ClusterQueue("cq-2").Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-admitted-default", "default").
					UID("wl-admitted-default-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-admitted-on-demand", "default").
					UID("wl-admitted-on-demand-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-1", "default").
					UID("wl-pending-high-prio-1-uid").
					JobUID("job-high-prio-1-uid").
					Queue("lq-1").
					Priority(11).
					Request(corev1.ResourceCPU, "10").
					Creation(now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-2", "default").
					UID("wl-pending-high-prio-2-uid").
					JobUID("job-high-prio-2-uid").
					Queue("lq-2").
					Priority(10).
					Request(corev1.ResourceCPU, "10").
					Creation(now.Add(time.Second)).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-admitted-default", "default").
					UID("wl-admitted-default-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-pending-high-prio-2-uid, JobUID: job-high-prio-2-uid) due to Fair Sharing within the cohort; preemptor path: /root/cq-2; preemptee path: /root/cq-1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortFairSharing",
						Message:            "Preempted to accommodate a workload (UID: wl-pending-high-prio-2-uid, JobUID: job-high-prio-2-uid) due to Fair Sharing within the cohort; preemptor path: /root/cq-2; preemptee path: /root/cq-1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-admitted-on-demand", "default").
					UID("wl-admitted-on-demand-uid").
					Queue("lq-1").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-1", "default").
					UID("wl-pending-high-prio-1-uid").
					JobUID("job-high-prio-1-uid").
					Queue("lq-1").
					Priority(11).
					Request(corev1.ResourceCPU, "10").
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            "Workload has overlapping preemption targets with another workload",
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
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("10"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-pending-high-prio-2", "default").
					UID("wl-pending-high-prio-2-uid").
					JobUID("job-high-prio-2-uid").
					Queue("lq-2").
					Priority(10).
					Request(corev1.ResourceCPU, "10").
					Creation(now.Add(time.Second)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 10 more needed, insufficient unused quota for cpu in flavor on-demand, 10 more needed. Pending the preemption of 1 workload(s)",
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
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("10"),
						},
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/wl-admitted-default":   *utiltestingapi.MakeAdmission("cq-1").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "10").Obj()).Obj(),
				"default/wl-admitted-on-demand": *utiltestingapi.MakeAdmission("cq-1").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()).Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-1": {"default/wl-pending-high-prio-1"},
				"cq-2": {"default/wl-pending-high-prio-2"},
			},
			wantSkippedPreemptions: map[string]int{
				"cq-1": 1,
				"cq-2": 0,
			},
		},
		"sibling TAS flavor usage is updated for overlapping preemption targets": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:                         true,
				features.TASHandleOverlappingFlavors:                     true,
				features.TASRecomputeAssignmentWithinSchedulingCycle:     true,
				features.RecomputeAssignmentUponPreemptionTargetsOverlap: true,
				features.TASCachingRemainingResources:                    true,
			},
			cohorts: []kueue.Cohort{
				*utiltestingapi.MakeCohort("tas-cohort").Obj(),
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("preemptor-a").
					Cohort("tas-cohort").
					Preemption(kueue.ClusterQueuePreemption{ReclaimWithinCohort: kueue.PreemptionPolicyAny}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("flavor-a").
							Resource(corev1.ResourceCPU, "3").
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("preemptor-b").
					Cohort("tas-cohort").
					Preemption(kueue.ClusterQueuePreemption{ReclaimWithinCohort: kueue.PreemptionPolicyAny}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("flavor-b").
							Resource(corev1.ResourceCPU, "3").
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("overlapping").
					Cohort("tas-cohort").
					Preemption(kueue.ClusterQueuePreemption{ReclaimWithinCohort: kueue.PreemptionPolicyAny}).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("flavor-b").
							Resource(corev1.ResourceCPU, "3").
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("victim-a").
					Cohort("tas-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("flavor-a").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").BorrowingLimit("3").Append().
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeClusterQueue("victim-b").
					Cohort("tas-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("flavor-b").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").BorrowingLimit("5").Append().
							Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("preemptor-a", "default").ClusterQueue("preemptor-a").Obj(),
				*utiltestingapi.MakeLocalQueue("preemptor-b", "default").ClusterQueue("preemptor-b").Obj(),
				*utiltestingapi.MakeLocalQueue("overlapping", "default").ClusterQueue("overlapping").Obj(),
				*utiltestingapi.MakeLocalQueue("victim-a", "default").ClusterQueue("victim-a").Obj(),
				*utiltestingapi.MakeLocalQueue("victim-b", "default").ClusterQueue("victim-b").Obj(),
			},
			additionalResourceFlavors: []kueue.ResourceFlavor{
				*utiltestingapi.MakeResourceFlavor("flavor-a").
					NodeLabel("flavor-a", "true").
					TopologyName("tas-single-level").
					Obj(),
				*utiltestingapi.MakeResourceFlavor("flavor-b").
					NodeLabel("flavor-b", "true").
					TopologyName("tas-single-level").
					Obj(),
			},
			topologies: []kueue.Topology{
				*utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level"),
			},
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("flavor-a", "true").
					Label("flavor-b", "true").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("5"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("y1").
					Label("flavor-b", "true").
					Label(corev1.LabelHostname, "y1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("5"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("z1").
					Label("flavor-a", "true").
					Label(corev1.LabelHostname, "z1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("5"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			// flavor-a selects x1/z1, flavor-b selects x1/y1, and x1 is shared.
			// The first two pending workloads preempt one victim in each flavor and
			// reserve z1 and y1. The third initially nominates y1 by targeting victim-b.
			// Once y1 is reserved, overlap recomputation temporarily removes both
			// victims and must observe victim-a's x1 removal through flavor-b.
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("victim-a", "default").
					UID("victim-a-uid").
					Queue("victim-a").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						NodeSelector(map[string]string{corev1.LabelHostname: "x1"}).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("victim-a").
							PodSets(utiltestingapi.MakePodSetAssignment("main").
								Assignment(corev1.ResourceCPU, "flavor-a", "3").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("victim-b", "default").
					UID("victim-b-uid").
					Queue("victim-b").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						NodeSelector(map[string]string{corev1.LabelHostname: "y1"}).
						// Fills y1, leaving x1 as the only candidate for the overlapping Workload.
						Request(corev1.ResourceCPU, "5").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("victim-b").
							PodSets(utiltestingapi.MakePodSetAssignment("main").
								Assignment(corev1.ResourceCPU, "flavor-b", "5").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor-a", "default").
					UID("preemptor-a-uid").
					JobUID("preemptor-a-job-uid").
					Queue("preemptor-a").
					Priority(30).
					Creation(now).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						// Keeps preemptor-a off x1 so nothing but the overlapping Workload
						// can take the capacity victim-a frees there.
						NodeSelector(map[string]string{corev1.LabelHostname: "z1"}).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor-b", "default").
					UID("preemptor-b-uid").
					JobUID("preemptor-b-job-uid").
					Queue("preemptor-b").
					Priority(20).
					Creation(now.Add(time.Second)).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						NodeSelector(map[string]string{corev1.LabelHostname: "y1"}).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("overlapping", "default").
					UID("overlapping-uid").
					JobUID("overlapping-job-uid").
					Queue("overlapping").
					Priority(10).
					Creation(now.Add(2 * time.Second)).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("overlapping", "default").
					UID("overlapping-uid").
					JobUID("overlapping-job-uid").
					Queue("overlapping").
					Priority(10).
					Creation(now.Add(2 * time.Second)).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "Workload has overlapping preemption targets with another workload, but will fit after these preemptions complete",
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
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("3"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor-a", "default").
					UID("preemptor-a-uid").
					JobUID("preemptor-a-job-uid").
					Queue("preemptor-a").
					Priority(30).
					Creation(now).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						NodeSelector(map[string]string{corev1.LabelHostname: "z1"}).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor flavor-a, 3 more needed. Pending the preemption of 1 workload(s)",
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
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("3"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("preemptor-b", "default").
					UID("preemptor-b-uid").
					JobUID("preemptor-b-job-uid").
					Queue("preemptor-b").
					Priority(20).
					Creation(now.Add(time.Second)).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						NodeSelector(map[string]string{corev1.LabelHostname: "y1"}).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor flavor-b, 2 more needed. Pending the preemption of 1 workload(s)",
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
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("3"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("victim-a", "default").
					UID("victim-a-uid").
					Queue("victim-a").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						NodeSelector(map[string]string{corev1.LabelHostname: "x1"}).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("victim-a").
							PodSets(utiltestingapi.MakePodSetAssignment("main").
								Assignment(corev1.ResourceCPU, "flavor-a", "3").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: preemptor-a-uid, JobUID: preemptor-a-job-uid) due to reclamation within the cohort; preemptor path: /tas-cohort/preemptor-a; preemptee path: /tas-cohort/victim-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: preemptor-a-uid, JobUID: preemptor-a-job-uid) due to reclamation within the cohort; preemptor path: /tas-cohort/preemptor-a; preemptee path: /tas-cohort/victim-a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("victim-b", "default").
					UID("victim-b-uid").
					Queue("victim-b").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						NodeSelector(map[string]string{corev1.LabelHostname: "y1"}).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("victim-b").
							PodSets(utiltestingapi.MakePodSetAssignment("main").
								Assignment(corev1.ResourceCPU, "flavor-b", "5").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
									Obj()).
								Obj()).
							Obj(),
						now,
					).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: preemptor-b-uid, JobUID: preemptor-b-job-uid) due to reclamation within the cohort; preemptor path: /tas-cohort/preemptor-b; preemptee path: /tas-cohort/victim-b",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: preemptor-b-uid, JobUID: preemptor-b-job-uid) due to reclamation within the cohort; preemptor path: /tas-cohort/preemptor-b; preemptee path: /tas-cohort/victim-b",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/victim-a": *utiltestingapi.MakeAdmission("victim-a").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "flavor-a", "3").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
				"default/victim-b": *utiltestingapi.MakeAdmission("victim-b").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "flavor-b", "5").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"y1"}, 1).Obj()).
							Obj()).
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"overlapping": {"default/overlapping"},
				"preemptor-a": {"default/preemptor-a"},
				"preemptor-b": {"default/preemptor-b"},
			},
			// The recomputation only fits because preemptor-b's preemption of victim-b
			// frees y1, so the overlapping workload defers instead of picking new targets.
			wantPreemptionTargetRecomputations: map[string]map[string]int{
				"overlapping": {"deferred_fit": 1},
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
