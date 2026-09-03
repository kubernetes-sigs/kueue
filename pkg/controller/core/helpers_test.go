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

package core

import (
	"math"
	"testing"
)

// This is the conversion the ClusterQueue and Cohort status fields are written
// through, and the API documents that field as ranging from 0 to MaxInt64. A
// share is a float64, so it can arrive as NaN, as an infinity, or finite and
// past that range, which an unguarded int64() is not defined to handle.
func TestWeightedShareStaysInTheReportedRange(t *testing.T) {
	cases := map[string]struct {
		share float64
		want  int64
	}{
		"not a number":                  {share: math.NaN(), want: math.MaxInt64},
		"positive infinity":             {share: math.Inf(1), want: math.MaxInt64},
		"finite but past the range":     {share: 1e19, want: math.MaxInt64},
		"the range boundary as a float": {share: float64(math.MaxInt64), want: math.MaxInt64},
		"the largest float below it":    {share: math.Nextafter(float64(math.MaxInt64), 0), want: 9223372036854774784},
		"a share too small to round up": {share: math.SmallestNonzeroFloat64, want: 1},
		"an ordinary share":             {share: 2.5, want: 3},
		"no share":                      {share: 0, want: 0},
		"a negative share":              {share: -1, want: 0},
		"negative infinity":             {share: math.Inf(-1), want: 0},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := WeightedShare(tc.share); got != tc.want {
				t.Errorf("WeightedShare(%v) = %d, want %d", tc.share, got, tc.want)
			}
		})
	}
}
