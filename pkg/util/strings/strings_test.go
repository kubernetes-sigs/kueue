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

package strings

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestStringContainsSubstrings(t *testing.T) {
	cases := map[string]struct {
		s          string
		substrings []string
		want       bool
	}{
		"empty string": {
			s:          "",
			substrings: []string{"a", "b"},
			want:       false,
		},
		"empty substrings": {
			s:          "abc",
			substrings: nil,
			want:       true,
		},
		"substrings not found": {
			s:          "abc",
			substrings: []string{"d"},
			want:       false,
		},
		"substrings partially found": {
			s:          "abc",
			substrings: []string{"a", "d"},
			want:       false,
		},
		"substrings found": {
			s:          "abc",
			substrings: []string{"a", "b"},
			want:       true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := StringContainsSubstrings(tc.s, tc.substrings...); got != tc.want {
				t.Errorf("Unexpected result: want: %v, got: %v", tc.want, got)
			}
		})
	}
}

func TestJoinMap(t *testing.T) {
	cases := map[string]struct {
		m           map[string][]int
		keyValueSep string
		valueSep    string
		entrySep    string
		want        string
	}{
		"empty map": {
			m:           map[string][]int{},
			keyValueSep: ":",
			valueSep:    ",",
			entrySep:    ";",
			want:        "",
		},
		"basic test": {
			m:           map[string][]int{"a": {1, 2}, "b": {3}},
			keyValueSep: ":",
			valueSep:    ",",
			entrySep:    "; ",
			want:        "a:1,2; b:3",
		},
		"key present with empty values": {
			m:           map[string][]int{"a": {}, "b": {1, 2}},
			keyValueSep: ":",
			valueSep:    ",",
			entrySep:    "; ",
			want:        "a; b:1,2",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := JoinMap(tc.m, tc.keyValueSep, tc.valueSep, tc.entrySep)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Unexpected result: want: %v, got: %v", tc.want, got)
			}
		})
	}
}
