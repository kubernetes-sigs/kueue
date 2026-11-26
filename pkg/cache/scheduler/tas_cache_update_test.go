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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/featuregate"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	coreindexer "sigs.k8s.io/kueue/pkg/controller/core/indexer"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestTASCacheUpdateFlavorTolerationsPreservesUsage(t *testing.T) {
	tasCache := NewTASCache(nil, resources.NewResourceFormatter())
	topology := utiltestingapi.MakeDefaultOneLevelTopology("default")
	tasCache.AddTopology(topology)

	flavor := utiltestingapi.MakeResourceFlavor("tas-flavor").
		NodeLabel("node-group", "tas").
		TopologyName(topology.Name).
		Obj()
	tasCache.AddOrUpdateFlavor(flavor)

	const wlKey = workload.Reference("default/wl")
	topologyRequests := []workload.TopologyDomainRequests{{
		Values: []string{"x1"},
		Count:  1,
		SinglePodRequests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
			corev1.ResourceCPU: 1,
		}),
	}}
	_, log := utiltesting.ContextWithLog(t)
	originalFlavorCache := tasCache.Get("tas-flavor")
	originalFlavorCache.addUsage(log, wlKey, topologyRequests)

	toleration := corev1.Toleration{
		Key:      "example.com/dedicated",
		Operator: corev1.TolerationOpEqual,
		Value:    "tas",
		Effect:   corev1.TaintEffectNoSchedule,
	}
	flavor.Spec.Tolerations = []corev1.Toleration{toleration}
	tasCache.AddOrUpdateFlavor(flavor)

	updatedFlavorCache := tasCache.Get("tas-flavor")
	if updatedFlavorCache != originalFlavorCache {
		t.Fatal("TAS flavor cache was replaced while updating tolerations")
	}

	wantUsage := map[utiltas.TopologyDomainID]resources.Requests{
		"x1": resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
			corev1.ResourceCPU:  1,
			corev1.ResourcePods: 1,
		}),
	}
	if diff := cmp.Diff([]corev1.Toleration{toleration}, updatedFlavorCache.flavor.Tolerations); diff != "" {
		t.Errorf("Unexpected tolerations after update (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantUsage, updatedFlavorCache.usage, cmp.Comparer(resources.Equal)); diff != "" {
		t.Errorf("Unexpected usage after adding toleration (-want +got):\n%s", diff)
	}
	if _, found := updatedFlavorCache.wlUsage[wlKey]; !found {
		t.Error("Workload usage was removed after adding toleration")
	}

	flavor.Spec.Tolerations = nil
	tasCache.AddOrUpdateFlavor(flavor)

	if len(updatedFlavorCache.flavor.Tolerations) != 0 {
		t.Errorf("Expected tolerations to be empty after removal, got %v", updatedFlavorCache.flavor.Tolerations)
	}
	if diff := cmp.Diff(wantUsage, updatedFlavorCache.usage, cmp.Comparer(resources.Equal)); diff != "" {
		t.Errorf("Unexpected usage after removing toleration (-want +got):\n%s", diff)
	}
	if _, found := updatedFlavorCache.wlUsage[wlKey]; !found {
		t.Error("Workload usage was removed after removing toleration")
	}
}

func TestTASCacheUpdateFlavorNodeLabelsPreservesUsage(t *testing.T) {
	tasCache := NewTASCache(nil, resources.NewResourceFormatter())
	topology := utiltestingapi.MakeDefaultOneLevelTopology("default")
	tasCache.AddTopology(topology)

	flavor := utiltestingapi.MakeResourceFlavor("tas-flavor").
		NodeLabel("node-group", "tas").
		TopologyName(topology.Name).
		Obj()
	tasCache.AddOrUpdateFlavor(flavor)

	const wlKey = workload.Reference("default/wl")
	topologyRequests := []workload.TopologyDomainRequests{{
		Values: []string{"x1"},
		Count:  1,
		SinglePodRequests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
			corev1.ResourceCPU: 1,
		}),
	}}
	_, log := utiltesting.ContextWithLog(t)
	originalFlavorCache := tasCache.Get("tas-flavor")
	originalTree, _ := originalFlavorCache.cachedOrBuiltTree()
	originalFlavorCache.addUsage(log, wlKey, topologyRequests)

	updatedNodeLabels := map[string]string{"node-group": "other"}
	flavor.Spec.NodeLabels = updatedNodeLabels
	tasCache.AddOrUpdateFlavor(flavor)

	updatedFlavorCache := tasCache.Get("tas-flavor")
	if updatedFlavorCache != originalFlavorCache {
		t.Fatal("TAS flavor cache was replaced while updating nodeLabels")
	}
	updatedTree, _ := updatedFlavorCache.cachedOrBuiltTree()
	if updatedTree == originalTree {
		t.Error("Topology tree was reused after updating nodeLabels")
	}

	wantUsage := map[utiltas.TopologyDomainID]resources.Requests{
		"x1": resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
			corev1.ResourceCPU:  1,
			corev1.ResourcePods: 1,
		}),
	}
	if diff := cmp.Diff(updatedNodeLabels, updatedFlavorCache.flavor.NodeLabels); diff != "" {
		t.Errorf("Unexpected nodeLabels after update (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantUsage, updatedFlavorCache.usage, cmp.Comparer(resources.Equal)); diff != "" {
		t.Errorf("Unexpected usage after updating nodeLabels (-want +got):\n%s", diff)
	}
	if _, found := updatedFlavorCache.wlUsage[wlKey]; !found {
		t.Error("Workload usage was removed after updating nodeLabels")
	}
}

func TestTASCacheUpdateTopologyLevelsPreservesUsage(t *testing.T) {
	tasCache := NewTASCache(nil, resources.NewResourceFormatter())
	topology := utiltestingapi.MakeTopology("default").
		Levels(utiltesting.DefaultRackTopologyLevel, corev1.LabelHostname).
		Obj()
	tasCache.AddTopology(topology)

	flavor := utiltestingapi.MakeResourceFlavor("tas-flavor").
		NodeLabel("node-group", "tas").
		TopologyName(topology.Name).
		Obj()
	tasCache.AddOrUpdateFlavor(flavor)

	const wlKey = workload.Reference("default/wl")
	topologyRequests := []workload.TopologyDomainRequests{{
		Values: []string{"x1"},
		Count:  1,
		SinglePodRequests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
			corev1.ResourceCPU: 1,
		}),
	}}
	_, log := utiltesting.ContextWithLog(t)
	originalFlavorCache := tasCache.Get("tas-flavor")
	originalTree, _ := originalFlavorCache.cachedOrBuiltTree()
	originalFlavorCache.addUsage(log, wlKey, topologyRequests)

	updatedTopology := utiltestingapi.MakeTopology("default").
		Levels(utiltesting.DefaultBlockTopologyLevel, corev1.LabelHostname).
		Obj()
	tasCache.AddTopology(updatedTopology)

	updatedFlavorCache := tasCache.Get("tas-flavor")
	if updatedFlavorCache != originalFlavorCache {
		t.Fatal("TAS flavor cache was replaced while updating topology levels")
	}
	updatedTree, _ := updatedFlavorCache.cachedOrBuiltTree()
	if updatedTree == originalTree {
		t.Error("Topology tree was reused after updating topology levels")
	}

	wantLevels := []string{utiltesting.DefaultBlockTopologyLevel, corev1.LabelHostname}
	if diff := cmp.Diff(wantLevels, updatedFlavorCache.TopologyLevels()); diff != "" {
		t.Errorf("Unexpected levels after update (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantLevels, updatedTree.levelKeys); diff != "" {
		t.Errorf("Unexpected topology tree levels after update (-want +got):\n%s", diff)
	}
	wantUsage := map[utiltas.TopologyDomainID]resources.Requests{
		"x1": resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
			corev1.ResourceCPU:  1,
			corev1.ResourcePods: 1,
		}),
	}
	if diff := cmp.Diff(wantUsage, updatedFlavorCache.usage, cmp.Comparer(resources.Equal)); diff != "" {
		t.Errorf("Unexpected usage after updating topology levels (-want +got):\n%s", diff)
	}
	if _, found := updatedFlavorCache.wlUsage[wlKey]; !found {
		t.Error("Workload usage was removed after updating topology levels")
	}

	tasCache.AddTopology(updatedTopology)
	resyncedTree, _ := updatedFlavorCache.cachedOrBuiltTree()
	if resyncedTree != updatedTree {
		t.Error("Topology tree was rebuilt after re-applying unchanged topology levels")
	}
}

func TestTASCacheUpdateFlavorTopology(t *testing.T) {
	topoA := utiltestingapi.MakeDefaultOneLevelTopology("topo-a")
	topoB := utiltestingapi.MakeTopology("topo-b").
		Levels(utiltesting.DefaultRackTopologyLevel, corev1.LabelHostname).
		Obj()

	cases := map[string]struct {
		addTopoBBeforeUpdate bool
	}{
		"changing topologyName rebuilds flavor cache when target topology exists": {
			addTopoBBeforeUpdate: true,
		},
		"changing topologyName clears stale cache until target topology is added": {
			addTopoBBeforeUpdate: false,
		},
	}

	wantLevels := []string{utiltesting.DefaultRackTopologyLevel, corev1.LabelHostname}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tasCache := NewTASCache(nil, resources.NewResourceFormatter())
			tasCache.AddTopology(topoA)
			if tc.addTopoBBeforeUpdate {
				tasCache.AddTopology(topoB)
			}

			flavor := utiltestingapi.MakeResourceFlavor("tas-flavor").
				NodeLabel("node-group", "tas").
				TopologyName(topoA.Name).
				Obj()
			tasCache.AddOrUpdateFlavor(flavor)
			originalFlavorCache := tasCache.Get("tas-flavor")
			if originalFlavorCache == nil {
				t.Fatal("expected TASFlavorCache to be created initially")
			}

			updatedFlavor := flavor.DeepCopy()
			updatedFlavor.Spec.TopologyName = new(kueue.TopologyReference(topoB.Name))
			tasCache.AddOrUpdateFlavor(updatedFlavor)

			if !tc.addTopoBBeforeUpdate {
				if got := tasCache.Get("tas-flavor"); got != nil {
					t.Fatalf("expected nil TASFlavorCache before target topology exists, got %+v", got)
				}
				tasCache.AddTopology(topoB)
			}

			got := tasCache.Get("tas-flavor")
			if got == nil {
				t.Fatal("expected TASFlavorCache to exist after update")
			}
			if got == originalFlavorCache {
				t.Fatal("expected TASFlavorCache pointer to be rebuilt when topologyName changes")
			}
			if diff := cmp.Diff(wantLevels, got.TopologyLevels()); diff != "" {
				t.Errorf("Unexpected topology levels (-want +got):\n%s", diff)
			}
		})
	}
}

func TestTASCacheAddOrUpdateFlavorNilTopology(t *testing.T) {
	tasCache := NewTASCache(nil, resources.NewResourceFormatter())
	topo := utiltestingapi.MakeDefaultOneLevelTopology("topo-a")
	tasCache.AddTopology(topo)

	flavor := utiltestingapi.MakeResourceFlavor("tas-flavor").
		NodeLabel("node-group", "tas").
		TopologyName(topo.Name).
		Obj()
	tasCache.AddOrUpdateFlavor(flavor)
	if tasCache.Get("tas-flavor") == nil {
		t.Fatal("expected TASFlavorCache to exist before unsetting topologyName")
	}

	unsetFlavor := flavor.DeepCopy()
	unsetFlavor.Spec.TopologyName = nil
	tasCache.AddOrUpdateFlavor(unsetFlavor)
	if got := tasCache.Get("tas-flavor"); got != nil {
		t.Fatalf("TASCache.Get(%q) = %+v, want nil", "tas-flavor", got)
	}
}

func TestCacheResourceFlavorTopologyMutation(t *testing.T) {
	topoA := utiltestingapi.MakeDefaultOneLevelTopology("topo-a")
	topoB := utiltestingapi.MakeDefaultOneLevelTopology("topo-b")

	otherTASRF := utiltestingapi.MakeResourceFlavor("other-tas").
		NodeLabel("node-group", "other").
		TopologyName(topoA.Name).
		Obj()
	nonTASRF := utiltestingapi.MakeResourceFlavor("non-tas").
		NodeLabel("pool", "plain").
		Obj()

	wl1Assignment := utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
		Assignment(corev1.ResourceCPU, "tas-flavor", "2").
		TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
			Domain(utiltas.TopologyDomainAssignment{Count: 1, Values: []string{"node-a"}}).
			Obj()).
		Obj()
	wl1 := utiltestingapi.MakeWorkload("wl1", "ns").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			Request(corev1.ResourceCPU, "2").
			RequiredTopologyRequest(corev1.LabelHostname).
			Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(wl1Assignment).Obj(), time.Now()).
		Obj()
	wl1Pod := testingpod.MakePod("wl1-pod", "ns").
		NodeName("node-a").
		Request(corev1.ResourceCPU, "2").
		Annotation(kueue.WorkloadAnnotation, wl1.Name).
		Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
		Obj()

	wl2 := utiltestingapi.MakeWorkload("wl2", "ns").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			Request(corev1.ResourceCPU, "1").
			Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(
			utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(corev1.ResourceCPU, "tas-flavor", "1").
				Obj(),
		).Obj(), time.Now()).
		Obj()
	wl2Pod := testingpod.MakePod("wl2-pod", "ns").
		NodeName("node-a").
		Request(corev1.ResourceCPU, "1").
		Annotation(kueue.WorkloadAnnotation, wl2.Name).
		Obj()

	const (
		wantOnlyWl1Capacity = `{"node-a":{"freeCapacity":{"cpu":"4","pods":"10"},"tasUsage":{"cpu":"2","pods":"1"}}}`
		wantWl1AndNonTASCap = `{"node-a":{"freeCapacity":{"cpu":"3","pods":"9"},"tasUsage":{"cpu":"2","pods":"1"}}}`
		tasFlavorName       = kueue.ResourceFlavorReference("tas-flavor")
		otherTASFlavorName  = kueue.ResourceFlavorReference("other-tas")
		clusterQueueName    = kueue.ClusterQueueReference("cq")
	)

	cases := map[string]struct {
		featureGates             map[featuregate.Feature]bool
		extraTopologies          []*kueue.Topology
		initialWorkloads         []*kueue.Workload
		initialTASPods           []*corev1.Pod
		initialNonTASPods        []*corev1.Pod
		flavorUpdates            []*kueue.ResourceFlavor
		wantTASCachePresent      bool
		wantTopology             kueue.TopologyReference
		wantReadinessStatus      metav1.ConditionStatus
		wantReadinessReason      string
		wantCQInactiveInSnapshot bool
		wantIsTASOnly            bool
		wantFlavorInSnapshot     bool
		wantSerializedCapacity   string
	}{
		"unsetting topologyName drops TAS cache and keeps CQ active and workload admitted": {
			initialWorkloads:     []*kueue.Workload{wl1},
			flavorUpdates:        []*kueue.ResourceFlavor{utiltestingapi.MakeResourceFlavor("tas-flavor").NodeLabel("node-group", "tas").Obj()},
			wantTASCachePresent:  false,
			wantReadinessStatus:  metav1.ConditionTrue,
			wantReadinessReason:  kueue.ClusterQueueActiveReasonReady,
			wantIsTASOnly:        false,
			wantFlavorInSnapshot: false,
		},
		"unsetting and re-setting topologyName restores TAS usage with TASResidualPodAccounting disabled": {
			featureGates:     map[featuregate.Feature]bool{features.TASResidualPodAccounting: false},
			initialWorkloads: []*kueue.Workload{wl1},
			initialTASPods:   []*corev1.Pod{wl1Pod},
			flavorUpdates: []*kueue.ResourceFlavor{
				utiltestingapi.MakeResourceFlavor("tas-flavor").NodeLabel("node-group", "tas").Obj(),
				utiltestingapi.MakeResourceFlavor("tas-flavor").NodeLabel("node-group", "tas").TopologyName(topoA.Name).Obj(),
			},
			wantTASCachePresent:    true,
			wantTopology:           kueue.TopologyReference(topoA.Name),
			wantReadinessStatus:    metav1.ConditionTrue,
			wantReadinessReason:    kueue.ClusterQueueActiveReasonReady,
			wantIsTASOnly:          true,
			wantFlavorInSnapshot:   true,
			wantSerializedCapacity: wantOnlyWl1Capacity,
		},
		"unsetting and re-setting topologyName restores TAS usage without double counting when TASResidualPodAccounting enabled": {
			featureGates:     map[featuregate.Feature]bool{features.TASResidualPodAccounting: true},
			initialWorkloads: []*kueue.Workload{wl1},
			initialTASPods:   []*corev1.Pod{wl1Pod},
			flavorUpdates: []*kueue.ResourceFlavor{
				utiltestingapi.MakeResourceFlavor("tas-flavor").NodeLabel("node-group", "tas").Obj(),
				utiltestingapi.MakeResourceFlavor("tas-flavor").NodeLabel("node-group", "tas").TopologyName(topoA.Name).Obj(),
			},
			wantTASCachePresent:    true,
			wantTopology:           kueue.TopologyReference(topoA.Name),
			wantReadinessStatus:    metav1.ConditionTrue,
			wantReadinessReason:    kueue.ClusterQueueActiveReasonReady,
			wantIsTASOnly:          true,
			wantFlavorInSnapshot:   true,
			wantSerializedCapacity: wantOnlyWl1Capacity,
		},
		"re-setting topologyName accounts earlier TAS reservation and bound non-TAS pod": {
			featureGates:      map[featuregate.Feature]bool{features.TASResidualPodAccounting: true},
			initialWorkloads:  []*kueue.Workload{wl1, wl2},
			initialTASPods:    []*corev1.Pod{wl1Pod},
			initialNonTASPods: []*corev1.Pod{wl2Pod},
			flavorUpdates: []*kueue.ResourceFlavor{
				utiltestingapi.MakeResourceFlavor("tas-flavor").NodeLabel("node-group", "tas").Obj(),
				utiltestingapi.MakeResourceFlavor("tas-flavor").NodeLabel("node-group", "tas").TopologyName(topoA.Name).Obj(),
			},
			wantTASCachePresent:    true,
			wantTopology:           kueue.TopologyReference(topoA.Name),
			wantReadinessStatus:    metav1.ConditionTrue,
			wantReadinessReason:    kueue.ClusterQueueActiveReasonReady,
			wantIsTASOnly:          true,
			wantFlavorInSnapshot:   true,
			wantSerializedCapacity: wantWl1AndNonTASCap,
		},
		"direct topologyName change rebuilds flavor cache and resyncs workload usage": {
			extraTopologies:        []*kueue.Topology{topoB},
			initialWorkloads:       []*kueue.Workload{wl1},
			flavorUpdates:          []*kueue.ResourceFlavor{utiltestingapi.MakeResourceFlavor("tas-flavor").NodeLabel("node-group", "tas").TopologyName(topoB.Name).Obj()},
			wantTASCachePresent:    true,
			wantTopology:           kueue.TopologyReference(topoB.Name),
			wantReadinessStatus:    metav1.ConditionTrue,
			wantReadinessReason:    kueue.ClusterQueueActiveReasonReady,
			wantIsTASOnly:          true,
			wantFlavorInSnapshot:   true,
			wantSerializedCapacity: wantOnlyWl1Capacity,
		},
		"direct topologyName change to missing topology marks CQ inactive": {
			initialWorkloads:         []*kueue.Workload{wl1},
			flavorUpdates:            []*kueue.ResourceFlavor{utiltestingapi.MakeResourceFlavor("tas-flavor").NodeLabel("node-group", "tas").TopologyName("missing-topo").Obj()},
			wantTASCachePresent:      false,
			wantReadinessStatus:      metav1.ConditionFalse,
			wantReadinessReason:      kueue.ClusterQueueActiveReasonTopologyNotFound,
			wantCQInactiveInSnapshot: true,
			wantIsTASOnly:            true,
			wantFlavorInSnapshot:     false,
		},
		"unsetting missing topologyName restores Ready": {
			initialWorkloads: []*kueue.Workload{wl1},
			flavorUpdates: []*kueue.ResourceFlavor{
				utiltestingapi.MakeResourceFlavor("tas-flavor").NodeLabel("node-group", "tas").TopologyName("missing-topo").Obj(),
				utiltestingapi.MakeResourceFlavor("tas-flavor").NodeLabel("node-group", "tas").Obj(),
			},
			wantTASCachePresent:  false,
			wantReadinessStatus:  metav1.ConditionTrue,
			wantReadinessReason:  kueue.ClusterQueueActiveReasonReady,
			wantIsTASOnly:        false,
			wantFlavorInSnapshot: false,
		},
		"updating unrelated non-TAS flavor leaves TAS flavor cache untouched": {
			initialWorkloads:       []*kueue.Workload{wl1},
			flavorUpdates:          []*kueue.ResourceFlavor{nonTASRF},
			wantTASCachePresent:    true,
			wantTopology:           kueue.TopologyReference(topoA.Name),
			wantReadinessStatus:    metav1.ConditionTrue,
			wantReadinessReason:    kueue.ClusterQueueActiveReasonReady,
			wantIsTASOnly:          true,
			wantFlavorInSnapshot:   true,
			wantSerializedCapacity: wantOnlyWl1Capacity,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, log := utiltesting.ContextWithLog(t)

			clientBuilder := utiltesting.NewClientBuilder()
			if err := utiltesting.AsIndexer(clientBuilder).IndexField(ctx, &corev1.LimitRange{},
				coreindexer.LimitRangeHasContainerOrPodType, coreindexer.IndexLimitRangeHasContainerOrPodType); err != nil {
				t.Fatalf("setting up LimitRange index: %v", err)
			}
			if err := tasindexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder)); err != nil {
				t.Fatalf("setting up TAS indexes: %v", err)
			}

			c := New(clientBuilder.Build())
			c.AddOrUpdateTopology(log, topoA.DeepCopy())
			for _, topo := range tc.extraTopologies {
				c.AddOrUpdateTopology(log, topo.DeepCopy())
			}

			c.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("tas-flavor").NodeLabel("node-group", "tas").TopologyName(topoA.Name).Obj())
			c.AddOrUpdateResourceFlavor(log, otherTASRF.DeepCopy())
			otherBefore := c.TASCache().Get(otherTASFlavorName)
			if otherBefore == nil {
				t.Fatal("expected other-tas cache entry")
			}

			c.TASCache().SyncNode(testingnode.MakeNode("node-a").
				Label("node-group", "tas").
				Label(corev1.LabelHostname, "node-a").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:  resource.MustParse("4"),
					corev1.ResourcePods: resource.MustParse("10"),
				}).
				Ready().
				Obj())

			cq := utiltestingapi.MakeClusterQueue(string(clusterQueueName)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(string(tasFlavorName)).Resource(corev1.ResourceCPU, "10").Obj()).
				Obj()
			if err := c.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("adding ClusterQueue: %v", err)
			}

			for _, wl := range tc.initialWorkloads {
				if !c.AddOrUpdateWorkload(ctx, log, wl.DeepCopy()) {
					t.Fatalf("expected workload %s to be added to cache", wl.Name)
				}
			}
			for _, pod := range tc.initialTASPods {
				c.TASCache().UpdateTASPodUsage(pod.DeepCopy(), log)
			}
			for _, pod := range tc.initialNonTASPods {
				c.TASCache().UpdateNonTASUsage(pod.DeepCopy(), log)
			}

			for _, rf := range tc.flavorUpdates {
				c.AddOrUpdateResourceFlavor(log, rf.DeepCopy())
			}

			gotFlavorCache := c.TASCache().Get(tasFlavorName)
			if (gotFlavorCache != nil) != tc.wantTASCachePresent {
				t.Fatalf("TASCache().Get(%q) != nil is %t, want %t", tasFlavorName, gotFlavorCache != nil, tc.wantTASCachePresent)
			}
			if tc.wantTASCachePresent && gotFlavorCache.Topology() != tc.wantTopology {
				t.Errorf("TASCache().Get(%q).Topology() = %q, want %q", tasFlavorName, gotFlavorCache.Topology(), tc.wantTopology)
			}
			if c.TASCache().Get(otherTASFlavorName) != otherBefore {
				t.Error("expected unrelated TASFlavorCache pointer to remain unchanged")
			}

			gotStatus, gotReason, _ := c.ClusterQueueReadiness(clusterQueueName)
			if gotStatus != tc.wantReadinessStatus || gotReason != tc.wantReadinessReason {
				t.Errorf("ClusterQueueReadiness() = (%v, %q), want (%v, %q)", gotStatus, gotReason, tc.wantReadinessStatus, tc.wantReadinessReason)
			}
			cqObj := c.hm.ClusterQueue(clusterQueueName)
			if cqObj.isTASOnly() != tc.wantIsTASOnly {
				t.Errorf("cq.isTASOnly() = %t, want %t", cqObj.isTASOnly(), tc.wantIsTASOnly)
			}
			for _, wl := range tc.initialWorkloads {
				if _, ok := cqObj.Workloads[workload.Key(wl)]; !ok {
					t.Errorf("expected workload %s to remain in cq.Workloads", wl.Name)
				}
			}

			snap, err := c.Snapshot(ctx)
			if err != nil {
				t.Fatalf("building snapshot: %v", err)
			}
			if gotInactive := snap.InactiveClusterQueueSets.Has(clusterQueueName); gotInactive != tc.wantCQInactiveInSnapshot {
				t.Fatalf("InactiveClusterQueueSets.Has(%q) = %t, want %t", clusterQueueName, gotInactive, tc.wantCQInactiveInSnapshot)
			}
			if tc.wantCQInactiveInSnapshot {
				return
			}

			cqSnap := snap.ClusterQueue(clusterQueueName)
			if cqSnap.IsTASOnly() != tc.wantIsTASOnly {
				t.Errorf("cqSnapshot.IsTASOnly() = %t, want %t", cqSnap.IsTASOnly(), tc.wantIsTASOnly)
			}
			tasSnap, present := cqSnap.TASFlavors[tasFlavorName]
			if present != tc.wantFlavorInSnapshot {
				t.Fatalf("cqSnapshot.TASFlavors[%q] present = %t, want %t", tasFlavorName, present, tc.wantFlavorInSnapshot)
			}
			if tc.wantFlavorInSnapshot {
				gotCapacity, err := tasSnap.SerializeFreeCapacityPerDomain()
				if err != nil {
					t.Fatalf("serializing free capacity: %v", err)
				}
				if diff := cmp.Diff(tc.wantSerializedCapacity, gotCapacity); diff != "" {
					t.Errorf("Unexpected capacity per domain (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func TestClusterQueueSkipInactiveReasonDuringTASResync(t *testing.T) {
	topoA := utiltestingapi.MakeDefaultOneLevelTopology("topo-a")
	topoB := utiltestingapi.MakeDefaultOneLevelTopology("topo-b")

	cases := map[string]struct {
		metadataSteps []*kueue.ResourceFlavor
	}{
		"unset then re-set skips CQ as TASUsageNotSynced until ensureTASIsSynced": {
			metadataSteps: []*kueue.ResourceFlavor{
				utiltestingapi.MakeResourceFlavor("tas-flavor").NodeLabel("node-group", "tas").Obj(),
				utiltestingapi.MakeResourceFlavor("tas-flavor").NodeLabel("node-group", "tas").TopologyName(topoA.Name).Obj(),
			},
		},
		"direct topology change skips CQ as TASUsageNotSynced until ensureTASIsSynced": {
			metadataSteps: []*kueue.ResourceFlavor{
				utiltestingapi.MakeResourceFlavor("tas-flavor").NodeLabel("node-group", "tas").TopologyName(topoB.Name).Obj(),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder()
			if err := tasindexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder)); err != nil {
				t.Fatalf("setting up TAS indexes: %v", err)
			}

			c := New(clientBuilder.Build())
			c.AddOrUpdateTopology(log, topoA.DeepCopy())
			c.AddOrUpdateTopology(log, topoB.DeepCopy())
			c.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("tas-flavor").NodeLabel("node-group", "tas").TopologyName(topoA.Name).Obj())

			cq := utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-flavor").Resource(corev1.ResourceCPU, "10").Obj()).
				Obj()
			if err := c.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("adding ClusterQueue: %v", err)
			}

			cqObj := c.hm.ClusterQueue("cq")
			for _, stepRF := range tc.metadataSteps {
				cqObj.updateFlavorMetadata(log, map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
					"tas-flavor": stepRF.DeepCopy(),
				})
			}

			if cqObj.isTASSynced {
				t.Fatal("expected cq.isTASSynced == false before ensureTASIsSynced")
			}
			if got := skipInactiveCQReason(cqObj); got != inactiveCQReasonTASUsageNotSynced {
				t.Fatalf("skipInactiveCQReason() = %q, want %q", got, inactiveCQReasonTASUsageNotSynced)
			}

			cqObj.ensureTASIsSynced(log)
			if !cqObj.isTASSynced {
				t.Fatal("expected cq.isTASSynced == true after ensureTASIsSynced")
			}
			if got := skipInactiveCQReason(cqObj); got != "" {
				t.Fatalf("skipInactiveCQReason() after resync = %q, want empty", got)
			}
		})
	}
}
