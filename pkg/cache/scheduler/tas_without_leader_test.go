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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/node"
)

func TestFindLevelWithFitDomainsWithoutLeader(t *testing.T) {
	cases := map[string]struct {
		count         int32
		required      bool
		unconstrained bool
		mixedProfile  bool
		affinity      bool
		want          []string
		wantReason    string
	}{
		"required domain":                  {count: 8, required: true, want: []string{"large"}},
		"best fit":                         {count: 4, required: true, want: []string{"small"}},
		"preferred spans domains":          {count: 14, want: []string{"large", "small"}},
		"unconstrained spans domains":      {count: 14, unconstrained: true, want: []string{"large", "small"}},
		"least free capacity":              {count: 4, unconstrained: true, mixedProfile: true, want: []string{"small"}},
		"fallback from preferred affinity": {count: 8, required: true, affinity: true, want: []string{"large"}},
		"insufficient capacity":            {count: 18, required: true, wantReason: `topology "dummy" allows to fit only 5 out of 9 slice(s)`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TASProfileMixed, tc.mixedProfile)
			features.SetFeatureGateDuringTest(t, features.TASRespectNodeAffinityPreferred, tc.affinity)
			for _, perturbLeaderCapacity := range []bool{false, true} {
				_, log := utiltesting.ContextWithLog(t)
				s := newTASFlavorSnapshot(log, flavorInformation{TopologyName: "dummy"}, newTopologyTree([]string{"rack"}, nil, 0), newDefaultSimulatorSnapshot())
				smallState := domainState{podCount: 6, sliceCount: 3}
				if tc.affinity {
					smallState.affinityScore = 10
				}
				if perturbLeaderCapacity {
					// Leader-only capacities must not affect a worker-only request.
					smallState.leaderCount = 1
					smallState.podCountWithLeader = 100
					smallState.sliceCountWithLeader = 100
				}
				for _, d := range addDomainsWithState(s, []testDomainSpec{
					{domain: domain{id: "small", levelValues: []string{"small"}}, state: smallState},
					{domain: domain{id: "large", levelValues: []string{"large"}}, state: domainState{podCount: 10, sliceCount: 5}},
				}) {
					s.domainsPerLevel[0][d.id] = d
				}
				_, got, reason := s.findLevelWithFitDomains(0, &findTopologyAssignmentState{
					topologyAssignmentParameters: topologyAssignmentParameters{
						count: tc.count, sliceSize: 2, required: tc.required, unconstrained: tc.unconstrained,
					},
					stats: newTASExclusionStats(),
				})
				if reason != tc.wantReason {
					t.Errorf("perturbLeaderCapacity=%t: reason = %q, want %q", perturbLeaderCapacity, reason, tc.wantReason)
				}
				if diff := cmp.Diff(tc.want, domainIDs(got)); diff != "" {
					t.Errorf("perturbLeaderCapacity=%t: unexpected domains (-want,+got):\n%s", perturbLeaderCapacity, diff)
				}
			}
		})
	}
}

func TestBalancedPlacementWithoutLeader(t *testing.T) {
	for name, perturbLeaderCapacity := range map[string]bool{"zero leader capacity": false, "unrelated leader capacity": true} {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, flavorInformation{TopologyName: "dummy"}, newTopologyTree([]string{"rack"}, nil, 0), newDefaultSimulatorSnapshot())
			smallState := domainState{podCount: 4, sliceCount: 2}
			if perturbLeaderCapacity {
				smallState.leaderCount = 1
				smallState.podCountWithLeader = 100
				smallState.sliceCountWithLeader = 100
			}
			domains := addDomainsWithState(s, []testDomainSpec{
				{domain: domain{id: "small", levelValues: []string{"a"}}, state: smallState},
				{domain: domain{id: "large", levelValues: []string{"b"}}, state: domainState{podCount: 8, sliceCount: 4}},
			})
			got, reason := placeSlicesOnDomainsBalanced(s, domains, 5, 0, 2, 2)
			if reason != "" {
				t.Fatalf("unexpected placement failure: %s", reason)
			}
			if diff := cmp.Diff([]string{"large", "small"}, domainIDs(got)); diff != "" {
				t.Errorf("unexpected domain order (-want,+got):\n%s", diff)
			}
			wantCounts := map[string]int32{"large": 6, "small": 4}
			for _, d := range got {
				if count := s.domainStateOf(d).podCount; count != wantCounts[string(d.id)] {
					t.Errorf("domain %s: pod count = %d, want %d", d.id, count, wantCounts[string(d.id)])
				}
			}
		})
	}
}

func TestSelectOptimalDomainSetWithoutLeader(t *testing.T) {
	for name, perturbLeaderCapacity := range map[string]bool{"zero leader capacity": false, "unrelated leader capacity": true} {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, flavorInformation{TopologyName: "dummy"}, newTopologyTree([]string{"rack"}, nil, 0), newDefaultSimulatorSnapshot())
			// Equal pod capacities make the DP retain the first domain. Slice capacity
			// must determine that order even when leader capacities disagree.
			smallState := domainState{podCount: 8, sliceCount: 2}
			if perturbLeaderCapacity {
				smallState.leaderCount = 1
				smallState.sliceCountWithLeader = 100
			}
			domains := addDomainsWithState(s, []testDomainSpec{
				{domain: domain{id: "small", levelValues: []string{"a"}}, state: smallState},
				{domain: domain{id: "large", levelValues: []string{"b"}}, state: domainState{podCount: 8, sliceCount: 4}},
			})
			got := selectOptimalDomainSetToFit(s, domains, 2, 0, 2, true)
			if diff := cmp.Diff([]string{"large"}, domainIDs(got)); diff != "" {
				t.Errorf("unexpected domain selection (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestTopologyAssignmentWithoutLeaderAfterGroupedRequest(t *testing.T) {
	const rackLabel = "cloud.provider.com/topology-rack"
	cases := map[string]struct {
		levels   []string
		balanced bool
	}{
		"hostname best fit": {levels: []string{rackLabel, corev1.LabelHostname}},
		"hostname balanced": {levels: []string{rackLabel, corev1.LabelHostname}, balanced: true},
		"rack best fit":     {levels: []string{rackLabel}},
		"rack balanced":     {levels: []string{rackLabel}, balanced: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TASNodeFeasibilityForAllLevels, true)
			features.SetFeatureGateDuringTest(t, features.TASBalancedPlacement, tc.balanced)
			ctx, log := utiltesting.ContextWithLog(t)
			rackNode := node.MakeNode("").StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourcePods: resource.MustParse("20"),
			}).Ready()
			nodes := []*corev1.Node{
				rackNode.Clone().Name("n1").Label(rackLabel, "r1").Label(corev1.LabelHostname, "n1").Obj(),
				rackNode.Clone().Name("n2").Label(rackLabel, "r2").Label(corev1.LabelHostname, "n2").Obj(),
			}
			tree := newTopologyTree(tc.levels, nodes, 0)
			s := newTASFlavorSnapshot(log, flavorInformation{TopologyName: "dummy"}, tree, newDefaultSimulatorSnapshot())
			workers := TASPodSetRequests{
				PodSet:            &kueue.PodSet{Name: "workers", TopologyRequest: &kueue.PodSetTopologyRequest{Preferred: ptr.To(rackLabel)}},
				SinglePodRequests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 1000}),
				Count:             6,
			}
			leader := TASPodSetRequests{PodSet: &kueue.PodSet{Name: "leader"}, SinglePodRequests: workers.SinglePodRequests, Count: 1}
			want, _, reason := s.findTopologyAssignment(ctx, workers, nil, newAssumedUsage(nil), false, "", nil, nil)
			if reason != "" {
				t.Fatalf("unexpected worker placement failure: %s", reason)
			}
			for range 2 {
				grouped, _, reason := s.findTopologyAssignment(ctx, workers, &leader, newAssumedUsage(nil), false, "", nil, nil)
				if reason != "" {
					t.Fatalf("unexpected grouped placement failure: %s", reason)
				}
				for podSet, count := range map[kueue.PodSetReference]int32{"workers": 6, "leader": 1} {
					var got int32
					for _, d := range grouped[podSet].Domains {
						got += d.Count
					}
					if got != count {
						t.Errorf("PodSet %s: assigned %d pods, want %d", podSet, got, count)
					}
				}
				got, _, reason := s.findTopologyAssignment(ctx, workers, nil, newAssumedUsage(nil), false, "", nil, nil)
				if reason != "" {
					t.Fatalf("unexpected worker placement failure after grouped request: %s", reason)
				}
				if diff := cmp.Diff(want, got); diff != "" {
					t.Errorf("unexpected assignment after grouped request (-want,+got):\n%s", diff)
				}
				for i, state := range s.domainStates {
					if state.leaderCount != 0 || state.podCountWithLeader != 0 || state.sliceCountWithLeader != 0 ||
						state.capacityBound.leaderCount != 0 || state.capacityBound.podCountWithLeader != 0 {
						t.Errorf("domain state %d retains leader capacity after worker-only placement: %+v", i, state)
					}
				}
			}
		})
	}
}
