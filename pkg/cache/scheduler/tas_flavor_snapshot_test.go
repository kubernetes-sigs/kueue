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
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestFreeCapacityPerDomain(t *testing.T) {
	snapshot := &TASFlavorSnapshot{
		leaves: leafDomainByID{
			"domain2": &leafDomain{
				freeCapacity: resources.MapRequests{
					corev1.ResourceCPU:    1000,
					corev1.ResourceMemory: 2 * 1024 * 1024 * 1024, // 2 GiB
				},
				tasUsage: resources.MapRequests{
					corev1.ResourceMemory: 1 * 1024 * 1024 * 1024, // 1 GiB
					corev1.ResourceCPU:    500,
				},
			},
			"domain1": &leafDomain{
				freeCapacity: resources.MapRequests{
					corev1.ResourceMemory: 4 * 1024 * 1024 * 1024, // 4 GiB
					corev1.ResourceCPU:    2000,
					"nvidia.com/gpu":      1,
				},
				tasUsage: resources.MapRequests{
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
			s := newTASFlavorSnapshot(log, "dummy", levels, nil, &defaultChecker{})
			for i := range nodes {
				s.addNode(&nodes[i])
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
			s := newTASFlavorSnapshot(log, "dummy", levels, nil, &defaultChecker{})
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
			s := newTASFlavorSnapshot(log, "test", levels, nil, &defaultChecker{})

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
			s := newTASFlavorSnapshot(log, "test", levels, nil, &defaultChecker{})

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

func TestCompareDomainLevelValues(t *testing.T) {
	_, log := utiltesting.ContextWithLog(t)
	hostnameLevels := []string{"block", "rack", corev1.LabelHostname}
	nonHostnameLevels := []string{"block", "rack"}

	parent1 := &domain{id: "b1-r1", levelValues: []string{"b1", "r1"}}
	parent2 := &domain{id: "b1-r2", levelValues: []string{"b1", "r2"}}

	testCases := map[string]struct {
		levels []string
		a      *domain
		b      *domain
		want   int
	}{
		"isLowestLevelNode with same-parent sibling domains: ascending by hostname": {
			levels: hostnameLevels,
			a:      &domain{id: "node-a", parent: parent1, levelValues: []string{"b1", "r1", "node-a"}},
			b:      &domain{id: "node-b", parent: parent1, levelValues: []string{"b1", "r1", "node-b"}},
			want:   -1,
		},
		"isLowestLevelNode with same-parent sibling domains: descending by hostname": {
			levels: hostnameLevels,
			a:      &domain{id: "node-b", parent: parent1, levelValues: []string{"b1", "r1", "node-b"}},
			b:      &domain{id: "node-a", parent: parent1, levelValues: []string{"b1", "r1", "node-a"}},
			want:   1,
		},
		"isLowestLevelNode with same-parent sibling domains: equal hostname": {
			levels: hostnameLevels,
			a:      &domain{id: "node-a", parent: parent1, levelValues: []string{"b1", "r1", "node-a"}},
			b:      &domain{id: "node-a", parent: parent1, levelValues: []string{"b1", "r1", "node-a"}},
			want:   0,
		},
		"fallback comparator: multi-level inputs with different parents sorted lexicographically across levels": {
			levels: hostnameLevels,
			a:      &domain{id: "node-z", parent: parent1, levelValues: []string{"b1", "r1", "node-z"}},
			b:      &domain{id: "node-a", parent: parent2, levelValues: []string{"b1", "r2", "node-a"}},
			want:   -1,
		},
		"fallback comparator: non-hostname levels sorted lexicographically across levels": {
			levels: nonHostnameLevels,
			a:      &domain{id: "b1-r1", levelValues: []string{"b1", "r1"}},
			b:      &domain{id: "b1-r2", levelValues: []string{"b1", "r2"}},
			want:   -1,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			s := newTASFlavorSnapshot(log, "test", tc.levels, nil, &defaultChecker{})
			got := s.compareDomainLevelValues(tc.a, tc.b)
			if (got < 0 && tc.want >= 0) || (got > 0 && tc.want <= 0) || (got == 0 && tc.want != 0) {
				t.Errorf("compareDomainLevelValues() = %d, want sign matching %d", got, tc.want)
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
	singlePodRequests := resources.MapRequests{
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
				"node-a": resources.MapRequests{
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
				"node-a": resources.MapRequests{
					corev1.ResourceCPU:    2000,
					corev1.ResourceMemory: 2048,
					corev1.ResourcePods:   2,
				},
				"node-b": resources.MapRequests{
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
				"node-a": resources.MapRequests{
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
				SinglePodRequests: resources.MapRequests{
					corev1.ResourceCPU:    500,
					corev1.ResourceMemory: 2048,
				},
			},
			want: map[tas.TopologyDomainID]resources.Requests{
				"node-a": resources.MapRequests{
					corev1.ResourceCPU:    1500,
					corev1.ResourceMemory: 2048,
					corev1.ResourcePods:   2,
				},
				"node-b": resources.MapRequests{
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
				SinglePodRequests: resources.MapRequests{
					corev1.ResourceCPU:    250,
					corev1.ResourceMemory: 512,
				},
			},
			want: map[tas.TopologyDomainID]resources.Requests{
				"node-a": resources.MapRequests{
					corev1.ResourceCPU:    750,
					corev1.ResourceMemory: 1536,
					corev1.ResourcePods:   3,
				},
			},
		},
	}

	equateRequests := cmp.Transformer("Requests", func(r resources.Requests) map[corev1.ResourceName]int64 {
		if r == nil {
			return nil
		}
		m := make(map[corev1.ResourceName]int64)
		r.ForEach(func(name corev1.ResourceName, val int64) {
			m[name] = val
		})
		return m
	})

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			addAssumedUsage(tc.assumedUsage, tc.assignment, tc.tasRequests)
			if diff := cmp.Diff(tc.want, tc.assumedUsage, equateRequests); diff != "" {
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

func TestTASCachingRemainingResourcesFeatureGate(t *testing.T) {
	for _, enableCaching := range []bool{true, false} {
		t.Run(fmt.Sprintf("enableCaching=%t", enableCaching), func(t *testing.T) {
			g := gomega.NewWithT(t)
			features.SetFeatureGateDuringTest(t, features.TASCachingRemainingResources, enableCaching)

			_, log := utiltesting.ContextWithLog(t)
			snapshot := newTASFlavorSnapshot(log, "tas-topology", []string{"hostname"}, nil, &defaultChecker{})
			nodeObj := node.MakeNode("node-a").
				Label("hostname", "node-a").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("8"),
					corev1.ResourceMemory: resource.MustParse("10Gi"),
				}).
				Ready().
				Obj()
			domainID := snapshot.addNode(nodeObj)

			leaf := snapshot.leaves[domainID]
			g.Expect(leaf).ToNot(gomega.BeNil())

			flavorUsage := workload.TASFlavorUsage{
				{
					Values: []string{"node-a"},
					SinglePodRequests: resources.MapRequests{
						corev1.ResourceCPU: 5000,
					},
					Count: 1,
				},
			}

			// Warm the Fits cache before adding TAS usage
			g.Expect(snapshot.Fits(flavorUsage)).To(gomega.BeTrue())

			// Add TAS usage of 4 CPU (4000m), leaving 4 CPU (8000m - 4000m = 4000m) remaining
			usage := resources.MapRequests{
				corev1.ResourceCPU: 4000,
			}
			snapshot.updateTASUsage(domainID, usage, add, 1)

			// Fits should now return false because 5 CPU > 4 CPU remaining
			g.Expect(snapshot.Fits(flavorUsage)).To(gomega.BeFalse())

			// Remove TAS usage
			snapshot.updateTASUsage(domainID, usage, subtract, 1)

			// Fits should now return true again after cache invalidation / re-evaluation
			g.Expect(snapshot.Fits(flavorUsage)).To(gomega.BeTrue())
		})
	}
}

func TestFindReplacementAssignmentZeroCountSkipsInvalidSliceSize(t *testing.T) {
	tests := map[string]struct {
		tr                        *TASPodSetRequests
		existingAssignment        *tas.TopologyAssignment
		wl                        *kueue.Workload
		assumedUsage              map[tas.TopologyDomainID]resources.Requests
		wantReason                string
		wantNewAssignment         *tas.TopologyAssignment
		wantReplacementAssignment *tas.TopologyAssignment
	}{
		"annotation-based slice size zero is skipped for zero replacement count": {
			tr: &TASPodSetRequests{
				PodSet: &kueue.PodSet{
					Name: "main",
					TopologyRequest: &kueue.PodSetTopologyRequest{
						PodSetSliceRequiredTopology: new(string(corev1.LabelHostname)),
						PodSetSliceSize:             new(int32(0)),
					},
				},
			},
			existingAssignment: &tas.TopologyAssignment{
				Levels: []string{corev1.LabelHostname},
				Domains: []tas.TopologyDomainAssignment{{
					Values: []string{"node-a"},
					Count:  0,
				}},
			},
			wl: &kueue.Workload{
				Status: kueue.WorkloadStatus{
					UnhealthyNodes: []kueue.UnhealthyNode{{Name: "node-a"}},
				},
			},
			assumedUsage: make(map[tas.TopologyDomainID]resources.Requests),
			wantReason:   "",
			wantNewAssignment: &tas.TopologyAssignment{
				Levels:  []string{corev1.LabelHostname},
				Domains: []tas.TopologyDomainAssignment{},
			},
			wantReplacementAssignment: &tas.TopologyAssignment{
				Levels:  []string{corev1.LabelHostname},
				Domains: []tas.TopologyDomainAssignment{},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, "test", []string{corev1.LabelHostname}, nil, &defaultChecker{})

			newAssignment, replacementAssignment, reason := s.findReplacementAssignment(ctx, tc.tr, tc.existingAssignment, tc.wl, tc.assumedUsage)
			if diff := cmp.Diff(tc.wantReason, reason); diff != "" {
				t.Fatalf("unexpected failure reason (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantNewAssignment, newAssignment); diff != "" {
				t.Fatalf("unexpected new assignment (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantReplacementAssignment, replacementAssignment); diff != "" {
				t.Fatalf("unexpected replacement assignment (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestResolveSliceSizeOrReason(t *testing.T) {
	tests := map[string]struct {
		request    *kueue.PodSetTopologyRequest
		wantSize   int32
		wantReason string
	}{
		"nil request - should return 1 with no reason": {
			request:    nil,
			wantSize:   1,
			wantReason: "",
		},
		"no topology request - should return 1 with no reason": {
			request:    &kueue.PodSetTopologyRequest{},
			wantSize:   1,
			wantReason: "",
		},
		"constraints present - should return first constraint size": {
			request: &kueue.PodSetTopologyRequest{
				PodsetSliceRequiredTopologyConstraints: []kueue.PodsetSliceRequiredTopologyConstraint{{
					Topology: corev1.LabelHostname,
					Size:     4,
				}},
			},
			wantSize:   4,
			wantReason: "",
		},
		"constraints with zero size - should return 0 with reason": {
			request: &kueue.PodSetTopologyRequest{
				PodsetSliceRequiredTopologyConstraints: []kueue.PodsetSliceRequiredTopologyConstraint{{
					Topology: corev1.LabelHostname,
					Size:     0,
				}},
			},
			wantSize:   0,
			wantReason: sliceSizeNotProvidedReason,
		},
		"constraints with zero size still return reason (count handled by caller)": {
			request: &kueue.PodSetTopologyRequest{
				PodsetSliceRequiredTopologyConstraints: []kueue.PodsetSliceRequiredTopologyConstraint{{
					Topology: corev1.LabelHostname,
					Size:     0,
				}},
			},
			wantSize:   0,
			wantReason: sliceSizeNotProvidedReason,
		},
		"constraints with negative size - should return 0 with reason": {
			request: &kueue.PodSetTopologyRequest{
				PodsetSliceRequiredTopologyConstraints: []kueue.PodsetSliceRequiredTopologyConstraint{{
					Topology: corev1.LabelHostname,
					Size:     -2,
				}},
			},
			wantSize:   0,
			wantReason: sliceSizeNotProvidedReason,
		},
		"constraints with invalid inner-layer size - should return 0 with reason": {
			request: &kueue.PodSetTopologyRequest{
				PodsetSliceRequiredTopologyConstraints: []kueue.PodsetSliceRequiredTopologyConstraint{
					{Topology: "rack", Size: 8},
					{Topology: corev1.LabelHostname, Size: 0},
				},
			},
			wantSize:   0,
			wantReason: sliceSizeNotProvidedReason,
		},
		"annotation-based nil slice size - should return 0 with reason": {
			request: &kueue.PodSetTopologyRequest{
				PodSetSliceRequiredTopology: new(string("kubernetes.io/zone")),
			},
			wantSize:   0,
			wantReason: sliceSizeNotProvidedReason,
		},
		"annotation-based zero slice size - should return 0 with reason": {
			request: &kueue.PodSetTopologyRequest{
				PodSetSliceRequiredTopology: new(string("kubernetes.io/zone")),
				PodSetSliceSize:             new(int32(0)),
			},
			wantSize:   0,
			wantReason: sliceSizeNotProvidedReason,
		},
		"annotation-based negative slice size - should return 0 with reason": {
			request: &kueue.PodSetTopologyRequest{
				PodSetSliceRequiredTopology: new(string("kubernetes.io/zone")),
				PodSetSliceSize:             new(int32(-2)),
			},
			wantSize:   0,
			wantReason: sliceSizeNotProvidedReason,
		},
		"annotation-based positive slice size - should accept as-is": {
			request: &kueue.PodSetTopologyRequest{
				PodSetSliceRequiredTopology: new(string("kubernetes.io/zone")),
				PodSetSliceSize:             new(int32(4)),
			},
			wantSize:   4,
			wantReason: "",
		},
		"constraints take precedence over legacy positive": {
			request: &kueue.PodSetTopologyRequest{
				PodSetSliceRequiredTopology: new(string("kubernetes.io/zone")),
				PodSetSliceSize:             new(int32(7)),
				PodsetSliceRequiredTopologyConstraints: []kueue.PodsetSliceRequiredTopologyConstraint{{
					Topology: corev1.LabelHostname,
					Size:     3,
				}},
			},
			wantSize:   3,
			wantReason: "",
		},
		"constraints take precedence over legacy negative": {
			request: &kueue.PodSetTopologyRequest{
				PodSetSliceRequiredTopology: new(string("kubernetes.io/zone")),
				PodSetSliceSize:             new(int32(-7)),
				PodsetSliceRequiredTopologyConstraints: []kueue.PodsetSliceRequiredTopologyConstraint{{
					Topology: corev1.LabelHostname,
					Size:     3,
				}},
			},
			wantSize:   3,
			wantReason: "",
		},
		"negative constraints take precedence over legacy zero": {
			request: &kueue.PodSetTopologyRequest{
				PodSetSliceRequiredTopology: new(string("kubernetes.io/zone")),
				PodSetSliceSize:             new(int32(0)),
				PodsetSliceRequiredTopologyConstraints: []kueue.PodsetSliceRequiredTopologyConstraint{{
					Topology: corev1.LabelHostname,
					Size:     -3,
				}},
			},
			wantSize:   0,
			wantReason: sliceSizeNotProvidedReason,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			gotSize, gotReason := resolveSliceSizeOrReason(tc.request)
			if diff := cmp.Diff(tc.wantSize, gotSize); diff != "" {
				t.Errorf("resolveSliceSizeOrReason() size (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantReason, gotReason); diff != "" {
				t.Errorf("resolveSliceSizeOrReason() reason (-want,+got):\n%s", diff)
			}
		})
	}
}
