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
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
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
			s := newTASFlavorSnapshot(log, "dummy", levels, nil, "")
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
			s := newTASFlavorSnapshot(log, "dummy", levels, nil, "")
			got := s.HasLevel(tc.podSetTopologyRequest)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("unexpected HasLevel result (-want,+got): %s", diff)
			}
		})
	}
}

func TestSortedDomainsWithNodeAvoidance(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.NodeAvoidanceScheduling, true)
	levels := []string{"kubernetes.io/hostname"}
	avoidanceLabel := "unhealthy"
	nodes := []corev1.Node{
		*node.MakeNode("node-1-unhealthy").Label("kubernetes.io/hostname", "node-1-unhealthy").Label(avoidanceLabel, "true").Obj(),
		*node.MakeNode("node-2-healthy").Label("kubernetes.io/hostname", "node-2-healthy").Obj(),
	}

	cases := map[string]struct {
		policy string
		want   []string
	}{
		"no policy - sorts by name (unhealthy first because of name)": {
			policy: "",
			want:   []string{"node-1-unhealthy", "node-2-healthy"},
		},
		"prefer healthy - sorts by health (healthy first despite name)": {
			policy: controllerconsts.NodeAvoidancePolicyPreferNoSchedule,
			want:   []string{"node-2-healthy", "node-1-unhealthy"},
		},
		"disallow unhealthy": {
			policy: controllerconsts.NodeAvoidancePolicyRequired,
			want:   []string{"node-2-healthy"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, "dummy", levels, nil, avoidanceLabel)
			for _, node := range nodes {
				s.addNode(node)
			}
			s.initialize()

			// Construct domains list (order doesn't matter as it gets sorted, but let's mix it)
			domains := []*domain{
				s.domainsPerLevel[0][utiltas.DomainID([]string{"node-2-healthy"})],
				s.domainsPerLevel[0][utiltas.DomainID([]string{"node-1-unhealthy"})],
			}

			gotDomains := s.sortedDomains(domains, false, tc.policy)
			gotValues := make([]string, len(gotDomains))
			for i, d := range gotDomains {
				gotValues[i] = d.levelValues[0]
			}

			if diff := cmp.Diff(tc.want, gotValues); diff != "" {
				t.Errorf("unexpected sorted domains (-want,+got): %s", diff)
			}
		})
	}
}

func TestSortedDomainsWithNodeAvoidance_Hierarchy(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.NodeAvoidanceScheduling, true)
	levels := []string{"rack", "kubernetes.io/hostname"}
	avoidanceLabel := "unhealthy"
	nodes := []corev1.Node{
		*node.MakeNode("r1-n1").Label("rack", "r1").Label("kubernetes.io/hostname", "r1-n1").Label(avoidanceLabel, "true").Obj(),
		*node.MakeNode("r1-n2").Label("rack", "r1").Label("kubernetes.io/hostname", "r1-n2").Obj(),
		*node.MakeNode("r2-n1").Label("rack", "r2").Label("kubernetes.io/hostname", "r2-n1").Obj(),
		*node.MakeNode("r2-n2").Label("rack", "r2").Label("kubernetes.io/hostname", "r2-n2").Obj(),
	}

	cases := map[string]struct {
		policy string
		want   []string
	}{
		"prefer healthy - healthy rack first": {
			policy: controllerconsts.NodeAvoidancePolicyPreferNoSchedule,
			want:   []string{"r2", "r1"},
		},
		"no policy - sorts by name": {
			policy: "",
			want:   []string{"r1", "r2"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, "dummy", levels, nil, avoidanceLabel)
			for _, node := range nodes {
				s.addNode(node)
			}
			s.initialize()

			racks := []*domain{
				s.domainsPerLevel[0][utiltas.DomainID([]string{"r1"})],
				s.domainsPerLevel[0][utiltas.DomainID([]string{"r2"})],
			}

			gotDomains := s.sortedDomains(racks, false, tc.policy)
			gotValues := make([]string, len(gotDomains))
			for i, d := range gotDomains {
				gotValues[i] = d.levelValues[0]
			}

			if diff := cmp.Diff(tc.want, gotValues); diff != "" {
				t.Errorf("unexpected sorted domains (-want,+got): %s", diff)
			}
		})
	}
}

func TestSortedDomainsWithNodeAvoidance_Capacity(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.NodeAvoidanceScheduling, true)
	levels := []string{"kubernetes.io/hostname"}
	avoidanceLabel := "unhealthy"
	nodes := []corev1.Node{
		*node.MakeNode("node-unhealthy").
			Label("kubernetes.io/hostname", "node-unhealthy").
			Label(avoidanceLabel, "true").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("8"),
			}).Obj(),
		*node.MakeNode("node-healthy").
			Label("kubernetes.io/hostname", "node-healthy").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("1"),
			}).Obj(),
	}

	cases := map[string]struct {
		policy string
		want   []string
	}{
		"prefer healthy - healthy first despite lower capacity": {
			policy: controllerconsts.NodeAvoidancePolicyPreferNoSchedule,
			want:   []string{"node-healthy", "node-unhealthy"},
		},
		"no policy - capacity wins (LeastFreeCapacity/BestFit logic)": {
			policy: "",
			want:   []string{"node-unhealthy", "node-healthy"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, "dummy", levels, nil, avoidanceLabel)
			for _, node := range nodes {
				s.addNode(node)
			}
			s.initialize()

			s.leaves[utiltas.DomainID([]string{"node-unhealthy"})].sliceState = 8
			s.leaves[utiltas.DomainID([]string{"node-healthy"})].sliceState = 1

			domains := []*domain{
				s.domainsPerLevel[0][utiltas.DomainID([]string{"node-unhealthy"})],
				s.domainsPerLevel[0][utiltas.DomainID([]string{"node-healthy"})],
			}

			gotDomains := s.sortedDomains(domains, false, tc.policy)
			gotValues := make([]string, len(gotDomains))
			for i, d := range gotDomains {
				gotValues[i] = d.levelValues[0]
			}

			if diff := cmp.Diff(tc.want, gotValues); diff != "" {
				t.Errorf("unexpected sorted domains (-want,+got): %s", diff)
			}
		})
	}
}
