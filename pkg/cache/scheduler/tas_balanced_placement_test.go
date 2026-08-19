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
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func domainIDs(domains []*domain) []string {
	return utilslices.Map(domains, func(d **domain) string { return string((*d).id) })
}

func addDomainWithState(s *TASFlavorSnapshot, d *domain, state domainState) *domain {
	d.idx = len(s.domainStates)
	s.domainStates = append(s.domainStates, state)
	return d
}

func TestSelectOptimalDomainSetToFit(t *testing.T) {
	d1 := testDomainSpec{
		domain: domain{id: "d1", levelValues: []string{"d1"}},
		state: domainState{
			podCount:             9,
			sliceCount:           9,
			leaderCount:          1,
			podCountWithLeader:   8,
			sliceCountWithLeader: 8,
		},
	}
	d2 := testDomainSpec{
		domain: domain{id: "d2", levelValues: []string{"d2"}},
		state: domainState{
			podCount:             6,
			sliceCount:           6,
			leaderCount:          0,
			podCountWithLeader:   6,
			sliceCountWithLeader: 6,
		},
	}
	d3 := testDomainSpec{
		domain: domain{id: "d3", levelValues: []string{"d3"}},
		state: domainState{
			podCount:             4,
			sliceCount:           4,
			leaderCount:          1,
			podCountWithLeader:   3,
			sliceCountWithLeader: 3,
		},
	}
	d4 := testDomainSpec{
		domain: domain{id: "d4", levelValues: []string{"d4"}},
		state: domainState{
			podCount:             2,
			sliceCount:           2,
			leaderCount:          0,
			podCountWithLeader:   2,
			sliceCountWithLeader: 2,
		},
	}

	testCases := map[string]struct {
		domains     []testDomainSpec
		workerCount int32
		leaderCount int32
		want        []string
	}{
		"no fit": {
			domains:     []testDomainSpec{d1, d2, d3, d4},
			workerCount: 22,
			leaderCount: 0,
			want:        []string{},
		},
		"simple fit one domain": {
			domains:     []testDomainSpec{d1, d2, d3, d4},
			workerCount: 5,
			leaderCount: 1,
			want:        []string{"d1"},
		},
		"perfect fit with two domains": {
			domains:     []testDomainSpec{d1, d2, d3, d4},
			workerCount: 9,
			leaderCount: 1,
			want:        []string{"d2", "d3"},
		},
		"perfect fit with two domains 2": {
			domains:     []testDomainSpec{d1, d2, d3, d4},
			workerCount: 10,
			leaderCount: 1,
			want:        []string{"d1", "d4"},
		},
		"best fit, single domain": {
			domains:     []testDomainSpec{d1, d2, d3, d4},
			workerCount: 5,
			leaderCount: 0,
			want:        []string{"d2"},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, "dummy", newTopologyTree([]string{}, nil, 0), nil, newDefaultSimulatorSnapshot())
			domains := addDomainsWithState(s, tc.domains)
			got := selectOptimalDomainSetToFit(s, domains, tc.workerCount, tc.leaderCount, 1, true)
			gotIDs := make([]string, len(got))
			for i, d := range got {
				gotIDs[i] = string(d.id)
			}
			if diff := cmp.Diff(tc.want, gotIDs, cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
				t.Errorf("unexpected optimal domain set (-want,+got): %s", diff)
			}
		})
	}
}

func TestSelectOptimalDomainSetToFitStableTieBreak(t *testing.T) {
	testCases := map[string]struct {
		prioritizeByEntropy bool
	}{
		"balanced selection": {
			prioritizeByEntropy: false,
		},
		"entropy-prioritized selection": {
			prioritizeByEntropy: true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, "dummy", newTopologyTree([]string{}, nil, 0), nil, newDefaultSimulatorSnapshot())
			equalState := domainState{podCount: 3, sliceCount: 3, podCountWithLeader: 3, sliceCountWithLeader: 3}
			domains := []*domain{
				addDomainWithState(s, &domain{id: "leaf-a", levelValues: []string{"block-b", "host-a"}}, equalState),
				addDomainWithState(s, &domain{id: "leaf-m", levelValues: []string{"block-b", "host-m"}}, equalState),
				addDomainWithState(s, &domain{id: "leaf-z", levelValues: []string{"block-a", "host-z"}}, equalState),
				addDomainWithState(s, &domain{id: "leaf-zz", levelValues: []string{"block-a", "host-zz"}}, equalState),
			}

			got := selectOptimalDomainSetToFit(s, domains, 1, 0, 1, tc.prioritizeByEntropy)

			if diff := cmp.Diff([]string{"leaf-z"}, domainIDs(got)); diff != "" {
				t.Errorf("unexpected optimal domain set (-want,+got): %s", diff)
			}
		})
	}
}

func TestCompareDomainCapacityAndEntropy(t *testing.T) {
	testCases := map[string]struct {
		domains func(s *TASFlavorSnapshot) []*domain
		want    []string
	}{
		"tie-breaking on level values when capacity and entropy are equal": {
			domains: func(s *TASFlavorSnapshot) []*domain {
				leaderState := domainState{leaderCount: 1, sliceCountWithLeader: 5}
				childState := domainState{podCount: 2}
				return []*domain{
					addDomainWithState(s, &domain{id: "leaf-a", levelValues: []string{"block-b", "host-a"}, children: []*domain{
						addDomainWithState(s, &domain{}, childState), addDomainWithState(s, &domain{}, childState),
					}}, leaderState),
					addDomainWithState(s, &domain{id: "leaf-m", levelValues: []string{"block-b", "host-m"}, children: []*domain{
						addDomainWithState(s, &domain{}, childState), addDomainWithState(s, &domain{}, childState),
					}}, leaderState),
					addDomainWithState(s, &domain{id: "leaf-z", levelValues: []string{"block-a", "host-z"}, children: []*domain{
						addDomainWithState(s, &domain{}, childState), addDomainWithState(s, &domain{}, childState),
					}}, leaderState),
				}
			},
			want: []string{"leaf-z", "leaf-a", "leaf-m"},
		},
		"capacity overrides entropy, and higher entropy overrides level values": {
			domains: func(s *TASFlavorSnapshot) []*domain {
				return []*domain{
					addDomainWithState(s, &domain{id: "lower-leader", levelValues: []string{"a"}, children: []*domain{
						addDomainWithState(s, &domain{}, domainState{podCount: 50}), addDomainWithState(s, &domain{}, domainState{podCount: 50}),
					}}, domainState{leaderCount: 0, sliceCountWithLeader: 100}),
					addDomainWithState(s, &domain{id: "lower-capacity", levelValues: []string{"b"}, children: []*domain{
						addDomainWithState(s, &domain{}, domainState{podCount: 2}), addDomainWithState(s, &domain{}, domainState{podCount: 2}),
					}}, domainState{leaderCount: 1, sliceCountWithLeader: 4}),
					addDomainWithState(s, &domain{id: "low-entropy", levelValues: []string{"c"}, children: []*domain{
						addDomainWithState(s, &domain{}, domainState{podCount: 4}), addDomainWithState(s, &domain{}, domainState{podCount: 0}),
					}}, domainState{leaderCount: 1, sliceCountWithLeader: 5}),
					addDomainWithState(s, &domain{id: "high-entropy", levelValues: []string{"d"}, children: []*domain{
						addDomainWithState(s, &domain{}, domainState{podCount: 2}), addDomainWithState(s, &domain{}, domainState{podCount: 2}),
					}}, domainState{leaderCount: 1, sliceCountWithLeader: 5}),
				}
			},
			want: []string{"high-entropy", "low-entropy", "lower-capacity", "lower-leader"},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, "dummy", newTopologyTree([]string{}, nil, 0), nil, newDefaultSimulatorSnapshot())
			got := tc.domains(s)
			slices.SortFunc(got, s.compareDomainCapacityAndEntropy)

			if diff := cmp.Diff(tc.want, domainIDs(got)); diff != "" {
				t.Errorf("unexpected domain order (-want,+got): %s", diff)
			}
		})
	}
}

func TestPlaceSlicesOnDomainsBalanced(t *testing.T) {
	d1 := testDomainSpec{
		domain: domain{id: "d1", levelValues: []string{"d1"}},
		state: domainState{
			podCount:             18,
			sliceCount:           18,
			podCountWithLeader:   18,
			leaderCount:          0,
			sliceCountWithLeader: 18,
		},
	}
	d2 := testDomainSpec{
		domain: domain{id: "d2", levelValues: []string{"d2"}},
		state: domainState{
			podCount:             18,
			sliceCount:           18,
			podCountWithLeader:   18,
			leaderCount:          0,
			sliceCountWithLeader: 18,
		},
	}
	d3 := testDomainSpec{
		domain: domain{id: "d3", levelValues: []string{"d3"}},
		state: domainState{
			podCount:             18,
			sliceCount:           18,
			podCountWithLeader:   18,
			leaderCount:          0,
			sliceCountWithLeader: 18,
		},
	}
	d4 := testDomainSpec{
		domain: domain{id: "d4", levelValues: []string{"d4"}},
		state: domainState{
			podCount:             10,
			sliceCount:           10,
			podCountWithLeader:   10,
			leaderCount:          0,
			sliceCountWithLeader: 10,
		},
	}
	d5 := testDomainSpec{
		domain: domain{id: "d5", levelValues: []string{"d5"}},
		state: domainState{
			podCount:             2,
			sliceCount:           2,
			podCountWithLeader:   2,
			leaderCount:          0,
			sliceCountWithLeader: 2,
		},
	}

	testCases := map[string]struct {
		domains     []testDomainSpec
		sliceCount  int32
		leaderCount int32
		sliceSize   int32
		threshold   int32
		want        map[string]domainState
	}{
		"simple balanced placement on two domains": {
			domains:     []testDomainSpec{d1, d2, d3},
			sliceCount:  20,
			leaderCount: 0,
			sliceSize:   1,
			threshold:   10,
			want: map[string]domainState{
				"d1": {sliceCount: 10, podCount: 10, podCountWithLeader: 10, sliceCountWithLeader: 10, leaderCount: 0},
				"d2": {sliceCount: 10, podCount: 10, podCountWithLeader: 10, sliceCountWithLeader: 10, leaderCount: 0},
			},
		},
		"simple placement on three domains": {
			domains:     []testDomainSpec{d1, d2, d3},
			sliceCount:  40,
			leaderCount: 0,
			sliceSize:   1,
			threshold:   13,
			want: map[string]domainState{
				"d1": {sliceCount: 14, podCount: 14, podCountWithLeader: 14, sliceCountWithLeader: 14, leaderCount: 0},
				"d2": {sliceCount: 13, podCount: 13, podCountWithLeader: 13, sliceCountWithLeader: 13, leaderCount: 0},
				"d3": {sliceCount: 13, podCount: 13, podCountWithLeader: 13, sliceCountWithLeader: 13, leaderCount: 0},
			},
		},
		"find smallest domain that fits": {
			domains:     []testDomainSpec{d1, d2, d3, d4, d5},
			sliceCount:  2,
			leaderCount: 0,
			sliceSize:   1,
			threshold:   2,
			want: map[string]domainState{
				"d5": {sliceCount: 2, podCount: 2, podCountWithLeader: 2, sliceCountWithLeader: 2, leaderCount: 0},
			},
		},
		"correctly select domains": {
			domains:     []testDomainSpec{d1, d2, d3, d4, d5},
			sliceCount:  25,
			leaderCount: 0,
			sliceSize:   1,
			threshold:   10,
			want: map[string]domainState{
				"d1": {sliceCount: 15, podCount: 15, podCountWithLeader: 15, sliceCountWithLeader: 15, leaderCount: 0},
				"d4": {sliceCount: 10, podCount: 10, podCountWithLeader: 10, sliceCountWithLeader: 10, leaderCount: 0},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, "dummy", newTopologyTree([]string{}, nil, 0), nil, newDefaultSimulatorSnapshot())
			domains := addDomainsWithState(s, tc.domains)

			got, _ := placeSlicesOnDomainsBalanced(s, domains, tc.sliceCount, tc.leaderCount, tc.sliceSize, tc.threshold)

			gotStates := make(map[string]domainState, len(got))
			for _, d := range got {
				gotStates[string(d.id)] = *s.domainStateOf(d)
			}
			if diff := cmp.Diff(tc.want, gotStates, cmp.AllowUnexported(domainState{})); diff != "" {
				t.Errorf("Unexpected domains (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestPlaceSlicesOnDomainsBalancedStableTieBreak(t *testing.T) {
	_, log := utiltesting.ContextWithLog(t)
	s := newTASFlavorSnapshot(log, "dummy", newTopologyTree([]string{}, nil, 0), nil, newDefaultSimulatorSnapshot())
	equalState := domainState{podCount: 3, sliceCount: 3, podCountWithLeader: 3, sliceCountWithLeader: 3}
	domains := []*domain{
		addDomainWithState(s, &domain{id: "leaf-a", levelValues: []string{"block-b", "host-a"}}, equalState),
		addDomainWithState(s, &domain{id: "leaf-z", levelValues: []string{"block-a", "host-z"}}, equalState),
	}

	got, reason := placeSlicesOnDomainsBalanced(s, domains, 1, 0, 1, 1)

	if reason != "" {
		t.Fatalf("unexpected placement failure: %s", reason)
	}
	if diff := cmp.Diff([]string{"leaf-z"}, domainIDs(got)); diff != "" {
		t.Errorf("unexpected domain order (-want,+got): %s", diff)
	}
}

func TestPruneDomainsBelowThreshold(t *testing.T) {
	domainStateValues := func(s *TASFlavorSnapshot, d *domain) [5]int32 {
		st := s.domainStateOf(d)
		return [5]int32{st.podCount, st.sliceCount, st.podCountWithLeader, st.sliceCountWithLeader, st.leaderCount}
	}

	testCases := map[string]struct {
		domains        func(s *TASFlavorSnapshot) ([]*domain, map[string]*domain)
		threshold      int32
		sliceSize      int32
		sliceLevelIdx  int
		level          int
		leaderRequired bool
		want           map[string][5]int32
	}{
		"keeps worker only domain": {
			domains: func(s *TASFlavorSnapshot) ([]*domain, map[string]*domain) {
				leaderLeaf := addDomainWithState(s, &domain{
					id: "leader-leaf",
				}, domainState{
					podCount:             6,
					sliceCount:           6,
					leaderCount:          1,
					podCountWithLeader:   5,
					sliceCountWithLeader: 5,
				})
				leaderDomain := addDomainWithState(s, &domain{
					id:       "leader-domain",
					children: []*domain{leaderLeaf},
				}, domainState{
					podCount:             6,
					sliceCount:           6,
					leaderCount:          1,
					podCountWithLeader:   5,
					sliceCountWithLeader: 5,
				})
				leaderLeaf.parent = leaderDomain
				workerOnlyLeaf := addDomainWithState(s, &domain{
					id: "worker-only-leaf",
				}, domainState{
					podCount:             5,
					sliceCount:           5,
					leaderCount:          1,
					podCountWithLeader:   4,
					sliceCountWithLeader: 4,
				})
				workerOnlyDomain := addDomainWithState(s, &domain{
					id:       "worker-only-domain",
					children: []*domain{workerOnlyLeaf},
				}, domainState{
					podCount:             5,
					sliceCount:           5,
					leaderCount:          1,
					podCountWithLeader:   4,
					sliceCountWithLeader: 4,
				})
				workerOnlyLeaf.parent = workerOnlyDomain
				parentDomain := addDomainWithState(s, &domain{
					id:       "parent-domain",
					children: []*domain{leaderDomain, workerOnlyDomain},
				}, domainState{})
				leaderDomain.parent = parentDomain
				workerOnlyDomain.parent = parentDomain
				return []*domain{parentDomain}, map[string]*domain{
					"leaderDomain":     leaderDomain,
					"parentDomain":     parentDomain,
					"workerOnlyDomain": workerOnlyDomain,
					"workerOnlyLeaf":   workerOnlyLeaf,
				}
			},
			threshold:      5,
			sliceSize:      1,
			sliceLevelIdx:  2,
			level:          0,
			leaderRequired: true,
			want: map[string][5]int32{
				"leaderDomain":     {6, 6, 5, 5, 1},
				"parentDomain":     {11, 11, 10, 10, 1},
				"workerOnlyDomain": {5, 5, 0, 0, 0},
				"workerOnlyLeaf":   {5, 5, 0, 0, 0},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, "dummy", newTopologyTree([]string{}, nil, 0), nil, newDefaultSimulatorSnapshot())
			domains, domainsByName := tc.domains(s)

			s.pruneDomainsBelowThreshold(domains, tc.threshold, tc.sliceSize, tc.sliceLevelIdx, tc.level, tc.leaderRequired)

			got := make(map[string][5]int32, len(tc.want))
			for name := range tc.want {
				got[name] = domainStateValues(s, domainsByName[name])
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Unexpected domain state (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestPruneDomainsBelowThresholdPreservesAffinityScore(t *testing.T) {
	_, log := utiltesting.ContextWithLog(t)
	s := newTASFlavorSnapshot(log, "dummy", newTopologyTree([]string{}, nil, 0), nil, newDefaultSimulatorSnapshot())
	prunedLeaf := addDomainWithState(s, &domain{id: "pruned-leaf"}, domainState{
		sliceCount:    1,
		affinityScore: 100,
	})
	keptLeaf := addDomainWithState(s, &domain{id: "kept-leaf"}, domainState{
		podCount:           4,
		sliceCount:         4,
		podCountWithLeader: 4,
		affinityScore:      10,
	})
	parent := addDomainWithState(s, &domain{
		id:       "parent",
		children: []*domain{prunedLeaf, keptLeaf},
	}, domainState{})
	prunedLeaf.parent = parent
	keptLeaf.parent = parent

	s.pruneDomainsBelowThreshold([]*domain{parent}, 2, 1, 1, 0, false)

	if got, want := s.domainStateOf(prunedLeaf).affinityScore, int64(100); got != want {
		t.Errorf("Unexpected pruned leaf affinity score: got %d, want %d", got, want)
	}
	if got, want := s.domainStateOf(parent).affinityScore, int64(110); got != want {
		t.Errorf("Unexpected parent affinity score: got %d, want %d", got, want)
	}
}

func TestFindBestDomainsForBalancedPlacement(t *testing.T) {
	type domainSpec struct {
		id                   string
		parentID             string
		levelValues          []string
		podCount             int32
		sliceCount           int32
		podCountWithLeader   int32
		sliceCountWithLeader int32
		leaderCount          int32
	}

	testCases := map[string]struct {
		domains          []domainSpec
		params           topologyAssignmentParameters
		wantThreshold    int32
		wantDomainsCount int
	}{
		"falls back after pruning": {
			domains: []domainSpec{
				{id: "b1", levelValues: []string{"b1"}},
				{id: "b2", levelValues: []string{"b2"}},
				{id: "b1/r1", parentID: "b1", levelValues: []string{"b1", "r1"}, podCount: 3, sliceCount: 3, podCountWithLeader: 2, sliceCountWithLeader: 2, leaderCount: 1},
				{id: "b2/r1", parentID: "b2", levelValues: []string{"b2", "r1"}, podCount: 2, sliceCount: 2, podCountWithLeader: 1, sliceCountWithLeader: 1, leaderCount: 1},
				{id: "b2/r2", parentID: "b2", levelValues: []string{"b2", "r2"}, podCount: 4, sliceCount: 4, podCountWithLeader: 2, sliceCountWithLeader: 2, leaderCount: 1},
			},
			params: topologyAssignmentParameters{
				count:             8,
				sliceSize:         1,
				leaderCount:       1,
				requestedLevelIdx: 0,
				sliceLevelIdx:     1,
			},
			wantThreshold:    1,
			wantDomainsCount: 2,
		},
		"rejects after fallback": {
			domains: []domainSpec{
				{id: "b1", levelValues: []string{"b1"}},
				{id: "b2", levelValues: []string{"b2"}},
				{id: "b3", levelValues: []string{"b3"}},
				{id: "b1/r1", parentID: "b1", levelValues: []string{"b1", "r1"}, podCount: 2, sliceCount: 2, podCountWithLeader: 1, sliceCountWithLeader: 1, leaderCount: 1},
				{id: "b2/r1", parentID: "b2", levelValues: []string{"b2", "r1"}, podCount: 3, sliceCount: 3, podCountWithLeader: 1, sliceCountWithLeader: 1, leaderCount: 1},
				{id: "b2/r2", parentID: "b2", levelValues: []string{"b2", "r2"}, podCount: 4, sliceCount: 4, podCountWithLeader: 2, sliceCountWithLeader: 2, leaderCount: 1},
				{id: "b3/r1", parentID: "b3", levelValues: []string{"b3", "r1"}, podCount: 4, sliceCount: 4, podCountWithLeader: 3, sliceCountWithLeader: 3, leaderCount: 1},
			},
			params: topologyAssignmentParameters{
				count:             12,
				sliceSize:         1,
				leaderCount:       1,
				requestedLevelIdx: 0,
				sliceLevelIdx:     1,
			},
			wantThreshold:    0,
			wantDomainsCount: 0,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, "dummy", newTopologyTree([]string{"block", "rack"}, nil, 0), nil, newDefaultSimulatorSnapshot())
			domainsByID := make(map[string]*domain, len(tc.domains))
			for _, spec := range tc.domains {
				d := addDomainWithState(s, &domain{
					id:          utiltas.TopologyDomainID(spec.id),
					levelValues: spec.levelValues,
				}, domainState{
					podCount:             spec.podCount,
					sliceCount:           spec.sliceCount,
					podCountWithLeader:   spec.podCountWithLeader,
					sliceCountWithLeader: spec.sliceCountWithLeader,
					leaderCount:          spec.leaderCount,
				})
				if len(spec.parentID) == 0 {
					s.domainsPerLevel[0][d.id] = d
				} else {
					parent := domainsByID[spec.parentID]
					if parent == nil {
						t.Fatalf("Unknown parent domain %q", spec.parentID)
					}
					d.parent = parent
					parent.children = append(parent.children, d)
					s.domainsPerLevel[1][d.id] = d
				}
				domainsByID[spec.id] = d
			}

			gotDomains, gotThreshold := findBestDomainsForBalancedPlacement(s, &tc.params)

			if gotThreshold != tc.wantThreshold {
				t.Errorf("Unexpected threshold: got %d, want %d", gotThreshold, tc.wantThreshold)
			}
			if len(gotDomains) != tc.wantDomainsCount {
				t.Errorf("Unexpected domains count: got %d, want %d", len(gotDomains), tc.wantDomainsCount)
			}
		})
	}
}
