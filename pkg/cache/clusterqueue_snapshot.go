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

package cache

import (
	"maps"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/hierarchy"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
)

type ClusterQueueSnapshot struct {
	Name              string
	ResourceGroups    []ResourceGroup
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
	// AllocatableResourceGeneration will be increased when some admitted workloads are
	// deleted, or the resource groups are changed.
	AllocatableResourceGeneration int64

	ResourceNode ResourceNode
	hierarchy.ClusterQueue[*CohortSnapshot]

	TASFlavors map[kueue.ResourceFlavorReference]*TASFlavorSnapshot
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

// SimulateUsageRemoval the snapshot by removing the usage corresponding to the
// list of workloads. It returns the function which can be used to restore
// the usage.
func (c *ClusterQueueSnapshot) SimulateUsageRemoval(workloads []*workload.Info) func() {
	var usage []workload.Usage
	for _, w := range workloads {
		usage = append(usage, w.Usage())
	}
	for _, u := range usage {
		c.removeUsage(u)
	}
	return func() {
		for _, u := range usage {
			c.AddUsage(u)
		}
	}
}

func (c *ClusterQueueSnapshot) AddUsage(usage workload.Usage) {
	for fr, q := range usage.Quota {
		addUsage(c, fr, q)
	}
	c.updateTASUsage(usage.TAS, add)
}

func (c *ClusterQueueSnapshot) removeUsage(usage workload.Usage) {
	for fr, q := range usage.Quota {
		removeUsage(c, fr, q)
	}
	c.updateTASUsage(usage.TAS, subtract)
}

func (c *ClusterQueueSnapshot) updateTASUsage(usage workload.TASUsage, op usageOp) {
	if features.Enabled(features.TopologyAwareScheduling) {
		for tasFlavor, tasUsage := range usage {
			if tasFlvCache := c.TASFlavors[tasFlavor]; tasFlvCache != nil {
				for _, tr := range tasUsage {
					domainID := utiltas.DomainID(tr.Values)
					tasFlvCache.updateTASUsage(domainID, tr.TotalRequests(), op, tr.Count)
				}
			}
		}
	}
}

func (c *ClusterQueueSnapshot) Fits(usage workload.Usage) bool {
	for fr, q := range usage.Quota {
		if c.Available(fr) < q {
			return false
		}
	}
	return true
}

func (c *ClusterQueueSnapshot) QuotaFor(fr resources.FlavorResource) ResourceQuota {
	return c.ResourceNode.Quotas[fr]
}

func (c *ClusterQueueSnapshot) Borrowing(fr resources.FlavorResource) bool {
	return c.BorrowingWith(fr, 0)
}

func (c *ClusterQueueSnapshot) BorrowingWith(fr resources.FlavorResource, val int64) bool {
	return c.ResourceNode.Usage[fr]+val > c.QuotaFor(fr).Nominal
}

// Available returns the current capacity available, before preempting
// any workloads. Includes local capacity and capacity borrowed from
// Cohort. When the ClusterQueue/Cohort is in debt, Available
// will return 0.
func (c *ClusterQueueSnapshot) Available(fr resources.FlavorResource) int64 {
	return max(0, available(c, fr))
}

// PotentialAvailable returns the largest workload this ClusterQueue could
// possibly admit, accounting for its capacity and capacity borrowed
// its from Cohort.
func (c *ClusterQueueSnapshot) PotentialAvailable(fr resources.FlavorResource) int64 {
	return potentialAvailable(c, fr)
}

func (c *ClusterQueueSnapshot) GetName() string {
	return c.Name
}

// Implements dominantResourceShareNode interface.

func (c *ClusterQueueSnapshot) fairWeight() *resource.Quantity {
	return &c.FairWeight
}

// The methods below implement hierarchicalResourceNode interface.

func (c *ClusterQueueSnapshot) getResourceNode() ResourceNode {
	return c.ResourceNode
}

func (c *ClusterQueueSnapshot) parentHRN() hierarchicalResourceNode {
	return c.Parent()
}

func (c *ClusterQueueSnapshot) DominantResourceShare() (int, corev1.ResourceName) {
	return dominantResourceShare(c, nil)
}

func (c *ClusterQueueSnapshot) DominantResourceShareWith(wlReq resources.FlavorResourceQuantities) (int, corev1.ResourceName) {
	return dominantResourceShare(c, wlReq)
}

func (c *ClusterQueueSnapshot) DominantResourceShareWithout(wlReq resources.FlavorResourceQuantities) (int, corev1.ResourceName) {
	without := maps.Clone(wlReq)
	for fr, q := range without {
		without[fr] = -q
	}
	return dominantResourceShare(c, without)
}

type WorkloadTASRequests map[kueue.ResourceFlavorReference]FlavorTASRequests

func (c *ClusterQueueSnapshot) FindTopologyAssignmentsForWorkload(
	tasRequestsByFlavor WorkloadTASRequests,
	simulateEmpty bool) TASAssignmentsResult {
	result := make(TASAssignmentsResult)
	for tasFlavor, flavorTASRequests := range tasRequestsByFlavor {
		// We assume the `tasFlavor` is already in the snapshot as this was
		// already checked earlier during flavor assignment, and the set of
		// flavors is immutable in snapshot.
		tasFlavorCache := c.TASFlavors[tasFlavor]
		flvResult := tasFlavorCache.FindTopologyAssignmentsForFlavor(flavorTASRequests, simulateEmpty)
		for psName, psAssignment := range flvResult {
			result[psName] = psAssignment
		}
	}
	return result
}
