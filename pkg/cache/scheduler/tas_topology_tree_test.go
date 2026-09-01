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
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	treeTestBlockLabel = "cloud.provider.com/topology-block"
	treeTestRackLabel  = "cloud.provider.com/topology-rack"
)

func makeTreeTestNode(name, block, rack string) *corev1.Node {
	return testingnode.MakeNode(name).
		Label(treeTestBlockLabel, block).
		Label(treeTestRackLabel, rack).
		Label(corev1.LabelHostname, name).
		StatusAllocatable(corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("4"),
			corev1.ResourceMemory: resource.MustParse("16Gi"),
			corev1.ResourcePods:   resource.MustParse("110"),
		}).
		Ready().
		Obj()
}

func treeTestBalancedRequests(name kueue.PodSetReference) FlavorTASRequests {
	preferredLevel := treeTestRackLabel
	return FlavorTASRequests{{
		PodSet: &kueue.PodSet{
			Name: name,
			TopologyRequest: &kueue.PodSetTopologyRequest{
				Preferred: &preferredLevel,
			},
		},
		SinglePodRequests: resources.NewRequestsFromMap(resources.MapRequests{corev1.ResourceCPU: 1000}),
		Count:             6,
	}}
}

func TestNewTopologyTreeCopiesAndRightSizesNodeSlice(t *testing.T) {
	nodes := make([]*corev1.Node, 1, 100)
	nodes[0] = makeTreeTestNode("n1", "b1", "r1")

	tree := newTopologyTree([]string{corev1.LabelHostname}, nodes, 0)
	if got, want := cap(tree.nodes), len(tree.nodes); got != want {
		t.Errorf("tree.nodes capacity = %d, want %d", got, want)
	}

	nodes[0] = nil
	if tree.nodes[0] == nil {
		t.Error("topology tree retained the input node slice")
	}
}

// domainKey identifies a domain in the dumps below. A leaf and root can share
// an ID, so the level is part of the key.
type domainKey struct {
	Level int
	ID    utiltas.TopologyDomainID
}

func domainKeyOf(dom *domain) domainKey {
	return domainKey{Level: len(dom.levelValues) - 1, ID: dom.id}
}

func (k domainKey) compare(other domainKey) int {
	if k.Level != other.Level {
		return k.Level - other.Level
	}
	return strings.Compare(string(k.ID), string(other.ID))
}

// snapshotDomainDump is a comparison representation of one domain of a
// TASFlavorSnapshot, used to verify that snapshots sharing a cached topology
// tree behave exactly like snapshots built from scratch.
type snapshotDomainDump struct {
	Parent       domainKey
	Children     []domainKey
	LevelValues  []string
	Root         bool
	Leaf         bool
	NodeName     string
	FreeCapacity resources.Requests
	TASUsage     resources.Requests
}

func dumpSnapshotTree(t *testing.T, s *TASFlavorSnapshot) map[domainKey]snapshotDomainDump {
	t.Helper()
	perLevelCount := 0
	for _, level := range s.domainsPerLevel {
		perLevelCount += len(level)
	}
	if perLevelCount != s.domainCount {
		t.Errorf("domainsPerLevel holds %d domains, want %d", perLevelCount, s.domainCount)
	}
	dump := make(map[domainKey]snapshotDomainDump, perLevelCount)
	for _, level := range s.domainsPerLevel {
		for id, dom := range level {
			d := snapshotDomainDump{
				LevelValues: dom.levelValues,
				Root:        s.roots[id] == dom,
			}
			if dom.parent != nil {
				d.Parent = domainKeyOf(dom.parent)
			}
			for _, child := range dom.children {
				d.Children = append(d.Children, domainKeyOf(child))
			}
			slices.SortFunc(d.Children, domainKey.compare)
			if leaf, found := s.leaves[id]; found && &leaf.domain == dom {
				d.Leaf = true
				leafCapacity := s.leafCapacityOf(leaf)
				if leafCapacity.freeCapacity != nil {
					d.FreeCapacity = leafCapacity.freeCapacity.Clone()
				}
				if leafCapacity.tasUsage != nil {
					d.TASUsage = leafCapacity.tasUsage.Clone()
				}
				if leaf.node != nil {
					d.NodeName = leaf.node.Name
				}
			}
			dump[domainKeyOf(dom)] = d
		}
	}
	return dump
}

func TestSnapshotWithReusedTreeMatchesColdBuild(t *testing.T) {
	testCases := map[string]struct {
		levels         []string
		tasUsageValues []string
	}{
		"lowest level is hostname": {
			levels:         []string{treeTestBlockLabel, treeTestRackLabel, corev1.LabelHostname},
			tasUsageValues: []string{"b1", "r1", "n1"},
		},
		"lowest level is not hostname": {
			levels:         []string{treeTestBlockLabel, treeTestRackLabel},
			tasUsageValues: []string{"b1", "r1"},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			tasCache := NewTASCache(nil, newDefaultSimulator(), resources.NewResourceFormatter())
			for _, n := range []*corev1.Node{
				makeTreeTestNode("n1", "b1", "r1"),
				makeTreeTestNode("n2", "b1", "r1"),
				makeTreeTestNode("n3", "b1", "r2"),
				makeTreeTestNode("n4", "b2", "r3"),
			} {
				tasCache.SyncNode(n)
			}
			tasCache.UpdateNonTASUsage(testingpod.MakePod("bg-1", "default").
				NodeName("n1").
				Request(corev1.ResourceCPU, "500m").
				StatusPhase(corev1.PodRunning).
				Obj(), log)
			fc := tasCache.NewTASFlavorCache(
				topologyInformation{Levels: tc.levels},
				flavorInformation{TopologyName: "default"},
			)
			fc.addUsage(log, "wl", []workload.TopologyDomainRequests{{
				Values:            tc.tasUsageValues,
				SinglePodRequests: resources.NewRequestsFromMap(resources.MapRequests{corev1.ResourceCPU: 1000}),
				Count:             2,
			}})

			cold, err := fc.snapshot(ctx, log, newDefaultSimulatorSnapshot(), nil)
			if err != nil {
				t.Fatalf("cold snapshot failed: %v", err)
			}
			tree := fc.cachedTree()
			if tree == nil {
				t.Fatal("expected the cold build to store the topology tree")
			}
			reused, err := fc.snapshot(ctx, log, newDefaultSimulatorSnapshot(), nil)
			if err != nil {
				t.Fatalf("second snapshot failed: %v", err)
			}
			if fc.cachedTree() != tree {
				t.Error("expected the second snapshot to reuse the cached tree, but it was rebuilt")
			}
			if reused.topologyTree != tree {
				t.Error("expected the second snapshot to share the cached tree")
			}
			if diff := cmp.Diff(dumpSnapshotTree(t, cold), dumpSnapshotTree(t, reused), cmp.Comparer(resources.Equal)); diff != "" {
				t.Errorf("unexpected difference between cold-built and tree-reusing snapshots (-cold,+reused):\n%s", diff)
			}
		})
	}
}

func TestSnapshotsSharingTreeAreIsolated(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	tasCache := NewTASCache(nil, newDefaultSimulator(), resources.NewResourceFormatter())
	tasCache.SyncNode(makeTreeTestNode("n1", "b1", "r1"))
	tasCache.SyncNode(makeTreeTestNode("n2", "b1", "r2"))
	fc := tasCache.NewTASFlavorCache(
		topologyInformation{Levels: []string{treeTestBlockLabel, treeTestRackLabel, corev1.LabelHostname}},
		flavorInformation{TopologyName: "default"},
	)

	first, err := fc.snapshot(ctx, log, newDefaultSimulatorSnapshot(), nil)
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	second, err := fc.snapshot(ctx, log, newDefaultSimulatorSnapshot(), nil)
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	leafID := utiltas.TopologyDomainID("n1")
	if first.leaves[leafID] != second.leaves[leafID] {
		t.Fatal("expected both snapshots to share the tree's leaf domain")
	}
	firstDump := dumpSnapshotTree(t, first)

	// Mutating one snapshot, as the scheduler does during a cycle, must not
	// leak into snapshots of other cycles.
	second.addTASUsage(leafID, resources.NewRequestsFromMap(resources.MapRequests{corev1.ResourceCPU: 1000}))
	second.addNonTASUsage(leafID, resources.NewRequestsFromMap(resources.MapRequests{corev1.ResourceCPU: 500}))
	third, err := fc.snapshot(ctx, log, newDefaultSimulatorSnapshot(), nil)
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if diff := cmp.Diff(firstDump, dumpSnapshotTree(t, third), cmp.Comparer(resources.Equal)); diff != "" {
		t.Errorf("usage applied to one snapshot leaked into another (-first,+third):\n%s", diff)
	}
}

func TestSnapshotsDoNotShareTreeWhenCachingDisabled(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TASCacheTopologyTree, false)
	ctx, log := utiltesting.ContextWithLog(t)
	tasCache := NewTASCache(nil, newDefaultSimulator(), resources.NewResourceFormatter())
	tasCache.SyncNode(makeTreeTestNode("n1", "b1", "r1"))
	tasCache.SyncNode(makeTreeTestNode("n2", "b1", "r2"))
	fc := tasCache.NewTASFlavorCache(
		topologyInformation{Levels: []string{treeTestBlockLabel, treeTestRackLabel, corev1.LabelHostname}},
		flavorInformation{TopologyName: "default"},
	)

	first, err := fc.snapshot(ctx, log, newDefaultSimulatorSnapshot(), nil)
	if err != nil {
		t.Fatalf("first snapshot failed: %v", err)
	}
	second, err := fc.snapshot(ctx, log, newDefaultSimulatorSnapshot(), nil)
	if err != nil {
		t.Fatalf("second snapshot failed: %v", err)
	}
	if first.topologyTree == second.topologyTree {
		t.Error("snapshots share a topology tree while TASCacheTopologyTree is disabled")
	}
	if fc.cachedTree() != nil {
		t.Error("the flavor cache retained a topology tree while TASCacheTopologyTree is disabled")
	}
}

func TestSnapshotReuseAfterBalancedPlacement(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TASBalancedPlacement, true)
	ctx, log := utiltesting.ContextWithLog(t)
	tasCache := NewTASCache(nil, newDefaultSimulator(), resources.NewResourceFormatter())
	tasCache.SyncNode(makeTreeTestNode("n1", "b1", "r1"))
	tasCache.SyncNode(makeTreeTestNode("n2", "b1", "r2"))
	fc := tasCache.NewTASFlavorCache(
		topologyInformation{Levels: []string{treeTestBlockLabel, treeTestRackLabel, corev1.LabelHostname}},
		flavorInformation{TopologyName: "default"},
	)
	snapshot, err := fc.snapshot(ctx, log, newDefaultSimulatorSnapshot(), nil)
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}

	requests := treeTestBalancedRequests("workers")
	first := snapshot.FindTopologyAssignmentsForFlavor(ctx, requests)
	if failure := first.Failure(); failure != nil {
		t.Fatalf("first assignment failed: %s", failure.Reason)
	}
	if len(snapshot.domainStates) <= snapshot.domainCount {
		t.Fatalf("balanced placement created %d state slots for %d base domains, want clone state", len(snapshot.domainStates), snapshot.domainCount)
	}

	second := snapshot.FindTopologyAssignmentsForFlavor(ctx, requests)
	if failure := second.Failure(); failure != nil {
		t.Fatalf("second assignment failed: %s", failure.Reason)
	}
	if diff := cmp.Diff(first, second); diff != "" {
		t.Errorf("assignment changed after resetting clone state (-first,+second):\n%s", diff)
	}
}

func TestSnapshotsSharingTreeCanAssignConcurrently(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TASBalancedPlacement, true)
	ctx, log := utiltesting.ContextWithLog(t)
	tasCache := NewTASCache(nil, newDefaultSimulator(), resources.NewResourceFormatter())
	tasCache.SyncNode(makeTreeTestNode("n1", "b1", "r1"))
	tasCache.SyncNode(makeTreeTestNode("n2", "b1", "r2"))
	fc := tasCache.NewTASFlavorCache(
		topologyInformation{Levels: []string{treeTestBlockLabel, treeTestRackLabel, corev1.LabelHostname}},
		flavorInformation{TopologyName: "default"},
	)

	snapshots := make([]*TASFlavorSnapshot, 2)
	for i := range snapshots {
		var err error
		snapshots[i], err = fc.snapshot(ctx, log, newDefaultSimulatorSnapshot(), nil)
		if err != nil {
			t.Fatalf("snapshot %d failed: %v", i, err)
		}
	}
	if snapshots[0].topologyTree != snapshots[1].topologyTree {
		t.Fatal("expected snapshots to share the cached topology tree")
	}

	start := make(chan struct{})
	results := make(chan TASAssignmentsResult, len(snapshots))
	for _, snapshot := range snapshots {
		go func(snapshot *TASFlavorSnapshot) {
			<-start
			var result TASAssignmentsResult
			for range 20 {
				result = snapshot.FindTopologyAssignmentsForFlavor(ctx, treeTestBalancedRequests("workers"))
				if result.Failure() != nil {
					break
				}
			}
			results <- result
		}(snapshot)
	}
	close(start)

	got := make([]TASAssignmentsResult, 0, len(snapshots))
	for range snapshots {
		result := <-results
		if failure := result.Failure(); failure != nil {
			t.Fatalf("concurrent assignment failed: %s", failure.Reason)
		}
		got = append(got, result)
	}
	if diff := cmp.Diff(got[0], got[1]); diff != "" {
		t.Errorf("concurrent snapshots produced different assignments (-first,+second):\n%s", diff)
	}
}

func TestTopologyTreeInvalidation(t *testing.T) {
	tests := map[string]struct {
		initialNodeLabels map[string]string
		mutate            func(*tasCache, *TASFlavorCache)
		wantTreeReused    bool
		validate          func(*testing.T, *TASFlavorSnapshot)
	}{
		"heartbeat-only update": {
			mutate: func(tasCache *tasCache, _ *TASFlavorCache) {
				node := makeTreeTestNode("n1", "b1", "r1")
				node.ResourceVersion = "2"
				node.Status.Conditions[0].LastHeartbeatTime = metav1.Now()
				tasCache.SyncNode(node)
			},
			wantTreeReused: true,
		},
		"allocatable change": {
			mutate: func(tasCache *tasCache, _ *TASFlavorCache) {
				node := makeTreeTestNode("n1", "b1", "r1")
				node.Status.Allocatable[corev1.ResourceCPU] = resource.MustParse("8")
				tasCache.SyncNode(node)
			},
			validate: func(t *testing.T, snapshot *TASFlavorSnapshot) {
				n1Capacity := snapshot.leafCapacityOf(snapshot.leaves[utiltas.TopologyDomainID("n1")])
				if gotCapacity := n1Capacity.freeCapacity.ResourceValue(corev1.ResourceCPU); gotCapacity != 8000 {
					t.Errorf("snapshot has cpu capacity %d, want 8000", gotCapacity)
				}
			},
		},
		"node added": {
			mutate: func(tasCache *tasCache, _ *TASFlavorCache) {
				tasCache.SyncNode(makeTreeTestNode("n3", "b3", "r3"))
			},
			validate: func(t *testing.T, snapshot *TASFlavorSnapshot) {
				if _, found := snapshot.domainsPerLevel[0][utiltas.DomainID([]string{"b3"})]; !found {
					t.Error("snapshot does not contain the added node's block domain")
				}
			},
		},
		"node deletion": {
			mutate: func(tasCache *tasCache, _ *TASFlavorCache) {
				tasCache.DeleteNodeByName("n2")
			},
			validate: func(t *testing.T, snapshot *TASFlavorSnapshot) {
				if _, found := snapshot.domainsPerLevel[0][utiltas.DomainID([]string{"b2"})]; found {
					t.Error("snapshot still contains the deleted node's block domain")
				}
			},
		},
		"flavor node selector change": {
			initialNodeLabels: map[string]string{treeTestBlockLabel: "b1"},
			mutate: func(_ *tasCache, fc *TASFlavorCache) {
				fc.updateNodeLabels(map[string]string{treeTestBlockLabel: "b2"})
			},
			validate: func(t *testing.T, snapshot *TASFlavorSnapshot) {
				if _, found := snapshot.leaves[utiltas.TopologyDomainID("n2")]; !found {
					t.Error("snapshot does not contain n2, newly selected by the flavor")
				}
				if _, found := snapshot.leaves[utiltas.TopologyDomainID("n1")]; found {
					t.Error("snapshot still contains n1, no longer selected by the flavor")
				}
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			tasCache := NewTASCache(nil, newDefaultSimulator(), resources.NewResourceFormatter())
			tasCache.SyncNode(makeTreeTestNode("n1", "b1", "r1"))
			tasCache.SyncNode(makeTreeTestNode("n2", "b2", "r2"))
			fc := tasCache.NewTASFlavorCache(
				topologyInformation{Levels: []string{treeTestBlockLabel, treeTestRackLabel, corev1.LabelHostname}},
				flavorInformation{TopologyName: "default", NodeLabels: tc.initialNodeLabels},
			)

			if _, err := fc.snapshot(ctx, log, newDefaultSimulatorSnapshot(), nil); err != nil {
				t.Fatalf("initial snapshot failed: %v", err)
			}
			tree := fc.cachedTree()
			if tree == nil {
				t.Fatal("expected the cold build to store the topology tree")
			}

			tc.mutate(&tasCache, fc)
			snapshot, err := fc.snapshot(ctx, log, newDefaultSimulatorSnapshot(), nil)
			if err != nil {
				t.Fatalf("snapshot after cache mutation failed: %v", err)
			}
			if gotTreeReused := fc.cachedTree() == tree; gotTreeReused != tc.wantTreeReused {
				t.Errorf("cached tree reused = %t, want %t", gotTreeReused, tc.wantTreeReused)
			}
			if tc.validate != nil {
				tc.validate(t, snapshot)
			}
		})
	}
}

type topologyTreeDomainDump struct {
	LevelValues []string
	Parent      domainKey
	Children    []domainKey
	Root        bool
	Leaf        bool
	NodeName    string
	CPUCapacity int64
}

func dumpTopologyTree(tree *topologyTree) map[domainKey]topologyTreeDomainDump {
	dump := make(map[domainKey]topologyTreeDomainDump, tree.domainCount)
	for _, levelDomains := range tree.domainsPerLevel {
		for id, dom := range levelDomains {
			d := topologyTreeDomainDump{
				LevelValues: dom.levelValues,
				Root:        tree.roots[id] == dom,
			}
			if dom.parent != nil {
				d.Parent = domainKeyOf(dom.parent)
			}
			for _, child := range dom.children {
				d.Children = append(d.Children, domainKeyOf(child))
			}
			slices.SortFunc(d.Children, domainKey.compare)
			if leaf, found := tree.leaves[id]; found && &leaf.domain == dom {
				d.Leaf = true
				d.CPUCapacity = leaf.capacity.ResourceValue(corev1.ResourceCPU)
				if leaf.node != nil {
					d.NodeName = leaf.node.Name
				}
			}
			dump[domainKeyOf(dom)] = d
		}
	}
	return dump
}

func validateTopologyTreeStateIndexes(t *testing.T, tree *topologyTree) {
	t.Helper()
	_, log := utiltesting.ContextWithLog(t)
	// Validate each domain's index against the per-snapshot domain state addressed
	// by domain.idx.
	snapshot := newTASFlavorSnapshot(log, "default", tree, nil, newDefaultSimulatorSnapshot())
	seen := make(map[int]*domain, tree.domainCount)
	for _, levelDomains := range tree.domainsPerLevel {
		for _, dom := range levelDomains {
			if dom.idx < 0 || dom.idx >= len(snapshot.domainStates) {
				t.Errorf("domain %q has state index %d, outside [0, %d)", dom.levelValues, dom.idx, len(snapshot.domainStates))
				continue
			}
			if other, found := seen[dom.idx]; found {
				t.Errorf("domains %q and %q share state index %d", other.levelValues, dom.levelValues, dom.idx)
			}
			seen[dom.idx] = dom
		}
	}
	if got := len(seen); got != tree.domainCount {
		t.Errorf("domains with state indexes = %d, want %d", got, tree.domainCount)
	}
}

func TestNewTopologyTree(t *testing.T) {
	tests := map[string]struct {
		levels []string
		nodes  []*corev1.Node
		want   map[domainKey]topologyTreeDomainDump
	}{
		"lowest level is hostname": {
			levels: []string{treeTestBlockLabel, treeTestRackLabel, corev1.LabelHostname},
			nodes: []*corev1.Node{
				makeTreeTestNode("n1", "b1", "r1"),
				makeTreeTestNode("n2", "b1", "r1"),
				makeTreeTestNode("n3", "b1", "r2"),
			},
			want: map[domainKey]topologyTreeDomainDump{
				{Level: 0, ID: "b1"}: {
					LevelValues: []string{"b1"},
					Children:    []domainKey{{Level: 1, ID: "b1,r1"}, {Level: 1, ID: "b1,r2"}},
					Root:        true,
				},
				{Level: 1, ID: "b1,r1"}: {
					LevelValues: []string{"b1", "r1"},
					Parent:      domainKey{Level: 0, ID: "b1"},
					Children:    []domainKey{{Level: 2, ID: "n1"}, {Level: 2, ID: "n2"}},
				},
				{Level: 1, ID: "b1,r2"}: {
					LevelValues: []string{"b1", "r2"},
					Parent:      domainKey{Level: 0, ID: "b1"},
					Children:    []domainKey{{Level: 2, ID: "n3"}},
				},
				{Level: 2, ID: "n1"}: {
					LevelValues: []string{"b1", "r1", "n1"},
					Parent:      domainKey{Level: 1, ID: "b1,r1"},
					Leaf:        true,
					NodeName:    "n1",
					CPUCapacity: 4000,
				},
				{Level: 2, ID: "n2"}: {
					LevelValues: []string{"b1", "r1", "n2"},
					Parent:      domainKey{Level: 1, ID: "b1,r1"},
					Leaf:        true,
					NodeName:    "n2",
					CPUCapacity: 4000,
				},
				{Level: 2, ID: "n3"}: {
					LevelValues: []string{"b1", "r2", "n3"},
					Parent:      domainKey{Level: 1, ID: "b1,r2"},
					Leaf:        true,
					NodeName:    "n3",
					CPUCapacity: 4000,
				},
			},
		},
		"lowest level is not hostname": {
			levels: []string{treeTestBlockLabel, treeTestRackLabel},
			nodes: []*corev1.Node{
				makeTreeTestNode("n1", "b1", "r1"),
				makeTreeTestNode("n2", "b1", "r1"),
				makeTreeTestNode("n3", "b1", "r2"),
			},
			want: map[domainKey]topologyTreeDomainDump{
				{Level: 0, ID: "b1"}: {
					LevelValues: []string{"b1"},
					Children:    []domainKey{{Level: 1, ID: "b1,r1"}, {Level: 1, ID: "b1,r2"}},
					Root:        true,
				},
				{Level: 1, ID: "b1,r1"}: {
					LevelValues: []string{"b1", "r1"},
					Parent:      domainKey{Level: 0, ID: "b1"},
					Leaf:        true,
					CPUCapacity: 8000,
				},
				{Level: 1, ID: "b1,r2"}: {
					LevelValues: []string{"b1", "r2"},
					Parent:      domainKey{Level: 0, ID: "b1"},
					Leaf:        true,
					CPUCapacity: 4000,
				},
			},
		},
		"leaf and root IDs collide": {
			levels: []string{treeTestBlockLabel, corev1.LabelHostname},
			nodes: []*corev1.Node{
				makeTreeTestNode("b1", "b1", "r1"),
			},
			want: map[domainKey]topologyTreeDomainDump{
				{Level: 0, ID: "b1"}: {
					LevelValues: []string{"b1"},
					Children:    []domainKey{{Level: 1, ID: "b1"}},
					Root:        true,
				},
				{Level: 1, ID: "b1"}: {
					LevelValues: []string{"b1", "b1"},
					Parent:      domainKey{Level: 0, ID: "b1"},
					Leaf:        true,
					NodeName:    "b1",
					CPUCapacity: 4000,
				},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			tree := newTopologyTree(tc.levels, tc.nodes, 0)
			validateTopologyTreeStateIndexes(t, tree)
			if diff := cmp.Diff(tc.want, dumpTopologyTree(tree)); diff != "" {
				t.Errorf("unexpected topology tree (-want,+got):\n%s", diff)
			}
		})
	}
}
