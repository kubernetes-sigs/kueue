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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	schdcache "sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

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
			recorder := broadcaster.NewRecorder(scheme, corev1.EventSource{Component: constants.AdmissionName})
			preemptor := New(cl, workload.Ordering{}, recorder, config.FairSharing{
				Enable:               true,
				PreemptionStrategies: tc.strategies,
			}, false, clocktesting.NewFakeClock(now))

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
