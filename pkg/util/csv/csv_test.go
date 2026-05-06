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

package csv

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	cases := map[string]struct {
		input string
		want  []string
	}{
		"empty string": {
			input: "",
			want:  []string{},
		},
		"spaces only": {
			input: "   ",
			want:  []string{},
		},
		"single element": {
			input: "foo",
			want:  []string{"foo"},
		},
		"multiple elements": {
			input: "foo,bar,baz",
			want:  []string{"foo", "bar", "baz"},
		},
		"elements with spaces are trimmed": {
			input: " foo , bar , baz ",
			want:  []string{"foo", "bar", "baz"},
		},
		"empty elements are preserved but trimmed": {
			input: "foo,,bar",
			want:  []string{"foo", "", "bar"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := Parse(tc.input)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Unexpected result: want: %v, got: %v", tc.want, got)
			}
		})
	}
}

func TestSerialize(t *testing.T) {
	cases := map[string]struct {
		input []string
		want  string
	}{
		"nil slice": {
			input: nil,
			want:  "",
		},
		"empty slice": {
			input: []string{},
			want:  "",
		},
		"single element": {
			input: []string{"foo"},
			want:  "foo",
		},
		"multiple elements": {
			input: []string{"foo", "bar", "baz"},
			want:  "foo,bar,baz",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := Serialize(tc.input)
			if got != tc.want {
				t.Errorf("Unexpected result: want: %q, got: %q", tc.want, got)
			}
		})
	}
}
