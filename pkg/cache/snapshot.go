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
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
	"sigs.k8s.io/kueue/pkg/workload"
)

type Snapshot struct {
	ClusterQueues            map[string]*ClusterQueue
	ResourceFlavors          map[string]*kueue.ResourceFlavor
	InactiveClusterQueueSets sets.Set[string]
}

func (c *Cache) Snapshot() Snapshot {
	c.RLock()
	defer c.RUnlock()

	snap := Snapshot{
		ClusterQueues:            make(map[string]*ClusterQueue, len(c.clusterQueues)),
		ResourceFlavors:          make(map[string]*kueue.ResourceFlavor, len(c.resourceFlavors)),
		InactiveClusterQueueSets: sets.New[string](),
	}
	for _, cq := range c.clusterQueues {
		if !cq.Active() {
			snap.InactiveClusterQueueSets.Insert(cq.Name)
			continue
		}
		snap.ClusterQueues[cq.Name] = cq.snapshot()
	}
	for _, rf := range c.resourceFlavors {
		// Shallow copy is enough
		snap.ResourceFlavors[rf.Name] = rf
	}
	for _, cohort := range c.cohorts {
		cohortCopy := newCohort(cohort.Name, cohort.Members.Len())
		for cq := range cohort.Members {
			if cq.Active() {
				cqCopy := snap.ClusterQueues[cq.Name]
				cqCopy.accumulateResources(cohortCopy)
				cqCopy.Cohort = cohortCopy
				cohortCopy.Members.Insert(cqCopy)
			}
		}
	}
	return snap
}

// Snapshot creates a copy of ClusterQueue that includes references to immutable
// objects and deep copies of changing ones. A reference to the cohort is not included.
func (c *ClusterQueue) snapshot() *ClusterQueue {
	cc := &ClusterQueue{
		Name:                 c.Name,
		RequestableResources: c.RequestableResources, // Shallow copy is enough.
		UsedResources:        make(ResourceQuantities, len(c.UsedResources)),
		Workloads:            make(map[string]*workload.Info, len(c.Workloads)),
		LabelKeys:            c.LabelKeys, // Shallow copy is enough.
		NamespaceSelector:    c.NamespaceSelector,
		Status:               c.Status,
	}
	for res, flavors := range c.UsedResources {
		flavorsCopy := make(map[string]int64, len(flavors))
		for k, v := range flavors {
			flavorsCopy[k] = v
		}
		cc.UsedResources[res] = flavorsCopy
	}
	for k, v := range c.Workloads {
		// Shallow copy is enough.
		cc.Workloads[k] = v
	}
	return cc
}

func (c *ClusterQueue) accumulateResources(cohort *Cohort) {
	if cohort.RequestableResources == nil {
		cohort.RequestableResources = make(ResourceQuantities, len(c.RequestableResources))
	}
	for name, res := range c.RequestableResources {
		req := cohort.RequestableResources[name]
		if req == nil {
			req = make(map[string]int64, len(res.Flavors))
			cohort.RequestableResources[name] = req
		}
		for _, flavor := range res.Flavors {
			req[flavor.Name] += flavor.Min
		}
	}
	if cohort.UsedResources == nil {
		cohort.UsedResources = make(ResourceQuantities, len(c.UsedResources))
	}
	for res, flavors := range c.UsedResources {
		used := cohort.UsedResources[res]
		if used == nil {
			used = make(map[string]int64, len(flavors))
			cohort.UsedResources[res] = used
		}
		for flavor, val := range flavors {
			used[flavor] += val
		}
	}
}
