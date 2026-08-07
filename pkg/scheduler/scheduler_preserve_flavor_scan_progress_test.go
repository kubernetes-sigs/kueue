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
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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

// preserveProgressCycles is how many scheduling cycles each case drives. Anything above
// two is enough: the behaviour under test is whether the flavor progress recorded in one
// cycle is still usable in the next.
const preserveProgressCycles = 4

// equalTestPriority is shared by both Workloads so that a WithinClusterQueue:
// LowerPriority policy finds no preemption victim.
const equalTestPriority = 10

// TestScheduleForPreserveFlavorScanProgress drives real scheduling cycles to see whether a
// Workload whose TAS placement fails on the flavor that quota selected can ever reach the
// next flavor of its ResourceGroup.
//
// It exists because the two smaller tests for this gate (TestLastAssignmentOutdated and
// TestEntryMarkSkipped) each assert one mechanism in isolation, and neither shows what a
// Workload actually ends up admitted on. Only a multi-cycle run does, because the gate's
// whole purpose is to carry flavor progress from one cycle into the next.
//
// The cases vary two things:
//
//   - the gate, so the change is measured against its own absence;
//   - whether AllocatableResourceGeneration advances between cycles.
//
// The ClusterQueue is fixed at fair sharing with ReclaimWithinCohort: Any and
// WithinClusterQueue: LowerPriority, since that is the configuration this gate was written
// for. Neither policy offers the first flavor a way forward: the two Workloads share a
// priority, so LowerPriority finds no victim in the ClusterQueue, and nothing else in the
// Cohort is borrowing, so ReclaimWithinCohort has nothing to reclaim.
//
// The generation churn is the necessary condition. While it keeps advancing the Workload is
// never admitted with the gate off: every cycle re-nominates the first flavor, the TAS
// recompute reports Preempt, no victim is found, and the Workload is requeued with
// PreemptionNoCandidates. With the gate on it resumes the scan where the previous cycle left
// off and reaches the second flavor. Once the ClusterQueue settles and the generation stops
// advancing, the recorded progress is never discarded, so the Workload escapes the first
// flavor on its own and the gate makes no difference.
func TestScheduleForPreserveFlavorScanProgress(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	singleLevelTopology := *utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level")

	// flavor-1 has a single node that the blocking Workload fully occupies, so the tested
	// Workload's topology never fits there. flavor-2 has room for it.
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
	queues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("tas-lq", "default").ClusterQueue("tas-cq").Obj(),
	}
	// blocker occupies node-f1 entirely; pending needs a whole node and so can only be
	// placed on node-f2. They share a priority so that a LowerPriority policy finds no
	// victim, matching a ClusterQueue whose Workloads all run at the same priority.
	blocker := *utiltestingapi.MakeWorkload("blocker", "default").
		Queue("tas-lq").
		Creation(now.Add(-time.Minute)).
		Priority(equalTestPriority).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			NodeSelector(map[string]string{"tas-flavor": "f1"}).
			RequiredTopologyRequest(corev1.LabelHostname).
			Request(corev1.ResourceCPU, "2").
			Obj()).
		Obj()
	pending := *utiltestingapi.MakeWorkload("pending", "default").
		Queue("tas-lq").
		Creation(now).
		Priority(equalTestPriority).
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			RequiredTopologyRequest(corev1.LabelHostname).
			Request(corev1.ResourceCPU, "2").
			Obj()).
		Obj()

	cases := map[string]struct {
		gateEnabled bool
		// noChurn leaves the ClusterQueue's quotas alone between cycles, so
		// AllocatableResourceGeneration never advances.
		noChurn bool
		// wantPendingFlavor is the flavor "pending" must end up admitted on, or empty if
		// it must remain unadmitted.
		wantPendingFlavor kueue.ResourceFlavorReference
	}{
		"gate disabled": {
			gateEnabled:       false,
			wantPendingFlavor: "",
		},
		"gate enabled": {
			gateEnabled:       true,
			wantPendingFlavor: "tas-flavor-2",
		},
		// Without churn the recorded flavor progress is never discarded, so the Workload
		// escapes the first flavor on its own and the gate makes no difference. This is why
		// an integration spec cannot discriminate between the two gate states: a settled
		// ClusterQueue stops advancing its generation.
		"no generation churn, gate disabled": {
			gateEnabled:       false,
			noChurn:           true,
			wantPendingFlavor: "tas-flavor-2",
		},
		"no generation churn, gate enabled": {
			gateEnabled:       true,
			noChurn:           true,
			wantPendingFlavor: "tas-flavor-2",
		},
	}

	// Quota on each flavor exceeds what its single node can host, so flavor-1 keeps looking
	// admissible to the quota-only flavor selection even after its node is fully occupied.
	// That gap between quota and topology is what can pin a Workload to the first flavor.
	clusterQueue := *utiltestingapi.MakeClusterQueue("tas-cq").
		Preemption(kueue.ClusterQueuePreemption{
			WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
			ReclaimWithinCohort: kueue.PreemptionPolicyAny,
		}).
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("tas-flavor-1").Resource(corev1.ResourceCPU, "4").Obj(),
			*utiltestingapi.MakeFlavorQuotas("tas-flavor-2").Resource(corev1.ResourceCPU, "4").Obj(),
		).
		Obj()

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.PreserveFlavorScanProgress: tc.gateEnabled,
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
			cqCache := schdcache.New(cl, schdcache.WithFairSharing(true))
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
				WithFairSharing(&config.FairSharing{}),
				WithPreemptionExpectations(preemptexpectations.New()))
			wg := sync.WaitGroup{}
			scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
				func() { wg.Add(1) },
				func() { wg.Done() },
			))

			ctx, cancel := context.WithTimeout(ctx, queueingTimeout)
			go qManager.CleanUpOnContext(ctx)
			defer cancel()

			for i := range preserveProgressCycles {
				scheduler.schedule(ctx)
				wg.Wait()
				// Reproduce a busy Cohort. AllocatableResourceGeneration only advances when
				// the quotas actually change (see updateQuotasAndResourceGroups), so flip
				// flavor-2's quota between two values to force a real bump every cycle. The
				// bump is what discards LastAssignment in lastAssignmentOutdated, and with
				// it the flavor progress recorded one cycle earlier. On a cluster with
				// steady admissions and evictions that bump happens continuously, which is
				// the condition this gate exists to survive.
				//
				// flavor-2 is the one flipped so that flavor-1, the flavor under test,
				// keeps a constant quota throughout.
				if !tc.noChurn {
					churned := clusterQueue.DeepCopy()
					churnQuota := "4"
					if i%2 == 0 {
						churnQuota = "5"
					}
					churned.Spec.ResourceGroups[0].Flavors[1].Resources[0].NominalQuota = resource.MustParse(churnQuota)
					if err := cqCache.UpdateClusterQueue(log, churned); err != nil {
						t.Fatalf("Updating clusterQueue in cache: %v", err)
					}
				}
				// Move Workloads parked as inadmissible back into the queue so the next
				// cycle reconsiders them, as the controllers would in a cluster.
				requeuer.ProcessRequeues(ctx)
			}

			if got := admittedFlavorForPodSet(ctx, t, cl, "pending"); got != tc.wantPendingFlavor {
				t.Errorf("workload \"pending\" admitted on flavor %q, want %q", got, tc.wantPendingFlavor)
			}
			// The blocker must keep its place on flavor-1 in every case.
			if got := admittedFlavorForPodSet(ctx, t, cl, "blocker"); got != "tas-flavor-1" {
				t.Errorf("workload \"blocker\" admitted on flavor %q, want %q", got, "tas-flavor-1")
			}
		})
	}
}

// admittedFlavorForPodSet returns the flavor assigned to the Workload's only PodSet
// resource, or an empty reference when the Workload has no quota reservation.
func admittedFlavorForPodSet(ctx context.Context, t *testing.T, cl client.Client, name string) kueue.ResourceFlavorReference {
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
