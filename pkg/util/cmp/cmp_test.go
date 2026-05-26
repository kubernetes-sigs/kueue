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

package cmp

import (
	"testing"
)

func TestCompareBool(t *testing.T) {
	cases := map[string]struct {
		a   bool
		b   bool
		exp int
	}{
		"both true": {
			a:   true,
			b:   true,
			exp: 0,
		},
		"both false": {
			a:   false,
			b:   false,
			exp: 0,
		},
		"a true, b false": {
			a:   true,
			b:   false,
			exp: -1,
		},
		"a false, b true": {
			a:   false,
			b:   true,
			exp: 1,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			result := CompareBool(tc.a, tc.b)
			if result != tc.exp {
				t.Errorf("CompareBool(%t, %t) = %d; want %d", tc.a, tc.b, result, tc.exp)
			}
		})
	}
}
