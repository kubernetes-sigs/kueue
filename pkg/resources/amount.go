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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Amount is an exact integer quota amount, in the unit the resource is
// accounted in: milliCPU for cpu, whole units for everything else.
//
// Values inside the int64 range are held in small and cost no allocation.
// A result that leaves that range is held in large instead, and one that
// comes back inside it is held in small again, so two Amounts that are
// numerically equal have the same representation and == would answer for
// them. Use Equal or Cmp anyway: large is a pointer, and == on it compares
// identity rather than value.
//
// A stored *big.Int is never mutated and never handed out. big.Int does not
// support shallow copies, while resourceNode.Clone and
// FlavorResourceQuantities.Clone copy their maps shallowly, so a snapshot and
// the cache it came from share these pointers. Treating them as immutable is
// what makes that sharing safe.
type Amount struct {
	small int64
	large *big.Int
}

var (
	one = big.NewInt(1)
	ten = big.NewInt(10)
)

// NewAmount returns the Amount for v.
func NewAmount(v int64) Amount {
	return Amount{small: v}
}

// fromBig returns the Amount for v, holding it in an int64 when it fits so
// that equal values are represented the same way.
func fromBig(v *big.Int) Amount {
	if v.IsInt64() {
		return Amount{small: v.Int64()}
	}
	return Amount{large: v}
}

// AsBig returns a as a *big.Int. The result aliases the stored value, which is
// never mutated, so callers must not modify it either.
func (a Amount) AsBig() *big.Int { return a.big() }

// big returns a as a *big.Int the caller may not modify.
func (a Amount) big() *big.Int {
	if a.large != nil {
		return a.large
	}
	return big.NewInt(a.small)
}

// AmountFromQuantity converts a resource.Quantity into the Amount for it.
//
// Quantity is arbitrary precision, so a quota past int64 arrives here intact
// and is charged as the number it is rather than becoming a sentinel. The
// conversion goes through the decimal the Quantity already holds rather than
// through Value or MilliValue, which return zero for a magnitude they cannot
// hold: MilliValue of the smallest int64 milliCPU is one of those.
//
// This is the safe constructor that all quota-side conversion (Nominal,
// BorrowingLimit, LendingLimit) must use. ResourceValue is the equivalent for
// workload requests, which clamp to the int64 range instead.
func AmountFromQuantity(name corev1.ResourceName, q resource.Quantity) Amount {
	return fromBig(scaledBig(name, q))
}

// scaledBig returns the quantity in the unit the resource is accounted in,
// rounding away from zero the way Quantity.Value and MilliValue do.
func scaledBig(name corev1.ResourceName, q resource.Quantity) *big.Int {
	d := q.AsDec()
	unscaled := new(big.Int).Set(d.UnscaledBig())
	exp := int64(-d.Scale())
	if name == corev1.ResourceCPU {
		exp += 3
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
	return a.big().Cmp(b.big())
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
	f := float64(a.small)
	if a.large != nil {
		f, _ = new(big.Float).SetInt(a.large).Float64()
	}
	if name == corev1.ResourceCPU {
		return f / 1000
	}
	return f
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
	return a.big().String()
}

// Equal reports whether a and b are the same number. go-cmp reaches this
// rather than comparing the unexported fields, which would answer for the
// pointer in large.
func (a Amount) Equal(b Amount) bool {
	return a.Cmp(b) == 0
}
