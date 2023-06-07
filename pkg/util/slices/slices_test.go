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
