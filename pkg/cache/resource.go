package cache

import (
	"sigs.k8s.io/kueue/pkg/resources"
)

type resourceGroupNode interface {
	resourceGroups() []ResourceGroup
}

type flavorResourceQuota struct {
	fr    resources.FlavorResource
	quota *ResourceQuota
}

// flavorResourceQuotas returns all of the FlavorResource(s) defined in the given,
// node, along with their corresponding quotas.
func flavorResourceQuotas(node resourceGroupNode) (flavorResources []flavorResourceQuota) {
	for _, rg := range node.resourceGroups() {
		for _, flavor := range rg.Flavors {
			for resourceName, resource := range flavor.Resources {
				flavorResources = append(flavorResources,
					flavorResourceQuota{
						fr:    resources.FlavorResource{Flavor: flavor.Name, Resource: resourceName},
						quota: resource,
					},
				)
			}
		}
	}
	return
}

type netQuotaNode interface {
	usageFor(resources.FlavorResource) int64
	resourceGroups() []ResourceGroup
}

// remainingQuota computes the remaining quota for each FlavorResource. A
// negative value implies that the node is borrowing.
func remainingQuota(node netQuotaNode) resources.FlavorResourceQuantitiesFlat {
	remainingQuota := make(resources.FlavorResourceQuantitiesFlat)
	for _, frq := range flavorResourceQuotas(node) {
		remainingQuota[frq.fr] += frq.quota.Nominal - node.usageFor(frq.fr)
	}
	return remainingQuota
}
