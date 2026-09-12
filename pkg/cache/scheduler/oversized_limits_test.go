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

package scheduler

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

var cpuFR = resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}

func cpuQuota(t *testing.T, nominal string, lending, borrowing *string) ResourceQuota {
	t.Helper()
	q := ResourceQuota{Nominal: resources.AmountFromQuantity(corev1.ResourceCPU, resource.MustParse(nominal))}
	if lending != nil {
		q.LendingLimit = new(resources.AmountFromQuantity(corev1.ResourceCPU, resource.MustParse(*lending)))
	}
	if borrowing != nil {
		q.BorrowingLimit = new(resources.AmountFromQuantity(corev1.ResourceCPU, resource.MustParse(*borrowing)))
	}
	return q
}

func nodeWith(t *testing.T, quota ResourceQuota) resourceNode {
	t.Helper()
	n := NewResourceNode()
	n.Quotas[cpuFR] = quota
	n.SubtreeQuota[cpuFR] = quota.Nominal
	return n
}

// A quota and a lending limit that both left the int64 range used to become the
// same unlimited value, so localQuota was their difference and came out zero:
// the scheduler read the ClusterQueue as keeping nothing back. With exact
// amounts the two are the numbers the administrator wrote, and the difference
// between them is what stays local.
func TestLocalQuotaWithOversizedLimits(t *testing.T) {
	cases := map[string]struct {
		nominal string
		lending *string
		want    string
	}{
		"a lending limit past int64 keeps the difference local": {
			nominal: "1E", lending: new("10P"), want: "990P",
		},
		"lending everything keeps nothing local": {
			nominal: "1E", lending: new("1E"), want: "0",
		},
		"an ordinary pair is unchanged": {
			nominal: "10", lending: new("4"), want: "6",
		},
		"no lending limit keeps nothing local": {
			nominal: "1E", lending: nil, want: "0",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			n := nodeWith(t, cpuQuota(t, tc.nominal, tc.lending, nil))
			want := resources.AmountFromQuantity(corev1.ResourceCPU, resource.MustParse(tc.want))
			if got := n.localQuota(cpuFR); !got.Equal(want) {
				t.Errorf("localQuota() = %s, want %s", got, want)
			}
		})
	}
}

// An absent limit is unbounded, and stays that way. Only a limit the
// administrator wrote is a number.
func TestAbsentLimitsStayUnbounded(t *testing.T) {
	q := cpuQuota(t, "1E", nil, nil)
	if q.LendingLimit != nil {
		t.Error("an absent lending limit became a value")
	}
	if q.BorrowingLimit != nil {
		t.Error("an absent borrowing limit became a value")
	}
	// Two quotas with the same numbers and no limits are equal, which they were
	// not while equality compared the pointer inside a large Amount.
	if !q.Equal(cpuQuota(t, "1E", nil, nil)) {
		t.Error("two identical oversized quotas compared unequal")
	}
	withLimit := cpuQuota(t, "1E", new("10P"), nil)
	if !withLimit.Equal(cpuQuota(t, "1E", new("10P"), nil)) {
		t.Error("two identical oversized lending limits compared unequal")
	}
	if withLimit.Equal(cpuQuota(t, "1E", new("11P"), nil)) {
		t.Error("two different lending limits compared equal")
	}
}

// A borrowing limit past int64 used to become the same unlimited value as the
// capacity its Cohort had spare, so the ClusterQueue could take all of it. With
// exact amounts the limit is the number the administrator wrote, and it binds
// while the Cohort has more to give.
//
// The lender holds 1E of CPU, which is 10^21 milli, and the borrower may take
// 10P of it, which is 10^19 milli. Both are past int64 in milli.
func TestOversizedBorrowingLimitBounds(t *testing.T) {
	const (
		lendable   = "1000000000000000000000" // 1E cpu in milli, 10^21
		borrowable = "10000000000000000000"   // 10P cpu in milli, 10^19
		oneCPU     = 1_000
	)

	cases := map[string]struct {
		borrowingLimit  *string
		wantPotential   string
		wantAvailable   string
		wantAfterOneCPU string
	}{
		"a borrowing limit past int64 bounds what the Cohort offers": {
			borrowingLimit:  new("10P"),
			wantPotential:   borrowable,
			wantAvailable:   borrowable,
			wantAfterOneCPU: "9999999999999999000",
		},
		"no borrowing limit leaves the whole Cohort reachable": {
			borrowingLimit:  nil,
			wantPotential:   lendable,
			wantAvailable:   lendable,
			wantAfterOneCPU: "999999999999999999000",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			cache := New(utiltesting.NewFakeClient())
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
			if err := cache.AddOrUpdateCohort(utiltestingapi.MakeCohort("cohort").Obj()); err != nil {
				t.Fatalf("AddOrUpdateCohort() = %v", err)
			}

			lender := utiltestingapi.MakeClusterQueue("lender").
				Cohort("cohort").
				NamespaceSelector(nil).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					ResourceQuotaWrapper("cpu").NominalQuota("1E").Append().Obj()).
				Obj()
			if err := cache.AddClusterQueue(ctx, lender); err != nil {
				t.Fatalf("AddClusterQueue(lender) = %v", err)
			}

			quota := utiltestingapi.MakeFlavorQuotas("default").ResourceQuotaWrapper("cpu").NominalQuota("0")
			if tc.borrowingLimit != nil {
				quota = quota.BorrowingLimit(*tc.borrowingLimit)
			}
			borrower := utiltestingapi.MakeClusterQueue("borrower").
				Cohort("cohort").
				NamespaceSelector(nil).
				ResourceGroup(*quota.Append().Obj()).
				Obj()
			if err := cache.AddClusterQueue(ctx, borrower); err != nil {
				t.Fatalf("AddClusterQueue(borrower) = %v", err)
			}

			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("Snapshot() = %v", err)
			}
			cq := snapshot.ClusterQueue("borrower")
			if got := cq.PotentialAvailable(cpuFR).String(); got != tc.wantPotential {
				t.Errorf("PotentialAvailable() = %s, want %s", got, tc.wantPotential)
			}
			if got := cq.Available(cpuFR).String(); got != tc.wantAvailable {
				t.Errorf("Available() = %s, want %s", got, tc.wantAvailable)
			}

			cq.AddUsage(workload.Usage{Quota: workload.ResourceUsage{
				Assigned: resources.FlavorResourceQuantities{cpuFR: resources.NewAmount(oneCPU)},
			}})
			if got := cq.Available(cpuFR).String(); got != tc.wantAfterOneCPU {
				t.Errorf("Available() after one CPU = %s, want %s", got, tc.wantAfterOneCPU)
			}
		})
	}
}
