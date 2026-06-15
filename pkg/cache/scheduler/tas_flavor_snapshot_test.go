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
	"maps"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/node"
)

func TestFreeCapacityPerDomain(t *testing.T) {
	snapshot := &TASFlavorSnapshot{
		leaves: leafDomainByID{
			"domain2": &leafDomain{
				freeCapacity: resources.Requests{
					corev1.ResourceCPU:    1000,
					corev1.ResourceMemory: 2 * 1024 * 1024 * 1024, // 2 GiB
				},
				tasUsage: resources.Requests{
					corev1.ResourceMemory: 1 * 1024 * 1024 * 1024, // 1 GiB
					corev1.ResourceCPU:    500,
				},
			},
			"domain1": &leafDomain{
				freeCapacity: resources.Requests{
					corev1.ResourceMemory: 4 * 1024 * 1024 * 1024, // 4 GiB
					corev1.ResourceCPU:    2000,
					"nvidia.com/gpu":      1,
				},
				tasUsage: resources.Requests{
					corev1.ResourceCPU:    500,
					"nvidia.com/gpu":      1,
					corev1.ResourceMemory: 2 * 1024 * 1024 * 1024, // 1 GiB
				},
			},
		},
	}

	expected := `{"domain1":{"freeCapacity":{"cpu":"2","memory":"4Gi","nvidia.com/gpu":"1"},"tasUsage":{"cpu":"500m","memory":"2Gi","nvidia.com/gpu":"1"}},"domain2":{"freeCapacity":{"cpu":"1","memory":"2Gi"},"tasUsage":{"cpu":"500m","memory":"1Gi"}}}`
	var wantErr error

	got, gotErr := snapshot.SerializeFreeCapacityPerDomain()
	if diff := cmp.Diff(wantErr, gotErr, cmpopts.EquateErrors()); len(diff) != 0 {
		t.Errorf("Unexpected error (-want,+got):\n%s", diff)
	}
	if diff := cmp.Diff(expected, got); diff != "" {
		t.Errorf("SerializeFreeCapacityPerDomain() mismatch (-expected +got):\n%s", diff)
	}
}

func TestMergeTopologyAssignments(t *testing.T) {
	nodes := []corev1.Node{
		*node.MakeNode("x").Label("level-1", "a").Label("level-2", "b").Obj(),
		*node.MakeNode("y").Label("level-1", "a").Label("level-2", "c").Obj(),
		*node.MakeNode("z").Label("level-1", "d").Label("level-2", "e").Obj(),
		*node.MakeNode("w").Label("level-1", "d").Label("level-2", "f").Obj(),
	}
	levels := []string{"level-1", "level-2"}

	cases := map[string]struct {
		a    *tas.TopologyAssignment
		b    *tas.TopologyAssignment
		want tas.TopologyAssignment
	}{
		"topologies with different domains, all a before b": {
			a: &tas.TopologyAssignment{
				Levels: []string{"level-1", "level-2"},
				Domains: []tas.TopologyDomainAssignment{
					{
						Values: []string{"a", "b"},
						Count:  1,
					},
					{
						Values: []string{"a", "c"},
						Count:  1,
					},
				},
			},
			b: &tas.TopologyAssignment{
				Levels: []string{"level-1", "level-2"},
				Domains: []tas.TopologyDomainAssignment{
					{
						Values: []string{"d", "e"},
						Count:  1,
					},
					{
						Values: []string{"d", "f"},
						Count:  1,
					},
				},
			},
			want: tas.TopologyAssignment{
				Levels: []string{"level-1", "level-2"},
				Domains: []tas.TopologyDomainAssignment{
					{
						Values: []string{"a", "b"},
						Count:  1,
					},
					{
						Values: []string{"a", "c"},
						Count:  1,
					},
					{
						Values: []string{"d", "e"},
						Count:  1,
					},
					{
						Values: []string{"d", "f"},
						Count:  1,
					},
				},
			},
		},
		"topologies with different domains, all b before a": {
			a: &tas.TopologyAssignment{
				Levels: []string{"level-1", "level-2"},
				Domains: []tas.TopologyDomainAssignment{
					{
						Values: []string{"d", "e"},
						Count:  1,
					},
					{
						Values: []string{"d", "f"},
						Count:  1,
					},
				},
			},
			b: &tas.TopologyAssignment{
				Levels: []string{"level-1", "level-2"},
				Domains: []tas.TopologyDomainAssignment{
					{
						Values: []string{"a", "b"},
						Count:  1,
					},
					{
						Values: []string{"a", "c"},
						Count:  1,
					},
				},
			},
			want: tas.TopologyAssignment{
				Levels: []string{"level-1", "level-2"},
				Domains: []tas.TopologyDomainAssignment{
					{
						Values: []string{"a", "b"},
						Count:  1,
					},
					{
						Values: []string{"a", "c"},
						Count:  1,
					},
					{
						Values: []string{"d", "e"},
						Count:  1,
					},
					{
						Values: []string{"d", "f"},
						Count:  1,
					},
				},
			},
		},
		"topologies with different domains, mixed order": {
			a: &tas.TopologyAssignment{
				Levels: []string{"level-1", "level-2"},
				Domains: []tas.TopologyDomainAssignment{
					{
						Values: []string{"a", "c"},
						Count:  1,
					},
					{
						Values: []string{"d", "e"},
						Count:  1,
					},
				},
			},
			b: &tas.TopologyAssignment{
				Levels: []string{"level-1", "level-2"},
				Domains: []tas.TopologyDomainAssignment{
					{
						Values: []string{"a", "b"},
						Count:  1,
					},
					{
						Values: []string{"d", "f"},
						Count:  1,
					},
				},
			},
			want: tas.TopologyAssignment{
				Levels: []string{"level-1", "level-2"},
				Domains: []tas.TopologyDomainAssignment{
					{
						Values: []string{"a", "b"},
						Count:  1,
					},
					{
						Values: []string{"a", "c"},
						Count:  1,
					},
					{
						Values: []string{"d", "e"},
						Count:  1,
					},
					{
						Values: []string{"d", "f"},
						Count:  1,
					},
				},
			},
		},
		"topologies with different and the same domains, mixed order": {
			a: &tas.TopologyAssignment{
				Levels: []string{"level-1", "level-2"},
				Domains: []tas.TopologyDomainAssignment{
					{
						Values: []string{"a", "c"},
						Count:  1,
					},
					{
						Values: []string{"d", "e"},
						Count:  1,
					},
				},
			},
			b: &tas.TopologyAssignment{
				Levels: []string{"level-1", "level-2"},
				Domains: []tas.TopologyDomainAssignment{
					{
						Values: []string{"a", "b"},
						Count:  1,
					},
					{
						Values: []string{"d", "e"},
						Count:  1,
					},
				},
			},
			want: tas.TopologyAssignment{
				Levels: []string{"level-1", "level-2"},
				Domains: []tas.TopologyDomainAssignment{
					{
						Values: []string{"a", "b"},
						Count:  1,
					},
					{
						Values: []string{"a", "c"},
						Count:  1,
					},
					{
						Values: []string{"d", "e"},
						Count:  2,
					},
				},
			},
		},
		"topology a with empty domains": {
			a: &tas.TopologyAssignment{
				Levels:  []string{"level-1", "level-2"},
				Domains: []tas.TopologyDomainAssignment{},
			},
			b: &tas.TopologyAssignment{
				Levels: []string{"level-1", "level-2"},
				Domains: []tas.TopologyDomainAssignment{
					{
						Values: []string{"a", "b"},
						Count:  1,
					},
					{
						Values: []string{"d", "e"},
						Count:  1,
					},
				},
			},
			want: tas.TopologyAssignment{
				Levels: []string{"level-1", "level-2"},
				Domains: []tas.TopologyDomainAssignment{
					{
						Values: []string{"a", "b"},
						Count:  1,
					},
					{
						Values: []string{"d", "e"},
						Count:  1,
					},
				},
			},
		},
		"topology b with empty domain": {
			a: &tas.TopologyAssignment{
				Levels: []string{"level-1", "level-2"},
				Domains: []tas.TopologyDomainAssignment{
					{
						Values: []string{"a", "c"},
						Count:  1,
					},
					{
						Values: []string{"d", "e"},
						Count:  1,
					},
				},
			},
			b: &tas.TopologyAssignment{
				Levels:  []string{"level-1", "level-2"},
				Domains: []tas.TopologyDomainAssignment{},
			},
			want: tas.TopologyAssignment{
				Levels: []string{"level-1", "level-2"},
				Domains: []tas.TopologyDomainAssignment{
					{
						Values: []string{"a", "c"},
						Count:  1,
					},
					{
						Values: []string{"d", "e"},
						Count:  1,
					},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, "dummy", levels, nil)
			for i := range nodes {
				s.addNode(newNodeInfo(&nodes[i]))
			}
			s.initialize()

			got := s.mergeTopologyAssignments(tc.a, tc.b)
			if diff := cmp.Diff(tc.want, *got); diff != "" {
				t.Errorf("unexpected topology assignment (-want,+got): %s", diff)
			}
		})
	}
}

func TestHasLevel(t *testing.T) {
	levels := []string{"level-1", "level-2"}

	testCases := map[string]struct {
		podSetTopologyRequest *kueue.PodSetTopologyRequest
		want                  bool
	}{
		"topology request nil": {
			podSetTopologyRequest: nil,
			want:                  false,
		},
		"topology request empty": {
			podSetTopologyRequest: &kueue.PodSetTopologyRequest{},
			want:                  false,
		},
		"required": {
			podSetTopologyRequest: &kueue.PodSetTopologyRequest{
				Required: new("level-1"),
			},
			want: true,
		},
		"required – invalid level": {
			podSetTopologyRequest: &kueue.PodSetTopologyRequest{
				Required: new("invalid-level"),
			},
			want: false,
		},
		"preferred": {
			podSetTopologyRequest: &kueue.PodSetTopologyRequest{
				Preferred: new("level-1"),
			},
			want: true,
		},
		"preferred – invalid level": {
			podSetTopologyRequest: &kueue.PodSetTopologyRequest{
				Preferred: new("invalid-level"),
			},
			want: false,
		},
		"unconstrained": {
			podSetTopologyRequest: &kueue.PodSetTopologyRequest{
				Unconstrained: new(true),
			},
			want: true,
		},
		"slice-only": {
			podSetTopologyRequest: &kueue.PodSetTopologyRequest{
				PodSetSliceRequiredTopology: new("level-1"),
			},
			want: true,
		},
		"slice-only – invalid level": {
			podSetTopologyRequest: &kueue.PodSetTopologyRequest{
				PodSetSliceRequiredTopology: new("invalid-level"),
			},
			want: false,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, "dummy", levels, nil)
			got := s.HasLevel(tc.podSetTopologyRequest)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("unexpected HasLevel result (-want,+got): %s", diff)
			}
		})
	}
}

// TestSortedDomainsWithLeader verifies the sorting criteria (in order of priority):
// 1. leaderState - descending (always)
// 2. sliceStateWithLeader - descending (BestFit) or ascending (LeastFreeCapacity)
// 3. stateWithLeader - ascending (always, as tiebreaker)
// 4. levelValues - ascending (always, as final tiebreaker)
func TestSortedDomainsWithLeader(t *testing.T) {
	levels := []string{"block"}

	testCases := map[string]struct {
		domains                              []*domain
		unconstrained                        bool
		enableTASPreferredSchedulingAffinity bool
		wantOrder                            []string
	}{
		"affinityScore descending: higher affinity score comes first": {
			enableTASPreferredSchedulingAffinity: true,
			domains: []*domain{
				{id: "low-affinity", affinityScore: 10, leaderState: 1, sliceStateWithLeader: 5, stateWithLeader: 10, levelValues: []string{"a"}},
				{id: "high-affinity", affinityScore: 100, leaderState: 1, sliceStateWithLeader: 5, stateWithLeader: 10, levelValues: []string{"b"}},
			},
			unconstrained: false,
			wantOrder:     []string{"high-affinity", "low-affinity"},
		},
		"affinityScore ignored when feature gate is disabled": {
			enableTASPreferredSchedulingAffinity: false,
			domains: []*domain{
				{id: "low-affinity", affinityScore: 10, leaderState: 1, sliceStateWithLeader: 5, stateWithLeader: 10, levelValues: []string{"a"}},
				{id: "high-affinity", affinityScore: 100, leaderState: 1, sliceStateWithLeader: 5, stateWithLeader: 10, levelValues: []string{"b"}},
			},
			unconstrained: false,
			wantOrder:     []string{"low-affinity", "high-affinity"},
		},
		"leaderState descending: domains that can host leader come first": {
			domains: []*domain{
				{id: "no-leader", leaderState: 0, sliceStateWithLeader: 10, stateWithLeader: 10, levelValues: []string{"a"}},
				{id: "has-leader", leaderState: 1, sliceStateWithLeader: 1, stateWithLeader: 1, levelValues: []string{"b"}},
			},
			unconstrained: false,
			wantOrder:     []string{"has-leader", "no-leader"},
		},
		"leader capability prioritized over preferred affinity": {
			enableTASPreferredSchedulingAffinity: true,
			domains: []*domain{
				{id: "preferred-no-leader", affinityScore: 100, leaderState: 0, sliceStateWithLeader: 0, stateWithLeader: 0, levelValues: []string{"a"}},
				{id: "non-preferred-has-leader", affinityScore: 10, leaderState: 1, sliceStateWithLeader: 5, stateWithLeader: 10, levelValues: []string{"b"}},
			},
			unconstrained: false,
			wantOrder:     []string{"non-preferred-has-leader", "preferred-no-leader"},
		},
		"BestFit: sliceStateWithLeader descending": {
			domains: []*domain{
				{id: "a", leaderState: 1, sliceStateWithLeader: 3, stateWithLeader: 1, levelValues: []string{"a"}},
				{id: "b", leaderState: 1, sliceStateWithLeader: 1, stateWithLeader: 1, levelValues: []string{"b"}},
				{id: "c", leaderState: 1, sliceStateWithLeader: 2, stateWithLeader: 1, levelValues: []string{"c"}},
			},
			unconstrained: false,
			wantOrder:     []string{"a", "c", "b"},
		},
		"LeastFreeCapacity: sliceStateWithLeader ascending": {
			domains: []*domain{
				{id: "a", leaderState: 1, sliceStateWithLeader: 3, stateWithLeader: 1, levelValues: []string{"a"}},
				{id: "b", leaderState: 1, sliceStateWithLeader: 1, stateWithLeader: 1, levelValues: []string{"b"}},
				{id: "c", leaderState: 1, sliceStateWithLeader: 2, stateWithLeader: 1, levelValues: []string{"c"}},
			},
			unconstrained: true,
			wantOrder:     []string{"b", "c", "a"},
		},
		"BestFit: stateWithLeader ascending as tiebreaker": {
			domains: []*domain{
				{id: "large", leaderState: 1, sliceStateWithLeader: 5, stateWithLeader: 100, levelValues: []string{"a"}},
				{id: "small", leaderState: 1, sliceStateWithLeader: 5, stateWithLeader: 10, levelValues: []string{"b"}},
				{id: "medium", leaderState: 1, sliceStateWithLeader: 5, stateWithLeader: 50, levelValues: []string{"c"}},
			},
			unconstrained: false,
			wantOrder:     []string{"small", "medium", "large"},
		},
		"LeastFreeCapacity: stateWithLeader ascending as tiebreaker": {
			domains: []*domain{
				{id: "large", leaderState: 1, sliceStateWithLeader: 5, stateWithLeader: 100, levelValues: []string{"a"}},
				{id: "small", leaderState: 1, sliceStateWithLeader: 5, stateWithLeader: 10, levelValues: []string{"b"}},
				{id: "medium", leaderState: 1, sliceStateWithLeader: 5, stateWithLeader: 50, levelValues: []string{"c"}},
			},
			unconstrained: true,
			wantOrder:     []string{"small", "medium", "large"},
		},
		"levelValues ascending as final tiebreaker": {
			domains: []*domain{
				{id: "c", leaderState: 1, sliceStateWithLeader: 5, stateWithLeader: 10, levelValues: []string{"c"}},
				{id: "a", leaderState: 1, sliceStateWithLeader: 5, stateWithLeader: 10, levelValues: []string{"a"}},
				{id: "b", leaderState: 1, sliceStateWithLeader: 5, stateWithLeader: 10, levelValues: []string{"b"}},
			},
			unconstrained: false,
			wantOrder:     []string{"a", "b", "c"},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TASRespectNodeAffinityPreferred, tc.enableTASPreferredSchedulingAffinity)
			_, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, "test", levels, nil)

			sorted := s.sortedDomainsWithLeader(tc.domains, tc.unconstrained)

			gotOrder := make([]string, len(sorted))
			for i, d := range sorted {
				gotOrder[i] = string(d.id)
			}

			if diff := cmp.Diff(tc.wantOrder, gotOrder); diff != "" {
				t.Errorf("unexpected domain order (-want,+got): %s", diff)
			}
		})
	}
}

// TestSortedDomains verifies the sorting criteria (in order of priority):
// 1. affinityScore - descending (when TASRespectNodeAffinityPreferred is enabled)
// 2. sliceState - descending (BestFit) or ascending (LeastFreeCapacity)
// 3. state - ascending (always, as tiebreaker)
// 4. levelValues - ascending (always, as final tiebreaker)
func TestSortedDomains(t *testing.T) {
	levels := []string{"block"}

	testCases := map[string]struct {
		domains                              []*domain
		unconstrained                        bool
		enableTASPreferredSchedulingAffinity bool
		wantOrder                            []string
	}{
		"affinityScore descending: higher affinity score comes first": {
			enableTASPreferredSchedulingAffinity: true,
			domains: []*domain{
				{id: "low-affinity", affinityScore: 10, sliceState: 5, state: 10, levelValues: []string{"a"}},
				{id: "high-affinity", affinityScore: 100, sliceState: 5, state: 10, levelValues: []string{"b"}},
			},
			unconstrained: false,
			wantOrder:     []string{"high-affinity", "low-affinity"},
		},
		"affinityScore ignored when feature gate is disabled": {
			enableTASPreferredSchedulingAffinity: false,
			domains: []*domain{
				{id: "low-affinity", affinityScore: 10, sliceState: 5, state: 10, levelValues: []string{"a"}},
				{id: "high-affinity", affinityScore: 100, sliceState: 5, state: 10, levelValues: []string{"b"}},
			},
			unconstrained: false,
			wantOrder:     []string{"low-affinity", "high-affinity"},
		},
		"BestFit: sliceState descending": {
			domains: []*domain{
				{id: "a", sliceState: 3, state: 1, levelValues: []string{"a"}},
				{id: "b", sliceState: 1, state: 1, levelValues: []string{"b"}},
				{id: "c", sliceState: 2, state: 1, levelValues: []string{"c"}},
			},
			unconstrained: false,
			wantOrder:     []string{"a", "c", "b"},
		},
		"LeastFreeCapacity: sliceState ascending": {
			domains: []*domain{
				{id: "a", sliceState: 3, state: 1, levelValues: []string{"a"}},
				{id: "b", sliceState: 1, state: 1, levelValues: []string{"b"}},
				{id: "c", sliceState: 2, state: 1, levelValues: []string{"c"}},
			},
			unconstrained: true,
			wantOrder:     []string{"b", "c", "a"},
		},
		"BestFit: state ascending as tiebreaker": {
			domains: []*domain{
				{id: "large", sliceState: 5, state: 100, levelValues: []string{"a"}},
				{id: "small", sliceState: 5, state: 10, levelValues: []string{"b"}},
				{id: "medium", sliceState: 5, state: 50, levelValues: []string{"c"}},
			},
			unconstrained: false,
			wantOrder:     []string{"small", "medium", "large"},
		},
		"LeastFreeCapacity: state ascending as tiebreaker": {
			domains: []*domain{
				{id: "large", sliceState: 5, state: 100, levelValues: []string{"a"}},
				{id: "small", sliceState: 5, state: 10, levelValues: []string{"b"}},
				{id: "medium", sliceState: 5, state: 50, levelValues: []string{"c"}},
			},
			unconstrained: true,
			wantOrder:     []string{"small", "medium", "large"},
		},
		"levelValues ascending as final tiebreaker": {
			domains: []*domain{
				{id: "c", sliceState: 5, state: 10, levelValues: []string{"c"}},
				{id: "a", sliceState: 5, state: 10, levelValues: []string{"a"}},
				{id: "b", sliceState: 5, state: 10, levelValues: []string{"b"}},
			},
			unconstrained: false,
			wantOrder:     []string{"a", "b", "c"},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TASRespectNodeAffinityPreferred, tc.enableTASPreferredSchedulingAffinity)
			_, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, "test", levels, nil)

			sorted := s.sortedDomains(tc.domains, tc.unconstrained)

			gotOrder := make([]string, len(sorted))
			for i, d := range sorted {
				gotOrder[i] = string(d.id)
			}

			if diff := cmp.Diff(tc.wantOrder, gotOrder); diff != "" {
				t.Errorf("unexpected domain order (-want,+got): %s", diff)
			}
		})
	}
}

func TestCountPodsInAssignment(t *testing.T) {
	cases := map[string]struct {
		assignment *tas.TopologyAssignment
		want       int32
	}{
		"empty assignment": {
			assignment: &tas.TopologyAssignment{
				Levels:  []string{"hostname"},
				Domains: nil,
			},
			want: 0,
		},
		"single domain": {
			assignment: &tas.TopologyAssignment{
				Levels: []string{"hostname"},
				Domains: []tas.TopologyDomainAssignment{
					{Values: []string{"node-a"}, Count: 3},
				},
			},
			want: 3,
		},
		"multiple domains": {
			assignment: &tas.TopologyAssignment{
				Levels: []string{"hostname"},
				Domains: []tas.TopologyDomainAssignment{
					{Values: []string{"node-a"}, Count: 2},
					{Values: []string{"node-b"}, Count: 3},
					{Values: []string{"node-c"}, Count: 1},
				},
			},
			want: 6,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := tas.CountPodsInAssignment(tc.assignment)
			if got != tc.want {
				t.Errorf("CountPodsInAssignment() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestComputeAssumedUsageFromAssignment(t *testing.T) {
	singlePodRequests := resources.Requests{
		corev1.ResourceCPU:    1000,
		corev1.ResourceMemory: 1024,
	}

	cases := map[string]struct {
		assignment *tas.TopologyAssignment
		want       map[tas.TopologyDomainID]resources.Requests
	}{
		"empty assignment": {
			assignment: &tas.TopologyAssignment{
				Levels:  []string{"hostname"},
				Domains: nil,
			},
			want: map[tas.TopologyDomainID]resources.Requests{},
		},
		"single domain with one pod": {
			assignment: &tas.TopologyAssignment{
				Levels: []string{"hostname"},
				Domains: []tas.TopologyDomainAssignment{
					{Values: []string{"node-a"}, Count: 1},
				},
			},
			want: map[tas.TopologyDomainID]resources.Requests{
				"node-a": {
					corev1.ResourceCPU:    1000,
					corev1.ResourceMemory: 1024,
					corev1.ResourcePods:   1,
				},
			},
		},
		"multiple domains": {
			assignment: &tas.TopologyAssignment{
				Levels: []string{"hostname"},
				Domains: []tas.TopologyDomainAssignment{
					{Values: []string{"node-a"}, Count: 2},
					{Values: []string{"node-b"}, Count: 3},
				},
			},
			want: map[tas.TopologyDomainID]resources.Requests{
				"node-a": {
					corev1.ResourceCPU:    2000,
					corev1.ResourceMemory: 2048,
					corev1.ResourcePods:   2,
				},
				"node-b": {
					corev1.ResourceCPU:    3000,
					corev1.ResourceMemory: 3072,
					corev1.ResourcePods:   3,
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := tas.ComputeUsagePerDomain(tc.assignment, singlePodRequests)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ComputeUsagePerDomain() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAddAssumedUsage(t *testing.T) {
	cases := map[string]struct {
		assumedUsage map[tas.TopologyDomainID]resources.Requests
		assignment   *tas.TopologyAssignment
		tasRequests  *TASPodSetRequests
		want         map[tas.TopologyDomainID]resources.Requests
	}{
		"includes pod count for existing and new domains": {
			assumedUsage: map[tas.TopologyDomainID]resources.Requests{
				"node-a": {
					corev1.ResourceCPU:  1000,
					corev1.ResourcePods: 1,
				},
			},
			assignment: &tas.TopologyAssignment{
				Levels: []string{"hostname"},
				Domains: []tas.TopologyDomainAssignment{
					{Values: []string{"node-a"}, Count: 1},
					{Values: []string{"node-b"}, Count: 2},
				},
			},
			tasRequests: &TASPodSetRequests{
				SinglePodRequests: resources.Requests{
					corev1.ResourceCPU:    500,
					corev1.ResourceMemory: 2048,
				},
			},
			want: map[tas.TopologyDomainID]resources.Requests{
				"node-a": {
					corev1.ResourceCPU:    1500,
					corev1.ResourceMemory: 2048,
					corev1.ResourcePods:   2,
				},
				"node-b": {
					corev1.ResourceCPU:    1000,
					corev1.ResourceMemory: 4096,
					corev1.ResourcePods:   2,
				},
			},
		},
		"includes pod count starting from empty assumed usage": {
			assumedUsage: map[tas.TopologyDomainID]resources.Requests{},
			assignment: &tas.TopologyAssignment{
				Levels: []string{"hostname"},
				Domains: []tas.TopologyDomainAssignment{
					{Values: []string{"node-a"}, Count: 3},
				},
			},
			tasRequests: &TASPodSetRequests{
				SinglePodRequests: resources.Requests{
					corev1.ResourceCPU:    250,
					corev1.ResourceMemory: 512,
				},
			},
			want: map[tas.TopologyDomainID]resources.Requests{
				"node-a": {
					corev1.ResourceCPU:    750,
					corev1.ResourceMemory: 1536,
					corev1.ResourcePods:   3,
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			addAssumedUsage(tc.assumedUsage, tc.assignment, tc.tasRequests)
			if diff := cmp.Diff(tc.want, tc.assumedUsage); diff != "" {
				t.Errorf("addAssumedUsage() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestTruncateAssignment(t *testing.T) {
	cases := map[string]struct {
		assignment *tas.TopologyAssignment
		newCount   int32
		want       *tas.TopologyAssignment
	}{
		"truncate to zero": {
			assignment: &tas.TopologyAssignment{
				Levels: []string{"hostname"},
				Domains: []tas.TopologyDomainAssignment{
					{Values: []string{"node-a"}, Count: 2},
				},
			},
			newCount: 0,
			want: &tas.TopologyAssignment{
				Levels:  []string{"hostname"},
				Domains: nil,
			},
		},
		"no truncation needed": {
			assignment: &tas.TopologyAssignment{
				Levels: []string{"hostname"},
				Domains: []tas.TopologyDomainAssignment{
					{Values: []string{"node-a"}, Count: 2},
					{Values: []string{"node-b"}, Count: 1},
				},
			},
			newCount: 3,
			want: &tas.TopologyAssignment{
				Levels: []string{"hostname"},
				Domains: []tas.TopologyDomainAssignment{
					{Values: []string{"node-a"}, Count: 2},
					{Values: []string{"node-b"}, Count: 1},
				},
			},
		},
		"truncate to single domain": {
			assignment: &tas.TopologyAssignment{
				Levels: []string{"hostname"},
				Domains: []tas.TopologyDomainAssignment{
					{Values: []string{"node-a"}, Count: 3},
					{Values: []string{"node-b"}, Count: 2},
				},
			},
			newCount: 3,
			want: &tas.TopologyAssignment{
				Levels: []string{"hostname"},
				Domains: []tas.TopologyDomainAssignment{
					{Values: []string{"node-a"}, Count: 3},
				},
			},
		},
		"truncation preserves assignment order not lex order": {
			assignment: &tas.TopologyAssignment{
				Levels: []string{"hostname"},
				Domains: []tas.TopologyDomainAssignment{
					{Values: []string{"node-z"}, Count: 3},
					{Values: []string{"node-a"}, Count: 2},
				},
			},
			newCount: 3,
			want: &tas.TopologyAssignment{
				Levels: []string{"hostname"},
				Domains: []tas.TopologyDomainAssignment{
					{Values: []string{"node-z"}, Count: 3},
				},
			},
		},
		"partial domain truncation": {
			assignment: &tas.TopologyAssignment{
				Levels: []string{"hostname"},
				Domains: []tas.TopologyDomainAssignment{
					{Values: []string{"node-a"}, Count: 3},
					{Values: []string{"node-b"}, Count: 3},
				},
			},
			newCount: 4,
			want: &tas.TopologyAssignment{
				Levels: []string{"hostname"},
				Domains: []tas.TopologyDomainAssignment{
					{Values: []string{"node-a"}, Count: 3},
					{Values: []string{"node-b"}, Count: 1},
				},
			},
		},
		"truncate within first domain": {
			assignment: &tas.TopologyAssignment{
				Levels: []string{"hostname"},
				Domains: []tas.TopologyDomainAssignment{
					{Values: []string{"node-a"}, Count: 5},
					{Values: []string{"node-b"}, Count: 3},
				},
			},
			newCount: 2,
			want: &tas.TopologyAssignment{
				Levels: []string{"hostname"},
				Domains: []tas.TopologyDomainAssignment{
					{Values: []string{"node-a"}, Count: 2},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := tas.TruncateAssignment(tc.assignment, tc.newCount)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("TruncateAssignment() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPackGroupOntoLeaves(t *testing.T) {
	gpu := corev1.ResourceName("nvidia.com/gpu")
	// Helper to build a TASPodSetRequests with a name, count, and GPU footprint.
	ps := func(name string, count int32, gpus int64) TASPodSetRequests {
		return TASPodSetRequests{
			PodSet:            &kueue.PodSet{Name: kueue.PodSetReference(name)},
			Count:             count,
			SinglePodRequests: resources.Requests{gpu: gpus},
		}
	}
	// Helper to build a leaf with a hostname value and GPU capacity (plus pod slots).
	leaf := func(host string, gpus, pods int64) *leafDomain {
		return &leafDomain{
			domain: domain{
				id:          tas.TopologyDomainID(host),
				levelValues: []string{"b1", host},
			},
			freeCapacity: resources.Requests{gpu: gpus, corev1.ResourcePods: pods},
			node:         &nodeInfo{Name: host, Labels: map[string]string{corev1.LabelHostname: host}},
		}
	}

	newSnapshot := func() *TASFlavorSnapshot {
		return &TASFlavorSnapshot{
			levelKeys:         []string{"cloud.com/block", corev1.LabelHostname},
			isLowestLevelNode: true,
		}
	}

	// defaultConstraints builds permissive per-PodSet node constraints (match
	// any node) for the given PodSets, so the packer's eligibility filter is a
	// no-op in tests that aren't exercising taints/selectors/affinity.
	defaultConstraints := func(trs FlavorTASRequests) []podSetNodeConstraints {
		c := make([]podSetNodeConstraints, len(trs))
		for i := range c {
			c[i] = podSetNodeConstraints{selector: labels.Everything()}
		}
		return c
	}

	totalCount := func(res map[kueue.PodSetReference]*tas.TopologyAssignment, name string) int32 {
		var n int32
		for _, d := range res[kueue.PodSetReference(name)].Domains {
			n += d.Count
		}
		return n
	}

	t.Run("heterogeneous group fits across leaves", func(t *testing.T) {
		s := newSnapshot()
		// training: 4 pods x 8 GPU, inference: 6 pods x 1 GPU => 38 GPU total.
		trs := FlavorTASRequests{ps("training", 4, 8), ps("inference", 6, 1)}
		// Two leaves of 24 GPU each (48 total) -- fits 38.
		leaves := []*leafDomain{leaf("n1", 24, 24), leaf("n2", 24, 24)}
		caps := []resources.Requests{leaves[0].freeCapacity.Clone(), leaves[1].freeCapacity.Clone()}
		res := s.packGroupOntoLeaves(trs, defaultConstraints(trs), leaves, caps)
		if res == nil {
			t.Fatal("expected a packing, got nil")
		}
		if got := totalCount(res, "training"); got != 4 {
			t.Errorf("training placed %d pods, want 4", got)
		}
		if got := totalCount(res, "inference"); got != 6 {
			t.Errorf("inference placed %d pods, want 6", got)
		}
	})

	t.Run("does not fit when aggregate capacity is insufficient", func(t *testing.T) {
		s := newSnapshot()
		trs := FlavorTASRequests{ps("training", 4, 8), ps("inference", 6, 1)} // 38 GPU
		leaves := []*leafDomain{leaf("n1", 16, 16), leaf("n2", 16, 16)}       // 32 GPU
		caps := []resources.Requests{leaves[0].freeCapacity.Clone(), leaves[1].freeCapacity.Clone()}
		if res := s.packGroupOntoLeaves(trs, defaultConstraints(trs), leaves, caps); res != nil {
			t.Errorf("expected nil (group does not fit), got %v", res)
		}
	})

	t.Run("greedy best-fit packs complementary big/small pods", func(t *testing.T) {
		s := newSnapshot()
		// Two big pods (5 GPU each) and two small pods (3 GPU each) onto two
		// leaves of 8 GPU. The valid packing is one big + one small per leaf
		// (5+3=8); greedy best-fit FFD (largest first, tightest leaf) finds it.
		trs := FlavorTASRequests{ps("big", 2, 5), ps("small", 2, 3)}
		leaves := []*leafDomain{leaf("n1", 8, 8), leaf("n2", 8, 8)}
		caps := []resources.Requests{leaves[0].freeCapacity.Clone(), leaves[1].freeCapacity.Clone()}
		res := s.packGroupOntoLeaves(trs, defaultConstraints(trs), leaves, caps)
		if res == nil {
			t.Fatal("expected a packing, got nil")
		}
		if got := totalCount(res, "big"); got != 2 {
			t.Errorf("big placed %d pods, want 2", got)
		}
		if got := totalCount(res, "small"); got != 2 {
			t.Errorf("small placed %d pods, want 2", got)
		}
	})

	t.Run("exact fit on a single leaf", func(t *testing.T) {
		s := newSnapshot()
		trs := FlavorTASRequests{ps("a", 2, 2), ps("b", 1, 4)} // 8 GPU
		leaves := []*leafDomain{leaf("n1", 8, 8)}
		caps := []resources.Requests{leaves[0].freeCapacity.Clone()}
		res := s.packGroupOntoLeaves(trs, defaultConstraints(trs), leaves, caps)
		if res == nil {
			t.Fatal("expected a packing, got nil")
		}
		if got := totalCount(res, "a"); got != 2 {
			t.Errorf("a placed %d pods, want 2", got)
		}
		if got := totalCount(res, "b"); got != 1 {
			t.Errorf("b placed %d pods, want 1", got)
		}
	})

	t.Run("multi-resource group placed by best-fit greedy without fallback", func(t *testing.T) {
		// Two leaves with complementary scarcity: n-cpu is CPU-heavy, n-gpu is
		// GPU-heavy. The CPU-heavy pods only fit n-cpu and the GPU-heavy pods
		// only fit n-gpu, so the greedy best-fit pass places them correctly.
		s := newSnapshot()
		cpu := corev1.ResourceCPU
		mkPS := func(name string, count int32, cpus, gpus int64) TASPodSetRequests {
			return TASPodSetRequests{
				PodSet:            &kueue.PodSet{Name: kueue.PodSetReference(name)},
				Count:             count,
				SinglePodRequests: resources.Requests{cpu: cpus, gpu: gpus},
			}
		}
		mkLeaf := func(host string, cpus, gpus int64) *leafDomain {
			return &leafDomain{
				domain:       domain{id: tas.TopologyDomainID(host), levelValues: []string{"b1", host}},
				freeCapacity: resources.Requests{cpu: cpus, gpu: gpus, corev1.ResourcePods: 100},
				node:         &nodeInfo{Name: host, Labels: map[string]string{corev1.LabelHostname: host}},
			}
		}
		trs := FlavorTASRequests{mkPS("cpuheavy", 2, 8, 1), mkPS("gpuheavy", 2, 1, 8)}
		leaves := []*leafDomain{mkLeaf("n-cpu", 18, 4), mkLeaf("n-gpu", 4, 18)}
		caps := []resources.Requests{leaves[0].freeCapacity.Clone(), leaves[1].freeCapacity.Clone()}
		res := s.packGroupOntoLeaves(trs, defaultConstraints(trs), leaves, caps)
		if res == nil {
			t.Fatal("expected a packing, got nil")
		}
		if got := totalCount(res, "cpuheavy"); got != 2 {
			t.Errorf("cpuheavy placed %d pods, want 2", got)
		}
		if got := totalCount(res, "gpuheavy"); got != 2 {
			t.Errorf("gpuheavy placed %d pods, want 2", got)
		}
	})

	t.Run("best-fit places a pod on the tighter leaf, keeping the larger free", func(t *testing.T) {
		// One PodSet of a single 2-GPU pod, two leaves: n-small=2 GPU, n-big=4
		// GPU. Best-fit must place it on n-small (exact fit), leaving n-big's 4
		// GPUs intact for a future larger workload.
		s := newSnapshot()
		trs := FlavorTASRequests{ps("a", 1, 2)}
		leaves := []*leafDomain{leaf("n-small", 2, 10), leaf("n-big", 4, 10)}
		caps := []resources.Requests{leaves[0].freeCapacity.Clone(), leaves[1].freeCapacity.Clone()}
		res := s.packGroupOntoLeaves(trs, defaultConstraints(trs), leaves, caps)
		if res == nil {
			t.Fatal("expected a packing, got nil")
		}
		ta := res[kueue.PodSetReference("a")]
		if len(ta.Domains) != 1 {
			t.Fatalf("expected placement on a single leaf, got %d", len(ta.Domains))
		}
		if host := ta.Domains[0].Values[len(ta.Domains[0].Values)-1]; host != "n-small" {
			t.Errorf("placed on %q, want best-fit leaf n-small (keeps n-big free)", host)
		}
	})

	t.Run("placement is deterministic across repeated calls with tied leaves", func(t *testing.T) {
		// Three identical leaves and a group of identical pods: every leaf ties
		// on the best-fit score, so the distribution must be decided by a stable
		// leaf order, not map iteration. Repeated calls must produce identical
		// per-leaf counts.
		s := newSnapshot()
		trs := FlavorTASRequests{ps("a", 6, 1)}
		newLeaves := func() []*leafDomain {
			return []*leafDomain{leaf("n1", 4, 10), leaf("n2", 4, 10), leaf("n3", 4, 10)}
		}
		var first map[string]int32
		for iter := range 10 {
			leaves := newLeaves()
			caps := []resources.Requests{
				leaves[0].freeCapacity.Clone(),
				leaves[1].freeCapacity.Clone(),
				leaves[2].freeCapacity.Clone(),
			}
			res := s.packGroupOntoLeaves(trs, defaultConstraints(trs), leaves, caps)
			if res == nil {
				t.Fatal("expected a packing, got nil")
			}
			got := map[string]int32{}
			for _, d := range res[kueue.PodSetReference("a")].Domains {
				got[d.Values[len(d.Values)-1]] += d.Count
			}
			if first == nil {
				first = got
				continue
			}
			if !maps.Equal(first, got) {
				t.Fatalf("non-deterministic placement: iter %d got %v, want %v", iter, got, first)
			}
		}
	})
}

func TestBestFitScore(t *testing.T) {
	gpu := corev1.ResourceName("nvidia.com/gpu")
	cpu := corev1.ResourceCPU
	total := resources.Requests{cpu: 10, gpu: 10}

	// Best-fit: a tighter leaf (less leftover after placement) must score LOWER.
	// A 2-GPU pod leaves 0 on a 2-GPU leaf vs 2 on a 4-GPU leaf, so the 2-GPU
	// leaf is preferred (keeps the larger leaf free).
	need := resources.Requests{gpu: 2}
	tight := resources.Requests{gpu: 2}
	roomy := resources.Requests{gpu: 4}
	if bestFitScore(need, tight, total) >= bestFitScore(need, roomy, total) {
		t.Errorf("expected the tighter-fitting leaf to score lower (best-fit)")
	}

	// Multi-resource: the leaf left with the least scarcity-weighted leftover wins.
	mixedNeed := resources.Requests{cpu: 8, gpu: 1}
	cpuRich := resources.Requests{cpu: 9, gpu: 1}    // leftover (1,0)
	cpuExcess := resources.Requests{cpu: 18, gpu: 1} // leftover (10,0) -- much roomier
	if bestFitScore(mixedNeed, cpuRich, total) >= bestFitScore(mixedNeed, cpuExcess, total) {
		t.Errorf("expected the tighter CPU leaf to score lower")
	}

	// Zero total capacity for a resource must not panic or contribute.
	if got := bestFitScore(resources.Requests{gpu: 1}, resources.Requests{gpu: 1}, resources.Requests{}); got != 0 {
		t.Errorf("expected 0 score when total capacity is empty, got %v", got)
	}
}

func TestPackGroupOntoLeavesNodeEligibility(t *testing.T) {
	gpu := corev1.ResourceName("nvidia.com/gpu")
	s := &TASFlavorSnapshot{
		levelKeys:         []string{"cloud.com/block", corev1.LabelHostname},
		isLowestLevelNode: true,
	}
	ps := func(name string, count int32, gpus int64) TASPodSetRequests {
		return TASPodSetRequests{
			PodSet:            &kueue.PodSet{Name: kueue.PodSetReference(name)},
			Count:             count,
			SinglePodRequests: resources.Requests{gpu: gpus},
		}
	}
	// leaf with optional taint and a "pool" label.
	mkLeaf := func(host, pool string, gpus int64, taints []corev1.Taint) *leafDomain {
		return &leafDomain{
			domain:       domain{id: tas.TopologyDomainID(host), levelValues: []string{"b1", host}},
			freeCapacity: resources.Requests{gpu: gpus, corev1.ResourcePods: 100},
			node: &nodeInfo{
				Name:   host,
				Labels: map[string]string{corev1.LabelHostname: host, "pool": pool},
				Taints: taints,
			},
		}
	}
	totalCount := func(res map[kueue.PodSetReference]*tas.TopologyAssignment, name string) int32 {
		var n int32
		for _, d := range res[kueue.PodSetReference(name)].Domains {
			n += d.Count
		}
		return n
	}

	t.Run("excludes a capacity-sufficient leaf with an untolerated taint", func(t *testing.T) {
		// n-bad has plenty of GPU but a NoSchedule taint the PodSet doesn't
		// tolerate; n-good (exact fit) must be used instead.
		trs := FlavorTASRequests{ps("a", 2, 1)}
		constraints := []podSetNodeConstraints{{selector: labels.Everything()}} // no tolerations
		taint := []corev1.Taint{{Key: "dedicated", Value: "other", Effect: corev1.TaintEffectNoSchedule}}
		leaves := []*leafDomain{mkLeaf("n-bad", "p", 100, taint), mkLeaf("n-good", "p", 2, nil)}
		caps := []resources.Requests{leaves[0].freeCapacity.Clone(), leaves[1].freeCapacity.Clone()}
		res := s.packGroupOntoLeaves(trs, constraints, leaves, caps)
		if res == nil {
			t.Fatal("expected a packing on the untainted leaf, got nil")
		}
		for _, d := range res[kueue.PodSetReference("a")].Domains {
			if host := d.Values[len(d.Values)-1]; host != "n-good" {
				t.Errorf("placed on tainted node %q, want n-good", host)
			}
		}
		if totalCount(res, "a") != 2 {
			t.Errorf("placed %d pods, want 2", totalCount(res, "a"))
		}
	})

	t.Run("excludes a capacity-sufficient leaf that fails the node selector", func(t *testing.T) {
		// PodSet requires pool=gpu; n-big is pool=cpu (mismatch) despite ample
		// capacity, so the group must land on n-small (pool=gpu).
		trs := FlavorTASRequests{ps("a", 2, 1)}
		sel, err := labels.ValidatedSelectorFromSet(map[string]string{"pool": "gpu"})
		if err != nil {
			t.Fatal(err)
		}
		constraints := []podSetNodeConstraints{{selector: sel}}
		leaves := []*leafDomain{mkLeaf("n-big", "cpu", 100, nil), mkLeaf("n-small", "gpu", 2, nil)}
		caps := []resources.Requests{leaves[0].freeCapacity.Clone(), leaves[1].freeCapacity.Clone()}
		res := s.packGroupOntoLeaves(trs, constraints, leaves, caps)
		if res == nil {
			t.Fatal("expected a packing on the matching leaf, got nil")
		}
		for _, d := range res[kueue.PodSetReference("a")].Domains {
			if host := d.Values[len(d.Values)-1]; host != "n-small" {
				t.Errorf("placed on label-mismatched node %q, want n-small", host)
			}
		}
	})

	t.Run("not fit when the only capacity-sufficient leaf is ineligible", func(t *testing.T) {
		// Single leaf has capacity but a taint the PodSet can't tolerate.
		trs := FlavorTASRequests{ps("a", 2, 1)}
		constraints := []podSetNodeConstraints{{selector: labels.Everything()}}
		taint := []corev1.Taint{{Key: "dedicated", Value: "other", Effect: corev1.TaintEffectNoSchedule}}
		leaves := []*leafDomain{mkLeaf("n-bad", "p", 100, taint)}
		caps := []resources.Requests{leaves[0].freeCapacity.Clone()}
		if res := s.packGroupOntoLeaves(trs, constraints, leaves, caps); res != nil {
			t.Errorf("expected nil (only leaf is ineligible), got %v", res)
		}
	})
}

func TestFindGeneralizedGroupAssignmentBestFit(t *testing.T) {
	gpu := corev1.ResourceName("nvidia.com/gpu")
	block := "cloud.com/block"
	host := corev1.LabelHostname

	// Build a snapshot with two block-level domains, each with a single leaf
	// (host). "tight" has just enough capacity for the group; "roomy" has much
	// more. BestFit should choose "tight" to keep "roomy" free.
	newLeaf := func(blockVal, hostVal string, gpus int64) *leafDomain {
		return &leafDomain{
			domain: domain{
				id:          tas.TopologyDomainID(hostVal),
				levelValues: []string{blockVal, hostVal},
			},
			freeCapacity: resources.Requests{gpu: gpus, corev1.ResourcePods: 100},
			node:         &nodeInfo{Name: hostVal, Labels: map[string]string{corev1.LabelHostname: hostVal}},
		}
	}
	tightLeaf := newLeaf("b-tight", "n-tight", 8)
	roomyLeaf := newLeaf("b-roomy", "n-roomy", 64)

	tightBlock := &domain{id: "b-tight", levelValues: []string{"b-tight"}, children: []*domain{&tightLeaf.domain}}
	roomyBlock := &domain{id: "b-roomy", levelValues: []string{"b-roomy"}, children: []*domain{&roomyLeaf.domain}}

	s := &TASFlavorSnapshot{
		levelKeys:         []string{block, host},
		isLowestLevelNode: true,
		leaves: leafDomainByID{
			tightLeaf.id: tightLeaf,
			roomyLeaf.id: roomyLeaf,
		},
		domainsPerLevel: []domainByID{
			{tightBlock.id: tightBlock, roomyBlock.id: roomyBlock}, // level 0: block
			{tightLeaf.id: &tightLeaf.domain, roomyLeaf.id: &roomyLeaf.domain},
		},
	}

	// Group: 2 PodSets x 2 GPU pods x 2 = 8 GPU total, requiring block topology.
	mkPS := func(name string, count int32, gpus int64) TASPodSetRequests {
		return TASPodSetRequests{
			PodSet: &kueue.PodSet{
				Name: kueue.PodSetReference(name),
				TopologyRequest: &kueue.PodSetTopologyRequest{
					Required: &block,
				},
			},
			Count:             count,
			SinglePodRequests: resources.Requests{gpu: gpus},
		}
	}
	trs := FlavorTASRequests{mkPS("a", 2, 2), mkPS("b", 2, 2)} // 8 GPU

	result, reason := s.findGeneralizedGroupAssignment(trs, map[tas.TopologyDomainID]resources.Requests{}, false)
	if reason != "" {
		t.Fatalf("unexpected failure: %s", reason)
	}
	// All assignments must land in the tight block (host n-tight), not roomy.
	for name, ta := range result {
		for _, d := range ta.Domains {
			if got := d.Values[len(d.Values)-1]; got != "n-tight" {
				t.Errorf("pod set %s placed on %q, want best-fit domain n-tight", name, got)
			}
		}
	}
}
