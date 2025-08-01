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
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"k8s.io/component-base/featuregate"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/hierarchy"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

var snapCmpOpts = cmp.Options{
	// ignore zero values during comparison, as we consider
	// zero FlavorResource usage to be same as no map entry.
	cmpopts.IgnoreMapEntries(func(_ resources.FlavorResource, v int64) bool { return v == 0 }),
	cmp.AllowUnexported(hierarchy.Manager[*cache.ClusterQueueSnapshot, *cache.CohortSnapshot]{}),
	cmpopts.IgnoreFields(hierarchy.Manager[*cache.ClusterQueueSnapshot, *cache.CohortSnapshot]{}, "cohortFactory"),
	cmpopts.IgnoreFields(cache.CohortSnapshot{}, "Cohort"),
	cmp.AllowUnexported(cache.ClusterQueueSnapshot{}),
	cmpopts.IgnoreFields(cache.ClusterQueueSnapshot{}, "ClusterQueue"),
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
						utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "2000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "2000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "2000m").Obj(),
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
						utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "2000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "2000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "2000m").Obj(),
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
						utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "3000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "3000m").Obj(),
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
						utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "3000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "3000m").Obj(),
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
						utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "1000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "1000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "3000m").Obj(),
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
						utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "1000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("mid", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "2000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceCPU, "default", "3000m").Obj(),
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
						utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceMemory, "alpha", "2Gi").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("mid", "").
					Request(corev1.ResourceMemory, "1Gi").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceMemory, "beta", "1Gi").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("high", "").
					Priority(1).
					Request(corev1.ResourceMemory, "1Gi").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceMemory, "beta", "1Gi").Obj(),
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
						utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceCPU, "default", "3000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-mid", "").
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "3000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "6").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "6000m").Obj(),
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
						utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceCPU, "default", "4000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "4000m").Obj(),
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
						utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceCPU, "default", "4000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "4000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-low-2", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "4000m").Obj(),
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
						utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceCPU, "default", "4000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "4000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-high-2", "").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "4000m").Obj(),
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
						utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceCPU, "default", "4000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceCPU, "default", "4000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "4000m").Obj(),
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
						utiltesting.MakeAdmission("d1").Assignment(corev1.ResourceCPU, "default", "4").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("d1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("d1").Assignment(corev1.ResourceCPU, "default", "4").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("d2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("d2").Assignment(corev1.ResourceCPU, "default", "4").Obj(),
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
						utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceCPU, "default", "4000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-low-1", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "5").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "5000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-low-2", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "1000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-low-3", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "1000m").Obj(),
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
						utiltesting.MakeAdmission("l1").Assignment(corev1.ResourceCPU, "default", "4000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("l1-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("l1").Assignment(corev1.ResourceCPU, "default", "2000m").Obj(),
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
						utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceCPU, "default", "2000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-1", "").
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "4000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-2", "").
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "4000m").Obj(),
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
						utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceCPU, "default", "4000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c1-2", "").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceCPU, "default", "4000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "2000m").Obj(),
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
						utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceCPU, "default", "3000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c1-mid", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceCPU, "default", "2000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "3000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("c2-mid", "").
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "4000m").Obj(),
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
						utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "3000m").Obj(),
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
						utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceMemory, "alpha", "2Gi").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("low-beta", "").
					Priority(-1).
					Request(corev1.ResourceMemory, "2Gi").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("standalone").Assignment(corev1.ResourceMemory, "beta", "2Gi").Obj(),
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
						utiltesting.MakeAdmission("preventStarvation").Assignment(corev1.ResourceCPU, "default", "2").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("wl2", "").
					Priority(1).
					Creation(now).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("preventStarvation").Assignment(corev1.ResourceCPU, "default", "2").Obj(),
						now.Add(time.Second),
					).
					Obj(),
				*utiltesting.MakeWorkload("wl3", "").
					Priority(1).
					Creation(now).
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("preventStarvation").Assignment(corev1.ResourceCPU, "default", "2").Obj(),
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
						utiltesting.MakeAdmission("a_best_effort").Assignment(corev1.ResourceCPU, "default", "10").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b_best_effort_low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b_best_effort").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
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
						utiltesting.MakeAdmission("b_standard").Assignment(corev1.ResourceCPU, "default", "10000m").Obj(),
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
						utiltesting.MakeAdmission("b_standard").Assignment(corev1.ResourceCPU, "default", "13000m").Obj(),
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
						utiltesting.MakeAdmission("a_standard").Assignment(corev1.ResourceCPU, "default", "13000m").Obj(),
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
						utiltesting.MakeAdmission("a_standard").Assignment(corev1.ResourceCPU, "default", "10").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("a_standard_2", "").
					Priority(1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("a_standard").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b_standard_1", "").
					Priority(1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b_standard").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b_standard_2", "").
					Priority(2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b_standard").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
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
						utiltesting.MakeAdmission("b_standard").Assignment(corev1.ResourceCPU, "default", "10").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b_standard_mid", "").
					Priority(1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b_standard").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("a_best_effort_low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("a_best_effort").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("a_best_effort_lower", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("a_best_effort").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
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
						utiltesting.MakeAdmission("lend1").Assignment(corev1.ResourceCPU, "default", "3000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("lend2-mid", "").
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("lend2").Assignment(corev1.ResourceCPU, "default", "3000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("lend2-high", "").
					Priority(1).
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("lend2").Assignment(corev1.ResourceCPU, "default", "4000m").Obj(),
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
						utiltesting.MakeAdmission("lend1").Assignment(corev1.ResourceCPU, "default", "3000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("lend1-mid", "").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("lend1").Assignment(corev1.ResourceCPU, "default", "2000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("lend2-low", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "3").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("lend2").Assignment(corev1.ResourceCPU, "default", "3000m").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("lend2-mid", "").
					Request(corev1.ResourceCPU, "4").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("lend2").Assignment(corev1.ResourceCPU, "default", "4000m").Obj(),
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
						utiltesting.MakeAdmission("lend2").Assignment(corev1.ResourceCPU, "default", "10").Obj(),
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
						utiltesting.MakeAdmission("a").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("a2", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("a").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("a3", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("a").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b1", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b3", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
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
						utiltesting.MakeAdmission("a").Assignment(corev1.ResourceCPU, "default", "1").Assignment(corev1.ResourceMemory, "default", "1").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("a2", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("a").Assignment(corev1.ResourceCPU, "default", "1").Assignment(corev1.ResourceMemory, "default", "1").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("a3", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("a").Assignment(corev1.ResourceCPU, "default", "1").Assignment(corev1.ResourceMemory, "default", "1").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b1", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Assignment(corev1.ResourceMemory, "default", "1").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Assignment(corev1.ResourceMemory, "default", "1").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b3", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Assignment(corev1.ResourceMemory, "default", "1").Obj(),
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
						utiltesting.MakeAdmission("a").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("a2", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("a").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("a3", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("a").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b1", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b3", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
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
						utiltesting.MakeAdmission("a").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b1", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b3", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b4", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
						now,
					).
					Obj(),
				*utiltesting.MakeWorkload("b5", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(
						utiltesting.MakeAdmission("b").Assignment(corev1.ResourceCPU, "default", "1").Obj(),
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
						utiltesting.MakeAdmission("cq-right").Assignment(corev1.ResourceCPU, "default", "5").Obj(),
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

			cqCache := cache.New(cl)
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
			preemptor := New(cl, workload.Ordering{}, recorder, config.FairSharing{}, clocktesting.NewFakeClock(now))
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

func TestFairPreemptions(t *testing.T) {
	now := time.Now()
	flavors := []*kueue.ResourceFlavor{
		utiltesting.MakeResourceFlavor("default").Obj(),
	}
	baseCQs := []*kueue.ClusterQueue{
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
		clusterQueues []*kueue.ClusterQueue
		cohorts       []*kueue.Cohort
		strategies    []config.PreemptionStrategy
		admitted      []kueue.Workload
		incoming      *kueue.Workload
		targetCQ      kueue.ClusterQueueReference
		wantPreempted sets.Set[string]
	}{
		"reclaim nominal from user using the most": {
			clusterQueues: baseCQs,
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
			wantPreempted: sets.New(targetKeyReason("/b1", kueue.InCohortFairSharingReason)),
		},
		"can reclaim from queue using less, if taking the latest workload from user using the most isn't enough": {
			clusterQueues: baseCQs,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltesting.MakeWorkload("a2", "").Request(corev1.ResourceCPU, "1").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltesting.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("b2", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming:      utiltesting.MakeWorkload("c_incoming", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("a", "default", now).Obj(),
			targetCQ:      "c",
			wantPreempted: sets.New(targetKeyReason("/a1", kueue.InCohortFairSharingReason)), // attempts to preempt b1, but it's not enough.
		},
		"reclaim borrowable quota from user using the most": {
			clusterQueues: baseCQs,
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
			wantPreempted: sets.New(targetKeyReason("/b1", kueue.InCohortFairSharingReason)),
		},
		"preempt one from each CQ borrowing": {
			clusterQueues: baseCQs,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "").Request(corev1.ResourceCPU, "0.5").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltesting.MakeWorkload("a2", "").Request(corev1.ResourceCPU, "0.5").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltesting.MakeWorkload("a3", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltesting.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "0.5").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("b2", "").Request(corev1.ResourceCPU, "0.5").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("b3", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming: utiltesting.MakeWorkload("c_incoming", "").Request(corev1.ResourceCPU, "2").Obj(),
			targetCQ: "c",
			wantPreempted: sets.New(
				targetKeyReason("/a1", kueue.InCohortFairSharingReason),
				targetKeyReason("/b1", kueue.InCohortFairSharingReason),
			),
		},
		"can't preempt when everyone under nominal": {
			clusterQueues: baseCQs,
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
			clusterQueues: baseCQs,
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
			clusterQueues: baseCQs,
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
			incoming: utiltesting.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "2").Obj(),
			targetCQ: "a",
			wantPreempted: sets.New(
				targetKeyReason("/a1_low", kueue.InClusterQueueReason),
				targetKeyReason("/a2_low", kueue.InClusterQueueReason),
			),
		},
		"can preempt a combination of same CQ and highest user": {
			clusterQueues: baseCQs,
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
			incoming: utiltesting.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "2").Obj(),
			targetCQ: "a",
			wantPreempted: sets.New(
				targetKeyReason("/a_low", kueue.InClusterQueueReason),
				targetKeyReason("/b1", kueue.InCohortFairSharingReason),
			),
		},
		"preempt huge workload if there is no other option, as long as the target CQ gets a lower share": {
			clusterQueues: baseCQs,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "9").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming:      utiltesting.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "2").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New(targetKeyReason("/b1", kueue.InCohortFairSharingReason)),
		},
		"can't preempt huge workload if the incoming is also huge": {
			clusterQueues: baseCQs,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltesting.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "7").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming: utiltesting.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "5").Obj(),
			targetCQ: "a",
		},
		"can't preempt 2 smaller workloads if the incoming is huge": {
			clusterQueues: baseCQs,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("b2", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("b3", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming: utiltesting.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "6").Obj(),
			targetCQ: "a",
		},
		"preempt from target and others even if over nominal": {
			clusterQueues: baseCQs,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("a1_low", "").Priority(-1).Request(corev1.ResourceCPU, "2").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltesting.MakeWorkload("a2_low", "").Priority(-1).Request(corev1.ResourceCPU, "1").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltesting.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("b2", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming: utiltesting.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "4").Obj(),
			targetCQ: "a",
			wantPreempted: sets.New(
				targetKeyReason("/a1_low", kueue.InClusterQueueReason),
				targetKeyReason("/b1", kueue.InCohortFairSharingReason),
			),
		},
		"prefer to preempt workloads that don't make the target CQ have the biggest share": {
			clusterQueues: baseCQs,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("b2", "").Request(corev1.ResourceCPU, "1").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("b3", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("c1", "").Request(corev1.ResourceCPU, "1").SimpleReserveQuota("c", "default", now).Obj(),
			},
			incoming: utiltesting.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "3.5").Obj(),
			targetCQ: "a",
			// It would have been possible to preempt "/b1" under rule S2-b, but S2-a was possible first.
			wantPreempted: sets.New(targetKeyReason("/b2", kueue.InCohortFairSharingReason)),
		},
		"preempt from different cluster queues if the end result has a smaller max share": {
			clusterQueues: baseCQs,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("b2", "").Request(corev1.ResourceCPU, "2.5").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("c1", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("c", "default", now).Obj(),
				*utiltesting.MakeWorkload("c2", "").Request(corev1.ResourceCPU, "2.5").SimpleReserveQuota("c", "default", now).Obj(),
			},
			incoming: utiltesting.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "3.5").Obj(),
			targetCQ: "a",
			wantPreempted: sets.New(
				targetKeyReason("/b1", kueue.InCohortFairSharingReason),
				targetKeyReason("/c1", kueue.InCohortFairSharingReason),
			),
		},
		"scenario above does not flap": {
			clusterQueues: baseCQs,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "").Request(corev1.ResourceCPU, "3.5").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltesting.MakeWorkload("b2", "").Request(corev1.ResourceCPU, "2.5").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("c2", "").Request(corev1.ResourceCPU, "2.5").SimpleReserveQuota("c", "default", now).Obj(),
			},
			incoming: utiltesting.MakeWorkload("b_incoming", "").Request(corev1.ResourceCPU, "2").Obj(),
			targetCQ: "b",
		},
		"cannot preempt if it would make the candidate CQ go under nominal after preempting one element": {
			clusterQueues: baseCQs,
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("b2", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("c1", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("c", "default", now).Obj(),
			},
			incoming: utiltesting.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "4").Obj(),
			targetCQ: "a",
		},
		// preemption.borrowWithinCohort does not affect how
		// we handle Fair Sharing preemptions. Lower priority
		// workloads are not preempted unless
		// DominantResourceShare value indicates that they
		// should be preempted.
		"workloads under priority threshold not capriciously preempted": {
			clusterQueues: baseCQs,
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
			wantPreempted: nil,
		},
		"preempt lower priority first, even if big": {
			clusterQueues: baseCQs,
			strategies:    []config.PreemptionStrategy{config.LessThanInitialShare},
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltesting.MakeWorkload("b_low", "").Priority(0).Request(corev1.ResourceCPU, "5").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("b_high", "").Priority(1).Request(corev1.ResourceCPU, "1").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming:      utiltesting.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "1").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New(targetKeyReason("/b_low", kueue.InCohortFairSharingReason)),
		},
		"preempt workload that doesn't transfer the imbalance, even if high priority": {
			clusterQueues: baseCQs,
			strategies:    []config.PreemptionStrategy{config.LessThanOrEqualToFinalShare},
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("a1", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltesting.MakeWorkload("b_low", "").Priority(0).Request(corev1.ResourceCPU, "5").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltesting.MakeWorkload("b_high", "").Priority(1).Request(corev1.ResourceCPU, "1").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming:      utiltesting.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "1").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New(targetKeyReason("/b_high", kueue.InCohortFairSharingReason)),
		},
		"CQ with higher weight can preempt more": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("a").
					Cohort("all").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					FairWeight(resource.MustParse("2")).
					Obj(),
				utiltesting.MakeClusterQueue("b").
					Cohort("all").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltesting.MakeClusterQueue("c").
					Cohort("all").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a3").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b2").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b3").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b4").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b5").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b6").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming: utiltesting.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "2").Obj(),
			targetCQ: "a",
			wantPreempted: sets.New(
				targetKeyReason("/b1", kueue.InCohortFairSharingReason),
				targetKeyReason("/b2", kueue.InCohortFairSharingReason),
			),
		},
		"can preempt anything borrowing from CQ with 0 weight": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("a").
					Cohort("all").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltesting.MakeClusterQueue("b").
					Cohort("all").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					FairWeight(resource.MustParse("0")).
					Obj(),
				utiltesting.MakeClusterQueue("c").
					Cohort("all").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a3").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b2").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b3").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b4").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b5").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b6").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming: utiltesting.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "3").Obj(),
			targetCQ: "a",
			wantPreempted: sets.New(
				targetKeyReason("/b1", kueue.InCohortFairSharingReason),
				targetKeyReason("/b2", kueue.InCohortFairSharingReason),
				targetKeyReason("/b3", kueue.InCohortFairSharingReason),
			),
		},
		"can't preempt nominal from CQ with 0 weight": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("a").
					Cohort("all").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltesting.MakeClusterQueue("b").
					Cohort("all").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					FairWeight(resource.MustParse("0")).
					Obj(),
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a3").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b2").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b3").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming: unitWl.Clone().Name("a_incoming").Obj(),
			targetCQ: "a",
		},
		"can't preempt nominal from Cohort with 0 weight": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("left-cq").
					Cohort("root").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "0").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltesting.MakeClusterQueue("right-cq").
					Cohort("right-cohort").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "0").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					FairWeight(resource.MustParse("0")).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltesting.MakeCohort("right-cohort").
					FairWeight(resource.MustParse("0")).
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Parent("root").Obj(),
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("right-1").SimpleReserveQuota("right-cq", "default", now).Obj(),
			},
			incoming:      unitWl.Clone().Name("left-1").Obj(),
			wantPreempted: sets.New[string](),
			targetCQ:      "left-cq",
		},
		"can preempt within cluster queue when no cohort": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("a").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					Obj(),
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
			},
			incoming: unitWl.Clone().Name("a_incoming").Priority(1000).Obj(),
			targetCQ: "a",
			wantPreempted: sets.New(
				targetKeyReason("/a1", kueue.InClusterQueueReason),
			),
		},
		// Each Cohort provides 5 capacity, while each CQ
		// provides 1 capacity.
		//
		// Here is a representation of the tree the 3 tuple
		// is: (Usage,Quota,SubtreeQuota)
		//
		//                ROOT(20,5,20)
		//             /       |         \
		//       LEFT(5,5,7)  c(5,1,1)   RIGHT(10,5,7)
		//       /       \               /      \
		//     a(0,1,1)    b(5,1,1)   d(5,1,1)    e(5,1,1)
		//
		// We show how ClusterQueue a is able to preempt
		// workloads in all of these ClusterQueues.  We set
		// FairWeight of a, and LEFT, to 2.0, to make this
		// possible.  We set FairWeight of e to 0.99, to make
		// preemptions deterministic
		"hierarchical preemption": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("a").
					Cohort("LEFT").
					FairWeight(resource.MustParse("2")).
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltesting.MakeClusterQueue("b").
					Cohort("LEFT").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
				utiltesting.MakeClusterQueue("c").
					Cohort("ROOT").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
				utiltesting.MakeClusterQueue("d").
					Cohort("RIGHT").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
				utiltesting.MakeClusterQueue("e").
					Cohort("RIGHT").
					// for determinism, we slightly prefer preemptions from e
					// compared to d.
					FairWeight(resource.MustParse("0.99")).
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltesting.MakeCohort("ROOT").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "5").Obj()).
					Obj(),
				utiltesting.MakeCohort("LEFT").
					FairWeight(resource.MustParse("2")).
					Parent("ROOT").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "5").Obj()).
					Obj(),
				utiltesting.MakeCohort("RIGHT").
					Parent("ROOT").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "5").Obj()).
					Obj(),
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("b1").Priority(1).SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b2").Priority(2).SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b3").Priority(3).SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b4").Priority(4).SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b5").Priority(5).SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("c1").Priority(1).SimpleReserveQuota("c", "default", now).Obj(),
				*unitWl.Clone().Name("c2").Priority(2).SimpleReserveQuota("c", "default", now).Obj(),
				*unitWl.Clone().Name("c3").Priority(3).SimpleReserveQuota("c", "default", now).Obj(),
				*unitWl.Clone().Name("c4").Priority(4).SimpleReserveQuota("c", "default", now).Obj(),
				*unitWl.Clone().Name("c5").Priority(5).SimpleReserveQuota("c", "default", now).Obj(),
				*unitWl.Clone().Name("d1").Priority(1).SimpleReserveQuota("d", "default", now).Obj(),
				*unitWl.Clone().Name("d2").Priority(2).SimpleReserveQuota("d", "default", now).Obj(),
				*unitWl.Clone().Name("d3").Priority(3).SimpleReserveQuota("d", "default", now).Obj(),
				*unitWl.Clone().Name("d4").Priority(4).SimpleReserveQuota("d", "default", now).Obj(),
				*unitWl.Clone().Name("d5").Priority(5).SimpleReserveQuota("d", "default", now).Obj(),
				*unitWl.Clone().Name("e1").Priority(1).SimpleReserveQuota("e", "default", now).Obj(),
				*unitWl.Clone().Name("e2").Priority(2).SimpleReserveQuota("e", "default", now).Obj(),
				*unitWl.Clone().Name("e3").Priority(3).SimpleReserveQuota("e", "default", now).Obj(),
				*unitWl.Clone().Name("e4").Priority(4).SimpleReserveQuota("e", "default", now).Obj(),
				*unitWl.Clone().Name("e5").Priority(5).SimpleReserveQuota("e", "default", now).Obj(),
			},
			incoming: utiltesting.MakeWorkload("unit", "").Request(corev1.ResourceCPU, "5").Obj(),
			targetCQ: "a",
			wantPreempted: sets.New(
				targetKeyReason("/b1", kueue.InCohortFairSharingReason),
				targetKeyReason("/b2", kueue.InCohortFairSharingReason),
				targetKeyReason("/c1", kueue.InCohortFairSharingReason),
				targetKeyReason("/c2", kueue.InCohortFairSharingReason),
				targetKeyReason("/e1", kueue.InCohortFairSharingReason)),
		},
		// though ClusterQueue b is borrowing, its Cohort is not,
		// and therefore it cannot be preempted.
		//             ROOT
		//           /      \
		//       a(3,5,5)   RIGHT(1,1,1)
		//                        \
		//                         b(1,0,0)
		//
		// incoming workload to a, of size 5, must
		// preempt its own workloads, despite its fair weight
		// being high, and RIGHT/b having a low weight.
		"borrowing cq in non-borrowing cohort is protected": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("a").
					Cohort("ROOT").
					FairWeight(resource.MustParse("10")).
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "5").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					}).
					Obj(),
				utiltesting.MakeClusterQueue("b").
					Cohort("RIGHT").
					FairWeight(resource.MustParse("0.1")).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltesting.MakeCohort("ROOT").Obj(),
				utiltesting.MakeCohort("RIGHT").Parent("ROOT").
					FairWeight(resource.MustParse("0.1")).
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").Priority(-1).SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").Priority(-1).SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a3").Priority(-1).SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").Priority(-1).SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming: utiltesting.MakeWorkload("unit", "").Request(corev1.ResourceCPU, "5").Obj(),
			targetCQ: "a",
			wantPreempted: sets.New(
				targetKeyReason("/a1", kueue.InClusterQueueReason),
				targetKeyReason("/a2", kueue.InClusterQueueReason),
				targetKeyReason("/a3", kueue.InClusterQueueReason),
			),
		},
		// Preempting the small workload would bring
		// RIGHT to a DRS of 0, and we can't even consider the
		// higher priority workload. incoming workload to a,
		// of size 4, must preempt its own workloads
		//             ROOT
		//           /      \
		//       a(3,5,5)   RIGHT(4,3,3)
		//                        \
		//                         b(4,0,0)
		//
		"forced to preempt within clusterqueue because borrowing workload too important": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("a").
					Cohort("ROOT").
					FairWeight(resource.MustParse("10")).
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "5").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					}).
					Obj(),
				utiltesting.MakeClusterQueue("b").
					Cohort("RIGHT").
					FairWeight(resource.MustParse("0.1")).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltesting.MakeCohort("ROOT").Obj(),
				utiltesting.MakeCohort("RIGHT").Parent("ROOT").
					FairWeight(resource.MustParse("0.1")).
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "3").Obj()).
					Obj(),
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").Priority(-1).SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").Priority(-1).SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a3").Priority(-1).SimpleReserveQuota("a", "default", now).Obj(),
				*utiltesting.MakeWorkload("b1", "").Priority(100).Request(corev1.ResourceCPU, "4").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming: utiltesting.MakeWorkload("unit", "").Request(corev1.ResourceCPU, "4").Obj(),
			targetCQ: "a",
			wantPreempted: sets.New(
				targetKeyReason("/a1", kueue.InClusterQueueReason),
				targetKeyReason("/a2", kueue.InClusterQueueReason),
				targetKeyReason("/a3", kueue.InClusterQueueReason),
			),
		},
		//                      ROOT
		//                    /   |   \
		//                   A    B    C
		//                   |    |    |
		//                   AA   BB   CC
		//                   |    |    |
		//                  AAA  BBB  CCC
		//                   |    |    |
		//                   a    b   CCCC
		// cq-a wants capacity, cq-b uses capacity, and
		// Cohort-CCCC provides capacity
		//
		// Organization A is more important than organization
		// B, indicated by relative FairSharing value, so the
		// preemption is possible.
		"deep preemption": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("a").
					Cohort("AAA").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "0").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltesting.MakeClusterQueue("b").
					Cohort("BBB").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "0").Obj()).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltesting.MakeCohort("ROOT").Obj(),
				utiltesting.MakeCohort("A").Parent("ROOT").
					// we are comparing
					// almostLCAs=(A,B). In order
					// for the preemption to
					// occur, we indicate that A
					// is more important than B.
					FairWeight(resource.MustParse("1.01")).
					Obj(),
				utiltesting.MakeCohort("AA").Parent("A").
					Obj(),
				utiltesting.MakeCohort("AAA").Parent("AA").
					Obj(),
				utiltesting.MakeCohort("B").Parent("ROOT").
					FairWeight(resource.MustParse("0.99")).
					Obj(),
				utiltesting.MakeCohort("BB").Parent("B").
					Obj(),
				utiltesting.MakeCohort("BBB").Parent("BB").
					Obj(),
				utiltesting.MakeCohort("C").Parent("ROOT").
					Obj(),
				utiltesting.MakeCohort("CC").Parent("C").
					Obj(),
				utiltesting.MakeCohort("CCC").Parent("CC").
					Obj(),
				utiltesting.MakeCohort("CCCC").Parent("CCC").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			admitted: []kueue.Workload{
				unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Workload,
			},
			incoming: unitWl.Clone().Name("a1").Obj(),
			targetCQ: "a",
			wantPreempted: sets.New(
				targetKeyReason("/b1", kueue.InCohortFairSharingReason),
			),
		},
		"cq with zero weight can reclaim nominal quota": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("a").
					Cohort("ROOT").
					FairWeight(resource.MustParse("0.0")).
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltesting.MakeClusterQueue("b").
					Cohort("ROOT").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "0").Obj()).
					FairWeight(resource.MustParse("1.0")).
					Obj(),
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming: unitWl.Clone().Name("a1").Obj(),
			targetCQ: "a",
			wantPreempted: sets.New(
				targetKeyReason("/b1", kueue.InCohortFairSharingReason),
			),
		},
		"cohort with zero weight can reclaim nominal quota": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("a").
					Cohort("A").
					FairWeight(resource.MustParse("0.0")).
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "0").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltesting.MakeClusterQueue("b").
					Cohort("ROOT").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "0").Obj()).
					FairWeight(resource.MustParse("1.0")).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltesting.MakeCohort("A").
					Parent("ROOT").
					FairWeight(resource.MustParse("0.0")).
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).Obj(),
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming: unitWl.Clone().Name("a1").Obj(),
			targetCQ: "a",
			wantPreempted: sets.New(
				targetKeyReason("/b1", kueue.InCohortFairSharingReason),
			),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			// Set name as UID so that candidates sorting is predictable.
			for i := range tc.admitted {
				tc.admitted[i].UID = types.UID(tc.admitted[i].Name)
			}
			cl := utiltesting.NewClientBuilder().
				WithLists(&kueue.WorkloadList{Items: tc.admitted}).
				Build()
			cqCache := cache.New(cl)
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
			recorder := broadcaster.NewRecorder(scheme, corev1.EventSource{Component: constants.AdmissionName})
			preemptor := New(cl, workload.Ordering{}, recorder, config.FairSharing{
				Enable:               true,
				PreemptionStrategies: tc.strategies,
			}, clocktesting.NewFakeClock(now))

			beforeSnapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			snapshotWorkingCopy, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			wlInfo := workload.NewInfo(tc.incoming)
			wlInfo.ClusterQueue = tc.targetCQ
			targets := preemptor.GetTargets(log, *wlInfo, singlePodSetAssignment(
				flavorassigner.ResourceAssignment{
					corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
						Name: "default", Mode: flavorassigner.Preempt,
					},
				},
			), snapshotWorkingCopy)
			gotTargets := sets.New(slices.Map(targets, func(t **Target) string {
				return targetKeyReason(workload.Key((*t).WorkloadInfo.Obj), (*t).Reason)
			})...)
			if diff := cmp.Diff(tc.wantPreempted, gotTargets, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Issued preemptions (-want,+got):\n%s", diff)
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
		candidates         []workload.Info
		wantCandidates     []workload.Reference
		enableFeatureGates []featuregate.Feature
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
			wantCandidates:     []workload.Reference{"mid_lq_usage", "low_lq_usage"},
			enableFeatureGates: []featuregate.Feature{features.AdmissionFairSharing},
		},
		"workloads from different CQ are sorted based on priority and timestamp": {
			candidates: []workload.Info{
				*wlMidUsageLq,
				*wlHighUsageLqDifCQ,
			},
			wantCandidates:     []workload.Reference{"high_lq_usage_different_cq", "mid_lq_usage"},
			enableFeatureGates: []featuregate.Feature{features.AdmissionFairSharing},
		}}

	_, log := utiltesting.ContextWithLog(t)
	for _, tc := range cases {
		for _, gate := range tc.enableFeatureGates {
			features.SetFeatureGateDuringTest(t, gate, true)
		}
		sort.Slice(tc.candidates, func(i int, j int) bool {
			return CandidatesOrdering(log, &tc.candidates[i], &tc.candidates[j], kueue.ClusterQueueReference(preemptorCq), now)
		})
		got := slices.Map(tc.candidates, func(c *workload.Info) workload.Reference {
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

func TestHierarchicalPreemptions(t *testing.T) {
	now := time.Now()
	flavors := []*kueue.ResourceFlavor{
		utiltesting.MakeResourceFlavor("default").Obj(),
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
				utiltesting.MakeCohort("r").Obj(),
				utiltesting.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltesting.MakeClusterQueue("q_borrowing").
					Cohort("r").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("admitted2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("q_borrowing").
						Assignment(corev1.ResourceCPU, "default", "2").Obj()).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("incoming", "").
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
				utiltesting.MakeCohort("r").Obj(),
				utiltesting.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("q_nominal").
					Cohort("r").ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "2").
					Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltesting.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltesting.MakeClusterQueue("q_borrowing").
					Cohort("r").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("admitted1", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("q_nominal").
						Assignment(corev1.ResourceCPU, "default", "2").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("admitted2", "").
					Priority(0).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("q_borrowing").
						Assignment(corev1.ResourceCPU, "default", "2").Obj()).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("incoming", "").
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
				utiltesting.MakeCohort("r").Obj(),
				utiltesting.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltesting.MakeClusterQueue("q_borrowing").
					Cohort("r").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("admitted1", "").
					Priority(1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("q_borrowing").
						Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("admitted2", "").
					Priority(2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("q_borrowing").
						Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("incoming", "").
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
				utiltesting.MakeCohort("r").Obj(),
				utiltesting.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
							MaxPriorityThreshold: ptr.To[int32](0),
						},
					}).Obj(),
				utiltesting.MakeClusterQueue("q_same_cohort").
					Cohort("c").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("admitted_not_preemptible", "").
					Priority(1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("q_same_cohort").
						Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("admitted_preemptible", "").
					Priority(0).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("q_same_cohort").
						Assignment(corev1.ResourceCPU, "default", "2").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("admitted_own_queue", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("q").
						Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("incoming", "").
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
				utiltesting.MakeCohort("r").Obj(),
				utiltesting.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltesting.MakeClusterQueue("q_borrowing").
					Cohort("r").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("admitted_borrowing", "").
					Priority(1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("q_borrowing").
						Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("admitted_same_queue", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("q").
						Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("incoming", "").
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
				utiltesting.MakeCohort("r").Obj(),
				utiltesting.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
							MaxPriorityThreshold: ptr.To[int32](0),
						},
					}).Obj(),
				utiltesting.MakeClusterQueue("q_nominal").
					Cohort("r").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").
						Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltesting.MakeClusterQueue("q_same_cohort").
					Cohort("c").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("admitted_nominal", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("q_nominal").
						Assignment(corev1.ResourceCPU, "default", "2").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("admitted_same_cohort", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("q_same_cohort").
						Assignment(corev1.ResourceCPU, "default", "2").Obj()).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("incoming", "").
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
				utiltesting.MakeCohort("r").Obj(),
				utiltesting.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "4").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltesting.MakeClusterQueue("q_borrowing").
					Cohort("r").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltesting.MakeClusterQueue("q_same_cohort").
					Cohort("c").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("admitted_borrowing", "").
					Priority(10).
					Request(corev1.ResourceCPU, "3").
					ReserveQuota(utiltesting.MakeAdmission("q_borrowing").
						Assignment(corev1.ResourceCPU, "default", "3").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("admitted_same_cohort", "").
					Priority(10).
					Request(corev1.ResourceCPU, "3").
					ReserveQuota(utiltesting.MakeAdmission("q_same_cohort").
						Assignment(corev1.ResourceCPU, "default", "3").Obj()).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("incoming", "").
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
				utiltesting.MakeCohort("r").ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "1").
					Obj()).Obj(),
				utiltesting.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
							MaxPriorityThreshold: ptr.To[int32](0),
						},
					}).Obj(),
				utiltesting.MakeClusterQueue("q_borrowing").
					Cohort("r").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltesting.MakeClusterQueue("q_same_cohort").
					Cohort("c").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("admitted_borrowing", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("q_borrowing").
						Assignment(corev1.ResourceCPU, "default", "2").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("admitted_same_cohort_preemptible", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("q_same_cohort").
						Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("admitted_borrowing_not_preemptible", "").
					Priority(1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("q_borrowing").
						Assignment(corev1.ResourceCPU, "default", "2").Obj()).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("incoming", "").
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
				utiltesting.MakeCohort("r").ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "1").
					Obj()).Obj(),
				utiltesting.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
							MaxPriorityThreshold: ptr.To[int32](0),
						},
					}).Obj(),
				utiltesting.MakeClusterQueue("q_borrowing").
					Cohort("r").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltesting.MakeClusterQueue("q_same_cohort").
					Cohort("c").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("admitted_borrowing", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("q_borrowing").
						Assignment(corev1.ResourceCPU, "default", "2").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("admitted_same_queue_preemptible", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("q").
						Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("admitted_borrowing_not_preemptible", "").
					Priority(1).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("q_borrowing").
						Assignment(corev1.ResourceCPU, "default", "2").Obj()).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("incoming", "").
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
				utiltesting.MakeCohort("r").Obj(),
				utiltesting.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltesting.MakeClusterQueue("q_borrowing").
					Cohort("r").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltesting.MakeClusterQueue("q_nominal").
					Cohort("r").ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "2").
					Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("admitted_borrowing_prio_8", "").
					Priority(8).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("q_borrowing").
						Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("admitted_borrowing_prio_9", "").
					Priority(9).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("q_borrowing").
						Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("admitted_borrowing_prio_10", "").
					Priority(9).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("q_borrowing").
						Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("admitted_nominal", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("q_nominal").
						Assignment(corev1.ResourceCPU, "default", "2").Obj()).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("incoming", "").
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
				utiltesting.MakeCohort("r").Obj(),
				utiltesting.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").
						Obj()).Obj(),
				utiltesting.MakeCohort("c_other").
					Parent("r").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("q_other").
					Cohort("c_other").ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
					Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltesting.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltesting.MakeClusterQueue("q_same_cohort").
					Cohort("c").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("admitted_other_1", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("q_other").
						Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("admitted_other_2", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("q_other").
						Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("admitted_same_cohort", "").
					Priority(0).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("q_same_cohort").
						Assignment(corev1.ResourceCPU, "default", "2").Obj()).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("incoming", "").
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
				utiltesting.MakeCohort("r").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").
						Obj()).Obj(),
				utiltesting.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").
						Resource(corev1.ResourceMemory, "4Gi").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltesting.MakeClusterQueue("q_borrowing").
					Cohort("r").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltesting.MakeClusterQueue("q_same_cohort").
					Cohort("c").ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
					Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("admitted_borrowing", "").
					Priority(0).
					Request(corev1.ResourceCPU, "3").
					Request(corev1.ResourceMemory, "1Gi").
					ReserveQuota(utiltesting.MakeAdmission("q_borrowing").
						Assignment(corev1.ResourceCPU, "default", "3").
						Assignment(corev1.ResourceMemory, "default", "1Gi").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("admitted_same_cohort", "").
					Priority(-2).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "3Gi").
					ReserveQuota(utiltesting.MakeAdmission("q_same_cohort").
						Assignment(corev1.ResourceCPU, "default", "1").
						Assignment(corev1.ResourceMemory, "default", "3Gi").Obj()).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("incoming", "").
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
				utiltesting.MakeCohort("r").Obj(),
				utiltesting.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
							MaxPriorityThreshold: ptr.To[int32](0),
						},
					}).Obj(),
				utiltesting.MakeClusterQueue("q_borrowing").
					Cohort("r").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltesting.MakeClusterQueue("q_same_cohort").
					Cohort("c").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("admitted_borrowing", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("q_borrowing").
						Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("evicted_same_cohort", "").
					Priority(-1).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(utiltesting.MakeAdmission("q_same_cohort").
						Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.NewTime(now),
					}).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("incoming", "").
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
				utiltesting.MakeCohort("r").Obj(),
				utiltesting.MakeCohort("c").
					Parent("r").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("q").
					Cohort("c").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3", "", "2").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
				utiltesting.MakeClusterQueue("q_borrowing").
					Cohort("r").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("admitted_borrowing", "").
					Priority(0).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("q_borrowing").
						Assignment(corev1.ResourceCPU, "default", "4").Obj()).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("incoming", "").
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
				utiltesting.MakeCohort("r").Obj(),
				utiltesting.MakeCohort("c11").
					Parent("r").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").
						Obj()).Obj(),
				utiltesting.MakeCohort("c12").
					Parent("r").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").
						Obj()).Obj(),
				utiltesting.MakeCohort("c21").
					Parent("c11").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").
						Obj()).Obj(),
				utiltesting.MakeCohort("c22").
					Parent("c11").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").
						Obj()).Obj(),
				utiltesting.MakeCohort("c23").
					Parent("c11").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").
						Obj()).Obj(),
				utiltesting.MakeCohort("c31").
					Parent("c21").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").
						Obj()).Obj(),
				utiltesting.MakeCohort("c32").
					Parent("c21").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").
						Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("q1").
					Cohort("c12").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltesting.MakeClusterQueue("q2").
					Cohort("c23").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltesting.MakeClusterQueue("q3").
					Cohort("c22").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltesting.MakeClusterQueue("q4").
					Cohort("c32").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltesting.MakeClusterQueue("q5").
					Cohort("c31").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
			},
			admitted: []kueue.Workload{
				*utiltesting.MakeWorkload("admitted_borrowing_1", "").
					Priority(-6).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("q1").
						Assignment(corev1.ResourceCPU, "default", "4").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("admitted_borrowing_2", "").
					Priority(-5).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("q1").
						Assignment(corev1.ResourceCPU, "default", "4").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("admitted_borrowing_3", "").
					Priority(-9).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("q2").
						Assignment(corev1.ResourceCPU, "default", "4").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("admitted_borrowing_4", "").
					Priority(-10).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("q2").
						Assignment(corev1.ResourceCPU, "default", "4").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("admitted_borrowing_5", "").
					Priority(-4).
					Request(corev1.ResourceCPU, "4").
					ReserveQuota(utiltesting.MakeAdmission("q3").
						Assignment(corev1.ResourceCPU, "default", "4").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("admitted_borrowing_6", "").
					Priority(-3).
					Request(corev1.ResourceCPU, "3").
					ReserveQuota(utiltesting.MakeAdmission("q3").
						Assignment(corev1.ResourceCPU, "default", "3").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("admitted_borrowing_7", "").
					Priority(4).
					Request(corev1.ResourceCPU, "2").
					ReserveQuota(utiltesting.MakeAdmission("q4").
						Assignment(corev1.ResourceCPU, "default", "2").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("admitted_borrowing_8", "").
					Priority(2).
					Request(corev1.ResourceCPU, "3").
					ReserveQuota(utiltesting.MakeAdmission("q4").
						Assignment(corev1.ResourceCPU, "default", "3").Obj()).
					Obj(),
			},
			incoming: utiltesting.MakeWorkload("incoming", "").
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

			cqCache := cache.New(cl)
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
			preemptor := New(cl, workload.Ordering{}, recorder, config.FairSharing{}, clocktesting.NewFakeClock(now))
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
