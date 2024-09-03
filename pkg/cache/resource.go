/*
Copyright 2024 The Kubernetes Authors.

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

package cache

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
)

type ResourceGroup struct {
	CoveredResources sets.Set[corev1.ResourceName]
	Flavors          []kueue.ResourceFlavorReference
	// The set of key labels from all flavors.
	// Those keys define the affinity terms of a workload
	// that can be matched against the flavors.
	LabelKeys sets.Set[string]
}

func (rg *ResourceGroup) Clone() ResourceGroup {
	return ResourceGroup{
		CoveredResources: rg.CoveredResources.Clone(),
		Flavors:          rg.Flavors,
		LabelKeys:        rg.LabelKeys.Clone(),
	}
}

type ResourceQuota struct {
	Nominal        int64
	BorrowingLimit *int64
	LendingLimit   *int64
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
					Nominal: resources.ResourceValue(kueueQuota.Name, kueueQuota.NominalQuota),
				}
				if kueueQuota.BorrowingLimit != nil {
					quota.BorrowingLimit = ptr.To(resources.ResourceValue(kueueQuota.Name, *kueueQuota.BorrowingLimit))
				}
				if features.Enabled(features.LendingLimit) && kueueQuota.LendingLimit != nil {
					quota.LendingLimit = ptr.To(resources.ResourceValue(kueueQuota.Name, *kueueQuota.LendingLimit))
				}
				quotas[resources.FlavorResource{Flavor: kueueFlavor.Name, Resource: kueueQuota.Name}] = quota
			}
		}
	}
	return quotas
}

type resourceGroupNode interface {
	resourceGroups() []ResourceGroup
}

func flavorResourceCount(rgs []ResourceGroup) int {
	count := 0
	for _, rg := range rgs {
		count += len(rg.Flavors) * len(rg.CoveredResources)
	}
	return count
}

func flavorResources(r resourceGroupNode) []resources.FlavorResource {
	frs := make([]resources.FlavorResource, 0, flavorResourceCount(r.resourceGroups()))
	for _, rg := range r.resourceGroups() {
		for _, f := range rg.Flavors {
			for r := range rg.CoveredResources {
				frs = append(frs, resources.FlavorResource{Flavor: f, Resource: r})
			}
		}
	}

	return frs
}

type netQuotaNode interface {
	usageFor(resources.FlavorResource) int64
	QuotaFor(resources.FlavorResource) ResourceQuota
	resourceGroups() []ResourceGroup
}

// remainingQuota computes the remaining quota for each FlavorResource. A
// negative value implies that the node is borrowing.
func remainingQuota(node netQuotaNode) resources.FlavorResourceQuantities {
	remainingQuota := make(resources.FlavorResourceQuantities)
	for _, fr := range flavorResources(node) {
		remainingQuota[fr] += node.QuotaFor(fr).Nominal - node.usageFor(fr)
	}
	return remainingQuota
}
