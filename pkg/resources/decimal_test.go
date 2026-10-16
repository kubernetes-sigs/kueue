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
	"math/rand"
	"strings"
	"testing"

	"gopkg.in/inf.v0"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	utilresource "sigs.k8s.io/kueue/pkg/util/resource"
)

func nearest(v *big.Int) float64 {
	f, _ := new(big.Rat).SetFrac(v, nanoInt).Float64()
	return f
}

func TestFloat64EdgeValues(t *testing.T) {
	for _, v := range []int64{0, 1, -1, 1e9, -1e9, 999999999, -999999999,
		maxExactFloat64Int, -maxExactFloat64Int, maxExactFloat64Int + 1, -(maxExactFloat64Int + 1),
		math.MaxInt64, math.MinInt64, math.MinInt64 + 1} {
		if got, want := NewDecimalFromNano(v).NearestFloat64(), nearest(big.NewInt(v)); got != want {
			t.Errorf("NewDecimalFromNano(%d).NearestFloat64() = %v, want %v", v, got, want)
		}
	}
}

func TestFloat64IsTheNearestFloat64(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for range 200000 {
		v := r.Int63()
		if r.Intn(2) == 0 {
			v = -v
		}
		if got, want := NewDecimalFromNano(v).NearestFloat64(), nearest(big.NewInt(v)); got != want {
			t.Fatalf("NewDecimalFromNano(%d).NearestFloat64() = %v, want %v", v, got, want)
		}
	}
	for range 50000 {
		v := new(big.Int).Rand(r, new(big.Int).Lsh(big.NewInt(1), uint(64+r.Intn(60))))
		v.SetBit(v, 63, 1)
		if r.Intn(2) == 0 {
			v.Neg(v)
		}
		if got, want := decimalFromBig(v).NearestFloat64(), nearest(v); got != want {
			t.Fatalf("decimalFromBig(%s).NearestFloat64() = %v, want %v", v, got, want)
		}
	}
}

// Values sitting exactly on a rounding boundary are constructed rather than
// hoped for: a float64 is picked, the midpoint between it and its successor is
// taken exactly, and the base units landing there are derived from it.
//
// A midpoint is a whole number of base units only from 2^44 up, since it is an
// odd multiple of 2^(e-53) and 10^9 = 2^9 * 5^9 supplies just 2^9. Past 2^53
// the integer part stops fitting a float64 and NearestFloat64 leaves the split
// for big.Float, so both sides of that switch are drawn and both are required:
// the two resolve a tie by different means, the hardware addition on one and
// big.Float's rounding mode on the other.
func TestFloat64OnRoundingBoundaries(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	split, arbitrary := 0, 0
	for split+arbitrary < 2000 {
		e := r.Intn(17) + 44
		x := math.Ldexp(float64(1<<52+r.Int63n(1<<52)), e-52)
		mid := new(big.Rat).Add(new(big.Rat).SetFloat64(x), new(big.Rat).SetFloat64(math.Nextafter(x, math.Inf(1))))
		mid.Quo(mid, big.NewRat(2, 1))
		scaled := new(big.Rat).Mul(mid, new(big.Rat).SetInt(nanoInt))
		if !scaled.IsInt() {
			t.Fatalf("a midpoint at 2^%d is not a whole number of base units: %s", e, scaled)
		}
		onBoundary := new(big.Int).Quo(scaled.Num(), scaled.Denom())
		// Which branch NearestFloat64 takes, by its own condition.
		if q := new(big.Int).Quo(onBoundary, nanoInt); q.IsInt64() && q.Int64() < maxExactFloat64Int {
			split++
		} else {
			arbitrary++
		}
		for _, delta := range []int64{-1, 0, 1} {
			v := new(big.Int).Add(onBoundary, big.NewInt(delta))
			for _, v := range []*big.Int{v, new(big.Int).Neg(v)} {
				if got, want := decimalFromBig(v).NearestFloat64(), nearest(v); got != want {
					t.Fatalf("decimalFromBig(%s).NearestFloat64() = %v, want %v", v, got, want)
				}
			}
		}
	}
	if split == 0 || arbitrary == 0 {
		t.Fatalf("both branches must see a boundary: %d on the split, %d on big.Float", split, arbitrary)
	}
}

// MulFloatRoundDown must produce the same digits as MulByFloat, which the
// decay's agreement with the ResourceList ledger rests on.
func TestMulRoundDownMatchesMulByFloatAllValues(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	factors := []float64{0, 1, 0.5, 1e-9, 0.00034376390587387284, 0.9996562360941261, 0.3333333333333333, 2, 1234.5678}
	for range 20000 {
		factors = append(factors, r.Float64())
	}
	values := []*big.Int{big.NewInt(0), big.NewInt(1), big.NewInt(-1), big.NewInt(687527), big.NewInt(math.MaxInt64)}
	for range 200 {
		v := new(big.Int).Rand(r, new(big.Int).Lsh(big.NewInt(1), uint(10+r.Intn(90))))
		if r.Intn(2) == 0 {
			v.Neg(v)
		}
		values = append(values, v)
	}
	const name = corev1.ResourceCPU
	for _, v := range values {
		d := decimalFromBig(new(big.Int).Set(v))
		for _, f := range factors {
			want := utilresource.MulByFloat(corev1.ResourceList{name: d.AsQuantity()}, f)[name]
			got := d.MulFloatRoundDown(f).AsQuantity()
			if got.Cmp(want) != 0 {
				t.Fatalf("Decimal(%s).MulFloatRoundDown(%v) = %s, MulByFloat = %s", v, f, got.String(), want.String())
			}
		}
	}
}

// The message is part of what is checked: MulByFloat panics with the same
// shape, and a missed check would panic too, on a nil mantissa, which a bare
// recover cannot tell apart.
func TestMulRoundDownPanicsOnNonFinite(t *testing.T) {
	for _, f := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		func() {
			defer func() {
				rec := recover()
				if rec == nil {
					t.Errorf("MulFloatRoundDown(%v) did not panic", f)
					return
				}
				msg, ok := rec.(string)
				if !ok || !strings.Contains(msg, "non-finite factor") {
					t.Errorf("MulFloatRoundDown(%v) panicked with %v, want a non-finite factor message", f, rec)
				}
			}()
			NewDecimalFromNano(1).MulFloatRoundDown(f)
		}()
	}
}

func TestQuantityRoundTrip(t *testing.T) {
	for _, s := range []string{"0", "1", "15", "946.635135", "1374817n", "500m", "3k", "64Ti", "-2.5"} {
		q := resource.MustParse(s)
		back := FromQuantity(q).AsQuantity()
		if back.Cmp(q) != 0 {
			t.Errorf("FromQuantity(%s).AsQuantity() = %s, want the same value", s, back.String())
		}
	}
}

// The ledger as a whole: decay computed on Decimals matches the ResourceList
// path digit for digit, including the per-term rounding (#13621). The three
// steps mirror CalculateDecayedConsumed rather than calling it, so that both
// sides visibly use the same alpha; alpha here is the default pairing of a five
// minute sampling interval with a 168 hour half-life.
func TestDecayMatchesResourceListPath(t *testing.T) {
	alpha := 1 - math.Pow(0.5, 300.0/604800.0)
	usage := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3"), corev1.ResourceMemory: resource.MustParse("16Gi")}
	old := corev1.ResourceList{}
	dOld := FromResourceList(corev1.ResourceList{})
	dUsage := FromResourceList(usage)
	for tick := range 500 {
		scaledOld := utilresource.MulByFloat(old, 1-alpha)
		scaledNew := utilresource.MulByFloat(usage, alpha)
		old = utilresource.MergeResourceListKeepSum(scaledOld, scaledNew)

		next := make(Resources, len(dUsage))
		for name, u := range dUsage {
			next[name] = dOld[name].MulFloatRoundDown(1 - alpha).Add(u.MulFloatRoundDown(alpha))
		}
		dOld = next

		for name, q := range old {
			if got := dOld[name].AsQuantity(); got.Cmp(q) != 0 {
				t.Fatalf("tick %d, %s: Decimal ledger %s, ResourceList ledger %s", tick, name, got.String(), q.String())
			}
		}
	}
}

// A Decimal built from a Quantity must not alias that Quantity either. Only
// this shape can: the scale has to be exactly 9, so FromQuantity copies instead
// of computing a fresh big.Int, and the value has to be past int64, so
// decimalFromBig keeps it by pointer. Roughly 16Gi of memory with a nano tail,
// which is the shape a ledger value takes.
func TestFromQuantityDoesNotAlias(t *testing.T) {
	q := resource.MustParse("17179869184.000000001")
	d := FromQuantity(q)
	before := d.String()
	q.Add(resource.MustParse("1000"))
	if after := d.String(); after != before {
		t.Errorf("mutating the Quantity changed the Decimal: %s -> %s", before, after)
	}
}

// nano and scale are written separately; neither may drift from the other.
func TestNanoMatchesScale(t *testing.T) {
	want := new(big.Int).Exp(big.NewInt(10), big.NewInt(scale), nil)
	if nanoInt.Cmp(want) != 0 {
		t.Errorf("nano = %s, want 10^%d = %s", nanoInt, scale, want)
	}
}

// Add, Sub and Neg must agree with big.Int at the edges of the int64 range,
// where the fast paths decide whether to fall back.
func TestArithmeticAtInt64Edges(t *testing.T) {
	edges := []int64{math.MinInt64, math.MinInt64 + 1, -1 << 62, -1, 0, 1, 1 << 62, math.MaxInt64 - 1, math.MaxInt64}
	for _, x := range edges {
		dx := NewDecimalFromNano(x)
		if got, want := dx.Neg().big(), new(big.Int).Neg(big.NewInt(x)); got.Cmp(want) != 0 {
			t.Errorf("Decimal(%d).Neg() = %s, want %s", x, got, want)
		}
		for _, y := range edges {
			dy := NewDecimalFromNano(y)
			if got, want := dx.Add(dy).big(), new(big.Int).Add(big.NewInt(x), big.NewInt(y)); got.Cmp(want) != 0 {
				t.Errorf("Decimal(%d).Add(%d) = %s, want %s", x, y, got, want)
			}
			if got, want := dx.Sub(dy).big(), new(big.Int).Sub(big.NewInt(x), big.NewInt(y)); got.Cmp(want) != 0 {
				t.Errorf("Decimal(%d).Sub(%d) = %s, want %s", x, y, got, want)
			}
			if got, want := dx.Cmp(dy), big.NewInt(x).Cmp(big.NewInt(y)); got != want {
				t.Errorf("Decimal(%d).Cmp(%d) = %d, want %d", x, y, got, want)
			}
		}
	}
	// Arithmetic that starts past int64 has to narrow back when the result
	// fits, since Cmp decides by representation before it looks at the value.
	past := new(big.Int).Lsh(big.NewInt(1), 70)
	large := func() Decimal { return decimalFromBig(new(big.Int).Set(past)) }
	for _, tc := range []struct {
		name string
		got  Decimal
	}{
		{"large + -large", large().Add(decimalFromBig(new(big.Int).Neg(past)))},
		{"large - large", large().Sub(large())},
		{"large + small - large", large().Add(NewDecimalFromNano(1)).Sub(large())},
	} {
		if tc.got.large != nil {
			t.Errorf("%s = %s held in large, want it narrowed to small", tc.name, tc.got.big())
		}
	}
}

// Cmp and Equal must hold across the int64/big.Int boundary, in both directions.
func TestCmpAcrossRepresentations(t *testing.T) {
	past := new(big.Int).Lsh(big.NewInt(1), 70)
	big1, big2 := decimalFromBig(new(big.Int).Set(past)), decimalFromBig(new(big.Int).Neg(past))
	small := NewDecimalFromNano(1031291)
	for _, tc := range []struct {
		name string
		a, b Decimal
		want int
	}{
		{"large positive vs small", big1, small, 1},
		{"small vs large positive", small, big1, -1},
		{"large negative vs small", big2, small, -1},
		{"small vs large negative", small, big2, 1},
		{"large vs large, equal", big1, decimalFromBig(new(big.Int).Set(past)), 0},
		{"large vs larger", big1, decimalFromBig(new(big.Int).Lsh(big.NewInt(1), 71)), -1},
		{"larger vs large", decimalFromBig(new(big.Int).Lsh(big.NewInt(1), 71)), big1, 1},
		{"large negative vs larger negative", big2, decimalFromBig(new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 71))), 1},
	} {
		if got := tc.a.Cmp(tc.b); got != tc.want {
			t.Errorf("%s: Cmp = %d, want %d", tc.name, got, tc.want)
		}
	}
	if !big1.Equal(decimalFromBig(new(big.Int).Set(past))) {
		t.Error("two Decimals holding the same large value are not Equal")
	}
	// decimalFromBig has to narrow a value that fits an int64. If it did not,
	// Cmp would take the large operand as outside the int64 range and answer by
	// its sign alone, so this would come out unequal.
	if !NewDecimalFromNano(5).Equal(decimalFromBig(big.NewInt(5))) {
		t.Error("decimalFromBig did not narrow a value that fits an int64")
	}
}

// apimachinery rounds a parsed Quantity to the ninth decimal, away from zero
// (quantity.go:371-375), so a status can never carry a finer value.
func TestParsedQuantityIsAlreadyAtScale(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
	}{
		{"0.0000000004", 1}, // RoundUp, not round-half: any remainder goes up
		{"0.0000000005", 1},
		{"0.0000000015", 2},
		{"-0.0000000015", -2},
		{"0.001031291", 1031291},
	} {
		q := resource.MustParse(tc.in)
		if got := FromQuantity(q); !got.Equal(NewDecimalFromNano(tc.want)) {
			t.Errorf("FromQuantity(%s) = %s, want %d base units", tc.in, got.String(), tc.want)
		}
	}
}

// A Quantity built in code can still be finer than the ledger's scale. Those
// digits are dropped towards zero, as inf.RoundDown would.
func TestFromQuantityDropsDigitsPastScale(t *testing.T) {
	for _, tc := range []struct {
		unscaled int64
		scale    int32
		want     int64
	}{
		{1234567, 12, 1234},   // 0.000001234567 -> 1234n
		{-1234567, 12, -1234}, // towards zero, not down
		{5, 10, 0},            // 0.0000000005 -> 0n
		{1031291, 9, 1031291}, // already at the ledger's scale
	} {
		q := resource.NewDecimalQuantity(*inf.NewDec(tc.unscaled, inf.Scale(tc.scale)), resource.DecimalSI)
		if got := FromQuantity(*q); !got.Equal(NewDecimalFromNano(tc.want)) {
			t.Errorf("FromQuantity(%s) = %s, want %d base units", q.AsDec(), got.String(), tc.want)
		}
	}
}

// nil and empty must survive the round trip: MulByFloat preserves them today
// and the #13621 tests rely on it.
func TestResourcesNilAndEmpty(t *testing.T) {
	if got := FromResourceList(nil); got != nil {
		t.Errorf("FromResourceList(nil) = %v, want nil", got)
	}
	if got := Resources(nil).AsResourceList(); got != nil {
		t.Errorf("Resources(nil).AsResourceList() = %v, want nil", got)
	}
	if got := FromResourceList(corev1.ResourceList{}); got == nil || len(got) != 0 {
		t.Errorf("FromResourceList(empty) = %v, want an empty non-nil map", got)
	}
	if got := (Resources{}).AsResourceList(); got == nil || len(got) != 0 {
		t.Errorf("Resources{}.AsResourceList() = %v, want an empty non-nil map", got)
	}
}

func TestPow10TableMatchesExp(t *testing.T) {
	for n := range 45 {
		want := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
		if got := pow10(n); got.Cmp(want) != 0 {
			t.Errorf("pow10(%d) = %s, want %s", n, got, want)
		}
	}
}

// The table's entries are shared, so anyone mutating one in place would
// corrupt every multiply that follows. Both factors of a decay tick are used,
// since they reach different entries, and the whole table is compared against
// freshly computed powers rather than against itself before the loop: an entry
// damaged earlier in the run would otherwise look unchanged here.
func TestPow10TableIsNotMutatedByUse(t *testing.T) {
	for i := range 100 {
		d := NewDecimalFromNano(int64(i))
		d.MulFloatRoundDown(0.00034376390587387284) // reaches pow10(20)
		d.MulFloatRoundDown(0.9996562360941261)     // reaches pow10(16)
	}
	for n := range pow10Table {
		want := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
		if got := pow10(n); got.Cmp(want) != 0 {
			t.Fatalf("pow10(%d) = %s after use, want %s", n, got, want)
		}
	}
}

// AsQuantity is reachable from anywhere, not only from the ledger, so what it
// does at the magnitudes a Quantity cannot spell is pinned here rather than
// argued from the ledger's own magnitudes. The last case is a hole in
// Quantity.String, which has no suffix past E; the ResourceList path in use
// today has it identically, so this test pins it rather than working around it.
func TestAsQuantityAtQuantityStringLimits(t *testing.T) {
	units := func(s string) Decimal {
		v, ok := new(big.Int).SetString(s, 10)
		if !ok {
			t.Fatalf("bad test input %q", s)
		}
		return decimalFromBig(new(big.Int).Mul(v, nanoInt))
	}
	for _, tc := range []struct {
		name      string
		in        Decimal
		want      string
		roundTrip bool
	}{
		{"MaxInt64 units", units("9223372036854775807"), "9223372036854775807", true},
		{"past MaxInt64 units", units("9223372036854775808"), "9223372036854775808", true},
		{"10^21 units, off by one base unit", units("1000000000000000000000").Add(NewDecimalFromNano(1)), "1000000000000000000000000000001n", true},
		{"just past 10^21 units", units("1000000000000000000001"), "1000000000000000000001", true},
		// Needs a suffix of 10^21, which the canonical form has no letter for,
		// so the exponent is dropped. Same for the ResourceList path.
		{"10^21 units exactly", units("1000000000000000000000"), "1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := tc.in.AsQuantity()
			if got := q.String(); got != tc.want {
				t.Fatalf("AsQuantity().String() = %s, want %s", got, tc.want)
			}
			// Whatever it prints, the Quantity itself holds the exact value, and
			// the ResourceList path prints the same thing.
			if q.AsDec().Cmp(inf.NewDecBig(tc.in.big(), scale)) != 0 {
				t.Errorf("AsQuantity() is not exact: %s", q.AsDec())
			}
			// That this is pre-existing is checked rather than asserted: the
			// ResourceList path prints the same string for the same value.
			if got := utilresource.MulByFloat(corev1.ResourceList{corev1.ResourceCPU: q}, 1.0)[corev1.ResourceCPU]; got.String() != tc.want {
				t.Errorf("the ResourceList path prints %s, want the same %s", got.String(), tc.want)
			}
			parsed, err := resource.ParseQuantity(q.String())
			if err != nil {
				t.Fatalf("ParseQuantity(%s): %v", q.String(), err)
			}
			if gotRT := FromQuantity(parsed).Cmp(tc.in) == 0; gotRT != tc.roundTrip {
				t.Errorf("round trip through the string form = %v, want %v (got %s)", gotRT, tc.roundTrip, FromQuantity(parsed).String())
			}
		})
	}
}

// NearestFloat64's int64 branch splits the value and adds the parts back,
// which rounds rem/10^9 before the final addition. That is correct only while
// the smallest distance from an exact value to a float64 rounding boundary
// stays above that rounding's error. Both quantities follow from scale, so
// this pins the margin rather than the constant: it is about 8.6 at scale 9 and
// below 1 at scale 10, where the split would start answering wrongly.
func TestNearestFloat64SplitIsSafeAtThisScale(t *testing.T) {
	// The smallest magnitude the split branch sees, in the reported unit.
	smallest := math.Ldexp(1, 53) / math.Pow(10, scale)
	_, binade := math.Frexp(smallest)
	exponent := binade - 1
	// Boundaries are odd multiples of 2^(exponent-53), so they lie on a grid of
	// 2^-grid, and a coarser grid only helps.
	grid := 53 - exponent
	// 10^scale = 2^scale * 5^scale, so the numerator of the distance from an
	// exact value to a boundary is a non-zero multiple of 2^scale.
	bound := math.Pow(2, scale) / (math.Pow(10, scale) * math.Ldexp(1, grid))
	// The error of float64(rem)/10^scale, at most half an ulp of a value below 1.
	err := math.Ldexp(1, -54)

	t.Logf("scale=%d: smallest split value %.4f, 2^%d grid, bound %.3g, error %.3g, margin %.2f",
		scale, smallest, -grid, bound, err, bound/err)
	if bound <= err {
		t.Fatalf("the split in NearestFloat64 is not safe at scale %d: "+
			"boundary distance %.3g is not above the rounding error %.3g; "+
			"that branch has to go through big.Float instead", scale, bound, err)
	}
}

// The decay factor the MulByFloat tests use for a 168h half-life sampled every
// 5 minutes, repeated here so the ported cases below run on the same numbers.
const longHalfLifeDecayFactor = 0.00034376390587387284

// Ported from the MulByFloat tests for #13621. Repeatedly scaling by a factor
// below 1 models an idle LocalQueue decaying. Rounding up would settle on a
// non-zero fixed point and leave usage that never expires.
func TestMulFloatRoundDownDecaysToZero(t *testing.T) {
	cases := map[string]float64{
		"168h half-life": 1 - longHalfLifeDecayFactor,
		"1h half-life":   0.94387431268169349,
	}
	for name, factor := range cases {
		t.Run(name, func(t *testing.T) {
			d := NewDecimalFromNano(2 * nano)
			for range 200000 {
				d = d.MulFloatRoundDown(factor)
				if d.IsZero() {
					return
				}
			}
			t.Errorf("Unexpected residual usage, expecting decay to 0 got %s", d.String())
		})
	}
}
