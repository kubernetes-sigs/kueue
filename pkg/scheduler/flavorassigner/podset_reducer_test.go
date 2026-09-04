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

func TestOrderedSearch(t *testing.T) {
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
		"KEP scenario D phase 1: independent flavors force draining a podset with its own slack": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("ps0", 1).Obj(),
				*utiltestingapi.MakePodSet("ps1", 4).SetMinimumCount(2).Obj(),
				*utiltestingapi.MakePodSet("ps2", 20).SetMinimumCount(10).Obj(),
			},
			// ps1 tied to rf1 (quota 2), ps2 tied to rf2 (quota 20, never the bottleneck).
			ok: func(counts []int32) bool {
				return counts[1] <= 2
			},
			wantCount: []int32{1, 2, 10},
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
				// counts is reused across calls by Search, so it must be
				// cloned to be safely returned as the winning result.
				out := make([]int32, len(counts))
				copy(out, counts)
				return out, true
			}
			red := NewOrderedPodSetReducer(tc.podSets, fits)
			count, found := red.Search()
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

func TestSearchTotalDeltaOverflow(t *testing.T) {
	podSets := []kueue.PodSet{
		*utiltestingapi.MakePodSet("ps1", 1_500_000_000).SetMinimumCount(1).Obj(),
		*utiltestingapi.MakePodSet("ps2", 1_500_000_000).SetMinimumCount(1).Obj(),
	}

	fits := func(counts []int32) ([]int32, bool) {
		total := int64(counts[0]) + int64(counts[1])
		if total > 1_000_000_000 {
			return nil, false
		}

		out := make([]int32, len(counts))
		copy(out, counts)
		return out, true
	}

	red := NewOrderedPodSetReducer(podSets, fits)

	if want, got := int64(2_999_999_998), red.totalDelta; got != want {
		t.Errorf("Unexpected totalDelta: %d, want %d", got, want)
	}

	count, found := red.Search()
	if !found {
		t.Fatal("Expected a solution")
	}

	wantCount := []int32{999_999_999, 1}
	if diff := cmp.Diff(wantCount, count); diff != "" {
		t.Errorf("Unexpected counts (-want,+got):\n%s", diff)
	}
}
