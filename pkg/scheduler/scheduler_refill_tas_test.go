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
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
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

// TestScheduleForFairSharingRefillTAS pins that a refilled workload's topology
// placement accounts for the capacity earlier admissions in the same cycle
// took. The fixture is a single-level (hostname) topology with 2-CPU nodes and
// quota above the nodes' capacity, so placement rather than quota is the
// discriminating constraint. Each case runs with
// TASRecomputeAssignmentWithinSchedulingCycle on and off: the recompute would
// heal an outdated nomination, so the off arm pins the refill nomination's own
// freshness.
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
		// The head fills node x1 and the successor also needs a whole node,
		// so only x2 is left for it.
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
		// cumulative usage of every admission earlier in the cycle.
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
		// Gate-off contrast for the two-node fixture: the successor is never popped.
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
