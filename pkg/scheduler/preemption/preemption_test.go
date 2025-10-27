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
	now := time.Now()
	flavors := []*kueue.ResourceFlavor{
		utiltesting.MakeResourceFlavor("default").Obj(),
		utiltesting.MakeResourceFlavor("alpha").Obj(),
		utiltesting.MakeResourceFlavor("beta").Obj(),
	}
	defaultClusterQueues := []*kueue.ClusterQueue{
		utiltesting.MakeClusterQueue("standalone").
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "6").
					Obj(),
			).ResourceGroup(
			*utiltesting.MakeFlavorQuotas("alpha").
				Resource(corev1.ResourceMemory, "3Gi").
				Obj(),
			*utiltesting.MakeFlavorQuotas("beta").
				Resource(corev1.ResourceMemory, "3Gi").
				Obj(),
		).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj(),
		utiltesting.MakeClusterQueue("c1").
			Cohort("cohort").
			ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "6", "6").
				Resource(corev1.ResourceMemory, "3Gi", "3Gi").
				Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj(),
		utiltesting.MakeClusterQueue("c2").
			Cohort("cohort").
			ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "6", "6").
				Resource(corev1.ResourceMemory, "3Gi", "3Gi").
				Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyNever,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			Obj(),
		utiltesting.MakeClusterQueue("d1").
			Cohort("cohort-no-limits").
			ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "6").
				Resource(corev1.ResourceMemory, "3Gi").
				Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj(),
		utiltesting.MakeClusterQueue("d2").
			Cohort("cohort-no-limits").
			ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "6").
				Resource(corev1.ResourceMemory, "3Gi").
				Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyNever,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			Obj(),
		utiltesting.MakeClusterQueue("l1").
			Cohort("legion").
			ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "6", "12").
				Resource(corev1.ResourceMemory, "3Gi", "6Gi").
				Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj(),
		utiltesting.MakeClusterQueue("preventStarvation").
			ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "6").
				Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerOrNewerEqualPriority,
			}).
			Obj(),
		utiltesting.MakeClusterQueue("a_standard").
			Cohort("with_shared_cq").
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("default").
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
		utiltesting.MakeClusterQueue("b_standard").
			Cohort("with_shared_cq").
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("default").
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
		utiltesting.MakeClusterQueue("a_best_effort").
			Cohort("with_shared_cq").
			ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
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
		utiltesting.MakeClusterQueue("b_best_effort").
			Cohort("with_shared_cq").
			ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
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
		utiltesting.MakeClusterQueue("shared").
			Cohort("with_shared_cq").
			ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "10").
				Obj(),
			).
			Obj(),
		utiltesting.MakeClusterQueue("lend1").
			Cohort("cohort-lend").
			ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "6", "", "4").
				Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj(),
		utiltesting.MakeClusterQueue("lend2").
			Cohort("cohort-lend").
			ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "6", "", "2").
				Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj(),
		utiltesting.MakeClusterQueue("a").
			Cohort("cohort-three").
			ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "2").
				Resource(corev1.ResourceMemory, "2").
				Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			Obj(),
		utiltesting.MakeClusterQueue("b").
			Cohort("cohort-three").
			ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "2").
				Resource(corev1.ResourceMemory, "2").
				Obj(),
			).
			Obj(),
		utiltesting.MakeClusterQueue("c").
			Cohort("cohort-three").
			ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "2").
				Resource(corev1.ResourceMemory, "2").
				Obj(),
			).
			Obj(),
	}
	cases := map[string]struct {
		clusterQueues       []*kueue.ClusterQueue
		cohorts             []*kueue.Cohort
		admitted            []kueue.Workload
		incoming            *kueue.Workload
		targetCQ            kueue.ClusterQueueReference
		assignment          flavorassigner.Assignment
		wantPreempted       sets.Set[string]
		disableLendingLimit bool
	}{
		"preempt lowest priority": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
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
			wantPreempted: sets.New(targetKeyReason("/low", kueue.InClusterQueueReason)),
		},
		"preempt multiple": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
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
			wantPreempted: sets.New(targetKeyReason("/low", kueue.InClusterQueueReason), targetKeyReason("/mid", kueue.InClusterQueueReason)),
		},

		"no preemption for low priority": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
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
		},
		"not enough low priority workloads": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
				Request(corev1.ResourceCPU, "4").
				Obj(),
			targetCQ: "standalone",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
		},
		"some free quota, preempt low priority": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
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
			wantPreempted: sets.New(targetKeyReason("/low", kueue.InClusterQueueReason)),
		},
		"minimal set excludes low priority": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
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
			wantPreempted: sets.New(targetKeyReason("/mid", kueue.InClusterQueueReason)),
		},
		"only preempt workloads using the chosen flavor": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceMemory, "2Gi").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceMemory, "alpha", "2Gi").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("mid", "").
					Request(corev1.ResourceMemory, "1Gi").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceMemory, "beta", "1Gi").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("high", "").
					Priority(1).
					Request(corev1.ResourceMemory, "1Gi").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceMemory, "beta", "1Gi").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
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
			wantPreempted: sets.New(targetKeyReason("/mid", kueue.InClusterQueueReason)),
		},
		"reclaim quota from borrower": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("c1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c1").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-mid", "").
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "6").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "6000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
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
			wantPreempted: sets.New(targetKeyReason("/c2-mid", kueue.InCohortReclamationReason)),
		},
		"reclaim quota if workload requests 0 resources for a resource at nominal quota": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("c1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					Request(corev1.ResourceMemory, "3Gi").
					SimpleReserveQuota("c1", "default", now).
					Obj(),
				*utiltesting.MakeWorkload("c2-mid", "").
					Request(corev1.ResourceCPU, "3").
					SimpleReserveQuota("c2", "default", now).
					Obj(),
				*utiltesting.MakeWorkload("c2-high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "6").
					SimpleReserveQuota("c2", "default", now).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
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
			wantPreempted: sets.New(targetKeyReason("/c2-mid", kueue.InCohortReclamationReason)),
		},
		"no workloads borrowing": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("c1-high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c1").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
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
		},
		"not enough workloads borrowing": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("c1-high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c1").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-low-2", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
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
		},
		"preempting locally and borrowing other resources in cohort, without cohort candidates": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("c1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c1").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-high-2", "").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
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
			wantPreempted: sets.New(targetKeyReason("/c1-low", kueue.InClusterQueueReason)),
		},
		"preempting locally and borrowing same resource in cohort": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("c1-med", "").
					Priority(0).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c1").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c1").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
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
			wantPreempted: sets.New(targetKeyReason("/c1-low", kueue.InClusterQueueReason)),
		},
		"preempting locally and borrowing same resource in cohort; no borrowing limit in the cohort": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("d1-med", "").
					Priority(0).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("d1").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("d1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("d1").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("d2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("d2").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
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
			wantPreempted: sets.New(targetKeyReason("/d1-low", kueue.InClusterQueueReason)),
		},
		"preempting locally and borrowing other resources in cohort, with cohort candidates": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("c1-med", "").
					Priority(0).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c1").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "5").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "5000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-low-2", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-low-3", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
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
			wantPreempted: sets.New(targetKeyReason("/c1-med", kueue.InClusterQueueReason)),
		},
		"preempting locally and not borrowing same resource in 1-queue cohort": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("l1-med", "").
					Priority(0).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("l1").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("l1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("l1").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
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
			wantPreempted: sets.New(targetKeyReason("/l1-med", kueue.InClusterQueueReason)),
		},
		"do not reclaim borrowed quota from same priority for withinCohort=ReclaimFromLowerPriority": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("c1", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c1").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-1", "").
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-2", "").
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
				Request(corev1.ResourceCPU, "4").
				Obj(),
			targetCQ: "c1",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
		},
		"reclaim borrowed quota from same priority for withinCohort=ReclaimFromAny": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("c1-1", "").
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c1").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c1-2", "").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c1").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
				Request(corev1.ResourceCPU, "4").
				Obj(),
			targetCQ: "c2",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: sets.New(targetKeyReason("/c1-1", kueue.InCohortReclamationReason)),
		},
		"preempt from all ClusterQueues in cohort": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("c1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c1").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c1-mid", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c1").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-mid", "").
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
				Request(corev1.ResourceCPU, "4").
				Obj(),
			targetCQ: "c1",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: sets.New(targetKeyReason("/c1-low", kueue.InClusterQueueReason), targetKeyReason("/c2-low", kueue.InCohortReclamationReason)),
		},
		"can't preempt workloads in ClusterQueue for withinClusterQueue=Never": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("c2-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
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
		},
		"each podset preempts a different flavor": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("low-alpha", "").
					Priority(-1).
					Request(corev1.ResourceMemory, "2Gi").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceMemory, "alpha", "2Gi").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("low-beta", "").
					Priority(-1).
					Request(corev1.ResourceMemory, "2Gi").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceMemory, "beta", "2Gi").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
				PodSets(
					*utiltesting.MakePodSet("launcher", 1).
						Request(corev1.ResourceMemory, "2Gi").Obj(),
					*utiltesting.MakePodSet("workers", 2).
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
			wantPreempted: sets.New(targetKeyReason("/low-alpha", kueue.InClusterQueueReason), targetKeyReason("/low-beta", kueue.InClusterQueueReason)),
		},
		"preempt newer workloads with the same priority": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("wl1", "").
					Priority(2).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("preventStarvation").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("wl2", "").
					Priority(1).
					Creation(now).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("preventStarvation").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2").
								Obj()).
							Obj(),
						now.Add(time.Second),
					).
					Obj(),
				*utiltesting.MakeWorkload("wl3", "").
					Priority(1).
					Creation(now).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("preventStarvation").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
				Priority(1).
				Creation(now.Add(-15 * time.Second)).
				PodSets(
					*utiltesting.MakePodSet("launcher", 1).
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
			wantPreempted: sets.New(targetKeyReason("/wl2", kueue.InClusterQueueReason)),
		},
		"use BorrowWithinCohort; allow preempting a lower-priority workload from another ClusterQueue while borrowing": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("a_best_effort_low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("a_best_effort").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "10").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b_best_effort_low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b_best_effort").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
				Request(corev1.ResourceCPU, "10").
				Obj(),
			targetCQ: "a_standard",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: sets.New(targetKeyReason("/a_best_effort_low", kueue.InCohortReclaimWhileBorrowingReason)),
		},
		"use BorrowWithinCohort; don't allow preempting a lower-priority workload with priority above MaxPriorityThreshold, if borrowing is required even after the preemption": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("b_standard", "").
					Priority(1).
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b_standard").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "10000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
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
		},
		"use BorrowWithinCohort; allow preempting a lower-priority workload with priority above MaxPriorityThreshold, if borrowing is not required after the preemption": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				// this admitted workload consumes all resources so it needs to be preempted to run a new workload
				*utiltesting.MakeWorkload("b_standard", "").
					Priority(1).
					Request(corev1.ResourceCPU, "13").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b_standard").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "13000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
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
			wantPreempted: sets.New(targetKeyReason("/b_standard", kueue.InCohortReclamationReason)),
		},
		"use BorrowWithinCohort; don't allow for preemption of lower-priority workload from the same ClusterQueue": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("a_standard", "").
					Priority(1).
					Request(corev1.ResourceCPU, "13").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("a_standard").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "13000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
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
		},
		"use BorrowWithinCohort; only preempt from CQ if no workloads below threshold and already above nominal": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("a_standard_1", "").
					Priority(1).
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("a_standard").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "10").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("a_standard_2", "").
					Priority(1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("a_standard").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b_standard_1", "").
					Priority(1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b_standard").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b_standard_2", "").
					Priority(2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b_standard").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
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
			wantPreempted: sets.New(targetKeyReason("/b_standard_1", kueue.InClusterQueueReason)),
		},
		"use BorrowWithinCohort; preempt from CQ and from other CQs with workloads below threshold": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("b_standard_high", "").
					Priority(2).
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b_standard").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "10").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b_standard_mid", "").
					Priority(1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b_standard").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("a_best_effort_low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("a_best_effort").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("a_best_effort_lower", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("a_best_effort").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
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
			wantPreempted: sets.New(targetKeyReason("/b_standard_mid", kueue.InClusterQueueReason), targetKeyReason("/a_best_effort_lower", kueue.InCohortReclaimWhileBorrowingReason)),
		},
		"reclaim quota from lender": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("lend1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("lend1").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("lend2-mid", "").
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("lend2").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("lend2-high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("lend2").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
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
			wantPreempted: sets.New(targetKeyReason("/lend2-mid", kueue.InCohortReclamationReason)),
		},
		"preempt from all ClusterQueues in cohort-lend": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("lend1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("lend1").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("lend1-mid", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("lend1").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "2000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("lend2-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("lend2").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("lend2-mid", "").
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("lend2").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "4000m").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
				Request(corev1.ResourceCPU, "4").
				Obj(),
			targetCQ: "lend1",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: sets.New(targetKeyReason("/lend1-low", kueue.InClusterQueueReason), targetKeyReason("/lend2-low", kueue.InCohortReclamationReason)),
		},
		"cannot preempt from other ClusterQueues if exceeds requestable quota including lending limit": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("lend2-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("lend2").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "10").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
				Request(corev1.ResourceCPU, "9").
				Obj(),
			targetCQ: "lend1",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: nil,
		},
		"preemptions from cq when target queue is exhausted for the single requested resource": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("a").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("a2", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("a").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("a3", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("a").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b1", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b3", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
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
			wantPreempted: sets.New(targetKeyReason("/a1", kueue.InClusterQueueReason), targetKeyReason("/a2", kueue.InClusterQueueReason)),
		},
		"preemptions from cq when target queue is exhausted for two requested resources": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("a").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Assignment(corev1.ResourceMemory, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("a2", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("a").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Assignment(corev1.ResourceMemory, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("a3", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("a").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Assignment(corev1.ResourceMemory, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b1", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Assignment(corev1.ResourceMemory, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Assignment(corev1.ResourceMemory, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b3", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Assignment(corev1.ResourceMemory, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
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
			wantPreempted: sets.New(targetKeyReason("/a1", kueue.InClusterQueueReason), targetKeyReason("/a2", kueue.InClusterQueueReason)),
		},
		"preemptions from cq when target queue is exhausted for one requested resource, but not the other": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("a").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("a2", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("a").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("a3", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("a").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b1", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b3", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
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
			wantPreempted: sets.New(targetKeyReason("/a1", kueue.InClusterQueueReason), targetKeyReason("/a2", kueue.InClusterQueueReason)),
		},
		"allow preemption from other cluster queues if target cq is not exhausted for the requested resource": {
			clusterQueues: defaultClusterQueues,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("a").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b1", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b3", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b4", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b5", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "1").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
				Request(corev1.ResourceCPU, "2").
				Obj(),
			targetCQ: "a",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: sets.New(targetKeyReason("/a1", kueue.InClusterQueueReason), targetKeyReason("/b5", kueue.InCohortReclamationReason)),
		},
		"long range preemption": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("cq-left").
					Cohort("cohort-left").
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "10").
						Obj(),
					).Obj(),
				utiltesting.MakeClusterQueue("cq-right").
					Cohort("cohort-right").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "0").
						Obj(),
					).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltesting.MakeCohort("cohort-left").Parent("root").Obj(),
				utiltesting.MakeCohort("cohort-right").Parent("root").Obj(),
			},
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("to-be-preempted", "").
					Request(corev1.ResourceCPU, "5").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("cq-right").
							PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "5").
								Obj()).
							Obj(),
						now,
					).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("incoming", "").
				Request(corev1.ResourceCPU, "8").
				Obj(),
			targetCQ: "cq-left",
			assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "default",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: sets.New(targetKeyReason("/to-be-preempted", kueue.InCohortReclamationReason)),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if tc.disableLendingLimit {
				features.SetFeatureGateDuringTest(t, features.LendingLimit, false)
			}
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
			preempted, err := preemptor.IssuePreemptions(ctx, wlInfo, targets)
			if err != nil {
				t.Fatalf("Failed doing preemption")
			}
			if diff := cmp.Diff(tc.wantPreempted, gotPreempted, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Issued preemptions (-want,+got):\n%s", diff)
			}
			if preempted != tc.wantPreempted.Len() {
				t.Errorf("Reported %d preemptions, want %d", preempted, tc.wantPreempted.Len())
			}

			if diff := cmp.Diff(beforeSnapshot, snapshotWorkingCopy, snapCmpOpts); diff != "" {
				t.Errorf("Snapshot was modified (-initial,+end):\n%s", diff)
			}
		})
	}
}

func targetKeyReason(key workload.Reference, reason string) string {
	return fmt.Sprintf("%s:%s", key, reason)
}
func TestCandidatesOrdering(t *testing.T) {
	now := time.Now()

	preemptorCq := "preemptor"

	wlLowUsageLq := workload.NewInfo(utiltesting.MakeWorkload("low_lq_usage", "").
		Queue("low_usage_lq").
		ReserveQuotaAt(utiltesting.MakeAdmission(preemptorCq).Obj(), now).
		Priority(1).
		Obj())
	wlLowUsageLq.LocalQueueFSUsage = ptr.To(0.1)

	wlMidUsageLq := workload.NewInfo(utiltesting.MakeWorkload("mid_lq_usage", "").
		Queue("mid_usage_lq").
		ReserveQuotaAt(utiltesting.MakeAdmission(preemptorCq).Obj(), now).
		Priority(10).
		Obj())
	wlMidUsageLq.LocalQueueFSUsage = ptr.To(0.5)

	wlHighUsageLqDifCQ := workload.NewInfo(utiltesting.MakeWorkload("high_lq_usage_different_cq", "").
		Queue("high_usage_lq_different_cq").
		ReserveQuotaAt(utiltesting.MakeAdmission("different_cq").Obj(), now).
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
				*workload.NewInfo(utiltesting.MakeWorkload("high", "").
					ReserveQuotaAt(utiltesting.MakeAdmission(preemptorCq).Obj(), now).
					Priority(10).
					Obj()),
				*workload.NewInfo(utiltesting.MakeWorkload("low", "").
					ReserveQuotaAt(utiltesting.MakeAdmission(preemptorCq).Obj(), now).
					Priority(-10).
					Obj()),
			},
			wantCandidates: []workload.Reference{"low", "high"},
		},
		"evicted workload first": {
			candidates: []workload.Info{
				*workload.NewInfo(utiltesting.MakeWorkload("other", "").
					ReserveQuotaAt(utiltesting.MakeAdmission(preemptorCq).Obj(), now).
					Priority(10).
					Obj()),
				*workload.NewInfo(utiltesting.MakeWorkload("evicted", "").
					SetOrReplaceCondition(metav1.Condition{
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
				*workload.NewInfo(utiltesting.MakeWorkload("preemptorCq", "").
					ReserveQuotaAt(utiltesting.MakeAdmission(preemptorCq).Obj(), now).
					Priority(10).
					Obj()),
				*workload.NewInfo(utiltesting.MakeWorkload("other", "").
					ReserveQuotaAt(utiltesting.MakeAdmission("other").Obj(), now).
					Priority(10).
					Obj()),
			},
			wantCandidates: []workload.Reference{"other", "preemptorCq"},
		},
		"old workloads last": {
			candidates: []workload.Info{
				*workload.NewInfo(utiltesting.MakeWorkload("older", "").
					ReserveQuotaAt(utiltesting.MakeAdmission(preemptorCq).Obj(), now.Add(-time.Second)).
					Obj()),
				*workload.NewInfo(utiltesting.MakeWorkload("younger", "").
					ReserveQuotaAt(utiltesting.MakeAdmission(preemptorCq).Obj(), now.Add(time.Second)).
					Obj()),
				*workload.NewInfo(utiltesting.MakeWorkload("current", "").
					ReserveQuotaAt(utiltesting.MakeAdmission(preemptorCq).Obj(), now).
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
		preemptor *kueue.Workload
		reason    string
		want      string
	}{
		{
			preemptor: &kueue.Workload{},
			want:      "Preempted to accommodate a workload (UID: UNKNOWN, JobUID: UNKNOWN) due to UNKNOWN",
		},
		{
			preemptor: &kueue.Workload{ObjectMeta: metav1.ObjectMeta{UID: "uid"}},
			want:      "Preempted to accommodate a workload (UID: uid, JobUID: UNKNOWN) due to UNKNOWN",
		},
		{
			preemptor: &kueue.Workload{ObjectMeta: metav1.ObjectMeta{UID: "uid", Labels: map[string]string{controllerconstants.JobUIDLabel: "juid"}}},
			want:      "Preempted to accommodate a workload (UID: uid, JobUID: juid) due to UNKNOWN",
		},
		{
			preemptor: &kueue.Workload{ObjectMeta: metav1.ObjectMeta{UID: "uid", Labels: map[string]string{controllerconstants.JobUIDLabel: "juid"}}},
			reason:    kueue.InClusterQueueReason,
			want:      "Preempted to accommodate a workload (UID: uid, JobUID: juid) due to prioritization in the ClusterQueue",
		},
	}
	for _, tc := range cases {
		got := preemptionMessage(tc.preemptor, tc.reason)
		if got != tc.want {
			t.Errorf("preemptionMessage(preemptor=kueue.Workload{UID:%v, Labels:%v}, reason=%q) returned %q, want %q", tc.preemptor.UID, tc.preemptor.Labels, tc.reason, got, tc.want)
		}
	}
}
