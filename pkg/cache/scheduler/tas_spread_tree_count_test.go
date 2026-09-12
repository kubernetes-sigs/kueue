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

	"github.com/go-logr/logr/testr"
	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

const spreadCountsTestFlavor kueue.ResourceFlavorReference = "tas-flavor"

// appMainSpreading builds a spec selecting the Workloads these tests label
// app=main, and no others, so a case can add an unrelated Workload to prove it
// is not counted. It goes through NewSpreadingSpec because a hand-built
// literal would carry no compiled selector.
func appMainSpreading(t *testing.T, levelKeys ...string) *utiltas.SpreadingSpec {
	t.Helper()
	rules := make([]utiltas.SpreadingRule, 0, len(levelKeys))
	for _, key := range levelKeys {
		rules = append(rules, utiltas.SpreadingRule{TopologyKey: key})
	}
	spec, err := utiltas.NewSpreadingSpec(
		[]metav1.LabelSelectorRequirement{
			{Key: "app", Operator: metav1.LabelSelectorOpIn, Values: []string{"main"}},
		},
		rules,
		// An explicit selector, so the job-uid default is not in play here.
		"",
	)
	if err != nil {
		t.Fatalf("NewSpreadingSpec() unexpected error: %v", err)
	}
	return spec
}

// spreadKeyForGroupName is the PodSetGroupKey of a PodSet declaring the given
// group name; spreadKeyForPodSetName is the key of a PodSet that declares no
// group, so it falls back to its own name. Both go through the production
// helper, so spreading tests assert on the grouping rather than restating the
// key's string format - and a PodSet name and a group name that happen to be
// equal stay distinct here exactly as they do in production.
func spreadKeyForGroupName(name string) utiltas.PodSetGroupKey {
	return utiltas.GroupKeyForPodSet(&kueue.PodSet{
		TopologyRequest: &kueue.PodSetTopologyRequest{PodSetGroupName: &name},
	})
}

func spreadKeyForPodSetName(name kueue.PodSetReference) utiltas.PodSetGroupKey {
	return utiltas.GroupKeyForPodSet(&kueue.PodSet{Name: name})
}

func TestTopologySpreadCounts(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TASTopologySpreading, true)

	levels := []string{treeTestBlockLabel, treeTestRackLabel, corev1.LabelHostname}
	log := testr.New(t)
	tasFlavor := newTASFlavorSnapshot(log, "topology", newTopologyTree(levels, []*corev1.Node{
		makeTreeTestNode("n1", "b1", "r1"),
		makeTreeTestNode("n2", "b1", "r1"),
		makeTreeTestNode("n3", "b2", "r2"),
	}, 0), nil, newDefaultSimulatorSnapshot())

	incomingObj := utiltestingapi.MakeWorkload("incoming", "ns").
		PodSets(
			*utiltestingapi.MakePodSet("worker-a", 1).PodSetGroup("group-a").Obj(),
			*utiltestingapi.MakePodSet("worker-b", 1).PodSetGroup("group-b").Obj(),
		).
		Obj()
	incoming := workload.NewInfo(log, incomingObj)
	incoming.TopologySpreading = map[utiltas.PodSetGroupKey]*utiltas.SpreadingSpec{
		spreadKeyForGroupName("group-a"): appMainSpreading(t, treeTestBlockLabel, treeTestRackLabel),
		spreadKeyForGroupName("group-b"): appMainSpreading(t, treeTestBlockLabel),
	}

	requests := WorkloadTASRequests{
		spreadCountsTestFlavor: {
			{PodSet: &incomingObj.Spec.PodSets[0], PodSetGroupName: new("group-a")},
			{PodSet: &incomingObj.Spec.PodSets[1], PodSetGroupName: new("group-b")},
		},
	}

	existing := []*kueue.Workload{
		makeSpreadCountsWorkload("wl-1", "ns", "main", []spreadCountsPodSetPlacement{
			{name: "a-1", group: "group-a", node: "n1"},
			{name: "a-2", group: "group-a", node: "n2"},
			{name: "b", group: "group-b", node: "n3"},
		}),
		makeSpreadCountsWorkload("wl-2", "ns", "main", []spreadCountsPodSetPlacement{
			{name: "a", group: "group-a", node: "n2"},
		}),
		makeSpreadCountsWorkload("other-label", "ns", "other", []spreadCountsPodSetPlacement{
			{name: "a", group: "group-a", node: "n3"},
		}),
		makeSpreadCountsWorkload("other-ns", "other-ns", "main", []spreadCountsPodSetPlacement{
			{name: "a", group: "group-a", node: "n3"},
		}),
	}
	cq := &ClusterQueueSnapshot{
		Workloads:  make(map[workload.Reference]*workload.Info, len(existing)),
		TASFlavors: map[kueue.ResourceFlavorReference]*TASFlavorSnapshot{spreadCountsTestFlavor: tasFlavor},
	}
	for _, wl := range existing {
		cq.Workloads[workload.Key(wl)] = workload.NewInfo(log, wl)
	}

	got := cq.topologySpreadCountsForFlavor(incoming, spreadCountsTestFlavor, requests[spreadCountsTestFlavor])
	want := PodSetGroupNameToTreeCount{
		spreadKeyForGroupName("group-a"): {
			Total: 2,
			ByDomain: map[utiltas.TopologyDomainID]int32{
				"b1":    2,
				"b1,r1": 2,
			},
		},
		spreadKeyForGroupName("group-b"): {
			Total:    1,
			ByDomain: map[utiltas.TopologyDomainID]int32{"b2": 1},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("topologySpreadCountsForFlavor() mismatch (-want +got):\n%s", diff)
	}
}

type spreadCountsPodSetPlacement struct {
	name  kueue.PodSetReference
	group string
	// node is the assignment's single value when the assignment names the
	// hostname level; values overrides it for any other level set.
	node   string
	values []string
}

func makeSpreadCountsWorkload(name, namespace, app string, placements []spreadCountsPodSetPlacement) *kueue.Workload {
	return makeSpreadCountsWorkloadAtLevels(name, namespace, app, []string{corev1.LabelHostname}, placements)
}

// makeSpreadCountsWorkloadAtLevels builds an admitted Workload whose
// TopologyAssignment names assignmentLevels, with each placement's values
// taken from spreadCountsPodSetPlacement.values. Which levels an assignment
// names is decided by buildAssignment and differs per Topology - the hostname
// level alone when the Topology declares it, the declared levels when a
// hostname level was injected - so counting has to cope with either.
func makeSpreadCountsWorkloadAtLevels(name, namespace, app string, assignmentLevels []string, placements []spreadCountsPodSetPlacement) *kueue.Workload {
	podSets := make([]kueue.PodSet, 0, len(placements))
	assignments := make([]kueue.PodSetAssignment, 0, len(placements))
	for _, placement := range placements {
		podSets = append(podSets, *utiltestingapi.MakePodSet(placement.name, 1).PodSetGroup(placement.group).Obj())
		values := placement.values
		if len(values) == 0 {
			values = []string{placement.node}
		}
		topologyAssignment := utiltestingapi.MakeTopologyAssignment(assignmentLevels).
			Domain(utiltestingapi.MakeTopologyDomainAssignment(values, 1).Obj()).
			Obj()
		assignments = append(assignments, utiltestingapi.MakePodSetAssignment(placement.name).
			Flavor(corev1.ResourceCPU, spreadCountsTestFlavor).
			TopologyAssignment(topologyAssignment).
			Obj())
	}
	return utiltestingapi.MakeWorkload(name, namespace).
		Label("app", app).
		PodSets(podSets...).
		Admission(utiltestingapi.MakeAdmission("cq").PodSets(assignments...).Obj()).
		Obj()
}

func TestTopologySpreadCountsExcludesSelf(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TASTopologySpreading, true)

	levels := []string{treeTestBlockLabel, treeTestRackLabel, corev1.LabelHostname}
	log := testr.New(t)
	tasFlavor := newTASFlavorSnapshot(log, "topology", newTopologyTree(levels, []*corev1.Node{
		makeTreeTestNode("n1", "b1", "r1"),
		makeTreeTestNode("n3", "b2", "r2"),
	}, 0), nil, newDefaultSimulatorSnapshot())

	// An admitted Workload being re-placed: it matches its own selector and is
	// already in the snapshot with a topology assignment.
	incomingObj := makeSpreadCountsWorkload("incoming", "ns", "main", []spreadCountsPodSetPlacement{
		{name: "a", group: "group-a", node: "n1"},
	})
	incoming := workload.NewInfo(log, incomingObj)
	incoming.TopologySpreading = map[utiltas.PodSetGroupKey]*utiltas.SpreadingSpec{
		spreadKeyForGroupName("group-a"): appMainSpreading(t, treeTestBlockLabel, treeTestRackLabel),
	}

	other := makeSpreadCountsWorkload("wl-1", "ns", "main", []spreadCountsPodSetPlacement{
		{name: "a", group: "group-a", node: "n3"},
	})
	cq := &ClusterQueueSnapshot{
		Workloads: map[workload.Reference]*workload.Info{
			workload.Key(incomingObj): incoming,
			workload.Key(other):       workload.NewInfo(log, other),
		},
		TASFlavors: map[kueue.ResourceFlavorReference]*TASFlavorSnapshot{spreadCountsTestFlavor: tasFlavor},
	}

	requests := WorkloadTASRequests{
		spreadCountsTestFlavor: {
			{PodSet: &incomingObj.Spec.PodSets[0], PodSetGroupName: new("group-a")},
		},
	}

	got := cq.topologySpreadCountsForFlavor(incoming, spreadCountsTestFlavor, requests[spreadCountsTestFlavor])
	want := PodSetGroupNameToTreeCount{
		spreadKeyForGroupName("group-a"): {
			Total: 1,
			ByDomain: map[utiltas.TopologyDomainID]int32{
				"b2":    1,
				"b2,r2": 1,
			},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("topologySpreadCountsForFlavor() mismatch (-want +got):\n%s", diff)
	}
}

// TestTopologySpreadCountsHostnameLessTopology covers a Topology that does not
// declare kubernetes.io/hostname. TASNodeFeasibilityForAllLevels injects a
// virtual hostname leaf into the tree, but buildAssignment publishes the
// assignment rolled up to the declared levels, so the assignment is shallower
// than the tree. Counting must still attribute it, or every domain reads as
// empty and spreading silently stops constraining anything.
func TestTopologySpreadCountsHostnameLessTopology(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TASTopologySpreading, true)
	features.SetFeatureGateDuringTest(t, features.TASNodeFeasibilityForAllLevels, true)

	declaredLevels := []string{treeTestBlockLabel, treeTestRackLabel}
	log := testr.New(t)
	tree := newTopologyTree(declaredLevels, []*corev1.Node{
		makeTreeTestNode("n1", "b1", "r1"),
		makeTreeTestNode("n2", "b1", "r1"),
		makeTreeTestNode("n3", "b2", "r2"),
	}, 0)
	if !tree.virtualHostname {
		t.Fatalf("expected a virtual hostname level to be injected for levels %v", declaredLevels)
	}
	tasFlavor := newTASFlavorSnapshot(log, "topology", tree, nil, newDefaultSimulatorSnapshot())

	incomingObj := utiltestingapi.MakeWorkload("incoming", "ns").
		PodSets(*utiltestingapi.MakePodSet("worker", 1).PodSetGroup("group-a").Obj()).
		Obj()
	incoming := workload.NewInfo(log, incomingObj)
	incoming.TopologySpreading = map[utiltas.PodSetGroupKey]*utiltas.SpreadingSpec{
		spreadKeyForGroupName("group-a"): appMainSpreading(t, treeTestBlockLabel, treeTestRackLabel),
	}

	requests := WorkloadTASRequests{
		spreadCountsTestFlavor: {
			{PodSet: &incomingObj.Spec.PodSets[0], PodSetGroupName: new("group-a")},
		},
	}

	existing := []*kueue.Workload{
		makeSpreadCountsWorkloadAtLevels("wl-1", "ns", "main", declaredLevels, []spreadCountsPodSetPlacement{
			{name: "a", group: "group-a", values: []string{"b1", "r1"}},
		}),
		makeSpreadCountsWorkloadAtLevels("wl-2", "ns", "main", declaredLevels, []spreadCountsPodSetPlacement{
			{name: "a", group: "group-a", values: []string{"b2", "r2"}},
		}),
	}
	cq := &ClusterQueueSnapshot{
		Workloads:  make(map[workload.Reference]*workload.Info, len(existing)),
		TASFlavors: map[kueue.ResourceFlavorReference]*TASFlavorSnapshot{spreadCountsTestFlavor: tasFlavor},
	}
	for _, wl := range existing {
		cq.Workloads[workload.Key(wl)] = workload.NewInfo(log, wl)
	}

	got := cq.topologySpreadCountsForFlavor(incoming, spreadCountsTestFlavor, requests[spreadCountsTestFlavor])
	want := PodSetGroupNameToTreeCount{
		spreadKeyForGroupName("group-a"): {
			Total: 2,
			ByDomain: map[utiltas.TopologyDomainID]int32{
				"b1":    1,
				"b1,r1": 1,
				"b2":    1,
				"b2,r2": 1,
			},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("topologySpreadCountsForFlavor() mismatch (-want +got):\n%s", diff)
	}
}

// TestTopologySpreadCountsHostnameLevelRule covers a rule naming the hostname
// level of a Topology that declares it. A leaf's domain ID is the hostname
// alone, not its full level-values path, so the count has to be keyed the way
// the tree keys the domain - which is what populateSpreadCounts reads back.
func TestTopologySpreadCountsHostnameLevelRule(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TASTopologySpreading, true)

	levels := []string{treeTestBlockLabel, treeTestRackLabel, corev1.LabelHostname}
	log := testr.New(t)
	tasFlavor := newTASFlavorSnapshot(log, "topology", newTopologyTree(levels, []*corev1.Node{
		makeTreeTestNode("n1", "b1", "r1"),
		makeTreeTestNode("n3", "b2", "r2"),
	}, 0), nil, newDefaultSimulatorSnapshot())

	incomingObj := utiltestingapi.MakeWorkload("incoming", "ns").
		PodSets(*utiltestingapi.MakePodSet("worker", 1).PodSetGroup("group-a").Obj()).
		Obj()
	incoming := workload.NewInfo(log, incomingObj)
	incoming.TopologySpreading = map[utiltas.PodSetGroupKey]*utiltas.SpreadingSpec{
		spreadKeyForGroupName("group-a"): appMainSpreading(t, corev1.LabelHostname),
	}

	requests := WorkloadTASRequests{
		spreadCountsTestFlavor: {
			{PodSet: &incomingObj.Spec.PodSets[0], PodSetGroupName: new("group-a")},
		},
	}

	existing := makeSpreadCountsWorkload("wl-1", "ns", "main", []spreadCountsPodSetPlacement{
		{name: "a", group: "group-a", node: "n1"},
	})
	cq := &ClusterQueueSnapshot{
		Workloads: map[workload.Reference]*workload.Info{
			workload.Key(existing): workload.NewInfo(log, existing),
		},
		TASFlavors: map[kueue.ResourceFlavorReference]*TASFlavorSnapshot{spreadCountsTestFlavor: tasFlavor},
	}

	got := cq.topologySpreadCountsForFlavor(incoming, spreadCountsTestFlavor, requests[spreadCountsTestFlavor])
	want := PodSetGroupNameToTreeCount{
		spreadKeyForGroupName("group-a"): {
			Total: 1,
			ByDomain: map[utiltas.TopologyDomainID]int32{
				// The leaf, keyed as the tree keys it, plus its parent rack
				// as the denominator for a hostname-level rule.
				"n1":    1,
				"b1,r1": 1,
			},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("topologySpreadCountsForFlavor() mismatch (-want +got):\n%s", diff)
	}
}
