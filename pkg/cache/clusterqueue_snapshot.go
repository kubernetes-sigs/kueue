package cache

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/workload"
)

type ClusterQueueSnapshot struct {
	Name              string
	Cohort            *CohortSnapshot
	ResourceGroups    []ResourceGroup
	Usage             resources.FlavorResourceQuantities
	Workloads         map[string]*workload.Info
	WorkloadsNotReady sets.Set[string]
	NamespaceSelector labels.Selector
	Preemption        kueue.ClusterQueuePreemption
	FairWeight        resource.Quantity
	FlavorFungibility kueue.FlavorFungibility
	// Aggregates AdmissionChecks from both .spec.AdmissionChecks and .spec.AdmissionCheckStrategy
	// Sets hold ResourceFlavors to which an AdmissionCheck should apply.
	// In case its empty, it means an AdmissionCheck should apply to all ResourceFlavor
	AdmissionChecks map[string]sets.Set[kueue.ResourceFlavorReference]
	Status          metrics.ClusterQueueStatus
	// GuaranteedQuota records how much resource quota the ClusterQueue reserved
	// when feature LendingLimit is enabled and flavor's lendingLimit is not nil.
	GuaranteedQuota resources.FlavorResourceQuantities
	// AllocatableResourceGeneration will be increased when some admitted workloads are
	// deleted, or the resource groups are changed.
	AllocatableResourceGeneration int64

	// Lendable holds the total lendable quota for the resources of the ClusterQueue, independent of the flavor.
	Lendable map[corev1.ResourceName]int64
}

// RGByResource returns the ResourceGroup which contains capacity
// for the resource, or nil if the CQ doesn't provide this resource.
func (c *ClusterQueueSnapshot) RGByResource(resource corev1.ResourceName) *ResourceGroup {
	for i := range c.ResourceGroups {
		if c.ResourceGroups[i].CoveredResources.Has(resource) {
			return &c.ResourceGroups[i]
		}
	}
	return nil
}

// The methods below implement several interfaces. See
// dominantResourceShareNode, resourceGroupNode, and netQuotaNode.

func (c *ClusterQueueSnapshot) hasCohort() bool {
	return c.Cohort != nil
}
func (c *ClusterQueueSnapshot) fairWeight() *resource.Quantity {
	return &c.FairWeight
}
func (c *ClusterQueueSnapshot) lendableResourcesInCohort() map[corev1.ResourceName]int64 {
	return c.Cohort.Lendable
}

func (c *ClusterQueueSnapshot) usageFor(fr resources.FlavorResource) int64 {
	return c.Usage.For(fr)
}

func (c *ClusterQueueSnapshot) resourceGroups() []ResourceGroup {
	return c.ResourceGroups
}
