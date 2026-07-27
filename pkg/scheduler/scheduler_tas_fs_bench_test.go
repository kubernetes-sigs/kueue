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
	"math"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/util/routine"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
)

// BenchmarkSchedulerTASFairSharing measures a scheduling cycle in which every
// nomination runs a topology assignment and a preemption simulation.
//
// BenchmarkSchedulerTAS uses a single ClusterQueue with no cohort and no Fair
// Sharing, so look-ahead is inert there; BenchmarkSchedulerFairSharing has Fair
// Sharing but nominations that cost microseconds. This fixture is the TAS
// benchmark's cluster re-cut across a cohort with asymmetric quota, which is also
// the only one here that reaches the look-ahead recompute branch in
// updateAssignmentIfNeeded.
func BenchmarkSchedulerTASFairSharing(b *testing.B) {
	cases := []struct {
		name string
		// fillPercent is how much of the cluster the admitted workloads consume.
		// At 100 every nomination must simulate preemption and nothing is admitted
		// outright; below 100 nominations can succeed, which is the only regime
		// where the interrupt can fire.
		fillPercent         int
		nodes               int
		nodeGroups          int
		clusterQueues       int
		pendingPerCQ        int
		podsPerWorkload     int
		nodeFractionPercent int
		numResources        int
	}{
		{name: "packed", fillPercent: 100, nodes: 1000, nodeGroups: 8, clusterQueues: 4, pendingPerCQ: 3, podsPerWorkload: 10, nodeFractionPercent: 50, numResources: 10},
		{name: "spare", fillPercent: 60, nodes: 1000, nodeGroups: 8, clusterQueues: 4, pendingPerCQ: 3, podsPerWorkload: 10, nodeFractionPercent: 50, numResources: 10},
	}

	for _, tc := range cases {
		fixture := makeTASFairSharingFixture(tc.nodes, tc.nodeGroups, tc.clusterQueues,
			tc.pendingPerCQ, tc.podsPerWorkload, tc.nodeFractionPercent, tc.numResources, tc.fillPercent)
		for _, lookAhead := range []bool{false, true} {
			name := fmt.Sprintf("cluster=%s/nodes=%d/cqs=%d/lookAhead=%t",
				tc.name, tc.nodes, tc.clusterQueues, lookAhead)
			b.Run(name, func(b *testing.B) {
				features.SetFeatureGateDuringTest(b, features.FairSharingLookAhead, lookAhead)
				// Look-ahead doubles the number of logged entries, so the test
				// logger's default verbosity would charge the treatment for its own
				// formatting.
				ctx := ctrl.LoggerInto(b.Context(), logr.Discard())
				log := logr.Discard()

				b.ReportAllocs()
				totalAdmits, totalPreemptions := 0, 0
				for b.Loop() {
					b.StopTimer()

					admitted := make([]kueue.Workload, len(fixture.admittedWorkloads))
					for i := range fixture.admittedWorkloads {
						fixture.admittedWorkloads[i].DeepCopyInto(&admitted[i])
					}
					pending := make([]kueue.Workload, len(fixture.pendingWorkloads))
					for i := range fixture.pendingWorkloads {
						fixture.pendingWorkloads[i].DeepCopyInto(&pending[i])
					}

					objs := []client.Object{
						utiltesting.MakeNamespaceWrapper("default").Obj(),
						fixture.topology,
					}
					for i := range fixture.nodes {
						objs = append(objs, &fixture.nodes[i])
					}
					cl := utiltesting.NewClientBuilder(kueue.AddToScheme, corev1.AddToScheme).
						WithObjects(objs...).
						WithLists(
							&kueue.WorkloadList{Items: pending},
							&kueue.LocalQueueList{Items: fixture.localQueues},
							&kueue.ClusterQueueList{Items: fixture.clusterQueues},
						).
						WithStatusSubresource(&kueue.Workload{}).
						WithInterceptorFuncs(interceptor.Funcs{
							SubResourcePatch: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
								return nil // discard status updates to speed up the bench loop
							},
						}).
						Build()

					recorder := &utiltesting.EventRecorder{}
					cqCache := schdcache.New(cl, schdcache.WithFairSharing(true))
					expStore := preemptexpectations.New()
					qManager := qcache.NewManagerForUnitTests(cl, cqCache,
						qcache.WithFairSharing(true),
						qcache.WithPreemptionExpectations(expStore))

					cqCache.AddOrUpdateTopology(log, fixture.topology)
					cqCache.AddOrUpdateResourceFlavor(log, fixture.flavor)
					if err := cqCache.AddOrUpdateCohort(fixture.cohort); err != nil {
						b.Fatalf("Failed to add Cohort to cqCache: %v", err)
					}
					for i := range fixture.clusterQueues {
						if err := cqCache.AddClusterQueue(ctx, &fixture.clusterQueues[i]); err != nil {
							b.Fatalf("Failed to add ClusterQueue to cqCache: %v", err)
						}
						if err := qManager.AddClusterQueue(ctx, &fixture.clusterQueues[i]); err != nil {
							b.Fatalf("Failed to add ClusterQueue to qManager: %v", err)
						}
					}
					for i := range fixture.localQueues {
						if err := qManager.AddLocalQueue(ctx, &fixture.localQueues[i]); err != nil {
							b.Fatalf("Failed to add LocalQueue to qManager: %v", err)
						}
					}
					for i := range fixture.nodes {
						cqCache.TASCache().SyncNode(&fixture.nodes[i])
					}
					for i := range admitted {
						if !cqCache.AddOrUpdateWorkload(log, &admitted[i]) {
							b.Fatalf("Failed to add workload %s to cqCache", admitted[i].Name)
						}
					}
					for i := range pending {
						if err := qManager.AddOrUpdateWorkload(log, &pending[i]); err != nil {
							b.Fatalf("Failed to add workload %s to qManager: %v", pending[i].Name, err)
						}
					}

					scheduler := New(qManager, cqCache, cl, recorder,
						WithFairSharing(&config.FairSharing{}),
						WithPreemptionExpectations(expStore))
					var wg sync.WaitGroup
					scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
						func() { wg.Add(1) },
						func() { wg.Done() },
					))

					runtime.GC()

					b.StartTimer()
					scheduler.schedule(ctx)
					wg.Wait()
					b.StopTimer()

					admits, preemptions := 0, 0
					for _, event := range recorder.RecordedEvents {
						switch event.Reason {
						case "Admitted":
							admits++
						case "Preempted":
							preemptions++
						}
					}
					// A cycle that neither admits nor preempts is measuring an empty pass.
					if admits+preemptions == 0 {
						b.Fatal("Expected at least one admission or preemption per cycle, but found none")
					}
					totalAdmits += admits
					totalPreemptions += preemptions
					b.StartTimer()
				}

				b.ReportMetric(float64(totalAdmits)/float64(b.N), "admits/cycle")
				b.ReportMetric(float64(totalPreemptions)/float64(b.N), "preempts/cycle")
				if totalAdmits > 0 {
					b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(totalAdmits), "ns/admit")
				}
			})
		}
	}
}

type tasFairSharingFixture struct {
	nodes             []corev1.Node
	topology          *kueue.Topology
	flavor            *kueue.ResourceFlavor
	cohort            *kueue.Cohort
	clusterQueues     []kueue.ClusterQueue
	localQueues       []kueue.LocalQueue
	admittedWorkloads []kueue.Workload
	pendingWorkloads  []kueue.Workload
}

// makeTASFairSharingFixture spreads the TAS benchmark's cluster over a cohort of
// ClusterQueues with asymmetric nominal quota, so that half of them borrow and the
// fair-sharing tournament has a real ordering.
func makeTASFairSharingFixture(numNodes, numNodeGroups, numCQs, pendingPerCQ, podsPerWorkload, nodeFractionPercent, numResources, fillPercent int) *tasFairSharingFixture {
	now := time.Now()
	f := &tasFairSharingFixture{}

	branchingFactor := max(int(math.Sqrt(float64(numNodes))), 1)
	f.nodes = make([]corev1.Node, numNodes)

	type nodeCap struct{ cpu, ram int64 }
	caps := make([]nodeCap, numNodes)

	blockID, nodeID := 0, 0
	for i := range numNodes {
		nodeID++
		if nodeID == branchingFactor {
			nodeID = 0
			blockID++
		}
		host := fmt.Sprintf("node-%d-%d", blockID, nodeID)
		gName := fmt.Sprintf("group-%d", blockID%numNodeGroups+1)

		allocatable := corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("10"),
			corev1.ResourceMemory: resource.MustParse("100Gi"),
			corev1.ResourcePods:   resource.MustParse("110"),
		}
		for r := 2; r < numResources; r++ {
			allocatable[corev1.ResourceName(fmt.Sprintf("example.com/res-%d", r))] = resource.MustParse("10")
		}

		f.nodes[i] = *testingnode.MakeNode(host).
			Label("cloud.com/topology-block", fmt.Sprintf("b-%d", blockID)).
			Label(corev1.LabelHostname, host).
			Label("tas-node", "true").
			Label("node-group", gName).
			StatusAllocatable(allocatable).
			Ready().
			Obj()
		caps[i] = nodeCap{cpu: 10, ram: 100}
	}

	f.topology = utiltestingapi.MakeTopology("tas-topology").
		Levels("cloud.com/topology-block", corev1.LabelHostname).
		Obj()
	f.flavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
		NodeLabel("tas-node", "true").
		TopologyName("tas-topology").
		Obj()
	f.cohort = utiltestingapi.MakeCohort("root").Obj()

	for c := range numCQs {
		cqName := fmt.Sprintf("cq-%d", c)
		lqName := fmt.Sprintf("lq-%d", c)
		// DominantResourceShare stays zero while a queue is inside its own quota,
		// which would degenerate the tournament into FIFO.
		nominalCPU, nominalMem, nominalExtra := "100000", "1000000Gi", "100000"
		if c%2 == 1 {
			nominalCPU, nominalMem, nominalExtra = "1", "1Gi", "1"
		}
		fq := utiltestingapi.MakeFlavorQuotas("tas-flavor").
			Resource(corev1.ResourceCPU, nominalCPU).
			Resource(corev1.ResourceMemory, nominalMem)
		for r := 2; r < numResources; r++ {
			fq.Resource(corev1.ResourceName(fmt.Sprintf("example.com/res-%d", r)), nominalExtra)
		}

		f.clusterQueues = append(f.clusterQueues, *utiltestingapi.MakeClusterQueue(cqName).
			FairWeight(resource.MustParse("1")).
			Cohort("root").
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
			}).
			ResourceGroup(*fq.Obj()).
			Obj())

		f.localQueues = append(f.localQueues, *utiltestingapi.MakeLocalQueue(lqName, "default").
			ClusterQueue(cqName).Obj())
	}

	// These become the preemption candidates that every pending nomination has to
	// simulate evicting.
	wlIdx := 1
	consumedCPU, targetCPU := int64(0), int64(numNodes)*10*int64(fillPercent)/100
	for {
		cpuReq := int64(wlIdx%10) + 1
		ramReq := int64(wlIdx%100) + 1
		podsCount := (wlIdx*10)%branchingFactor + 1

		if consumedCPU+cpuReq*int64(podsCount) > targetCPU {
			break
		}

		podsPlaced := 0
		assignments := make(map[string]int32)
		capsBackup := make([]nodeCap, numNodes)
		copy(capsBackup, caps)

		for j := 0; j < numNodes && podsPlaced < podsCount; j++ {
			for caps[j].cpu >= cpuReq && caps[j].ram >= ramReq && podsPlaced < podsCount {
				caps[j].cpu -= cpuReq
				caps[j].ram -= ramReq
				podsPlaced++
				assignments[f.nodes[j].Labels[corev1.LabelHostname]]++
			}
		}
		if podsPlaced < podsCount {
			copy(caps, capsBackup)
			break
		}

		consumedCPU += cpuReq * int64(podsCount)
		cqIdx := wlIdx % numCQs
		cqName := fmt.Sprintf("cq-%d", cqIdx)
		lqName := fmt.Sprintf("lq-%d", cqIdx)

		ta := utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname})
		for h, count := range assignments {
			ta.Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{h}, count).Obj())
		}

		cpuReqStr := strconv.FormatInt(cpuReq, 10)
		ps := utiltestingapi.MakePodSet("main", podsCount).
			Request(corev1.ResourceCPU, cpuReqStr).
			Request(corev1.ResourceMemory, fmt.Sprintf("%dGi", ramReq))
		psa := utiltestingapi.MakePodSetAssignment("main").
			Assignment(corev1.ResourceCPU, "tas-flavor", cpuReqStr).
			Assignment(corev1.ResourceMemory, "tas-flavor", fmt.Sprintf("%dGi", ramReq)).
			TopologyAssignment(ta.Obj())
		for r := 2; r < numResources; r++ {
			res := corev1.ResourceName(fmt.Sprintf("example.com/res-%d", r))
			ps.Request(res, cpuReqStr)
			psa.Assignment(res, "tas-flavor", cpuReqStr)
		}

		name := fmt.Sprintf("wl-%d", wlIdx)
		f.admittedWorkloads = append(f.admittedWorkloads, *utiltestingapi.MakeWorkload(name, "default").
			UID(types.UID(name)).
			Queue(kueue.LocalQueueName(lqName)).
			Priority(int32(wlIdx)).
			PodSets(*ps.Obj()).
			ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(cqName)).
				PodSets(psa.Obj()).Obj(), now).
			AdmittedAt(true, now).
			Obj())
		wlIdx++
	}

	// Several per ClusterQueue so a depth-2 pop has something to pop; distinct
	// creation timestamps keep the FIFO tiebreak from being decided by map order.
	requestedCPU := strconv.FormatInt(int64(10*nodeFractionPercent/100), 10)
	requestedRAM := fmt.Sprintf("%dGi", 100*nodeFractionPercent/100)
	created := 0
	for c := range numCQs {
		lqName := fmt.Sprintf("lq-%d", c)
		for range pendingPerCQ {
			created++
			psReq := utiltestingapi.MakePodSet("main", podsPerWorkload).
				Request(corev1.ResourceCPU, requestedCPU).
				Request(corev1.ResourceMemory, requestedRAM).
				NodeSelector(map[string]string{"node-group": "group-1"}).
				UnconstrainedTopologyRequest()
			for r := 2; r < numResources; r++ {
				psReq.Request(corev1.ResourceName(fmt.Sprintf("example.com/res-%d", r)), requestedCPU)
			}

			name := fmt.Sprintf("pending-%d-%d", c, created)
			f.pendingWorkloads = append(f.pendingWorkloads, *utiltestingapi.MakeWorkload(name, "default").
				UID(types.UID(name)).
				Queue(kueue.LocalQueueName(lqName)).
				// Above every admitted workload, so preemption is possible.
				Priority(int32(wlIdx + created)).
				PodSets(*psReq.Obj()).
				Creation(now.Add(time.Duration(created) * time.Millisecond)).
				Obj())
		}
	}

	return f
}
