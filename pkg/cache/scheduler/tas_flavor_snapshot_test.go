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
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"

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

// TestBuildTopologyAssignmentForLevels_DropsNonPositiveStateDomains is a regression
// test for the over-subscription bug where a leaf domain whose tasUsage exceeds
// freeCapacity (typical when a higher-priority workload is admitted in preempt
// mode while a lower-priority one is still being evicted) caused
// CountInWithLimitingResource to return a negative count. That negative count
// was previously written verbatim into TopologyDomainAssignment.Count, which
// the apiserver rejects via CRD validation (`Individual` requires >= 1). This
// test asserts the defensive output-side filter in
// buildTopologyAssignmentForLevels: any domain with state <= 0 must be skipped,
// never emitted with a non-positive Count.
func TestBuildTopologyAssignmentForLevels_DropsNonPositiveStateDomains(t *testing.T) {
	levels := []string{"level-1", corev1.LabelHostname}
	s := &TASFlavorSnapshot{
		levelKeys: levels,
	}

	domains := []*domain{
		{id: "positive", levelValues: []string{"a", "x"}, state: 2},
		{id: "zero", levelValues: []string{"a", "y"}, state: 0},
		{id: "negative", levelValues: []string{"b", "z"}, state: -3},
	}

	got := s.buildTopologyAssignmentForLevels(domains, 0)

	want := &tas.TopologyAssignment{
		Levels: levels,
		Domains: []tas.TopologyDomainAssignment{
			{Values: []string{"a", "x"}, Count: 2},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("unexpected assignment (-want,+got):\n%s", diff)
	}
	for _, d := range got.Domains {
		if d.Count <= 0 {
			t.Errorf("emitted TopologyDomainAssignment with non-positive Count=%d for values=%v; "+
				"apiserver CRD validation requires Individual >= 1", d.Count, d.Values)
		}
	}
}

// TestFillInCounts_ClampsNegativeRemainingCapacity is a regression test for the
// source of the negative-Count bug. When the sum of tasUsage and assumedUsage
// exceeds freeCapacity for a leaf, remainingCapacity goes negative and
// CountInWithLimitingResource returns a negative int32. That value used to be
// stored on leaf.state and propagate through fillInCountsHelper
// (childrenCapacity += child.state) all the way to the emitted
// TopologyDomainAssignment. fillInCounts must clamp leaf.state to 0 in that
// case, since a leaf with negative remaining capacity can fit zero pods
// (not minus-one).
func TestFillInCounts_ClampsNegativeRemainingCapacity(t *testing.T) {
	levels := []string{corev1.LabelHostname}
	allocatable := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("4"),
		corev1.ResourceMemory: resource.MustParse("8Gi"),
	}
	n := node.MakeNode("host-a").
		Label(corev1.LabelHostname, "host-a").
		StatusAllocatable(allocatable).
		Obj()

	cases := map[string]struct {
		tasUsage     resources.Requests
		assumedUsage map[tas.TopologyDomainID]resources.Requests
		requests     resources.Requests
		// wantState is the value we expect leaf.state to be clamped to.
		wantState int32
	}{
		"under-subscribed: positive state preserved": {
			tasUsage: resources.Requests{
				corev1.ResourceCPU:    1000, // 1 core used; 3 free
				corev1.ResourceMemory: 1 * 1024 * 1024 * 1024,
			},
			requests: resources.Requests{
				corev1.ResourceCPU:    1000,
				corev1.ResourceMemory: 1 * 1024 * 1024 * 1024,
			},
			wantState: 3,
		},
		"exactly-subscribed via tasUsage: state == 0 (no negative)": {
			tasUsage: resources.Requests{
				corev1.ResourceCPU:    4000,
				corev1.ResourceMemory: 8 * 1024 * 1024 * 1024,
			},
			requests: resources.Requests{
				corev1.ResourceCPU:    1000,
				corev1.ResourceMemory: 1 * 1024 * 1024 * 1024,
			},
			wantState: 0,
		},
		"over-subscribed via tasUsage: clamped to 0, never negative": {
			tasUsage: resources.Requests{
				corev1.ResourceCPU:    6000, // 2 cores over capacity
				corev1.ResourceMemory: 9 * 1024 * 1024 * 1024,
			},
			requests: resources.Requests{
				corev1.ResourceCPU:    1000,
				corev1.ResourceMemory: 1 * 1024 * 1024 * 1024,
			},
			wantState: 0,
		},
		"over-subscribed via assumedUsage (preempt-mode pattern): clamped to 0": {
			tasUsage: resources.Requests{
				corev1.ResourceCPU:    3000,
				corev1.ResourceMemory: 6 * 1024 * 1024 * 1024,
			},
			assumedUsage: map[tas.TopologyDomainID]resources.Requests{
				"host-a": {
					corev1.ResourceCPU:    3000, // pushes remaining negative
					corev1.ResourceMemory: 4 * 1024 * 1024 * 1024,
				},
			},
			requests: resources.Requests{
				corev1.ResourceCPU:    1000,
				corev1.ResourceMemory: 1 * 1024 * 1024 * 1024,
			},
			wantState: 0,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, "dummy", levels, nil)
			s.addNode(newNodeInfo(n))
			s.initialize()

			leaf, ok := s.leaves["host-a"]
			if !ok {
				t.Fatalf("expected leaf domain host-a to exist")
			}
			leaf.tasUsage = tc.tasUsage

			stats := newExclusionStats()
			requirements := &topologyAssignmentPodRequirements{
				requests:     tc.requests,
				assumedUsage: tc.assumedUsage,
				selector:     labels.Everything(),
			}
			state := &findTopologyAssignmentState{
				topologyAssignmentParameters: topologyAssignmentParameters{
					sliceSize:     1,
					sliceLevelIdx: 0,
				},
				stats: stats,
			}
			s.fillInCounts(requirements, state)

			if leaf.state != tc.wantState {
				t.Errorf("leaf.state = %d, want %d (must never be negative regardless of over-subscription)", leaf.state, tc.wantState)
			}
			if leaf.state < 0 {
				t.Errorf("leaf.state = %d is negative; this would cause apiserver CRD validation to reject the resulting TopologyDomainAssignment", leaf.state)
			}

			assignment := s.buildAssignment([]*domain{&leaf.domain})
			for _, d := range assignment.Domains {
				if d.Count <= 0 {
					t.Errorf("emitted TopologyDomainAssignment.Count = %d for values=%v; must be >= 1 (output-side filter regressed)", d.Count, d.Values)
				}
			}
		})
	}
}
