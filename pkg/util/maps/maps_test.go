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

package maps

import (
	"maps"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestCopy(t *testing.T) {
	cases := map[string]struct {
		dst  *map[string]string
		src  map[string]string
		want *map[string]string
	}{
		"nil dst pointer": {
			dst: nil,
			src: map[string]string{
				"key1": "value1",
			},
			want: nil,
		},
		"nil dst, nil src": {
			dst:  new(map[string]string),
			src:  nil,
			want: new(map[string]string),
		},
		"nil dst, non-empty src": {
			dst: new(map[string]string),
			src: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
			want: &map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
		},
		"empty dst, non-empty src": {
			dst: &map[string]string{},
			src: map[string]string{
				"key1": "value1",
			},
			want: &map[string]string{
				"key1": "value1",
			},
		},
		"non-empty dst, empty src": {
			dst: &map[string]string{
				"existing": "value",
			},
			src: map[string]string{},
			want: &map[string]string{
				"existing": "value",
			},
		},
		"non-empty dst, non-empty src with new keys": {
			dst: &map[string]string{
				"existing": "value",
			},
			src: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
			want: &map[string]string{
				"existing": "value",
				"key1":     "value1",
				"key2":     "value2",
			},
		},
		"non-empty dst, non-empty src with overlapping keys": {
			dst: &map[string]string{
				"existing": "old_value",
				"key1":     "old_key1",
			},
			src: map[string]string{
				"existing": "new_value",
				"key2":     "value2",
			},
			want: &map[string]string{
				"existing": "new_value",
				"key1":     "old_key1",
				"key2":     "value2",
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			Copy(tc.dst, tc.src)
			if diff := cmp.Diff(tc.dst, tc.want,
				cmpopts.SortMaps(func(x, y string) bool { return x < y }),
			); len(diff) != 0 {
				t.Errorf("Unexpected copy result (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestToRefMap(t *testing.T) {
	cases := map[string]struct {
		orig       map[int]int
		updatesMap map[int]int
	}{
		"preserve nil": {
			orig: nil,
		},
		"preserve empty": {
			orig: map[int]int{},
		},
		"slice": {
			orig: map[int]int{
				1: 0xa,
				2: 0xb,
				3: 0xc,
			},
			updatesMap: map[int]int{
				1: 0xd,
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			result := maps.Clone(tc.orig)
			if diff := cmp.Diff(tc.orig, result); diff != "" {
				t.Errorf("Unexpected result (-want,+got):\n%s", diff)
			}

			if len(tc.updatesMap) > 0 {
				maps.Copy(result, tc.updatesMap)
				if diff := cmp.Diff(tc.orig, result); diff == "" {
					t.Errorf("changing the result should not alter the original")
				}
			}
		})
	}
}

func TestContains(t *testing.T) {
	cases := map[string]struct {
		a          map[string]int
		b          map[string]int
		wantResult bool
	}{
		"nil a": {
			a: nil,
			b: map[string]int{
				"v1": 1,
			},
			wantResult: false,
		},
		"nil b": {
			a: map[string]int{
				"v1": 1,
			},
			b:          nil,
			wantResult: true,
		},
		"extra in b": {
			a: map[string]int{
				"v1": 1,
			},
			b: map[string]int{
				"v1": 1,
				"v2": 2,
			},
			wantResult: false,
		},
		"extra in a": {
			a: map[string]int{
				"v1": 1,
				"v2": 2,
			},
			b: map[string]int{
				"v1": 1,
			},
			wantResult: true,
		},
		"mismatch": {
			a: map[string]int{
				"v1": 1,
				"v2": 3,
			},
			b: map[string]int{
				"v1": 1,
				"v2": 2,
			},
			wantResult: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := Contains(tc.a, tc.b)
			if got != tc.wantResult {
				t.Errorf("Unexpected result, expecting %v", tc.wantResult)
			}
		})
	}
}

func TestFilterKeys(t *testing.T) {
	cases := map[string]struct {
		m    map[string]int
		k    []string
		want map[string]int
	}{
		"nil m": {
			m: nil,
			k: []string{
				"k1",
			},
			want: nil,
		},
		"nil k": {
			m: map[string]int{
				"v1": 1,
			},
			k:    nil,
			want: nil,
		},
		"empty k": {
			m: map[string]int{
				"v1": 1,
				"v2": 2,
				"v3": 3,
			},
			k:    []string{},
			want: nil,
		},
		"empty m": {
			m:    map[string]int{},
			k:    []string{"k1"},
			want: map[string]int{},
		},
		"filter one": {
			m: map[string]int{
				"v1": 1,
				"v2": 2,
				"v3": 3,
			},
			k: []string{"v1", "v3"},
			want: map[string]int{
				"v1": 1,
				"v3": 3,
			},
		},
		"filter two": {
			m: map[string]int{
				"v1": 1,
				"v2": 2,
				"v3": 3,
			},
			k: []string{"v1"},
			want: map[string]int{
				"v1": 1,
			},
		},
		"filter all": {
			m: map[string]int{
				"v1": 1,
				"v2": 2,
				"v3": 3,
			},
			k:    []string{"v4"},
			want: map[string]int{},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := FilterKeys(tc.m, tc.k)
			if diff := cmp.Diff(got, tc.want); diff != "" {
				t.Errorf("Unexpected result, expecting %v", tc.want)
			}
		})
	}
}
