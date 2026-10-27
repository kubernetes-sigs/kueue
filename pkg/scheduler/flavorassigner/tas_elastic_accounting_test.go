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

package flavorassigner

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/component-base/featuregate"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestFindTopologyAssignmentsReplacesSliceUsage(t *testing.T) {
	clusterQueue := utiltestingapi.MakeClusterQueue("cq").ResourceGroup(
		*utiltestingapi.MakeFlavorQuotas("flavor-1").Resource(corev1.ResourceCPU, "10").Obj(),
	).Obj()
	flavor := utiltestingapi.MakeResourceFlavor("flavor-1").TopologyName("topology-1").Obj()
	topology := utiltestingapi.MakeTopology("topology-1").Levels(corev1.LabelHostname).Obj()
	node := testingnode.MakeNode("node-1").Label(corev1.LabelHostname, "node-1").
		StatusAllocatable(corev1.ResourceList{
			corev1.ResourceCPU:  resource.MustParse("4"),
			corev1.ResourcePods: resource.MustParse("32"),
		}).Ready().Obj()
	old := utiltestingapi.MakeWorkload("old", "default").UID("old-uid").
		Annotation(constants.ElasticJobAnnotation, "true").PodSets(
		*utiltestingapi.MakePodSet("workers", 2).Request(corev1.ResourceCPU, "1").
			UnconstrainedTopologyRequest().PodSetGroup("group").Obj(),
	).ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(
		utiltestingapi.MakePodSetAssignment("workers").Count(2).
			Assignment(corev1.ResourceCPU, "flavor-1", "2").
			TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
				Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"node-1"}, 2).Obj()).Obj()).Obj(),
	).Obj(), time.Now()).AdmittedAt(true, time.Now())
	incoming := utiltestingapi.MakeWorkload("new", "default").
		Annotation(constants.ElasticJobAnnotation, "true").PodSets(
		*utiltestingapi.MakePodSet("workers", 4).Request(corev1.ResourceCPU, "1").
			UnconstrainedTopologyRequest().PodSetGroup("group").Obj(),
	)
	oldWithLeader := old.Clone().PodSets(
		*utiltestingapi.MakePodSet("workers", 2).Request(corev1.ResourceCPU, "1").
			UnconstrainedTopologyRequest().PodSetGroup("group").Obj(),
		*utiltestingapi.MakePodSet("leader", 1).Request(corev1.ResourceCPU, "1").
			UnconstrainedTopologyRequest().PodSetGroup("group").Obj(),
	).ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(
		utiltestingapi.MakePodSetAssignment("workers").Count(2).
			Assignment(corev1.ResourceCPU, "flavor-1", "2").
			TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
				Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"node-1"}, 2).Obj()).Obj()).Obj(),
		utiltestingapi.MakePodSetAssignment("leader").Count(1).
			Assignment(corev1.ResourceCPU, "flavor-1", "1").
			TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
				Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"node-1"}, 1).Obj()).Obj()).Obj(),
	).Obj(), time.Now()).AdmittedAt(true, time.Now())

	cases := map[string]struct {
		featureGates      map[featuregate.Feature]bool
		clusterQueue      *kueue.ClusterQueue
		resourceFlavor    *kueue.ResourceFlavor
		topology          *kueue.Topology
		node              *corev1.Node
		cachedWorkloads   []*kueue.Workload
		predecessor       *kueue.Workload
		incoming          *kueue.Workload
		podSetAssignments []PodSetAssignment
		removedWorkloads  []workload.Reference
		releasedWorkloads []workload.Reference
		extraUsage        workload.Usage
		wantFailure       bool
	}{
		"accounts for predecessor workers exactly once": {
			clusterQueue:    clusterQueue.DeepCopy(),
			resourceFlavor:  flavor.DeepCopy(),
			topology:        topology.DeepCopy(),
			node:            node.DeepCopy(),
			cachedWorkloads: []*kueue.Workload{old.Clone().Obj()},
			predecessor:     old.Clone().Obj(),
			incoming:        incoming.Clone().Obj(),
			podSetAssignments: []PodSetAssignment{{
				Name: "workers", Count: 4,
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
				Flavors:  ResourceAssignment{corev1.ResourceCPU: {Name: "flavor-1", Mode: Fit}},
			}},
		},
		"accounts for predecessor leader and workers exactly once": {
			clusterQueue:    clusterQueue.DeepCopy(),
			resourceFlavor:  flavor.DeepCopy(),
			topology:        topology.DeepCopy(),
			node:            node.DeepCopy(),
			cachedWorkloads: []*kueue.Workload{oldWithLeader.Clone().Obj()},
			predecessor:     oldWithLeader.Clone().Obj(),
			incoming: incoming.Clone().PodSets(
				*utiltestingapi.MakePodSet("workers", 3).Request(corev1.ResourceCPU, "1").
					UnconstrainedTopologyRequest().PodSetGroup("group").Obj(),
				*utiltestingapi.MakePodSet("leader", 1).Request(corev1.ResourceCPU, "1").
					UnconstrainedTopologyRequest().PodSetGroup("group").Obj(),
			).Obj(),
			podSetAssignments: []PodSetAssignment{
				{
					Name: "workers", Count: 3,
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3")},
					Flavors:  ResourceAssignment{corev1.ResourceCPU: {Name: "flavor-1", Mode: Fit}},
				},
				{
					Name: "leader", Count: 1,
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
					Flavors:  ResourceAssignment{corev1.ResourceCPU: {Name: "flavor-1", Mode: Fit}},
				},
			},
		},
		"does not remove unrelated usage": {
			clusterQueue:    clusterQueue.DeepCopy(),
			resourceFlavor:  flavor.DeepCopy(),
			topology:        topology.DeepCopy(),
			node:            node.DeepCopy(),
			cachedWorkloads: []*kueue.Workload{old.Clone().Obj()},
			predecessor:     old.Clone().Obj(),
			incoming:        incoming.Clone().Obj(),
			podSetAssignments: []PodSetAssignment{{
				Name: "workers", Count: 4,
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
				Flavors:  ResourceAssignment{corev1.ResourceCPU: {Name: "flavor-1", Mode: Fit}},
			}},
			extraUsage: workload.Usage{TAS: workload.TASUsage{
				"flavor-1": {{
					Values: []string{"node-1"}, Count: 1,
					SinglePodRequests: resources.NewRequestsFromResourceList(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}),
				}},
			}},
			wantFailure: true,
		},
		"does not subtract an already removed predecessor": {
			clusterQueue:    clusterQueue.DeepCopy(),
			resourceFlavor:  flavor.DeepCopy(),
			topology:        topology.DeepCopy(),
			node:            node.DeepCopy(),
			cachedWorkloads: []*kueue.Workload{old.Clone().Obj()},
			predecessor:     old.Clone().Obj(),
			incoming:        incoming.Clone().Obj(),
			podSetAssignments: []PodSetAssignment{{
				Name: "workers", Count: 4,
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
				Flavors:  ResourceAssignment{corev1.ResourceCPU: {Name: "flavor-1", Mode: Fit}},
			}},
			removedWorkloads: []workload.Reference{"default/old"},
			extraUsage: workload.Usage{TAS: workload.TASUsage{
				"flavor-1": {{
					Values: []string{"node-1"}, Count: 1,
					SinglePodRequests: resources.NewRequestsFromResourceList(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}),
				}},
			}},
			wantFailure: true,
		},
		"does not subtract usage already released while the predecessor stays present": {
			clusterQueue:    clusterQueue.DeepCopy(),
			resourceFlavor:  flavor.DeepCopy(),
			topology:        topology.DeepCopy(),
			node:            node.DeepCopy(),
			cachedWorkloads: []*kueue.Workload{old.Clone().Obj()},
			predecessor:     old.Clone().Obj(),
			incoming:        incoming.Clone().Obj(),
			podSetAssignments: []PodSetAssignment{{
				Name: "workers", Count: 4,
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
				Flavors:  ResourceAssignment{corev1.ResourceCPU: {Name: "flavor-1", Mode: Fit}},
			}},
			releasedWorkloads: []workload.Reference{"default/old"},
			extraUsage: workload.Usage{TAS: workload.TASUsage{
				"flavor-1": {{
					Values: []string{"node-1"}, Count: 1,
					SinglePodRequests: resources.NewRequestsFromResourceList(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}),
				}},
			}},
			wantFailure: true,
		},
		"does not subtract a different workload with the same name": {
			clusterQueue:    clusterQueue.DeepCopy(),
			resourceFlavor:  flavor.DeepCopy(),
			topology:        topology.DeepCopy(),
			node:            node.DeepCopy(),
			cachedWorkloads: []*kueue.Workload{old.Clone().UID("new-uid").Obj()},
			predecessor:     old.Clone().Obj(),
			incoming:        incoming.Clone().Obj(),
			podSetAssignments: []PodSetAssignment{{
				Name: "workers", Count: 4,
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
				Flavors:  ResourceAssignment{corev1.ResourceCPU: {Name: "flavor-1", Mode: Fit}},
			}},
			wantFailure: true,
		},
		"does not subtract usage without a predecessor": {
			clusterQueue:    clusterQueue.DeepCopy(),
			resourceFlavor:  flavor.DeepCopy(),
			topology:        topology.DeepCopy(),
			node:            node.DeepCopy(),
			cachedWorkloads: []*kueue.Workload{old.Clone().Obj()},
			incoming:        incoming.Clone().Obj(),
			podSetAssignments: []PodSetAssignment{{
				Name: "workers", Count: 4,
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
				Flavors:  ResourceAssignment{corev1.ResourceCPU: {Name: "flavor-1", Mode: Fit}},
			}},
			wantFailure: true,
		},
		"does not subtract predecessor usage when the gate is off": {
			featureGates:    map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlicesWithTAS: false},
			clusterQueue:    clusterQueue.DeepCopy(),
			resourceFlavor:  flavor.DeepCopy(),
			topology:        topology.DeepCopy(),
			node:            node.DeepCopy(),
			cachedWorkloads: []*kueue.Workload{old.Clone().Obj()},
			predecessor:     old.Clone().Obj(),
			incoming:        incoming.Clone().Obj(),
			podSetAssignments: []PodSetAssignment{{
				Name: "workers", Count: 4,
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
				Flavors:  ResourceAssignment{corev1.ResourceCPU: {Name: "flavor-1", Mode: Fit}},
			}},
			wantFailure: true,
		},
		"does not subtract predecessor usage when topology scheduling is off": {
			featureGates:    map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			clusterQueue:    clusterQueue.DeepCopy(),
			resourceFlavor:  flavor.DeepCopy(),
			topology:        topology.DeepCopy(),
			node:            node.DeepCopy(),
			cachedWorkloads: []*kueue.Workload{old.Clone().Obj()},
			predecessor:     old.Clone().Obj(),
			incoming:        incoming.Clone().Obj(),
			podSetAssignments: []PodSetAssignment{{
				Name: "workers", Count: 4,
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
				Flavors:  ResourceAssignment{corev1.ResourceCPU: {Name: "flavor-1", Mode: Fit}},
			}},
			wantFailure: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:             true,
				features.ElasticJobsViaWorkloadSlices:        true,
				features.ElasticJobsViaWorkloadSlicesWithTAS: true,
			})
			ctx, log := utiltesting.ContextWithLog(t)
			cache := schdcache.New(utiltesting.NewFakeClient())
			cache.AddOrUpdateResourceFlavor(log, tc.resourceFlavor)
			cache.AddOrUpdateTopology(log, tc.topology)
			cache.TASCache().SyncNode(tc.node)
			if err := cache.AddClusterQueue(ctx, tc.clusterQueue); err != nil {
				t.Fatalf("adding ClusterQueue: %v", err)
			}
			for _, wl := range tc.cachedWorkloads {
				if !cache.AddOrUpdateWorkload(ctx, log, wl) {
					t.Fatalf("adding cached Workload %s", workload.Key(wl))
				}
			}
			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("building snapshot: %v", err)
			}
			cq := snapshot.ClusterQueue(kueue.ClusterQueueReference(tc.clusterQueue.Name))
			if cq == nil {
				t.Fatal("ClusterQueue missing from snapshot")
			}
			for _, ref := range tc.removedWorkloads {
				info := cq.Workloads[ref]
				if info == nil {
					t.Fatalf("Workload %s missing before removal", ref)
				}
				restore := snapshot.SimulateWorkloadRemoval([]*workload.Info{info})
				defer restore()
			}
			for _, ref := range tc.releasedWorkloads {
				info := cq.Workloads[ref]
				if info == nil {
					t.Fatalf("Workload %s missing before releasing usage", ref)
				}
				snapshot.ReleaseWorkloadUsage(info)
				if cq.Workloads[ref] == nil {
					t.Fatalf("released Workload %s should remain present", ref)
				}
			}
			cq.AddUsage(tc.extraUsage)
			var predecessor *workload.Info
			if tc.predecessor != nil {
				predecessor = workload.NewInfo(log, tc.predecessor)
			}
			incoming := workload.NewInfo(log, tc.incoming)
			assignment := Assignment{replaceWorkloadSlice: predecessor, PodSets: tc.podSetAssignments}
			tasRequests := assignment.WorkloadsTopologyRequests(log, incoming, cq)
			// The disabled-gate cases still need a populated TAS fixture.
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			tasFlavor := cq.TASFlavors[kueue.ResourceFlavorReference(tc.resourceFlavor.Name)]
			before, err := tasFlavor.SerializeFreeCapacityPerDomain()
			if err != nil {
				t.Fatalf("reading TAS capacity: %v", err)
			}
			quotaBefore := cq.ResourceNode.Usage.Clone()
			result := assignment.FindTopologyAssignments(ctx, cq, tasRequests, schdcache.WithWorkload(incoming.Obj))
			if got := result.Failure() != nil; got != tc.wantFailure {
				t.Errorf("topology failure=%v, want %v: %v", got, tc.wantFailure, result.Failure())
			}
			after, err := tasFlavor.SerializeFreeCapacityPerDomain()
			if err != nil {
				t.Fatalf("reading restored TAS capacity: %v", err)
			}
			if before != after {
				t.Errorf("placement changed TAS capacity: before=%s after=%s", before, after)
			}
			if diff := cmp.Diff(quotaBefore, cq.ResourceNode.Usage); diff != "" {
				t.Errorf("placement changed quota usage (-want +got):\n%s", diff)
			}
		})
	}
}
