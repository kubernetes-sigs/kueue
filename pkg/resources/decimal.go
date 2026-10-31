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
	"cmp"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"gopkg.in/inf.v0"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// scale is how many decimal places a Decimal keeps. It is not a free parameter:
// MulByFloat rounds to it, a Quantity carries nothing finer, and the split in
// NearestFloat64 relies on it. See that function before changing it.
const scale = 9

// nano is 10^scale: the base units in one unit the API reports. A test keeps
// the two in step.
const nano = 1_000_000_000

var (
	nanoInt   = big.NewInt(nano)
	nanoFloat = new(big.Float).SetInt64(nano)
)

// Decimal is an exact fractional resource amount, counted in 10^-9 of the unit
// the API reports the resource in. It is what AdmissionFairSharing keeps its
// decayed history and pending penalties in; quota accounting stays on Amount,
// which is integral. The count is an integer at a fixed scale, so unlike a
// decimal there is no scale here to grow. Equal amounts are held the same way
// however they were reached.
type Decimal struct {
	// Makes Decimal uncomparable: == would compare the pointer in large.
	_ [0]func()
	// small holds the value while it fits an int64.
	small int64
	// large holds it otherwise. It is never mutated, since a ledger entry is
	// copied shallowly and a snapshot shares it with readers.
	large *big.Int
}

// NewDecimalFromNano returns the Decimal for v base units, v * 10^-9 of the
// unit the API reports the resource in. The name says which unit v is in.
func NewDecimalFromNano(v int64) Decimal { return Decimal{small: v} }

// decimalFromBig returns the Decimal for v, in an int64 when it fits one. It
// takes ownership of v: a value past int64 is kept by pointer and never copied.
func decimalFromBig(v *big.Int) Decimal {
	if v.IsInt64() {
		return Decimal{small: v.Int64()}
	}
	return Decimal{large: v}
}

// big returns d as a *big.Int the caller may not modify.
func (d Decimal) big() *big.Int {
	if d.large != nil {
		return d.large
	}
	return big.NewInt(d.small)
}

func (d Decimal) Add(o Decimal) Decimal {
	if d.large == nil && o.large == nil {
		if sum, ok := addInt64(d.small, o.small); ok {
			return Decimal{small: sum}
		}
	}
	return decimalFromBig(new(big.Int).Add(d.big(), o.big()))
}

func (d Decimal) Sub(o Decimal) Decimal {
	if d.large == nil && o.large == nil {
		if diff, ok := subInt64(d.small, o.small); ok {
			return Decimal{small: diff}
		}
	}
	return decimalFromBig(new(big.Int).Sub(d.big(), o.big()))
}

func (d Decimal) Neg() Decimal {
	// MinInt64 is the one value whose negation leaves the int64 range, since
	// the range is not symmetric.
	if d.large == nil && d.small > math.MinInt64 {
		return Decimal{small: -d.small}
	}
	return decimalFromBig(new(big.Int).Neg(d.big()))
}

func (d Decimal) Sign() int {
	if d.large != nil {
		return d.large.Sign()
	}
	switch {
	case d.small > 0:
		return 1
	case d.small < 0:
		return -1
	}
	return 0
}

func (d Decimal) IsZero() bool { return d.Sign() == 0 }

// Cmp returns -1 / 0 / +1 like bytes.Compare. It leans on the invariant that
// decimalFromBig keeps: a value that fits an int64 is always held in small, so
// a value held in large is outside that range and its sign alone decides the
// comparison against one held in small.
func (d Decimal) Cmp(o Decimal) int {
	if d.large == nil && o.large == nil {
		return cmp.Compare(d.small, o.small)
	}
	switch {
	case o.large == nil:
		return d.large.Sign()
	case d.large == nil:
		return -o.large.Sign()
	default:
		return d.large.Cmp(o.large)
	}
}

func (d Decimal) Equal(o Decimal) bool { return d.Cmp(o) == 0 }

// MulFloatRoundDown returns d * f, rounded towards zero at the scale, which
// reproduces resource.MulByFloat digit for digit: f is taken as the shortest
// decimal reading back as the same float64, the product is formed exactly, and
// only then are the digits past the scale dropped. Rounding towards zero is
// what keeps a decayed value from settling above the usage it decays towards
// (#13621). It panics on a non-finite factor, as MulByFloat does.
func (d Decimal) MulFloatRoundDown(f float64) Decimal {
	mantissa, exp := decimalParts(f)
	product := new(big.Int).Mul(d.big(), mantissa)
	if exp == 0 {
		return decimalFromBig(product)
	}
	return decimalFromBig(product.Quo(product, pow10(-exp)))
}

// pow10 returns 10^n as a *big.Int the caller may not modify. The exponents
// repeat, so the small ones are built once rather than on every multiplication.
func pow10(n int) *big.Int {
	if n < len(pow10Table) {
		return pow10Table[n]
	}
	return new(big.Int).Exp(bigTen, big.NewInt(int64(n)), nil)
}

// The exponent MulFloatRoundDown needs is the number of decimal places in the
// factor's shortest form, at most 23 across the configurations AFS allows. The
// table holds those with room rather than being a bound; pow10 computes the rest.
var (
	bigTen     = big.NewInt(10)
	pow10Table = buildPow10Table(41)
)

func buildPow10Table(n int) []*big.Int {
	t := make([]*big.Int, n)
	t[0] = big.NewInt(1)
	for i := 1; i < n; i++ {
		t[i] = new(big.Int).Mul(t[i-1], bigTen)
	}
	return t
}

// decimalParts returns m and e with f == m * 10^e exactly, taking f through the
// same shortest decimal string MulByFloat parses.
func decimalParts(f float64) (*big.Int, int) {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	exp := 0
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		exp = -(len(s) - dot - 1)
		s = s[:dot] + s[dot+1:]
	}
	m, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic(fmt.Sprintf("MulFloatRoundDown called with a non-finite factor: %v", f))
	}
	return m, exp
}

// NearestFloat64 returns the float64 nearest to the exact value, ties to even.
// The name is deliberate: Quantity.AsApproximateFloat64 rounds twice and its
// result depends on how the Quantity happens to be stored, while this depends
// only on the value. Most values have no exact float64, so which neighbour is
// chosen is decided here rather than left to a two-step conversion.
//
// Each branch rounds the exact value once:
//
//   - |d| <= 2^53: both operands are exact float64s, so the division rounds once.
//   - a larger int64: q and rem are each exact as float64s, so only the addition
//     rounds. The error in rem/10^9 is at most 2^-54, while an exact value here
//     is at least 2^9/(10^9 * 2^30) from any rounding boundary, so it cannot
//     cross one: boundaries lie on a grid no finer than 2^-30 because the result
//     exceeds 2^23, and the distance has a numerator divisible by 2^9 because
//     10^9 = 2^9 * 5^9, and is never zero because that would need |d| past
//     MaxInt64. The margin is only 8.6x and it comes from the scale, so this
//     branch holds at scale 9 and not finer; a test pins it.
//   - past int64: the same split holds while the integer part is exact, and a
//     value sitting on a boundary is then dyadic, so rem/10^9 is exact and the
//     addition breaks the tie. Beyond that the quotient is taken in arbitrary
//     precision and rounded once to 53 bits instead.
func (d Decimal) NearestFloat64() float64 {
	if d.large == nil {
		if d.small >= -maxExactFloat64Int && d.small <= maxExactFloat64Int {
			return float64(d.small) / nano
		}
		q, rem := d.small/nano, d.small%nano
		return float64(q) + float64(rem)/nano
	}
	q, rem := new(big.Int).QuoRem(d.large, nanoInt, new(big.Int))
	if q.IsInt64() && q.Int64() > -maxExactFloat64Int && q.Int64() < maxExactFloat64Int {
		return float64(q.Int64()) + float64(rem.Int64())/nano
	}
	exact := new(big.Float).SetPrec(uint(d.large.BitLen()) + 1).SetInt(d.large)
	f, _ := new(big.Float).SetPrec(53).Quo(exact, nanoFloat).Float64()
	return f
}

// FromQuantity converts q, the form the status carries, into base units. Digits
// past the scale are dropped towards zero; a status written by Kueue has none.
// AsDec can hand back the Quantity's own inf.Dec, so the unscaled value is
// copied rather than stored.
func FromQuantity(q resource.Quantity) Decimal {
	dec := q.AsDec()
	unscaled, s := dec.UnscaledBig(), int(dec.Scale())
	switch {
	case s == scale:
		return decimalFromBig(new(big.Int).Set(unscaled))
	case s < scale:
		return decimalFromBig(new(big.Int).Mul(unscaled, pow10(scale-s)))
	default:
		return decimalFromBig(new(big.Int).Quo(unscaled, pow10(s-scale)))
	}
}

// AsQuantity returns d in the form the status carries, the inverse of
// FromQuantity and the same Quantity MulByFloat produces today. No copy is
// needed: inf.Dec holds its unscaled value in an unexported field and copies in
// every setter reaching it, so the Quantity cannot alias large.
//
// The result is exact at any magnitude and is not capped the way Amount's is,
// which caps because its values are summed across a cohort tree. The one
// magnitude that does not survive a round trip is a whole multiple of 10^21 of
// the reported unit, where Quantity.String runs out of suffixes and drops the
// exponent: the API type's limit, shared with the ResourceList path.
func (d Decimal) AsQuantity() resource.Quantity {
	return *resource.NewDecimalQuantity(*inf.NewDecBig(d.big(), scale), resource.DecimalSI)
}

func (d Decimal) String() string {
	q := d.AsQuantity()
	return q.String()
}

// Resources is a ledger's worth of Decimals, the Decimal counterpart of the
// ResourceList the AFS ledger holds today.
type Resources map[corev1.ResourceName]Decimal

func FromResourceList(rl corev1.ResourceList) Resources {
	if rl == nil {
		return nil
	}
	r := make(Resources, len(rl))
	for name, q := range rl {
		r[name] = FromQuantity(q)
	}
	return r
}

func (r Resources) AsResourceList() corev1.ResourceList {
	if r == nil {
		return nil
	}
	rl := make(corev1.ResourceList, len(r))
	for name, d := range r {
		rl[name] = d.AsQuantity()
	}
	return rl
}
