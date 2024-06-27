package cache

import (
	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/kueue/pkg/resources"
)

type resourceGroupNode interface {
	getResourceGroups() *[]ResourceGroup
}

// resourceGroupForResource returns the ResourceGroup which contains capacity
// for the resource, or nil if the CQ doesn't provide this resource.
func resourceGroupForResource(node resourceGroupNode, resource corev1.ResourceName) *ResourceGroup {
	resourceGroups := node.getResourceGroups()
	for i := range *resourceGroups {
		if (*resourceGroups)[i].CoveredResources.Has(resource) {
			return &(*resourceGroups)[i]
		}
	}
	return nil
}

// flavorResources returns all of the FlavorResource(s) defined in the given node.
// note: this is copy heavy, but we don't care - the structure of ResourceGroup will change
// soon, which will have these values precomputed (or cheap to access).
func flavorResources(node resourceGroupNode) []resources.FlavorResource {
	flavorResources := make([]resources.FlavorResource, 0)
	for _, rg := range *node.getResourceGroups() {
		for _, flavor := range rg.Flavors {
			for resource := range flavor.Resources {
				flavorResources = append(flavorResources, resources.FlavorResource{Flavor: flavor.Name, Resource: resource})
			}
		}
	}
	return flavorResources
}

// resourceQuota returns the ResourceQuota for the given FlavorResource, or nil
// if the CQ doesn't provide the FlavorResource.
func resourceQuota(node resourceGroupNode, fr resources.FlavorResource) *ResourceQuota {
	resourceGroup := resourceGroupForResource(node, fr.Resource)
	if resourceGroup == nil {
		return nil
	}
	for _, flavor := range resourceGroup.Flavors {
		if flavor.Name == fr.Flavor {
			return flavor.Resources[fr.Resource]
		}
	}
	return nil
}

// nominalCapacity returns the nominal capacity for the given (CQ, FlavorResource).
// if the FlavorResource isn't provided by CQ, 0 is returned.
func nominalCapacity(node resourceGroupNode, fr resources.FlavorResource) int64 {
	quota := resourceQuota(node, fr)
	if quota == nil {
		return 0
	}
	return quota.Nominal
}

type netQuotaNode interface {
	usage(resources.FlavorResource) int64
	getResourceGroups() *[]ResourceGroup
}

// getNetQuota computes the remaining quota for each FlavorResource. A
// negative value implies that the node is borrowing.
func getNetQuota(node netQuotaNode) resources.FlavorResourceQuantitiesFlat {
	netQuota := make(resources.FlavorResourceQuantitiesFlat)
	for _, fr := range flavorResources(node) {
		netQuota[fr] += nominalCapacity(node, fr) - node.usage(fr)
	}
	return netQuota
}
