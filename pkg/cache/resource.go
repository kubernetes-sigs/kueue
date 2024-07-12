package cache

import (
	"sigs.k8s.io/kueue/pkg/resources"
)

type resourceGroupNode interface {
	resourceGroups() []ResourceGroup
}

func flavorResources(r resourceGroupNode) (frs []resources.FlavorResource) {
	for _, rg := range r.resourceGroups() {
		frs = append(frs, rg.FlavorResources()...)
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
func remainingQuota(node netQuotaNode) resources.FlavorResourceQuantitiesFlat {
	remainingQuota := make(resources.FlavorResourceQuantitiesFlat)
	for _, fr := range flavorResources(node) {
		remainingQuota[fr] += node.QuotaFor(fr).Nominal - node.usageFor(fr)
	}
	return remainingQuota
}
