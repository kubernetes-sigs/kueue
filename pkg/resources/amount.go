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
	"strconv"

	"gopkg.in/inf.v0"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Amount is an exact integer resource amount, in the unit the resource is
// accounted in: milliCPU for cpu, whole units for everything else.
//
// Values inside the int64 range are held in small and cost no allocation. A
// result that leaves that range is held in large instead, and one that comes
// back inside it is held in small again.
//
// A stored *big.Int is never mutated and never handed out. big.Int does not
// support shallow copies, while resourceNode.Clone and
// FlavorResourceQuantities.Clone copy their maps shallowly, so a snapshot and
// the cache it came from share these pointers. Treating them as immutable is
// what makes that sharing safe, and nothing outside this package can reach one
// to break it.
//
// The zero-sized func field makes Amount uncomparable, so == is a compile error
// rather than an answer about the pointer in large. Use Equal or Cmp.
type Amount struct {
	_     [0]func()
	small int64
	large *big.Int
}

var (
	one         = big.NewInt(1)
	ten         = big.NewInt(10)
	thousand    = big.NewRat(1000, 1)
	thousandInt = big.NewInt(1000)

	// The largest magnitude a single Quantity holds, in the unit it reports.
	maxQuantityInt = big.NewInt(math.MaxInt64)

	// A Quantity holds at most MaxInt64 in magnitude in the unit it reports,
	// and it reports CPU in cores, so this is that ceiling in milli.
	maxCPUAmount = new(big.Int).Mul(maxQuantityInt, thousandInt)
)

// maxQuantityDigits is how many decimal digits maxQuantityInt has.
const maxQuantityDigits = 19

// maxExactFloat64Int is the largest integer magnitude a float64 holds exactly.
const maxExactFloat64Int = 1 << 53

// NewAmount returns the Amount for v.
func NewAmount(v int64) Amount {
	return Amount{small: v}
}

// fromBig returns the Amount for v, holding it in an int64 when it fits so
// that equal values are represented the same way.
//
// It takes ownership of v. A caller must not retain or mutate v afterwards,
// because a value that does not fit an int64 is kept by pointer and the whole
// immutability argument for sharing Amounts between a snapshot and the cache
// rests on nothing else holding a writable reference to it.
func fromBig(v *big.Int) Amount {
	if v.IsInt64() {
		return Amount{small: v.Int64()}
	}
	return Amount{large: v}
}

// big returns a as a *big.Int the caller may not modify.
func (a Amount) big() *big.Int {
	if a.large != nil {
		return a.large
	}
	return big.NewInt(a.small)
}

// AmountFromQuantity converts one API Quantity into the unit the resource is
// accounted in, milli for CPU. A magnitude past what a Quantity carries is
// capped at the same ceiling AmountQuantity reports back, so the boundary is
// reached in one pass rather than moving each time a value crosses it: the
// parser caps the binary spelling there and not the decimal one, and taking
// either at face value would let the same quota differ by the suffix it was
// written with. It reads the decimal the Quantity holds rather than Value or
// MilliValue, which document that they may overflow: at the smallest int64
// milliCPU, MilliValue returns zero.
//
// All quota-side conversion (Nominal, BorrowingLimit, LendingLimit) goes
// through here. ResourceValue is the equivalent for workload requests, which
// clamp to the int64 range instead.
func AmountFromQuantity(name corev1.ResourceName, q resource.Quantity) Amount {
	return fromBig(scaledBig(name, q))
}

// scaledBig returns the quantity in the unit the resource is accounted in,
// rounding away from zero the way Quantity.Value and MilliValue do.
func scaledBig(name corev1.ResourceName, q resource.Quantity) *big.Int {
	d := q.AsDec()
	unscaled := new(big.Int).Set(d.UnscaledBig())
	if unscaled.Sign() == 0 {
		return unscaled
	}
	// Bounded in the unit the API reports, which for CPU is cores, before the
	// conversion into the unit it is accounted in. MaxInt64 cores is a legal
	// Quantity whose milli value is not a legal one, and that is the case the
	// exact amount exists for.
	if exceedsQuantity(unscaled, -int64(d.Scale())) {
		return quantityCapAmount(name, unscaled.Sign())
	}
	exp := -int64(d.Scale())
	if name == corev1.ResourceCPU {
		exp += 3
	}
	if scaledIsBelowOne(unscaled, exp) {
		// Rounds away from zero, and the divisor below can be too large to build.
		return big.NewInt(int64(unscaled.Sign()))
	}
	if exp >= 0 {
		return unscaled.Mul(unscaled, new(big.Int).Exp(ten, big.NewInt(exp), nil))
	}
	quo, rem := new(big.Int).QuoRem(unscaled, new(big.Int).Exp(ten, big.NewInt(-exp), nil), new(big.Int))
	switch rem.Sign() {
	case 1:
		quo.Add(quo, one)
	case -1:
		quo.Sub(quo, one)
	}
	return quo
}

// Add returns a + b.
func (a Amount) Add(b Amount) Amount {
	if a.large == nil && b.large == nil {
		if sum, ok := addInt64(a.small, b.small); ok {
			return Amount{small: sum}
		}
	}
	return fromBig(new(big.Int).Add(a.big(), b.big()))
}

// AddInt64 returns a + v.
func (a Amount) AddInt64(v int64) Amount {
	return a.Add(Amount{small: v})
}

// Sub returns a - b.
func (a Amount) Sub(b Amount) Amount {
	if a.large == nil && b.large == nil {
		if diff, ok := subInt64(a.small, b.small); ok {
			return Amount{small: diff}
		}
	}
	return fromBig(new(big.Int).Sub(a.big(), b.big()))
}

// SubInt64 returns a - v.
func (a Amount) SubInt64(v int64) Amount {
	return a.Sub(Amount{small: v})
}

// addInt64 returns x + y, and false when the sum leaves the int64 range.
func addInt64(x, y int64) (int64, bool) {
	sum := x + y
	if (x > 0 && y > 0 && sum < 0) || (x < 0 && y < 0 && sum >= 0) {
		return 0, false
	}
	return sum, true
}

// subInt64 returns x - y, and false when the difference leaves the int64 range.
func subInt64(x, y int64) (int64, bool) {
	if y == math.MinInt64 {
		return 0, false
	}
	return addInt64(x, -y)
}

// Cmp returns -1 / 0 / +1 like bytes.Compare.
func (a Amount) Cmp(b Amount) int {
	if a.large == nil && b.large == nil {
		switch {
		case a.small < b.small:
			return -1
		case a.small > b.small:
			return 1
		default:
			return 0
		}
	}
	// Only a value outside the int64 range is held in large, so against one
	// held in small its sign is already the answer.
	switch {
	case b.large == nil:
		return a.large.Sign()
	case a.large == nil:
		return -b.large.Sign()
	default:
		return a.large.Cmp(b.large)
	}
}

// CmpInt64 returns -1 / 0 / +1 for a against v.
func (a Amount) CmpInt64(v int64) int {
	return a.Cmp(Amount{small: v})
}

// Sign returns -1, 0 or +1 for a negative, zero or positive amount.
func (a Amount) Sign() int {
	if a.large != nil {
		return a.large.Sign()
	}
	switch {
	case a.small < 0:
		return -1
	case a.small > 0:
		return 1
	default:
		return 0
	}
}

// AsInt64 returns a as an int64, and false when it does not fit one.
func (a Amount) AsInt64() (int64, bool) {
	if a.large != nil {
		return 0, false
	}
	return a.small, true
}

// AsSaturatedInt64 returns a clamped to the int64 range. Only the boundaries
// that have no way to report a refusal should use it.
func (a Amount) AsSaturatedInt64() int64 {
	if a.large == nil {
		return a.small
	}
	if a.large.Sign() > 0 {
		return math.MaxInt64
	}
	return math.MinInt64
}

// AsApproximateFloat64 returns the amount in the resource's standard unit,
// converting milliCPU to CPU. A magnitude past float64 becomes an infinity,
// which is what the metrics boundary reports for it.
func (a Amount) AsApproximateFloat64(name corev1.ResourceName) float64 {
	if a.large == nil {
		f := float64(a.small)
		if name == corev1.ResourceCPU {
			return f / 1000
		}
		return f
	}
	// Dividing after the float conversion would answer +Inf for a milliCPU
	// magnitude past float64 whose value in cores is not.
	r := new(big.Rat).SetInt(a.large)
	if name == corev1.ResourceCPU {
		r.Quo(r, thousand)
	}
	f, _ := r.Float64()
	return f
}

// PerThousandOf returns a over b in thousandths, as a float64. The division is
// taken on the exact values and only the result is approximated, so operands
// past the range float64 holds exactly do not move the answer. A ratio too
// small for float64 comes back as the smallest positive value rather than as
// zero, which a caller reads as no borrowing at all.
func (a Amount) PerThousandOf(b Amount) float64 {
	if b.Sign() == 0 {
		return 0
	}
	// Both operands are exact in float64 and the scale is applied without
	// leaving that range, so the IEEE division is already the correctly
	// rounded result that the exact ratio converts to.
	if a.large == nil && b.large == nil &&
		absAtMost(a.small, maxExactFloat64Int/1000) && absAtMost(b.small, maxExactFloat64Int) {
		return float64(a.small*1000) / float64(b.small)
	}
	r := new(big.Rat).SetFrac(a.big(), b.big())
	r.Mul(r, thousand)
	f, _ := r.Float64()
	if f == 0 && r.Sign() > 0 {
		return math.SmallestNonzeroFloat64
	}
	return f
}

// exceedsQuantity reports whether the magnitude of unscaled x 10^exp is past
// what a Quantity holds. It answers from the digit count wherever it can: a
// short exponent spelling reaches a scale of two billion, and building that
// power of ten to compare is the allocation the bound exists to prevent.
func exceedsQuantity(unscaled *big.Int, exp int64) bool {
	abs := new(big.Int).Abs(unscaled)
	switch digits := int64(len(abs.String())); {
	case digits+exp < maxQuantityDigits:
		return false
	case digits+exp > maxQuantityDigits:
		return true
	case exp >= 0:
		// Within one power of ten of the ceiling, so the shorter side is scaled
		// up to meet the other. Either power is at most as long as the mantissa,
		// and scaling the ceiling up rather than the mantissa down keeps the
		// fraction that decides the comparison.
		abs.Mul(abs, new(big.Int).Exp(ten, big.NewInt(exp), nil))
		return abs.Cmp(maxQuantityInt) > 0
	default:
		limit := new(big.Int).Mul(maxQuantityInt, new(big.Int).Exp(ten, big.NewInt(-exp), nil))
		return abs.Cmp(limit) > 0
	}
}

// scaledIsBelowOne reports whether the magnitude of unscaled x 10^exp is under
// one, which is the other end of the same scale: the divisor would be the power
// of ten the check above refuses to build.
func scaledIsBelowOne(unscaled *big.Int, exp int64) bool {
	return exp < 0 && -exp >= int64(len(new(big.Int).Abs(unscaled).String()))
}

// quantityCapAmount is the largest amount one Quantity expresses, with sign, in
// the unit the resource is accounted in.
func quantityCapAmount(name corev1.ResourceName, sign int) *big.Int {
	limit := maxQuantityInt
	if name == corev1.ResourceCPU {
		limit = maxCPUAmount
	}
	capped := new(big.Int).Set(limit)
	if sign < 0 {
		capped.Neg(capped)
	}
	return capped
}

// absAtMost reports whether the magnitude of v is at most limit. MinInt64
// cannot be negated, so both ends are compared rather than an absolute taken.
func absAtMost(v, limit int64) bool {
	return v <= limit && v >= -limit
}

// milliDec returns a as a decimal at milli scale, and false when its magnitude
// is past what a Quantity carries. Only the CPU boundary uses it, where the
// amount is held in milli. inf copies the value it is handed, so passing the
// stored pointer does not expose it.
func (a Amount) milliDec() (*inf.Dec, bool) {
	v := a.big()
	if v.CmpAbs(maxCPUAmount) > 0 {
		return nil, false
	}
	return inf.NewDecBig(v, 3), true
}

// MinAmount returns the smaller of a and b.
func MinAmount(a, b Amount) Amount {
	if a.Cmp(b) < 0 {
		return a
	}
	return b
}

// MaxAmount returns the larger of a and b.
func MaxAmount(a, b Amount) Amount {
	if a.Cmp(b) > 0 {
		return a
	}
	return b
}

// String formats the Amount in the unit it is accounted in.
func (a Amount) String() string {
	if a.large == nil {
		return strconv.FormatInt(a.small, 10)
	}
	return a.large.String()
}

// Equal reports whether a and b are the same number. go-cmp reaches this
// rather than comparing the unexported fields, which would answer for the
// pointer in large.
func (a Amount) Equal(b Amount) bool {
	return a.Cmp(b) == 0
}
