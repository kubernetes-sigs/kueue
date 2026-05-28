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
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	utilmath "sigs.k8s.io/kueue/pkg/util/math"
)

// Amount is the safe representation of a quota amount used for ClusterQueue
// and Cohort math (Nominal, BorrowingLimit, LendingLimit, SubtreeQuota).
//
// It is a value type so that:
//   - "1E" CPU quotas (whose milliCPU value overflows int64) cannot collapse
//     to 0, since AmountFromQuantity returns Unlimited instead;
//   - cohort aggregation can't wrap around int64 - Add of an Unlimited yields
//     Unlimited, and bounded arithmetic saturates rather than overflowing;
//   - the type system funnels quota arithmetic through methods, so a future
//     contributor can't accidentally fall back to raw "+" / "-" on quota
//     values and silently re-introduce the overflow.
//
// Keep usage values (which are bounded by what real workloads consume) as
// int64. Mixed Amount-vs-int64 arithmetic uses the *Int64 methods.
//
// math.MaxInt64 is the sentinel for "unlimited"; bounded amounts must never
// equal math.MaxInt64 (AmountFromQuantity enforces this at the quota boundary).
type Amount struct {
	value int64
}

// Unlimited represents an effectively-infinite quota amount. It is the result
// of AmountFromQuantity for a quantity whose canonical int64 representation
// would overflow, and any Amount arithmetic involving Unlimited stays
// Unlimited.
var Unlimited = Amount{value: math.MaxInt64}

// NewAmount returns a bounded Amount with the given int64 value.
func NewAmount(v int64) Amount {
	return Amount{value: v}
}

// maxCPUQuantityForAmount is the largest CPU resource.Quantity whose value in
// milliCPU still fits in int64 (math.MaxInt64 / 1000 cores).
var maxCPUQuantityForAmount = *resource.NewQuantity(math.MaxInt64/1000, resource.DecimalSI)

// maxNonCPUQuantityForAmount is the largest non-CPU resource.Quantity whose
// absolute value fits in int64.
var maxNonCPUQuantityForAmount = *resource.NewQuantity(math.MaxInt64, resource.DecimalSI)

// AmountFromQuantity converts a resource.Quantity into an Amount, returning
// Unlimited when the canonical value would overflow int64.
//
// This is the safe constructor that all quota-side conversion (Nominal,
// BorrowingLimit, LendingLimit) must use. ResourceValue is preserved for
// non-quota call sites (workload requests) where the historical
// truncate-on-overflow behavior is what existing tests document.
func AmountFromQuantity(name corev1.ResourceName, q resource.Quantity) Amount {
	if name == corev1.ResourceCPU {
		if q.Cmp(maxCPUQuantityForAmount) >= 0 {
			return Unlimited
		}
		return Amount{value: q.MilliValue()}
	}
	if q.Cmp(maxNonCPUQuantityForAmount) >= 0 {
		return Unlimited
	}
	return Amount{value: q.Value()}
}

// isUnlimited reports whether a is the Unlimited sentinel.
func (a Amount) isUnlimited() bool { return a.value == math.MaxInt64 }

// Int64 returns the int64 representation of a. Unlimited is math.MaxInt64;
// this is only correct at consumption boundaries (metrics, formatting,
// comparisons that already saturate). Don't use Int64 inside quota arithmetic
// - use the Amount methods instead.
func (a Amount) Int64() int64 {
	return a.value
}

// Add returns a + b, propagating Unlimited and saturating bounded overflow at
// math.MaxInt64.
func (a Amount) Add(b Amount) Amount {
	if a.isUnlimited() || b.isUnlimited() {
		return Unlimited
	}
	return Amount{value: utilmath.SaturatingAdd(a.value, b.value)}
}

// AddInt64 returns a + v.
func (a Amount) AddInt64(v int64) Amount {
	if a.isUnlimited() {
		return a
	}
	return Amount{value: utilmath.SaturatingAdd(a.value, v)}
}

// Sub returns a - b. If a is Unlimited and b is bounded, the result stays
// Unlimited. If b is Unlimited and a is bounded, the result is math.MinInt64
// (callers treat any negative as "no available capacity").
// Sub(Unlimited, Unlimited) returns bounded zero.
func (a Amount) Sub(b Amount) Amount {
	if a.isUnlimited() && b.isUnlimited() {
		return Amount{}
	}
	if a.isUnlimited() {
		return Unlimited
	}
	if b.isUnlimited() {
		return Amount{value: math.MinInt64}
	}
	return Amount{value: utilmath.SaturatingSub(a.value, b.value)}
}

// SubInt64 returns a - v.
func (a Amount) SubInt64(v int64) Amount {
	if a.isUnlimited() {
		return a
	}
	return Amount{value: utilmath.SaturatingSub(a.value, v)}
}

// Cmp returns -1 / 0 / +1 like bytes.Compare. Two Unlimited values compare
// equal; Unlimited is greater than any bounded value.
func (a Amount) Cmp(b Amount) int {
	switch {
	case a.isUnlimited() && b.isUnlimited():
		return 0
	case a.isUnlimited():
		return 1
	case b.isUnlimited():
		return -1
	case a.value < b.value:
		return -1
	case a.value > b.value:
		return 1
	default:
		return 0
	}
}

// CmpInt64 returns -1 / 0 / +1 for a vs the bounded value v.
func (a Amount) CmpInt64(v int64) int {
	if a.isUnlimited() {
		return 1
	}
	switch {
	case a.value < v:
		return -1
	case a.value > v:
		return 1
	default:
		return 0
	}
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

// String formats the Amount; Unlimited is printed as "<unlimited>".
func (a Amount) String() string {
	if a.isUnlimited() {
		return "<unlimited>"
	}
	return strconv.FormatInt(a.value, 10)
}

// Equal lets go-cmp and other reflection-based comparators treat Amount as a
// comparable value type without requiring cmpopts.EquateComparable everywhere
// quotas appear in test fixtures.
func (a Amount) Equal(b Amount) bool {
	return a == b
}
