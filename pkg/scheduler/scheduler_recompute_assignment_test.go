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
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestScheduleRecomputeAssignmentUponQuotaExhaustion(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(now)

	resourceFlavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("default").Obj(),
	}

	clusterQueues := []kueue.ClusterQueue{
		*utiltestingapi.MakeClusterQueue("cq-1").
			Cohort("cohort-1").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "100").Obj(),
			).Obj(),
		*utiltestingapi.MakeClusterQueue("cq-2").
			Cohort("cohort-1").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "100").Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).Obj(),
	}

	queues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("lq-1", "eng-alpha").ClusterQueue("cq-1").Obj(),
		*utiltestingapi.MakeLocalQueue("lq-2", "eng-alpha").ClusterQueue("cq-2").Obj(),
	}

	cases := map[string]scheduleTestCase{
		"feature gate enabled; quota stolen by preceding workload forces fallback to preemption": {
			featureGates: map[featuregate.Feature]bool{
				features.RecomputeAssignmentUponQuotaExhaustion: true,
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-1", "eng-alpha").
					UID("wl-1").
					JobUID("job-1-uid").
					Queue("lq-1").
					Priority(10).
					Request(corev1.ResourceCPU, "100").
					Creation(now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-2", "eng-alpha").
					UID("wl-2").
					JobUID("job-2-uid").
					Queue("lq-2").
					Priority(5).
					Request(corev1.ResourceCPU, "100").
					Creation(now.Add(time.Second)).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-admitted", "eng-alpha").
					UID("wl-admitted-uid").
					JobUID("wl-admitted-job-uid").
					Queue("lq-2").
					Priority(1).
					Request(corev1.ResourceCPU, "100").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-2").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "100").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-1", "eng-alpha").
					UID("wl-1").
					JobUID("job-1-uid").
					Queue("lq-1").
					Priority(10).
					Request(corev1.ResourceCPU, "100").
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq-1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "100").Obj()).
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-2", "eng-alpha").
					UID("wl-2").
					JobUID("job-2-uid").
					Queue("lq-2").
					Priority(5).
					Request(corev1.ResourceCPU, "100").
					Creation(now.Add(time.Second)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 100 more needed. Pending the preemption of 1 workload(s)",
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
							corev1.ResourceCPU: resource.MustParse("100"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-admitted", "eng-alpha").
					UID("wl-admitted-uid").
					JobUID("wl-admitted-job-uid").
					Queue("lq-2").
					Priority(1).
					Request(corev1.ResourceCPU, "100").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-2").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "100").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-2, JobUID: job-2-uid) due to prioritization in the ClusterQueue; preemptor path: /cohort-1/cq-2; preemptee path: /cohort-1/cq-2",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-2, JobUID: job-2-uid) due to prioritization in the ClusterQueue; preemptor path: /cohort-1/cq-2; preemptee path: /cohort-1/cq-2",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/wl-1":        *utiltestingapi.MakeAdmission("cq-1").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "100").Obj()).Obj(),
				"eng-alpha/wl-admitted": *utiltestingapi.MakeAdmission("cq-2").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "100").Obj()).Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-2": {"eng-alpha/wl-2"},
			},
		},
		"feature gate disabled; quota stolen by preceding workload causes workload to be skipped": {
			featureGates: map[featuregate.Feature]bool{
				features.RecomputeAssignmentUponQuotaExhaustion: false,
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-1", "eng-alpha").
					UID("wl-1").
					JobUID("job-1-uid").
					Queue("lq-1").
					Priority(10).
					Request(corev1.ResourceCPU, "100").
					Creation(now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-2", "eng-alpha").
					UID("wl-2").
					JobUID("job-2-uid").
					Queue("lq-2").
					Priority(5).
					Request(corev1.ResourceCPU, "100").
					Creation(now.Add(time.Second)).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-admitted", "eng-alpha").
					UID("wl-admitted-uid").
					JobUID("wl-admitted-job-uid").
					Queue("lq-2").
					Priority(1).
					Request(corev1.ResourceCPU, "100").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-2").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "100").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-1", "eng-alpha").
					UID("wl-1").
					JobUID("job-1-uid").
					Queue("lq-1").
					Priority(10).
					Request(corev1.ResourceCPU, "100").
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq-1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(utiltestingapi.MakeAdmission("cq-1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "100").Obj()).
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-2", "eng-alpha").
					UID("wl-2").
					JobUID("job-2-uid").
					Queue("lq-2").
					Priority(5).
					Request(corev1.ResourceCPU, "100").
					Creation(now.Add(time.Second)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            "Workload no longer fits after processing another workload",
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
							corev1.ResourceCPU: resource.MustParse("100"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-admitted", "eng-alpha").
					UID("wl-admitted-uid").
					JobUID("wl-admitted-job-uid").
					Queue("lq-2").
					Priority(1).
					Request(corev1.ResourceCPU, "100").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq-2").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "100").Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/wl-1":        *utiltestingapi.MakeAdmission("cq-1").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "100").Obj()).Obj(),
				"eng-alpha/wl-admitted": *utiltestingapi.MakeAdmission("cq-2").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "100").Obj()).Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-2": {"eng-alpha/wl-2"},
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
