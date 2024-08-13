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
	QuotaFor(resources.FlavorResource) *ResourceQuota
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
