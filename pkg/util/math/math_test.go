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

	"k8s.io/apimachinery/pkg/api/resource"
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

func TestSafeValue(t *testing.T) {
	cases := map[string]struct {
		q    resource.Quantity
		want int64
	}{
		"zero":            {q: resource.MustParse("0"), want: 0},
		"small positive":  {q: resource.MustParse("5"), want: 5},
		"negative":        {q: resource.MustParse("-5"), want: -5},
		"max int64":       {q: resource.MustParse("9223372036854775807"), want: stdmath.MaxInt64},
		"above int64":     {q: resource.MustParse("9223372036854775808"), want: stdmath.MaxInt64},
		"far above int64": {q: resource.MustParse("1e19"), want: stdmath.MaxInt64},
		"below min int64": {q: resource.MustParse("-9223372036854775809"), want: stdmath.MinInt64},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := SafeValue(tc.q); got != tc.want {
				t.Errorf("SafeValue(%s) = %d, want %d", tc.q.String(), got, tc.want)
			}
		})
	}
}

func TestSaturatingSub(t *testing.T) {
	cases := map[string]struct {
		a    int64
		b    int64
		want int64
	}{
		"normal subtraction":              {a: 10, b: 3, want: 7},
		"zero":                            {a: 5, b: 5, want: 0},
		"negative result":                 {a: 3, b: 10, want: -7},
		"underflow: MinInt64 - 1":         {a: stdmath.MinInt64, b: 1, want: stdmath.MinInt64},
		"underflow: MinInt64 - large":     {a: stdmath.MinInt64, b: stdmath.MaxInt64, want: stdmath.MinInt64},
		"overflow: MaxInt64 - (-1)":       {a: stdmath.MaxInt64, b: -1, want: stdmath.MaxInt64},
		"overflow: MaxInt64 - MinInt64":   {a: stdmath.MaxInt64, b: stdmath.MinInt64, want: stdmath.MaxInt64},
		"subtract negative: 0 - MinInt64": {a: 0, b: stdmath.MinInt64, want: stdmath.MaxInt64},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := SaturatingSub(tc.a, tc.b); got != tc.want {
				t.Errorf("SaturatingSub(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
