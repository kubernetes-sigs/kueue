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

func addDomainWithState(s *TASFlavorSnapshot, d *domain, st domainState) *domain {
	d.idx = len(s.state)
	s.state = append(s.state, st)
	return d
}

func TestSelectOptimalDomainSetToFit(t *testing.T) {
	d1 := testDomainSpec{
		domain: domain{id: "d1", levelValues: []string{"d1"}},
		state: domainState{
			state:                9,
			sliceState:           9,
			leaderState:          1,
			stateWithLeader:      8,
			sliceStateWithLeader: 8,
		},
	}
	d2 := testDomainSpec{
		domain: domain{id: "d2", levelValues: []string{"d2"}},
		state: domainState{
			state:                6,
			sliceState:           6,
			leaderState:          0,
			stateWithLeader:      6,
			sliceStateWithLeader: 6,
		},
	}
	d3 := testDomainSpec{
		domain: domain{id: "d3", levelValues: []string{"d3"}},
		state: domainState{
			state:                4,
			sliceState:           4,
			leaderState:          1,
			stateWithLeader:      3,
			sliceStateWithLeader: 3,
		},
	}
	d4 := testDomainSpec{
		domain: domain{id: "d4", levelValues: []string{"d4"}},
		state: domainState{
			state:                2,
			sliceState:           2,
			leaderState:          0,
			stateWithLeader:      2,
			sliceStateWithLeader: 2,
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
			s := newTASFlavorSnapshot(log, "dummy", newTopologyTree([]string{}, nil, 0), nil, &defaultChecker{})
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
			s := newTASFlavorSnapshot(log, "dummy", newTopologyTree([]string{}, nil, 0), nil, &defaultChecker{})
			equalState := domainState{state: 3, sliceState: 3, stateWithLeader: 3, sliceStateWithLeader: 3}
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
				leaderState := domainState{leaderState: 1, sliceStateWithLeader: 5}
				childState := domainState{state: 2}
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
						addDomainWithState(s, &domain{}, domainState{state: 50}), addDomainWithState(s, &domain{}, domainState{state: 50}),
					}}, domainState{leaderState: 0, sliceStateWithLeader: 100}),
					addDomainWithState(s, &domain{id: "lower-capacity", levelValues: []string{"b"}, children: []*domain{
						addDomainWithState(s, &domain{}, domainState{state: 2}), addDomainWithState(s, &domain{}, domainState{state: 2}),
					}}, domainState{leaderState: 1, sliceStateWithLeader: 4}),
					addDomainWithState(s, &domain{id: "low-entropy", levelValues: []string{"c"}, children: []*domain{
						addDomainWithState(s, &domain{}, domainState{state: 4}), addDomainWithState(s, &domain{}, domainState{state: 0}),
					}}, domainState{leaderState: 1, sliceStateWithLeader: 5}),
					addDomainWithState(s, &domain{id: "high-entropy", levelValues: []string{"d"}, children: []*domain{
						addDomainWithState(s, &domain{}, domainState{state: 2}), addDomainWithState(s, &domain{}, domainState{state: 2}),
					}}, domainState{leaderState: 1, sliceStateWithLeader: 5}),
				}
			},
			want: []string{"high-entropy", "low-entropy", "lower-capacity", "lower-leader"},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, "dummy", newTopologyTree([]string{}, nil, 0), nil, &defaultChecker{})
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
			state:                18,
			sliceState:           18,
			stateWithLeader:      18,
			leaderState:          0,
			sliceStateWithLeader: 18,
		},
	}
	d2 := testDomainSpec{
		domain: domain{id: "d2", levelValues: []string{"d2"}},
		state: domainState{
			state:                18,
			sliceState:           18,
			stateWithLeader:      18,
			leaderState:          0,
			sliceStateWithLeader: 18,
		},
	}
	d3 := testDomainSpec{
		domain: domain{id: "d3", levelValues: []string{"d3"}},
		state: domainState{
			state:                18,
			sliceState:           18,
			stateWithLeader:      18,
			leaderState:          0,
			sliceStateWithLeader: 18,
		},
	}
	d4 := testDomainSpec{
		domain: domain{id: "d4", levelValues: []string{"d4"}},
		state: domainState{
			state:                10,
			sliceState:           10,
			stateWithLeader:      10,
			leaderState:          0,
			sliceStateWithLeader: 10,
		},
	}
	d5 := testDomainSpec{
		domain: domain{id: "d5", levelValues: []string{"d5"}},
		state: domainState{
			state:                2,
			sliceState:           2,
			stateWithLeader:      2,
			leaderState:          0,
			sliceStateWithLeader: 2,
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
				"d1": {sliceState: 10, state: 10, stateWithLeader: 10, sliceStateWithLeader: 10, leaderState: 0},
				"d2": {sliceState: 10, state: 10, stateWithLeader: 10, sliceStateWithLeader: 10, leaderState: 0},
			},
		},
		"simple placement on three domains": {
			domains:     []testDomainSpec{d1, d2, d3},
			sliceCount:  40,
			leaderCount: 0,
			sliceSize:   1,
			threshold:   13,
			want: map[string]domainState{
				"d1": {sliceState: 14, state: 14, stateWithLeader: 14, sliceStateWithLeader: 14, leaderState: 0},
				"d2": {sliceState: 13, state: 13, stateWithLeader: 13, sliceStateWithLeader: 13, leaderState: 0},
				"d3": {sliceState: 13, state: 13, stateWithLeader: 13, sliceStateWithLeader: 13, leaderState: 0},
			},
		},
		"find smallest domain that fits": {
			domains:     []testDomainSpec{d1, d2, d3, d4, d5},
			sliceCount:  2,
			leaderCount: 0,
			sliceSize:   1,
			threshold:   2,
			want: map[string]domainState{
				"d5": {sliceState: 2, state: 2, stateWithLeader: 2, sliceStateWithLeader: 2, leaderState: 0},
			},
		},
		"correctly select domains": {
			domains:     []testDomainSpec{d1, d2, d3, d4, d5},
			sliceCount:  25,
			leaderCount: 0,
			sliceSize:   1,
			threshold:   10,
			want: map[string]domainState{
				"d1": {sliceState: 15, state: 15, stateWithLeader: 15, sliceStateWithLeader: 15, leaderState: 0},
				"d4": {sliceState: 10, state: 10, stateWithLeader: 10, sliceStateWithLeader: 10, leaderState: 0},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, "dummy", newTopologyTree([]string{}, nil, 0), nil, &defaultChecker{})
			domains := addDomainsWithState(s, tc.domains)

			got, _ := placeSlicesOnDomainsBalanced(s, domains, tc.sliceCount, tc.leaderCount, tc.sliceSize, tc.threshold)

			gotStates := make(map[string]domainState, len(got))
			for _, d := range got {
				gotStates[string(d.id)] = *s.stateOf(d)
			}
			if diff := cmp.Diff(tc.want, gotStates, cmp.AllowUnexported(domainState{})); diff != "" {
				t.Errorf("Unexpected domains (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestPlaceSlicesOnDomainsBalancedStableTieBreak(t *testing.T) {
	_, log := utiltesting.ContextWithLog(t)
	s := newTASFlavorSnapshot(log, "dummy", newTopologyTree([]string{}, nil, 0), nil, &defaultChecker{})
	equalState := domainState{state: 3, sliceState: 3, stateWithLeader: 3, sliceStateWithLeader: 3}
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
		st := s.stateOf(d)
		return [5]int32{st.state, st.sliceState, st.stateWithLeader, st.sliceStateWithLeader, st.leaderState}
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
					state:                6,
					sliceState:           6,
					leaderState:          1,
					stateWithLeader:      5,
					sliceStateWithLeader: 5,
				})
				leaderDomain := addDomainWithState(s, &domain{
					id:       "leader-domain",
					children: []*domain{leaderLeaf},
				}, domainState{
					state:                6,
					sliceState:           6,
					leaderState:          1,
					stateWithLeader:      5,
					sliceStateWithLeader: 5,
				})
				leaderLeaf.parent = leaderDomain
				workerOnlyLeaf := addDomainWithState(s, &domain{
					id: "worker-only-leaf",
				}, domainState{
					state:                5,
					sliceState:           5,
					leaderState:          1,
					stateWithLeader:      4,
					sliceStateWithLeader: 4,
				})
				workerOnlyDomain := addDomainWithState(s, &domain{
					id:       "worker-only-domain",
					children: []*domain{workerOnlyLeaf},
				}, domainState{
					state:                5,
					sliceState:           5,
					leaderState:          1,
					stateWithLeader:      4,
					sliceStateWithLeader: 4,
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
			s := newTASFlavorSnapshot(log, "dummy", newTopologyTree([]string{}, nil, 0), nil, &defaultChecker{})
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
	s := newTASFlavorSnapshot(log, "dummy", newTopologyTree([]string{}, nil, 0), nil, &defaultChecker{})
	prunedLeaf := addDomainWithState(s, &domain{id: "pruned-leaf"}, domainState{
		sliceState:    1,
		affinityScore: 100,
	})
	keptLeaf := addDomainWithState(s, &domain{id: "kept-leaf"}, domainState{
		state:           4,
		sliceState:      4,
		stateWithLeader: 4,
		affinityScore:   10,
	})
	parent := addDomainWithState(s, &domain{
		id:       "parent",
		children: []*domain{prunedLeaf, keptLeaf},
	}, domainState{})
	prunedLeaf.parent = parent
	keptLeaf.parent = parent

	s.pruneDomainsBelowThreshold([]*domain{parent}, 2, 1, 1, 0, false)

	if got, want := s.stateOf(prunedLeaf).affinityScore, int64(100); got != want {
		t.Errorf("Unexpected pruned leaf affinity score: got %d, want %d", got, want)
	}
	if got, want := s.stateOf(parent).affinityScore, int64(110); got != want {
		t.Errorf("Unexpected parent affinity score: got %d, want %d", got, want)
	}
}

func TestFindBestDomainsForBalancedPlacement(t *testing.T) {
	type domainSpec struct {
		id                   string
		parentID             string
		levelValues          []string
		state                int32
		sliceState           int32
		stateWithLeader      int32
		sliceStateWithLeader int32
		leaderState          int32
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
				{id: "b1/r1", parentID: "b1", levelValues: []string{"b1", "r1"}, state: 3, sliceState: 3, stateWithLeader: 2, sliceStateWithLeader: 2, leaderState: 1},
				{id: "b2/r1", parentID: "b2", levelValues: []string{"b2", "r1"}, state: 2, sliceState: 2, stateWithLeader: 1, sliceStateWithLeader: 1, leaderState: 1},
				{id: "b2/r2", parentID: "b2", levelValues: []string{"b2", "r2"}, state: 4, sliceState: 4, stateWithLeader: 2, sliceStateWithLeader: 2, leaderState: 1},
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
				{id: "b1/r1", parentID: "b1", levelValues: []string{"b1", "r1"}, state: 2, sliceState: 2, stateWithLeader: 1, sliceStateWithLeader: 1, leaderState: 1},
				{id: "b2/r1", parentID: "b2", levelValues: []string{"b2", "r1"}, state: 3, sliceState: 3, stateWithLeader: 1, sliceStateWithLeader: 1, leaderState: 1},
				{id: "b2/r2", parentID: "b2", levelValues: []string{"b2", "r2"}, state: 4, sliceState: 4, stateWithLeader: 2, sliceStateWithLeader: 2, leaderState: 1},
				{id: "b3/r1", parentID: "b3", levelValues: []string{"b3", "r1"}, state: 4, sliceState: 4, stateWithLeader: 3, sliceStateWithLeader: 3, leaderState: 1},
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
			s := newTASFlavorSnapshot(log, "dummy", newTopologyTree([]string{"block", "rack"}, nil, 0), nil, &defaultChecker{})
			domainsByID := make(map[string]*domain, len(tc.domains))
			for _, spec := range tc.domains {
				d := addDomainWithState(s, &domain{
					id:          utiltas.TopologyDomainID(spec.id),
					levelValues: spec.levelValues,
				}, domainState{
					state:                spec.state,
					sliceState:           spec.sliceState,
					stateWithLeader:      spec.stateWithLeader,
					sliceStateWithLeader: spec.sliceStateWithLeader,
					leaderState:          spec.leaderState,
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
