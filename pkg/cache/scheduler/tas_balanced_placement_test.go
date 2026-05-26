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

	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestSelectOptimalDomainSetToFit(t *testing.T) {
	d1 := &domain{id: "d1", state: 9, sliceState: 9, leaderState: 1, stateWithLeader: 8, sliceStateWithLeader: 8}
	d2 := &domain{id: "d2", state: 6, sliceState: 6, leaderState: 0, stateWithLeader: 6, sliceStateWithLeader: 6}
	d3 := &domain{id: "d3", state: 4, sliceState: 4, leaderState: 1, stateWithLeader: 3, sliceStateWithLeader: 3}
	d4 := &domain{id: "d4", state: 2, sliceState: 2, leaderState: 0, stateWithLeader: 2, sliceStateWithLeader: 2}

	testCases := map[string]struct {
		domains     []*domain
		workerCount int32
		leaderCount int32
		want        []string
	}{
		"no fit": {
			domains:     []*domain{d1, d2, d3, d4},
			workerCount: 22,
			leaderCount: 0,
			want:        []string{},
		},
		"simple fit one domain": {
			domains:     []*domain{d1, d2, d3, d4},
			workerCount: 5,
			leaderCount: 1,
			want:        []string{"d1"},
		},
		"perfect fit with two domains": {
			domains:     []*domain{d1, d2, d3, d4},
			workerCount: 9,
			leaderCount: 1,
			want:        []string{"d2", "d3"},
		},
		"perfect fit with two domains 2": {
			domains:     []*domain{d1, d2, d3, d4},
			workerCount: 10,
			leaderCount: 1,
			want:        []string{"d1", "d4"},
		},
		"best fit, single domain": {
			domains:     []*domain{d1, d2, d3, d4},
			workerCount: 5,
			leaderCount: 0,
			want:        []string{"d2"},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, "dummy", []string{}, nil)
			got := selectOptimalDomainSetToFit(s, tc.domains, tc.workerCount, tc.leaderCount, 1, true)
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

func TestPlaceSlicesOnDomainsBalanced(t *testing.T) {
	d1 := &domain{id: "d1", state: 18, sliceState: 18, stateWithLeader: 18, leaderState: 0, sliceStateWithLeader: 18}
	d2 := &domain{id: "d2", state: 18, sliceState: 18, stateWithLeader: 18, leaderState: 0, sliceStateWithLeader: 18}
	d3 := &domain{id: "d3", state: 18, sliceState: 18, stateWithLeader: 18, leaderState: 0, sliceStateWithLeader: 18}
	d4 := &domain{id: "d4", state: 10, sliceState: 10, stateWithLeader: 10, leaderState: 0, sliceStateWithLeader: 10}
	d5 := &domain{id: "d5", state: 2, sliceState: 2, stateWithLeader: 2, leaderState: 0, sliceStateWithLeader: 2}

	testCases := map[string]struct {
		domains     []*domain
		sliceCount  int32
		leaderCount int32
		sliceSize   int32
		threshold   int32
		want        []*domain
	}{
		"simple balanced placement on two domains": {
			domains:     []*domain{d1, d2, d3},
			sliceCount:  20,
			leaderCount: 0,
			sliceSize:   1,
			threshold:   10,
			want: []*domain{
				{id: "d1", sliceState: 10, state: 10, stateWithLeader: 10, sliceStateWithLeader: 10, leaderState: 0},
				{id: "d2", sliceState: 10, state: 10, stateWithLeader: 10, sliceStateWithLeader: 10, leaderState: 0},
			},
		},
		"simple placement on three domains": {
			domains:     []*domain{d1, d2, d3},
			sliceCount:  40,
			leaderCount: 0,
			sliceSize:   1,
			threshold:   13,
			want: []*domain{
				{id: "d1", sliceState: 14, state: 14, stateWithLeader: 14, sliceStateWithLeader: 14, leaderState: 0},
				{id: "d2", sliceState: 13, state: 13, stateWithLeader: 13, sliceStateWithLeader: 13, leaderState: 0},
				{id: "d3", sliceState: 13, state: 13, stateWithLeader: 13, sliceStateWithLeader: 13, leaderState: 0},
			},
		},
		"find smallest domain that fits": {
			domains:     []*domain{d1, d2, d3, d4, d5},
			sliceCount:  2,
			leaderCount: 0,
			sliceSize:   1,
			threshold:   2,
			want: []*domain{
				{id: "d5", sliceState: 2, state: 2, stateWithLeader: 2, sliceStateWithLeader: 2, leaderState: 0},
			},
		},
		"correctly select domains": {
			domains:     []*domain{d1, d2, d3, d4, d5},
			sliceCount:  25,
			leaderCount: 0,
			sliceSize:   1,
			threshold:   10,
			want: []*domain{
				{id: "d1", sliceState: 15, state: 15, stateWithLeader: 15, sliceStateWithLeader: 15, leaderState: 0},
				{id: "d4", sliceState: 10, state: 10, stateWithLeader: 10, sliceStateWithLeader: 10, leaderState: 0},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			domains := make([]*domain, len(tc.domains))
			_, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, "dummy", []string{}, nil)
			for i, d := range tc.domains {
				clone := *d
				domains[i] = &clone
			}

			got, _ := placeSlicesOnDomainsBalanced(s, domains, tc.sliceCount, tc.leaderCount, tc.sliceSize, tc.threshold)

			if diff := cmp.Diff(
				tc.want,
				got,
				cmp.AllowUnexported(domain{}),
				cmpopts.IgnoreFields(domain{}, "parent", "children", "levelValues"),
				cmpopts.SortSlices(func(a, b *domain) bool { return a.id < b.id }),
			); diff != "" {
				t.Errorf("Unexpected domains (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestPruneDomainsBelowThreshold(t *testing.T) {
	domainState := func(d *domain) [5]int32 {
		return [5]int32{d.state, d.sliceState, d.stateWithLeader, d.sliceStateWithLeader, d.leaderState}
	}

	testCases := map[string]struct {
		domains        func() ([]*domain, map[string]*domain)
		threshold      int32
		sliceSize      int32
		sliceLevelIdx  int
		level          int
		leaderRequired bool
		want           map[string][5]int32
	}{
		"keeps worker only domain": {
			domains: func() ([]*domain, map[string]*domain) {
				leaderLeaf := &domain{id: "leader-leaf", state: 6, sliceState: 6, leaderState: 1, stateWithLeader: 5, sliceStateWithLeader: 5}
				leaderDomain := &domain{id: "leader-domain", state: 6, sliceState: 6, leaderState: 1, stateWithLeader: 5, sliceStateWithLeader: 5, children: []*domain{leaderLeaf}}
				leaderLeaf.parent = leaderDomain
				workerOnlyLeaf := &domain{id: "worker-only-leaf", state: 5, sliceState: 5, leaderState: 1, stateWithLeader: 4, sliceStateWithLeader: 4}
				workerOnlyDomain := &domain{id: "worker-only-domain", state: 5, sliceState: 5, leaderState: 1, stateWithLeader: 4, sliceStateWithLeader: 4, children: []*domain{workerOnlyLeaf}}
				workerOnlyLeaf.parent = workerOnlyDomain
				parentDomain := &domain{id: "parent-domain", children: []*domain{leaderDomain, workerOnlyDomain}}
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
			domains, domainsByName := tc.domains()
			_, log := utiltesting.ContextWithLog(t)
			s := newTASFlavorSnapshot(log, "dummy", []string{}, nil)

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
			s := newTASFlavorSnapshot(log, "dummy", []string{"block", "rack"}, nil)
			domainsByID := make(map[string]*domain, len(tc.domains))
			for _, spec := range tc.domains {
				d := &domain{
					id:                   utiltas.TopologyDomainID(spec.id),
					levelValues:          spec.levelValues,
					state:                spec.state,
					sliceState:           spec.sliceState,
					stateWithLeader:      spec.stateWithLeader,
					sliceStateWithLeader: spec.sliceStateWithLeader,
					leaderState:          spec.leaderState,
				}
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
