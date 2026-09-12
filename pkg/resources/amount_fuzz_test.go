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

package resources

import (
	"math"
	"math/big"
	"testing"
)

// pairs covers the boundaries the int64 fast paths turn on, so the seed corpus
// exercises them before the fuzzer starts.
var pairs = [][2]int64{
	{0, 0}, {1, 1}, {1, -1}, {-1, 1}, {7, -7},
	{math.MaxInt64, 1}, {math.MaxInt64, -1}, {math.MinInt64, -1}, {math.MinInt64, 1},
	{math.MaxInt64, math.MaxInt64}, {math.MinInt64, math.MinInt64},
	{math.MaxInt64, math.MinInt64}, {math.MinInt64, math.MaxInt64},
	{1 << 53, 1}, {1<<53 + 1, 1 << 53}, {maxExactFloat64Int / 1000, maxExactFloat64Int},
	{maxExactFloat64Int/1000 + 1, maxExactFloat64Int}, {1, math.MaxInt64},
}

// Amount answers what big.Int answers, including where the int64 fast paths
// hand over to the exact ones.
func FuzzAmountAgainstBigInt(f *testing.F) {
	for _, seed := range pairs {
		f.Add(seed[0], seed[1])
	}

	f.Fuzz(func(t *testing.T, x, y int64) {
		a, b := NewAmount(x), NewAmount(y)
		bx, by := big.NewInt(x), big.NewInt(y)
		sum := new(big.Int).Add(bx, by)

		if got, want := a.Add(b).String(), sum.String(); got != want {
			t.Errorf("Add(%d, %d) = %s, want %s", x, y, got, want)
		}
		if got, want := a.Sub(b).String(), new(big.Int).Sub(bx, by).String(); got != want {
			t.Errorf("Sub(%d, %d) = %s, want %s", x, y, got, want)
		}
		// Taking back what was added leaves the value where it started, which
		// is the property the ledger rests on.
		if got := a.Add(b).Sub(b); !got.Equal(a) {
			t.Errorf("(%d+%d)-%d = %s, want %d", x, y, y, got, x)
		}
		if got := a.Sub(b).Add(b); !got.Equal(a) {
			t.Errorf("(%d-%d)+%d = %s, want %d", x, y, y, got, x)
		}

		if got, want := a.Cmp(b), bx.Cmp(by); got != want {
			t.Errorf("Cmp(%d, %d) = %d, want %d", x, y, got, want)
		}
		if a.Cmp(b) != -b.Cmp(a) {
			t.Errorf("Cmp(%d, %d) = %d but Cmp(%d, %d) = %d", x, y, a.Cmp(b), y, x, b.Cmp(a))
		}
		if a.Equal(b) != (a.Cmp(b) == 0) {
			t.Errorf("Equal = %v while Cmp = %d", a.Equal(b), a.Cmp(b))
		}
		if got, want := a.Sign(), bx.Sign(); got != want {
			t.Errorf("Sign(%d) = %d, want %d", x, got, want)
		}

		// Large against small is the pair Cmp answers from the sign alone.
		large := a.Add(NewAmount(math.MaxInt64)).Add(NewAmount(math.MaxInt64))
		blarge := new(big.Int).Add(bx, new(big.Int).Mul(big.NewInt(math.MaxInt64), big.NewInt(2)))
		if got, want := large.Cmp(b), blarge.Cmp(by); got != want {
			t.Errorf("Cmp(%s, %d) = %d, want %d", blarge, y, got, want)
		}
		if got, want := b.Cmp(large), by.Cmp(blarge); got != want {
			t.Errorf("Cmp(%d, %s) = %d, want %d", y, blarge, got, want)
		}

		// A value is held in an int64 exactly when it fits one.
		v, ok := a.Add(b).asInt64()
		if want := sum.IsInt64(); ok != want {
			t.Errorf("asInt64() ok = %v for %s, want %v", ok, sum, want)
		} else if ok && v != sum.Int64() {
			t.Errorf("asInt64() = %d, want %d", v, sum.Int64())
		}
	})
}

// The int64 division PerThousandOf takes for small operands has to answer what
// the exact ratio answers, or the fast path is a second implementation.
func FuzzPerThousandOfMatchesTheExactRatio(f *testing.F) {
	for _, seed := range pairs {
		f.Add(seed[0], seed[1])
	}

	f.Fuzz(func(t *testing.T, x, y int64) {
		if y == 0 {
			return
		}
		r := new(big.Rat).SetFrac(big.NewInt(x), big.NewInt(y))
		r.Mul(r, big.NewRat(1000, 1))
		want, _ := r.Float64()
		if want == 0 && r.Sign() > 0 {
			want = math.SmallestNonzeroFloat64
		}
		if got := NewAmount(x).PerThousandOf(NewAmount(y)); got != want {
			t.Errorf("PerThousandOf(%d, %d) = %v, want %v", x, y, got, want)
		}
	})
}
