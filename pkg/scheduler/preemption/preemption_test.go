/*
Copyright 2023 The Kubernetes Authors.

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
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

var snapCmpOpts = []cmp.Option{
	cmpopts.EquateEmpty(),
	cmpopts.IgnoreUnexported(cache.ClusterQueue{}),
	cmpopts.IgnoreFields(cache.Cohort{}, "AllocatableResourceGeneration"),
	cmpopts.IgnoreFields(cache.ClusterQueue{}, "AllocatableResourceGeneration"),
	cmp.Transformer("Cohort.Members", func(s sets.Set[*cache.ClusterQueue]) sets.Set[string] {
		result := make(sets.Set[string], len(s))
		for cq := range s {
			result.Insert(cq.Name)
		}
		return result
	}), // avoid recursion.
}

func TestPreemption(t *testing.T) {
	flavors := []*kueue.ResourceFlavor{
		utiltesting.MakeResourceFlavor("default").Obj(),
		utiltesting.MakeResourceFlavor("alpha").Obj(),
		utiltesting.MakeResourceFlavor("beta").Obj(),
	}
	clusterQueues := []*kueue.ClusterQueue{
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
				Resource(corev1.ResourceCPU, "6", "12").
				Resource(corev1.ResourceMemory, "3Gi", "6Gi").
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
				Resource(corev1.ResourceCPU, "6", "12").
				Resource(corev1.ResourceMemory, "3Gi", "6Gi").
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
		admitted           []kueue.Workload
		incoming           *kueue.Workload
		targetCQ           string
		assignment         flavorassigner.Assignment
		wantPreempted      sets.Set[string]
		enableLendingLimit bool
	}{
		"preempt lowest priority": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "2000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "2000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "2000m").Obj()).
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
			wantPreempted: sets.New("/low"),
		},
		"preempt multiple": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "2000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "2000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "2000m").Obj()).
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
			wantPreempted: sets.New("/low", "/mid"),
		},

		"no preemption for low priority": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuota(utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "3000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "3").
					ReserveQuota(utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "3000m").Obj()).
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
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuota(utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "3000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "3").
					ReserveQuota(utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "3000m").Obj()).
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
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "1000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "1000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuota(utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "3000m").Obj()).
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
			wantPreempted: sets.New("/low"),
		},
		"minimal set excludes low priority": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "1000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "2000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuota(utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "3000m").Obj()).
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
			wantPreempted: sets.New("/mid"),
		},
		"only preempt workloads using the chosen flavor": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("low", "").
					Priority(-1).
					Request(corev1.ResourceMemory, "2Gi").
					ReserveQuota(utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceMemory, "alpha", "2Gi").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("mid", "").
					Request(corev1.ResourceMemory, "1Gi").
					ReserveQuota(utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceMemory, "beta", "1Gi").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("high", "").
					Priority(1).
					Request(corev1.ResourceMemory, "1Gi").
					ReserveQuota(utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceMemory, "beta", "1Gi").Obj()).
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
			wantPreempted: sets.New("/mid"),
		},
		"reclaim quota from borrower": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("c1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuota(utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceCPU, "default", "3000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c2-mid", "").
					Request(corev1.ResourceCPU, "3").
					ReserveQuota(utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "3000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c2-high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "6").
					ReserveQuota(utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "6000m").Obj()).
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
			wantPreempted: sets.New("/c2-mid"),
		},
		"no workloads borrowing": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("c1-high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceCPU, "default", "4000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "4000m").Obj()).
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
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("c1-high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceCPU, "default", "4000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "4000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c2-low-2", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "4000m").Obj()).
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
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("c1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceCPU, "default", "4000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "4000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c2-high-2", "").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "4000m").Obj()).
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
					Name: "alpha",
					Mode: flavorassigner.Preempt,
				},
			}),
			wantPreempted: sets.New("/c1-low"),
		},
		"preempting locally and borrowing same resource in cohort": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("c1-med", "").
					Priority(0).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceCPU, "default", "4000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceCPU, "default", "4000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "4000m").Obj()).
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
			wantPreempted: sets.New("/c1-low"),
		},
		"preempting locally and borrowing same resource in cohort; no borrowing limit in the cohort": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("d1-med", "").
					Priority(0).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("d1").Assignment(corev1.ResourceCPU, "default", "4").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("d1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("d1").Assignment(corev1.ResourceCPU, "default", "4").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("d2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("d2").Assignment(corev1.ResourceCPU, "default", "4").Obj()).
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
			wantPreempted: sets.New("/d1-low"),
		},
		"preempting locally and borrowing other resources in cohort, with cohort candidates": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("c1-med", "").
					Priority(0).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceCPU, "default", "4000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "5").
					ReserveQuota(utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "5000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c2-low-2", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "1000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c2-low-3", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "1000m").Obj()).
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
			wantPreempted: sets.New("/c1-med"),
		},
		"preempting locally and not borrowing same resource in 1-queue cohort": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("l1-med", "").
					Priority(0).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("l1").Assignment(corev1.ResourceCPU, "default", "4000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("l1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("l1").Assignment(corev1.ResourceCPU, "default", "2000m").Obj()).
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
			wantPreempted: sets.New("/l1-med"),
		},
		"do not reclaim borrowed quota from same priority for withinCohort=ReclaimFromLowerPriority": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("c1", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceCPU, "default", "2000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c2-1", "").
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "4000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c2-2", "").
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "4000m").Obj()).
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
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("c1-1", "").
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceCPU, "default", "4000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c1-2", "").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceCPU, "default", "4000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c2", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "2000m").Obj()).
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
			wantPreempted: sets.New("/c1-1"),
		},
		"preempt from all ClusterQueues in cohort": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("c1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuota(utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceCPU, "default", "3000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c1-mid", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceCPU, "default", "2000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c2-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuota(utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "3000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("c2-mid", "").
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "4000m").Obj()).
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
			wantPreempted: sets.New("/c1-low", "/c2-low"),
		},
		"can't preempt workloads in ClusterQueue for withinClusterQueue=Never": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("c2-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuota(utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "3000m").Obj()).
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
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("low-alpha", "").
					Priority(-1).
					Request(corev1.ResourceMemory, "2Gi").
					ReserveQuota(utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceMemory, "alpha", "2Gi").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("low-beta", "").
					Priority(-1).
					Request(corev1.ResourceMemory, "2Gi").
					ReserveQuota(utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceMemory, "beta", "2Gi").Obj()).
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
					},
					{
						Name: "workers",
						Flavors: flavorassigner.ResourceAssignment{
							corev1.ResourceMemory: {
								Name: "beta",
								Mode: flavorassigner.Preempt,
							},
						},
					},
				},
			},
			wantPreempted: sets.New("/low-alpha", "/low-beta"),
		},
		"preempt newer workloads with the same priority": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("wl1", "").
					Priority(2).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("preventStarvation").Assignment(corev1.ResourceCPU, "default", "2").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("wl2", "").
					Priority(1).
					Creation(time.Now()).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("preventStarvation").Assignment(corev1.ResourceCPU, "default", "2").Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.NewTime(time.Now().Add(time.Second)),
					}).
					Obj(),
				*utiltesting.MakeWorkload("wl3", "").
					Priority(1).
					Creation(time.Now()).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("preventStarvation").Assignment(corev1.ResourceCPU, "default", "2").Obj()).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("in", "").
				Priority(1).
				Creation(time.Now().Add(-15 * time.Second)).
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
			wantPreempted: sets.New("/wl2"),
		},
		"use BorrowWithinCohort; allow preempting a lower-priority workload from another ClusterQueue while borrowing": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("a_best_effort_low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "10").
					ReserveQuota(utiltesting.MakeAdmission("a_best_effort").Assignment(corev1.ResourceCPU, "default", "10000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b_best_effort_low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("b_best_effort").Assignment(corev1.ResourceCPU, "default", "1000m").Obj()).
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
			wantPreempted: sets.New("/a_best_effort_low"),
		},
		"use BorrowWithinCohort; don't allow preempting a lower-priority workload with priority above MaxPriorityThreshold, if borrowing is required even after the preemption": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("b_standard", "").
					Priority(1).
					Request(corev1.ResourceCPU, "10").
					ReserveQuota(utiltesting.MakeAdmission("b_standard").Assignment(corev1.ResourceCPU, "default", "10000m").Obj()).
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
			admitted: []kueue.Workload{
				// this admitted workload consumes all resources so it needs to be preempted to run a new workload
				*utiltesting.MakeWorkload("b_standard", "").
					Priority(1).
					Request(corev1.ResourceCPU, "13").
					ReserveQuota(utiltesting.MakeAdmission("b_standard").Assignment(corev1.ResourceCPU, "default", "13000m").Obj()).
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
			wantPreempted: sets.New("/b_standard"),
		},
		"use BorrowWithinCohort; don't allow for preemption of lower-priority workload from the same ClusterQueue": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("a_standard", "").
					Priority(1).
					Request(corev1.ResourceCPU, "13").
					ReserveQuota(utiltesting.MakeAdmission("a_standard").Assignment(corev1.ResourceCPU, "default", "13000m").Obj()).
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
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("a_standard_1", "").
					Priority(1).
					Request(corev1.ResourceCPU, "10").
					ReserveQuota(utiltesting.MakeAdmission("a_standard").Assignment(corev1.ResourceCPU, "default", "10").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a_standard_2", "").
					Priority(1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("a_standard").Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b_standard_1", "").
					Priority(1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("b_standard").Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b_standard_2", "").
					Priority(2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("b_standard").Assignment(corev1.ResourceCPU, "default", "1").Obj()).
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
			wantPreempted: sets.New("/b_standard_1"),
		},
		"use BorrowWithinCohort; preempt from CQ and from other CQs with workloads below threshold": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("b_standard_high", "").
					Priority(2).
					Request(corev1.ResourceCPU, "10").
					ReserveQuota(utiltesting.MakeAdmission("b_standard").Assignment(corev1.ResourceCPU, "default", "10").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b_standard_mid", "").
					Priority(1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("b_standard").Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a_best_effort_low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("a_best_effort").Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a_best_effort_lower", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("a_best_effort").Assignment(corev1.ResourceCPU, "default", "1").Obj()).
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
			wantPreempted: sets.New("/b_standard_mid", "/a_best_effort_lower"),
		},
		"reclaim quota from lender": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("lend1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuota(utiltesting.MakeAdmission("lend1").Assignment(corev1.ResourceCPU, "default", "3000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("lend2-mid", "").
					Request(corev1.ResourceCPU, "3").
					ReserveQuota(utiltesting.MakeAdmission("lend2").Assignment(corev1.ResourceCPU, "default", "3000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("lend2-high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("lend2").Assignment(corev1.ResourceCPU, "default", "4000m").Obj()).
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
			wantPreempted:      sets.New("/lend2-mid"),
			enableLendingLimit: true,
		},
		"preempt from all ClusterQueues in cohort-lend": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("lend1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuota(utiltesting.MakeAdmission("lend1").Assignment(corev1.ResourceCPU, "default", "3000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("lend1-mid", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("lend1").Assignment(corev1.ResourceCPU, "default", "2000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("lend2-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuota(utiltesting.MakeAdmission("lend2").Assignment(corev1.ResourceCPU, "default", "3000m").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("lend2-mid", "").
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("lend2").Assignment(corev1.ResourceCPU, "default", "4000m").Obj()).
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
			wantPreempted:      sets.New("/lend1-low", "/lend2-low"),
			enableLendingLimit: true,
		},
		"cannot preempt from other ClusterQueues if exceeds requestable quota including lending limit": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("lend2-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "10").
					ReserveQuota(utiltesting.MakeAdmission("lend2").Assignment(corev1.ResourceCPU, "default", "10").Obj()).
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
			wantPreempted:      nil,
			enableLendingLimit: true,
		},
		"preemptions from cq when target queue is exhausted for the single requested resource": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("a").Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a2", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("a").Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a3", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("a").Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b3", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Obj()).
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
			wantPreempted: sets.New("/a1", "/a2"),
		},
		"preemptions from cq when target queue is exhausted for two requested resources": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuota(utiltesting.MakeAdmission("a").Assignment(corev1.ResourceCPU, "default", "1").Assignment(corev1.ResourceMemory, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a2", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuota(utiltesting.MakeAdmission("a").Assignment(corev1.ResourceCPU, "default", "1").Assignment(corev1.ResourceMemory, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a3", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuota(utiltesting.MakeAdmission("a").Assignment(corev1.ResourceCPU, "default", "1").Assignment(corev1.ResourceMemory, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuota(utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Assignment(corev1.ResourceMemory, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuota(utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Assignment(corev1.ResourceMemory, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b3", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuota(utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Assignment(corev1.ResourceMemory, "default", "1").Obj()).
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
			wantPreempted: sets.New("/a1", "/a2"),
		},
		"preemptions from cq when target queue is exhausted for one requested resource, but not the other": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("a").Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a2", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("a").Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("a3", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("a").Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b3", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Obj()).
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
			wantPreempted: sets.New("/a1", "/a2"),
		},
		"allow preemption from other cluster queues if target cq is not exhausted for the requested resource": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("a").Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b1", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b3", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b4", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("b5", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Obj()).
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
			wantPreempted: sets.New("/a1", "/b5"),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			defer features.SetFeatureGateDuringTest(t, features.LendingLimit, tc.enableLendingLimit)()
			ctx, _ := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewClientBuilder().
				WithLists(&kueue.WorkloadList{Items: tc.admitted}).
				Build()

			cqCache := cache.New(cl)
			for _, flv := range flavors {
				cqCache.AddOrUpdateResourceFlavor(flv)
			}
			for _, cq := range clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
				}
			}

			var lock sync.Mutex
			gotPreempted := sets.New[string]()
			broadcaster := record.NewBroadcaster()
			scheme := runtime.NewScheme()
			recorder := broadcaster.NewRecorder(scheme, corev1.EventSource{Component: constants.AdmissionName})
			preemptor := New(cl, workload.Ordering{}, recorder, false)
			preemptor.applyPreemption = func(ctx context.Context, w *kueue.Workload, _, _ string) error {
				lock.Lock()
				gotPreempted.Insert(workload.Key(w))
				lock.Unlock()
				return nil
			}

			startingSnapshot := cqCache.Snapshot()
			// make a working copy of the snapshot than preemption can temporarily modify
			snapshot := cqCache.Snapshot()
			wlInfo := workload.NewInfo(tc.incoming)
			wlInfo.ClusterQueue = tc.targetCQ
			targetClusterQueue := snapshot.ClusterQueues[wlInfo.ClusterQueue]
			targets := preemptor.GetTargets(*wlInfo, tc.assignment, &snapshot)
			preempted, err := preemptor.IssuePreemptions(ctx, wlInfo, targets, targetClusterQueue)
			if err != nil {
				t.Fatalf("Failed doing preemption")
			}
			if diff := cmp.Diff(tc.wantPreempted, gotPreempted, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Issued preemptions (-want,+got):\n%s", diff)
			}
			if preempted != tc.wantPreempted.Len() {
				t.Errorf("Reported %d preemptions, want %d", preempted, tc.wantPreempted.Len())
			}
			if diff := cmp.Diff(startingSnapshot, snapshot, snapCmpOpts...); diff != "" {
				t.Errorf("Snapshot was modified (-initial,+end):\n%s", diff)
			}
		})
	}
}

func TestFairPreemptions(t *testing.T) {
	now := time.Now()
	flavors := []*kueue.ResourceFlavor{
		utiltesting.MakeResourceFlavor("default").Obj(),
	}
	clusterQueues := []*kueue.ClusterQueue{
		utiltesting.MakeClusterQueue("a").
			Cohort("all").
			ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "3").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				BorrowWithinCohort: &kueue.BorrowWithinCohort{
					Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
					MaxPriorityThreshold: ptr.To[int32](-3),
				},
			}).
			Obj(),
		utiltesting.MakeClusterQueue("b").
			Cohort("all").
			ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "3").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				BorrowWithinCohort: &kueue.BorrowWithinCohort{
					Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
					MaxPriorityThreshold: ptr.To[int32](-3),
				},
			}).
			Obj(),
		utiltesting.MakeClusterQueue("c").
			Cohort("all").
			ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "3").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				BorrowWithinCohort: &kueue.BorrowWithinCohort{
					Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
					MaxPriorityThreshold: ptr.To[int32](-3),
				},
			}).
			Obj(),
		utiltesting.MakeClusterQueue("preemptible").
			Cohort("all").
			ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "0").Obj()).
			Obj(),
	}
	unitWl := *utiltesting.MakeWorkload("unit", "").Request(corev1.ResourceCPU, "1")
	cases := map[string]struct {
		admitted      []kueue.Workload
		incoming      *kueue.Workload
		targetCQ      string
		wantPreempted sets.Set[string]
	}{
		"reclaim nominal from user using the most": {
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a3").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b2").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b3").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b4").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b5").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("c1").SimpleReserveQuota("c", "default", now).Obj(),
			},
			incoming:      unitWl.Clone().Name("c_incoming").Obj(),
			targetCQ:      "c",
			wantPreempted: sets.New("/b1"),
		},
		"can reclaim from queue using less, if taking the latest workload from user using the most isn't enough": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltesting.MakeWorkload("a2", "").Request(corev1.ResourceCPU, "1").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltesting.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("b2", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming:      utiltesting.MakeWorkload("c_incoming", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("a", "default", now).Obj(),
			targetCQ:      "c",
			wantPreempted: sets.New("/a1"), // attempts to preempt b1, but it's not enough.
		},
		"reclaim borrowable quota from user using the most": {
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a3").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b2").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b3").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b4").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b5").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("c1").SimpleReserveQuota("c", "default", now).Obj(),
			},
			incoming:      unitWl.Clone().Name("a_incoming").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New("/b1"),
		},
		"preempt one from each CQ borrowing": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "").Request(corev1.ResourceCPU, "0.5").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltesting.MakeWorkload("a2", "").Request(corev1.ResourceCPU, "0.5").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltesting.MakeWorkload("a3", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltesting.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "0.5").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("b2", "").Request(corev1.ResourceCPU, "0.5").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("b3", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming:      utiltesting.MakeWorkload("c_incoming", "").Request(corev1.ResourceCPU, "2").Obj(),
			targetCQ:      "c",
			wantPreempted: sets.New("/a1", "/b1"),
		},
		"can't preempt when everyone under nominal": {
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a3").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b2").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b3").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("c1").SimpleReserveQuota("c", "default", now).Obj(),
				*unitWl.Clone().Name("c2").SimpleReserveQuota("c", "default", now).Obj(),
				*unitWl.Clone().Name("c3").SimpleReserveQuota("c", "default", now).Obj(),
			},
			incoming: unitWl.Clone().Name("c_incoming").Obj(),
			targetCQ: "c",
		},
		"can't preempt when it would switch the imbalance": {
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a3").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b2").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b3").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b4").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b5").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming: utiltesting.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "2").Obj(),
			targetCQ: "a",
		},
		"can preempt lower priority workloads from same CQ": {
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1_low").Priority(-1).SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2_low").Priority(-1).SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a3").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a4").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b2").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b3").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b4").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b5").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming:      utiltesting.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "2").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New("/a1_low", "/a2_low"),
		},
		"can preempt a combination of same CQ and highest user": {
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a_low").Priority(-1).SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a3").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b2").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b3").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b4").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b5").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b6").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming:      utiltesting.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "2").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New("/a_low", "/b1"),
		},
		"preempt huge workload if there is no other option, as long as the target CQ gets a lower share": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "9").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming:      utiltesting.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "2").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New("/b1"),
		},
		"can't preempt huge workload if the incoming is also huge": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltesting.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "7").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming: utiltesting.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "5").Obj(),
			targetCQ: "a",
		},
		"can't preempt 2 smaller workloads if the incoming is huge": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("b2", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("b3", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming: utiltesting.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "6").Obj(),
			targetCQ: "a",
		},
		"preempt from target and others even if over nominal": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("a1_low", "").Priority(-1).Request(corev1.ResourceCPU, "2").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("a2_low", "").Priority(-1).Request(corev1.ResourceCPU, "1").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("b2", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming:      utiltesting.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "4").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New("/a1_low", "/b1"),
		},
		"prefer to preempt workloads that don't make the target CQ have the biggest share": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("b2", "").Request(corev1.ResourceCPU, "1").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("b3", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("c1", "").Request(corev1.ResourceCPU, "1").SimpleReserveQuota("c", "default", now).Obj(),
			},
			incoming: utiltesting.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "3.5").Obj(),
			targetCQ: "a",
			// It would have been possible to preempt "/b1" under rule S2-b, but S2-a was possible first.
			wantPreempted: sets.New("/b2"),
		},
		"preempt from different cluster queues if the end result has a smaller max share": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("b2", "").Request(corev1.ResourceCPU, "2.5").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("c1", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("c", "default", now).Obj(),
				*utiltesting.MakeWorkload("c2", "").Request(corev1.ResourceCPU, "2.5").SimpleReserveQuota("c", "default", now).Obj(),
			},
			incoming:      utiltesting.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "3.5").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New("/b1", "/c1"),
		},
		"scenario above does not flap": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "").Request(corev1.ResourceCPU, "3.5").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltesting.MakeWorkload("b2", "").Request(corev1.ResourceCPU, "2.5").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("c2", "").Request(corev1.ResourceCPU, "2.5").SimpleReserveQuota("c", "default", now).Obj(),
			},
			incoming: utiltesting.MakeWorkload("b_incoming", "").Request(corev1.ResourceCPU, "2").Obj(),
			targetCQ: "b",
		},
		"cannot preempt if it would make the candidate CQ go under nominal after preempting one element": {
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("b2", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("c1", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("c", "default", now).Obj(),
			},
			incoming: utiltesting.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "4").Obj(),
			targetCQ: "a",
		},
		"workloads under priority threshold can always be preempted": {
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a3").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b2").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b3").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("preemptible1").Priority(-3).SimpleReserveQuota("preemptible", "default", now).Obj(),
				*unitWl.Clone().Name("preemptible2").Priority(-3).SimpleReserveQuota("preemptible", "default", now).Obj(),
				*unitWl.Clone().Name("preemptible3").Priority(-3).SimpleReserveQuota("preemptible", "default", now).Obj(),
			},
			incoming:      utiltesting.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "2").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New("/preemptible1", "/preemptible2"),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			// Set name as UID so that candidates sorting is predictable.
			for i := range tc.admitted {
				tc.admitted[i].UID = types.UID(tc.admitted[i].Name)
			}
			cl := utiltesting.NewClientBuilder().
				WithLists(&kueue.WorkloadList{Items: tc.admitted}).
				Build()
			cqCache := cache.New(cl)
			for _, flv := range flavors {
				cqCache.AddOrUpdateResourceFlavor(flv)
			}
			for _, cq := range clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
				}
			}

			broadcaster := record.NewBroadcaster()
			scheme := runtime.NewScheme()
			recorder := broadcaster.NewRecorder(scheme, corev1.EventSource{Component: constants.AdmissionName})
			preemptor := New(cl, workload.Ordering{}, recorder, true)

			snapshot := cqCache.Snapshot()
			wlInfo := workload.NewInfo(tc.incoming)
			wlInfo.ClusterQueue = tc.targetCQ
			targets := preemptor.GetTargets(*wlInfo, singlePodSetAssignment(
				flavorassigner.ResourceAssignment{
					corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
						Name: "default", Mode: flavorassigner.Preempt,
					},
				},
			), &snapshot)
			gotTargets := sets.New(slices.Map(targets, func(w **workload.Info) string {
				return workload.Key((*w).Obj)
			})...)
			if diff := cmp.Diff(tc.wantPreempted, gotTargets, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Issued preemptions (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestCandidatesOrdering(t *testing.T) {
	now := time.Now()
	candidates := []*workload.Info{
		workload.NewInfo(utiltesting.MakeWorkload("high", "").
			ReserveQuota(utiltesting.MakeAdmission("self").Obj()).
			Priority(10).
			Obj()),
		workload.NewInfo(utiltesting.MakeWorkload("low", "").
			ReserveQuota(utiltesting.MakeAdmission("self").Obj()).
			Priority(-10).
			Obj()),
		workload.NewInfo(utiltesting.MakeWorkload("other", "").
			ReserveQuota(utiltesting.MakeAdmission("other").Obj()).
			Priority(10).
			Obj()),
		workload.NewInfo(utiltesting.MakeWorkload("evicted", "").
			SetOrReplaceCondition(metav1.Condition{
				Type:   kueue.WorkloadEvicted,
				Status: metav1.ConditionTrue,
			}).
			Obj()),
		workload.NewInfo(utiltesting.MakeWorkload("old-a", "").
			UID("old-a").
			ReserveQuotaAt(utiltesting.MakeAdmission("self").Obj(), now).
			Obj()),
		workload.NewInfo(utiltesting.MakeWorkload("old-b", "").
			UID("old-b").
			ReserveQuotaAt(utiltesting.MakeAdmission("self").Obj(), now).
			Obj()),
		workload.NewInfo(utiltesting.MakeWorkload("current", "").
			ReserveQuota(utiltesting.MakeAdmission("self").Obj()).
			SetOrReplaceCondition(metav1.Condition{
				Type:               kueue.WorkloadQuotaReserved,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.NewTime(now.Add(time.Second)),
			}).
			Obj()),
	}
	sort.Slice(candidates, candidatesOrdering(candidates, "self", now))
	gotNames := make([]string, len(candidates))
	for i, c := range candidates {
		gotNames[i] = workload.Key(c.Obj)
	}
	wantCandidates := []string{"/evicted", "/other", "/low", "/current", "/old-a", "/old-b", "/high"}
	if diff := cmp.Diff(wantCandidates, gotNames); diff != "" {
		t.Errorf("Sorted with wrong order (-want,+got):\n%s", diff)
	}
}

func singlePodSetAssignment(assignments flavorassigner.ResourceAssignment) flavorassigner.Assignment {
	return flavorassigner.Assignment{
		PodSets: []flavorassigner.PodSetAssignment{{
			Name:    kueue.DefaultPodSetName,
			Flavors: assignments,
		}},
	}
}
