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
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

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
			s := newTASFlavorSnapshot(log, "dummy", []string{}, nil, "")
			// The original instruction included a loop `for _, node := range nodes { s.addNode(node) }`
			// and a partial line `tToFit(...)`.
			// Since `nodes` is undefined and `tToFit` is a fragment,
			// and to ensure the resulting file is syntactically correct as per instructions,
			// only the `newTASFlavorSnapshot` argument change is applied.
			got := selectOptimalDomainSetToFit(s, tc.domains, tc.workerCount, tc.leaderCount, 1, true, "")
			gotIDs := make([]string, len(got))
			for i, d := range got {
				gotIDs[i] = string(d.id)
			}
			sort.Strings(gotIDs)
			sort.Strings(tc.want)
			if diff := cmp.Diff(tc.want, gotIDs); diff != "" {
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
			s := newTASFlavorSnapshot(log, "dummy", []string{}, nil, "")
			for i, d := range tc.domains {
				clone := *d
				domains[i] = &clone
			}

			got, _ := placeSlicesOnDomainsBalanced(s, domains, tc.sliceCount, tc.leaderCount, tc.sliceSize, tc.threshold, "")

			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(domain{}), cmpopts.IgnoreFields(domain{}, "parent", "children", "levelValues"), cmpopts.SortSlices(func(a, b *domain) bool { return a.id < b.id })); diff != "" {
				t.Errorf("Unexpected domains (-want,+got):\n%s", diff)
			}
		})
	}
}
