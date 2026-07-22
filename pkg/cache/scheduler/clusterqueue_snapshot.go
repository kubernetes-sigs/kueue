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
	"context"
	"iter"
	"maps"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
)

// FitsCheck represents the result of the "fits" check during scheduling cycle.
type FitsCheck int

const (
	// FitsCheckOk indicates workload fits on both quota and TAS.
	FitsCheckOk FitsCheck = iota

	// FitsCheckNoQuota indicates the workload does not fit due to quota.
	FitsCheckNoQuota

	// FitsCheckNoTAS indicates the workload fits within quota, but TAS rejects.
	FitsCheckNoTAS
)

type ClusterQueueSnapshot struct {
	Name                      kueue.ClusterQueueReference
	ResourceGroups            []ResourceGroup
	Workloads                 map[workload.Reference]*workload.Info
	WorkloadsNotReady         sets.Set[workload.Reference]
	NamespaceSelector         labels.Selector
	Preemption                kueue.ClusterQueuePreemption
	FairWeight                float64
	FlavorFungibility         kueue.FlavorFungibility
	AdmissionScope            kueue.AdmissionScope
	ConcurrentAdmissionPolicy *kueue.ConcurrentAdmissionPolicy
	// Aggregates AdmissionChecks from both .spec.AdmissionChecks and .spec.AdmissionCheckStrategy
	// Sets hold ResourceFlavors to which an AdmissionCheck should apply.
	// In case its empty, it means an AdmissionCheck should apply to all ResourceFlavor
	AdmissionChecks workload.AdmissionChecks
	Status          metrics.ClusterQueueStatus
	// AllocatableResourceGeneration will be increased when some admitted workloads are
	// deleted, or the resource groups are changed.
	AllocatableResourceGeneration int64

	ResourceNode resourceNode
	hierarchy.ClusterQueue[*CohortSnapshot]

	TASFlavors map[kueue.ResourceFlavorReference]*TASFlavorSnapshot
	tasOnly    bool

	flavorsForProvReqACs sets.Set[kueue.ResourceFlavorReference]
	hasMultiKueueAC      bool
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

// SimulateUsageAddition modifies the snapshot by adding usage, and
// returns a function used to restore the usage.
func (c *ClusterQueueSnapshot) SimulateUsageAddition(usage workload.Usage) func() {
	c.AddUsage(usage)
	return func() {
		c.RemoveUsage(usage)
	}
}

// SimulateUsageRemoval modifies the snapshot by removing usage, and
// returns a function used to restore the usage.
func (c *ClusterQueueSnapshot) SimulateUsageRemoval(usage workload.Usage) func() {
	c.RemoveUsage(usage)
	return func() {
		c.AddUsage(usage)
	}
}

func (c *ClusterQueueSnapshot) AddUsage(usage workload.Usage) {
	for fr, q := range usage.Quota {
		addUsage(c, fr, q)
	}
	c.updateTASUsage(usage.TAS, add)
}

func (c *ClusterQueueSnapshot) RemoveUsage(usage workload.Usage) {
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

func (c *ClusterQueueSnapshot) Fits(usage workload.Usage) FitsCheck {
	for fr, q := range usage.Quota {
		if c.Available(fr).Cmp(q) < 0 {
			return FitsCheckNoQuota
		}
	}
	for tasFlavor, flvUsage := range usage.TAS {
		// We assume the `tasFlavor` is already in the snapshot as this was
		// already checked earlier during flavor assignment, and the set of
		// flavors is immutable in snapshot.
		if !c.TASFlavors[tasFlavor].Fits(flvUsage) {
			return FitsCheckNoTAS
		}
	}
	return FitsCheckOk
}

func (c *ClusterQueueSnapshot) QuotaFor(fr resources.FlavorResource) ResourceQuota {
	return c.ResourceNode.Quotas[fr]
}

func (c *ClusterQueueSnapshot) Borrowing(fr resources.FlavorResource) bool {
	return c.BorrowingWith(fr, resources.NewAmount(0))
}

func (c *ClusterQueueSnapshot) BorrowingWith(fr resources.FlavorResource, val resources.Amount) bool {
	return c.QuotaFor(fr).Nominal.Cmp(c.ResourceNode.Usage[fr].Add(val)) < 0
}

// Available returns the current capacity available, before preempting
// any workloads. Includes local capacity and capacity borrowed from
// Cohort. When the ClusterQueue/Cohort is in debt, Available
// will return 0.
func (c *ClusterQueueSnapshot) Available(fr resources.FlavorResource) resources.Amount {
	return resources.MaxAmount(resources.NewAmount(0), available(c, fr))
}

// PotentialAvailable returns the largest workload this ClusterQueue could
// possibly admit, accounting for its capacity and capacity borrowed
// its from Cohort.
func (c *ClusterQueueSnapshot) PotentialAvailable(fr resources.FlavorResource) resources.Amount {
	return potentialAvailable(c, fr)
}

func (c *ClusterQueueSnapshot) GetName() kueue.ClusterQueueReference {
	return c.Name
}

// Implements dominantResourceShareNode interface.

func (c *ClusterQueueSnapshot) fairWeight() float64 {
	return c.FairWeight
}

// implement flatResourceNode/hierarchicalResourceNode interfaces

func (c *ClusterQueueSnapshot) getResourceNode() resourceNode {
	return c.ResourceNode
}

func (c *ClusterQueueSnapshot) parentHRN() hierarchicalResourceNode {
	return c.Parent()
}

func (c *ClusterQueueSnapshot) DominantResourceShare() DRS {
	return dominantResourceShare(c, nil)
}

type WorkloadTASRequests map[kueue.ResourceFlavorReference]FlavorTASRequests

func (c *ClusterQueueSnapshot) FindTopologyAssignmentsForWorkload(
	ctx context.Context,
	tasRequestsByFlavor WorkloadTASRequests,
	options ...FindTopologyAssignmentsOption,
) TASAssignmentsResult {
	opts := &findTopologyAssignmentsOption{}
	for _, option := range options {
		option(opts)
	}

	var aggregatedDomainUsages map[utiltas.TopologyDomainID]resources.Requests
	if features.Enabled(features.TASHandleOverlappingFlavors) {
		aggregatedDomainUsages = make(map[utiltas.TopologyDomainID]resources.Requests)
	}

	result := make(TASAssignmentsResult)
	for _, tasFlavor := range slices.Sorted(maps.Keys(tasRequestsByFlavor)) {
		flavorTASRequests := tasRequestsByFlavor[tasFlavor]
		// We assume the `tasFlavor` is already in the snapshot as this was
		// already checked earlier during flavor assignment, and the set of
		// flavors is immutable in snapshot.
		tasFlavorCache := c.TASFlavors[tasFlavor]
		flvOpts := options
		if features.Enabled(features.TASHandleOverlappingFlavors) && tasFlavorCache.isLowestLevelNode {
			flvOpts = append(slices.Clone(options), WithAggregatedDomainUsages(aggregatedDomainUsages))
		}
		flvResult := tasFlavorCache.FindTopologyAssignmentsForFlavor(ctx, flavorTASRequests, flvOpts...)
		for psName, res := range flvResult {
			res.Flavor = tasFlavor
			result[psName] = res
		}
	}
	return result
}

func (c *ClusterQueueSnapshot) IsTASOnly() bool {
	return c.tasOnly
}

func (c *ClusterQueueSnapshot) HasProvRequestAdmissionCheck(rf kueue.ResourceFlavorReference) bool {
	return c.flavorsForProvReqACs.Has(rf)
}

func (c *ClusterQueueSnapshot) HasMultiKueueAdmissionCheck() bool {
	return c.hasMultiKueueAC
}

// Returns all ancestors starting with parent and ending with root
func (c *ClusterQueueSnapshot) PathParentToRoot() iter.Seq[*CohortSnapshot] {
	return func(yield func(*CohortSnapshot) bool) {
		a := c.Parent()
		for a != nil {
			if !yield(a) {
				return
			}
			a = a.Parent()
		}
	}
}
