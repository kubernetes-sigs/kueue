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
		"the zero value is zero":            {got: Amount{}, want: "0"},
		"MaxInt64 is an ordinary amount":    {got: NewAmount(math.MaxInt64), want: "9223372036854775807"},
		"a sum past int64 is exact":         {got: NewAmount(math.MaxInt64).AddInt64(7), want: "9223372036854775814"},
		"and comes back when it is undone":  {got: NewAmount(math.MaxInt64).AddInt64(7).SubInt64(math.MaxInt64), want: "7"},
		"whichever way round":               {got: NewAmount(math.MaxInt64).AddInt64(7).SubInt64(7), want: "9223372036854775807"},
		"a difference past int64 is exact":  {got: NewAmount(math.MinInt64).SubInt64(7), want: "-9223372036854775815"},
		"and comes back too":                {got: NewAmount(math.MinInt64).SubInt64(7).AddInt64(7), want: "-9223372036854775808"},
		"two large amounts add":             {got: bigAmount(t, "9223372036854775814").AddInt64(1), want: "9223372036854775815"},
		"a large amount minus a large one":  {got: bigAmount(t, "9223372036854775814").Sub(bigAmount(t, "9223372036854775814")), want: "0"},
		"MinInt64 subtracted from MinInt64": {got: NewAmount(math.MinInt64).SubInt64(math.MinInt64), want: "0"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.got.String(); got != tc.want {
				t.Errorf("= %s, want %s", got, tc.want)
			}
		})
	}
}

// A value that leaves the int64 range and comes back is held the same way as
// one that never left it, so the two are equal however they were reached.
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

// Two large amounts that are numerically equal hold different pointers, so
// equality has to read the value.
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

// Arithmetic builds a new value rather than writing through the pointer a
// snapshot may be sharing.
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
		"10P of cpu either":         {name: corev1.ResourceCPU, qty: "10P", want: "10000000000000000000"},
		"the largest int64 milli":   {name: corev1.ResourceCPU, qty: "9223372036854775807m", want: "9223372036854775807"},
		"one milli past it":         {name: corev1.ResourceCPU, qty: "9223372036854775808m", want: "9223372036854775808"},
		"whole devices":             {name: "example.com/gpu", qty: "8", want: "8"},
		"a fraction rounds up":      {name: "example.com/gpu", qty: "0.5", want: "1"},
		"the largest int64":         {name: "example.com/gpu", qty: "9223372036854775807", want: "9223372036854775807"},
		"one past it is capped":     {name: "example.com/gpu", qty: "9223372036854775808", want: "9223372036854775807"},
		"and downward too":          {name: "example.com/gpu", qty: "-9223372036854775808", want: "-9223372036854775807"},
		"the binary spelling of it": {name: "example.com/gpu", qty: "8Ei", want: "9223372036854775807"},
		"a whole prefix past it":    {name: "example.com/gpu", qty: "1000E", want: "9223372036854775807"},
		// The largest int64 and a fraction is past the ceiling as surely as the
		// next whole number is, and rounding it away from zero the way the
		// accessors do would land one past what the API reports back.
		"a fraction past it":          {name: "example.com/gpu", qty: "9223372036854775807.001", want: "9223372036854775807"},
		"a fraction past it downward": {name: "example.com/gpu", qty: "-9223372036854775807.001", want: "-9223372036854775807"},
		"a fraction short of it":      {name: "example.com/gpu", qty: "9223372036854775806.999", want: "9223372036854775807"},
		"the largest cpu in cores":    {name: corev1.ResourceCPU, qty: "9223372036854775807", want: "9223372036854775807000"},
		"one core past it":            {name: corev1.ResourceCPU, qty: "9223372036854775808", want: "9223372036854775807000"},
		// The bound is on the cores the API reports, so a fraction too small to
		// reach a milli still puts the value past it.
		"a fraction past it in cpu":     {name: corev1.ResourceCPU, qty: "9223372036854775807.001", want: "9223372036854775807000"},
		"below a milli past it in cpu":  {name: corev1.ResourceCPU, qty: "9223372036854775807.0009", want: "9223372036854775807000"},
		"a fraction short of it in cpu": {name: corev1.ResourceCPU, qty: "9223372036854775806.999", want: "9223372036854775806999"},
		"a negative amount":             {name: "example.com/gpu", qty: "-3", want: "-3"},
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

// The sequence from #14105: adding a workload and removing it again leaves the
// ledger where it started, whatever the numbers were.
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

func TestAmountApproximateFloat64(t *testing.T) {
	if got := AmountFromQuantity(corev1.ResourceCPU, resource.MustParse("1E")).AsApproximateFloat64(corev1.ResourceCPU); got != 1e18 {
		t.Errorf("1E cpu = %g, want 1e18", got)
	}
	if got := AmountFromQuantity(corev1.ResourceCPU, resource.MustParse("10P")).AsApproximateFloat64(corev1.ResourceCPU); got != 1e16 {
		t.Errorf("10P cpu = %g, want 1e16", got)
	}
	huge := NewAmount(math.MaxInt64)
	for range 1100 {
		huge = huge.Add(huge)
	}
	if got := huge.AsApproximateFloat64("example.com/gpu"); !math.IsInf(got, 1) {
		t.Errorf("an amount past float64 = %g, want +Inf", got)
	}
}

func BenchmarkAmountAddSmall(b *testing.B) {
	a := NewAmount(1 << 20)
	for b.Loop() {
		a = a.AddInt64(1)
		a = a.SubInt64(1)
	}
}

// The slow path has to answer what the Quantity accessors answer, since the
// fast path uses them and the two meet at the int64 boundary.
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

// A Quantity is not only built by the parser, and one built directly carries a
// scale the parser never reaches. The scale here is large enough to show the
// conversion answering from the sign rather than from a division, and small
// enough that a regression fails an assertion instead of exhausting the
// process. The int32 extremes belong to TestScaledIsBelowOne and
// TestExceedsQuantity, which decide them from the digit count and build
// nothing.
func TestAmountFromQuantityBelowOneAtALargeScale(t *testing.T) {
	for _, tc := range []struct {
		unscaled int64
		want     string
	}{
		{1, "1"},
		{-1, "-1"},
	} {
		q := resource.NewDecimalQuantity(*inf.NewDec(tc.unscaled, 100_000), resource.DecimalSI)
		if got := AmountFromQuantity("example.com/gpu", *q); got.String() != tc.want {
			t.Errorf("AmountFromQuantity(%d at scale 100000) = %s, want %s", tc.unscaled, got, tc.want)
		}
	}
}

// The exponents either side of the ceiling that the parser cannot reach: it
// normalizes every spelling to one scale, so the bound is called directly.
func TestExceedsQuantity(t *testing.T) {
	cases := map[string]struct {
		unscaled string
		exp      int64
		want     bool
	}{
		"the ceiling itself":           {unscaled: "9223372036854775807", exp: 0},
		"one past the ceiling":         {unscaled: "9223372036854775808", exp: 0, want: true},
		"nine at the ceiling's scale":  {unscaled: "9", exp: 18},
		"one digit more":               {unscaled: "1", exp: 19, want: true},
		"the ceiling written smaller":  {unscaled: "9223372036854775807000", exp: -3},
		"the ceiling and a thousandth": {unscaled: "9223372036854775807001", exp: -3, want: true},
		"a thousandth short of it":     {unscaled: "9223372036854775806999", exp: -3},
		// Large enough that only the digit count can decide them, bounded so a
		// regression fails an assertion rather than expanding a power of ten
		// nothing can stop. The int32 extremes are pinned by
		// TestScaledIsBelowOne, whose predicate cannot allocate at all.
		"a scale far below the ceiling": {unscaled: "1", exp: -100_000},
		"a scale far above it":          {unscaled: "1", exp: 100_000, want: true},
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

// One is the boundary, and the conversion answers the same either side of it
// once the divisor is small enough to build, so a line drawn one place over
// would go unnoticed there.
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

// The parser caps the binary path at the magnitude a Quantity holds and leaves
// the decimal path alone, so two spellings of the same number arrive as
// different values. Reading them as different quotas would make capacity depend
// on the suffix it was written with, which is not something the format decides.
func TestAmountFromQuantityDoesNotDependOnTheSuffix(t *testing.T) {
	for _, tc := range []struct{ decimal, binary string }{
		{"9223372036854775808", "8Ei"},
		{"-9223372036854775808", "-8Ei"},
	} {
		t.Run(tc.decimal, func(t *testing.T) {
			d := AmountFromQuantity("example.com/gpu", resource.MustParse(tc.decimal))
			b := AmountFromQuantity("example.com/gpu", resource.MustParse(tc.binary))
			if !d.Equal(b) {
				t.Errorf("%s = %s and %s = %s, want one amount", tc.decimal, d, tc.binary, b)
			}
		})
	}
}

// What the API reports converts back to the amount it was reported for, so a
// value at the boundary is bounded once rather than moving on every pass. An
// oversized Quantity is bounded on the way in, so it is the bounded amount that
// makes the round trip and not the spelling it arrived as.
func TestAmountFromQuantityRoundTrips(t *testing.T) {
	f := NewResourceFormatter()
	for _, tc := range []struct {
		name corev1.ResourceName
		qty  string
	}{
		{"example.com/gpu", "9223372036854775807"},
		{"example.com/gpu", "9223372036854775808"},
		{"example.com/gpu", "8Ei"},
		{corev1.ResourceCPU, "1E"},
		{corev1.ResourceCPU, "9223372036854775807"},
	} {
		t.Run(string(tc.name)+"/"+tc.qty, func(t *testing.T) {
			in := AmountFromQuantity(tc.name, resource.MustParse(tc.qty))
			out := f.AmountQuantity(tc.name, in)
			if back := AmountFromQuantity(tc.name, out); !back.Equal(in) {
				t.Errorf("%s went in as %s and came back as %s", tc.qty, in, back)
			}
		})
	}
}

// Where the accessor gives up, the conversion still has to answer. MilliValue
// documents that it may overflow, and at this boundary it returns zero, which
// would read a quota of that size as no quota at all.
func TestAmountFromQuantityPastTheAccessor(t *testing.T) {
	q := resource.MustParse("-9223372036854775808m")
	if got := AmountFromQuantity(corev1.ResourceCPU, q); got.String() != "-9223372036854775808" {
		t.Errorf("AmountFromQuantity = %s, want -9223372036854775808", got)
	}
	if got := q.MilliValue(); got != 0 {
		t.Logf("MilliValue no longer overflows here, it returns %d", got)
	}
}
