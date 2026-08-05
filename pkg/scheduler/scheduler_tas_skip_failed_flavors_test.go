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
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/util/routine"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
)

// TestScheduleForTASSkipRecentlyFailedFlavors exercises the cross-cycle behavior of the
// TASSkipRecentlyFailedFlavors gate: a Workload whose TAS placement fails on the first
// flavor must reach the second flavor on a later cycle, rather than being re-nominated
// to the first flavor forever because quota alone still reports it as fitting.
//
// This needs its own runner because the shared runTASScheduleTestCases harness drives a
// single scheduling cycle, and the behavior under test only appears across cycles.
func TestScheduleForTASSkipRecentlyFailedFlavors(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	singleLevelTopology := *utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level")

	// flavor-1 has a single node that the blocking Workload fully occupies, so the
	// tested Workload's topology never fits there. flavor-2 has room for it.
	nodes := []corev1.Node{
		*testingnode.MakeNode("node-f1").
			Label("tas-node", "true").
			Label("tas-flavor", "f1").
			Label(corev1.LabelHostname, "node-f1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("2"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("node-f2").
			Label("tas-node", "true").
			Label("tas-flavor", "f2").
			Label(corev1.LabelHostname, "node-f2").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("2"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj(),
	}
	resourceFlavors := []kueue.ResourceFlavor{
		*utiltestingapi.MakeResourceFlavor("tas-flavor-1").
			NodeLabel("tas-flavor", "f1").
			TopologyName("tas-single-level").
			Obj(),
		*utiltestingapi.MakeResourceFlavor("tas-flavor-2").
			NodeLabel("tas-flavor", "f2").
			TopologyName("tas-single-level").
			Obj(),
	}
	// Quota on flavor-1 exceeds what its single node can actually host, so the flavor
	// keeps looking admissible to the quota-only picker after the node is occupied.
	// The default flavorFungibility (WhenCanPreempt: Preempt) makes the picker stop at
	// flavor-1 once its mode is downgraded to Preempt, which is what pins the Workload
	// there across cycles.
	clusterQueue := *utiltestingapi.MakeClusterQueue("tas-cq").
		Preemption(kueue.ClusterQueuePreemption{
			WithinClusterQueue:  kueue.PreemptionPolicyNever,
			ReclaimWithinCohort: kueue.PreemptionPolicyNever,
		}).
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("tas-flavor-1").Resource(corev1.ResourceCPU, "4").Obj(),
			*utiltestingapi.MakeFlavorQuotas("tas-flavor-2").Resource(corev1.ResourceCPU, "4").Obj(),
		).
		Obj()
	queues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("tas-lq", "default").ClusterQueue("tas-cq").Obj(),
	}
	// blocker occupies node-f1 entirely; pending needs a whole node and can only be
	// placed on node-f2.
	blocker := *utiltestingapi.MakeWorkload("blocker", "default").
		Queue("tas-lq").
		Creation(now.Add(-time.Minute)).
		Priority(10).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			NodeSelector(map[string]string{"tas-flavor": "f1"}).
			RequiredTopologyRequest(corev1.LabelHostname).
			Request(corev1.ResourceCPU, "2").
			Obj()).
		Obj()
	pending := *utiltestingapi.MakeWorkload("pending", "default").
		Queue("tas-lq").
		Creation(now).
		Priority(5).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			RequiredTopologyRequest(corev1.LabelHostname).
			Request(corev1.ResourceCPU, "2").
			Obj()).
		Obj()

	cases := map[string]struct {
		gateEnabled bool
		// cycles is how many scheduling cycles to drive.
		cycles int
		// wantPendingFlavor is the flavor "pending" must end up admitted on, or empty
		// if it must remain unadmitted.
		wantPendingFlavor kueue.ResourceFlavorReference
	}{
		"gate disabled: pending stays pinned to the first flavor and is never admitted": {
			gateEnabled:       false,
			cycles:            failedFlavorPlacementTTL + 1,
			wantPendingFlavor: "",
		},
		"gate enabled: pending reaches the second flavor on a later cycle": {
			gateEnabled:       true,
			cycles:            failedFlavorPlacementTTL + 1,
			wantPendingFlavor: "tas-flavor-2",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.TASSkipRecentlyFailedFlavors: tc.gateEnabled,
			})

			ctx, log := utiltesting.ContextWithLog(t)
			testWls := []kueue.Workload{*blocker.DeepCopy(), *pending.DeepCopy()}
			clientBuilder := utiltesting.NewClientBuilder().
				WithLists(
					&kueue.WorkloadList{Items: testWls},
					&kueue.TopologyList{Items: []kueue.Topology{singleLevelTopology}},
					&corev1.NodeList{Items: nodes},
					&kueue.LocalQueueList{Items: queues}).
				WithObjects(utiltesting.MakeNamespace("default")).
				WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge}).
				WithStatusSubresource(&kueue.Workload{})
			_ = tasindexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder))
			cl := clientBuilder.Build()
			recorder := &utiltesting.EventRecorder{}
			cqCache := schdcache.New(cl)
			qManager, requeuer := qcache.NewManagerForUnitTestsWithRequeuer(cl, cqCache)

			for i := range nodes {
				cqCache.TASCache().SyncNode(&nodes[i])
			}
			for _, flavor := range resourceFlavors {
				cqCache.AddOrUpdateResourceFlavor(log, &flavor)
				cqCache.AddOrUpdateTopology(log, &singleLevelTopology)
			}
			if err := cqCache.AddClusterQueue(ctx, &clusterQueue); err != nil {
				t.Fatalf("Inserting clusterQueue in cache: %v", err)
			}
			if err := qManager.AddClusterQueue(ctx, &clusterQueue); err != nil {
				t.Fatalf("Inserting clusterQueue in manager: %v", err)
			}
			cqCopy := clusterQueue.DeepCopy()
			cqCopy.ResourceVersion = ""
			if err := cl.Create(ctx, cqCopy); err != nil {
				t.Fatalf("Creating clusterQueue: %v", err)
			}
			for _, q := range queues {
				if err := qManager.AddLocalQueue(ctx, &q); err != nil {
					t.Fatalf("Inserting queue %s/%s in manager: %v", q.Namespace, q.Name, err)
				}
			}

			scheduler := New(qManager, cqCache, cl, recorder,
				WithClock(t, testingclock.NewFakeClock(now)),
				WithPreemptionExpectations(preemptexpectations.New()))
			wg := sync.WaitGroup{}
			scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
				func() { wg.Add(1) },
				func() { wg.Done() },
			))

			ctx, cancel := context.WithTimeout(ctx, queueingTimeout)
			go qManager.CleanUpOnContext(ctx)
			defer cancel()

			for i := range tc.cycles {
				scheduler.schedule(ctx)
				wg.Wait()
				// Reproduce a busy cohort. AllocatableResourceGeneration only advances
				// when the quotas actually change (see updateQuotasAndResourceGroups), so
				// flip flavor-2's quota between two values to force a real bump each
				// cycle. The bump discards LastAssignment (see lastAssignmentOutdated)
				// and with it the built-in flavor bookmark, which on a cluster with
				// steady admissions and evictions happens every cycle. That is the
				// condition the cross-cycle record exists to survive.
				//
				// flavor-2 is the one flipped so that flavor-1, the flavor under test,
				// keeps a constant quota throughout.
				churned := clusterQueue.DeepCopy()
				churnQuota := "4"
				if i%2 == 0 {
					churnQuota = "5"
				}
				churned.Spec.ResourceGroups[0].Flavors[1].Resources[0].NominalQuota = resource.MustParse(churnQuota)
				if err := cqCache.UpdateClusterQueue(log, churned); err != nil {
					t.Fatalf("Updating clusterQueue in cache: %v", err)
				}
				// Move workloads that were parked as inadmissible back into the queue so
				// the next cycle reconsiders them, as the controllers would in a cluster.
				requeuer.ProcessRequeues(ctx)
			}

			gotFlavor := admittedFlavor(ctx, t, cl, "pending")
			if gotFlavor != tc.wantPendingFlavor {
				t.Errorf("workload \"pending\" admitted on flavor %q, want %q", gotFlavor, tc.wantPendingFlavor)
			}
			// The blocker must keep its place on flavor-1 either way.
			if got := admittedFlavor(ctx, t, cl, "blocker"); got != "tas-flavor-1" {
				t.Errorf("workload \"blocker\" admitted on flavor %q, want %q", got, "tas-flavor-1")
			}
		})
	}
}

// admittedFlavor returns the flavor assigned to the Workload's only PodSet resource, or
// an empty reference when the Workload has no quota reservation.
func admittedFlavor(ctx context.Context, t *testing.T, cl client.Client, name string) kueue.ResourceFlavorReference {
	t.Helper()
	var wl kueue.Workload
	if err := cl.Get(ctx, client.ObjectKey{Namespace: "default", Name: name}, &wl); err != nil {
		t.Fatalf("Getting workload %q: %v", name, err)
	}
	if !workload.HasQuotaReservation(&wl) {
		return ""
	}
	for _, psa := range wl.Status.Admission.PodSetAssignments {
		for _, flavor := range psa.Flavors {
			return flavor
		}
	}
	return ""
}

const testWorkloadUID = types.UID("test-wl-uid")

func failedPlacements(pairs map[kueue.PodSetReference][]kueue.ResourceFlavorReference) map[kueue.PodSetReference]sets.Set[kueue.ResourceFlavorReference] {
	if pairs == nil {
		return nil
	}
	out := make(map[kueue.PodSetReference]sets.Set[kueue.ResourceFlavorReference], len(pairs))
	for ps, flavors := range pairs {
		out[ps] = sets.New(flavors...)
	}
	return out
}

// entryWithFailedPlacements builds an entry as it looks after a scheduling cycle in
// which the TAS placement search rejected the given flavors. podSetCount controls the
// shape of the Workload, since recording is limited to single-PodSet Workloads.
func entryWithFailedPlacements(reason qcache.RequeueReason, placements map[kueue.PodSetReference][]kueue.ResourceFlavorReference, podSetCount int) *entry {
	podSets := make([]kueue.PodSet, podSetCount)
	for i := range podSets {
		podSets[i] = kueue.PodSet{Name: kueue.PodSetReference("ps" + strconv.Itoa(i))}
	}
	return &entry{
		Info: workload.Info{
			Obj: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{Name: "wl", Namespace: "ns", UID: testWorkloadUID},
				Spec:       kueue.WorkloadSpec{PodSets: podSets},
			},
		},
		requeueReason: reason,
		assignment: flavorassigner.Assignment{
			FailedFlavorPlacements: failedPlacements(placements),
		},
	}
}

func TestRecordFailedFlavorPlacements(t *testing.T) {
	cases := map[string]struct {
		enabled bool
		// multiPodSet builds the Workload with two PodSets instead of one. Recording is
		// limited to single-PodSet Workloads, so this must suppress it.
		multiPodSet bool
		// cycles are replayed in order; each records one entry at the given cycle.
		cycles []struct {
			cycle      int64
			reason     qcache.RequeueReason
			placements map[kueue.PodSetReference][]kueue.ResourceFlavorReference
		}
		readAtCycle int64
		want        map[kueue.PodSetReference]sets.Set[kueue.ResourceFlavorReference]
	}{
		"gate disabled: nothing is recorded": {
			enabled: false,
			cycles: []struct {
				cycle      int64
				reason     qcache.RequeueReason
				placements map[kueue.PodSetReference][]kueue.ResourceFlavorReference
			}{
				{cycle: 1, reason: qcache.RequeueReasonPreemptionNoCandidates, placements: map[kueue.PodSetReference][]kueue.ResourceFlavorReference{"main": {"flavor-a"}}},
			},
			readAtCycle: 2,
			want:        nil,
		},
		"records the failed flavor": {
			enabled: true,
			cycles: []struct {
				cycle      int64
				reason     qcache.RequeueReason
				placements map[kueue.PodSetReference][]kueue.ResourceFlavorReference
			}{
				{cycle: 1, reason: qcache.RequeueReasonPreemptionNoCandidates, placements: map[kueue.PodSetReference][]kueue.ResourceFlavorReference{"main": {"flavor-a"}}},
			},
			readAtCycle: 2,
			want:        failedPlacements(map[kueue.PodSetReference][]kueue.ResourceFlavorReference{"main": {"flavor-a"}}),
		},
		"accumulates flavors across cycles": {
			enabled: true,
			cycles: []struct {
				cycle      int64
				reason     qcache.RequeueReason
				placements map[kueue.PodSetReference][]kueue.ResourceFlavorReference
			}{
				{cycle: 1, reason: qcache.RequeueReasonNoFit, placements: map[kueue.PodSetReference][]kueue.ResourceFlavorReference{"main": {"flavor-a"}}},
				{cycle: 2, reason: qcache.RequeueReasonNoFit, placements: map[kueue.PodSetReference][]kueue.ResourceFlavorReference{"main": {"flavor-b"}}},
			},
			readAtCycle: 3,
			want:        failedPlacements(map[kueue.PodSetReference][]kueue.ResourceFlavorReference{"main": {"flavor-a", "flavor-b"}}),
		},
		"a fresh failure refreshes the stamp of earlier ones": {
			enabled: true,
			cycles: []struct {
				cycle      int64
				reason     qcache.RequeueReason
				placements map[kueue.PodSetReference][]kueue.ResourceFlavorReference
			}{
				{cycle: 1, reason: qcache.RequeueReasonNoFit, placements: map[kueue.PodSetReference][]kueue.ResourceFlavorReference{"main": {"flavor-a"}}},
				{cycle: failedFlavorPlacementTTL, reason: qcache.RequeueReasonNoFit, placements: map[kueue.PodSetReference][]kueue.ResourceFlavorReference{"main": {"flavor-b"}}},
			},
			// flavor-a alone would have expired by now, but the later write kept it.
			readAtCycle: failedFlavorPlacementTTL + 1,
			want:        failedPlacements(map[kueue.PodSetReference][]kueue.ResourceFlavorReference{"main": {"flavor-a", "flavor-b"}}),
		},
		"record expires after the TTL": {
			enabled: true,
			cycles: []struct {
				cycle      int64
				reason     qcache.RequeueReason
				placements map[kueue.PodSetReference][]kueue.ResourceFlavorReference
			}{
				{cycle: 1, reason: qcache.RequeueReasonNoFit, placements: map[kueue.PodSetReference][]kueue.ResourceFlavorReference{"main": {"flavor-a"}}},
			},
			readAtCycle: 1 + failedFlavorPlacementTTL,
			want:        nil,
		},
		"record survives up to the last cycle within the TTL": {
			enabled: true,
			cycles: []struct {
				cycle      int64
				reason     qcache.RequeueReason
				placements map[kueue.PodSetReference][]kueue.ResourceFlavorReference
			}{
				{cycle: 1, reason: qcache.RequeueReasonNoFit, placements: map[kueue.PodSetReference][]kueue.ResourceFlavorReference{"main": {"flavor-a"}}},
			},
			readAtCycle: failedFlavorPlacementTTL,
			want:        failedPlacements(map[kueue.PodSetReference][]kueue.ResourceFlavorReference{"main": {"flavor-a"}}),
		},
		"pending preemption keeps the flavor available for the preemption retry": {
			enabled: true,
			cycles: []struct {
				cycle      int64
				reason     qcache.RequeueReason
				placements map[kueue.PodSetReference][]kueue.ResourceFlavorReference
			}{
				{cycle: 1, reason: qcache.RequeueReasonPendingPreemption, placements: map[kueue.PodSetReference][]kueue.ResourceFlavorReference{"main": {"flavor-a"}}},
			},
			readAtCycle: 2,
			want:        nil,
		},
		"pending migration keeps the flavor available": {
			enabled: true,
			cycles: []struct {
				cycle      int64
				reason     qcache.RequeueReason
				placements map[kueue.PodSetReference][]kueue.ResourceFlavorReference
			}{
				{cycle: 1, reason: qcache.RequeueReasonPendingMigration, placements: map[kueue.PodSetReference][]kueue.ResourceFlavorReference{"main": {"flavor-a"}}},
			},
			readAtCycle: 2,
			want:        nil,
		},
		"preemption gated keeps the flavor available": {
			enabled: true,
			cycles: []struct {
				cycle      int64
				reason     qcache.RequeueReason
				placements map[kueue.PodSetReference][]kueue.ResourceFlavorReference
			}{
				{cycle: 1, reason: qcache.RequeueReasonPreemptionGated, placements: map[kueue.PodSetReference][]kueue.ResourceFlavorReference{"main": {"flavor-a"}}},
			},
			readAtCycle: 2,
			want:        nil,
		},
		"an assignment without placement failures records nothing": {
			enabled: true,
			cycles: []struct {
				cycle      int64
				reason     qcache.RequeueReason
				placements map[kueue.PodSetReference][]kueue.ResourceFlavorReference
			}{
				{cycle: 1, reason: qcache.RequeueReasonNoFit, placements: nil},
			},
			readAtCycle: 2,
			want:        nil,
		},
		"multi-PodSet Workload records nothing": {
			enabled:     true,
			multiPodSet: true,
			cycles: []struct {
				cycle      int64
				reason     qcache.RequeueReason
				placements map[kueue.PodSetReference][]kueue.ResourceFlavorReference
			}{
				{cycle: 1, reason: qcache.RequeueReasonNoFit, placements: map[kueue.PodSetReference][]kueue.ResourceFlavorReference{"leader": {"flavor-a"}}},
				{cycle: 2, reason: qcache.RequeueReasonNoFit, placements: map[kueue.PodSetReference][]kueue.ResourceFlavorReference{"worker": {"flavor-b"}}},
			},
			readAtCycle: 3,
			want:        nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TASSkipRecentlyFailedFlavors, tc.enabled)
			_, log := utiltesting.ContextWithLog(t)

			podSetCount := 1
			if tc.multiPodSet {
				podSetCount = 2
			}
			s := &Scheduler{failedFlavorPlacements: make(map[types.UID]failedFlavorPlacement)}
			for _, c := range tc.cycles {
				s.schedulingCycle = c.cycle
				s.recordFailedFlavorPlacements(log, entryWithFailedPlacements(c.reason, c.placements, podSetCount))
			}

			s.schedulingCycle = tc.readAtCycle
			got := s.failedFlavorPlacementsFor(testWorkloadUID)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("failedFlavorPlacementsFor() returned unexpected flavors (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestSweepFailedFlavorPlacements(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TASSkipRecentlyFailedFlavors, true)

	const (
		freshUID   = types.UID("fresh")
		expiredUID = types.UID("expired")
	)
	s := &Scheduler{
		schedulingCycle: 10,
		failedFlavorPlacements: map[types.UID]failedFlavorPlacement{
			freshUID: {
				flavors:       failedPlacements(map[kueue.PodSetReference][]kueue.ResourceFlavorReference{"main": {"flavor-a"}}),
				recordedCycle: 10 - failedFlavorPlacementTTL + 1,
			},
			expiredUID: {
				flavors:       failedPlacements(map[kueue.PodSetReference][]kueue.ResourceFlavorReference{"main": {"flavor-a"}}),
				recordedCycle: 10 - failedFlavorPlacementTTL,
			},
		},
	}

	s.sweepFailedFlavorPlacements()

	if _, ok := s.failedFlavorPlacements[expiredUID]; ok {
		t.Errorf("sweepFailedFlavorPlacements() kept the expired record for %q", expiredUID)
	}
	if _, ok := s.failedFlavorPlacements[freshUID]; !ok {
		t.Errorf("sweepFailedFlavorPlacements() dropped the unexpired record for %q", freshUID)
	}
}
