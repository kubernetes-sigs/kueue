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
