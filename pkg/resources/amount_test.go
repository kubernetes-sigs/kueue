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
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// TestAmountFromQuantity exercises the safe quota-construction path that
// replaced raw ResourceValue for ClusterQueue/Cohort quotas. Compared with
// TestResourceValueDecimalExponent (which documents the unsafe legacy
// behavior) this test pins the saturating semantics that fix issue #9843:
//
//   - "1E" CPU no longer collapses to 0; it returns Unlimited so the
//     scheduler treats the quota as effectively unbounded instead of zero.
//   - Normal-sized quotas keep their exact pre-existing values, including
//     the milliCPU scale.
//   - Non-CPU "1E" still fits in int64 and is returned as a bounded Amount.
//   - A quantity past the int64 ceiling for the resource returns Unlimited
//     rather than wrapping or rounding to zero.
func TestAmountFromQuantity(t *testing.T) {
	cases := map[string]struct {
		resource      corev1.ResourceName
		quantity      string
		wantUnlimited bool
		wantValue     int64
	}{
		"cpu small value preserved": {
			resource:  corev1.ResourceCPU,
			quantity:  "2",
			wantValue: 2_000,
		},
		"cpu sub-cpu preserved": {
			resource:  corev1.ResourceCPU,
			quantity:  "500m",
			wantValue: 500,
		},
		"cpu 1P preserved (no overflow)": {
			resource:  corev1.ResourceCPU,
			quantity:  "1P",
			wantValue: 1_000_000_000_000_000_000,
		},
		"cpu 1E becomes Unlimited (was 0 with raw ResourceValue)": {
			resource:      corev1.ResourceCPU,
			quantity:      "1E",
			wantUnlimited: true,
		},
		"cpu 10P becomes Unlimited": {
			resource:      corev1.ResourceCPU,
			quantity:      "10P",
			wantUnlimited: true,
		},
		"memory small value preserved": {
			resource:  corev1.ResourceMemory,
			quantity:  "512Mi",
			wantValue: 512 * 1024 * 1024,
		},
		"memory 1E preserved (fits in int64)": {
			resource:  corev1.ResourceMemory,
			quantity:  "1E",
			wantValue: 1_000_000_000_000_000_000,
		},
		"memory past int64 becomes Unlimited": {
			resource:      corev1.ResourceMemory,
			quantity:      "10E",
			wantUnlimited: true,
		},
		"extended resource preserved": {
			resource:  corev1.ResourceName("example.com/gpu"),
			quantity:  "8",
			wantValue: 8,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			q := resource.MustParse(tc.quantity)
			got := AmountFromQuantity(tc.resource, q)
			if got.isUnlimited() != tc.wantUnlimited {
				t.Fatalf("AmountFromQuantity(%s, %s).IsUnlimited() = %v, want %v",
					tc.resource, tc.quantity, got.isUnlimited(), tc.wantUnlimited)
			}
			if !tc.wantUnlimited && got.Int64() != tc.wantValue {
				t.Errorf("AmountFromQuantity(%s, %s) = %d, want %d",
					tc.resource, tc.quantity, got.Int64(), tc.wantValue)
			}
		})
	}
}

// TestAmountArithmetic pins the unlimited-propagation and saturation rules
// that prevent cohort math from overflowing. Quota arithmetic must funnel
// through Amount rather than raw int64 so these invariants hold globally.
func TestAmountArithmetic(t *testing.T) {
	testCases := map[string]struct {
		run func(t *testing.T)
	}{
		"Add propagates Unlimited": {
			run: func(t *testing.T) {
				got := NewAmount(5).Add(Unlimited)
				if !got.isUnlimited() {
					t.Errorf("bounded + Unlimited should be Unlimited, got %v", got)
				}
			},
		},
		"Add saturates on bounded overflow becomes Unlimited": {
			run: func(t *testing.T) {
				got := NewAmount(math.MaxInt64 - 5).Add(NewAmount(100))
				if !got.isUnlimited() {
					t.Errorf("bounded saturated overflow should become Unlimited (MaxInt64 sentinel), got %v", got)
				}
			},
		},
		"Sub of Unlimited stays Unlimited": {
			run: func(t *testing.T) {
				got := Unlimited.SubInt64(1000)
				if !got.isUnlimited() {
					t.Errorf("Unlimited - bounded should stay Unlimited, got %v", got)
				}
			},
		},
		"Cmp - Unlimited greater than any bounded": {
			run: func(t *testing.T) {
				// MaxInt64 is the Unlimited sentinel; use MaxInt64-1 for a bounded value.
				if Unlimited.CmpInt64(math.MaxInt64-1) <= 0 {
					t.Errorf("Unlimited should compare greater than MaxInt64-1")
				}
				if NewAmount(math.MaxInt64-1).Cmp(Unlimited) >= 0 {
					t.Errorf("MaxInt64-1 should compare less than Unlimited")
				}
			},
		},
		"Cmp - two Unlimited equal": {
			run: func(t *testing.T) {
				left, right := Unlimited, Unlimited
				if got := left.Cmp(right); got != 0 {
					t.Errorf("Unlimited.Cmp(Unlimited) = %d, want 0", got)
				}
			},
		},
		"Sub Unlimited minus Unlimited yields zero": {
			run: func(t *testing.T) {
				// Intentional edge case: Unlimited - Unlimited = 0.
				// This arises in localQuota when SubtreeQuota == LendingLimit == Unlimited,
				// meaning the node lends everything and retains nothing locally.
				got := Unlimited.Sub(Unlimited)
				if got.isUnlimited() {
					t.Errorf("Unlimited.Sub(Unlimited) should be bounded zero, got Unlimited")
				}
				if got.Int64() != 0 {
					t.Errorf("Unlimited.Sub(Unlimited) = %d, want 0", got.Int64())
				}
			},
		},
		"Sub bounded minus Unlimited yields MinInt64": {
			run: func(t *testing.T) {
				got := NewAmount(1000).Sub(Unlimited)
				if got.Int64() != math.MinInt64 {
					t.Errorf("bounded.Sub(Unlimited) = %d, want MinInt64", got.Int64())
				}
			},
		},
		"AddInt64 saturates": {
			run: func(t *testing.T) {
				got := NewAmount(math.MaxInt64 - 1).AddInt64(100)
				if got.Int64() != math.MaxInt64 {
					t.Errorf("got %d, want MaxInt64", got.Int64())
				}
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, tc.run)
	}
}
