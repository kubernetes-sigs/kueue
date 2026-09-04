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
	if _, ok := roundTrip.AsInt64(); !ok {
		t.Error("AsInt64() reports it does not fit an int64")
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
		"the largest cpu in cores":  {name: corev1.ResourceCPU, qty: "9223372036854775807", want: "9223372036854775807000"},
		"one core past it":          {name: corev1.ResourceCPU, qty: "9223372036854775808", want: "9223372036854775807000"},
		// A scale this large is never expanded, so these answer at once rather
		// than building a power of ten with two billion digits.
		"a scale too large to hold": {name: "example.com/gpu", qty: "1e2147483647", want: "9223372036854775807"},
		"a negative amount":         {name: "example.com/gpu", qty: "-3", want: "-3"},
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
	if v, ok := NewAmount(math.MaxInt64).AsInt64(); !ok || v != math.MaxInt64 {
		t.Errorf("AsInt64() = (%d, %v), want (%d, true)", v, ok, int64(math.MaxInt64))
	}
	past := NewAmount(math.MaxInt64).AddInt64(1)
	if _, ok := past.AsInt64(); ok {
		t.Error("AsInt64() reports a value past int64 fits one")
	}
	if got := past.AsSaturatedInt64(); got != math.MaxInt64 {
		t.Errorf("AsSaturatedInt64() = %d, want %d", got, int64(math.MaxInt64))
	}
	if got := NewAmount(math.MinInt64).SubInt64(1).AsSaturatedInt64(); got != math.MinInt64 {
		t.Errorf("AsSaturatedInt64() = %d, want %d", got, int64(math.MinInt64))
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
// scale the parser never reaches: ParseQuantity does not return for a scale
// this size. The conversion answers from the sign rather than building the
// divisor, which would be a power of ten with two billion digits. Without that
// short circuit this test does not fail, it hangs.
func TestAmountFromQuantityBelowOneAtAScaleTooLargeToBuild(t *testing.T) {
	for _, tc := range []struct {
		unscaled int64
		want     string
	}{
		{1, "1"},
		{-1, "-1"},
	} {
		q := resource.NewDecimalQuantity(*inf.NewDec(tc.unscaled, 2147483647), resource.DecimalSI)
		if got := AmountFromQuantity("example.com/gpu", *q); got.String() != tc.want {
			t.Errorf("AmountFromQuantity(%d at scale 2147483647) = %s, want %s", tc.unscaled, got, tc.want)
		}
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

// The bound is applied in the unit the API reports and the conversion in the
// unit the resource is accounted in, so a Quantity that goes in comes back out.
// The two are only the same unit for CPU when the cap is built in cores.
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
			out, exact := f.AmountQuantity(tc.name, in)
			if !exact {
				t.Errorf("AmountQuantity(%s) reported %s inexactly", in, out.String())
			}
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
