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
	"k8s.io/apimachinery/pkg/labels"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

const spreadCountsTestFlavor kueue.ResourceFlavorReference = "tas-flavor"

func TestTopologySpreadCounts(t *testing.T) {
	levels := []string{treeTestBlockLabel, treeTestRackLabel, corev1.LabelHostname}
	tasFlavor := newTASFlavorSnapshot(testr.New(t), "topology", newTopologyTree(levels, []*corev1.Node{
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
	incoming := workload.NewInfo(incomingObj)
	incoming.TopologySpreading = map[utiltas.PodSetGroupKey]*utiltas.SpreadingSpec{
		"group-a": {
			WorkloadLabelSelector: labels.SelectorFromSet(labels.Set{"app": "main"}),
			Rules: []utiltas.SpreadingRule{
				{Key: treeTestBlockLabel},
				{Key: treeTestRackLabel},
			},
		},
		"group-b": {
			WorkloadLabelSelector: labels.SelectorFromSet(labels.Set{"app": "main"}),
			Rules:                 []utiltas.SpreadingRule{{Key: treeTestBlockLabel}},
		},
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
		cq.Workloads[workload.Key(wl)] = workload.NewInfo(wl)
	}

	got := cq.topologySpreadCounts(incoming, requests)
	want := FlavorToSpreadTreeCount{
		spreadCountsTestFlavor: {
			"group-a": {
				"":      2,
				"b1":    2,
				"b1,r1": 2,
			},
			"group-b": {
				"":   1,
				"b2": 1,
			},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("topologySpreadCounts() mismatch (-want +got):\n%s", diff)
	}
}

type spreadCountsPodSetPlacement struct {
	name  kueue.PodSetReference
	group string
	node  string
}

func makeSpreadCountsWorkload(name, namespace, app string, placements []spreadCountsPodSetPlacement) *kueue.Workload {
	podSets := make([]kueue.PodSet, 0, len(placements))
	assignments := make([]kueue.PodSetAssignment, 0, len(placements))
	for _, placement := range placements {
		podSets = append(podSets, *utiltestingapi.MakePodSet(placement.name, 1).PodSetGroup(placement.group).Obj())
		topologyAssignment := utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
			Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{placement.node}, 1).Obj()).
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
	levels := []string{treeTestBlockLabel, treeTestRackLabel, corev1.LabelHostname}
	tasFlavor := newTASFlavorSnapshot(testr.New(t), "topology", newTopologyTree(levels, []*corev1.Node{
		makeTreeTestNode("n1", "b1", "r1"),
		makeTreeTestNode("n3", "b2", "r2"),
	}, 0), nil, newDefaultSimulatorSnapshot())

	// An admitted Workload being re-placed: it matches its own selector and is
	// already in the snapshot with a topology assignment.
	incomingObj := makeSpreadCountsWorkload("incoming", "ns", "main", []spreadCountsPodSetPlacement{
		{name: "a", group: "group-a", node: "n1"},
	})
	incoming := workload.NewInfo(incomingObj)
	incoming.TopologySpreading = map[utiltas.PodSetGroupKey]*utiltas.SpreadingSpec{
		"group-a": {
			WorkloadLabelSelector: labels.SelectorFromSet(labels.Set{"app": "main"}),
			Rules: []utiltas.SpreadingRule{
				{Key: treeTestBlockLabel},
				{Key: treeTestRackLabel},
			},
		},
	}

	other := makeSpreadCountsWorkload("wl-1", "ns", "main", []spreadCountsPodSetPlacement{
		{name: "a", group: "group-a", node: "n3"},
	})
	cq := &ClusterQueueSnapshot{
		Workloads: map[workload.Reference]*workload.Info{
			workload.Key(incomingObj): incoming,
			workload.Key(other):       workload.NewInfo(other),
		},
		TASFlavors: map[kueue.ResourceFlavorReference]*TASFlavorSnapshot{spreadCountsTestFlavor: tasFlavor},
	}

	requests := WorkloadTASRequests{
		spreadCountsTestFlavor: {
			{PodSet: &incomingObj.Spec.PodSets[0], PodSetGroupName: new("group-a")},
		},
	}

	got := cq.topologySpreadCounts(incoming, requests)
	want := FlavorToSpreadTreeCount{
		spreadCountsTestFlavor: {
			"group-a": {
				"":      1,
				"b2":    1,
				"b2,r2": 1,
			},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("topologySpreadCounts() mismatch (-want +got):\n%s", diff)
	}
}
