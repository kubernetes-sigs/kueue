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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/util/routine"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
)

func BenchmarkSchedulerTAS(b *testing.B) {
	cases := []struct {
		// nodes is the total number of nodes in the simulated cluster.
		nodes int
		// nodeGroups is the number of node groups (e.g. racks or zones) the nodes are divided into.
		nodeGroups int
		// requestedPods is the number of pods in the pending workload we try to schedule.
		requestedPods int
		// requestedNodeFractionPercent is the percentage of a single node's capacity
		// (CPU and Memory) that each pod in the requested workload requires.
		// For example, 50 means each pod requires 50% of a node's resources.
		requestedNodeFractionPercent int
		// numResources is the number of resources per node (CPU, Memory, plus mock custom resources).
		numResources int
	}{
		{
			nodes:                        1000,
			nodeGroups:                   8,
			requestedPods:                200,
			requestedNodeFractionPercent: 50,
			numResources:                 30,
		},
	}
	for _, tc := range cases {
		b.Run(fmt.Sprintf("nodes=%d/nodeGroups=%d/pods=%d/res=%d", tc.nodes, tc.nodeGroups, tc.requestedPods, tc.numResources), func(b *testing.B) {
			numNodes := tc.nodes
			numNodeGroups := tc.nodeGroups
			numRequestedPods := tc.requestedPods
			requestedNodeFractionPercent := tc.requestedNodeFractionPercent
			numResources := tc.numResources

			now := time.Now()
			branchingFactor := max(int(math.Sqrt(float64(numNodes))), 1)

			nodes := make([]corev1.Node, numNodes)

			type nodeCap struct {
				cpu int64
				ram int64
			}
			caps := make([]nodeCap, numNodes)

			blockID := 0
			nodeID := 0
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
				// 0 and 1 are CPU/Memory, additional ones are mocked.
				for r := 2; r < numResources; r++ {
					allocatable[corev1.ResourceName(fmt.Sprintf("example.com/res-%d", r))] = resource.MustParse("10")
				}

				nodes[i] = *testingnode.MakeNode(host).
					Label("cloud.com/topology-block", fmt.Sprintf("b-%d", blockID)).
					Label(corev1.LabelHostname, host).
					Label("tas-node", "true").
					Label("node-group", gName).
					StatusAllocatable(allocatable).
					Ready().
					Obj()
				caps[i] = nodeCap{cpu: 10, ram: 100}
			}

			var workloads []kueue.Workload

			// Pre-populate the cluster with admitted workloads until it is full.
			// These workloads will be targets for preemption later.
			wlIdx := 1
			for {
				cpuReq := int64(wlIdx%10) + 1
				ramReq := int64(wlIdx%100) + 1
				podsCount := (wlIdx*10)%int(math.Sqrt(float64(numNodes))) + 1

				podsPlaced := 0
				assignments := make(map[string]int32)
				capsBackup := make([]nodeCap, numNodes)
				copy(capsBackup, caps)

				for j := 0; j < numNodes && podsPlaced < podsCount; j++ {
					for caps[j].cpu >= cpuReq && caps[j].ram >= ramReq && podsPlaced < podsCount {
						caps[j].cpu -= cpuReq
						caps[j].ram -= ramReq
						podsPlaced++
						assignments[nodes[j].Labels[corev1.LabelHostname]]++
					}
				}

				if podsPlaced < podsCount {
					copy(caps, capsBackup)
					break
				}

				levels := []string{corev1.LabelHostname}
				ta := utiltestingapi.MakeTopologyAssignment(levels)
				for h, count := range assignments {
					ta.Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{h}, count).Obj())
				}

				cpuReqStr := strconv.FormatInt(cpuReq, 10)
				ps := utiltestingapi.MakePodSet("main", podsCount).
					Request(corev1.ResourceCPU, cpuReqStr).
					Request(corev1.ResourceMemory, fmt.Sprintf("%dGi", ramReq))
				for r := 2; r < numResources; r++ {
					ps.Request(corev1.ResourceName(fmt.Sprintf("example.com/res-%d", r)), cpuReqStr)
				}

				psa := utiltestingapi.MakePodSetAssignment("main").
					Assignment(corev1.ResourceCPU, "tas-flavor", cpuReqStr).
					Assignment(corev1.ResourceMemory, "tas-flavor", fmt.Sprintf("%dGi", ramReq)).
					TopologyAssignment(ta.Obj())
				for r := 2; r < numResources; r++ {
					psa.Assignment(corev1.ResourceName(fmt.Sprintf("example.com/res-%d", r)), "tas-flavor", cpuReqStr)
				}

				wl := utiltestingapi.MakeWorkload(fmt.Sprintf("wl-%d", wlIdx), "default").
					UID(types.UID(fmt.Sprintf("wl-%d", wlIdx))).
					Queue("tas-main").
					Priority(int32(wlIdx)).
					PodSets(*ps.Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("tas-main").
						PodSets(psa.Obj()).Obj(), now).
					AdmittedAt(true, now).
					Obj()

				workloads = append(workloads, *wl)
				wlIdx++
			}

			// Create a pending workload that requires a fraction of a node's capacity.
			// Since the cluster is full, scheduling this workload will trigger preemption.
			requestedCPU := int64(10 * requestedNodeFractionPercent / 100)
			requestedRAM := int64(100 * requestedNodeFractionPercent / 100)

			requestedCPUStr := strconv.FormatInt(requestedCPU, 10)
			psReq := utiltestingapi.MakePodSet("main", numRequestedPods).
				Request(corev1.ResourceCPU, requestedCPUStr).
				Request(corev1.ResourceMemory, fmt.Sprintf("%dGi", requestedRAM)).
				NodeSelector(map[string]string{"node-group": "group-1"}).
				UnconstrainedTopologyRequest()
			for r := 2; r < numResources; r++ {
				psReq.Request(corev1.ResourceName(fmt.Sprintf("example.com/res-%d", r)), requestedCPUStr)
			}

			requestedWl := utiltestingapi.MakeWorkload("requested-wl", "default").
				UID("requested-wl").
				Queue("tas-main").
				Priority(int32(wlIdx)).
				PodSets(*psReq.Obj()).
				Obj()

			tasTopology := utiltestingapi.MakeTopology("tas-topology").
				Levels("cloud.com/topology-block", corev1.LabelHostname).
				Obj()
			tasFlavor := utiltestingapi.MakeResourceFlavor("tas-flavor").
				NodeLabel("tas-node", "true").
				TopologyName("tas-topology").
				Obj()

			fq := utiltestingapi.MakeFlavorQuotas("tas-flavor").
				Resource(corev1.ResourceCPU, "100000").
				Resource(corev1.ResourceMemory, "1000000Gi")
			for r := 2; r < numResources; r++ {
				fq.Resource(corev1.ResourceName(fmt.Sprintf("example.com/res-%d", r)), "100000")
			}

			cq := utiltestingapi.MakeClusterQueue("tas-main").
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				ResourceGroup(*fq.Obj()).
				Obj()

			lq := utiltestingapi.MakeLocalQueue("tas-main", "default").
				ClusterQueue("tas-main").
				Obj()

			objs := []client.Object{
				utiltesting.MakeNamespaceWrapper("default").Obj(),
				tasTopology,
			}
			for i := range nodes {
				objs = append(objs, &nodes[i])
			}

			ctx, log := utiltesting.ContextWithLog(b)

			for b.Loop() {
				b.StopTimer()
				cb := utiltesting.NewClientBuilder(kueue.AddToScheme, corev1.AddToScheme).
					WithObjects(objs...).
					WithLists(
						&kueue.WorkloadList{Items: workloads},
						&kueue.LocalQueueList{Items: []kueue.LocalQueue{*lq}},
						&kueue.ClusterQueueList{Items: []kueue.ClusterQueue{*cq}},
					).
					WithStatusSubresource(&kueue.Workload{}).
					WithInterceptorFuncs(interceptor.Funcs{
						SubResourcePatch: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
							return nil // discard updates to speed up bench loop
						},
					})
				cl := cb.Build()
				recorder := &utiltesting.EventRecorder{}
				cqCache := schdcache.New(cl)
				expStore := preemptexpectations.New()
				qManager := qcache.NewManagerForUnitTests(cl, cqCache, qcache.WithPreemptionExpectations(expStore))

				cqCache.AddOrUpdateTopology(log, tasTopology)
				cqCache.AddOrUpdateResourceFlavor(log, tasFlavor)
				if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
					b.Fatalf("Failed to add ClusterQueue to cqCache: %v", err)
				}
				if err := qManager.AddClusterQueue(ctx, cq); err != nil {
					b.Fatalf("Failed to add ClusterQueue to qManager: %v", err)
				}
				if err := qManager.AddLocalQueue(ctx, lq); err != nil {
					b.Fatalf("Failed to add LocalQueue to qManager: %v", err)
				}

				for i := range nodes {
					cqCache.TASCache().SyncNode(&nodes[i])
				}
				for i := range workloads {
					// Admitted workloads go to scheduling cache directly
					if !cqCache.AddOrUpdateWorkload(log, &workloads[i]) {
						b.Fatalf("Failed to add workload %s to cqCache", workloads[i].Name)
					}
				}

				scheduler := New(qManager, cqCache, cl, recorder, WithPreemptionExpectations(expStore))
				var wg sync.WaitGroup
				scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
					func() { wg.Add(1) },
					func() { wg.Done() },
				))

				if err := qManager.AddOrUpdateWorkload(log, requestedWl); err != nil {
					b.Fatalf("Failed to add requested workload to qManager: %v", err)
				}

				b.StartTimer()
				scheduler.schedule(ctx)
				b.StopTimer()

				wg.Wait()

				preemptees := sets.New[int]()
				for _, event := range recorder.RecordedEvents {
					if event.Reason == "Preempted" {
						parts := strings.Split(event.Key.String(), "-")
						idx, err := strconv.Atoi(parts[len(parts)-1])
						if err != nil {
							b.Fatalf("Failed to parse workload index from event key %q: %v", event.Key.String(), err)
						}
						preemptees.Insert(idx)
					}
				}
				if preemptees.Len() == 0 {
					b.Fatal("Expected at least one preemptee to be recorded, but found none")
				}
				b.Logf("[BENCH-DEBUG] Preempted workloads: %v", sets.List(preemptees))
				b.StartTimer()
			}
		})
	}
}
