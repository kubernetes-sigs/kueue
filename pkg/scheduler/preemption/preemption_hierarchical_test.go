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
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestHierarchicalPreemptions(t *testing.T) {
	now := time.Now()
	flavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("default").Obj(),
	}
	cases := map[string]struct {
		clusterQueues []*kueue.ClusterQueue
		cohorts       []*kueue.Cohort
		admitted      []kueue.Workload
		incoming      *kueue.Workload
		targetCQ      kueue.ClusterQueueReference
		assignment    flavorassigner.Assignment
		wantPreempted sets.Set[string]
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
					ReserveQuota(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj()).
					Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("incoming", "").
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
			wantPreempted: sets.New(targetKeyReason("/admitted2", kueue.InCohortReclamationReason)),
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
					ReserveQuota(utiltestingapi.MakeAdmission("q_nominal").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj()).
					Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("incoming", "").
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
			wantPreempted: sets.New(targetKeyReason("/admitted2", kueue.InCohortReclamationReason)),
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
					ReserveQuota(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted2", "").
					Priority(2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj()).
					Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("incoming", "").
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
			wantPreempted: sets.New(
				targetKeyReason("/admitted1", kueue.InCohortReclamationReason),
				targetKeyReason("/admitted2", kueue.InCohortReclamationReason)),
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
							MaxPriorityThreshold: ptr.To[int32](0),
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
					ReserveQuota(utiltestingapi.MakeAdmission("q_same_cohort").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_preemptible", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltestingapi.MakeAdmission("q_same_cohort").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_own_queue", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltestingapi.MakeAdmission("q").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj()).
					Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("incoming", "").
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
			wantPreempted: sets.New(
				targetKeyReason("/admitted_preemptible", kueue.InCohortReclaimWhileBorrowingReason),
				targetKeyReason("/admitted_own_queue", kueue.InClusterQueueReason)),
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
					ReserveQuota(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_same_queue", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltestingapi.MakeAdmission("q").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj()).
					Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("incoming", "").
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
			wantPreempted: sets.New(
				targetKeyReason("/admitted_borrowing", kueue.InCohortReclamationReason)),
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
							MaxPriorityThreshold: ptr.To[int32](0),
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
					ReserveQuota(utiltestingapi.MakeAdmission("q_nominal").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_same_cohort", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltestingapi.MakeAdmission("q_same_cohort").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj()).
					Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("incoming", "").
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
			wantPreempted: sets.New(
				targetKeyReason("/admitted_same_cohort", kueue.InCohortReclaimWhileBorrowingReason)),
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
					ReserveQuota(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_same_cohort", "").
					Priority(10).
					Request(corev1.ResourceCPU, "3").
					ReserveQuota(utiltestingapi.MakeAdmission("q_same_cohort").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj()).
					Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("incoming", "").
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
			wantPreempted: sets.New(
				targetKeyReason("/admitted_borrowing", kueue.InCohortReclamationReason),
				targetKeyReason("/admitted_same_cohort", kueue.InCohortReclamationReason)),
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
							MaxPriorityThreshold: ptr.To[int32](0),
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
					ReserveQuota(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_same_cohort_preemptible", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltestingapi.MakeAdmission("q_same_cohort").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_not_preemptible", "").
					Priority(1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj()).
					Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("incoming", "").
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
			wantPreempted: sets.New(
				targetKeyReason("/admitted_borrowing", kueue.InCohortReclamationReason),
				targetKeyReason("/admitted_same_cohort_preemptible", kueue.InCohortReclaimWhileBorrowingReason)),
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
							MaxPriorityThreshold: ptr.To[int32](0),
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
					ReserveQuota(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_same_queue_preemptible", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltestingapi.MakeAdmission("q").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_not_preemptible", "").
					Priority(1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj()).
					Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("incoming", "").
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
			wantPreempted: sets.New(
				targetKeyReason("/admitted_borrowing", kueue.InCohortReclamationReason),
				targetKeyReason("/admitted_same_queue_preemptible", kueue.InClusterQueueReason)),
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
					ReserveQuota(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_prio_9", "").
					Priority(9).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_prio_10", "").
					Priority(9).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_nominal", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltestingapi.MakeAdmission("q_nominal").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj()).
					Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("incoming", "").
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
			wantPreempted: sets.New(
				targetKeyReason("/admitted_borrowing_prio_8", kueue.InCohortReclamationReason)),
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
					ReserveQuota(utiltestingapi.MakeAdmission("q_other").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_other_2", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltestingapi.MakeAdmission("q_other").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_same_cohort", "").
					Priority(0).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltestingapi.MakeAdmission("q_same_cohort").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj()).
					Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("incoming", "").
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
			wantPreempted: sets.New[string](),
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
					ReserveQuota(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").
							Assignment(corev1.ResourceMemory, "default", "1Gi").Obj()).Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_same_cohort", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "3Gi").
					ReserveQuota(utiltestingapi.MakeAdmission("q_same_cohort").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").
							Assignment(corev1.ResourceMemory, "default", "3Gi").Obj()).Obj()).
					Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("incoming", "").
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
			wantPreempted: sets.New(targetKeyReason("/admitted_borrowing", kueue.InCohortReclamationReason)),
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
							MaxPriorityThreshold: ptr.To[int32](0),
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
					ReserveQuota(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("evicted_same_cohort", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltestingapi.MakeAdmission("q_same_cohort").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.NewTime(now),
					}).
					Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("incoming", "").
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
			wantPreempted: sets.New[string](),
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
					ReserveQuota(utiltestingapi.MakeAdmission("q_borrowing").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "4").Obj()).Obj()).
					Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("incoming", "").
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
			wantPreempted: sets.New(
				targetKeyReason("/admitted_borrowing", kueue.InCohortReclamationReason)),
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
					ReserveQuota(utiltestingapi.MakeAdmission("q1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "4").Obj()).Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_2", "").
					Priority(-5).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltestingapi.MakeAdmission("q1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "4").Obj()).Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_3", "").
					Priority(-9).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltestingapi.MakeAdmission("q2").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "4").Obj()).Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_4", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltestingapi.MakeAdmission("q2").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "4").Obj()).Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_5", "").
					Priority(-4).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltestingapi.MakeAdmission("q3").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "4").Obj()).Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_6", "").
					Priority(-3).
					Request(corev1.ResourceCPU, "3").
					ReserveQuota(utiltestingapi.MakeAdmission("q3").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_7", "").
					Priority(4).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltestingapi.MakeAdmission("q4").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("admitted_borrowing_8", "").
					Priority(2).
					Request(corev1.ResourceCPU, "3").
					ReserveQuota(utiltestingapi.MakeAdmission("q4").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj()).
					Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("incoming", "").
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
			wantPreempted: sets.New(
				targetKeyReason("/admitted_borrowing_1", kueue.InCohortReclamationReason),
				targetKeyReason("/admitted_borrowing_4", kueue.InCohortReclamationReason)),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewClientBuilder().
				WithLists(&kueue.WorkloadList{Items: tc.admitted}).
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

			var lock sync.Mutex
			gotPreempted := sets.New[string]()
			broadcaster := record.NewBroadcaster()
			scheme := runtime.NewScheme()
			if err := kueue.AddToScheme(scheme); err != nil {
				t.Fatalf("Failed adding kueue scheme: %v", err)
			}
			recorder := broadcaster.NewRecorder(scheme, corev1.EventSource{Component: constants.AdmissionName})
			preemptor := New(cl, workload.Ordering{}, recorder, config.FairSharing{}, false, clocktesting.NewFakeClock(now))
			preemptor.applyPreemption = func(ctx context.Context, w *kueue.Workload, reason, _ string) error {
				lock.Lock()
				gotPreempted.Insert(targetKeyReason(workload.Key(w), reason))
				lock.Unlock()
				return nil
			}
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
			_, err = preemptor.IssuePreemptions(ctx, wlInfo, targets)
			if err != nil {
				t.Fatalf("Failed doing preemption")
			}
			if diff := cmp.Diff(tc.wantPreempted, gotPreempted, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Issued preemptions (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(beforeSnapshot, snapshotWorkingCopy, snapCmpOpts); diff != "" {
				t.Errorf("Snapshot was modified (-initial,+end):\n%s", diff)
			}
		})
	}
}
