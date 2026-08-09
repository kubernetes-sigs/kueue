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
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/util/routine"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
)

// TestScheduleForFairSharingRefillTAS exercises refill on a TAS ClusterQueue,
// where the refilled workload's topology placement must account for the
// domain capacity the same cycle's earlier admissions already took. The
// fixture is a single-level (hostname) topology with 2-CPU nodes and quota
// above the nodes' capacity, so the discriminating constraint is placement,
// not quota. Each case runs with TASRecomputeAssignmentWithinSchedulingCycle
// on and off: the recompute would heal an outdated nomination, so the off
// arm is what pins the freshness of the refill nomination itself.
func TestScheduleForFairSharingRefillTAS(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	singleLevelTopology := utiltestingapi.MakeTopology("tas-single-level").
		Levels(corev1.LabelHostname).
		Obj()
	tasFlavor := utiltestingapi.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-single-level").
		Obj()
	clusterQueue := utiltestingapi.MakeClusterQueue("tas-refill").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "6").Obj()).
		Obj()
	localQueue := utiltestingapi.MakeLocalQueue("tas-refill-lq", "default").
		ClusterQueue("tas-refill").Obj()

	node := func(name string) corev1.Node {
		return *testingnode.MakeNode(name).
			Label("tas-node", "true").
			Label(corev1.LabelHostname, name).
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("2"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj()
	}
	// tasWl requires all pods on one hostname, so a workload occupies exactly
	// one node and the per-node remaining capacity decides its placement.
	tasWl := func(name string, pods int, creation time.Time) kueue.Workload {
		return *utiltestingapi.MakeWorkload(name, "default").
			Queue("tas-refill-lq").
			Creation(creation).
			PodSets(*utiltestingapi.MakePodSet("one", pods).
				RequiredTopologyRequest(corev1.LabelHostname).
				Request(corev1.ResourceCPU, "1").
				Obj()).
			Obj()
	}
	tasAdmission := func(hostname string, pods int32, cpu string) kueue.Admission {
		return *utiltestingapi.MakeAdmission("tas-refill").PodSets(
			utiltestingapi.MakePodSetAssignment("one").
				Assignment(corev1.ResourceCPU, "tas-default", cpu).
				Count(pods).
				TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
					Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{hostname}, pods).Obj()).
					Obj()).
				Obj(),
		).Obj()
	}

	cases := map[string]struct {
		refillEnabled        bool
		nodes                []corev1.Node
		workloads            []kueue.Workload
		wantAssignments      map[workload.Reference]kueue.Admission
		wantLeft             map[kueue.ClusterQueueReference][]workload.Reference
		wantInadmissibleLeft map[kueue.ClusterQueueReference][]workload.Reference
	}{
		// The head fills node x1; the refilled successor also needs a whole
		// node and must land on x2. Both workloads land in one cycle, on
		// disjoint nodes.
		"refilled workload is placed on the node its predecessor left free": {
			refillEnabled: true,
			nodes:         []corev1.Node{node("x1"), node("x2")},
			workloads: []kueue.Workload{
				tasWl("tas-a", 2, now.Add(-2*time.Minute)),
				tasWl("tas-b", 2, now.Add(-time.Minute)),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/tas-a": tasAdmission("x1", 2, "2000m"),
				"default/tas-b": tasAdmission("x2", 2, "2000m"),
			},
		},
		// A refill chain of two: each successor's nomination must see the
		// cumulative usage of every admission earlier in the cycle, so the
		// three workloads spread over the three nodes within one cycle.
		"a refill chain places each successor on a remaining node": {
			refillEnabled: true,
			nodes:         []corev1.Node{node("x1"), node("x2"), node("x3")},
			workloads: []kueue.Workload{
				tasWl("tas-a", 2, now.Add(-3*time.Minute)),
				tasWl("tas-b", 2, now.Add(-2*time.Minute)),
				tasWl("tas-c", 2, now.Add(-time.Minute)),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/tas-a": tasAdmission("x1", 2, "2000m"),
				"default/tas-b": tasAdmission("x2", 2, "2000m"),
				"default/tas-c": tasAdmission("x3", 2, "2000m"),
			},
		},
		// One node only: the head fills it, and the refilled successor still
		// fits the quota but not the topology, so its flavor attempt degrades
		// to Preempt with no candidates and it is parked as inadmissible.
		"refilled workload fails placement once the only node is full": {
			refillEnabled: true,
			nodes:         []corev1.Node{node("x1")},
			workloads: []kueue.Workload{
				tasWl("tas-a", 2, now.Add(-2*time.Minute)),
				tasWl("tas-b", 1, now.Add(-time.Minute)),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/tas-a": tasAdmission("x1", 2, "2000m"),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-refill": {"default/tas-b"},
			},
		},
		// Gate-off contrast for the two-node fixture: the successor is never
		// popped and waits in the heap for the next cycle.
		"gate off: the successor waits for the next cycle": {
			refillEnabled: false,
			nodes:         []corev1.Node{node("x1"), node("x2")},
			workloads: []kueue.Workload{
				tasWl("tas-a", 2, now.Add(-2*time.Minute)),
				tasWl("tas-b", 2, now.Add(-time.Minute)),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/tas-a": tasAdmission("x1", 2, "2000m"),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"tas-refill": {"default/tas-b"},
			},
		},
	}
	for name, tc := range cases {
		for _, recompute := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s recompute:%t", name, recompute), func(t *testing.T) {
				features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
					features.FairSharingRefill:                           tc.refillEnabled,
					features.TASRecomputeAssignmentWithinSchedulingCycle: recompute,
				})
				ctx, log := utiltesting.ContextWithLog(t)

				clientBuilder := utiltesting.NewClientBuilder().
					WithLists(
						&kueue.WorkloadList{Items: tc.workloads},
						&corev1.NodeList{Items: tc.nodes},
						&kueue.TopologyList{Items: []kueue.Topology{*singleLevelTopology}},
						&kueue.LocalQueueList{Items: []kueue.LocalQueue{*localQueue}}).
					WithObjects(utiltesting.MakeNamespace("default")).
					WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge}).
					WithStatusSubresource(&kueue.Workload{}, &kueue.ClusterQueue{}, &kueue.LocalQueue{})
				_ = tasindexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder))
				cl := clientBuilder.Build()

				recorder := &utiltesting.EventRecorder{}
				cqCache := schdcache.New(cl)
				fakeClock := testingclock.NewFakeClock(now)
				qManager := qcache.NewManagerForUnitTests(cl, cqCache, qcache.WithClock(fakeClock))
				for i := range tc.nodes {
					cqCache.TASCache().SyncNode(&tc.nodes[i])
				}
				cqCache.AddOrUpdateResourceFlavor(log, tasFlavor.DeepCopy())
				cqCache.AddOrUpdateTopology(log, singleLevelTopology.DeepCopy())
				if err := cqCache.AddClusterQueue(ctx, clusterQueue.DeepCopy()); err != nil {
					t.Fatalf("Inserting clusterQueue %s in cache: %v", clusterQueue.Name, err)
				}
				if err := qManager.AddClusterQueue(ctx, clusterQueue.DeepCopy()); err != nil {
					t.Fatalf("Inserting clusterQueue %s in manager: %v", clusterQueue.Name, err)
				}
				if err := qManager.AddLocalQueue(ctx, localQueue.DeepCopy()); err != nil {
					t.Fatalf("Inserting queue %s/%s in manager: %v", localQueue.Namespace, localQueue.Name, err)
				}

				scheduler := New(qManager, cqCache, cl, recorder,
					WithFairSharing(&config.FairSharing{}),
					WithClock(t, fakeClock),
					WithPreemptionExpectations(preemptexpectations.New()))
				wg := sync.WaitGroup{}
				scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
					func() { wg.Add(1) },
					func() { wg.Done() },
				))

				ctx, cancel := context.WithTimeout(ctx, queueingTimeout)
				go qManager.CleanUpOnContext(ctx)
				defer cancel()

				scheduler.schedule(ctx)
				wg.Wait()

				snapshot, err := cqCache.Snapshot(ctx)
				if err != nil {
					t.Fatalf("unexpected error while building snapshot: %v", err)
				}
				gotAssignments := make(map[workload.Reference]kueue.Admission)
				for _, c := range snapshot.ClusterQueues() {
					for name, w := range c.Workloads {
						if !workload.HasQuotaReservation(w.Obj) {
							t.Errorf("Workload %s is in the cache without a quota reservation", name)
							continue
						}
						gotAssignments[name] = *w.Obj.Status.Admission
					}
				}
				if len(gotAssignments) == 0 {
					gotAssignments = nil
				}
				if diff := cmp.Diff(tc.wantAssignments, gotAssignments); diff != "" {
					t.Errorf("Unexpected assignments (-want,+got):\n%s", diff)
				}
				if diff := cmp.Diff(tc.wantLeft, qManager.Dump(), cmpDump...); diff != "" {
					t.Errorf("Unexpected elements left in the queue (-want,+got):\n%s", diff)
				}
				if diff := cmp.Diff(tc.wantInadmissibleLeft, qManager.DumpInadmissible(), cmpDump...); diff != "" {
					t.Errorf("Unexpected elements left in inadmissible workloads (-want,+got):\n%s", diff)
				}
			})
		}
	}
}

// TestRefillNotTriggeredBySecondPassAdmission covers the second-pass
// exemption: a workload admitted on its second pass already held its quota
// before the cycle, so its admission frees no head slot and must not pop a
// successor. The successor can only enter this cycle through a refill pop, as
// it sits behind a head that is inadmissible at nomination; pods-ready
// tracking stays off so that guard cannot mask the reservation guard.
//
// The second pass requires a TAS shape (a delayed topology assignment), which
// neither schedule-test harness supports in combination with fair sharing.
func TestRefillNotTriggeredBySecondPassAdmission(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{features.FairSharingRefill: true})

	now := time.Now().Truncate(time.Second)
	singleLevelTopology := utiltestingapi.MakeTopology("tas-single-level").
		Levels(corev1.LabelHostname).
		Obj()
	tasFlavor := utiltestingapi.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-single-level").
		Obj()
	provCheck := utiltestingapi.MakeAdmissionCheck("prov-check").
		ControllerName(kueue.ProvisioningRequestControllerName).
		Active(metav1.ConditionTrue).
		Obj()
	clusterQueue := utiltestingapi.MakeClusterQueue("tas-refill").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
			Resource(corev1.ResourceCPU, "6").Obj()).
		AdmissionChecks("prov-check").
		Obj()
	localQueue := utiltestingapi.MakeLocalQueue("tas-refill-lq", "default").
		ClusterQueue("tas-refill").Obj()
	nodes := []corev1.Node{
		*testingnode.MakeNode("x1").
			Label("tas-node", "true").
			Label(corev1.LabelHostname, "x1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("2"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("x2").
			Label("tas-node", "true").
			Label(corev1.LabelHostname, "x2").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("2"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj(),
	}
	pendingTASWl := func(name string, pods int, creation time.Time) *utiltestingapi.WorkloadWrapper {
		return utiltestingapi.MakeWorkload(name, "default").
			Queue("tas-refill-lq").
			Creation(creation).
			PodSets(*utiltestingapi.MakePodSet("one", pods).
				RequiredTopologyRequest(corev1.LabelHostname).
				Request(corev1.ResourceCPU, "1").
				Obj())
	}
	// secondPass holds a 2-CPU reservation with its topology assignment still
	// pending, and all checks ready: the second-pass shape.
	secondPass := pendingTASWl("second-pass", 2, now.Add(-4*time.Minute)).
		ReserveQuotaAt(
			utiltestingapi.MakeAdmission("tas-refill").
				PodSets(utiltestingapi.MakePodSetAssignment("one").
					Assignment(corev1.ResourceCPU, "tas-default", "2000m").
					Count(2).
					DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
					Obj()).
				Obj(),
			now,
		).
		AdmissionCheck(kueue.AdmissionCheckState{
			Name:  "prov-check",
			State: kueue.CheckStateReady,
		}).
		Obj()
	// blocker is inadmissible at nomination (a retrying admission check), so
	// it occupies the head slot without entering the cycle, where it would
	// collide with the second-pass entry: the iterator holds one entry per CQ.
	blocker := pendingTASWl("blocker", 1, now.Add(-3*time.Minute)).
		AdmissionCheck(kueue.AdmissionCheckState{
			Name:  "prov-check",
			State: kueue.CheckStateRetry,
		}).
		Obj()
	// next fits a free node, so a wrongly triggered refill pop would admit it.
	next := pendingTASWl("next", 2, now.Add(-2*time.Minute)).Obj()

	clientBuilder := utiltesting.NewClientBuilder().
		WithLists(
			&kueue.WorkloadList{Items: []kueue.Workload{*secondPass, *blocker, *next}},
			&corev1.NodeList{Items: nodes},
			&kueue.TopologyList{Items: []kueue.Topology{*singleLevelTopology}},
			&kueue.LocalQueueList{Items: []kueue.LocalQueue{*localQueue}}).
		WithObjects(utiltesting.MakeNamespace("default")).
		WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge}).
		WithStatusSubresource(&kueue.Workload{}, &kueue.ClusterQueue{}, &kueue.LocalQueue{})
	_ = tasindexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder))
	cl := clientBuilder.Build()

	recorder := &utiltesting.EventRecorder{}
	cqCache := schdcache.New(cl)
	fakeClock := testingclock.NewFakeClock(now)
	qManager := qcache.NewManagerForUnitTests(cl, cqCache, qcache.WithClock(fakeClock))
	for i := range nodes {
		cqCache.TASCache().SyncNode(&nodes[i])
	}
	cqCache.AddOrUpdateAdmissionCheck(log, provCheck.DeepCopy())
	cqCache.AddOrUpdateResourceFlavor(log, tasFlavor.DeepCopy())
	cqCache.AddOrUpdateTopology(log, singleLevelTopology.DeepCopy())
	if err := cqCache.AddClusterQueue(ctx, clusterQueue.DeepCopy()); err != nil {
		t.Fatalf("Inserting clusterQueue %s in cache: %v", clusterQueue.Name, err)
	}
	if err := qManager.AddClusterQueue(ctx, clusterQueue.DeepCopy()); err != nil {
		t.Fatalf("Inserting clusterQueue %s in manager: %v", clusterQueue.Name, err)
	}
	if err := qManager.AddLocalQueue(ctx, localQueue.DeepCopy()); err != nil {
		t.Fatalf("Inserting queue %s/%s in manager: %v", localQueue.Namespace, localQueue.Name, err)
	}
	// Contributes the reserved usage and queues the second pass.
	cqCache.AddOrUpdateWorkload(log, secondPass.DeepCopy())
	if !qManager.QueueSecondPassIfNeeded(ctx, secondPass, 0) {
		t.Fatal("expected the workload to be queued for a second pass")
	}
	fakeClock.Step(time.Second)

	scheduler := New(qManager, cqCache, cl, recorder,
		WithFairSharing(&config.FairSharing{}),
		WithClock(t, fakeClock),
		WithPreemptionExpectations(preemptexpectations.New()))
	wg := sync.WaitGroup{}
	scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
		func() { wg.Add(1) },
		func() { wg.Done() },
	))

	ctx, cancel := context.WithTimeout(ctx, queueingTimeout)
	defer cancel()
	go cqCache.CleanUpOnContext(ctx)
	go qManager.CleanUpOnContext(ctx)

	scheduler.schedule(ctx)
	wg.Wait()

	// The second pass completed and kept the reservation.
	var gotSecondPass kueue.Workload
	if err := cl.Get(ctx, client.ObjectKeyFromObject(secondPass), &gotSecondPass); err != nil {
		t.Fatalf("Getting the second-pass workload: %v", err)
	}
	if !workload.HasQuotaReservation(&gotSecondPass) {
		t.Fatal("The second-pass workload lost its quota reservation")
	}
	psa := gotSecondPass.Status.Admission.PodSetAssignments[0]
	if psa.TopologyAssignment == nil || psa.DelayedTopologyRequest == nil ||
		*psa.DelayedTopologyRequest != kueue.DelayedTopologyRequestStateReady {
		t.Errorf("The second pass did not complete the delayed topology assignment, got assignment %v, delayed state %v",
			psa.TopologyAssignment, psa.DelayedTopologyRequest)
	}

	wantLeft := map[kueue.ClusterQueueReference][]workload.Reference{
		"tas-refill": {"default/next"},
	}
	if diff := cmp.Diff(wantLeft, qManager.Dump(), cmpDump...); diff != "" {
		t.Errorf("The successor did not stay in the heap (-want,+got):\n%s", diff)
	}
	wantInadmissible := map[kueue.ClusterQueueReference][]workload.Reference{
		"tas-refill": {"default/blocker"},
	}
	if diff := cmp.Diff(wantInadmissible, qManager.DumpInadmissible(), cmpDump...); diff != "" {
		t.Errorf("Unexpected inadmissible workloads (-want,+got):\n%s", diff)
	}
}
