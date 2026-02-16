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
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	preemptioncommon "sigs.k8s.io/kueue/pkg/scheduler/preemption/common"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

var snapCmpOpts = cmp.Options{
	// ignore zero values during comparison, as we consider
	// zero FlavorResource usage to be same as no map entry.
	cmpopts.IgnoreMapEntries(func(_ resources.FlavorResource, v int64) bool { return v == 0 }),
	cmp.AllowUnexported(hierarchy.Manager[*schdcache.ClusterQueueSnapshot, *schdcache.CohortSnapshot]{}),
	cmpopts.IgnoreFields(hierarchy.Manager[*schdcache.ClusterQueueSnapshot, *schdcache.CohortSnapshot]{}, "cohortFactory"),
	cmpopts.IgnoreFields(schdcache.CohortSnapshot{}, "Cohort"),
	cmp.AllowUnexported(schdcache.ClusterQueueSnapshot{}),
	cmpopts.IgnoreFields(schdcache.ClusterQueueSnapshot{}, "ClusterQueue"),
}

func TestPreemption(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	flavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("default").Obj(),
		utiltestingapi.MakeResourceFlavor("alpha").Obj(),
		utiltestingapi.MakeResourceFlavor("beta").Obj(),
	}
	defaultClusterQueues := []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("standalone").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "6").
					Obj(),
			).ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("alpha").
				Resource(corev1.ResourceMemory, "3Gi").
				Obj(),
			*utiltestingapi.MakeFlavorQuotas("beta").
				Resource(corev1.ResourceMemory, "3Gi").
				Obj(),
		).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj(),
		utiltestingapi.MakeClusterQueue("c1").
			Cohort("cohort").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "6", "6").
				Resource(corev1.ResourceMemory, "3Gi", "3Gi").
				Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj(),
		utiltestingapi.MakeClusterQueue("c2").
			Cohort("cohort").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "6", "6").
				Resource(corev1.ResourceMemory, "3Gi", "3Gi").
				Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyNever,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			Obj(),
		utiltestingapi.MakeClusterQueue("d1").
			Cohort("cohort-no-limits").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "6").
				Resource(corev1.ResourceMemory, "3Gi").
				Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj(),
		utiltestingapi.MakeClusterQueue("d2").
			Cohort("cohort-no-limits").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "6").
				Resource(corev1.ResourceMemory, "3Gi").
				Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyNever,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			Obj(),
		utiltestingapi.MakeClusterQueue("l1").
			Cohort("legion").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "6", "12").
				Resource(corev1.ResourceMemory, "3Gi", "6Gi").
				Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj(),
		utiltestingapi.MakeClusterQueue("preventStarvation").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "6").
				Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerOrNewerEqualPriority,
			}).
			Obj(),
		utiltestingapi.MakeClusterQueue("a_standard").
			Cohort("with_shared_cq").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "1", "12").
					Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyNever,
				ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
				BorrowWithinCohort: &kueue.BorrowWithinCohort{
					Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
					MaxPriorityThreshold: ptr.To[int32](0),
				},
			}).
			Obj(),
		utiltestingapi.MakeClusterQueue("b_standard").
			Cohort("with_shared_cq").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "1", "12").
					Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				BorrowWithinCohort: &kueue.BorrowWithinCohort{
					Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
					MaxPriorityThreshold: ptr.To[int32](0),
				},
			}).
			Obj(),
		utiltestingapi.MakeClusterQueue("a_best_effort").
			Cohort("with_shared_cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "1", "12").
				Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyNever,
				ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
				BorrowWithinCohort: &kueue.BorrowWithinCohort{
					Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
					MaxPriorityThreshold: ptr.To[int32](0),
				},
			}).
			Obj(),
		utiltestingapi.MakeClusterQueue("b_best_effort").
			Cohort("with_shared_cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "0", "13").
				Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyNever,
				ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
				BorrowWithinCohort: &kueue.BorrowWithinCohort{
					Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
					MaxPriorityThreshold: ptr.To[int32](0),
				},
			}).
			Obj(),
		utiltestingapi.MakeClusterQueue("shared").
			Cohort("with_shared_cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "10").
				Obj(),
			).
			Obj(),
		utiltestingapi.MakeClusterQueue("lend1").
			Cohort("cohort-lend").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "6", "", "4").
				Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj(),
		utiltestingapi.MakeClusterQueue("lend2").
			Cohort("cohort-lend").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "6", "", "2").
				Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj(),
		utiltestingapi.MakeClusterQueue("a").
			Cohort("cohort-three").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "2").
				Resource(corev1.ResourceMemory, "2").
				Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			Obj(),
		utiltestingapi.MakeClusterQueue("b").
			Cohort("cohort-three").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "2").
				Resource(corev1.ResourceMemory, "2").
				Obj(),
			).
			Obj(),
		utiltestingapi.MakeClusterQueue("c").
			Cohort("cohort-three").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "2").
				Resource(corev1.ResourceMemory, "2").
				Obj(),
			).
			Obj(),
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
		"preempt lowest priority": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(1).
				Request(corev1.ResourceCPU, "2").
				Obj(),
			targetCQ: "standalone",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /standalone; preemptee path: /standalone",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /standalone; preemptee path: /standalone",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
		"preempt multiple": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(1).
				Request(corev1.ResourceCPU, "3").
				Obj(),
			targetCQ: "standalone",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 2,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /standalone; preemptee path: /standalone",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /standalone; preemptee path: /standalone",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /standalone; preemptee path: /standalone",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /standalone; preemptee path: /standalone",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
		},
		"no preemption for low priority": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("in", "").
				Priority(-1).
				Request(corev1.ResourceCPU, "1").
				Obj(),
			targetCQ: "standalone",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
		"not enough low priority workloads": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("in", "").
				Request(corev1.ResourceCPU, "4").
				Obj(),
			targetCQ: "standalone",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
		"some free quota, preempt low priority": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(1).
				Request(corev1.ResourceCPU, "2").
				Obj(),
			targetCQ: "standalone",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1000m").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /standalone; preemptee path: /standalone",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /standalone; preemptee path: /standalone",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
		"minimal set excludes low priority": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(1).
				Request(corev1.ResourceCPU, "2").
				Obj(),
			targetCQ: "standalone",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /standalone; preemptee path: /standalone",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /standalone; preemptee path: /standalone",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
		},
		"only preempt workloads using the chosen flavor": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceMemory, "2Gi").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceMemory, "alpha", "2Gi").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("mid", "").
					Request(corev1.ResourceMemory, "1Gi").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceMemory, "beta", "1Gi").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("high", "").
					Priority(1).
					Request(corev1.ResourceMemory, "1Gi").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceMemory, "beta", "1Gi").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(1).
				Request(corev1.ResourceCPU, "1").
				Request(corev1.ResourceMemory, "2Gi").
				Obj(),
			targetCQ: "standalone",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Fit,
				},
				corev1.ResourceMemory: &flavorassigner.FlavorAssignment{
					Name: "beta",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("high", "").
					Priority(1).
					Request(corev1.ResourceMemory, "1Gi").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceMemory, "beta", "1Gi").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceMemory, "2Gi").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceMemory, "alpha", "2Gi").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("mid", "").
					Request(corev1.ResourceMemory, "1Gi").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceMemory, "beta", "1Gi").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /standalone; preemptee path: /standalone",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /standalone; preemptee path: /standalone",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
		},
		"reclaim quota from borrower": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("c1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-mid", "").
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "6").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "6000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(1).
				Request(corev1.ResourceCPU, "3").
				Obj(),
			targetCQ: "c1",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("c1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "6").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "6000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-mid", "").
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /cohort/c1; preemptee path: /cohort/c2",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /cohort/c1; preemptee path: /cohort/c2",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
		},
		"reclaim quota if workload requests 0 resources for a resource at nominal quota": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("c1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					Request(corev1.ResourceMemory, "3Gi").
					SimpleReserveQuota("c1", "default", now).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-mid", "").
					Request(corev1.ResourceCPU, "3").
					SimpleReserveQuota("c2", "default", now).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "6").
					SimpleReserveQuota("c2", "default", now).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(1).
				Request(corev1.ResourceCPU, "3").
				Request(corev1.ResourceMemory, "0").
				Obj(),
			targetCQ: "c1",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
				corev1.ResourceMemory: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Fit,
				},
			}),
			wantPreempted: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("c1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					Request(corev1.ResourceMemory, "3Gi").
					SimpleReserveQuota("c1", "default", now).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "6").
					SimpleReserveQuota("c2", "default", now).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-mid", "").
					Request(corev1.ResourceCPU, "3").
					SimpleReserveQuota("c2", "default", now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /cohort/c1; preemptee path: /cohort/c2",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /cohort/c1; preemptee path: /cohort/c2",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
		},
		"no workloads borrowing": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("c1-high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(1).
				Request(corev1.ResourceCPU, "4").
				Obj(),
			targetCQ: "c1",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("c1-high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
		"not enough workloads borrowing": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("c1-high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-low-2", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(1).
				Request(corev1.ResourceCPU, "4").
				Obj(),
			targetCQ: "c1",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("c1-high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-low-2", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
		"preempting locally and borrowing other resources in cohort, without cohort candidates": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("c1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-high-2", "").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(1).
				Request(corev1.ResourceCPU, "4").
				Request(corev1.ResourceMemory, "5Gi").
				Obj(),
			targetCQ: "c1",
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
				*utiltestingapi.MakeWorkload("c1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /cohort/c1; preemptee path: /cohort/c1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /cohort/c1; preemptee path: /cohort/c1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-high-2", "").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
		"preempting locally and borrowing same resource in cohort": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("c1-med", "").
					Priority(0).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(1).
				Request(corev1.ResourceCPU, "4").
				Obj(),
			targetCQ: "c1",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("c1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /cohort/c1; preemptee path: /cohort/c1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /cohort/c1; preemptee path: /cohort/c1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("c1-med", "").
					Priority(0).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
		"preempting locally and borrowing same resource in cohort; no borrowing limit in the cohort": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("d1-med", "").
					Priority(0).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("d1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("d1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("d1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("d2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("d2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(1).
				Request(corev1.ResourceCPU, "4").
				Obj(),
			targetCQ: "d1",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("d1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("d1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /cohort-no-limits/d1; preemptee path: /cohort-no-limits/d1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /cohort-no-limits/d1; preemptee path: /cohort-no-limits/d1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("d1-med", "").
					Priority(0).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("d1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("d2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("d2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
		"preempting locally and borrowing other resources in cohort, with cohort candidates": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("c1-med", "").
					Priority(0).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "5").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "5000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-low-2", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-low-3", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(1).
				Request(corev1.ResourceCPU, "2").
				Request(corev1.ResourceMemory, "5Gi").
				Obj(),
			targetCQ: "c1",
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
				*utiltestingapi.MakeWorkload("c1-med", "").
					Priority(0).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /cohort/c1; preemptee path: /cohort/c1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /cohort/c1; preemptee path: /cohort/c1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "5").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "5000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-low-2", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-low-3", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
		"preempting locally and not borrowing same resource in 1-queue cohort": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("l1-med", "").
					Priority(0).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("l1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("l1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("l1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(1).
				Request(corev1.ResourceCPU, "4").
				Obj(),
			targetCQ: "l1",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("l1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("l1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("l1-med", "").
					Priority(0).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("l1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /legion/l1; preemptee path: /legion/l1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /legion/l1; preemptee path: /legion/l1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
		},
		"do not reclaim borrowed quota from same priority for withinCohort=ReclaimFromLowerPriority": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("c1", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-1", "").
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-2", "").
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Request(corev1.ResourceCPU, "4").
				Obj(),
			targetCQ: "c1",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("c1", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-1", "").
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-2", "").
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
		"reclaim borrowed quota from same priority for withinCohort=ReclaimFromAny": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("c1-1", "").
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c1-2", "").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Request(corev1.ResourceCPU, "4").
				Obj(),
			targetCQ: "c2",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("c1-1", "").
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /cohort/c2; preemptee path: /cohort/c1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /cohort/c2; preemptee path: /cohort/c1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("c1-2", "").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
		"preempt from all ClusterQueues in cohort": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("c1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c1-mid", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-mid", "").
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Request(corev1.ResourceCPU, "4").
				Obj(),
			targetCQ: "c1",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 2,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("c1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /cohort/c1; preemptee path: /cohort/c1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /cohort/c1; preemptee path: /cohort/c1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("c1-mid", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /cohort/c1; preemptee path: /cohort/c2",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /cohort/c1; preemptee path: /cohort/c2",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("c2-mid", "").
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
		"can't preempt workloads in ClusterQueue for withinClusterQueue=Never": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("c2-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(1).
				Request(corev1.ResourceCPU, "4").
				Obj(),
			targetCQ: "c2",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("c2-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("c2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
		"each podset preempts a different flavor": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("low-alpha", "").
					Priority(-1).
					Request(corev1.ResourceMemory, "2Gi").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceMemory, "alpha", "2Gi").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("low-beta", "").
					Priority(-1).
					Request(corev1.ResourceMemory, "2Gi").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceMemory, "beta", "2Gi").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				PodSets(
					*utiltestingapi.MakePodSet("launcher", 1).
						Request(corev1.ResourceMemory, "2Gi").Obj(),
					*utiltestingapi.MakePodSet("workers", 2).
						Request(corev1.ResourceMemory, "1Gi").Obj(),
				).
				Obj(),
			targetCQ: "standalone",
			assignment: flavorassigner.Assignment{
				PodSets: []flavorassigner.PodSetAssignment{
					{
						Name: "launcher",
						Flavors: flavorassigner.ResourceAssignment{
							corev1.ResourceMemory: {
								Name: "alpha",
								Mode: flavorassigner.Preempt,
							},
						},
						Count: 1,
					},
					{
						Name: "workers",
						Flavors: flavorassigner.ResourceAssignment{
							corev1.ResourceMemory: {
								Name: "beta",
								Mode: flavorassigner.Preempt,
							},
						},
						Count: 2,
					},
				},
			},
			wantPreempted: 2,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("low-alpha", "").
					Priority(-1).
					Request(corev1.ResourceMemory, "2Gi").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceMemory, "alpha", "2Gi").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /standalone; preemptee path: /standalone",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /standalone; preemptee path: /standalone",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("low-beta", "").
					Priority(-1).
					Request(corev1.ResourceMemory, "2Gi").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceMemory, "beta", "2Gi").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /standalone; preemptee path: /standalone",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /standalone; preemptee path: /standalone",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
		},
		"preempt newer workloads with the same priority": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "").
					Priority(2).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("preventStarvation").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("wl2", "").
					Priority(1).
					Creation(now).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("preventStarvation").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2").
								Obj()).
							Obj(),
						now.Add(time.Second),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("wl3", "").
					Priority(1).
					Creation(now).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("preventStarvation").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(1).
				Creation(now.Add(-15 * time.Second)).
				PodSets(
					*utiltestingapi.MakePodSet("launcher", 1).
						Request(corev1.ResourceCPU, "2").Obj(),
				).
				Obj(),
			targetCQ: "preventStarvation",
			assignment: flavorassigner.Assignment{
				PodSets: []flavorassigner.PodSetAssignment{
					{
						Name: "launcher",
						Flavors: flavorassigner.ResourceAssignment{
							corev1.ResourceCPU: {
								Name: "default",
								Mode: flavorassigner.Preempt,
							},
						},
					},
				},
			},
			wantPreempted: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "").
					Priority(2).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("preventStarvation").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("wl2", "").
					Priority(1).
					Creation(now).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("preventStarvation").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2").
								Obj()).
							Obj(),
						now.Add(time.Second),
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /preventStarvation; preemptee path: /preventStarvation",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /preventStarvation; preemptee path: /preventStarvation",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl3", "").
					Priority(1).
					Creation(now).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("preventStarvation").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
		"use BorrowWithinCohort; allow preempting a lower-priority workload from another ClusterQueue while borrowing": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a_best_effort_low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a_best_effort").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "10").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b_best_effort_low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b_best_effort").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Request(corev1.ResourceCPU, "10").
				Obj(),
			targetCQ: "a_standard",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a_best_effort_low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a_best_effort").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "10").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort while borrowing; preemptor path: /with_shared_cq/a_standard; preemptee path: /with_shared_cq/a_best_effort",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclaimWhileBorrowing",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort while borrowing; preemptor path: /with_shared_cq/a_standard; preemptee path: /with_shared_cq/a_best_effort",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("b_best_effort_low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b_best_effort").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
		"use BorrowWithinCohort; don't allow preempting a lower-priority workload with priority above MaxPriorityThreshold, if borrowing is required even after the preemption": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("b_standard", "").
					Priority(1).
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b_standard").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "10000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(2).
				Request(corev1.ResourceCPU, "10").
				Obj(),
			targetCQ: "a_standard",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("b_standard", "").
					Priority(1).
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b_standard").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "10000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
		"use BorrowWithinCohort; allow preempting a lower-priority workload with priority above MaxPriorityThreshold, if borrowing is not required after the preemption": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				// this admitted workload consumes all resources so it needs to be preempted to run a new workload
				*utiltestingapi.MakeWorkload("b_standard", "").
					Priority(1).
					Request(corev1.ResourceCPU, "13").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b_standard").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "13000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				// this is a small workload which can be admitted without borrowing, if the b_standard workload is preempted
				Priority(2).
				Request(corev1.ResourceCPU, "1").
				Obj(),
			targetCQ: "a_standard",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 1,
			wantWorkloads: []kueue.Workload{
				// this admitted workload consumes all resources so it needs to be preempted to run a new workload
				*utiltestingapi.MakeWorkload("b_standard", "").
					Priority(1).
					Request(corev1.ResourceCPU, "13").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b_standard").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "13000m").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /with_shared_cq/a_standard; preemptee path: /with_shared_cq/b_standard",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /with_shared_cq/a_standard; preemptee path: /with_shared_cq/b_standard",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
		},
		"use BorrowWithinCohort; don't allow for preemption of lower-priority workload from the same ClusterQueue": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a_standard", "").
					Priority(1).
					Request(corev1.ResourceCPU, "13").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a_standard").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "13000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(2).
				Request(corev1.ResourceCPU, "1").
				Obj(),
			targetCQ: "a_standard",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a_standard", "").
					Priority(1).
					Request(corev1.ResourceCPU, "13").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a_standard").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "13000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
		"use BorrowWithinCohort; only preempt from CQ if no workloads below threshold and already above nominal": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a_standard_1", "").
					Priority(1).
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a_standard").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "10").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("a_standard_2", "").
					Priority(1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a_standard").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b_standard_1", "").
					Priority(1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b_standard").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b_standard_2", "").
					Priority(2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b_standard").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(3).
				Request(corev1.ResourceCPU, "1").
				Obj(),
			targetCQ: "b_standard",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a_standard_1", "").
					Priority(1).
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a_standard").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "10").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("a_standard_2", "").
					Priority(1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a_standard").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b_standard_1", "").
					Priority(1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b_standard").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /with_shared_cq/b_standard; preemptee path: /with_shared_cq/b_standard",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /with_shared_cq/b_standard; preemptee path: /with_shared_cq/b_standard",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("b_standard_2", "").
					Priority(2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b_standard").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
		"use BorrowWithinCohort; preempt from CQ and from other CQs with workloads below threshold": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("b_standard_high", "").
					Priority(2).
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b_standard").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "10").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b_standard_mid", "").
					Priority(1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b_standard").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("a_best_effort_low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a_best_effort").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("a_best_effort_lower", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a_best_effort").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(2).
				Request(corev1.ResourceCPU, "2").
				Obj(),
			targetCQ: "b_standard",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 2,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a_best_effort_low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a_best_effort").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("a_best_effort_lower", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a_best_effort").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort while borrowing; preemptor path: /with_shared_cq/b_standard; preemptee path: /with_shared_cq/a_best_effort",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclaimWhileBorrowing",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort while borrowing; preemptor path: /with_shared_cq/b_standard; preemptee path: /with_shared_cq/a_best_effort",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("b_standard_high", "").
					Priority(2).
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b_standard").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "10").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b_standard_mid", "").
					Priority(1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b_standard").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /with_shared_cq/b_standard; preemptee path: /with_shared_cq/b_standard",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /with_shared_cq/b_standard; preemptee path: /with_shared_cq/b_standard",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
		},
		"reclaim quota from lender": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("lend1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("lend1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("lend2-mid", "").
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("lend2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("lend2-high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("lend2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Priority(1).
				Request(corev1.ResourceCPU, "3").
				Obj(),
			targetCQ: "lend1",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("lend1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("lend1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("lend2-high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("lend2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("lend2-mid", "").
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("lend2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /cohort-lend/lend1; preemptee path: /cohort-lend/lend2",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /cohort-lend/lend1; preemptee path: /cohort-lend/lend2",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
		},
		"preempt from all ClusterQueues in cohort-lend": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("lend1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("lend1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("lend1-mid", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("lend1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("lend2-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("lend2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("lend2-mid", "").
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("lend2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Request(corev1.ResourceCPU, "4").
				Obj(),
			targetCQ: "lend1",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 2,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("lend1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("lend1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /cohort-lend/lend1; preemptee path: /cohort-lend/lend1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /cohort-lend/lend1; preemptee path: /cohort-lend/lend1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("lend1-mid", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("lend1").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("lend2-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("lend2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /cohort-lend/lend1; preemptee path: /cohort-lend/lend2",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /cohort-lend/lend1; preemptee path: /cohort-lend/lend2",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("lend2-mid", "").
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("lend2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
		"cannot preempt from other ClusterQueues if exceeds requestable quota including lending limit": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("lend2-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("lend2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "10").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Request(corev1.ResourceCPU, "9").
				Obj(),
			targetCQ: "lend1",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("lend2-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("lend2").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "10").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
		"preemptions from cq when target queue is exhausted for the single requested resource": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("a2", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("a3", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b3", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Request(corev1.ResourceCPU, "2").
				Priority(0).
				Obj(),
			targetCQ: "a",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 2,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /cohort-three/a; preemptee path: /cohort-three/a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /cohort-three/a; preemptee path: /cohort-three/a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("a2", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /cohort-three/a; preemptee path: /cohort-three/a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /cohort-three/a; preemptee path: /cohort-three/a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("a3", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b3", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
		"preemptions from cq when target queue is exhausted for two requested resources": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Assignment(corev1.ResourceMemory, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("a2", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Assignment(corev1.ResourceMemory, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("a3", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Assignment(corev1.ResourceMemory, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Assignment(corev1.ResourceMemory, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Assignment(corev1.ResourceMemory, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b3", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Assignment(corev1.ResourceMemory, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Request(corev1.ResourceCPU, "2").
				Request(corev1.ResourceMemory, "2").
				Priority(0).
				Obj(),
			targetCQ: "a",
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
				*utiltestingapi.MakeWorkload("a1", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Assignment(corev1.ResourceMemory, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /cohort-three/a; preemptee path: /cohort-three/a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /cohort-three/a; preemptee path: /cohort-three/a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("a2", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Assignment(corev1.ResourceMemory, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /cohort-three/a; preemptee path: /cohort-three/a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /cohort-three/a; preemptee path: /cohort-three/a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("a3", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Assignment(corev1.ResourceMemory, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Assignment(corev1.ResourceMemory, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Assignment(corev1.ResourceMemory, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b3", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Assignment(corev1.ResourceMemory, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
		"preemptions from cq when target queue is exhausted for one requested resource, but not the other": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("a2", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("a3", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b3", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Request(corev1.ResourceCPU, "2").
				Request(corev1.ResourceMemory, "2").
				Priority(0).
				Obj(),
			targetCQ: "a",
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
				*utiltestingapi.MakeWorkload("a1", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /cohort-three/a; preemptee path: /cohort-three/a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /cohort-three/a; preemptee path: /cohort-three/a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("a2", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /cohort-three/a; preemptee path: /cohort-three/a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /cohort-three/a; preemptee path: /cohort-three/a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("a3", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b3", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
		"allow preemption from other cluster queues if target cq is not exhausted for the requested resource": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b3", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b4", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b5", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Request(corev1.ResourceCPU, "2").
				Obj(),
			targetCQ: "a",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 2,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("a").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /cohort-three/a; preemptee path: /cohort-three/a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /cohort-three/a; preemptee path: /cohort-three/a",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b3", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b4", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("b5", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("b").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /cohort-three/a; preemptee path: /cohort-three/b",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /cohort-three/a; preemptee path: /cohort-three/b",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
			},
		},
		"long range preemption": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-left").
					Cohort("cohort-left").
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "10").
						Obj(),
					).Obj(),
				utiltestingapi.MakeClusterQueue("cq-right").
					Cohort("cohort-right").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "0").
						Obj(),
					).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("cohort-left").Parent("root").Obj(),
				utiltestingapi.MakeCohort("cohort-right").Parent("root").Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("to-be-preempted", "").
					Request(corev1.ResourceCPU, "5").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq-right").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "5").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: baseIncomingWl.Clone().
				Request(corev1.ResourceCPU, "8").
				Obj(),
			targetCQ: "cq-left",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: 1,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("to-be-preempted", "").
					Request(corev1.ResourceCPU, "5").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq-right").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "5").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /root/cohort-left/cq-left; preemptee path: /root/cohort-right/cq-right",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InCohortReclamation",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to reclamation within the cohort; preemptor path: /root/cohort-left/cq-left; preemptee path: /root/cohort-right/cq-right",
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

				broadcaster := record.NewBroadcaster()
				scheme := runtime.NewScheme()
				if err := kueue.AddToScheme(scheme); err != nil {
					t.Fatalf("Failed adding kueue scheme: %v", err)
				}
				recorder := broadcaster.NewRecorder(scheme, corev1.EventSource{Component: constants.AdmissionName})
				preemptor := New(cl, workload.Ordering{}, recorder, nil, false, clocktesting.NewFakeClock(now), nil)

				beforeSnapshot, err := cqCache.Snapshot(ctx)
				if err != nil {
					t.Fatalf("unexpected error while building snapshot: %v", err)
				}
				// make a working copy of the snapshotWorkingCopy than preemption can temporarily modify
				snapshotWorkingCopy, err := cqCache.Snapshot(ctx)
				if err != nil {
					t.Fatalf("unexpected error while building snapshot: %v", err)
				}
				wlInfo := workload.NewInfo(tc.incoming)
				wlInfo.ClusterQueue = tc.targetCQ
				targets := preemptor.GetTargets(log, *wlInfo, tc.assignment, snapshotWorkingCopy)
				preempted, failed, err := preemptor.IssuePreemptions(ctx, wlInfo, targets, snapshotWorkingCopy.ClusterQueue(wlInfo.ClusterQueue))
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

func TestPreemptionWhenWorkloadModifiedConcurrently(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	rf := utiltestingapi.MakeResourceFlavor("default").Obj()
	cq := utiltestingapi.MakeClusterQueue("standalone").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "6").
				Obj(),
		).ResourceGroup().
		Preemption(kueue.ClusterQueuePreemption{
			WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
		}).
		Obj()

	cases := map[string]struct {
		workloads     []kueue.Workload
		incoming      *kueue.Workload
		assignment    flavorassigner.Assignment
		wantWorkloads []kueue.Workload
	}{
		"preempt lowest priority": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("low", "").
					ResourceVersion("1").
					Priority(-1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("mid", "").
					ResourceVersion("1").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("high", "").
					ResourceVersion("1").
					Priority(1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("in", "").
				UID("wl-in").
				ResourceVersion("1").
				Label(controllerconstants.JobUIDLabel, "job-in").Clone().
				Priority(1).
				Request(corev1.ResourceCPU, "2").
				Obj(),
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: kueue.ResourceFlavorReference(rf.Name),
					Mode: flavorassigner.Preempt,
				},
			}),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("high", "").
					ResourceVersion("2").
					Priority(1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("low", "").
					ResourceVersion("3").
					Priority(-1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /standalone; preemptee path: /standalone",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             "InClusterQueue",
						Message:            "Preempted to accommodate a workload (UID: wl-in, JobUID: job-in) due to prioritization in the ClusterQueue; preemptor path: /standalone; preemptee path: /standalone",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Preempted", Count: 1}).
					Obj(),
				*utiltestingapi.MakeWorkload("mid", "").
					ResourceVersion("2").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("standalone").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
		},
	}
	for name, tc := range cases {
		for _, useMergePatch := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s when the WorkloadRequestUseMergePatch feature is %t", name, useMergePatch), func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, useMergePatch)
				ctx, log := utiltesting.ContextWithLog(t)
				var patched bool
				cl := utiltesting.NewClientBuilder().
					WithLists(&kueue.WorkloadList{Items: tc.workloads}).
					WithStatusSubresource(&kueue.Workload{}).
					WithInterceptorFuncs(interceptor.Funcs{
						SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
							if _, ok := obj.(*kueue.Workload); ok && subResourceName == "status" && !patched {
								patched = true
								for _, wl := range tc.workloads {
									// Simulate concurrent modification by another controller
									wlCopy := wl.DeepCopy()
									if wlCopy.Labels == nil {
										wlCopy.Labels = make(map[string]string, 1)
									}
									wlCopy.Labels["test.kueue.x-k8s.io/timestamp"] = time.Now().String()
									if err := c.Update(ctx, wlCopy); err != nil {
										return err
									}
								}
							}
							return utiltesting.TreatSSAAsStrategicMerge(ctx, c, subResourceName, obj, patch, opts...)
						},
					}).
					Build()

				cqCache := schdcache.New(cl)
				cqCache.AddOrUpdateResourceFlavor(log, rf.DeepCopy())
				if err := cqCache.AddClusterQueue(ctx, cq.DeepCopy()); err != nil {
					t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
				}

				broadcaster := record.NewBroadcaster()
				scheme := runtime.NewScheme()
				if err := kueue.AddToScheme(scheme); err != nil {
					t.Fatalf("Failed adding kueue scheme: %v", err)
				}
				recorder := broadcaster.NewRecorder(scheme, corev1.EventSource{Component: constants.AdmissionName})
				preemptor := New(cl, workload.Ordering{}, recorder, nil, false, clocktesting.NewFakeClock(now), nil)

				beforeSnapshot, err := cqCache.Snapshot(ctx)
				if err != nil {
					t.Fatalf("unexpected error while building snapshot: %v", err)
				}
				// make a working copy of the snapshotWorkingCopy than preemption can temporarily modify
				snapshotWorkingCopy, err := cqCache.Snapshot(ctx)
				if err != nil {
					t.Fatalf("unexpected error while building snapshot: %v", err)
				}
				wlInfo := workload.NewInfo(tc.incoming)
				wlInfo.ClusterQueue = kueue.ClusterQueueReference(cq.Name)
				targets := preemptor.GetTargets(log, *wlInfo, tc.assignment, snapshotWorkingCopy)
				_, _, err = preemptor.IssuePreemptions(ctx, wlInfo, targets, snapshotWorkingCopy.ClusterQueue(wlInfo.ClusterQueue))
				if err != nil {
					t.Fatalf("Failed doing preemption")
				}

				workloads := &kueue.WorkloadList{}
				err = cl.List(ctx, workloads)
				if err != nil {
					t.Fatalf("Failed to List workloads: %v", err)
				}

				defaultCmpOpts := cmp.Options{
					cmpopts.EquateEmpty(),
					// cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
					cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Labels"),
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

func targetKeyReason(key workload.Reference, reason string) string {
	return fmt.Sprintf("%s:%s", key, reason)
}
func TestCandidatesOrdering(t *testing.T) {
	now := time.Now()

	preemptorCq := "preemptor"

	wlLowUsageLq := workload.NewInfo(utiltestingapi.MakeWorkload("low_lq_usage", "").
		Queue("low_usage_lq").
		ReserveQuotaAt(utiltestingapi.MakeAdmission(preemptorCq).Obj(), now).
		Priority(1).
		Obj())
	wlLowUsageLq.LocalQueueFSUsage = ptr.To(0.1)

	wlMidUsageLq := workload.NewInfo(utiltestingapi.MakeWorkload("mid_lq_usage", "").
		Queue("mid_usage_lq").
		ReserveQuotaAt(utiltestingapi.MakeAdmission(preemptorCq).Obj(), now).
		Priority(10).
		Obj())
	wlMidUsageLq.LocalQueueFSUsage = ptr.To(0.5)

	wlHighUsageLqDifCQ := workload.NewInfo(utiltestingapi.MakeWorkload("high_lq_usage_different_cq", "").
		Queue("high_usage_lq_different_cq").
		ReserveQuotaAt(utiltestingapi.MakeAdmission("different_cq").Obj(), now).
		Priority(1).
		Obj())
	wlHighUsageLqDifCQ.LocalQueueFSUsage = ptr.To(1.0)

	cases := map[string]struct {
		candidates                  []workload.Info
		wantCandidates              []workload.Reference
		admissionFairSharingEnabled bool
	}{
		"workloads sorted by priority": {
			candidates: []workload.Info{
				*workload.NewInfo(utiltestingapi.MakeWorkload("high", "").
					ReserveQuotaAt(utiltestingapi.MakeAdmission(preemptorCq).Obj(), now).
					Priority(10).
					Obj()),
				*workload.NewInfo(utiltestingapi.MakeWorkload("low", "").
					ReserveQuotaAt(utiltestingapi.MakeAdmission(preemptorCq).Obj(), now).
					Priority(-10).
					Obj()),
			},
			wantCandidates: []workload.Reference{"low", "high"},
		},
		"evicted workload first": {
			candidates: []workload.Info{
				*workload.NewInfo(utiltestingapi.MakeWorkload("other", "").
					ReserveQuotaAt(utiltestingapi.MakeAdmission(preemptorCq).Obj(), now).
					Priority(10).
					Obj()),
				*workload.NewInfo(utiltestingapi.MakeWorkload("evicted", "").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.NewTime(now),
					}).
					Obj()),
			},
			wantCandidates: []workload.Reference{"evicted", "other"},
		},
		"workload from different CQ first": {
			candidates: []workload.Info{
				*workload.NewInfo(utiltestingapi.MakeWorkload("preemptorCq", "").
					ReserveQuotaAt(utiltestingapi.MakeAdmission(preemptorCq).Obj(), now).
					Priority(10).
					Obj()),
				*workload.NewInfo(utiltestingapi.MakeWorkload("other", "").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("other").Obj(), now).
					Priority(10).
					Obj()),
			},
			wantCandidates: []workload.Reference{"other", "preemptorCq"},
		},
		"old workloads last": {
			candidates: []workload.Info{
				*workload.NewInfo(utiltestingapi.MakeWorkload("older", "").
					ReserveQuotaAt(utiltestingapi.MakeAdmission(preemptorCq).Obj(), now.Add(-time.Second)).
					Obj()),
				*workload.NewInfo(utiltestingapi.MakeWorkload("younger", "").
					ReserveQuotaAt(utiltestingapi.MakeAdmission(preemptorCq).Obj(), now.Add(time.Second)).
					Obj()),
				*workload.NewInfo(utiltestingapi.MakeWorkload("current", "").
					ReserveQuotaAt(utiltestingapi.MakeAdmission(preemptorCq).Obj(), now).
					Obj()),
			},
			wantCandidates: []workload.Reference{"younger", "current", "older"},
		},
		"workloads with higher LQ usage first": {
			candidates: []workload.Info{
				*wlLowUsageLq,
				*wlMidUsageLq,
			},
			wantCandidates:              []workload.Reference{"mid_lq_usage", "low_lq_usage"},
			admissionFairSharingEnabled: true,
		},
		"workloads from different CQ are sorted based on priority and timestamp": {
			candidates: []workload.Info{
				*wlMidUsageLq,
				*wlHighUsageLqDifCQ,
			},
			wantCandidates:              []workload.Reference{"high_lq_usage_different_cq", "mid_lq_usage"},
			admissionFairSharingEnabled: true,
		}}

	_, log := utiltesting.ContextWithLog(t)
	for _, tc := range cases {
		features.SetFeatureGateDuringTest(t, features.AdmissionFairSharing, tc.admissionFairSharingEnabled)
		slices.SortFunc(tc.candidates, func(a, b workload.Info) int {
			return preemptioncommon.CandidatesOrdering(log, tc.admissionFairSharingEnabled, &a, &b, kueue.ClusterQueueReference(preemptorCq), now)
		})
		got := utilslices.Map(tc.candidates, func(c *workload.Info) workload.Reference {
			return workload.Reference(c.Obj.Name)
		})
		if diff := cmp.Diff(tc.wantCandidates, got); diff != "" {
			t.Errorf("Sorted with wrong order (-want,+got):\n%s", diff)
		}
	}
}

func singlePodSetAssignment(assignments flavorassigner.ResourceAssignment) flavorassigner.Assignment {
	return flavorassigner.Assignment{
		PodSets: []flavorassigner.PodSetAssignment{{
			Name:    kueue.DefaultPodSetName,
			Flavors: assignments,
			Count:   1,
		}},
	}
}

func TestPreemptionMessage(t *testing.T) {
	cases := []struct {
		preemptor     *kueue.Workload
		reason        string
		preemptorPath string
		preempteePath string
		want          string
	}{
		{
			preemptor: &kueue.Workload{},
			want:      "Preempted to accommodate a workload (UID: UNKNOWN, JobUID: UNKNOWN) due to UNKNOWN; preemptor path: UNKNOWN; preemptee path: UNKNOWN",
		},
		{
			preemptor: &kueue.Workload{ObjectMeta: metav1.ObjectMeta{UID: "uid"}},
			want:      "Preempted to accommodate a workload (UID: uid, JobUID: UNKNOWN) due to UNKNOWN; preemptor path: UNKNOWN; preemptee path: UNKNOWN",
		},
		{
			preemptor: &kueue.Workload{ObjectMeta: metav1.ObjectMeta{UID: "uid", Labels: map[string]string{controllerconstants.JobUIDLabel: "juid"}}},
			want:      "Preempted to accommodate a workload (UID: uid, JobUID: juid) due to UNKNOWN; preemptor path: UNKNOWN; preemptee path: UNKNOWN",
		},
		{
			preemptor:     &kueue.Workload{ObjectMeta: metav1.ObjectMeta{UID: "uid", Labels: map[string]string{controllerconstants.JobUIDLabel: "juid"}}},
			reason:        kueue.InClusterQueueReason,
			preemptorPath: "/a",
			preempteePath: "/b",
			want:          "Preempted to accommodate a workload (UID: uid, JobUID: juid) due to prioritization in the ClusterQueue; preemptor path: /a; preemptee path: /b",
		},
	}
	for _, tc := range cases {
		got := preemptionMessage(tc.preemptor, tc.reason, tc.preemptorPath, tc.preempteePath)
		if got != tc.want {
			t.Errorf("preemptionMessage(preemptor=kueue.Workload{UID:%v, Labels:%v}, reason=%q) returned %q, want %q", tc.preemptor.UID, tc.preemptor.Labels, tc.reason, got, tc.want)
		}
	}
}
