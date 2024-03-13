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
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/maps"
	"sigs.k8s.io/kueue/pkg/workload"
)

type Snapshot struct {
	ClusterQueues            map[string]*ClusterQueue
	ResourceFlavors          map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor
	InactiveClusterQueueSets sets.Set[string]
}

// RemoveWorkload removes a workload from its corresponding ClusterQueue and
// updates resource usage.
func (s *Snapshot) RemoveWorkload(wl *workload.Info) {
	cq := s.ClusterQueues[wl.ClusterQueue]
	delete(cq.Workloads, workload.Key(wl.Obj))
	updateUsage(wl, cq.Usage, -1)
	if cq.Cohort != nil {
		if features.Enabled(features.LendingLimit) {
			updateCohortUsage(wl, cq, -1)
		} else {
			updateUsage(wl, cq.Cohort.Usage, -1)
		}
	}
}

// AddWorkload adds a workload from its corresponding ClusterQueue and
// updates resource usage.
func (s *Snapshot) AddWorkload(wl *workload.Info) {
	cq := s.ClusterQueues[wl.ClusterQueue]
	cq.Workloads[workload.Key(wl.Obj)] = wl
	updateUsage(wl, cq.Usage, 1)
	if cq.Cohort != nil {
		if features.Enabled(features.LendingLimit) {
			updateCohortUsage(wl, cq, 1)
		} else {
			updateUsage(wl, cq.Cohort.Usage, 1)
		}
	}
}

func (s *Snapshot) Log(log logr.Logger) {
	cohorts := make(map[string]*Cohort)
	for name, cq := range s.ClusterQueues {
		cohortName := "<none>"
		if cq.Cohort != nil {
			cohorts[cq.Name] = cq.Cohort
			cohortName = cq.Cohort.Name
		}

		log.Info("Found ClusterQueue",
			"clusterQueue", klog.KRef("", name),
			"cohort", cohortName,
			"resourceGroups", cq.ResourceGroups,
			"usage", cq.Usage,
			"workloads", maps.Keys(cq.Workloads),
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
		ClusterQueues:            make(map[string]*ClusterQueue, len(c.clusterQueues)),
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
		cohortCopy := newCohort(cohort.Name, cohort.Members.Len())
		cohortCopy.AllocatableResourceGeneration = 0
		for cq := range cohort.Members {
			if cq.Active() {
				cqCopy := snap.ClusterQueues[cq.Name]
				cqCopy.accumulateResources(cohortCopy)
				cqCopy.Cohort = cohortCopy
				cohortCopy.Members.Insert(cqCopy)
				cohortCopy.AllocatableResourceGeneration += cqCopy.AllocatableResourceGeneration
			}
		}
	}
	return snap
}

// snapshot creates a copy of ClusterQueue that includes references to immutable
// objects and deep copies of changing ones. A reference to the cohort is not included.
func (c *ClusterQueue) snapshot() *ClusterQueue {
	cc := &ClusterQueue{
		Name:                          c.Name,
		ResourceGroups:                c.ResourceGroups, // Shallow copy is enough.
		RGByResource:                  c.RGByResource,   // Shallow copy is enough.
		FlavorFungibility:             c.FlavorFungibility,
		AllocatableResourceGeneration: c.AllocatableResourceGeneration,
		Usage:                         make(FlavorResourceQuantities, len(c.Usage)),
		Workloads:                     make(map[string]*workload.Info, len(c.Workloads)),
		Preemption:                    c.Preemption,
		NamespaceSelector:             c.NamespaceSelector,
		Status:                        c.Status,
		AdmissionChecks:               c.AdmissionChecks.Clone(),
	}
	for fName, rUsage := range c.Usage {
		rUsageCopy := make(map[corev1.ResourceName]int64, len(rUsage))
		for k, v := range rUsage {
			rUsageCopy[k] = v
		}
		cc.Usage[fName] = rUsageCopy
	}
	for k, v := range c.Workloads {
		// Shallow copy is enough.
		cc.Workloads[k] = v
	}

	if features.Enabled(features.LendingLimit) {
		cc.GuaranteedQuota = c.GuaranteedQuota
	}

	return cc
}

func (c *ClusterQueue) accumulateResources(cohort *Cohort) {
	if cohort.RequestableResources == nil {
		cohort.RequestableResources = make(FlavorResourceQuantities, len(c.ResourceGroups))
	}
	for _, rg := range c.ResourceGroups {
		for _, flvQuotas := range rg.Flavors {
			res := cohort.RequestableResources[flvQuotas.Name]
			if res == nil {
				res = make(map[corev1.ResourceName]int64, len(flvQuotas.Resources))
				cohort.RequestableResources[flvQuotas.Name] = res
			}
			for rName, rQuota := range flvQuotas.Resources {
				// When feature LendingLimit enabled, cohort.RequestableResources indicates
				// the sum of cq.NominalQuota and other cqs' LendingLimit (if not nil).
				// If LendingLimit is not nil, we should count the lendingLimit as the requestable
				// resource because we can't borrow more quota than lendingLimit.
				if features.Enabled(features.LendingLimit) && rQuota.LendingLimit != nil {
					res[rName] += *rQuota.LendingLimit
				} else {
					res[rName] += rQuota.Nominal
				}
			}
		}
	}
	if cohort.Usage == nil {
		cohort.Usage = make(FlavorResourceQuantities, len(c.Usage))
	}
	for fName, resUsages := range c.Usage {
		used := cohort.Usage[fName]
		if used == nil {
			used = make(map[corev1.ResourceName]int64, len(resUsages))
			cohort.Usage[fName] = used
		}
		for res, val := range resUsages {
			// Similar to cohort.RequestableResources, we accumulate the usage above the guaranteed resources,
			// here we should remove the guaranteed quota as well for that part can not be borrowed.
			val -= c.guaranteedQuota(fName, res)
			// if val < 0, it means the cq is not using any quota belongs to LendingLimit
			if val < 0 {
				val = 0
			}
			used[res] += val
		}
	}
}
