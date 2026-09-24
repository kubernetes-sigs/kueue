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
)

func TestPlaceChunks(t *testing.T) {
	cases := map[string]struct {
		sizes      []int32
		capacities []int32

		wantAssigned []int32
		wantUnplaced int32
		wantOK       bool
	}{
		"each chunk gets its own domain when capacities match exactly": {
			sizes:        []int32{1, 3, 4},
			capacities:   []int32{1, 3, 4},
			wantAssigned: []int32{1, 3, 4},
			wantOK:       true,
		},
		"chunks may share a domain": {
			sizes:        []int32{4, 3, 1},
			capacities:   []int32{8},
			wantAssigned: []int32{8},
			wantOK:       true,
		},
		"a domain holding two chunks records the combined count": {
			sizes:        []int32{3, 3, 2},
			capacities:   []int32{6, 2},
			wantAssigned: []int32{6, 2},
			wantOK:       true,
		},
		"largest chunk is placed first so a small chunk cannot steal the only large domain": {
			// List order puts the 1 first; walking in list order would drop it
			// into the 4-domain and leave the 4 with nowhere to go.
			sizes:        []int32{1, 4},
			capacities:   []int32{4, 1},
			wantAssigned: []int32{4, 1},
			wantOK:       true,
		},
		"duplicate sizes describe separate chunks": {
			sizes:        []int32{2, 2},
			capacities:   []int32{2, 2},
			wantAssigned: []int32{2, 2},
			wantOK:       true,
		},
		"a single chunk equal to the count is accepted": {
			sizes:        []int32{8},
			capacities:   []int32{8, 8},
			wantAssigned: []int32{8, 0},
			wantOK:       true,
		},
		"spare capacity is left untouched": {
			sizes:        []int32{2},
			capacities:   []int32{5},
			wantAssigned: []int32{2},
			wantOK:       true,
		},
		"reports the chunk that did not fit": {
			// Total free capacity is 8, which is enough in aggregate, but no
			// single domain has room for a chunk of 4.
			sizes:        []int32{4, 4},
			capacities:   []int32{3, 3, 2},
			wantUnplaced: 4,
		},
		"fails when capacity runs out after sharing": {
			sizes:        []int32{3, 3},
			capacities:   []int32{5},
			wantUnplaced: 3,
		},
		"no domains at all": {
			sizes:        []int32{1},
			capacities:   nil,
			wantUnplaced: 1,
		},
		"empty chunk list is not a valid request": {
			sizes:      nil,
			capacities: []int32{4},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assigned, unplaced, ok := placeChunks(tc.sizes, tc.capacities)
			if ok != tc.wantOK {
				t.Errorf("placeChunks ok = %v, want %v", ok, tc.wantOK)
			}
			if unplaced != tc.wantUnplaced {
				t.Errorf("placeChunks unplaced = %d, want %d", unplaced, tc.wantUnplaced)
			}
			if diff := cmp.Diff(tc.wantAssigned, assigned); diff != "" {
				t.Errorf("placeChunks assigned (-want,+got):\n%s", diff)
			}
		})
	}
}

// TestPlaceChunksDoesNotMutateCapacities guards the snapshot: the caller reuses
// the capacity slice across candidate scopes, so a rejected scope must not
// leave reduced capacities behind for the next one.
func TestPlaceChunksDoesNotMutateCapacities(t *testing.T) {
	capacities := []int32{4, 3}
	want := []int32{4, 3}

	if _, _, ok := placeChunks([]int32{4, 4}, capacities); ok {
		t.Fatalf("placeChunks succeeded, want failure")
	}
	if diff := cmp.Diff(want, capacities); diff != "" {
		t.Errorf("capacities were mutated (-want,+got):\n%s", diff)
	}

	if _, _, ok := placeChunks([]int32{4, 3}, capacities); !ok {
		t.Fatalf("placeChunks failed, want success")
	}
	if diff := cmp.Diff(want, capacities); diff != "" {
		t.Errorf("capacities were mutated (-want,+got):\n%s", diff)
	}
}

// TestPlaceChunksFollowsDomainOrder checks that the caller's domain order picks
// the winner, since that order is how the active TAS placement mode -- BestFit
// or LeastFreeCapacity -- influences chunk placement.
func TestPlaceChunksFollowsDomainOrder(t *testing.T) {
	sizes := []int32{2}

	mostFreeFirst, _, ok := placeChunks(sizes, []int32{8, 2})
	if !ok {
		t.Fatalf("placeChunks failed for most-free-first order")
	}
	if diff := cmp.Diff([]int32{2, 0}, mostFreeFirst); diff != "" {
		t.Errorf("most-free-first (-want,+got):\n%s", diff)
	}

	leastFreeFirst, _, ok := placeChunks(sizes, []int32{2, 8})
	if !ok {
		t.Fatalf("placeChunks failed for least-free-first order")
	}
	if diff := cmp.Diff([]int32{2, 0}, leastFreeFirst); diff != "" {
		t.Errorf("least-free-first (-want,+got):\n%s", diff)
	}
}

func TestChunksLargestFirst(t *testing.T) {
	cases := map[string]struct {
		sizes []int32
		want  []int32
	}{
		"descending":            {sizes: []int32{1, 3, 4}, want: []int32{4, 3, 1}},
		"already sorted":        {sizes: []int32{4, 3, 1}, want: []int32{4, 3, 1}},
		"duplicates are kept":   {sizes: []int32{2, 4, 2}, want: []int32{4, 2, 2}},
		"single entry":          {sizes: []int32{5}, want: []int32{5}},
		"empty stays empty":     {sizes: []int32{}, want: []int32{}},
		"all values equal":      {sizes: []int32{3, 3, 3}, want: []int32{3, 3, 3}},
		"does not alias source": {sizes: []int32{1, 2}, want: []int32{2, 1}},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			source := slices.Clone(tc.sizes)
			got := chunksLargestFirst(tc.sizes)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("chunksLargestFirst (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(source, tc.sizes); diff != "" {
				t.Errorf("chunksLargestFirst mutated its input (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestSliceSizesNotFitReason(t *testing.T) {
	got := sliceSizesNotFitReason([]int32{4, 4}, []int32{3, 3, 2}, 4, nil)
	want := "topology slice sizes do not fit: chunk of 4 pods could not be placed, " +
		"largest free domain holds 3 pods (sizes [4 4], free capacity [3 3 2])"
	if got != want {
		t.Errorf("sliceSizesNotFitReason =\n%s\nwant\n%s", got, want)
	}
}

func TestRepeatChunksToCover(t *testing.T) {
	cases := map[string]struct {
		sizes []int32
		pods  int32

		want       []int32
		wantReason string
	}{
		"one repetition leaves the list alone": {
			sizes: []int32{1, 3, 4},
			pods:  8,
			want:  []int32{1, 3, 4},
		},
		"two repetitions when the enclosing domain holds two chunks": {
			sizes: []int32{1, 3, 4},
			pods:  16,
			want:  []int32{1, 3, 4, 1, 3, 4},
		},
		"a single-entry list repeats too": {
			sizes: []int32{2},
			pods:  6,
			want:  []int32{2, 2, 2},
		},
		"rejected when the sum does not divide the pod count": {
			sizes:      []int32{1, 3, 4},
			pods:       12,
			wantReason: "topology slice sizes [1 3 4] sum to 8, which does not divide the 12 pods assigned to the enclosing domain",
		},
		"rejected when the list is empty": {
			sizes:      nil,
			pods:       8,
			wantReason: "topology slice sizes [] must be positive",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, reason := repeatChunksToCover(tc.sizes, tc.pods)
			if reason != tc.wantReason {
				t.Errorf("repeatChunksToCover reason = %q, want %q", reason, tc.wantReason)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("repeatChunksToCover (-want,+got):\n%s", diff)
			}
		})
	}
}
