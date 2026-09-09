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

package flavorassigner

import (
	"math"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestDistributeOrderBased(t *testing.T) {
	cases := map[string]struct {
		fullCounts []int32
		deltas     []int32
		amount     int64
		wantOut    []int32
	}{
		"zero amount leaves everything at full count": {
			fullCounts: []int32{10, 10},
			deltas:     []int32{4, 2},
			amount:     0,
			wantOut:    []int32{10, 10},
		},
		"amount fully absorbed by the last podset": {
			fullCounts: []int32{10, 10},
			deltas:     []int32{4, 2},
			amount:     1,
			wantOut:    []int32{10, 9},
		},
		"amount spills into the previous podset once the last is drained": {
			fullCounts: []int32{10, 10},
			deltas:     []int32{4, 2},
			amount:     3,
			wantOut:    []int32{9, 8},
		},
		"amount equal to totalDelta drains every podset to its minimum": {
			fullCounts: []int32{10, 10},
			deltas:     []int32{4, 2},
			amount:     6,
			wantOut:    []int32{6, 8},
		},
		"podset with no room to shrink is skipped regardless of position": {
			fullCounts: []int32{10, 10, 10},
			deltas:     []int32{4, 0, 2},
			amount:     6,
			wantOut:    []int32{6, 10, 8},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			out := make([]int32, len(tc.fullCounts))
			totalDelta := int64(0)
			for _, d := range tc.deltas {
				totalDelta += int64(d)
			}
			distributeOrderBased(out, tc.fullCounts, tc.deltas, tc.amount, totalDelta)
			if diff := cmp.Diff(tc.wantOut, out); diff != "" {
				t.Errorf("Unexpected output (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestOrderedReduce(t *testing.T) {
	cases := map[string]struct {
		podSets   []kueue.PodSet
		ok        func(counts []int32) bool
		wantCount []int32
		wantFound bool
	}{
		"last podset drains to its minimum before the previous one is touched": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("ps1", 10).SetMinimumCount(6).Obj(),
				*utiltestingapi.MakePodSet("ps2", 10).SetMinimumCount(8).Obj(),
			},
			ok: func(counts []int32) bool {
				return counts[0]+counts[1] <= 16
			},
			wantCount: []int32{8, 8},
			wantFound: true,
		},
		"KEP scenario C: podset with no minCount blocks the search once others are exhausted": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("ps0", 1).Obj(),
				*utiltestingapi.MakePodSet("ps1", 4).SetMinimumCount(2).Obj(),
				*utiltestingapi.MakePodSet("ps2", 20).SetMinimumCount(10).Obj(),
			},
			ok: func(counts []int32) bool {
				return counts[0]+counts[1]+counts[2] <= 10
			},
			wantFound: false,
		},
		// The PodSets below draw on separate capacity, which is what lets a count be given back.
		// In practice that separation comes from PodSets carrying different node selectors tied
		// to different node groups, and so being assigned different resource flavors; here it is
		// just a per-PodSet bound in the fake.
		"KEP scenario A: only the last podset is cut, and only as far as needed": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("ps0", 1).Obj(),
				*utiltestingapi.MakePodSet("ps1", 4).SetMinimumCount(2).Obj(),
				*utiltestingapi.MakePodSet("ps2", 20).SetMinimumCount(10).Obj(),
			},
			// One shared pool of 19 against the 25 requested, so 6 pods have to go.
			ok: func(counts []int32) bool {
				return counts[0]+counts[1]+counts[2] <= 19
			},
			// ps2 alone absorbs the cut, so only one podset is reduced and there is nothing for
			// the give-back phase to redistribute.
			wantCount: []int32{1, 4, 14},
			wantFound: true,
		},
		"KEP scenario B: the cut spills into the previous podset, and cannot be given back": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("ps0", 1).Obj(),
				*utiltestingapi.MakePodSet("ps1", 4).SetMinimumCount(2).Obj(),
				*utiltestingapi.MakePodSet("ps2", 20).SetMinimumCount(10).Obj(),
			},
			// One shared pool of 13, so every count competes with every other.
			ok: func(counts []int32) bool {
				return counts[0]+counts[1]+counts[2] <= 13
			},
			wantCount: []int32{1, 2, 10},
			wantFound: true,
		},
		"KEP scenario D: a podset drained for another's sake is restored in full": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("ps0", 1).Obj(),
				*utiltestingapi.MakePodSet("ps1", 4).SetMinimumCount(2).Obj(),
				*utiltestingapi.MakePodSet("ps2", 20).SetMinimumCount(10).Obj(),
			},
			// ps1 capped at 2, ps2 at its full 20. Both bounds are modelled: with only the ps1
			// bound, a give-back that restored ps2 without limit would pass just as happily as
			// a correct one.
			ok: func(counts []int32) bool {
				return counts[1] <= 2 && counts[2] <= 20
			},
			// The ordered shrink drains ps2 to its minimum before it can touch ps1, even though
			// ps2's own capacity was never the constraint, so ps2 is given back afterwards.
			wantCount: []int32{1, 2, 20},
			wantFound: true,
		},
		"a podset whose own capacity is partly exhausted is given back only as far as it fits": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("ps0", 1).Obj(),
				*utiltestingapi.MakePodSet("ps1", 4).SetMinimumCount(2).Obj(),
				*utiltestingapi.MakePodSet("ps2", 20).SetMinimumCount(10).Obj(),
			},
			// ps2 has room for 15 of its 20, so the give-back lands strictly between the count
			// the shrink settled on and the full count.
			ok: func(counts []int32) bool {
				return counts[1] <= 2 && counts[2] <= 15
			},
			wantCount: []int32{1, 2, 15},
			wantFound: true,
		},
		"no podset has room to shrink": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("ps1", 5).Obj(),
			},
			ok:        func(counts []int32) bool { return true },
			wantFound: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fits := func(counts []int32) ([]int32, bool) {
				if !tc.ok(counts) {
					return nil, false
				}
				// counts is reused across calls by Reduce, so it must be
				// cloned to be safely returned as the winning result.
				out := make([]int32, len(counts))
				copy(out, counts)
				return out, true
			}
			red := NewOrderedPodSetReducer(tc.podSets, fits)
			count, found := red.Reduce()
			if found != tc.wantFound {
				t.Errorf("Unexpected found:%v, want: %v", found, tc.wantFound)
			}
			if tc.wantFound {
				if diff := cmp.Diff(tc.wantCount, count); diff != "" {
					t.Errorf("Unexpected counts (-want,+got):\n%s", diff)
				}
			}
		})
	}
}

func TestReduceTotalDeltaLarge(t *testing.T) {
	podSets := []kueue.PodSet{
		*utiltestingapi.MakePodSet("ps1", math.MaxInt32).SetMinimumCount(0).Obj(),
		*utiltestingapi.MakePodSet("ps2", math.MaxInt32).SetMinimumCount(0).Obj(),
		*utiltestingapi.MakePodSet("ps3", 1).SetMinimumCount(0).Obj(),
	}

	fits := func(counts []int32) ([]int32, bool) {
		total := int64(counts[0]) + int64(counts[1]) + int64(counts[2])
		if total > 1 {
			return nil, false
		}

		out := make([]int32, len(counts))
		copy(out, counts)
		return out, true
	}

	red := NewOrderedPodSetReducer(podSets, fits)

	if want, got := int64(4_294_967_295), red.totalDelta; got != want {
		t.Fatalf("Unexpected totalDelta: %d, want %d", got, want)
	}

	count, found := red.Reduce()
	if !found {
		t.Fatal("Expected a solution")
	}

	wantCount := []int32{1, 0, 0}
	if diff := cmp.Diff(wantCount, count); diff != "" {
		t.Errorf("Unexpected counts (-want,+got):\n%s", diff)
	}
}

// TestGiveBack covers the guard that decides whether the second pass runs at all. It calls
// giveBack directly with a fits() that accepts anything, so a pass that runs is visible in both
// the counts it returns and the number of probes it makes - which a test going through Reduce
// could not show, since with one reduced PodSet the pass provably cannot change the counts.
func TestGiveBack(t *testing.T) {
	podSets := []kueue.PodSet{
		*utiltestingapi.MakePodSet("ps1", 4).SetMinimumCount(2).Obj(),
		*utiltestingapi.MakePodSet("ps2", 20).SetMinimumCount(10).Obj(),
	}

	cases := map[string]struct {
		// counts stands in for what the ordered shrink settled on.
		counts     []int32
		wantCounts []int32
		wantProbes int
	}{
		"one reduced podset: the pass is skipped": {
			counts:     []int32{4, 15},
			wantCounts: []int32{4, 15},
			wantProbes: 0,
		},
		"two reduced podsets: both are restored": {
			counts:     []int32{3, 15},
			wantCounts: []int32{4, 20},
			wantProbes: 2,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			probes := 0
			fits := func(counts []int32) ([]int32, bool) {
				probes++
				return slices.Clone(counts), true
			}
			psr := NewOrderedPodSetReducer(podSets, fits)

			counts := slices.Clone(tc.counts)
			best, found := psr.giveBack(counts, slices.Clone(tc.counts))
			if !found {
				t.Fatal("Expected giveBack to keep the solution it was given")
			}
			if diff := cmp.Diff(tc.wantCounts, counts); diff != "" {
				t.Errorf("Unexpected counts (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantCounts, best); diff != "" {
				t.Errorf("Unexpected result, out of step with counts (-want,+got):\n%s", diff)
			}
			if probes != tc.wantProbes {
				t.Errorf("Unexpected fits() probes: %d, want %d", probes, tc.wantProbes)
			}
		})
	}
}
