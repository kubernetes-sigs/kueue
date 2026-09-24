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
	tasFlavor := newTASFlavorSnapshot(log, flavorInformation{TopologyName: "topology"}, newTopologyTree(levels, []*corev1.Node{
		makeTreeTestNode("n1", "b1", "r1"),
		makeTreeTestNode("n2", "b1", "r1"),
		makeTreeTestNode("n3", "b2", "r2"),
	}, 0), newDefaultSimulator())

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
	tasFlavor := newTASFlavorSnapshot(log, flavorInformation{TopologyName: "topology"}, newTopologyTree(levels, []*corev1.Node{
		makeTreeTestNode("n1", "b1", "r1"),
		makeTreeTestNode("n3", "b2", "r2"),
	}, 0), newDefaultSimulator())

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
	tasFlavor := newTASFlavorSnapshot(log, flavorInformation{TopologyName: "topology"}, tree, newDefaultSimulator())

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
	tasFlavor := newTASFlavorSnapshot(log, flavorInformation{TopologyName: "topology"}, newTopologyTree(levels, []*corev1.Node{
		makeTreeTestNode("n1", "b1", "r1"),
		makeTreeTestNode("n3", "b2", "r2"),
	}, 0), newDefaultSimulator())

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

// makeSpreadCountsWorkloadInFlavor is makeSpreadCountsWorkload admitted to a
// flavor other than the one being counted.
func makeSpreadCountsWorkloadInFlavor(name, namespace, app string, flavor kueue.ResourceFlavorReference, placements []spreadCountsPodSetPlacement) *kueue.Workload {
	wl := makeSpreadCountsWorkload(name, namespace, app, placements)
	for i := range wl.Status.Admission.PodSetAssignments {
		wl.Status.Admission.PodSetAssignments[i].Flavors = map[corev1.ResourceName]kueue.ResourceFlavorReference{
			corev1.ResourceCPU: flavor,
		}
	}
	return wl
}

// TestTopologySpreadCountsSkipped covers the inputs that make counting bail
// out or skip a PodSet: either spreading does not apply at all, and the result
// is nil, or it applies but nothing an existing Workload holds can be
// attributed to a rule's domain, and the group's counts stay empty.
func TestTopologySpreadCountsSkipped(t *testing.T) {
	levels := []string{treeTestBlockLabel, treeTestRackLabel, corev1.LabelHostname}
	nodes := []*corev1.Node{
		makeTreeTestNode("n1", "b1", "r1"),
		makeTreeTestNode("n3", "b2", "r2"),
	}
	// A group whose counts exist but stay at zero, so a skipped PodSet is
	// distinguishable from spreading not applying at all.
	emptyCounts := PodSetGroupNameToTreeCount{
		spreadKeyForGroupName("group-a"): {ByDomain: map[utiltas.TopologyDomainID]int32{}},
	}

	cases := map[string]struct {
		gateOff bool
		// nilWorkload and noSpreadingSpec make spreading inapplicable to the
		// incoming Workload; unknownFlavor and requestWithoutSpec make it
		// inapplicable to the flavor or the PodSets asking for it.
		nilWorkload        bool
		noSpreadingSpec    bool
		unknownFlavor      bool
		requestWithoutSpec bool

		// ruleKeys defaults to the rack level when empty.
		ruleKeys []string
		existing []*kueue.Workload

		want PodSetGroupNameToTreeCount
	}{
		"feature gate disabled": {
			gateOff: true,
			want:    nil,
		},
		"no incoming Workload": {
			nilWorkload: true,
			want:        nil,
		},
		"incoming Workload carries no spreading spec": {
			noSpreadingSpec: true,
			want:            nil,
		},
		"flavor is not a TAS flavor": {
			unknownFlavor: true,
			want:          nil,
		},
		"no PodSet requesting the flavor carries a spreading spec": {
			requestWithoutSpec: true,
			want:               nil,
		},
		"existing Workload is not admitted": {
			existing: []*kueue.Workload{
				utiltestingapi.MakeWorkload("wl-1", "ns").
					Label("app", "main").
					PodSets(*utiltestingapi.MakePodSet("a", 1).PodSetGroup("group-a").Obj()).
					Obj(),
			},
			want: emptyCounts,
		},
		"existing Workload is admitted to another flavor": {
			existing: []*kueue.Workload{
				makeSpreadCountsWorkloadInFlavor("wl-1", "ns", "main", "other-flavor", []spreadCountsPodSetPlacement{
					{name: "a", group: "group-a", node: "n1"},
				}),
			},
			want: emptyCounts,
		},
		"existing Workload holds a domain absent from the flavor topology": {
			existing: []*kueue.Workload{
				makeSpreadCountsWorkload("wl-1", "ns", "main", []spreadCountsPodSetPlacement{
					{name: "a", group: "group-a", node: "deleted-node"},
				}),
			},
			want: emptyCounts,
		},
		"rule names a level absent from the flavor topology": {
			ruleKeys: []string{"cloud.provider.com/topology-zone"},
			existing: []*kueue.Workload{
				makeSpreadCountsWorkload("wl-1", "ns", "main", []spreadCountsPodSetPlacement{
					{name: "a", group: "group-a", node: "n1"},
				}),
			},
			want: emptyCounts,
		},
		// Placed above the rule's level, the Workload spans every domain there
		// and pins none of them.
		"existing Workload is placed above the rule's level": {
			ruleKeys: []string{treeTestRackLabel},
			existing: []*kueue.Workload{
				makeSpreadCountsWorkloadAtLevels("wl-1", "ns", "main", []string{treeTestBlockLabel}, []spreadCountsPodSetPlacement{
					{name: "a", group: "group-a", values: []string{"b1"}},
				}),
			},
			want: emptyCounts,
		},
		// A PodSet of a different group is not counted towards this one.
		"existing Workload belongs to another PodSet group": {
			existing: []*kueue.Workload{
				makeSpreadCountsWorkload("wl-1", "ns", "main", []spreadCountsPodSetPlacement{
					{name: "a", group: "group-b", node: "n1"},
				}),
			},
			want: emptyCounts,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TASTopologySpreading, !tc.gateOff)

			log := testr.New(t)
			tasFlavor := newTASFlavorSnapshot(log, flavorInformation{TopologyName: "topology"},
				newTopologyTree(levels, nodes, 0), newDefaultSimulator())

			podSetName := kueue.PodSetReference("worker")
			groupName := "group-a"
			incomingObj := utiltestingapi.MakeWorkload("incoming", "ns").
				PodSets(*utiltestingapi.MakePodSet(podSetName, 1).PodSetGroup(groupName).Obj()).
				Obj()
			incoming := workload.NewInfo(log, incomingObj)
			if !tc.noSpreadingSpec {
				ruleKeys := tc.ruleKeys
				if len(ruleKeys) == 0 {
					ruleKeys = []string{treeTestRackLabel}
				}
				incoming.TopologySpreading = map[utiltas.PodSetGroupKey]*utiltas.SpreadingSpec{
					spreadKeyForGroupName(groupName): appMainSpreading(t, ruleKeys...),
				}
			}
			if tc.nilWorkload {
				incoming = nil
			}

			// A PodSet declaring no group falls back to its own name, a key the
			// incoming Workload's spreading map has no entry for.
			requestedPodSet := &incomingObj.Spec.PodSets[0]
			requests := FlavorTASRequests{{PodSet: requestedPodSet, PodSetGroupName: new(groupName)}}
			if tc.requestWithoutSpec {
				ungrouped := *utiltestingapi.MakePodSet("ungrouped", 1).Obj()
				requests = FlavorTASRequests{{PodSet: &ungrouped}}
			}

			cq := &ClusterQueueSnapshot{
				Workloads:  make(map[workload.Reference]*workload.Info, len(tc.existing)),
				TASFlavors: map[kueue.ResourceFlavorReference]*TASFlavorSnapshot{spreadCountsTestFlavor: tasFlavor},
			}
			for _, wl := range tc.existing {
				cq.Workloads[workload.Key(wl)] = workload.NewInfo(log, wl)
			}

			flavor := spreadCountsTestFlavor
			if tc.unknownFlavor {
				flavor = "not-a-tas-flavor"
			}

			got := cq.topologySpreadCountsForFlavor(incoming, flavor, requests)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("topologySpreadCountsForFlavor() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
