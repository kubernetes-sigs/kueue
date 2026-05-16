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

package math

import (
	stdmath "math"
	"testing"
)

func TestSaturatingMul(t *testing.T) {
	cases := map[string]struct {
		a    int64
		b    int64
		want int64
	}{
		"zero a":             {a: 0, b: stdmath.MaxInt64, want: 0},
		"zero b":             {a: stdmath.MinInt64, b: 0, want: 0},
		"positive":           {a: 2, b: 3, want: 6},
		"negative":           {a: -2, b: 3, want: -6},
		"both negative":      {a: -2, b: -3, want: 6},
		"max int64":          {a: stdmath.MaxInt64, b: 1, want: stdmath.MaxInt64},
		"min int64":          {a: stdmath.MinInt64, b: 1, want: stdmath.MinInt64},
		"overflow positive":  {a: stdmath.MaxInt64, b: 2, want: stdmath.MaxInt64},
		"overflow negative":  {a: stdmath.MaxInt64, b: -2, want: stdmath.MinInt64},
		"underflow positive": {a: stdmath.MinInt64, b: 2, want: stdmath.MinInt64},
		"underflow negative": {a: stdmath.MinInt64, b: -2, want: stdmath.MaxInt64},
		"min int * -1":       {a: stdmath.MinInt64, b: -1, want: stdmath.MaxInt64},
		"-1 * min int":       {a: -1, b: stdmath.MinInt64, want: stdmath.MaxInt64},
		"large overflow":     {a: 3000000000, b: 4000000000, want: stdmath.MaxInt64},
		"large underflow":    {a: 3000000000, b: -4000000000, want: stdmath.MinInt64},
		"max int * max int":  {a: stdmath.MaxInt64, b: stdmath.MaxInt64, want: stdmath.MaxInt64},
		"min int * min int":  {a: stdmath.MinInt64, b: stdmath.MinInt64, want: stdmath.MaxInt64},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := SaturatingMul(tc.a, tc.b); got != tc.want {
				t.Errorf("SaturatingMul(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
