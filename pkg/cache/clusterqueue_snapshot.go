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
	Quotas          map[resources.FlavorResource]*ResourceQuota
	// GuaranteedQuota records how much resource quota the ClusterQueue reserved
	// when feature LendingLimit is enabled and flavor's lendingLimit is not nil.
	GuaranteedQuota resources.FlavorResourceQuantities
	// AllocatableResourceGeneration will be increased when some admitted workloads are
	// deleted, or the resource groups are changed.
	AllocatableResourceGeneration int64
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

func (c *ClusterQueueSnapshot) AddUsage(frq resources.FlavorResourceQuantities) {
	c.addOrRemoveUsage(frq, 1)
}

func (c *ClusterQueueSnapshot) Fits(frq resources.FlavorResourceQuantities) bool {
	for fr, q := range frq {
		if c.Available(fr) < q {
			return false
		}
	}
	return true
}

func (c *ClusterQueueSnapshot) QuotaFor(fr resources.FlavorResource) *ResourceQuota {
	return c.Quotas[fr]
}

func (c *ClusterQueueSnapshot) Borrowing(fr resources.FlavorResource) bool {
	return c.BorrowingWith(fr, 0)
}

func (c *ClusterQueueSnapshot) BorrowingWith(fr resources.FlavorResource, val int64) bool {
	return c.Usage[fr]+val > c.nominal(fr)
}

func (c *ClusterQueueSnapshot) Available(fr resources.FlavorResource) int64 {
	if c.Cohort == nil {
		return max(0, c.nominal(fr)-c.Usage[fr])
	}
	capacityAvailable := c.RequestableCohortQuota(fr) - c.UsedCohortQuota(fr)

	// if the borrowing limit exists, we cap our available capacity by the borrowing limit.
	if borrowingLimit := c.borrowingLimit(fr); borrowingLimit != nil {
		withBorrowingRemaining := c.nominal(fr) + *borrowingLimit - c.Usage[fr]
		capacityAvailable = min(capacityAvailable, withBorrowingRemaining)
	}
	return max(0, capacityAvailable)
}

func (c *ClusterQueueSnapshot) nominal(fr resources.FlavorResource) int64 {
	if quota := c.QuotaFor(fr); quota != nil {
		return quota.Nominal
	}
	return 0
}

func (c *ClusterQueueSnapshot) borrowingLimit(fr resources.FlavorResource) *int64 {
	if quota := c.QuotaFor(fr); quota != nil {
		return quota.BorrowingLimit
	}
	return nil
}

// The methods below implement several interfaces. See
// dominantResourceShareNode, resourceGroupNode, and netQuotaNode.

func (c *ClusterQueueSnapshot) HasCohort() bool {
	return c.Cohort != nil
}
func (c *ClusterQueueSnapshot) fairWeight() *resource.Quantity {
	return &c.FairWeight
}
func (c *ClusterQueueSnapshot) lendableResourcesInCohort() map[corev1.ResourceName]int64 {
	return c.Cohort.Lendable
}

func (c *ClusterQueueSnapshot) usageFor(fr resources.FlavorResource) int64 {
	return c.Usage[fr]
}

func (c *ClusterQueueSnapshot) resourceGroups() []ResourceGroup {
	return c.ResourceGroups
}
