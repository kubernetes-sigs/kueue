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
	"time"
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
			exp: 1,
		},
		"a false, b true": {
			a:   false,
			b:   true,
			exp: -1,
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

func TestEarliestTime(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(10 * time.Second)
	t1Copy := t1

	cases := map[string]struct {
		a, b *time.Time
		want *time.Time
	}{
		"both nil": {
			want: nil,
		},
		"first nil": {
			b:    &t1,
			want: &t1,
		},
		"second nil": {
			a:    &t1,
			want: &t1,
		},
		"first earlier": {
			a:    &t1,
			b:    &t2,
			want: &t1,
		},
		"second earlier": {
			a:    &t2,
			b:    &t1,
			want: &t1,
		},
		"equal timestamps keeps first": {
			a:    &t1,
			b:    &t1Copy,
			want: &t1,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := EarliestTime(tc.a, tc.b); got != tc.want {
				t.Errorf("EarliestTime(%v, %v) = %p (%v), want %p (%v)", tc.a, tc.b, got, got, tc.want, tc.want)
			}
		})
	}
}
