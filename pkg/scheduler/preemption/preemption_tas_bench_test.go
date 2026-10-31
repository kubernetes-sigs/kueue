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
	"fmt"
	"strconv"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	clocktesting "k8s.io/utils/clock/testing"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
)

func BenchmarkTopologyPreemption(b *testing.B) {
	const nodes, slots = 256, 8
	gpu := corev1.ResourceName("nvidia.com/gpu")
	for _, cacheEnabled := range []bool{false, true} {
		b.Run(fmt.Sprintf("cache=%t", cacheEnabled), func(b *testing.B) {
			for _, tc := range []struct {
				name        string
				nominal     int
				scattered   bool
				wantTargets int
			}{
				{name: "ordinary-success", nominal: nodes*slots - slots, wantTargets: slots},
				{name: "retry-success", nominal: nodes*slots - slots, scattered: true, wantTargets: slots},
				{name: "retry-no-legal-victims", nominal: nodes*slots - slots + 1, scattered: true, wantTargets: 0},
			} {
				b.Run(tc.name, func(b *testing.B) {
					features.SetFeatureGateDuringTest(b, features.TopologyAwareScheduling, true)
					features.SetFeatureGateDuringTest(b, features.ClassicalPreemptionCandidateOrderCache, cacheEnabled)
					ctx, log := utiltesting.ContextWithLog(b)
					now := time.Now().Truncate(time.Second)
					admitted := make([]kueue.Workload, 0, nodes*slots)
					for n := range nodes {
						name := fmt.Sprintf("node-%03d", n)
						admission := utiltestingapi.MakeAdmission("borrower").PodSets(
							utiltestingapi.MakePodSetAssignment("main").Count(1).
								Assignment(corev1.ResourceCPU, "tas", "1").
								Assignment(corev1.ResourceMemory, "tas", "1Gi").
								Assignment(gpu, "tas", "1").
								TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
									Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{name}, 1).Obj()).Obj()).Obj()).Obj()
						for s := range slots {
							age := n*slots + s
							if tc.scattered {
								age = s*nodes + n
							}
							wl := utiltestingapi.MakeWorkload(fmt.Sprintf("worker-%03d-%d", n, s), "default").Priority(1).
								PodSets(*utiltestingapi.MakePodSet("main", 1).Request(corev1.ResourceCPU, "1").
									Request(corev1.ResourceMemory, "1Gi").Request(gpu, "1").UnconstrainedTopologyRequest().Obj()).
								ReserveQuotaAt(admission, now.Add(-time.Duration(age)*time.Second)).Obj()
							admitted = append(admitted, *wl)
						}
					}
					cl := utiltesting.NewClientBuilder().WithLists(&kueue.WorkloadList{Items: admitted}).
						WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).Build()
					cache := schdcache.New(cl)
					cache.AddOrUpdateTopology(log, utiltestingapi.MakeTopology("hostname").Levels(corev1.LabelHostname).Obj())
					cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("tas").NodeLabel("pool", "tas").TopologyName("hostname").Obj())
					for n := range nodes {
						name := fmt.Sprintf("node-%03d", n)
						cache.TASCache().SyncNode(testingnode.MakeNode(name).Label("pool", "tas").Label(corev1.LabelHostname, name).
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("8"), corev1.ResourceMemory: resource.MustParse("8Gi"),
								gpu: resource.MustParse("8"), corev1.ResourcePods: resource.MustParse("20"),
							}).Ready().Obj())
					}
					nominal := strconv.Itoa(tc.nominal)
					for _, cq := range []*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("incoming").Cohort("shared").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas").Resource(corev1.ResourceCPU, "8", "0").Resource(corev1.ResourceMemory, "8Gi", "0").Resource(gpu, "8", "0").Obj()).
							Preemption(kueue.ClusterQueuePreemption{ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority}).Obj(),
						utiltestingapi.MakeClusterQueue("borrower").Cohort("shared").
							ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas").Resource(corev1.ResourceCPU, nominal).Resource(corev1.ResourceMemory, nominal+"Gi").Resource(gpu, nominal).Obj()).Obj(),
					} {
						if err := cache.AddClusterQueue(ctx, cq); err != nil {
							b.Fatal(err)
						}
					}
					info := workload.NewInfo(log, utiltestingapi.MakeWorkload("incoming", "default").Priority(2).
						PodSets(*utiltestingapi.MakePodSet("main", 1).Request(corev1.ResourceCPU, "8").Request(corev1.ResourceMemory, "8Gi").Request(gpu, "8").UnconstrainedTopologyRequest().Obj()).
						Obj())
					info.ClusterQueue = "incoming"
					assignment := flavorassigner.Assignment{PodSets: []flavorassigner.PodSetAssignment{
						{
							Name:     "main",
							Count:    1,
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8"), corev1.ResourceMemory: resource.MustParse("8Gi"), gpu: resource.MustParse("8")},
							Flavors: flavorassigner.ResourceAssignment{
								corev1.ResourceCPU:    {Name: "tas", Mode: flavorassigner.Preempt},
								corev1.ResourceMemory: {Name: "tas", Mode: flavorassigner.Preempt},
								gpu:                   {Name: "tas", Mode: flavorassigner.Preempt},
							},
						},
					}}
					snapshot, err := cache.Snapshot(ctx)
					if err != nil {
						b.Fatal(err)
					}
					preemptor := New(cl, workload.Ordering{}, &utiltesting.EventRecorder{}, nil, false, clocktesting.NewFakeClock(now), nil, preemptexpectations.New(), nil)
					// Warm the candidate order cache for repeated attempts on this snapshot.
					preemptor.GetTargetsWithStrategy(ctx, preemptor.GetPreemptionStrategyIterator(ctx, *info, snapshot, assignment))
					b.ReportAllocs()
					b.ResetTimer()
					targetCount := 0
					for range b.N {
						targets := preemptor.GetTargetsWithStrategy(ctx, preemptor.GetPreemptionStrategyIterator(ctx, *info, snapshot, assignment))
						if len(targets) != tc.wantTargets {
							b.Fatalf("got %d targets, want %d", len(targets), tc.wantTargets)
						}
						targetCount += len(targets)
					}
					b.StopTimer()
					b.ReportMetric(float64(targetCount)/float64(b.N), "targets/op")
					b.ReportMetric(nodes, "nodes")
					b.ReportMetric(nodes*slots, "workloads")
				})
			}
		})
	}
}
