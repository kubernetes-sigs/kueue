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
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestHierarchicalPreemptions(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	flavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("default").Obj(),
	}
	baseIncomingWl := utiltestingapi.MakeWorkload("in", "").
		UID("wl-in").
		Label(controllerconstants.JobUIDLabel, "job-in")
	cases := map[string]struct {
		clusterQueues []*kueue.ClusterQueue
		cohorts       []*kueue.Cohort
		admitted      []kueue.Workload
		incoming      *kueue.Workload
		targetCQ      kueue.ClusterQueueReference
		assignment    flavorassigner.Assignment
		wantPreempted int
		wantWorkloads []kueue.Workload
	}{
		//
		//            R
		//      /      |
		//   C(2) q_borrowing(0)
		//  /
		// q
		"preempt with hierarchical advantage": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("r").Obj(),
				utiltestingapi.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q_borrowing").
					Cohort("r").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(0).
				Request(corev1.ResourceCPU, "2").
				Obj(),
			targetCQ: "q",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c/q; preemptee path: /r/q_borrowing",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c/q; preemptee path: /r/q_borrowing",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
		},
		//
		//             R
		//      /      |         \
		//   C(2) q_borrowing(0)  q_nominal(2)
		//  /
		// q
		"avoid queues within nominal quota": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("r").Obj(),
				utiltestingapi.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("q_nominal").
					Cohort("r").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "2").
					Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q_borrowing").
					Cohort("r").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted1", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_nominal").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(0).
				Request(corev1.ResourceCPU, "2").
				Obj(),
			targetCQ: "q",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted1", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_nominal").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c/q; preemptee path: /r/q_borrowing",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c/q; preemptee path: /r/q_borrowing",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
		},
		//
		//            R(0)
		//      /      |
		//   C(2) q_borrowing(0)
		//  /
		// q(0)
		"preempt multiple with hierarchical advantage": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("r").Obj(),
				utiltestingapi.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q_borrowing").
					Cohort("r").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted1", "").
					Priority(1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted2", "").
					Priority(2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(0).
				Request(corev1.ResourceCPU, "2").
				Obj(),
			targetCQ: "q",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 2,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted1", "").
					Priority(1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c/q; preemptee path: /r/q_borrowing",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c/q; preemptee path: /r/q_borrowing",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted2", "").
					Priority(2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c/q; preemptee path: /r/q_borrowing",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c/q; preemptee path: /r/q_borrowing",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
		},
		//
		//        R(0)
		//      /
		//   C(3)
		//  /   \
		// q(0) q_same_cohort(0)
		"preempt in cohort and own CQ": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("r").Obj(),
				utiltestingapi.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
							MaxPriorityThreshold: new(int32(0)),
						},
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q_same_cohort").
					Cohort("c").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted_not_preemptible", "").
					Priority(1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_same_cohort").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_preemptible", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_same_cohort").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_own_queue", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(1).
				Request(corev1.ResourceCPU, "2").
				Obj(),
			targetCQ: "q",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 2,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted_not_preemptible", "").
					Priority(1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_same_cohort").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_own_queue", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /r/c/q; preemptee path: /r/c/q",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /r/c/q; preemptee path: /r/c/q",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_preemptible", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_same_cohort").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort while borrowing; preemptor path: /r/c/q; preemptee path: /r/c/q_same_cohort",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclaimWhileBorrowing",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort while borrowing; preemptor path: /r/c/q; preemptee path: /r/c/q_same_cohort",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
		},
		//
		//            R(0)
		//      /      |
		//   C(2) q_borrowing(0)
		//  /
		// q(0)
		"prefer to preempt hierarchical candidate": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("r").Obj(),
				utiltestingapi.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q_borrowing").
					Cohort("r").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted_borrowing", "").
					Priority(1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_same_queue", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(0).
				Request(corev1.ResourceCPU, "1").
				Obj(),
			targetCQ: "q",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted_borrowing", "").
					Priority(1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c/q; preemptee path: /r/q_borrowing",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c/q; preemptee path: /r/q_borrowing",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_same_queue", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Obj(),
			},
		},
		//
		//           R(0)
		//      /      |
		//   C(2)   q_nominal(2)
		//  /   \
		// q(0) q_same_cohort(0)
		"forced to preempt priority candidate": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("r").Obj(),
				utiltestingapi.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
							MaxPriorityThreshold: new(int32(0)),
						},
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q_nominal").
					Cohort("r").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").
						Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q_same_cohort").
					Cohort("c").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted_nominal", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_nominal").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_same_cohort", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_same_cohort").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(0).
				Request(corev1.ResourceCPU, "2").
				Obj(),
			targetCQ: "q",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted_nominal", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_nominal").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_same_cohort", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_same_cohort").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort while borrowing; preemptor path: /r/c/q; preemptee path: /r/c/q_same_cohort",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclaimWhileBorrowing",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort while borrowing; preemptor path: /r/c/q; preemptee path: /r/c/q_same_cohort",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
		},
		//
		//           R(0)
		//      /      |
		//   C(2)   q_borrowing(0)
		//  /    \
		// q(4)  q_same_cohort(0)
		//
		"incoming workload fits in CQ nominal quota": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("r").Obj(),
				utiltestingapi.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "4").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q_borrowing").
					Cohort("r").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q_same_cohort").
					Cohort("c").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted_borrowing", "").
					Priority(10).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_same_cohort", "").
					Priority(10).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_same_cohort").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj(), now).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(0).
				Request(corev1.ResourceCPU, "4").
				Obj(),
			targetCQ: "q",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 2,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted_borrowing", "").
					Priority(10).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c/q; preemptee path: /r/q_borrowing",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c/q; preemptee path: /r/q_borrowing",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_same_cohort", "").
					Priority(10).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_same_cohort").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c/q; preemptee path: /r/c/q_same_cohort",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c/q; preemptee path: /r/c/q_same_cohort",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
		},
		//
		//           R(1)
		//      /      |
		//   C(4)   q_borrowing(0)
		//  /    \
		// q(0)  q_same_cohort(0)
		//
		"preempt hierarchical and priority candidates": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("r").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "1").
					Obj()).Obj(),
				utiltestingapi.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
							MaxPriorityThreshold: new(int32(0)),
						},
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q_borrowing").
					Cohort("r").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q_same_cohort").
					Cohort("c").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted_borrowing", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_same_cohort_preemptible", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_same_cohort").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_not_preemptible", "").
					Priority(1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(0).
				Request(corev1.ResourceCPU, "3").
				Obj(),
			targetCQ: "q",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 2,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted_borrowing", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c/q; preemptee path: /r/q_borrowing",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c/q; preemptee path: /r/q_borrowing",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_not_preemptible", "").
					Priority(1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_same_cohort_preemptible", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_same_cohort").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort while borrowing; preemptor path: /r/c/q; preemptee path: /r/c/q_same_cohort",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclaimWhileBorrowing",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort while borrowing; preemptor path: /r/c/q; preemptee path: /r/c/q_same_cohort",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
		},
		//
		//           R(1)
		//      /      |
		//   C(4)   q_borrowing(0)
		//  /    \
		// q(0)  q_same_cohort(0)
		//
		"preempt hierarchical candidates and inside CQ": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("r").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "1").
					Obj()).Obj(),
				utiltestingapi.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
							MaxPriorityThreshold: new(int32(0)),
						},
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q_borrowing").
					Cohort("r").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q_same_cohort").
					Cohort("c").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted_borrowing", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_same_queue_preemptible", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_not_preemptible", "").
					Priority(1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(0).
				Request(corev1.ResourceCPU, "3").
				Obj(),
			targetCQ: "q",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 2,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted_borrowing", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c/q; preemptee path: /r/q_borrowing",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c/q; preemptee path: /r/q_borrowing",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_not_preemptible", "").
					Priority(1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_same_queue_preemptible", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /r/c/q; preemptee path: /r/c/q",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /r/c/q; preemptee path: /r/c/q",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
		},
		//
		//            R(0)
		//      /      |          \
		//   C(3) q_borrowing(0)  q_nominal(2)
		//  /
		// q(0)
		"reclaim nominal quota from lowest priority workload, excluding non-borrowing": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("r").Obj(),
				utiltestingapi.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q_borrowing").
					Cohort("r").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q_nominal").
					Cohort("r").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "2").
					Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted_borrowing_prio_8", "").
					Priority(8).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_prio_9", "").
					Priority(9).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_prio_10", "").
					Priority(9).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_nominal", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_nominal").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(0).
				Request(corev1.ResourceCPU, "1").
				Obj(),
			targetCQ: "q",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted_borrowing_prio_10", "").
					Priority(9).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_prio_8", "").
					Priority(8).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c/q; preemptee path: /r/q_borrowing",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c/q; preemptee path: /r/q_borrowing",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_prio_9", "").
					Priority(9).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_nominal", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_nominal").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
			},
		},
		//
		//                  R
		//            /            \
		//      C(2)                   C_other(2)
		//     /    \                     |
		//    q(0)  q_same_cohort(0)   q_other(0)
		"infeasible preemption all available workloads in pruned subtrees": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("r").Obj(),
				utiltestingapi.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").
						Obj()).Obj(),
				utiltestingapi.MakeCohort("c_other").
					Parent("r").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("q_other").
					Cohort("c_other").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q_same_cohort").
					Cohort("c").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted_other_1", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_other").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_other_2", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_other").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_same_cohort", "").
					Priority(0).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_same_cohort").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(0).
				Request(corev1.ResourceCPU, "2").
				Obj(),
			targetCQ: "q",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted_other_1", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_other").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_other_2", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_other").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_same_cohort", "").
					Priority(0).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_same_cohort").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
			},
		},
		//
		//          R(3CPU, 0Gi)
		//      /      |
		//   C(4CPU,4Gi) q_borrowing(0)
		//  /    \
		// q(0)   q_same_cohort(0)
		"hiearchical preemption with multiple resources": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("r").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").
						Obj()).Obj(),
				utiltestingapi.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").
						Resource(corev1.ResourceMemory, "4Gi").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q_borrowing").
					Cohort("r").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q_same_cohort").
					Cohort("c").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted_borrowing", "").
					Priority(0).
					Request(corev1.ResourceCPU, "3").
					Request(corev1.ResourceMemory, "1Gi").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").
							Assignment(corev1.ResourceMemory, "default", "1Gi").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_same_cohort", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "3Gi").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_same_cohort").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").
							Assignment(corev1.ResourceMemory, "default", "3Gi").Obj()).Obj(), now).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(-2).
				Request(corev1.ResourceCPU, "2").
				Request(corev1.ResourceMemory, "1Gi").
				Obj(),
			targetCQ: "q",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
				corev1.ResourceMemory: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted_borrowing", "").
					Priority(0).
					Request(corev1.ResourceCPU, "3").
					Request(corev1.ResourceMemory, "1Gi").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").
							Assignment(corev1.ResourceMemory, "default", "1Gi").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c/q; preemptee path: /r/q_borrowing",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c/q; preemptee path: /r/q_borrowing",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_same_cohort", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "3Gi").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_same_cohort").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").
							Assignment(corev1.ResourceMemory, "default", "3Gi").Obj()).Obj(), now).
					Obj(),
			},
		},
		//
		//           R(0)
		//      /      |
		//   C(2)   q_borrowing(0)
		//  /    \
		// q(0)  q_same_cohort(0)
		//
		"prefer to preempt evicted workloads": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("r").Obj(),
				utiltestingapi.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
							MaxPriorityThreshold: new(int32(0)),
						},
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q_borrowing").
					Cohort("r").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q_same_cohort").
					Cohort("c").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted_borrowing", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("evicted_same_cohort", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_same_cohort").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.NewTime(now),
					}).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(0).
				Request(corev1.ResourceCPU, "1").
				Obj(),
			targetCQ: "q",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 1, // preemption on going
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted_borrowing", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("evicted_same_cohort", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_same_cohort").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.NewTime(now),
					}).
					Obj(),
			},
		},
		//
		//           R(0)
		//      /      |
		//   C(2)   q_borrowing(0)
		//  /
		// q(3, lending limit 2)
		//
		"respect lending limits": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("r").Obj(),
				utiltestingapi.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3", "", "2").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q_borrowing").
					Cohort("r").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted_borrowing", "").
					Priority(0).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "4").Obj()).Obj(), now).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(-2).
				Request(corev1.ResourceCPU, "5").
				Obj(),
			targetCQ: "q",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted_borrowing", "").
					Priority(0).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "4").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c/q; preemptee path: /r/q_borrowing",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c/q; preemptee path: /r/q_borrowing",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
		},
		//                                r
		//                             /      \
		//                          c11        c12
		//                       /   |   \       \
		//                    c21   c22    c23    q1
		//                  /  |     |     |
		//                c31  c32   q3    q2
		//              /      |
		//            q5       q4
		//	quotas:
		//	4: c11, c12, c21, c22, c23, c32, c31
		//	0: q1, q3, q4, q5
		"reclaim in complex hierarchy": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("r").Obj(),
				utiltestingapi.MakeCohort("c11").
					Parent("r").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").
						Obj()).Obj(),
				utiltestingapi.MakeCohort("c12").
					Parent("r").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").
						Obj()).Obj(),
				utiltestingapi.MakeCohort("c21").
					Parent("c11").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").
						Obj()).Obj(),
				utiltestingapi.MakeCohort("c22").
					Parent("c11").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").
						Obj()).Obj(),
				utiltestingapi.MakeCohort("c23").
					Parent("c11").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").
						Obj()).Obj(),
				utiltestingapi.MakeCohort("c31").
					Parent("c21").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").
						Obj()).Obj(),
				utiltestingapi.MakeCohort("c32").
					Parent("c21").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("q1").
					Cohort("c12").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltestingapi.MakeClusterQueue("q2").
					Cohort("c23").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltestingapi.MakeClusterQueue("q3").
					Cohort("c22").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltestingapi.MakeClusterQueue("q4").
					Cohort("c32").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltestingapi.MakeClusterQueue("q5").
					Cohort("c31").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted_borrowing_1", "").
					Priority(-6).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "4").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_2", "").
					Priority(-5).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "4").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_3", "").
					Priority(-9).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q2").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "4").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_4", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q2").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "4").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_5", "").
					Priority(-4).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q3").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "4").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_6", "").
					Priority(-3).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q3").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_7", "").
					Priority(4).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q4").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_8", "").
					Priority(2).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q4").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj(), now).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(-2).
				Request(corev1.ResourceCPU, "7").
				Obj(),
			targetCQ: "q5",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			// only one of workloads from q2 will be preempted because
			// after preempting the first one, the usage of cohort
			// c23 will be back within nominal quota
			wantPreempted: 2,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted_borrowing_1", "").
					Priority(-6).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "4").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c11/c21/c31/q5; preemptee path: /r/c12/q1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c11/c21/c31/q5; preemptee path: /r/c12/q1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_2", "").
					Priority(-5).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "4").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_3", "").
					Priority(-9).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q2").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "4").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_4", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q2").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "4").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c11/c21/c31/q5; preemptee path: /r/c11/c23/q2",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /r/c11/c21/c31/q5; preemptee path: /r/c11/c23/q2",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_5", "").
					Priority(-4).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q3").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "4").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_6", "").
					Priority(-3).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q3").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_7", "").
					Priority(4).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q4").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_8", "").
					Priority(2).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q4").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj(), now).
					Obj(),
			},
		},
		//
		//            R(0)
		//        /          \
		//   q_exceed(2,       q_other(4)
		//     borrowLimit 2)
		//
		// q_exceed borrows the full extra 2 (usage 4 = nominal 2 + borrowingLimit 2),
		// borrowed from q_other's idle nominal. incoming(1) into q_exceed:
		// usage(4)+1 = 5 > nominal(2)+borrowingLimit(2) = 4 => band == exceedsBorrowing.
		// Borrowing more from the cohort can never lift the per-queue borrowing limit,
		// so q_other's workload is useless as a candidate; only reclaiming q_exceed's
		// own usage frees space. We preempt the in-queue workload and, once its 4 units
		// are freed, incoming(1) fits within nominal. q_other is left untouched.
		"exceeds borrowing limit: reclaim within queue only": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("r").Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("q_exceed").
					Cohort("r").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "2", "2").
					Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q_other").
					Cohort("r").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "4").
					Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("in_queue_low", "").
					Priority(-5).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_exceed").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "4").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("other_queue_low", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_other").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(0).
				Request(corev1.ResourceCPU, "1").
				Obj(),
			targetCQ: "q_exceed",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("in_queue_low", "").
					Priority(-5).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_exceed").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "4").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /r/q_exceed; preemptee path: /r/q_exceed",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /r/q_exceed; preemptee path: /r/q_exceed",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("other_queue_low", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_other").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
			},
		},
		//            R(0)
		//        /          \
		//     q(2,          donor(2)
		//  borrowLimit 2)
		// q usage 1, donor usage 3 (borrowing 1 of q's lent nominal),
		// incoming(4) into q: usage(1)+4 = 5 > nominal(2)+borrowingLimit(2) = 4.
		// Same-queue preemption alone is insufficient: after preempting q's own
		// workload, q still needs to borrow 2 but the cohort has only 1 free,
		// so donor's workload must be preempted to free cohort quota.
		"exceeds borrowing limit with full cohort: cross-queue reclaim needed": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("r").Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("q").
					Cohort("r").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "2", "2").
					Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
						},
					}).Obj(),
				utiltestingapi.MakeClusterQueue("donor").
					Cohort("r").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "2").
					Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("q_low", "").
					Priority(-5).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("donor_low", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("donor").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj(), now).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(10).
				Request(corev1.ResourceCPU, "4").
				Obj(),
			targetCQ: "q",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 2,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("donor_low", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("donor").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort while borrowing; preemptor path: /r/q; preemptee path: /r/donor",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclaimWhileBorrowing",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort while borrowing; preemptor path: /r/q; preemptee path: /r/donor",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(), *utiltestingapi.MakeWorkload("q_low", "").
					Priority(-5).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /r/q; preemptee path: /r/q",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /r/q; preemptee path: /r/q",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
		},
		// cohort has slack but it is occupied by a borrowing sibling, so
		// same-queue preemption alone cannot free it.
		//              R(0)
		//        /       |        \
		//     q(4,     donor(2)   idle(6)
		//  borrowLimit 4)
		// q usage 1 (below nominal, not borrowing), donor usage 5 (borrowing 3),
		// idle usage 0 -> cohort free = 12-6 = 6.
		// incoming(8) into q: usage(1)+8 = 9 > nominal(4)+borrowingLimit(4) = 8.
		// After preempting q's own workload, q can still only pull
		// min(withMaxFromParent 8, parentAvailable 7) = 7 < 8, because donor's
		// borrowed 5 keeps parentAvailable below q's borrowing cap. Only
		// reclaiming donor frees the cohort quota q needs, so the cross-queue
		// candidate must be kept. The pre-fix "needed = after - nominal = 5"
		// wrongly judged cohort free (6) >= 5 and pruned the donor candidate,
		// yielding 0 preemptions; "needed = request - usage = 7" > 6 keeps it.
		"exceeds borrowing limit but cohort slack is held by a borrowing sibling: cross-queue reclaim needed": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("r").Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("q").
					Cohort("r").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "4", "4").
					Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
						},
					}).Obj(),
				utiltestingapi.MakeClusterQueue("donor").
					Cohort("r").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "2").
					Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltestingapi.MakeClusterQueue("idle").
					Cohort("r").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "6").
					Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("q_low", "").
					Priority(-5).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("donor_low", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "5").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("donor").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "5").Obj()).Obj(), now).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(10).
				Request(corev1.ResourceCPU, "8").
				Obj(),
			targetCQ: "q",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 2,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("donor_low", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "5").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("donor").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "5").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort while borrowing; preemptor path: /r/q; preemptee path: /r/donor",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclaimWhileBorrowing",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort while borrowing; preemptor path: /r/q; preemptee path: /r/donor",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(), *utiltestingapi.MakeWorkload("q_low", "").
					Priority(-5).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /r/q; preemptee path: /r/q",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /r/q; preemptee path: /r/q",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
		},
		//              R(0)
		//        /      |       \
		//     q(2,   q_other(2)  q_idle(10)
		//  borrowLimit 2)
		// q usage 4 (at cap), q_other usage 3 (over nominal -> a legal
		// cross-queue candidate), q_idle usage 0 -> cohort has 7 free.
		// incoming(1) into q: usage(4)+1 = 5 > nominal(2)+borrowingLimit(2) = 4,
		// and needed = request(1) - usage(4) <= 0 <= cohort free 7 => band ==
		// exceedsBorrowing, cross-queue collection is skipped. On the base
		// revision the same outcome must hold: other_low may be transiently
		// preempted first, but fillBackWorkloads adds it back because same-queue
		// preemption alone suffices when the cohort supply is not the binding
		// constraint.
		"exceeds borrowing limit with idle cohort: same-queue preemption only, cross-queue candidate untouched": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("r").Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("q").
					Cohort("r").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "2", "2").
					Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
						},
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q_other").
					Cohort("r").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "2").
					Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q_idle").
					Cohort("r").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "10").
					Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("q_low", "").
					Priority(-5).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "4").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("other_low", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_other").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj(), now).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(10).
				Request(corev1.ResourceCPU, "1").
				Obj(),
			targetCQ: "q",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("other_low", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_other").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("q_low", "").
					Priority(-5).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "4").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /r/q; preemptee path: /r/q",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /r/q; preemptee path: /r/q",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
		},
		//
		//
		//            R(0)
		//        /         \
		//   q_exceed(2,      q_other(2)
		//     borrowLimit 2,
		//     withinCQ Never)
		//
		// Same exceedsBorrowing topology, but q_exceed forbids in-queue preemption.
		// sameQueueCandidates is empty and no other-queue candidate can help, so
		// nothing is preempted.
		"exceeds borrowing limit, within-CQ never: no candidate": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("r").Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("q_exceed").
					Cohort("r").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "2", "2").
					Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyNever,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q_other").
					Cohort("r").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "2").
					Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("in_queue_low", "").
					Priority(-5).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_exceed").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("other_queue_low", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_other").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(0).
				Request(corev1.ResourceCPU, "3").
				Obj(),
			targetCQ: "q_exceed",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 0,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("in_queue_low", "").
					Priority(-5).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_exceed").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("other_queue_low", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_other").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
			},
		},
		//
		//            R(0)
		//        /         \
		//   q_borrow(2)      q_donor(4)
		//
		// q_borrow already borrows (usage 3 > nominal 2), incoming(1) keeps it
		// borrowing (band == withinBorrowing) with no hierarchical advantage.
		// BorrowWithinCohort is nil (not configured) => the special case must treat
		// borrowing-within-cohort as forbidden without a nil-pointer panic, and fall
		// back to same-queue reclamation. Regression guard for that nil dereference.
		//
		// The cohort must be genuinely full so that Preempt is the honest mode:
		// cohort total = q_borrow(2) + q_donor(4) = 6, borrower_low uses 3 in
		// q_borrow (borrowing 1) and donor_mid uses 3 in q_donor (within its own
		// nominal, so it is NOT a candidate). Together they fill the cohort (6/6),
		// so q_borrow.Available == 0 and incoming(1) cannot fit without preemption.
		// donor_mid stays within nominal => no other-queue candidate; only same-queue
		// reclamation of borrower_low frees room.
		"borrowing needed, BorrowWithinCohort nil: reclaim within queue": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("r").Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("q_borrow").
					Cohort("r").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "2").
					Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltestingapi.MakeClusterQueue("q_donor").
					Cohort("r").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "4").
					Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("borrower_low", "").
					Priority(-5).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrow").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("donor_mid", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_donor").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj(), now).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(0).
				Request(corev1.ResourceCPU, "1").
				Obj(),
			targetCQ: "q_borrow",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("borrower_low", "").
					Priority(-5).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_borrow").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /r/q_borrow; preemptee path: /r/q_borrow",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /r/q_borrow; preemptee path: /r/q_borrow",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("donor_mid", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q_donor").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj(), now).
					Obj(),
			},
		},
		//
		//             c(cpu=2, mem=2Gi)
		//        /                    \
		// q(cpu=2/lim0, mem=1Gi)   donor(cpu=0, mem=1Gi)
		//
		// same_queue_cpu fills q's cpu (2/2); donor_mem borrows q's unused
		// memory nominal (using 2Gi of the cohort's 2Gi). The incoming workload
		// needs 1 cpu + 1Gi memory: cpu exceeds q's borrowing limit
		// (usage 2 + 1 > nominal 2 + limit 0), while memory only lacks cohort
		// capacity. One resource exceeding the borrowing limit must not skip the
		// hierarchy/priority pools, because cross-queue reclaim is still
		// required for the other resource.
		"mixed resources: only cpu exceeds borrowing limit, memory still reclaimed across queues": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("c").Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "2", "0").
					Resource(corev1.ResourceMemory, "1Gi").
					Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
						},
					}).Obj(),
				utiltestingapi.MakeClusterQueue("donor").
					Cohort("c").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "0").
					Resource(corev1.ResourceMemory, "1Gi").
					Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("same_queue_cpu", "").
					Priority(0).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("donor_mem", "").
					Priority(0).
					Request(corev1.ResourceMemory, "2Gi").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("donor").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceMemory, "default", "2Gi").Obj()).Obj(), now).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(1).
				Request(corev1.ResourceCPU, "1").
				Request(corev1.ResourceMemory, "1Gi").
				Obj(),
			targetCQ: "q",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
				corev1.ResourceMemory: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 2,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("donor_mem", "").
					Priority(0).
					Request(corev1.ResourceMemory, "2Gi").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("donor").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceMemory, "default", "2Gi").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort while borrowing; preemptor path: /c/q; preemptee path: /c/donor",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclaimWhileBorrowing",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort while borrowing; preemptor path: /c/q; preemptee path: /c/donor",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("same_queue_cpu", "").
					Priority(0).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /c/q; preemptee path: /c/q",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /c/q; preemptee path: /c/q",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
		},
		//
		//             c(cpu=10, mem=20Gi)
		//        /                       \
		// q(cpu=10, mem=10Gi)          b(cpu=0, mem=10Gi)
		//
		// q borrows cpu (xy_hog uses 11 of nominal 10), but cpu does NOT need
		// preemption. b overuses memory (25Gi of the cohort's 20Gi), so q's
		// Available(memory) is 0 and memory needs preemption even though q's
		// own memory usage is far below nominal. Borrowing a resource that is
		// not up for preemption must not cause the priority candidates to be
		// dropped: the consumer still runs the no-borrowing pass, in which
		// ReclaimWithoutBorrowing candidates like b_mem are valid.
		"borrowing a resource not needing preemption: priority candidates kept": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("c").Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "10").
					Resource(corev1.ResourceMemory, "10Gi").
					Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						// BorrowWithinCohort is unset, so borrowing within the
						// cohort is forbidden.
					}).Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("c").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "0").
					Resource(corev1.ResourceMemory, "10Gi").
					Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("xy_hog", "").
					Priority(0).
					Request(corev1.ResourceCPU, "11").
					Request(corev1.ResourceMemory, "1Gi").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "11").
							Assignment(corev1.ResourceMemory, "default", "1Gi").Obj()).Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("b_mem", "").
					Priority(0).
					Request(corev1.ResourceMemory, "25Gi").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("b").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceMemory, "default", "25Gi").Obj()).Obj(), now).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(1).
				Request(corev1.ResourceCPU, "1").
				Request(corev1.ResourceMemory, "1Gi").
				Obj(),
			targetCQ: "q",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Fit,
				},
				corev1.ResourceMemory: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 2,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("b_mem", "").
					Priority(0).
					Request(corev1.ResourceMemory, "25Gi").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("b").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceMemory, "default", "25Gi").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /c/q; preemptee path: /c/b",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /c/q; preemptee path: /c/b",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(), *utiltestingapi.MakeWorkload("xy_hog", "").
					Priority(0).
					Request(corev1.ResourceCPU, "11").
					Request(corev1.ResourceMemory, "1Gi").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "11").
							Assignment(corev1.ResourceMemory, "default", "1Gi").Obj()).Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /c/q; preemptee path: /c/q",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /c/q; preemptee path: /c/q",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
		},
	}
	for name, tc := range cases {
		for _, useMergePatch := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s when the WorkloadRequestUseMergePatch feature is %t", name, useMergePatch), func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, useMergePatch)

				ctx, log := utiltesting.ContextWithLog(t)
				cl := utiltesting.NewClientBuilder().
					WithLists(&kueue.WorkloadList{Items: tc.admitted}).
					WithStatusSubresource(&kueue.Workload{}).
					WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge}).
					Build()

				cqCache := schdcache.New(cl)
				for _, flv := range flavors {
					cqCache.AddOrUpdateResourceFlavor(log, flv)
				}
				for _, cq := range tc.clusterQueues {
					if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
						t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
					}
				}
				for _, cohort := range tc.cohorts {
					if err := cqCache.AddOrUpdateCohort(cohort); err != nil {
						t.Fatalf("Couldn't add Cohort to cache: %v", err)
					}
				}

				recorder := &utiltesting.EventRecorder{}
				preemptor := New(cl, workload.Ordering{}, recorder, nil, false, clocktesting.NewFakeClock(now), nil, preemptexpectations.New(), nil)

				beforeSnapshot, err := cqCache.Snapshot(ctx)
				if err != nil {
					t.Fatalf("unexpected error while building snapshot: %v", err)
				}
				// make a working copy of the snapshotWorkingCopy than preemption can temporarily modify
				snapshotWorkingCopy, err := cqCache.Snapshot(ctx)
				if err != nil {
					t.Fatalf("unexpected error while building snapshot: %v", err)
				}
				wlInfo := workload.NewInfo(log, tc.incoming)
				wlInfo.ClusterQueue = tc.targetCQ
				targets := preemptor.GetTargets(ctx, *wlInfo, tc.assignment, snapshotWorkingCopy)
				preempted, failed, err := preemptor.IssuePreemptions(ctx, cqCache, wlInfo, targets, snapshotWorkingCopy.ClusterQueue(wlInfo.ClusterQueue))
				if err != nil {
					t.Fatalf("Failed doing preemption")
				}
				if preempted != tc.wantPreempted {
					t.Errorf("Reported %d preemptions, want %d", preempted, tc.wantPreempted)
				}
				if failed != 0 {
					t.Errorf("Reported %d failed preemptions, want 0", failed)
				}

				workloads := &kueue.WorkloadList{}
				err = cl.List(ctx, workloads)
				if err != nil {
					t.Fatalf("Failed to List workloads: %v", err)
				}

				defaultCmpOpts := cmp.Options{
					cmpopts.EquateEmpty(),
					cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
					cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
				}
				if diff := cmp.Diff(tc.wantWorkloads, workloads.Items, defaultCmpOpts); diff != "" {
					t.Errorf("Unexpected workloads (-want/+got)\n%s", diff)
				}

				if diff := cmp.Diff(beforeSnapshot, snapshotWorkingCopy, snapCmpOpts); diff != "" {
					t.Errorf("Snapshot was modified (-initial,+end):\n%s", diff)
				}
			})
		}
	}
}
