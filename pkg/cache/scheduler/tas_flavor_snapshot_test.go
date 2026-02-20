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

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
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
			for _, node := range nodes {
				s.addNode(node)
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
				Required: ptr.To("level-1"),
			},
			want: true,
		},
		"required – invalid level": {
			podSetTopologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To("invalid-level"),
			},
			want: false,
		},
		"preferred": {
			podSetTopologyRequest: &kueue.PodSetTopologyRequest{
				Preferred: ptr.To("level-1"),
			},
			want: true,
		},
		"preferred – invalid level": {
			podSetTopologyRequest: &kueue.PodSetTopologyRequest{
				Preferred: ptr.To("invalid-level"),
			},
			want: false,
		},
		"unconstrained": {
			podSetTopologyRequest: &kueue.PodSetTopologyRequest{
				Unconstrained: ptr.To(true),
			},
			want: true,
		},
		"slice-only": {
			podSetTopologyRequest: &kueue.PodSetTopologyRequest{
				PodSetSliceRequiredTopology: ptr.To("level-1"),
			},
			want: true,
		},
		"slice-only – invalid level": {
			podSetTopologyRequest: &kueue.PodSetTopologyRequest{
				PodSetSliceRequiredTopology: ptr.To("invalid-level"),
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
		domains       []*domain
		unconstrained bool
		wantOrder     []string
	}{
		"leaderState descending: domains that can host leader come first": {
			domains: []*domain{
				{id: "no-leader", leaderState: 0, sliceStateWithLeader: 10, stateWithLeader: 10, levelValues: []string{"a"}},
				{id: "has-leader", leaderState: 1, sliceStateWithLeader: 1, stateWithLeader: 1, levelValues: []string{"b"}},
			},
			unconstrained: false,
			wantOrder:     []string{"has-leader", "no-leader"},
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
