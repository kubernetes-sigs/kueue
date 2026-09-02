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
)

func TestMatchExactSizes(t *testing.T) {
	cases := map[string]struct {
		sizes          []int32
		capacities     []int32
		preferSmallest bool
		want           []int
		wantOK         bool
	}{
		"least free capacity picks the tightest fit": {
			sizes:          []int32{1, 3, 4},
			capacities:     []int32{1, 2, 3, 7, 10},
			preferSmallest: true,
			want:           []int{0, 2, 3},
			wantOK:         true,
		},
		"best fit picks the roomiest": {
			sizes:      []int32{1, 3, 4},
			capacities: []int32{1, 2, 3, 7, 10},
			want:       []int{2, 3, 4},
			wantOK:     true,
		},
		"reordering the request only moves the entries, not the domains chosen": {
			sizes:          []int32{4, 3, 1},
			capacities:     []int32{1, 2, 3, 7, 10},
			preferSmallest: true,
			want:           []int{3, 2, 0},
			wantOK:         true,
		},
		"aggregate capacity is sufficient but the shape does not fit": {
			sizes:          []int32{1, 3, 4},
			capacities:     []int32{2, 2, 4},
			preferSmallest: true,
			wantOK:         false,
		},
		"exact fit consumes every domain": {
			sizes:          []int32{1, 3, 4},
			capacities:     []int32{1, 3, 4},
			preferSmallest: true,
			want:           []int{0, 1, 2},
			wantOK:         true,
		},
		"duplicate sizes still need distinct domains": {
			sizes:          []int32{2, 2, 4},
			capacities:     []int32{2, 2, 4},
			preferSmallest: true,
			want:           []int{0, 1, 2},
			wantOK:         true,
		},
		"duplicate sizes fail when a domain would have to be shared": {
			sizes:          []int32{2, 2},
			capacities:     []int32{4},
			preferSmallest: true,
			wantOK:         false,
		},
		"fewer domains than entries": {
			sizes:          []int32{1, 1, 1},
			capacities:     []int32{5, 5},
			preferSmallest: true,
			wantOK:         false,
		},
		"largest first avoids stranding a large entry": {
			// Smallest-first would put 2 into the capacity-4 domain and leave
			// 4 with nowhere to go.
			sizes:          []int32{2, 4},
			capacities:     []int32{4, 4},
			preferSmallest: true,
			want:           []int{1, 0},
			wantOK:         true,
		},
		"equal capacities tie-break towards the earlier domain": {
			sizes:          []int32{1, 1},
			capacities:     []int32{5, 5, 5},
			preferSmallest: true,
			want:           []int{0, 1},
			wantOK:         true,
		},
		"single entry": {
			sizes:          []int32{8},
			capacities:     []int32{3, 9},
			preferSmallest: true,
			want:           []int{1},
			wantOK:         true,
		},
		"empty request": {
			sizes:      []int32{},
			capacities: []int32{1},
			wantOK:     false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := matchExactSizes(tc.sizes, tc.capacities, tc.preferSmallest)
			if ok != tc.wantOK {
				t.Fatalf("matchExactSizes() ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("unexpected assignment (-want +got):\n%s", diff)
			}
			assertDistinct(t, got)
			for entry, domain := range got {
				if tc.capacities[domain] < tc.sizes[entry] {
					t.Errorf("entry %d (size %d) placed in domain %d with capacity %d",
						entry, tc.sizes[entry], domain, tc.capacities[domain])
				}
			}
		})
	}
}

// TestMatchExactSizesFeasibilityIsPolicyIndependent guards the reason the
// matcher runs two passes: whether a request fits must not depend on which
// placement mode is active.
func TestMatchExactSizesFeasibilityIsPolicyIndependent(t *testing.T) {
	cases := []struct {
		sizes      []int32
		capacities []int32
	}{
		{[]int32{1, 3, 4}, []int32{1, 2, 3, 7, 10}},
		{[]int32{1, 3, 4}, []int32{2, 2, 4}},
		{[]int32{2, 2, 4}, []int32{2, 2, 4}},
		{[]int32{5}, []int32{4}},
		{[]int32{1, 1, 1}, []int32{1, 1}},
		{[]int32{3, 3, 3}, []int32{3, 3, 9}},
	}
	for _, tc := range cases {
		_, smallest := matchExactSizes(tc.sizes, tc.capacities, true)
		_, roomiest := matchExactSizes(tc.sizes, tc.capacities, false)
		if smallest != roomiest {
			t.Errorf("sizes=%v capacities=%v: feasibility differs by mode (smallest=%v roomiest=%v)",
				tc.sizes, tc.capacities, smallest, roomiest)
		}
	}
}

func assertDistinct(t *testing.T, assignment []int) {
	t.Helper()
	seen := make(map[int]int, len(assignment))
	for entry, domain := range assignment {
		if prev, dup := seen[domain]; dup {
			t.Errorf("domain %d assigned to both entry %d and entry %d", domain, prev, entry)
		}
		seen[domain] = entry
	}
}
