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
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	zaplog "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	crzap "sigs.k8s.io/controller-runtime/pkg/log/zap"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
)

type testDomainSpec struct {
	domain domain
	state  domainState
}

func addDomainsWithState(s *TASFlavorSnapshot, specs []testDomainSpec) []*domain {
	result := make([]*domain, len(specs))
	for i, spec := range specs {
		d := spec.domain
		d.idx = len(s.domainStates)
		s.domainStates = append(s.domainStates, spec.state)
		result[i] = &d
	}
	return result
}

func newFreeCapacityTestSnapshot(capacities map[tas.TopologyDomainID]leafCapacity) *TASFlavorSnapshot {
	leaves := make(leafDomainByID, len(capacities))
	leafCapacities := make([]leafCapacity, 0, len(capacities))
	for id, capacity := range capacities {
		leaves[id] = &leafDomain{domain: domain{id: id}, leafIdx: len(leafCapacities)}
		leafCapacities = append(leafCapacities, capacity)
	}
	return &TASFlavorSnapshot{
		topologyTree:   &topologyTree{leaves: leaves},
		leafCapacities: leafCapacities,
	}
}

func TestFreeCapacityPerDomain(t *testing.T) {
	cases := map[string]struct {
		// snapshot is a function, so that the requests are built with the
		// VectorizedResourceRequests feature gate already set.
		snapshot func() *TASFlavorSnapshot
		expected string
	}{
		"domains with free capacity and TAS usage": {
			snapshot: func() *TASFlavorSnapshot {
				return newFreeCapacityTestSnapshot(map[tas.TopologyDomainID]leafCapacity{
					"domain2": {
						freeCapacity: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
							corev1.ResourceCPU:    1000,
							corev1.ResourceMemory: 2 * 1024 * 1024 * 1024, // 2 GiB
						}),
						tasUsage: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
							corev1.ResourceMemory: 1 * 1024 * 1024 * 1024, // 1 GiB
							corev1.ResourceCPU:    500,
						}),
					},
					"domain1": {
						freeCapacity: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
							corev1.ResourceMemory: 4 * 1024 * 1024 * 1024, // 4 GiB
							corev1.ResourceCPU:    2000,
							"nvidia.com/gpu":      1,
						}),
						tasUsage: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
							corev1.ResourceCPU:    500,
							"nvidia.com/gpu":      1,
							corev1.ResourceMemory: 2 * 1024 * 1024 * 1024, // 1 GiB
						}),
					},
				})
			},
			expected: `{"domain1":{"freeCapacity":{"cpu":"2","memory":"4Gi","nvidia.com/gpu":"1"},"tasUsage":{"cpu":"500m","memory":"2Gi","nvidia.com/gpu":"1"}},"domain2":{"freeCapacity":{"cpu":"1","memory":"2Gi"},"tasUsage":{"cpu":"500m","memory":"1Gi"}}}`,
		},
		"domain with free capacity, but without TAS usage": {
			snapshot: func() *TASFlavorSnapshot {
				return newFreeCapacityTestSnapshot(map[tas.TopologyDomainID]leafCapacity{
					"domain1": {
						freeCapacity: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
							corev1.ResourceCPU: 1000,
						}),
						// tasUsage is left nil, as there is no TAS workload
						// admitted in the domain.
					},
				})
			},
			expected: `{"domain1":{"freeCapacity":{"cpu":"1"},"tasUsage":{}}}`,
		},
		"domain without free capacity and without TAS usage": {
			snapshot: func() *TASFlavorSnapshot {
				return newFreeCapacityTestSnapshot(map[tas.TopologyDomainID]leafCapacity{"domain1": {}})
			},
			expected: `{"domain1":{"freeCapacity":{},"tasUsage":{}}}`,
		},
		"snapshot without domains": {
			snapshot: func() *TASFlavorSnapshot {
				return newFreeCapacityTestSnapshot(nil)
			},
			expected: `{}`,
		},
	}

	for name, tc := range cases {
		for _, enableVectorizedRequests := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s, VectorizedResourceRequests=%t", name, enableVectorizedRequests), func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.VectorizedResourceRequests, enableVectorizedRequests)

				snapshot := tc.snapshot()
				var wantErr error

				got, gotErr := snapshot.SerializeFreeCapacityPerDomain()
				if diff := cmp.Diff(wantErr, gotErr, cmpopts.EquateErrors()); len(diff) != 0 {
					t.Errorf("Unexpected error (-want,+got):\n%s", diff)
				}
				if diff := cmp.Diff(tc.expected, got); diff != "" {
					t.Errorf("SerializeFreeCapacityPerDomain() mismatch (-expected +got):\n%s", diff)
				}
			})
		}
	}
}

// Usage recorded against a domain the snapshot does not hold is skipped rather
// than panicking. This happens when the backing node was deleted or stopped
// being Ready between the Workload being admitted and the snapshot being taken.
func TestApplyTASUsageSkipsDomainTheSnapshotDoesNotHold(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TASNodeFeasibilityForAllLevels, true)
	const rackLabel = "cloud.provider.com/topology-rack"
	_, log := utiltesting.ContextWithLog(t)

	rackNode := node.MakeNode("n1").
		Label(rackLabel, "r1").
		StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}).
		Ready().
		Obj()
	tree := newTopologyTree([]string{rackLabel}, []*corev1.Node{rackNode}, 0)
	snapshot := newTASFlavorSnapshot(log, "tas-topology", tree, nil, newDefaultSimulatorSnapshot())

	oneCPU := resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 1000})
	snapshot.updateTASUsage(tas.DomainID([]string{"gone"}), oneCPU, add, 1)

	if got := len(snapshot.domainTASUsage); got != 0 {
		t.Errorf("usage recorded for %d domains, want none", got)
	}
}

// A virtual topology keeps usage on the domains rather than the leaves, so the
// dump must report those domains or it reads as if nothing is used.
func TestFreeCapacityPerDomainReportsUsageDomains(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TASNodeFeasibilityForAllLevels, true)
	const rackLabel = "cloud.provider.com/topology-rack"
	_, log := utiltesting.ContextWithLog(t)

	rackNode := node.MakeNode("").
		Label(rackLabel, "r1").
		StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}).
		Ready()
	tree := newTopologyTree([]string{rackLabel}, []*corev1.Node{
		rackNode.Clone().Name("n1").Obj(),
		rackNode.Clone().Name("n2").Obj(),
	}, 0)
	snapshot := newTASFlavorSnapshot(log, "tas-topology", tree, nil, newDefaultSimulatorSnapshot(),
		withResourceFormatter(resources.NewResourceFormatter()))
	snapshot.updateTASUsage(tas.DomainID([]string{"r1"}),
		resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 1000}), add, 1)

	got, err := snapshot.SerializeFreeCapacityPerDomain()
	if err != nil {
		t.Fatalf("SerializeFreeCapacityPerDomain() error = %v", err)
	}
	want := `"r1":{"freeCapacity":{"cpu":"4"},"tasUsage":{"cpu":"1","pods":"1"}}`
	if !strings.Contains(got, want) {
		t.Errorf("SerializeFreeCapacityPerDomain() = %s, want it to contain %s", got, want)
	}
}

func TestIsTopologyAssignmentStale(t *testing.T) {
	const blockLabel = "cloud.provider.com/topology-block"
	const rackLabel = "cloud.provider.com/topology-rack"

	hostnameLowest := newTopologyTree(
		[]string{blockLabel, corev1.LabelHostname},
		[]*corev1.Node{
			node.MakeNode("n1").
				Label(blockLabel, "b1").
				Label(corev1.LabelHostname, "n1").
				Obj(),
		},
		0,
	)
	rackLowest := newTopologyTree(
		[]string{blockLabel, rackLabel},
		[]*corev1.Node{
			node.MakeNode("n1").
				Label(blockLabel, "b1").
				Label(rackLabel, "r1").
				Obj(),
		},
		0,
	)

	cases := map[string]struct {
		tree            *topologyTree
		assignment      *tas.TopologyAssignment
		wantStale       bool
		wantStaleDomain string
	}{
		"existing hostname leaf is not stale": {
			tree: hostnameLowest,
			assignment: &tas.TopologyAssignment{
				Levels:  []string{corev1.LabelHostname},
				Domains: []tas.TopologyDomainAssignment{{Values: []string{"n1"}}},
			},
		},
		"missing hostname leaf is stale": {
			tree: hostnameLowest,
			assignment: &tas.TopologyAssignment{
				Levels:  []string{corev1.LabelHostname},
				Domains: []tas.TopologyDomainAssignment{{Values: []string{"n2"}}},
			},
			wantStale:       true,
			wantStaleDomain: "n2",
		},
		"deleted node is stale even when its hostname matches an existing root domain ID": {
			tree: hostnameLowest,
			assignment: &tas.TopologyAssignment{
				Levels:  []string{corev1.LabelHostname},
				Domains: []tas.TopologyDomainAssignment{{Values: []string{"b1"}}},
			},
			wantStale:       true,
			wantStaleDomain: "b1",
		},
		"existing non-hostname leaf is not stale": {
			tree: rackLowest,
			assignment: &tas.TopologyAssignment{
				Levels:  []string{blockLabel, rackLabel},
				Domains: []tas.TopologyDomainAssignment{{Values: []string{"b1", "r1"}}},
			},
		},
		"missing non-hostname leaf is stale": {
			tree: rackLowest,
			assignment: &tas.TopologyAssignment{
				Levels:  []string{blockLabel, rackLabel},
				Domains: []tas.TopologyDomainAssignment{{Values: []string{"b1", "r2"}}},
			},
			wantStale:       true,
			wantStaleDomain: "b1",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			snapshot := &TASFlavorSnapshot{topologyTree: tc.tree}
			gotStale, gotStaleDomain := snapshot.IsTopologyAssignmentStale(tc.assignment)
			if gotStale != tc.wantStale {
				t.Errorf("IsTopologyAssignmentStale() stale = %t, want %t", gotStale, tc.wantStale)
			}
			if gotStaleDomain != tc.wantStaleDomain {
				t.Errorf("IsTopologyAssignmentStale() stale domain = %q, want %q", gotStaleDomain, tc.wantStaleDomain)
			}
		})
	}
}

func TestMergeTopologyAssignments(t *testing.T) {
	nodes := []*corev1.Node{
		node.MakeNode("x").Label("level-1", "a").Label("level-2", "b").Obj(),
		node.MakeNode("y").Label("level-1", "a").Label("level-2", "c").Obj(),
		node.MakeNode("z").Label("level-1", "d").Label("level-2", "e").Obj(),
		node.MakeNode("w").Label("level-1", "d").Label("level-2", "f").Obj(),
	}
	levels := []string{"level-1", "level-2"}
	tree := newTopologyTree(levels, nodes, 0)

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
			s := newTASFlavorSnapshot(log, "dummy", tree, nil, newDefaultSimulatorSnapshot())

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
			s := newTASFlavorSnapshot(log, "dummy", newTopologyTree(levels, nil, 0), nil, newDefaultSimulatorSnapshot())
			got := s.HasLevel(tc.podSetTopologyRequest)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("unexpected HasLevel result (-want,+got): %s", diff)
			}
		})
	}
}

// TestSortedDomainsWithLeader verifies the sorting criteria (in order of priority):
// 1. leaderCount - descending (always)
// 2. sliceCountWithLeader - descending (BestFit) or ascending (LeastFreeCapacity)
// 3. podCountWithLeader - ascending (always, as tiebreaker)
// 4. levelValues - ascending (always, as final tiebreaker)
func TestSortedDomainsWithLeader(t *testing.T) {
	levels := []string{"block"}

	testCases := map[string]struct {
		domains                              []testDomainSpec
		unconstrained                        bool
		enableTASPreferredSchedulingAffinity bool
		wantOrder                            []string
	}{
		"affinityScore descending: higher affinity score comes first": {
			enableTASPreferredSchedulingAffinity: true,
			domains: []testDomainSpec{
				{
					domain: domain{id: "low-affinity", levelValues: []string{"a"}},
					state: domainState{
						affinityScore:        10,
						leaderCount:          1,
						sliceCountWithLeader: 5,
						podCountWithLeader:   10,
					},
				},
				{
					domain: domain{id: "high-affinity", levelValues: []string{"b"}},
					state: domainState{
						affinityScore:        100,
						leaderCount:          1,
						sliceCountWithLeader: 5,
						podCountWithLeader:   10,
					},
				},
			},
			unconstrained: false,
			wantOrder:     []string{"high-affinity", "low-affinity"},
		},
		"affinityScore ignored when feature gate is disabled": {
			enableTASPreferredSchedulingAffinity: false,
			domains: []testDomainSpec{
				{
					domain: domain{id: "low-affinity", levelValues: []string{"a"}},
					state: domainState{
						affinityScore:        10,
						leaderCount:          1,
						sliceCountWithLeader: 5,
						podCountWithLeader:   10,
					},
				},
				{
					domain: domain{id: "high-affinity", levelValues: []string{"b"}},
					state: domainState{
						affinityScore:        100,
						leaderCount:          1,
						sliceCountWithLeader: 5,
						podCountWithLeader:   10,
					},
				},
			},
			unconstrained: false,
			wantOrder:     []string{"low-affinity", "high-affinity"},
		},
		"leaderCount descending: domains that can host leader come first": {
			domains: []testDomainSpec{
				{
					domain: domain{id: "no-leader", levelValues: []string{"a"}},
					state: domainState{
						leaderCount:          0,
						sliceCountWithLeader: 10,
						podCountWithLeader:   10,
					},
				},
				{
					domain: domain{id: "has-leader", levelValues: []string{"b"}},
					state: domainState{
						leaderCount:          1,
						sliceCountWithLeader: 1,
						podCountWithLeader:   1,
					},
				},
			},
			unconstrained: false,
			wantOrder:     []string{"has-leader", "no-leader"},
		},
		"leader capability prioritized over preferred affinity": {
			enableTASPreferredSchedulingAffinity: true,
			domains: []testDomainSpec{
				{
					domain: domain{id: "preferred-no-leader", levelValues: []string{"a"}},
					state: domainState{
						affinityScore:        100,
						leaderCount:          0,
						sliceCountWithLeader: 0,
						podCountWithLeader:   0,
					},
				},
				{
					domain: domain{id: "non-preferred-has-leader", levelValues: []string{"b"}},
					state: domainState{
						affinityScore:        10,
						leaderCount:          1,
						sliceCountWithLeader: 5,
						podCountWithLeader:   10,
					},
				},
			},
			unconstrained: false,
			wantOrder:     []string{"non-preferred-has-leader", "preferred-no-leader"},
		},
		"BestFit: sliceCountWithLeader descending": {
			domains: []testDomainSpec{
				{
					domain: domain{id: "a", levelValues: []string{"a"}},
					state: domainState{
						leaderCount:          1,
						sliceCountWithLeader: 3,
						podCountWithLeader:   1,
					},
				},
				{
					domain: domain{id: "b", levelValues: []string{"b"}},
					state: domainState{
						leaderCount:          1,
						sliceCountWithLeader: 1,
						podCountWithLeader:   1,
					},
				},
				{
					domain: domain{id: "c", levelValues: []string{"c"}},
					state: domainState{
						leaderCount:          1,
						sliceCountWithLeader: 2,
						podCountWithLeader:   1,
					},
				},
			},
			unconstrained: false,
			wantOrder:     []string{"a", "c", "b"},
		},
		"LeastFreeCapacity: sliceCountWithLeader ascending": {
			domains: []testDomainSpec{
				{
					domain: domain{id: "a", levelValues: []string{"a"}},
					state: domainState{
						leaderCount:          1,
						sliceCountWithLeader: 3,
						podCountWithLeader:   1,
					},
				},
				{
					domain: domain{id: "b", levelValues: []string{"b"}},
					state: domainState{
						leaderCount:          1,
						sliceCountWithLeader: 1,
						podCountWithLeader:   1,
					},
				},
				{
					domain: domain{id: "c", levelValues: []string{"c"}},
					state: domainState{
						leaderCount:          1,
						sliceCountWithLeader: 2,
						podCountWithLeader:   1,
					},
				},
			},
			unconstrained: true,
			wantOrder:     []string{"b", "c", "a"},
		},
		"BestFit: podCountWithLeader ascending as tiebreaker": {
			domains: []testDomainSpec{
				{
					domain: domain{id: "large", levelValues: []string{"a"}},
					state: domainState{
						leaderCount:          1,
						sliceCountWithLeader: 5,
						podCountWithLeader:   100,
					},
				},
				{
					domain: domain{id: "small", levelValues: []string{"b"}},
					state: domainState{
						leaderCount:          1,
						sliceCountWithLeader: 5,
						podCountWithLeader:   10,
					},
				},
				{
					domain: domain{id: "medium", levelValues: []string{"c"}},
					state: domainState{
						leaderCount:          1,
						sliceCountWithLeader: 5,
						podCountWithLeader:   50,
					},
				},
			},
			unconstrained: false,
			wantOrder:     []string{"small", "medium", "large"},
		},
		"LeastFreeCapacity: podCountWithLeader ascending as tiebreaker": {
			domains: []testDomainSpec{
				{
					domain: domain{id: "large", levelValues: []string{"a"}},
					state: domainState{
						leaderCount:          1,
						sliceCountWithLeader: 5,
						podCountWithLeader:   100,
					},
				},
				{
					domain: domain{id: "small", levelValues: []string{"b"}},
					state: domainState{
						leaderCount:          1,
						sliceCountWithLeader: 5,
						podCountWithLeader:   10,
					},
				},
				{
					domain: domain{id: "medium", levelValues: []string{"c"}},
					state: domainState{
						leaderCount:          1,
						sliceCountWithLeader: 5,
						podCountWithLeader:   50,
					},
				},
			},
			unconstrained: true,
			wantOrder:     []string{"small", "medium", "large"},
		},
		"levelValues ascending as final tiebreaker": {
			domains: []testDomainSpec{
				{
					domain: domain{id: "c", levelValues: []string{"c"}},
					state: domainState{
						leaderCount:          1,
						sliceCountWithLeader: 5,
						podCountWithLeader:   10,
					},
				},
				{
					domain: domain{id: "a", levelValues: []string{"a"}},
					state: domainState{
						leaderCount:          1,
						sliceCountWithLeader: 5,
						podCountWithLeader:   10,
					},
				},
				{
					domain: domain{id: "b", levelValues: []string{"b"}},
					state: domainState{
						leaderCount:          1,
						sliceCountWithLeader: 5,
						podCountWithLeader:   10,
					},
				},
			},
			unconstrained: false,
			wantOrder:     []string{"a", "b", "c"},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TASRespectNodeAffinityPreferred, tc.enableTASPreferredSchedulingAffinity)
			_, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, "test", newTopologyTree(levels, nil, 0), nil, newDefaultSimulatorSnapshot())

			sorted := s.sortedDomainsWithLeader(addDomainsWithState(s, tc.domains), tc.unconstrained)

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
// 2. sliceCount - descending (BestFit) or ascending (LeastFreeCapacity)
// 3. podCount - ascending (always, as tiebreaker)
// 4. levelValues - ascending (always, as final tiebreaker)
func TestSortedDomains(t *testing.T) {
	levels := []string{"block"}

	testCases := map[string]struct {
		domains                              []testDomainSpec
		unconstrained                        bool
		enableTASPreferredSchedulingAffinity bool
		wantOrder                            []string
	}{
		"affinityScore descending: higher affinity score comes first": {
			enableTASPreferredSchedulingAffinity: true,
			domains: []testDomainSpec{
				{
					domain: domain{id: "low-affinity", levelValues: []string{"a"}},
					state: domainState{
						affinityScore: 10,
						sliceCount:    5,
						podCount:      10,
					},
				},
				{
					domain: domain{id: "high-affinity", levelValues: []string{"b"}},
					state: domainState{
						affinityScore: 100,
						sliceCount:    5,
						podCount:      10,
					},
				},
			},
			unconstrained: false,
			wantOrder:     []string{"high-affinity", "low-affinity"},
		},
		"affinityScore ignored when feature gate is disabled": {
			enableTASPreferredSchedulingAffinity: false,
			domains: []testDomainSpec{
				{
					domain: domain{id: "low-affinity", levelValues: []string{"a"}},
					state: domainState{
						affinityScore: 10,
						sliceCount:    5,
						podCount:      10,
					},
				},
				{
					domain: domain{id: "high-affinity", levelValues: []string{"b"}},
					state: domainState{
						affinityScore: 100,
						sliceCount:    5,
						podCount:      10,
					},
				},
			},
			unconstrained: false,
			wantOrder:     []string{"low-affinity", "high-affinity"},
		},
		"BestFit: sliceCount descending": {
			domains: []testDomainSpec{
				{
					domain: domain{id: "a", levelValues: []string{"a"}},
					state: domainState{
						sliceCount: 3,
						podCount:   1,
					},
				},
				{
					domain: domain{id: "b", levelValues: []string{"b"}},
					state: domainState{
						sliceCount: 1,
						podCount:   1,
					},
				},
				{
					domain: domain{id: "c", levelValues: []string{"c"}},
					state: domainState{
						sliceCount: 2,
						podCount:   1,
					},
				},
			},
			unconstrained: false,
			wantOrder:     []string{"a", "c", "b"},
		},
		"LeastFreeCapacity: sliceCount ascending": {
			domains: []testDomainSpec{
				{
					domain: domain{id: "a", levelValues: []string{"a"}},
					state: domainState{
						sliceCount: 3,
						podCount:   1,
					},
				},
				{
					domain: domain{id: "b", levelValues: []string{"b"}},
					state: domainState{
						sliceCount: 1,
						podCount:   1,
					},
				},
				{
					domain: domain{id: "c", levelValues: []string{"c"}},
					state: domainState{
						sliceCount: 2,
						podCount:   1,
					},
				},
			},
			unconstrained: true,
			wantOrder:     []string{"b", "c", "a"},
		},
		"BestFit: podCount ascending as tiebreaker": {
			domains: []testDomainSpec{
				{
					domain: domain{id: "large", levelValues: []string{"a"}},
					state: domainState{
						sliceCount: 5,
						podCount:   100,
					},
				},
				{
					domain: domain{id: "small", levelValues: []string{"b"}},
					state: domainState{
						sliceCount: 5,
						podCount:   10,
					},
				},
				{
					domain: domain{id: "medium", levelValues: []string{"c"}},
					state: domainState{
						sliceCount: 5,
						podCount:   50,
					},
				},
			},
			unconstrained: false,
			wantOrder:     []string{"small", "medium", "large"},
		},
		"LeastFreeCapacity: podCount ascending as tiebreaker": {
			domains: []testDomainSpec{
				{
					domain: domain{id: "large", levelValues: []string{"a"}},
					state: domainState{
						sliceCount: 5,
						podCount:   100,
					},
				},
				{
					domain: domain{id: "small", levelValues: []string{"b"}},
					state: domainState{
						sliceCount: 5,
						podCount:   10,
					},
				},
				{
					domain: domain{id: "medium", levelValues: []string{"c"}},
					state: domainState{
						sliceCount: 5,
						podCount:   50,
					},
				},
			},
			unconstrained: true,
			wantOrder:     []string{"small", "medium", "large"},
		},
		"levelValues ascending as final tiebreaker": {
			domains: []testDomainSpec{
				{
					domain: domain{id: "c", levelValues: []string{"c"}},
					state: domainState{
						sliceCount: 5,
						podCount:   10,
					},
				},
				{
					domain: domain{id: "a", levelValues: []string{"a"}},
					state: domainState{
						sliceCount: 5,
						podCount:   10,
					},
				},
				{
					domain: domain{id: "b", levelValues: []string{"b"}},
					state: domainState{
						sliceCount: 5,
						podCount:   10,
					},
				},
			},
			unconstrained: false,
			wantOrder:     []string{"a", "b", "c"},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TASRespectNodeAffinityPreferred, tc.enableTASPreferredSchedulingAffinity)
			_, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, "test", newTopologyTree(levels, nil, 0), nil, newDefaultSimulatorSnapshot())

			sorted := s.sortedDomains(addDomainsWithState(s, tc.domains), tc.unconstrained)

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
		"leafIsNode with same-parent sibling domains: ascending by hostname": {
			levels: hostnameLevels,
			a:      &domain{id: "node-a", parent: parent1, levelValues: []string{"b1", "r1", "node-a"}},
			b:      &domain{id: "node-b", parent: parent1, levelValues: []string{"b1", "r1", "node-b"}},
			want:   -1,
		},
		"leafIsNode with same-parent sibling domains: descending by hostname": {
			levels: hostnameLevels,
			a:      &domain{id: "node-b", parent: parent1, levelValues: []string{"b1", "r1", "node-b"}},
			b:      &domain{id: "node-a", parent: parent1, levelValues: []string{"b1", "r1", "node-a"}},
			want:   1,
		},
		"leafIsNode with same-parent sibling domains: equal hostname": {
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
			s := newTASFlavorSnapshot(log, "test", newTopologyTree(tc.levels, nil, 0), nil, newDefaultSimulatorSnapshot())
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
	singlePodRequests := resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
		corev1.ResourceCPU:    1000,
		corev1.ResourceMemory: 1024,
	})

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
				"node-a": resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
					corev1.ResourceCPU:    1000,
					corev1.ResourceMemory: 1024,
					corev1.ResourcePods:   1,
				}),
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
				"node-a": resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
					corev1.ResourceCPU:    2000,
					corev1.ResourceMemory: 2048,
					corev1.ResourcePods:   2,
				}),
				"node-b": resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
					corev1.ResourceCPU:    3000,
					corev1.ResourceMemory: 3072,
					corev1.ResourcePods:   3,
				}),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := tas.ComputeUsagePerDomain(tc.assignment, singlePodRequests)
			if diff := cmp.Diff(tc.want, got, cmp.Comparer(resources.Equal)); diff != "" {
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
				"node-a": resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
					corev1.ResourceCPU:  1000,
					corev1.ResourcePods: 1,
				}),
			},
			assignment: &tas.TopologyAssignment{
				Levels: []string{"hostname"},
				Domains: []tas.TopologyDomainAssignment{
					{Values: []string{"node-a"}, Count: 1},
					{Values: []string{"node-b"}, Count: 2},
				},
			},
			tasRequests: &TASPodSetRequests{
				SinglePodRequests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
					corev1.ResourceCPU:    500,
					corev1.ResourceMemory: 2048,
				}),
			},
			want: map[tas.TopologyDomainID]resources.Requests{
				"node-a": resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
					corev1.ResourceCPU:    1500,
					corev1.ResourceMemory: 2048,
					corev1.ResourcePods:   2,
				}),
				"node-b": resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
					corev1.ResourceCPU:    1000,
					corev1.ResourceMemory: 4096,
					corev1.ResourcePods:   2,
				}),
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
				SinglePodRequests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
					corev1.ResourceCPU:    250,
					corev1.ResourceMemory: 512,
				}),
			},
			want: map[tas.TopologyDomainID]resources.Requests{
				"node-a": resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
					corev1.ResourceCPU:    750,
					corev1.ResourceMemory: 1536,
					corev1.ResourcePods:   3,
				}),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			addAssumedUsage(tc.assumedUsage, tc.assignment, tc.tasRequests)
			if diff := cmp.Diff(tc.want, tc.assumedUsage, cmp.Comparer(resources.Equal)); diff != "" {
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
			features.SetFeatureGateDuringTest(t, features.TASCachingRemainingResources, enableCaching)

			_, log := utiltesting.ContextWithLog(t)
			nodeObj := node.MakeNode("node-a").
				Label("hostname", "node-a").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("8"),
					corev1.ResourceMemory: resource.MustParse("10Gi"),
				}).
				Ready().
				Obj()
			snapshot := newTASFlavorSnapshot(log, "tas-topology", newTopologyTree([]string{"hostname"}, []*corev1.Node{nodeObj}, 0), nil, newDefaultSimulatorSnapshot())
			domainID := snapshot.nodeToDomain[nodeObj.Name]

			if snapshot.leaves[domainID] == nil {
				t.Fatalf("leaves[%q] = nil, want non-nil", domainID)
			}

			flavorUsage := workload.TASFlavorUsage{
				{
					Values: []string{"node-a"},
					SinglePodRequests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 5000,
					}),
					Count: 1,
				},
			}

			// Warm the Fits cache before adding TAS usage
			if got := snapshot.Fits(flavorUsage); !got {
				t.Errorf("Fits() before adding usage = %t, want true", got)
			}

			// Add TAS usage of 4 CPU (4000m), leaving 4 CPU (8000m - 4000m = 4000m) remaining
			usage := resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
				corev1.ResourceCPU: 4000,
			})
			snapshot.updateTASUsage(domainID, usage, add, 1)

			// Fits should now return false because 5 CPU > 4 CPU remaining
			if got := snapshot.Fits(flavorUsage); got {
				t.Errorf("Fits() after adding usage = %t, want false", got)
			}

			// Remove TAS usage
			snapshot.updateTASUsage(domainID, usage, subtract, 1)

			// Fits should now return true again after cache invalidation / re-evaluation
			if got := snapshot.Fits(flavorUsage); !got {
				t.Errorf("Fits() after removing usage = %t, want true", got)
			}
		})
	}
}

func TestFitsNonHostnameLowestLevel(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TASNodeFeasibilityForAllLevels, true)
	const blockLabel = "cloud.provider.com/topology-block"
	const rackLabel = "cloud.provider.com/topology-rack"

	rackNode := node.MakeNode("").
		Label(blockLabel, "b1").
		Label(rackLabel, "r1").
		StatusAllocatable(corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("8"),
		}).
		Ready()

	cases := map[string]struct {
		count int32
		want  bool
	}{
		"pods fit across the rack's nodes": {
			count: 2,
			want:  true,
		},
		// The rack aggregates 16 CPU, but each node fits only one 5-CPU pod.
		"pods fit in the rack's aggregate but not per node": {
			count: 3,
			want:  false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			nodes := []*corev1.Node{rackNode.Clone().Name("n1").Obj(), rackNode.Clone().Name("n2").Obj()}
			tree := newTopologyTree([]string{blockLabel, rackLabel}, nodes, 0)
			snapshot := newTASFlavorSnapshot(log, "tas-topology", tree, nil, newDefaultSimulatorSnapshot())

			flavorUsage := workload.TASFlavorUsage{{
				Values: []string{"b1", "r1"},
				SinglePodRequests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
					corev1.ResourceCPU: 5000,
				}),
				Count: tc.count,
			}}
			if got := snapshot.Fits(flavorUsage); got != tc.want {
				t.Errorf("Fits() = %t, want %t", got, tc.want)
			}
		})
	}
}

// Usage recorded against a domain that spans several nodes bounds the domain,
// not its nodes: which node inside the domain holds a Pod is decided by
// kube-scheduler after ungating and is never reported back. Charging a share
// of it to every node instead makes the nodes look full and rejects Workloads
// the domain has room for.
func TestFindAssignmentsWithDomainRecordedUsage(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TASNodeFeasibilityForAllLevels, true)
	const blockLabel = "cloud.provider.com/topology-block"
	const rackLabel = "cloud.provider.com/topology-rack"
	rack := rackLabel
	oneCPU := resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 1000})

	// A rack of two nodes, one CPU each.
	rackNode := node.MakeNode("").
		Label(blockLabel, "b1").
		Label(rackLabel, "r1").
		StatusAllocatable(corev1.ResourceList{
			corev1.ResourceCPU:  resource.MustParse("1"),
			corev1.ResourcePods: resource.MustParse("10"),
		}).
		Ready()
	nodes := []*corev1.Node{
		rackNode.Clone().Name("n1").Label(corev1.LabelHostname, "n1").Obj(),
		rackNode.Clone().Name("n2").Label(corev1.LabelHostname, "n2").Obj(),
	}

	podSet := func(name kueue.PodSetReference) TASPodSetRequests {
		return TASPodSetRequests{
			PodSet: &kueue.PodSet{
				Name:            name,
				TopologyRequest: &kueue.PodSetTopologyRequest{Preferred: &rack},
			},
			SinglePodRequests: oneCPU,
			Count:             1,
		}
	}

	// A PodSet pinned to one node by its nodeSelector.
	podSetOn := func(name kueue.PodSetReference, nodeName string) TASPodSetRequests {
		tr := podSet(name)
		tr.PodSet.Template.Spec.NodeSelector = map[string]string{corev1.LabelHostname: nodeName}
		return tr
	}

	cases := map[string]struct {
		priorRackUsage int32
		requests       FlavorTASRequests
		wantFit        bool
	}{
		// Both PodSets can only run on n1, which holds one of them. The rack
		// has room for both, so only per-node in-cycle usage rejects this.
		"two PodSets pinned to the same node do not both fit on it": {
			requests: FlavorTASRequests{podSetOn("ps-a", "n1"), podSetOn("ps-b", "n1")},
			wantFit:  false,
		},
		"two PodSets pinned to different nodes fit": {
			requests: FlavorTASRequests{podSetOn("ps-a", "n1"), podSetOn("ps-b", "n2")},
			wantFit:  true,
		},
		"two PodSets of one Pod each land on a node of their own": {
			requests: FlavorTASRequests{podSet("ps-a"), podSet("ps-b")},
			wantFit:  true,
		},
		"a Pod fits next to a Workload already admitted on the rack": {
			priorRackUsage: 1,
			requests:       FlavorTASRequests{podSet("ps-b")},
			wantFit:        true,
		},
		"no Pod fits once the rack's capacity is fully used": {
			priorRackUsage: 2,
			requests:       FlavorTASRequests{podSet("ps-b")},
			wantFit:        false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			tree := newTopologyTree([]string{blockLabel, rackLabel}, nodes, 0)
			snapshot := newTASFlavorSnapshot(log, "tas-topology", tree, nil, newDefaultSimulatorSnapshot())
			if tc.priorRackUsage > 0 {
				snapshot.updateTASUsage(tas.DomainID([]string{"b1", "r1"}),
					oneCPU.ScaledUp(int64(tc.priorRackUsage)), add, tc.priorRackUsage)
			}
			gotFit := snapshot.FindTopologyAssignmentsForFlavor(ctx, tc.requests).Failure() == nil
			if gotFit != tc.wantFit {
				t.Errorf("FindTopologyAssignmentsForFlavor() fit = %t, want %t", gotFit, tc.wantFit)
			}
		})
	}
}

// Usage domain IDs name the explicit-lowest level while the hostname level is
// virtual, so a node whose name equals a domain ID must not capture that
// domain's usage.
func TestUsageDomainIgnoresNodeNameCollision(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TASNodeFeasibilityForAllLevels, true)
	const rackLabel = "cloud.provider.com/topology-rack"
	_, log := utiltesting.ContextWithLog(t)

	rackNode := node.MakeNode("").
		StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")}).
		Ready()
	nodeA := rackNode.Clone().Name("node-a").Label(corev1.LabelHostname, "node-a").Label(rackLabel, "r1").Obj()
	nodeB := rackNode.Clone().Name("node-b").Label(corev1.LabelHostname, "node-b").Label(rackLabel, "r1").Obj()
	// A third node is named like rack r1 itself, but lives in rack r2.
	nodeNamedR1 := rackNode.Clone().Name("r1").Label(corev1.LabelHostname, "r1").Label(rackLabel, "r2").Obj()

	tree := newTopologyTree([]string{rackLabel}, []*corev1.Node{nodeA, nodeB, nodeNamedR1}, 0)
	snapshot := newTASFlavorSnapshot(log, "tas-topology", tree, nil, newDefaultSimulatorSnapshot())
	leaves := slices.Collect(snapshot.leavesOf(snapshot.usageDomain("r1")))
	if len(leaves) != 2 {
		t.Fatalf("usageDomain(\"r1\") holds %d leaves, want rack r1's 2 leaves", len(leaves))
	}
	for _, leaf := range leaves {
		if leaf.node.Name == "r1" {
			t.Error("usageDomain(\"r1\") resolved to the node named \"r1\" instead of the rack")
		}
	}

	// Control: with hostname declared as the lowest level, usage domains are
	// the leaves themselves and the leaf lookup must keep working.
	declaredTree := newTopologyTree([]string{rackLabel, corev1.LabelHostname}, []*corev1.Node{nodeA, nodeB}, 0)
	declaredSnapshot := newTASFlavorSnapshot(log, "tas-topology", declaredTree, nil, newDefaultSimulatorSnapshot())
	declaredLeaves := slices.Collect(declaredSnapshot.leavesOf(declaredSnapshot.usageDomain("node-a")))
	if len(declaredLeaves) != 1 || declaredLeaves[0].node.Name != "node-a" {
		t.Errorf("usageDomain(\"node-a\") holds %d leaves, want the node-a leaf", len(declaredLeaves))
	}
}

// Simulated removal of one Workload's usage must not erase the usage of the
// Workloads that remain admitted, or preemption would free capacity twice.
func TestFitsAfterPerWorkloadRemoval(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TASNodeFeasibilityForAllLevels, true)
	const blockLabel = "cloud.provider.com/topology-block"
	const rackLabel = "cloud.provider.com/topology-rack"
	dev := corev1.ResourceName("example.com/device")
	ctx, log := utiltesting.ContextWithLog(t)

	tasCache := NewTASCache(nil, newDefaultSimulator(), resources.NewResourceFormatter())
	rackNode := node.MakeNode("").
		Label(blockLabel, "b1").
		Label(rackLabel, "r1").
		StatusAllocatable(corev1.ResourceList{dev: resource.MustParse("1")}).
		Ready()
	for _, name := range []string{"n1", "n2"} {
		tasCache.SyncNode(rackNode.Clone().Name(name).Label(corev1.LabelHostname, name).Obj())
	}
	fc := tasCache.NewTASFlavorCache(
		topologyInformation{Levels: []string{blockLabel, rackLabel}},
		flavorInformation{TopologyName: "default"},
	)
	singleDevice := []workload.TopologyDomainRequests{{
		Values: []string{"b1", "r1"},
		SinglePodRequests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
			dev: 1,
		}),
		Count: 1,
	}}
	bothDevices := workload.TASFlavorUsage{{
		Values: []string{"b1", "r1"},
		SinglePodRequests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
			dev: 1,
		}),
		Count: 2,
	}}

	// Control: with no usage recorded, both devices are free.
	empty, err := fc.snapshot(ctx, log, newDefaultSimulatorSnapshot(), nil)
	if err != nil {
		t.Fatalf("snapshot() error = %v", err)
	}
	if got := empty.Fits(bothDevices); !got {
		t.Errorf("Fits() with no usage = %t, want true", got)
	}

	fc.addUsage(log, "wl1", singleDevice)
	fc.addUsage(log, "wl2", singleDevice)
	snapshot, err := fc.snapshot(ctx, log, newDefaultSimulatorSnapshot(), nil)
	if err != nil {
		t.Fatalf("snapshot() error = %v", err)
	}
	if got := snapshot.Fits(bothDevices); got {
		t.Errorf("Fits() with both devices used = %t, want false", got)
	}

	// Preemption simulation removes one Workload; the other still holds a device.
	for _, tr := range singleDevice {
		snapshot.updateTASUsage(tas.DomainID(tr.Values), tr.TotalRequests(), subtract, tr.Count)
	}
	if got := snapshot.Fits(bothDevices); got {
		t.Errorf("Fits() after removing one Workload = %t, want false", got)
	}
}

func newObservedLogger(level zapcore.Level) (logr.Logger, *observer.ObservedLogs) {
	logsObserver, observedLogs := observer.New(level)
	logger := crzap.New(
		crzap.WriteTo(io.Discard),
		crzap.Level(level),
		func(o *crzap.Options) {
			o.ZapOpts = append(o.ZapOpts, zaplog.WrapCore(func(zapcore.Core) zapcore.Core {
				return logsObserver
			}))
		},
	)
	return logger, observedLogs
}

func TestUpdateCountsToMinimumGenericLogsLeafSummary(t *testing.T) {
	// Error entries are not verbosity-gated, so leaf IDs must stay out of them.
	newSnapshot := func(log logr.Logger) *TASFlavorSnapshot {
		nodes := make([]*corev1.Node, 0, 2)
		for _, name := range []string{"node-a", "node-b"} {
			nodes = append(nodes, node.MakeNode(name).
				Label(corev1.LabelHostname, name).
				StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")}).
				Ready().
				Obj())
		}
		return newTASFlavorSnapshot(log, "tas-topology", newTopologyTree([]string{corev1.LabelHostname}, nodes, 0), nil, newDefaultSimulatorSnapshot())
	}
	// One domain with capacity 1 cannot satisfy count 10.
	callWithViolatedAssumptions := func(snapshot *TASFlavorSnapshot) []*domain {
		dom := &domain{id: "rack-1", idx: 0}
		snapshot.domainStateOf(dom).podCount = 1
		return snapshot.updateCountsToMinimumGeneric([]*domain{dom}, 10, 0, 1, false, false)
	}
	wantErrorFields := map[string]any{
		"error":                "code assumptions violated",
		"remainingCount":       int32(9),
		"remainingLeaderCount": int32(0),
		"count":                int32(10),
		"leaderCount":          int32(0),
		"sliceSize":            int32(1),
		"unconstrained":        false,
		"topologyName":         kueue.TopologyReference("tas-topology"),
		"domainCount":          int64(1),
		"leafCount":            int64(2),
	}

	t.Run("the error entry summarizes the leaf domains instead of dumping them", func(t *testing.T) {
		log, observedLogs := newObservedLogger(zapcore.InfoLevel)
		snapshot := newSnapshot(log)

		if got := callWithViolatedAssumptions(snapshot); got != nil {
			t.Fatalf("updateCountsToMinimumGeneric() = %v, want nil", got)
		}

		logs := observedLogs.TakeAll()
		if len(logs) != 1 {
			t.Fatalf("Observed %d log entries, want 1: %v", len(logs), logs)
		}
		if logs[0].Level != zapcore.ErrorLevel {
			t.Errorf("Observed log level %v, want %v", logs[0].Level, zapcore.ErrorLevel)
		}
		fields := logs[0].ContextMap()
		if diff := cmp.Diff(wantErrorFields, fields); diff != "" {
			t.Errorf("Observed error fields mismatch (-want +got):\n%s", diff)
		}
		// Leaf IDs must not appear under any error field.
		for _, leafDomainID := range []string{"node-a", "node-b"} {
			if rendered := fmt.Sprint(fields); strings.Contains(rendered, leafDomainID) {
				t.Errorf("Observed error entry mentions leaf domain %q: %s", leafDomainID, rendered)
			}
		}
	})

	t.Run("the leaf domains are logged at high verbosity", func(t *testing.T) {
		log, observedLogs := newObservedLogger(zapcore.Level(-6))
		snapshot := newSnapshot(log)

		if got := callWithViolatedAssumptions(snapshot); got != nil {
			t.Fatalf("updateCountsToMinimumGeneric() = %v, want nil", got)
		}

		logs := observedLogs.TakeAll()
		if len(logs) != 2 {
			t.Fatalf("Observed %d log entries, want 2: %v", len(logs), logs)
		}
		if diff := cmp.Diff(wantErrorFields, logs[0].ContextMap()); diff != "" {
			t.Errorf("Observed error fields mismatch (-want +got):\n%s", diff)
		}
		if logs[1].Level != zapcore.Level(-6) {
			t.Errorf("Observed log level %v, want %v", logs[1].Level, zapcore.Level(-6))
		}
		wantLeafFields := map[string]any{
			"topologyName": kueue.TopologyReference("tas-topology"),
			"leafDomains":  []tas.TopologyDomainID{"node-a", "node-b"},
		}
		if diff := cmp.Diff(wantLeafFields, logs[1].ContextMap()); diff != "" {
			t.Errorf("Observed leaf domain fields mismatch (-want +got):\n%s", diff)
		}
	})
}
