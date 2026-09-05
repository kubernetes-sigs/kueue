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
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/component-base/featuregate"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestFairPreemptions(t *testing.T) {
	now := time.Now()
	flavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("default").Obj(),
	}
	baseCQs := []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("a").
			Cohort("all").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
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
		utiltestingapi.MakeClusterQueue("b").
			Cohort("all").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
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
		utiltestingapi.MakeClusterQueue("c").
			Cohort("all").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
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
		utiltestingapi.MakeClusterQueue("preemptible").
			Cohort("all").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "0").Obj()).
			Obj(),
	}
	unitWl := *utiltestingapi.MakeWorkload("unit", "").Request(corev1.ResourceCPU, "1")
	cases := map[string]struct {
		clusterQueues    []*kueue.ClusterQueue
		cohorts          []*kueue.Cohort
		flavors          []*kueue.ResourceFlavor
		assignmentFlavor kueue.ResourceFlavorReference
		strategies       []config.PreemptionStrategy
		admitted         []kueue.Workload
		incoming         *kueue.Workload
		targetCQ         kueue.ClusterQueueReference
		wantPreempted    sets.Set[string]
		featureGates     map[featuregate.Feature]bool
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
			wantPreempted: sets.New(targetKeyReason("/b1", kueue.InCohortReclamationReason)),
		},
		"can reclaim from queue using less, if taking the latest workload from user using the most isn't enough": {
			clusterQueues: baseCQs,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("a2", "").Request(corev1.ResourceCPU, "1").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b2", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming:      utiltestingapi.MakeWorkload("c_incoming", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("a", "default", now).Obj(),
			targetCQ:      "c",
			wantPreempted: sets.New(targetKeyReason("/a1", kueue.InCohortReclamationReason)), // attempts to preempt b1, but it's not enough.
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
				*utiltestingapi.MakeWorkload("a1", "").Request(corev1.ResourceCPU, "0.5").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("a2", "").Request(corev1.ResourceCPU, "0.5").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("a3", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "0.5").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b2", "").Request(corev1.ResourceCPU, "0.5").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b3", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("c_incoming", "").Request(corev1.ResourceCPU, "2").Obj(),
			targetCQ: "c",
			wantPreempted: sets.New(
				targetKeyReason("/a1", kueue.InCohortReclamationReason),
				targetKeyReason("/b1", kueue.InCohortReclamationReason),
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
			incoming: utiltestingapi.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "2").Obj(),
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
			incoming: utiltestingapi.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "2").Obj(),
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
			incoming: utiltestingapi.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "2").Obj(),
			targetCQ: "a",
			wantPreempted: sets.New(
				targetKeyReason("/a_low", kueue.InClusterQueueReason),
				targetKeyReason("/b1", kueue.InCohortFairSharingReason),
			),
		},
		"preempt huge workload if there is no other option, as long as the target CQ gets a lower share": {
			clusterQueues: baseCQs,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "9").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming:      utiltestingapi.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "2").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New(targetKeyReason("/b1", kueue.InCohortReclamationReason)),
		},
		"can't preempt huge workload if the incoming is also huge": {
			clusterQueues: baseCQs,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "7").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "5").Obj(),
			targetCQ: "a",
		},
		"can't preempt 2 smaller workloads if the incoming is huge": {
			clusterQueues: baseCQs,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b2", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b3", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "6").Obj(),
			targetCQ: "a",
		},
		"preempt from target and others even if over nominal": {
			clusterQueues: baseCQs,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1_low", "").Priority(-1).Request(corev1.ResourceCPU, "2").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("a2_low", "").Priority(-1).Request(corev1.ResourceCPU, "1").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b2", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "4").Obj(),
			targetCQ: "a",
			wantPreempted: sets.New(
				targetKeyReason("/a1_low", kueue.InClusterQueueReason),
				targetKeyReason("/b1", kueue.InCohortFairSharingReason),
			),
		},
		"prefer to preempt workloads that don't make the target CQ have the biggest share": {
			clusterQueues: baseCQs,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b2", "").Request(corev1.ResourceCPU, "1").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b3", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("c1", "").Request(corev1.ResourceCPU, "1").SimpleReserveQuota("c", "default", now).Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "3.5").Obj(),
			targetCQ: "a",
			// It would have been possible to preempt "/b1" under rule S2-b, but S2-a was possible first.
			wantPreempted: sets.New(targetKeyReason("/b2", kueue.InCohortFairSharingReason)),
		},
		"preempt from different cluster queues if the end result has a smaller max share": {
			clusterQueues: baseCQs,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b2", "").Request(corev1.ResourceCPU, "2.5").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("c1", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("c", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("c2", "").Request(corev1.ResourceCPU, "2.5").SimpleReserveQuota("c", "default", now).Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "3.5").Obj(),
			targetCQ: "a",
			wantPreempted: sets.New(
				targetKeyReason("/b1", kueue.InCohortFairSharingReason),
				targetKeyReason("/c1", kueue.InCohortFairSharingReason),
			),
		},
		"scenario above does not flap": {
			clusterQueues: baseCQs,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "").Request(corev1.ResourceCPU, "3.5").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b2", "").Request(corev1.ResourceCPU, "2.5").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("c2", "").Request(corev1.ResourceCPU, "2.5").SimpleReserveQuota("c", "default", now).Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("b_incoming", "").Request(corev1.ResourceCPU, "2").Obj(),
			targetCQ: "b",
		},
		"cannot preempt if it would make the candidate CQ go under nominal after preempting one element": {
			clusterQueues: baseCQs,
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b2", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("c1", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("c", "default", now).Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "4").Obj(),
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
			incoming:      utiltestingapi.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "2").Obj(),
			targetCQ:      "a",
			wantPreempted: nil,
		},
		"preempt lower priority first, even if big": {
			clusterQueues: baseCQs,
			strategies:    []config.PreemptionStrategy{config.LessThanInitialShare},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b_low", "").Priority(0).Request(corev1.ResourceCPU, "5").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b_high", "").Priority(1).Request(corev1.ResourceCPU, "1").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming:      utiltestingapi.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "1").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New(targetKeyReason("/b_low", kueue.InCohortFairSharingReason)),
		},
		"preempt workload that doesn't transfer the imbalance, even if high priority": {
			clusterQueues: baseCQs,
			strategies:    []config.PreemptionStrategy{config.LessThanOrEqualToFinalShare},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "").Request(corev1.ResourceCPU, "3").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b_low", "").Priority(0).Request(corev1.ResourceCPU, "5").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b_high", "").Priority(1).Request(corev1.ResourceCPU, "1").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming:      utiltestingapi.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "1").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New(targetKeyReason("/b_high", kueue.InCohortFairSharingReason)),
		},
		"CQ with higher weight can preempt more": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					FairWeight(resource.MustParse("2")).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltestingapi.MakeClusterQueue("c").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
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
			incoming: utiltestingapi.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "2").Obj(),
			targetCQ: "a",
			wantPreempted: sets.New(
				targetKeyReason("/b1", kueue.InCohortFairSharingReason),
				targetKeyReason("/b2", kueue.InCohortFairSharingReason),
			),
		},
		"can preempt anything borrowing from CQ with 0 weight": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					FairWeight(resource.MustParse("0")).
					Obj(),
				utiltestingapi.MakeClusterQueue("c").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
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
			incoming: utiltestingapi.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "3").Obj(),
			targetCQ: "a",
			wantPreempted: sets.New(
				targetKeyReason("/b1", kueue.InCohortFairSharingReason),
				targetKeyReason("/b2", kueue.InCohortFairSharingReason),
				targetKeyReason("/b3", kueue.InCohortFairSharingReason),
			),
		},
		"can't preempt nominal from CQ with 0 weight": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
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
				utiltestingapi.MakeClusterQueue("left-cq").
					Cohort("root").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "0").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltestingapi.MakeClusterQueue("right-cq").
					Cohort("right-cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "0").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					FairWeight(resource.MustParse("0")).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("right-cohort").
					FairWeight(resource.MustParse("0")).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
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
				utiltestingapi.MakeClusterQueue("a").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
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
				utiltestingapi.MakeClusterQueue("a").
					Cohort("LEFT").
					FairWeight(resource.MustParse("2")).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("LEFT").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
				utiltestingapi.MakeClusterQueue("c").
					Cohort("ROOT").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
				utiltestingapi.MakeClusterQueue("d").
					Cohort("RIGHT").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
				utiltestingapi.MakeClusterQueue("e").
					Cohort("RIGHT").
					// for determinism, we slightly prefer preemptions from e
					// compared to d.
					FairWeight(resource.MustParse("0.99")).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("ROOT").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "5").Obj()).
					Obj(),
				utiltestingapi.MakeCohort("LEFT").
					FairWeight(resource.MustParse("2")).
					Parent("ROOT").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "5").Obj()).
					Obj(),
				utiltestingapi.MakeCohort("RIGHT").
					Parent("ROOT").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
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
			incoming: utiltestingapi.MakeWorkload("unit", "").Request(corev1.ResourceCPU, "5").Obj(),
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
				utiltestingapi.MakeClusterQueue("a").
					Cohort("ROOT").
					FairWeight(resource.MustParse("10")).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "5").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					}).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("RIGHT").
					FairWeight(resource.MustParse("0.1")).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("ROOT").Obj(),
				utiltestingapi.MakeCohort("RIGHT").Parent("ROOT").
					FairWeight(resource.MustParse("0.1")).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").Priority(-1).SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").Priority(-1).SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a3").Priority(-1).SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").Priority(-1).SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("unit", "").Request(corev1.ResourceCPU, "5").Obj(),
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
				utiltestingapi.MakeClusterQueue("a").
					Cohort("ROOT").
					FairWeight(resource.MustParse("10")).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "5").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					}).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("RIGHT").
					FairWeight(resource.MustParse("0.1")).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("ROOT").Obj(),
				utiltestingapi.MakeCohort("RIGHT").Parent("ROOT").
					FairWeight(resource.MustParse("0.1")).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "3").Obj()).
					Obj(),
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").Priority(-1).SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").Priority(-1).SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a3").Priority(-1).SimpleReserveQuota("a", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b1", "").Priority(100).Request(corev1.ResourceCPU, "4").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("unit", "").Request(corev1.ResourceCPU, "4").Obj(),
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
				utiltestingapi.MakeClusterQueue("a").
					Cohort("AAA").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "0").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("BBB").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "0").Obj()).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("ROOT").Obj(),
				utiltestingapi.MakeCohort("A").Parent("ROOT").
					// we are comparing
					// almostLCAs=(A,B). In order
					// for the preemption to
					// occur, we indicate that A
					// is more important than B.
					FairWeight(resource.MustParse("1.01")).
					Obj(),
				utiltestingapi.MakeCohort("AA").Parent("A").
					Obj(),
				utiltestingapi.MakeCohort("AAA").Parent("AA").
					Obj(),
				utiltestingapi.MakeCohort("B").Parent("ROOT").
					FairWeight(resource.MustParse("0.99")).
					Obj(),
				utiltestingapi.MakeCohort("BB").Parent("B").
					Obj(),
				utiltestingapi.MakeCohort("BBB").Parent("BB").
					Obj(),
				utiltestingapi.MakeCohort("C").Parent("ROOT").
					Obj(),
				utiltestingapi.MakeCohort("CC").Parent("C").
					Obj(),
				utiltestingapi.MakeCohort("CCC").Parent("CC").
					Obj(),
				utiltestingapi.MakeCohort("CCCC").Parent("CCC").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj()).
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
				utiltestingapi.MakeClusterQueue("a").
					Cohort("ROOT").
					FairWeight(resource.MustParse("0.0")).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("ROOT").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
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
				targetKeyReason("/b1", kueue.InCohortReclamationReason),
			),
		},
		"cohort with zero weight can reclaim nominal quota": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("A").
					FairWeight(resource.MustParse("0.0")).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "0").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("ROOT").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "0").Obj()).
					FairWeight(resource.MustParse("1.0")).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("A").
					Parent("ROOT").
					FairWeight(resource.MustParse("0.0")).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
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
		// CQ "a": 3 CPU nominal on premium, 0 on cheap.
		// CQ "b": 0 CPU nominal on premium, 6 on cheap.
		//
		// Admitted state:
		//   a: 2 premium (within nominal) + 5 cheap (borrowed) → high DRS
		//   b: 1 premium (borrowed from a's lendable) → low DRS
		//
		// Incoming: a wants 1 premium CPU (within nominal: 2+1=3 <= 3).
		// Without nominal-first: DRS(a) > DRS(b) → preemption blocked.
		// With nominal-first: a is within nominal for premium → preemption allowed.
		"nominal first: workload fitting within nominal can preempt despite high aggregate DRS": {
			flavors: []*kueue.ResourceFlavor{
				utiltestingapi.MakeResourceFlavor("premium").Obj(),
				utiltestingapi.MakeResourceFlavor("cheap").Obj(),
			},
			assignmentFlavor: "premium",
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("all").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("premium").Resource(corev1.ResourceCPU, "3").Obj(),
						*utiltestingapi.MakeFlavorQuotas("cheap").Resource(corev1.ResourceCPU, "0").Obj(),
					).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					FlavorFungibility(kueue.FlavorFungibility{
						WhenCanBorrow:  kueue.MayStopSearch,
						WhenCanPreempt: kueue.MayStopSearch,
					}).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("all").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("premium").Resource(corev1.ResourceCPU, "0").Obj(),
						*utiltestingapi.MakeFlavorQuotas("cheap").Resource(corev1.ResourceCPU, "6").Obj(),
					).
					Obj(),
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a_prem1").SimpleReserveQuota("a", "premium", now).Obj(),
				*unitWl.Clone().Name("a_prem2").SimpleReserveQuota("a", "premium", now).Obj(),
				*unitWl.Clone().Name("a_cheap1").SimpleReserveQuota("a", "cheap", now).Obj(),
				*unitWl.Clone().Name("a_cheap2").SimpleReserveQuota("a", "cheap", now).Obj(),
				*unitWl.Clone().Name("a_cheap3").SimpleReserveQuota("a", "cheap", now).Obj(),
				*unitWl.Clone().Name("a_cheap4").SimpleReserveQuota("a", "cheap", now).Obj(),
				*unitWl.Clone().Name("a_cheap5").SimpleReserveQuota("a", "cheap", now).Obj(),
				*unitWl.Clone().Name("b_prem1").SimpleReserveQuota("b", "premium", now).Obj(),
			},
			incoming:      unitWl.Clone().Name("a_incoming").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New(targetKeyReason("/b_prem1", kueue.InCohortReclamationReason)),
		},
		// The preemptor has a fair weight of 0 and borrows, so its share is
		// +Inf. The target borrows on a non-zero weight, so its share is
		// finite. CompareDRS ranks the preemptor above the target no matter
		// which of the target's workloads is removed, so neither strategy can
		// pass and no target is returned.
		"zero weight borrowing preemptor cannot preempt from a borrowing CQ with a non-zero weight": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("all").
					FairWeight(resource.MustParse("0")).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("all").
					FairWeight(resource.MustParse("1")).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b2").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming:      unitWl.Clone().Name("a_incoming").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New[string](),
		},
		// Both the preemptor and the target have a fair weight of 0 and
		// borrow, so both shares are +Inf. CompareDRS falls back to the
		// unweighted ratios in that case, and a candidate can win, so the
		// tournament must still run.
		"zero weight borrowing preemptor can preempt from a borrowing CQ which also has zero weight": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("all").
					FairWeight(resource.MustParse("0")).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("all").
					FairWeight(resource.MustParse("0")).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "0").Obj()).
					Obj(),
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b2").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b3").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "2").Obj(),
			targetCQ: "a",
			wantPreempted: sets.New(
				targetKeyReason("/b1", kueue.InCohortFairSharingReason),
			),
		},
		// Same shape as the case above, with non-zero fair weights on both
		// sides. Neither share is +Inf, so the early exit must not trigger
		// and the outcome is the same.
		"non-zero weight borrowing preemptor preempts from a borrowing CQ": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("all").
					FairWeight(resource.MustParse("1")).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("all").
					FairWeight(resource.MustParse("1")).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "0").Obj()).
					Obj(),
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b2").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b3").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "2").Obj(),
			targetCQ: "a",
			wantPreempted: sets.New(
				targetKeyReason("/b1", kueue.InCohortFairSharingReason),
			),
		},
		// Hierarchy:
		//
		//	ROOT ── A (weight 0, 1 CPU) ── p (weight 1, 2 CPU, preemptor)
		//	     │                      └─ y (weight 1, 2 CPU)
		//	     └─ x (weight 1, 1 CPU)
		//
		// The preemptor's almostLCA is p for target y, and A for target x.
		// A borrows on a zero weight, so its share is +Inf and no candidate
		// in x can pass the first strategy. The candidates from x must still
		// be kept for the second strategy: the first strategy visits A's
		// subtree first, and once y1 is preempted A stops borrowing, which
		// brings A's share down to 0 and lets a candidate from x win.
		"candidates skipped by the first strategy are still available to the second strategy": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("A").
					Parent("ROOT").
					FairWeight(resource.MustParse("0")).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("p").
					Cohort("A").
					FairWeight(resource.MustParse("1")).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltestingapi.MakeClusterQueue("y").
					Cohort("A").
					FairWeight(resource.MustParse("1")).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").Obj()).
					Obj(),
				utiltestingapi.MakeClusterQueue("x").
					Cohort("ROOT").
					FairWeight(resource.MustParse("1")).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("p1", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("p", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("y1", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("y", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("y2", "").Request(corev1.ResourceCPU, "2").SimpleReserveQuota("y", "default", now).Obj(),
				*unitWl.Clone().Name("x1").SimpleReserveQuota("x", "default", now).Obj(),
				*unitWl.Clone().Name("x2").SimpleReserveQuota("x", "default", now).Obj(),
			},
			incoming: unitWl.Clone().Name("p_incoming").Obj(),
			targetCQ: "p",
			wantPreempted: sets.New(
				targetKeyReason("/y1", kueue.InCohortFairSharingReason),
				targetKeyReason("/x1", kueue.InCohortFairSharingReason),
			),
		},
		// The case when workloads from preemptor's CQ have lower priority, so all the other
		// workloads are processed first, but none are considered fair by DRS.
		// Later, once the preemptor's workloads are preempted, now workloads from
		// other CQs are considered fair to be preempted by DRS.
		"can admit after preempting workloads from the preemptor's CQ with lower processing priority": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("left-a").
					Cohort("left").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "0").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyAny,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltestingapi.MakeClusterQueue("right-b").
					Cohort("right").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "0").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyAny,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltestingapi.MakeClusterQueue("right-c").
					Cohort("right").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "0").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyAny,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("root").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "15").Obj()).
					Obj(),
				utiltestingapi.MakeCohort("left").
					Parent("root").
					FairWeight(resource.MustParse("20")).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "0").Obj()).
					Obj(),
				utiltestingapi.MakeCohort("right").
					Parent("root").
					FairWeight(resource.MustParse("10")).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "0").Obj()).
					Obj(),
			},
			strategies: []config.PreemptionStrategy{config.LessThanOrEqualToFinalShare},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a1", "").Request(corev1.ResourceCPU, "1").SimpleReserveQuota("left-a", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("a2", "").Request(corev1.ResourceCPU, "1").SimpleReserveQuota("left-a", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("a3", "").Request(corev1.ResourceCPU, "1").SimpleReserveQuota("left-a", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "1").SimpleReserveQuota("right-b", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b2", "").Request(corev1.ResourceCPU, "1").SimpleReserveQuota("right-b", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b3", "").Request(corev1.ResourceCPU, "1").SimpleReserveQuota("right-b", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b4", "").Request(corev1.ResourceCPU, "1").SimpleReserveQuota("right-b", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b5", "").Request(corev1.ResourceCPU, "1").SimpleReserveQuota("right-b", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("b6", "").Request(corev1.ResourceCPU, "1").SimpleReserveQuota("right-b", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("c1", "").Request(corev1.ResourceCPU, "1").SimpleReserveQuota("right-c", "default", now).Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "10").Obj(),
			targetCQ: "left-a",
			// Detailed scenario:
			// * Workloads from CQs "right-b" and "right-c" are processed first because of fairWeights.
			// * Initially workloads from "right" cohort doesn't satisfy the strategy because
			//   DRS of preemptor's is large (because of fairWright).
			// * The fair preemption algorithm removes workloads from preemptor's CQ "left-a" without
			//   considering the strategy (because they are from the same CQ), so preemptor's DRS decreases.
			//   (preempted: a1, a2, a3)
			// * Now, workloads from CQs "right-b" and "right-c" satisfy the fairness condition by the strategy.
			// * Workloads from the "right-b" CQ are preempted because they had larger DRS than workloads from "right-c" CQ.
			//   (preempted: b1, b2)
			wantPreempted: sets.New(
				targetKeyReason("/a1", kueue.InClusterQueueReason),
				targetKeyReason("/a2", kueue.InClusterQueueReason),
				targetKeyReason("/a3", kueue.InClusterQueueReason),
				targetKeyReason("/b1", kueue.InCohortFairSharingReason),
				targetKeyReason("/b2", kueue.InCohortFairSharingReason),
			),
			featureGates: map[featuregate.Feature]bool{features.FairSharingReevaluatePreemptionCandidates: true},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)

			ctx, log := utiltesting.ContextWithLog(t)
			// Set name as UID so that candidates sorting is predictable.
			for i := range tc.admitted {
				tc.admitted[i].UID = types.UID(tc.admitted[i].Name)
			}
			cl := utiltesting.NewClientBuilder().
				WithLists(&kueue.WorkloadList{Items: tc.admitted}).
				Build()
			cqCache := schdcache.New(cl)
			flvs := flavors
			if tc.flavors != nil {
				flvs = tc.flavors
			}
			for _, flv := range flvs {
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
			preemptor := New(cl, workload.Ordering{}, recorder, &config.FairSharing{
				PreemptionStrategies: tc.strategies,
			}, false, clocktesting.NewFakeClock(now), nil, preemptexpectations.New(), nil)

			beforeSnapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			snapshotWorkingCopy, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			flavorName := kueue.ResourceFlavorReference("default")
			if tc.assignmentFlavor != "" {
				flavorName = tc.assignmentFlavor
			}
			wlInfo := workload.NewInfo(log, tc.incoming)
			wlInfo.ClusterQueue = tc.targetCQ
			targets := preemptor.GetTargets(log, *wlInfo, singlePodSetAssignment(
				flavorassigner.ResourceAssignment{
					corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
						Name: flavorName, Mode: flavorassigner.Preempt,
					},
				},
			), snapshotWorkingCopy)
			gotTargets := sets.New(utilslices.Map(targets, func(t **Target) string {
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

// TestFairPreemptionSkipsUnsatisfiableTournament checks that a target
// ClusterQueue whose candidates provably cannot pass either strategy is
// skipped without simulating each candidate, and that skipping it changes
// neither the targets chosen nor the progress of the ordering.
//
// Both cases use the same topology, differing only in the preemptor's fair
// weight. With a weight of 0 the borrowing preemptor's share is +Inf, so no
// candidate can win and the tournament is skipped. With a weight of 1 the
// share is finite and every candidate is simulated. The outcome is the same
// either way.
//
// The exact skipped-queue count is also a spin check. The ordering only stops
// yielding a ClusterQueue once its workloads are gone, so a skip which failed
// to consume them would yield the same ClusterQueue forever.
func TestFairPreemptionSkipsUnsatisfiableTournament(t *testing.T) {
	now := time.Now()
	unitWl := *utiltestingapi.MakeWorkload("unit", "").Request(corev1.ResourceCPU, "1")

	cases := map[string]struct {
		preemptorFairWeight     string
		wantFirstStrategyEvals  int
		wantSecondStrategyEvals int
		wantSkippedQueues       int
	}{
		"zero weight preemptor: share is +Inf and the tournament is skipped": {
			preemptorFairWeight:    "0",
			wantFirstStrategyEvals: 0,
			// The candidates are still handed to the second strategy, which
			// evaluates the target ClusterQueue once.
			wantSecondStrategyEvals: 1,
			wantSkippedQueues:       1,
		},
		"non-zero weight preemptor: share is finite so the tournament runs": {
			preemptorFairWeight: "1",
			// The first strategy collapses both candidate workloads of
			// ClusterQueue "b" into one log entry, so this is 1, not 2.
			wantFirstStrategyEvals:  1,
			wantSecondStrategyEvals: 1,
			wantSkippedQueues:       0,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, setupLog := utiltesting.ContextWithLog(t)
			// Count the FairSharing V(4) log entries. The first strategy logs
			// one collapsed entry per candidate ClusterQueue, carrying a
			// strategyEvaluations array (so its args include targetNewShare);
			// the second strategy logs the same message without it, and a
			// skipped tournament logs its own message. Counting them is how the
			// test sees the work was skipped.
			var firstStrategyEvals, secondStrategyEvals, skippedQueues int
			log := funcr.New(func(_, args string) {
				switch {
				case strings.Contains(args, "Skipping FairSharing strategy evaluation"):
					skippedQueues++
				case strings.Contains(args, "Evaluating FairSharing strategy") && strings.Contains(args, "targetNewShare"):
					firstStrategyEvals++
				case strings.Contains(args, "Evaluating FairSharing strategy"):
					secondStrategyEvals++
				}
			}, funcr.Options{Verbosity: 4})
			ctx := ctrl.LoggerInto(t.Context(), log)

			admitted := []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b2").SimpleReserveQuota("b", "default", now).Obj(),
			}
			for i := range admitted {
				admitted[i].UID = types.UID(admitted[i].Name)
			}
			cl := utiltesting.NewClientBuilder().
				WithLists(&kueue.WorkloadList{Items: admitted}).
				Build()
			cqCache := schdcache.New(cl)
			cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
			clusterQueues := []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("all").
					FairWeight(resource.MustParse(tc.preemptorFairWeight)).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("all").
					FairWeight(resource.MustParse("1")).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			}
			for _, cq := range clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
				}
			}
			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}

			preemptor := New(cl, workload.Ordering{}, &utiltesting.EventRecorder{}, &config.FairSharing{},
				false, clocktesting.NewFakeClock(now), nil, preemptexpectations.New(), nil)
			wlInfo := workload.NewInfo(setupLog, unitWl.Clone().Name("a_incoming").Obj())
			wlInfo.ClusterQueue = "a"
			targets := preemptor.GetTargets(log, *wlInfo, singlePodSetAssignment(
				flavorassigner.ResourceAssignment{
					corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
						Name: "default", Mode: flavorassigner.Preempt,
					},
				},
			), snapshot)

			// The preemptor's share is at or above the target's either way, so
			// no candidate wins and nothing is preempted.
			if len(targets) != 0 {
				t.Errorf("Issued preemptions: got %d targets, want 0", len(targets))
			}
			if firstStrategyEvals != tc.wantFirstStrategyEvals {
				t.Errorf("First strategy candidate simulations: got %d, want %d",
					firstStrategyEvals, tc.wantFirstStrategyEvals)
			}
			if secondStrategyEvals != tc.wantSecondStrategyEvals {
				t.Errorf("Second strategy evaluations: got %d, want %d",
					secondStrategyEvals, tc.wantSecondStrategyEvals)
			}
			if skippedQueues != tc.wantSkippedQueues {
				t.Errorf("Skipped target ClusterQueues: got %d, want %d",
					skippedQueues, tc.wantSkippedQueues)
			}
		})
	}
}
