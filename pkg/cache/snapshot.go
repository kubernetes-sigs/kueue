/*
Copyright 2022 The Kubernetes Authors.

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

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	"sigs.k8s.io/kueue/pkg/workload"
)

type Snapshot struct {
	ClusterQueues            map[string]*ClusterQueueSnapshot
	ResourceFlavors          map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor
	InactiveClusterQueueSets sets.Set[string]
}

// RemoveWorkload removes a workload from its corresponding ClusterQueue and
// updates resource usage.
func (s *Snapshot) RemoveWorkload(wl *workload.Info) {
	cq := s.ClusterQueues[wl.ClusterQueue]
	delete(cq.Workloads, workload.Key(wl.Obj))
	cq.addOrRemoveUsage(wl.FlavorResourceUsage(), -1)
}

// AddWorkload adds a workload from its corresponding ClusterQueue and
// updates resource usage.
func (s *Snapshot) AddWorkload(wl *workload.Info) {
	cq := s.ClusterQueues[wl.ClusterQueue]
	cq.Workloads[workload.Key(wl.Obj)] = wl
	cq.addOrRemoveUsage(wl.FlavorResourceUsage(), 1)
}

func (c *ClusterQueueSnapshot) addOrRemoveUsage(usage resources.FlavorResourceQuantities, m int64) {
	updateFlavorUsage(usage, c.Usage, m)
	if c.Cohort != nil {
		if features.Enabled(features.LendingLimit) {
			updateCohortUsage(usage, c, m)
		} else {
			updateFlavorUsage(usage, c.Cohort.Usage, m)
		}
	}
}

func (s *Snapshot) Log(log logr.Logger) {
	cohorts := make(map[string]*CohortSnapshot)
	for name, cq := range s.ClusterQueues {
		cohortName := "<none>"
		if cq.Cohort != nil {
			cohortName = cq.Cohort.Name
			cohorts[cohortName] = cq.Cohort
		}

		log.Info("Found ClusterQueue",
			"clusterQueue", klog.KRef("", name),
			"cohort", cohortName,
			"resourceGroups", cq.ResourceGroups,
			"usage", cq.Usage,
			"workloads", utilmaps.Keys(cq.Workloads),
		)
	}
	for name, cohort := range cohorts {
		log.Info("Found cohort",
			"cohort", name,
			"resources", cohort.RequestableResources,
			"usage", cohort.Usage,
		)
	}
}

func (c *Cache) Snapshot() Snapshot {
	c.RLock()
	defer c.RUnlock()

	snap := Snapshot{
		ClusterQueues:            make(map[string]*ClusterQueueSnapshot, len(c.clusterQueues)),
		ResourceFlavors:          make(map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor, len(c.resourceFlavors)),
		InactiveClusterQueueSets: sets.New[string](),
	}
	for _, cq := range c.clusterQueues {
		if !cq.Active() {
			snap.InactiveClusterQueueSets.Insert(cq.Name)
			continue
		}
		snap.ClusterQueues[cq.Name] = cq.snapshot()
	}
	for name, rf := range c.resourceFlavors {
		// Shallow copy is enough
		snap.ResourceFlavors[name] = rf
	}
	for _, cohort := range c.cohorts {
		cohort.snapshotInto(snap.ClusterQueues)
	}
	return snap
}

// snapshot creates a copy of ClusterQueue that includes references to immutable
// objects and deep copies of changing ones. A reference to the cohort is not included.
func (c *clusterQueue) snapshot() *ClusterQueueSnapshot {
	cc := &ClusterQueueSnapshot{
		Name:                          c.Name,
		ResourceGroups:                c.ResourceGroups, // Shallow copy is enough.
		FlavorFungibility:             c.FlavorFungibility,
		FairWeight:                    c.FairWeight,
		AllocatableResourceGeneration: c.AllocatableResourceGeneration,
		Usage:                         maps.Clone(c.Usage),
		Workloads:                     maps.Clone(c.Workloads),
		Preemption:                    c.Preemption,
		NamespaceSelector:             c.NamespaceSelector,
		Status:                        c.Status,
		AdmissionChecks:               utilmaps.DeepCopySets[kueue.ResourceFlavorReference](c.AdmissionChecks),
		Quotas:                        c.quotas,
	}

	if features.Enabled(features.LendingLimit) {
		cc.GuaranteedQuota = c.GuaranteedQuota
	}

	return cc
}

func (c *cohort) snapshotInto(cqs map[string]*ClusterQueueSnapshot) {
	cohortSnap := &CohortSnapshot{
		Name:                 c.Name,
		Members:              make(sets.Set[*ClusterQueueSnapshot], c.Members.Len()),
		Lendable:             c.CalculateLendable(),
		Usage:                make(resources.FlavorResourceQuantities),
		RequestableResources: make(resources.FlavorResourceQuantities),
	}
	cohortSnap.AllocatableResourceGeneration = 0
	for cq := range c.Members {
		if cq.Active() {
			cqSnap := cqs[cq.Name]
			cqSnap.accumulateResources(cohortSnap)
			cqSnap.Cohort = cohortSnap
			cohortSnap.Members.Insert(cqSnap)
			cohortSnap.AllocatableResourceGeneration += cqSnap.AllocatableResourceGeneration
		}
	}
}

func (c *ClusterQueueSnapshot) accumulateResources(cohort *CohortSnapshot) {
	for _, rg := range c.ResourceGroups {
		for _, fName := range rg.Flavors {
			for rName := range rg.CoveredResources {
				fr := resources.FlavorResource{Flavor: fName, Resource: rName}
				rQuota := c.QuotaFor(fr)
				// When feature LendingLimit enabled, cohort.RequestableResources indicates
				// the sum of cq.NominalQuota and other cqs' LendingLimit (if not nil).
				// If LendingLimit is not nil, we should count the lendingLimit as the requestable
				// resource because we can't borrow more quota than lendingLimit.
				if features.Enabled(features.LendingLimit) && rQuota.LendingLimit != nil {
					cohort.RequestableResources[fr] += *rQuota.LendingLimit
				} else {
					cohort.RequestableResources[fr] += rQuota.Nominal
				}
			}
		}
	}
	for fr, val := range c.Usage {
		// Similar to cohort.RequestableResources, we accumulate the usage above the guaranteed resources,
		// here we should remove the guaranteed quota as well for that part can not be borrowed.
		val -= c.guaranteedQuota(fr)
		// if val < 0, it means the cq is not using any quota belongs to LendingLimit
		if val < 0 {
			val = 0
		}
		cohort.Usage[fr] += val
	}
}
