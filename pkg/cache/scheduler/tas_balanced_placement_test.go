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

	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func domainIDs(domains []*simulator.Domain) []string {
	return utilslices.Map(domains, func(d **simulator.Domain) string { return string((*d).ID) })
}

func TestSelectOptimalDomainSetToFit(t *testing.T) {
	d1 := &simulator.Domain{ID: "d1", LevelValues: []string{"d1"}, State: 9, SliceState: 9, LeaderState: 1, StateWithLeader: 8, SliceStateWithLeader: 8}
	d2 := &simulator.Domain{ID: "d2", LevelValues: []string{"d2"}, State: 6, SliceState: 6, LeaderState: 0, StateWithLeader: 6, SliceStateWithLeader: 6}
	d3 := &simulator.Domain{ID: "d3", LevelValues: []string{"d3"}, State: 4, SliceState: 4, LeaderState: 1, StateWithLeader: 3, SliceStateWithLeader: 3}
	d4 := &simulator.Domain{ID: "d4", LevelValues: []string{"d4"}, State: 2, SliceState: 2, LeaderState: 0, StateWithLeader: 2, SliceStateWithLeader: 2}

	testCases := map[string]struct {
		domains     []*simulator.Domain
		workerCount int32
		leaderCount int32
		want        []string
	}{
		"no fit": {
			domains:     []*simulator.Domain{d1, d2, d3, d4},
			workerCount: 22,
			leaderCount: 0,
			want:        []string{},
		},
		"simple fit one domain": {
			domains:     []*simulator.Domain{d1, d2, d3, d4},
			workerCount: 5,
			leaderCount: 1,
			want:        []string{"d1"},
		},
		"perfect fit with two domains": {
			domains:     []*simulator.Domain{d1, d2, d3, d4},
			workerCount: 9,
			leaderCount: 1,
			want:        []string{"d2", "d3"},
		},
		"perfect fit with two domains 2": {
			domains:     []*simulator.Domain{d1, d2, d3, d4},
			workerCount: 10,
			leaderCount: 1,
			want:        []string{"d1", "d4"},
		},
		"best fit, single domain": {
			domains:     []*simulator.Domain{d1, d2, d3, d4},
			workerCount: 5,
			leaderCount: 0,
			want:        []string{"d2"},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, "dummy", []string{}, nil, &defaultChecker{})
			got := selectOptimalDomainSetToFit(s, tc.domains, tc.workerCount, tc.leaderCount, 1, true)
			gotIDs := make([]string, len(got))
			for i, d := range got {
				gotIDs[i] = string(d.ID)
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
			s := newTASFlavorSnapshot(log, "dummy", []string{}, nil, &defaultChecker{})
			domains := []*simulator.Domain{
				{ID: "leaf-a", LevelValues: []string{"block-b", "host-a"}, State: 3, SliceState: 3, StateWithLeader: 3, SliceStateWithLeader: 3},
				{ID: "leaf-m", LevelValues: []string{"block-b", "host-m"}, State: 3, SliceState: 3, StateWithLeader: 3, SliceStateWithLeader: 3},
				{ID: "leaf-z", LevelValues: []string{"block-a", "host-z"}, State: 3, SliceState: 3, StateWithLeader: 3, SliceStateWithLeader: 3},
				{ID: "leaf-zz", LevelValues: []string{"block-a", "host-zz"}, State: 3, SliceState: 3, StateWithLeader: 3, SliceStateWithLeader: 3},
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
		domains []*simulator.Domain
		want    []string
	}{
		"tie-breaking on level values when capacity and entropy are equal": {
			domains: []*simulator.Domain{
				{ID: "leaf-a", LevelValues: []string{"block-b", "host-a"}, LeaderState: 1, SliceStateWithLeader: 5, Children: []*simulator.Domain{{State: 2}, {State: 2}}},
				{ID: "leaf-m", LevelValues: []string{"block-b", "host-m"}, LeaderState: 1, SliceStateWithLeader: 5, Children: []*simulator.Domain{{State: 2}, {State: 2}}},
				{ID: "leaf-z", LevelValues: []string{"block-a", "host-z"}, LeaderState: 1, SliceStateWithLeader: 5, Children: []*simulator.Domain{{State: 2}, {State: 2}}},
			},
			want: []string{"leaf-z", "leaf-a", "leaf-m"},
		},
		"capacity overrides entropy, and higher entropy overrides level values": {
			domains: []*simulator.Domain{
				{ID: "lower-leader", LevelValues: []string{"a"}, LeaderState: 0, SliceStateWithLeader: 100, Children: []*simulator.Domain{{State: 50}, {State: 50}}},
				{ID: "lower-capacity", LevelValues: []string{"b"}, LeaderState: 1, SliceStateWithLeader: 4, Children: []*simulator.Domain{{State: 2}, {State: 2}}},
				{ID: "low-entropy", LevelValues: []string{"c"}, LeaderState: 1, SliceStateWithLeader: 5, Children: []*simulator.Domain{{State: 4}, {State: 0}}},
				{ID: "high-entropy", LevelValues: []string{"d"}, LeaderState: 1, SliceStateWithLeader: 5, Children: []*simulator.Domain{{State: 2}, {State: 2}}},
			},
			want: []string{"high-entropy", "low-entropy", "lower-capacity", "lower-leader"},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			slices.SortFunc(tc.domains, compareDomainCapacityAndEntropy)

			if diff := cmp.Diff(tc.want, domainIDs(tc.domains)); diff != "" {
				t.Errorf("unexpected domain order (-want,+got): %s", diff)
			}
		})
	}
}

func TestPlaceSlicesOnDomainsBalanced(t *testing.T) {
	d1 := &simulator.Domain{ID: "d1", LevelValues: []string{"d1"}, State: 18, SliceState: 18, StateWithLeader: 18, LeaderState: 0, SliceStateWithLeader: 18}
	d2 := &simulator.Domain{ID: "d2", LevelValues: []string{"d2"}, State: 18, SliceState: 18, StateWithLeader: 18, LeaderState: 0, SliceStateWithLeader: 18}
	d3 := &simulator.Domain{ID: "d3", LevelValues: []string{"d3"}, State: 18, SliceState: 18, StateWithLeader: 18, LeaderState: 0, SliceStateWithLeader: 18}
	d4 := &simulator.Domain{ID: "d4", LevelValues: []string{"d4"}, State: 10, SliceState: 10, StateWithLeader: 10, LeaderState: 0, SliceStateWithLeader: 10}
	d5 := &simulator.Domain{ID: "d5", LevelValues: []string{"d5"}, State: 2, SliceState: 2, StateWithLeader: 2, LeaderState: 0, SliceStateWithLeader: 2}

	testCases := map[string]struct {
		domains     []*simulator.Domain
		sliceCount  int32
		leaderCount int32
		sliceSize   int32
		threshold   int32
		want        []*simulator.Domain
	}{
		"simple balanced placement on two domains": {
			domains:     []*simulator.Domain{d1, d2, d3},
			sliceCount:  20,
			leaderCount: 0,
			sliceSize:   1,
			threshold:   10,
			want: []*simulator.Domain{
				{ID: "d1", SliceState: 10, State: 10, StateWithLeader: 10, SliceStateWithLeader: 10, LeaderState: 0},
				{ID: "d2", SliceState: 10, State: 10, StateWithLeader: 10, SliceStateWithLeader: 10, LeaderState: 0},
			},
		},
		"simple placement on three domains": {
			domains:     []*simulator.Domain{d1, d2, d3},
			sliceCount:  40,
			leaderCount: 0,
			sliceSize:   1,
			threshold:   13,
			want: []*simulator.Domain{
				{ID: "d1", SliceState: 14, State: 14, StateWithLeader: 14, SliceStateWithLeader: 14, LeaderState: 0},
				{ID: "d2", SliceState: 13, State: 13, StateWithLeader: 13, SliceStateWithLeader: 13, LeaderState: 0},
				{ID: "d3", SliceState: 13, State: 13, StateWithLeader: 13, SliceStateWithLeader: 13, LeaderState: 0},
			},
		},
		"find smallest domain that fits": {
			domains:     []*simulator.Domain{d1, d2, d3, d4, d5},
			sliceCount:  2,
			leaderCount: 0,
			sliceSize:   1,
			threshold:   2,
			want: []*simulator.Domain{
				{ID: "d5", SliceState: 2, State: 2, StateWithLeader: 2, SliceStateWithLeader: 2, LeaderState: 0},
			},
		},
		"correctly select domains": {
			domains:     []*simulator.Domain{d1, d2, d3, d4, d5},
			sliceCount:  25,
			leaderCount: 0,
			sliceSize:   1,
			threshold:   10,
			want: []*simulator.Domain{
				{ID: "d1", SliceState: 15, State: 15, StateWithLeader: 15, SliceStateWithLeader: 15, LeaderState: 0},
				{ID: "d4", SliceState: 10, State: 10, StateWithLeader: 10, SliceStateWithLeader: 10, LeaderState: 0},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			domains := make([]*simulator.Domain, len(tc.domains))
			_, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, "dummy", []string{}, nil, &defaultChecker{})
			for i, d := range tc.domains {
				clone := *d
				domains[i] = &clone
			}

			got, _ := placeSlicesOnDomainsBalanced(s, domains, tc.sliceCount, tc.leaderCount, tc.sliceSize, tc.threshold)

			if diff := cmp.Diff(
				tc.want,
				got,
				cmp.AllowUnexported(simulator.Domain{}),
				cmpopts.IgnoreFields(simulator.Domain{}, "Parent", "Children", "LevelValues"),
				cmpopts.SortSlices(func(a, b *simulator.Domain) bool { return a.ID < b.ID }),
			); diff != "" {
				t.Errorf("Unexpected domains (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestPlaceSlicesOnDomainsBalancedStableTieBreak(t *testing.T) {
	_, log := utiltesting.ContextWithLog(t)
	s := newTASFlavorSnapshot(log, "dummy", []string{}, nil, &defaultChecker{})
	domains := []*simulator.Domain{
		{ID: "leaf-a", LevelValues: []string{"block-b", "host-a"}, State: 3, SliceState: 3, StateWithLeader: 3, SliceStateWithLeader: 3},
		{ID: "leaf-z", LevelValues: []string{"block-a", "host-z"}, State: 3, SliceState: 3, StateWithLeader: 3, SliceStateWithLeader: 3},
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
	domainState := func(d *simulator.Domain) [5]int32 {
		return [5]int32{d.State, d.SliceState, d.StateWithLeader, d.SliceStateWithLeader, d.LeaderState}
	}

	testCases := map[string]struct {
		domains        func() ([]*simulator.Domain, map[string]*simulator.Domain)
		threshold      int32
		sliceSize      int32
		sliceLevelIdx  int
		level          int
		leaderRequired bool
		want           map[string][5]int32
	}{
		"keeps worker only domain": {
			domains: func() ([]*simulator.Domain, map[string]*simulator.Domain) {
				leaderLeaf := &simulator.Domain{
					ID:                   "leader-leaf",
					State:                6,
					SliceState:           6,
					LeaderState:          1,
					StateWithLeader:      5,
					SliceStateWithLeader: 5,
				}
				leaderDomain := &simulator.Domain{
					ID:                   "leader-domain",
					State:                6,
					SliceState:           6,
					LeaderState:          1,
					StateWithLeader:      5,
					SliceStateWithLeader: 5,
					Children:             []*simulator.Domain{leaderLeaf},
				}
				leaderLeaf.Parent = leaderDomain
				workerOnlyLeaf := &simulator.Domain{
					ID:                   "worker-only-leaf",
					State:                5,
					SliceState:           5,
					LeaderState:          1,
					StateWithLeader:      4,
					SliceStateWithLeader: 4,
				}
				workerOnlyDomain := &simulator.Domain{
					ID:                   "worker-only-domain",
					State:                5,
					SliceState:           5,
					LeaderState:          1,
					StateWithLeader:      4,
					SliceStateWithLeader: 4,
					Children:             []*simulator.Domain{workerOnlyLeaf},
				}
				workerOnlyLeaf.Parent = workerOnlyDomain
				parentDomain := &simulator.Domain{
					ID:       "parent-domain",
					Children: []*simulator.Domain{leaderDomain, workerOnlyDomain},
				}
				leaderDomain.Parent = parentDomain
				workerOnlyDomain.Parent = parentDomain
				return []*simulator.Domain{parentDomain}, map[string]*simulator.Domain{
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
			domains, domainsByName := tc.domains()
			_, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, "dummy", []string{}, nil, &defaultChecker{})

			s.pruneDomainsBelowThreshold(domains, tc.threshold, tc.sliceSize, tc.sliceLevelIdx, tc.level, tc.leaderRequired)

			got := make(map[string][5]int32, len(tc.want))
			for name := range tc.want {
				got[name] = domainState(domainsByName[name])
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Unexpected domain state (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestFindBestDomainsForBalancedPlacement(t *testing.T) {
	type domainSpec struct {
		ID                   string
		ParentID             string
		LevelValues          []string
		State                int32
		SliceState           int32
		StateWithLeader      int32
		SliceStateWithLeader int32
		LeaderState          int32
	}

	testCases := map[string]struct {
		domains          []domainSpec
		params           topologyAssignmentParameters
		wantThreshold    int32
		wantDomainsCount int
	}{
		"falls back after pruning": {
			domains: []domainSpec{
				{ID: "b1", LevelValues: []string{"b1"}},
				{ID: "b2", LevelValues: []string{"b2"}},
				{ID: "b1/r1", ParentID: "b1", LevelValues: []string{"b1", "r1"}, State: 3, SliceState: 3, StateWithLeader: 2, SliceStateWithLeader: 2, LeaderState: 1},
				{ID: "b2/r1", ParentID: "b2", LevelValues: []string{"b2", "r1"}, State: 2, SliceState: 2, StateWithLeader: 1, SliceStateWithLeader: 1, LeaderState: 1},
				{ID: "b2/r2", ParentID: "b2", LevelValues: []string{"b2", "r2"}, State: 4, SliceState: 4, StateWithLeader: 2, SliceStateWithLeader: 2, LeaderState: 1},
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
				{ID: "b1", LevelValues: []string{"b1"}},
				{ID: "b2", LevelValues: []string{"b2"}},
				{ID: "b3", LevelValues: []string{"b3"}},
				{ID: "b1/r1", ParentID: "b1", LevelValues: []string{"b1", "r1"}, State: 2, SliceState: 2, StateWithLeader: 1, SliceStateWithLeader: 1, LeaderState: 1},
				{ID: "b2/r1", ParentID: "b2", LevelValues: []string{"b2", "r1"}, State: 3, SliceState: 3, StateWithLeader: 1, SliceStateWithLeader: 1, LeaderState: 1},
				{ID: "b2/r2", ParentID: "b2", LevelValues: []string{"b2", "r2"}, State: 4, SliceState: 4, StateWithLeader: 2, SliceStateWithLeader: 2, LeaderState: 1},
				{ID: "b3/r1", ParentID: "b3", LevelValues: []string{"b3", "r1"}, State: 4, SliceState: 4, StateWithLeader: 3, SliceStateWithLeader: 3, LeaderState: 1},
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
			s := newTASFlavorSnapshot(log, "dummy", []string{"block", "rack"}, nil, &defaultChecker{})
			domainsByID := make(map[string]*simulator.Domain, len(tc.domains))
			for _, spec := range tc.domains {
				d := &simulator.Domain{
					ID:                   utiltas.TopologyDomainID(spec.ID),
					LevelValues:          spec.LevelValues,
					State:                spec.State,
					SliceState:           spec.SliceState,
					StateWithLeader:      spec.StateWithLeader,
					SliceStateWithLeader: spec.SliceStateWithLeader,
					LeaderState:          spec.LeaderState,
				}
				if len(spec.ParentID) == 0 {
					s.domainsPerLevel[0][d.ID] = d
				} else {
					parent := domainsByID[spec.ParentID]
					if parent == nil {
						t.Fatalf("Unknown parent domain %q", spec.ParentID)
					}
					d.Parent = parent
					parent.Children = append(parent.Children, d)
					s.domainsPerLevel[1][d.ID] = d
				}
				domainsByID[spec.ID] = d
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
