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

package admissiongates

import (
	"testing"

	"github.com/google/go-cmp/cmp"
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
		"whitespace only": {
			input: "   ",
			want:  []string{},
		},
		"single gate": {
			input: "example.com/controller",
			want:  []string{"example.com/controller"},
		},
		"single gate with trailing space": {
			input: "example.com/gate ",
			want:  []string{"example.com/gate"},
		},
		"single gate with leading space": {
			input: " example.com/gate",
			want:  []string{"example.com/gate"},
		},
		"single gate with leading and trailing spaces": {
			input: "  example.com/gate  ",
			want:  []string{"example.com/gate"},
		},
		"multiple gates": {
			input: "example.com/a,not.example.com/b",
			want:  []string{"example.com/a", "not.example.com/b"},
		},
		"multiple gates with space before comma": {
			input: "example.com/gate ,example.com/gate2",
			want:  []string{"example.com/gate", "example.com/gate2"},
		},
		"multiple gates with space after comma": {
			input: "example.com/gate, example.com/gate2",
			want:  []string{"example.com/gate", "example.com/gate2"},
		},
		"multiple gates with spaces around commas": {
			input: "example.com/gate , example.com/gate2",
			want:  []string{"example.com/gate", "example.com/gate2"},
		},
		"multiple gates with various spacing": {
			input: " example.com/a , not.example.com/b, another.com/c ",
			want:  []string{"example.com/a", "not.example.com/b", "another.com/c"},
		},
		"three gates": {
			input: "first.com/gate,second.com/gate,third.com/gate",
			want:  []string{"first.com/gate", "second.com/gate", "third.com/gate"},
		},
		"gates with invalid characters are preserved": {
			input: "this is an invalid value",
			want:  []string{"this is an invalid value"},
		},
		"duplicate gates are preserved": {
			input: "duplicates.are/invalid,duplicates.are/invalid",
			want:  []string{"duplicates.are/invalid", "duplicates.are/invalid"},
		},
		"gate with space in path component": {
			input: "example.com/gate name",
			want:  []string{"example.com/gate name"},
		},
		"gate with space in domain component": {
			input: "example .com/gate",
			want:  []string{"example .com/gate"},
		},
		"multiple gates with one containing space": {
			input: "valid.com/gate,invalid gate.com/controller",
			want:  []string{"valid.com/gate", "invalid gate.com/controller"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := Parse(tc.input)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Unexpected result (-want,+got):\n%s", diff)
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
		"single gate": {
			input: []string{"example.com/controller"},
			want:  "example.com/controller",
		},
		"two gates": {
			input: []string{"example.com/a", "not.example.com/b"},
			want:  "example.com/a,not.example.com/b",
		},
		"three gates": {
			input: []string{"first.com/gate", "second.com/gate", "third.com/gate"},
			want:  "first.com/gate,second.com/gate,third.com/gate",
		},
		"gates with spaces are preserved": {
			input: []string{"example.com/gate name", "another .com/gate"},
			want:  "example.com/gate name,another .com/gate",
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

func TestParseSerializeRoundTrip(t *testing.T) {
	cases := map[string]struct {
		input string
		want  string
	}{
		"empty string": {
			input: "",
			want:  "",
		},
		"single gate": {
			input: "example.com/controller",
			want:  "example.com/controller",
		},
		"multiple gates": {
			input: "example.com/a,not.example.com/b",
			want:  "example.com/a,not.example.com/b",
		},
		"gates with spaces are stripped": {
			input: " example.com/gate , not.example.com/gate2 ",
			want:  "example.com/gate,not.example.com/gate2",
		},
		"whitespace only becomes empty": {
			input: "   ",
			want:  "",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			parsed := Parse(tc.input)
			got := Serialize(parsed)
			if got != tc.want {
				t.Errorf("Unexpected result: want: %q, got: %q", tc.want, got)
			}
		})
	}
}
