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
	"sigs.k8s.io/kueue/pkg/resources"
)

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
