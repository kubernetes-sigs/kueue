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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/resources"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
)

type ResourceGroup struct {
	CoveredResources sets.Set[corev1.ResourceName]
	Flavors          []kueue.ResourceFlavorReference
}

func (rg *ResourceGroup) Clone() ResourceGroup {
	return ResourceGroup{
		CoveredResources: rg.CoveredResources.Clone(),
		Flavors:          rg.Flavors,
	}
}

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
	if !ptr.Equal(q.BorrowingLimit, other.BorrowingLimit) {
		return false
	}
	return ptr.Equal(q.LendingLimit, other.LendingLimit)
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

func AllFlavors(rgs []ResourceGroup) sets.Set[kueue.ResourceFlavorReference] {
	return utilslices.Reduce(
		rgs,
		func(acc sets.Set[kueue.ResourceFlavorReference], rg ResourceGroup) sets.Set[kueue.ResourceFlavorReference] {
			return acc.Insert(rg.Flavors...)
		},
		sets.New[kueue.ResourceFlavorReference](),
	)
}
