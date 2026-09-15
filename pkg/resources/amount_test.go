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
	"strings"
	"testing"

	"gopkg.in/inf.v0"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func bigAmount(t *testing.T, s string) Amount {
	t.Helper()
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("SetString(%q) failed", s)
	}
	return fromBig(v)
}

func TestAmountArithmetic(t *testing.T) {
	cases := map[string]struct {
		got  Amount
		want string
	}{
		"the zero value is zero":                      {got: Amount{}, want: "0"},
		"MaxInt64 is an ordinary amount":              {got: NewAmount(math.MaxInt64), want: "9223372036854775807"},
		"a sum past int64 is exact":                   {got: NewAmount(math.MaxInt64).AddInt64(7), want: "9223372036854775814"},
		"a sum past int64 minus the first operand":    {got: NewAmount(math.MaxInt64).AddInt64(7).SubInt64(math.MaxInt64), want: "7"},
		"a sum past int64 minus the second operand":   {got: NewAmount(math.MaxInt64).AddInt64(7).SubInt64(7), want: "9223372036854775807"},
		"a difference past int64 is exact":            {got: NewAmount(math.MinInt64).SubInt64(7), want: "-9223372036854775815"},
		"a difference past int64 plus the subtrahend": {got: NewAmount(math.MinInt64).SubInt64(7).AddInt64(7), want: "-9223372036854775808"},
		"two large amounts add":                       {got: bigAmount(t, "9223372036854775814").AddInt64(1), want: "9223372036854775815"},
		"a large amount minus a large one":            {got: bigAmount(t, "9223372036854775814").Sub(bigAmount(t, "9223372036854775814")), want: "0"},
		"MinInt64 subtracted from MinInt64":           {got: NewAmount(math.MinInt64).SubInt64(math.MinInt64), want: "0"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.got.String(); got != tc.want {
				t.Errorf("= %s, want %s", got, tc.want)
			}
		})
	}
}

func TestAmountDemotes(t *testing.T) {
	roundTrip := NewAmount(math.MaxInt64).AddInt64(7).SubInt64(7)
	direct := NewAmount(math.MaxInt64)
	if !roundTrip.Equal(direct) {
		t.Errorf("%s != %s", roundTrip, direct)
	}
	if _, ok := roundTrip.asInt64(); !ok {
		t.Error("asInt64() reports it does not fit an int64")
	}
}

func TestAmountEqualIsNumeric(t *testing.T) {
	a := bigAmount(t, "9223372036854775814")
	b := bigAmount(t, "9223372036854775814")
	if a.large == b.large {
		t.Fatal("the two amounts share a pointer, so this proves nothing")
	}
	if !a.Equal(b) {
		t.Error("Equal() = false for equal values")
	}
	if a.Cmp(b) != 0 {
		t.Errorf("Cmp() = %d, want 0", a.Cmp(b))
	}
}

func TestAmountIsImmutable(t *testing.T) {
	shared := bigAmount(t, "9223372036854775814")
	before := shared.String()
	for range 5 {
		_ = shared.AddInt64(1000)
		_ = shared.SubInt64(1000)
	}
	if after := shared.String(); after != before {
		t.Errorf("the amount changed under arithmetic: %s -> %s", before, after)
	}
}

func TestAmountFromQuantity(t *testing.T) {
	cases := map[string]struct {
		name corev1.ResourceName
		qty  string
		want string
	}{
		"whole cores in milli":      {name: corev1.ResourceCPU, qty: "2", want: "2000"},
		"a fraction of a core":      {name: corev1.ResourceCPU, qty: "1.5", want: "1500"},
		"a milli":                   {name: corev1.ResourceCPU, qty: "500m", want: "500"},
		"below a milli rounds up":   {name: corev1.ResourceCPU, qty: "500u", want: "1"},
		"1E of cpu is not infinite": {name: corev1.ResourceCPU, qty: "1E", want: "1000000000000000000000"},
		"10P of cpu is exact":       {name: corev1.ResourceCPU, qty: "10P", want: "10000000000000000000"},
		"the largest int64 milli":   {name: corev1.ResourceCPU, qty: "9223372036854775807m", want: "9223372036854775807"},
		"one milli past int64":      {name: corev1.ResourceCPU, qty: "9223372036854775808m", want: "9223372036854775808"},
		// MilliValue overflows to zero at this bound, which would read as no quota at all.
		"one milli below MinInt64":     {name: corev1.ResourceCPU, qty: "-9223372036854775808m", want: "-9223372036854775808"},
		"whole devices":                {name: "example.com/gpu", qty: "8", want: "8"},
		"a fraction rounds up":         {name: "example.com/gpu", qty: "0.5", want: "1"},
		"the largest int64":            {name: "example.com/gpu", qty: "9223372036854775807", want: "9223372036854775807"},
		"one past MaxInt64 is capped":  {name: "example.com/gpu", qty: "9223372036854775808", want: "9223372036854775807"},
		"one below MinInt64 is capped": {name: "example.com/gpu", qty: "-9223372036854775808", want: "-9223372036854775807"},
		"8Ei is capped":                {name: "example.com/gpu", qty: "8Ei", want: "9223372036854775807"},
		"1000E is capped":              {name: "example.com/gpu", qty: "1000E", want: "9223372036854775807"},
		// Rounding away from zero would land one past what the API reports back.
		"a fraction past MaxInt64":     {name: "example.com/gpu", qty: "9223372036854775807.001", want: "9223372036854775807"},
		"a fraction below MinInt64":    {name: "example.com/gpu", qty: "-9223372036854775807.001", want: "-9223372036854775807"},
		"a fraction short of MaxInt64": {name: "example.com/gpu", qty: "9223372036854775806.999", want: "9223372036854775807"},
		"the largest cpu in cores":     {name: corev1.ResourceCPU, qty: "9223372036854775807", want: "9223372036854775807000"},
		"one core past MaxInt64 cores": {name: corev1.ResourceCPU, qty: "9223372036854775808", want: "9223372036854775807000"},
		// The bound is on cores, so a fraction below a milli still puts the value past it.
		"a fraction past MaxInt64 cores":     {name: corev1.ResourceCPU, qty: "9223372036854775807.001", want: "9223372036854775807000"},
		"under a milli past MaxInt64 cores":  {name: corev1.ResourceCPU, qty: "9223372036854775807.0009", want: "9223372036854775807000"},
		"a fraction short of MaxInt64 cores": {name: corev1.ResourceCPU, qty: "9223372036854775806.999", want: "9223372036854775806999"},
		"a negative amount":                  {name: "example.com/gpu", qty: "-3", want: "-3"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := AmountFromQuantity(tc.name, resource.MustParse(tc.qty))
			if got.String() != tc.want {
				t.Errorf("AmountFromQuantity(%s, %s) = %s, want %s", tc.name, tc.qty, got, tc.want)
			}
		})
	}
}

func TestAmountLedgerRecovers(t *testing.T) {
	saturating := NewAmount(math.MaxInt64)
	seven := NewAmount(7)

	var usage Amount
	usage = usage.Add(saturating)
	usage = usage.Add(seven)
	if want := "9223372036854775814"; usage.String() != want {
		t.Fatalf("after both joined = %s, want %s", usage, want)
	}
	usage = usage.Sub(saturating)
	if usage.String() != "7" {
		t.Fatalf("after the saturating one left = %s, want 7", usage)
	}
	usage = usage.Sub(seven)
	if usage.String() != "0" {
		t.Errorf("after both left = %s, want 0", usage)
	}
}

func TestAmountAsInt64(t *testing.T) {
	if v, ok := NewAmount(math.MaxInt64).asInt64(); !ok || v != math.MaxInt64 {
		t.Errorf("asInt64() = (%d, %v), want (%d, true)", v, ok, int64(math.MaxInt64))
	}
	past := NewAmount(math.MaxInt64).AddInt64(1)
	if _, ok := past.asInt64(); ok {
		t.Error("asInt64() reports a value past int64 fits one")
	}
	if got := past.asSaturatedInt64(); got != math.MaxInt64 {
		t.Errorf("asSaturatedInt64() = %d, want %d", got, int64(math.MaxInt64))
	}
	if got := NewAmount(math.MinInt64).SubInt64(1).asSaturatedInt64(); got != math.MinInt64 {
		t.Errorf("asSaturatedInt64() = %d, want %d", got, int64(math.MinInt64))
	}
}

func TestAmountAsApproximateFloat64(t *testing.T) {
	cases := map[string]struct {
		name   corev1.ResourceName
		amount Amount
		want   float64
	}{
		"bounded memory":              {name: corev1.ResourceMemory, amount: NewAmount(1e18), want: 1e18},
		"bounded CPU":                 {name: corev1.ResourceCPU, amount: NewAmount(5_500), want: 5.5},
		"at the old sentinel is +Inf": {name: corev1.ResourceMemory, amount: NewAmount(math.MaxInt64), want: math.Inf(1)},
		"past int64 is +Inf":          {name: corev1.ResourceMemory, amount: NewAmount(math.MaxInt64).AddInt64(7), want: math.Inf(1)},
		"cpu at 1E is +Inf":           {name: corev1.ResourceCPU, amount: AmountFromQuantity(corev1.ResourceCPU, resource.MustParse("1E")), want: math.Inf(1)},
		"below MinInt64 saturates":    {name: corev1.ResourceMemory, amount: NewAmount(math.MinInt64).SubInt64(1), want: float64(math.MinInt64)},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.amount.AsApproximateFloat64(tc.name); got != tc.want {
				t.Errorf("AsApproximateFloat64(%s) = %g, want %g", tc.name, got, tc.want)
			}
		})
	}
}

func BenchmarkAmountAddSmall(b *testing.B) {
	a := NewAmount(1 << 20)
	for b.Loop() {
		a = a.AddInt64(1)
		a = a.SubInt64(1)
	}
}

func TestPerThousandOf(t *testing.T) {
	const twoTo53 = int64(1) << 53
	cases := map[string]struct {
		a, b Amount
		want float64
	}{
		"a small ratio":      {a: NewAmount(1_000), b: NewAmount(1_000_000), want: 1},
		"a zero denominator": {a: NewAmount(5), b: Amount{}, want: 0},
		// Dividing as float64 loses the difference above 2^53 and answers 1000.
		"exact above float64 integers": {a: NewAmount(twoTo53 + 1), b: NewAmount(twoTo53), want: math.Nextafter(1000, 2000)},
		"a ratio past float64":         {a: bigAmount(t, "1"+strings.Repeat("0", 400)), b: NewAmount(1), want: math.Inf(1)},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.a.PerThousandOf(tc.b); got != tc.want {
				t.Errorf("PerThousandOf() = %v, want %v", got, tc.want)
			}
		})
	}
}

var benchRatio float64

func BenchmarkPerThousandOfSmall(b *testing.B) {
	borrowed, lendable := NewAmount(1_000), NewAmount(1_000_000)
	for b.Loop() {
		benchRatio = borrowed.PerThousandOf(lendable)
	}
}

func BenchmarkPerThousandOfLarge(b *testing.B) {
	borrowed := NewAmount(1_000)
	lendable := NewAmount(math.MaxInt64).AddInt64(1)
	for b.Loop() {
		benchRatio = borrowed.PerThousandOf(lendable)
	}
}

// The slow path must agree with Quantity.Value and MilliValue where both apply.
func TestScaledBigMatchesTheAccessors(t *testing.T) {
	for _, qty := range []string{
		"0", "1", "-1", "8", "1.5", "0.5", "500m", "500u", "-1.5", "-500m",
		"1Ki", "1Gi", "128974848", "9223372036854775807m",
	} {
		for _, name := range []corev1.ResourceName{corev1.ResourceCPU, "example.com/gpu"} {
			t.Run(string(name)+"/"+qty, func(t *testing.T) {
				q := resource.MustParse(qty)
				want := q.Value()
				if name == corev1.ResourceCPU {
					want = q.MilliValue()
				}
				if got := scaledBig(name, q); got.Cmp(big.NewInt(want)) != 0 {
					t.Errorf("scaledBig(%s, %s) = %s, accessor says %d", name, qty, got, want)
				}
			})
		}
	}
}

// A scale the parser never produces: the sign decides, without building the divisor.
func TestAmountFromQuantityBelowOneAtALargeScale(t *testing.T) {
	cases := map[string]struct {
		unscaled int64
		want     string
	}{
		"below one rounds away from zero":          {unscaled: 1, want: "1"},
		"below one rounds away from zero downward": {unscaled: -1, want: "-1"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			q := resource.NewDecimalQuantity(*inf.NewDec(tc.unscaled, 100_000), resource.DecimalSI)
			if got := AmountFromQuantity("example.com/gpu", *q); got.String() != tc.want {
				t.Errorf("AmountFromQuantity(%d at scale 100000) = %s, want %s", tc.unscaled, got, tc.want)
			}
		})
	}
}

// The parser normalizes to one scale, so the exponents are exercised directly.
func TestExceedsQuantity(t *testing.T) {
	cases := map[string]struct {
		unscaled string
		exp      int64
		want     bool
	}{
		"the ceiling itself":                {unscaled: "9223372036854775807", exp: 0},
		"one past the ceiling":              {unscaled: "9223372036854775808", exp: 0, want: true},
		"nine at the ceiling's scale":       {unscaled: "9", exp: 18},
		"one digit more":                    {unscaled: "1", exp: 19, want: true},
		"the ceiling written smaller":       {unscaled: "9223372036854775807000", exp: -3},
		"the ceiling and a thousandth":      {unscaled: "9223372036854775807001", exp: -3, want: true},
		"a thousandth short of the ceiling": {unscaled: "9223372036854775806999", exp: -3},
		// Decided from the digit count alone; the int32 extremes are in TestScaledIsBelowOne.
		"a scale far below the ceiling": {unscaled: "1", exp: -100_000},
		"a scale far above the ceiling": {unscaled: "1", exp: 100_000, want: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			unscaled, ok := new(big.Int).SetString(tc.unscaled, 10)
			if !ok {
				t.Fatalf("SetString(%s) did not parse", tc.unscaled)
			}
			if got := exceedsQuantity(unscaled, tc.exp); got != tc.want {
				t.Errorf("exceedsQuantity(%s, %d) = %v, want %v", tc.unscaled, tc.exp, got, tc.want)
			}
			if got := exceedsQuantity(new(big.Int).Neg(unscaled), tc.exp); got != tc.want {
				t.Errorf("exceedsQuantity(-%s, %d) = %v, want %v", tc.unscaled, tc.exp, got, tc.want)
			}
		})
	}
}

func TestScaledIsBelowOne(t *testing.T) {
	cases := map[string]struct {
		unscaled int64
		exp      int64
		want     bool
	}{
		"nine tenths":                {unscaled: 9, exp: -1, want: true},
		"exactly one":                {unscaled: 10, exp: -1},
		"ninety-nine hundredths":     {unscaled: 99, exp: -2, want: true},
		"a whole number":             {unscaled: 5, exp: 0},
		"a scaled-up number":         {unscaled: 5, exp: 3},
		"a scale too large to build": {unscaled: 1, exp: -2147483647, want: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := scaledIsBelowOne(big.NewInt(tc.unscaled), tc.exp); got != tc.want {
				t.Errorf("scaledIsBelowOne(%d, %d) = %v, want %v", tc.unscaled, tc.exp, got, tc.want)
			}
		})
	}
}

// The parser caps 8Ei but not 9223372036854775808; both must convert to one amount.
func TestAmountFromQuantityDoesNotDependOnTheSuffix(t *testing.T) {
	cases := map[string]struct{ decimal, binary string }{
		"one past the largest int64": {decimal: "9223372036854775808", binary: "8Ei"},
		"one below MinInt64":         {decimal: "-9223372036854775808", binary: "-8Ei"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			d := AmountFromQuantity("example.com/gpu", resource.MustParse(tc.decimal))
			b := AmountFromQuantity("example.com/gpu", resource.MustParse(tc.binary))
			if !d.Equal(b) {
				t.Errorf("%s = %s and %s = %s, want one amount", tc.decimal, d, tc.binary, b)
			}
		})
	}
}

// A reported value converts back to the amount it was reported for.
func TestAmountFromQuantityRoundTrips(t *testing.T) {
	f := NewResourceFormatter()
	cases := map[string]struct {
		name corev1.ResourceName
		qty  string
	}{
		"the largest int64 device":     {name: "example.com/gpu", qty: "9223372036854775807"},
		"one past MaxInt64 is bounded": {name: "example.com/gpu", qty: "9223372036854775808"},
		"8Ei is bounded":               {name: "example.com/gpu", qty: "8Ei"},
		"cpu past int64 milli":         {name: corev1.ResourceCPU, qty: "1E"},
		"the largest int64 in cores":   {name: corev1.ResourceCPU, qty: "9223372036854775807"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			in := AmountFromQuantity(tc.name, resource.MustParse(tc.qty))
			out := f.AmountQuantity(tc.name, in)
			if back := AmountFromQuantity(tc.name, out); !back.Equal(in) {
				t.Errorf("%s went in as %s and came back as %s", tc.qty, in, back)
			}
		})
	}
}
