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

package api

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateMessage(t *testing.T) {
	cases := map[string]struct {
		message string
		limit   int
		want    string
	}{
		"shorter than the limit is unchanged": {
			message: "Insufficient cpu",
			limit:   20,
			want:    "Insufficient cpu",
		},
		"exactly at the limit is unchanged": {
			message: strings.Repeat("a", 10),
			limit:   10,
			want:    strings.Repeat("a", 10),
		},
		"one byte over the limit is truncated with the suffix inside the limit": {
			message: strings.Repeat("a", 11),
			limit:   10,
			want:    strings.Repeat("a", 6) + " ...",
		},
		"multibyte character straddling the boundary is dropped whole": {
			// "é" is 2 bytes. The 6-byte prefix would end after the first byte of the
			// 4th "é", so the cut moves back to the previous character boundary.
			message: strings.Repeat("a", 5) + strings.Repeat("é", 4),
			limit:   10,
			want:    strings.Repeat("a", 5) + " ...",
		},
		"multibyte character ending exactly at the boundary is kept": {
			message: strings.Repeat("a", 4) + strings.Repeat("é", 4),
			limit:   10,
			want:    strings.Repeat("a", 4) + "é" + " ...",
		},
		"limit smaller than the suffix returns only the suffix": {
			message: "ééé",
			limit:   2,
			want:    " ...",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := TruncateMessage(tc.message, tc.limit)
			if got != tc.want {
				t.Errorf("TruncateMessage(%q, %d) = %q, want %q", tc.message, tc.limit, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("result %q is not valid UTF-8", got)
			}
			if len(tc.message) > tc.limit && tc.limit >= len(" ...") && len(got) > tc.limit {
				t.Errorf("result has %d bytes, want at most %d", len(got), tc.limit)
			}
		})
	}
}
