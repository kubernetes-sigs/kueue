/*
Copyright 2023 The Kubernetes Authors.

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

package slices

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestToRefMap(t *testing.T) {
	data := [3]int{0xa, 0xb, 0xc}
	cases := map[string]struct {
		slice   []int
		wantMap map[int]*int
	}{
		"preserve nil": {
			slice:   nil,
			wantMap: nil,
		},
		"preserve empty": {
			slice:   []int{},
			wantMap: map[int]*int{},
		},
		"slice": {
			slice: data[:],
			wantMap: map[int]*int{
				0xd: &data[0],
				0xe: &data[1],
				0xf: &data[2],
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			result := ToRefMap(tc.slice, func(p *int) int { return *p + 3 })
			if diff := cmp.Diff(tc.wantMap, result); diff != "" {
				t.Errorf("Unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestToCmpNoOrder(t *testing.T) {
	cases := map[string]struct {
		sliceA []int
		sliceB []int
		want   bool
	}{
		"equal sets": {
			sliceA: []int{1, 2, 3},
			sliceB: []int{3, 2, 1},
			want:   true,
		},
		"equal multisets": {
			sliceA: []int{1, 1, 2},
			sliceB: []int{1, 2, 1},
			want:   true,
		},
		"unequal multisets": {
			sliceA: []int{1, 2, 2},
			sliceB: []int{1, 2, 1},
			want:   false,
		},
		"unequal sets": {
			sliceA: []int{1, 2},
			sliceB: []int{1, 1},
			want:   false,
		},
		"slice A is longer": {
			sliceA: []int{1, 2, 3},
			sliceB: []int{1, 2},
			want:   false,
		},
		"slice B is longer": {
			sliceA: []int{1, 2},
			sliceB: []int{1, 2, 3},
			want:   false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res := CmpNoOrder[int](tc.sliceA, tc.sliceB)
			if res != tc.want {
				t.Errorf("Unexpected result: want: %v, got: %v", tc.want, res)
			}
		})
	}
}

func TestPick(t *testing.T) {
	cases := map[string]struct {
		testSlice []int
		want      []int
	}{
		"nil input": {
			testSlice: nil,
			want:      nil,
		},
		"empty input": {
			testSlice: []int{},
			want:      nil,
		},
		"no match": {
			testSlice: []int{1, 3, 5, 7, 9},
			want:      nil,
		},
		"match": {
			testSlice: []int{1, 2, 3, 4, 5, 6, 7, 8, 9},
			want:      []int{2, 4, 6, 8},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			result := Pick(tc.testSlice, func(i *int) bool { return (*i)%2 == 0 })
			if diff := cmp.Diff(tc.want, result); diff != "" {
				t.Errorf("Unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestOrderStringSlices(t *testing.T) {
	// Define test cases as a map
	testCases := map[string]struct {
		a        []string
		b        []string
		expected int
	}{
		"A smaller": {
			a:        []string{"a", "b", "c"},
			b:        []string{"a", "b", "d"},
			expected: -1,
		},
		"A greater": {
			a:        []string{"a", "c", "b"},
			b:        []string{"a", "b", "d"},
			expected: 1,
		},
		"Equal": {
			a:        []string{"a", "b", "c"},
			b:        []string{"a", "b", "c"},
			expected: 0,
		},
		"A shorter": {
			a:        []string{"a", "b"},
			b:        []string{"a", "b", "c"},
			expected: -1,
		},
		"A longer": {
			a:        []string{"a", "b", "c"},
			b:        []string{"a", "b"},
			expected: 1,
		},
		"Empty A": {
			a:        []string{},
			b:        []string{"a"},
			expected: -1,
		},
		"Empty B": {
			a:        []string{"a"},
			b:        []string{},
			expected: 1,
		},
		"Both Empty": {
			a:        []string{},
			b:        []string{},
			expected: 0,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotResult := OrderStringSlices(tc.a, tc.b)

			if diff := cmp.Diff(tc.expected, gotResult); diff != "" {
				t.Errorf("unexpected result (-want,+got): %s", diff)
			}
		})
	}
}
