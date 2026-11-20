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
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
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

func TestPreemptionReclaimsTopologyDomain(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	worker := utiltestingapi.MakeWorkload("worker", "default").Priority(1).
		PodSets(*utiltestingapi.MakePodSet("main", 1).Request(corev1.ResourceCPU, "1").UnconstrainedTopologyRequest().Obj())
	x1Admission := utiltestingapi.MakeAdmission("borrower").PodSets(
		utiltestingapi.MakePodSetAssignment("main").Count(1).
			Assignment(corev1.ResourceCPU, "tas", "1").
			TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
				Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).Obj()).Obj()).Obj()
	x2Admission := utiltestingapi.MakeAdmission("borrower").PodSets(
		utiltestingapi.MakePodSetAssignment("main").Count(1).
			Assignment(corev1.ResourceCPU, "tas", "1").
			TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
				Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x2"}, 1).Obj()).Obj()).Obj()).Obj()

	cases := map[string]struct {
		nominal         string
		oldX1Priority   int32
		oldX2Priority   int32
		evictOld        bool
		mixedResources  bool
		wantTargets     []string
		wantNextTargets []string
	}{
		"newest victims on different nodes cannot free a whole node before reaching nominal": {
			nominal: "2", oldX1Priority: 1, oldX2Priority: 1,
			wantTargets:     []string{"new-x1", "old-x1"},
			wantNextTargets: []string{"new-x2"},
		},
		"skip the node with a higher-priority occupant": {
			nominal: "2", oldX1Priority: 4, oldX2Priority: 1,
			wantTargets: []string{"new-x2", "old-x2"},
		},
		"topology cannot prefer a higher-priority eligible victim over lower-priority victims": {
			nominal: "2", oldX1Priority: 2, oldX2Priority: 1,
		},
		"CPU and memory need preemption while GPU quota fits": {
			nominal: "2", oldX1Priority: 1, oldX2Priority: 1, mixedResources: true,
			wantTargets: []string{"new-x1", "old-x1"},
		},
		"evicted candidates on both nodes remain first": {
			nominal: "2", oldX1Priority: 1, oldX2Priority: 1, evictOld: true,
		},
		"cannot reclaim a whole node without exceeding the borrowed quota": {
			nominal: "3", oldX1Priority: 1, oldX2Priority: 1,
		},
		"neither node can be freed under the priority policy": {
			nominal: "2", oldX1Priority: 4, oldX2Priority: 4,
		},
	}
	for _, cacheEnabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("cache=%t", cacheEnabled), func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ClassicalPreemptionCandidateOrderCache, cacheEnabled)
			for name, tc := range cases {
				t.Run(name, func(t *testing.T) {
					features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
					ctx, log := utiltesting.ContextWithLog(t)
					admitted := []kueue.Workload{
						*worker.Clone().Name("old-x1").Priority(tc.oldX1Priority).ReserveQuotaAt(x1Admission.DeepCopy(), now.Add(-2*time.Hour)).Obj(),
						*worker.Clone().Name("old-x2").Priority(tc.oldX2Priority).ReserveQuotaAt(x2Admission.DeepCopy(), now.Add(-time.Hour)).Obj(),
						*worker.Clone().Name("new-x1").ReserveQuotaAt(x1Admission.DeepCopy(), now.Add(-time.Minute)).Obj(),
						*worker.Clone().Name("new-x2").ReserveQuotaAt(x2Admission.DeepCopy(), now).Obj(),
					}
					if tc.evictOld {
						for i := range admitted[:2] {
							admitted[i] = *(&utiltestingapi.WorkloadWrapper{Workload: admitted[i]}).EvictedAt(now).Obj()
						}
					}

					capacity := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourcePods: resource.MustParse("10")}
					incomingQuota := utiltestingapi.MakeFlavorQuotas("tas").Resource(corev1.ResourceCPU, "2", "0")
					borrowerQuota := utiltestingapi.MakeFlavorQuotas("tas").Resource(corev1.ResourceCPU, tc.nominal)
					incoming := utiltestingapi.MakeWorkload("incoming", "default").Priority(3).
						PodSets(*utiltestingapi.MakePodSet("main", 1).Request(corev1.ResourceCPU, "2").UnconstrainedTopologyRequest().Obj())
					assignment := flavorassigner.Assignment{PodSets: []flavorassigner.PodSetAssignment{{
						Name: "main", Count: 1, Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
						Flavors: flavorassigner.ResourceAssignment{corev1.ResourceCPU: {Name: "tas", Mode: flavorassigner.Preempt}},
					}}}
					if tc.mixedResources {
						for _, r := range []struct {
							name                                corev1.ResourceName
							worker, incoming, capacity, nominal string
							mode                                flavorassigner.FlavorAssignmentMode
						}{
							{corev1.ResourceMemory, "1Gi", "2Gi", "2Gi", "2Gi", flavorassigner.Preempt},
							{"nvidia.com/gpu", "1", "1", "2", "4", flavorassigner.Fit},
						} {
							capacity[r.name] = resource.MustParse(r.capacity)
							incomingQuota.Resource(r.name, r.incoming, "0")
							borrowerQuota.Resource(r.name, r.nominal)
							incoming.Request(r.name, r.incoming)
							assignment.PodSets[0].Requests[r.name] = resource.MustParse(r.incoming)
							assignment.PodSets[0].Flavors[r.name] = &flavorassigner.FlavorAssignment{Name: "tas", Mode: r.mode}
							for i := range admitted {
								(&utiltestingapi.WorkloadWrapper{Workload: admitted[i]}).Request(r.name, r.worker)
								ps := &admitted[i].Status.Admission.PodSetAssignments[0]
								ps.Flavors[r.name] = "tas"
								ps.ResourceUsage[r.name] = resource.MustParse(r.worker)
							}
						}
					}

					cl := utiltesting.NewClientBuilder().WithLists(&kueue.WorkloadList{Items: admitted}).
						WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).Build()
					cache := schdcache.New(cl)
					cache.AddOrUpdateTopology(log, utiltestingapi.MakeTopology("hostname").Levels(corev1.LabelHostname).Obj())
					cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("tas").NodeLabel("pool", "tas").TopologyName("hostname").Obj())
					for _, nodeName := range []string{"x1", "x2"} {
						cache.TASCache().SyncNode(testingnode.MakeNode(nodeName).Label("pool", "tas").Label(corev1.LabelHostname, nodeName).
							StatusAllocatable(capacity).Ready().Obj())
					}
					for _, cq := range []*kueue.ClusterQueue{
						utiltestingapi.MakeClusterQueue("incoming").Cohort("shared").
							ResourceGroup(*incomingQuota.Obj()).
							Preemption(kueue.ClusterQueuePreemption{ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority}).Obj(),
						utiltestingapi.MakeClusterQueue("borrower").Cohort("shared").
							ResourceGroup(*borrowerQuota.Obj()).Obj(),
					} {
						if err := cache.AddClusterQueue(ctx, cq); err != nil {
							t.Fatal(err)
						}
					}
					info := workload.NewInfo(log, incoming.Obj())
					info.ClusterQueue = "incoming"
					snapshot, err := cache.Snapshot(ctx)
					if err != nil {
						t.Fatal(err)
					}
					beforeUsage := maps.Clone(snapshot.ClusterQueue("borrower").ResourceNode.Usage)
					beforeCapacity, err := snapshot.ClusterQueue("incoming").TASFlavors["tas"].SerializeFreeCapacityPerDomain()
					if err != nil {
						t.Fatal(err)
					}

					preemptor := New(cl, workload.Ordering{}, &utiltesting.EventRecorder{}, nil, false, clocktesting.NewFakeClock(now), nil, preemptexpectations.New(), nil)
					targets := preemptor.GetTargetsWithStrategy(ctx, preemptor.GetPreemptionStrategyIterator(ctx, *info, snapshot, assignment))
					var gotTargets []string
					for _, target := range targets {
						gotTargets = append(gotTargets, target.WorkloadInfo.Obj.Name)
					}
					slices.Sort(gotTargets)
					if diff := cmp.Diff(tc.wantTargets, gotTargets, cmpopts.EquateEmpty()); diff != "" {
						t.Errorf("unexpected preemption targets (-want/+got):\n%s", diff)
					}

					if tc.wantNextTargets != nil {
						next := workload.NewInfo(log, incoming.Clone().Name("next").Request(corev1.ResourceCPU, "1").Obj())
						next.ClusterQueue = "incoming"
						nextAssignment := flavorassigner.Assignment{PodSets: []flavorassigner.PodSetAssignment{{
							Name: "main", Count: 1, Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
							Flavors: flavorassigner.ResourceAssignment{corev1.ResourceCPU: {Name: "tas", Mode: flavorassigner.Preempt}},
						}}}
						targets = preemptor.GetTargetsWithStrategy(ctx, preemptor.GetPreemptionStrategyIterator(ctx, *next, snapshot, nextAssignment))
						gotTargets = nil
						for _, target := range targets {
							gotTargets = append(gotTargets, target.WorkloadInfo.Obj.Name)
						}
						if diff := cmp.Diff(tc.wantNextTargets, gotTargets); diff != "" {
							t.Errorf("ordinary search after retry changed victim order (-want/+got):\n%s", diff)
						}
					}

					if diff := cmp.Diff(beforeUsage, snapshot.ClusterQueue("borrower").ResourceNode.Usage); diff != "" {
						t.Errorf("quota snapshot changed (-before/+after):\n%s", diff)
					}
					afterCapacity, err := snapshot.ClusterQueue("incoming").TASFlavors["tas"].SerializeFreeCapacityPerDomain()
					if err != nil {
						t.Fatal(err)
					}
					if diff := cmp.Diff(beforeCapacity, afterCapacity); diff != "" {
						t.Errorf("topology snapshot changed (-before/+after):\n%s", diff)
					}
				})
			}
		})
	}
}
