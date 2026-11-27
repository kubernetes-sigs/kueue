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

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

// TestScheduleForPartialPreemption checks a scheduling cycle in which the
// workload "in", at the head of ClusterQueue "a", needs preemption: what the
// cycle does to the victims, and to the workload.
//
// The ClusterQueues "a", "b" and "c" share the cohort "cohort". Every Pod of a
// victim requests one CPU.
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
func TestScheduleForPartialPreemption(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	// cohortPreemption lets a ClusterQueue preempt workloads of a lower priority
	// of its own, and reclaim from its cohort.
	cohortPreemption := kueue.ClusterQueuePreemption{
		WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
		ReclaimWithinCohort: kueue.PreemptionPolicyAny,
	}

	cases := map[string]scheduleTestCase{
		"a victim shrinks only as far as the workload needs": {
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("a").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "6").Obj()).
					Preemption(cohortPreemption).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("v", "default").
					Queue("a").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 5).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(1).
						Obj()).
					SimpleReserveQuota("a", "default", now).
					Obj(),
				*utiltestingapi.MakeWorkload("in", "default").
					UID("wl-in").
					Queue("a").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("in", "default").
					UID("wl-in").
					Queue("a").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 3 more needed. Pending the preemption of 1 workload(s)",
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
							corev1.ResourceCPU: resource.MustParse("4"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("v", "default").
					Queue("a").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 5).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(1).
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("a").
						PodSets(utiltestingapi.MakePodSetAssignment("main").
							Assignment(corev1.ResourceCPU, "default", "5").
							Count(5).
							PartialPreemptionRetainedCount(2).
							Obj()).
						Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPartiallyPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.InClusterQueueReason,
						Message:            "Requested scale-down of PodSet \"main\" to 2 to accommodate workload default/in",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/v": *utiltestingapi.MakeAdmission("a").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "default", "5").
						Count(5).
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"a": {"default/in"},
			},
		},
		"gate off: the victim is evicted": {
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: false},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("a").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "6").Obj()).
					Preemption(cohortPreemption).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("v", "default").
					Queue("a").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 5).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(1).
						Obj()).
					SimpleReserveQuota("a", "default", now).
					Obj(),
				*utiltestingapi.MakeWorkload("in", "default").
					UID("wl-in").
					Queue("a").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("in", "default").
					UID("wl-in").
					Queue("a").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 3 more needed. Pending the preemption of 1 workload(s)",
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
							corev1.ResourceCPU: resource.MustParse("4"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("v", "default").
					Queue("a").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 5).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(1).
						Obj()).
					SimpleReserveQuota("a", "default", now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: UNKNOWN) due to prioritization in the ClusterQueue; preemptor path: /cohort/a; preemptee path: /cohort/a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.InClusterQueueReason,
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: UNKNOWN) due to prioritization in the ClusterQueue; preemptor path: /cohort/a; preemptee path: /cohort/a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: kueue.WorkloadEvictedByPreemption, Count: 1}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/v": *utiltestingapi.MakeAdmission("a").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "default", "5").
						Count(5).
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"a": {"default/in"},
			},
		},
		// ClusterQueue b uses all 6 CPUs of the cohort. The workload makes a borrow
		// 2, and b gives them only as long as it borrows more than a.
		"fair sharing: a victim of another ClusterQueue shrinks only as far as the workload needs": {
			featureGates:      map[featuregate.Feature]bool{features.PartialPreemption: true},
			enableFairSharing: true,
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("a").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "2").Obj()).
					Preemption(cohortPreemption).
					Obj(),
				*utiltestingapi.MakeClusterQueue("b").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "2").Obj()).
					Preemption(cohortPreemption).
					Obj(),
				*utiltestingapi.MakeClusterQueue("c").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "2").Obj()).
					Preemption(cohortPreemption).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("v", "default").
					Queue("b").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 6).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(1).
						Obj()).
					SimpleReserveQuota("b", "default", now).
					Obj(),
				*utiltestingapi.MakeWorkload("in", "default").
					UID("wl-in").
					Queue("a").
					Priority(10).
					Request(corev1.ResourceCPU, "4").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("in", "default").
					UID("wl-in").
					Queue("a").
					Priority(10).
					Request(corev1.ResourceCPU, "4").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 4 more needed. Pending the preemption of 1 workload(s)",
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
							corev1.ResourceCPU: resource.MustParse("4"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("v", "default").
					Queue("b").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 6).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(1).
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("b").
						PodSets(utiltestingapi.MakePodSetAssignment("main").
							Assignment(corev1.ResourceCPU, "default", "6").
							Count(6).
							PartialPreemptionRetainedCount(2).
							Obj()).
						Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPartiallyPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.InCohortFairSharingReason,
						Message:            "Requested scale-down of PodSet \"main\" to 2 to accommodate workload default/in",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/v": *utiltestingapi.MakeAdmission("b").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "default", "6").
						Count(6).
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"a": {"default/in"},
			},
		},
		// ClusterQueue b uses the 4 CPUs of a, and the workload needs them back.
		// The shrink of p to 3 is not enough, and p is the first to be evicted.
		"fair sharing, LessThanOrEqualToFinalShare: a victim that is already shrinking is evicted when its shrink is not enough": {
			featureGates:          map[featuregate.Feature]bool{features.PartialPreemption: true},
			enableFairSharing:     true,
			fairSharingStrategies: []config.PreemptionStrategy{config.LessThanOrEqualToFinalShare},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("a").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "4").Obj()).
					Preemption(cohortPreemption).
					Obj(),
				*utiltestingapi.MakeClusterQueue("b").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "4").Obj()).
					Preemption(cohortPreemption).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("p", "default").
					Queue("b").
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
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPartiallyPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.InCohortFairSharingReason,
						Message:            "Requested scale-down of PodSet \"main\" to 3 to accommodate workload default/in",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("fixed", "default").
					Queue("b").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("main", 4).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(4).
						Obj()).
					SimpleReserveQuota("b", "default", now).
					Obj(),
				*utiltestingapi.MakeWorkload("in", "default").
					UID("wl-in").
					Queue("a").
					Priority(10).
					Request(corev1.ResourceCPU, "4").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("fixed", "default").
					Queue("b").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("main", 4).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(4).
						Obj()).
					SimpleReserveQuota("b", "default", now).
					Obj(),
				*utiltestingapi.MakeWorkload("in", "default").
					UID("wl-in").
					Queue("a").
					Priority(10).
					Request(corev1.ResourceCPU, "4").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 4 more needed. Pending the preemption of 1 workload(s)",
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
							corev1.ResourceCPU: resource.MustParse("4"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("p", "default").
					Queue("b").
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
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPartiallyPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.InCohortFairSharingReason,
						Message:            "Requested scale-down of PodSet \"main\" to 3 to accommodate workload default/in",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: UNKNOWN) due to reclamation within the cohort; preemptor path: /cohort/a; preemptee path: /cohort/b",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.InCohortReclamationReason,
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: UNKNOWN) due to reclamation within the cohort; preemptor path: /cohort/a; preemptee path: /cohort/b",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: kueue.WorkloadEvictedByPreemption, Count: 1}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/p": *utiltestingapi.MakeAdmission("b").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "default", "4").
						Count(4).
						PartialPreemptionRetainedCount(3).
						Obj()).
					Obj(),
				"default/fixed": *utiltestingapi.MakeAdmission("b").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "default", "4").
						Count(4).
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"a": {"default/in"},
			},
		},
		// ClusterQueue b uses all 10 CPUs of the cohort, and the workload needs 6.
		// With p and q at 2 Pods each, b borrows 2. Undoing the shrink of p makes b
		// borrow 4, as much as a borrows, so no strategy allows that shrink, and
		// the cycle asks nobody to shrink.
		"fair sharing: a victim is not asked for a shrink that is unfair": {
			featureGates:      map[featuregate.Feature]bool{features.PartialPreemption: true},
			enableFairSharing: true,
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("a").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "2").Obj()).
					Preemption(cohortPreemption).
					Obj(),
				*utiltestingapi.MakeClusterQueue("b").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "2").Obj()).
					Preemption(cohortPreemption).
					Obj(),
				*utiltestingapi.MakeClusterQueue("c").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "6").Obj()).
					Preemption(cohortPreemption).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("p", "default").
					Queue("b").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 4).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(2).
						Obj()).
					SimpleReserveQuota("b", "default", now).
					Obj(),
				*utiltestingapi.MakeWorkload("q", "default").
					Queue("b").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("main", 6).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(0).
						Obj()).
					SimpleReserveQuota("b", "default", now).
					Obj(),
				*utiltestingapi.MakeWorkload("in", "default").
					UID("wl-in").
					Queue("a").
					Priority(10).
					Request(corev1.ResourceCPU, "6").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("in", "default").
					UID("wl-in").
					Queue("a").
					Priority(10).
					Request(corev1.ResourceCPU, "6").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 6 more needed",
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
							corev1.ResourceCPU: resource.MustParse("6"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("p", "default").
					Queue("b").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 4).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(2).
						Obj()).
					SimpleReserveQuota("b", "default", now).
					Obj(),
				*utiltestingapi.MakeWorkload("q", "default").
					Queue("b").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("main", 6).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(0).
						Obj()).
					SimpleReserveQuota("b", "default", now).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/p": *utiltestingapi.MakeAdmission("b").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "default", "4").
						Count(4).
						Obj()).
					Obj(),
				"default/q": *utiltestingapi.MakeAdmission("b").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "default", "6").
						Count(6).
						Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"a": {"default/in"},
			},
		},
		"fair sharing: two victims of one ClusterQueue shrink, and neither is evicted": {
			// The workload needs 2 of the 3 CPUs that b borrows. p and q can release
			// one Pod each, so the cycle asks both to shrink and evicts neither.
			// Afterwards a and b borrow 1 CPU each.
			//
			//   a [1]  .          <- in: 2 CPUs
			//   b [3]  PPp QQq
			//   c [2]  ..
			featureGates:      map[featuregate.Feature]bool{features.PartialPreemption: true},
			enableFairSharing: true,
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("a").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj()).
					Preemption(cohortPreemption).
					Obj(),
				*utiltestingapi.MakeClusterQueue("b").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "3").Obj()).
					Preemption(cohortPreemption).
					Obj(),
				*utiltestingapi.MakeClusterQueue("c").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "2").Obj()).
					Preemption(cohortPreemption).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("p", "default").
					Queue("b").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 3).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(2).
						Obj()).
					SimpleReserveQuota("b", "default", now).
					Obj(),
				*utiltestingapi.MakeWorkload("q", "default").
					Queue("b").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(1).
					PodSets(*utiltestingapi.MakePodSet("main", 3).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(2).
						Obj()).
					SimpleReserveQuota("b", "default", now).
					Obj(),
				*utiltestingapi.MakeWorkload("in", "default").
					UID("wl-in").
					Queue("a").
					Priority(10).
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("in", "default").
					UID("wl-in").
					Queue("a").
					Priority(10).
					Request(corev1.ResourceCPU, "2").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 2 more needed. Pending the preemption of 2 workload(s)",
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
							corev1.ResourceCPU: resource.MustParse("2"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("p", "default").
					Queue("b").
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
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPartiallyPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.InCohortFairSharingReason,
						Message:            "Requested scale-down of PodSet \"main\" to 2 to accommodate workload default/in",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("q", "default").
					Queue("b").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(1).
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
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPartiallyPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.InCohortFairSharingReason,
						Message:            "Requested scale-down of PodSet \"main\" to 2 to accommodate workload default/in",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/p": *utiltestingapi.MakeAdmission("b").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "default", "3").
						Count(3).
						Obj()).
					Obj(),
				"default/q": *utiltestingapi.MakeAdmission("b").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "default", "3").
						Count(3).
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"a": {"default/in"},
			},
		},
		"fair sharing: LessThanOrEqualToFinalShare shrinks a victim whose whole removal would be unfair": {
			// Removing v whole would drop b to 0, below a's 1/6. Shrinking v to 3 leaves
			// b at 1/6, matching a's share under LessThanOrEqualToFinalShare alone.
			//
			//   a [2]  ..          <- in: 3 CPUs
			//   b [2]  VVVvvv
			//   c [2]  ..
			featureGates:          map[featuregate.Feature]bool{features.PartialPreemption: true},
			enableFairSharing:     true,
			fairSharingStrategies: []config.PreemptionStrategy{config.LessThanOrEqualToFinalShare},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("a").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "2").Obj()).
					Preemption(cohortPreemption).
					Obj(),
				*utiltestingapi.MakeClusterQueue("b").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "2").Obj()).
					Preemption(cohortPreemption).
					Obj(),
				*utiltestingapi.MakeClusterQueue("c").
					Cohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "2").Obj()).
					Preemption(cohortPreemption).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("v", "default").
					Queue("b").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 6).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(3).
						Obj()).
					SimpleReserveQuota("b", "default", now).
					Obj(),
				*utiltestingapi.MakeWorkload("in", "default").
					UID("wl-in").
					Queue("a").
					Priority(10).
					Request(corev1.ResourceCPU, "3").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("in", "default").
					UID("wl-in").
					Queue("a").
					Priority(10).
					Request(corev1.ResourceCPU, "3").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 3 more needed. Pending the preemption of 1 workload(s)",
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
				*utiltestingapi.MakeWorkload("v", "default").
					Queue("b").
					Annotation(constants.PartialPreemptionAnnotation, "true").
					Priority(0).
					PodSets(*utiltestingapi.MakePodSet("main", 6).
						Request(corev1.ResourceCPU, "1").
						PartialPreemptionMinRetainedCount(3).
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("b").
						PodSets(utiltestingapi.MakePodSetAssignment("main").
							Assignment(corev1.ResourceCPU, "default", "6").
							Count(6).
							PartialPreemptionRetainedCount(3).
							Obj()).
						Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPartiallyPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.InCohortFairSharingReason,
						Message:            "Requested scale-down of PodSet \"main\" to 3 to accommodate workload default/in",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/v": *utiltestingapi.MakeAdmission("b").
					PodSets(utiltestingapi.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "default", "6").
						Count(6).
						Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"a": {"default/in"},
			},
		},
	}

	runScheduleTestCases(t, scheduleTestConfig{
		queues: []kueue.LocalQueue{
			*utiltestingapi.MakeLocalQueue("a", "default").ClusterQueue("a").Obj(),
			*utiltestingapi.MakeLocalQueue("b", "default").ClusterQueue("b").Obj(),
			*utiltestingapi.MakeLocalQueue("c", "default").ClusterQueue("c").Obj(),
		},
		resourceFlavors: []*kueue.ResourceFlavor{utiltestingapi.MakeResourceFlavor("default").Obj()},
		fakeClock:       testingclock.NewFakeClock(now),
	}, cases)
}
