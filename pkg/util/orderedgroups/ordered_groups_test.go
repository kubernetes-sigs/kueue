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

package orderedgroups

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestInsert(t *testing.T) {
	cases := map[string]struct {
		initialGroups *OrderedGroups[string, int]
		newKey        string
		newVal        int
		wantGroups    *OrderedGroups[string, int]
	}{
		"tracks order of a newly inserted group": {
			initialGroups: &OrderedGroups[string, int]{
				groups:         make(map[string][]int),
				insertionOrder: make([]string, 0),
			},
			newKey: "newgroup",
			newVal: 10,
			wantGroups: &OrderedGroups[string, int]{
				groups:         map[string][]int{"newgroup": {10}},
				insertionOrder: []string{"newgroup"},
			},
		},
		"tracks a newly inserted group": {
			initialGroups: &OrderedGroups[string, int]{
				groups:         make(map[string][]int),
				insertionOrder: make([]string, 0),
			},
			newKey: "newgroup",
			newVal: 10,
			wantGroups: &OrderedGroups[string, int]{
				groups:         map[string][]int{"newgroup": {10}},
				insertionOrder: []string{"newgroup"},
			},
		},
		"appends to an existing group": {
			initialGroups: &OrderedGroups[string, int]{
				groups:         map[string][]int{"oldgroup": {10}},
				insertionOrder: []string{"oldgroup"},
			},
			newKey: "oldgroup",
			newVal: 20,
			wantGroups: &OrderedGroups[string, int]{
				groups:         map[string][]int{"oldgroup": {10, 20}},
				insertionOrder: []string{"oldgroup"},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			groups := tc.initialGroups
			groups.Insert(tc.newKey, tc.newVal)
			if diff := cmp.Diff(tc.wantGroups, groups, cmp.AllowUnexported(OrderedGroups[string, int]{})); diff != "" {
				t.Errorf("Processed result (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestInOrder(t *testing.T) {
	cases := map[string]struct {
		groups    *OrderedGroups[string, int]
		wantOrder [][]int
	}{
		"handles no groups": {
			groups: &OrderedGroups[string, int]{
				groups:         make(map[string][]int),
				insertionOrder: make([]string, 0),
			},
			wantOrder: make([][]int, 0),
		},
		"returns groups in order of insertion": {
			groups: &OrderedGroups[string, int]{
				groups:         map[string][]int{"second": {2, 2}, "first": {1, 1}, "third": {3, 3}},
				insertionOrder: []string{"first", "second", "third"},
			},
			wantOrder: [][]int{{1, 1}, {2, 2}, {3, 3}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ordering := make([][]int, 0, len(tc.groups.insertionOrder))
			for _, group := range tc.groups.InOrder {
				ordering = append(ordering, group)
			}
			if diff := cmp.Diff(tc.wantOrder, ordering, cmp.AllowUnexported(OrderedGroups[string, int]{})); diff != "" {
				t.Errorf("Processed result (-want,+got):\n%s", diff)
			}
		})
	}
}
