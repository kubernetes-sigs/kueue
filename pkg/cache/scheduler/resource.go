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
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/resources"
)

type ResourceQuota struct {
	Nominal        resources.Amount
	BorrowingLimit *resources.Amount
	LendingLimit   *resources.Amount
}

// Equal reports whether two ResourceQuota values are equal, using
// resources.Amount semantics for the Nominal/BorrowingLimit/LendingLimit
// fields. This is preferred over k8s equality.Semantic.DeepEqual, which
// uses a forked reflect that panics on structs with unexported fields
// from another package (see resources.Amount).
func (q ResourceQuota) Equal(other ResourceQuota) bool {
	if !q.Nominal.Equal(other.Nominal) {
		return false
	}
	if !amountPtrEqual(q.BorrowingLimit, other.BorrowingLimit) {
		return false
	}
	return amountPtrEqual(q.LendingLimit, other.LendingLimit)
}

// amountPtrEqual compares the amounts rather than the pointers. ptr.Equal
// dereferences and uses ==, which for an Amount holding a *big.Int answers for
// the pointer, so two limits of the same size would read as different.
func amountPtrEqual(a, b *resources.Amount) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func createResourceQuotas(kueueRgs []kueue.ResourceGroup) map[resources.FlavorResource]ResourceQuota {
	frCount := 0
	for _, rg := range kueueRgs {
		frCount += len(rg.Flavors) * len(rg.CoveredResources)
	}
	quotas := make(map[resources.FlavorResource]ResourceQuota, frCount)
	for _, kueueRg := range kueueRgs {
		for _, kueueFlavor := range kueueRg.Flavors {
			for _, kueueQuota := range kueueFlavor.Resources {
				quota := ResourceQuota{
					Nominal: resources.AmountFromQuantity(kueueQuota.Name, kueueQuota.NominalQuota),
				}
				if kueueQuota.BorrowingLimit != nil {
					quota.BorrowingLimit = new(resources.AmountFromQuantity(kueueQuota.Name, *kueueQuota.BorrowingLimit))
				}
				if kueueQuota.LendingLimit != nil {
					quota.LendingLimit = new(resources.AmountFromQuantity(kueueQuota.Name, *kueueQuota.LendingLimit))
				}
				quotas[resources.FlavorResource{Flavor: kueueFlavor.Name, Resource: kueueQuota.Name}] = quota
			}
		}
	}
	return quotas
}
