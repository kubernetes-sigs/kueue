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
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

// HoldsInitialObjects reports whether every listed ClusterQueue, explicit
// Cohort, ResourceFlavor, and Topology is present in the cache, and every
// listed Workload reference is recorded in workloadAssignedQueues.
//
// The caller lists from the apiserver without holding this cache's lock.
// This method takes the lock only for the compare. An implicit cohort, one
// created because a ClusterQueue names it, does not count as the Cohort
// object. Callers pass only workloads that have an active quota reservation.
func (c *Cache) HoldsInitialObjects(
	clusterQueues []kueue.ClusterQueueReference,
	cohorts []kueue.CohortReference,
	flavors []kueue.ResourceFlavorReference,
	topologies []kueue.TopologyReference,
	reservedWorkloads []workload.Reference,
) bool {
	c.RLock()
	defer c.RUnlock()

	for _, name := range clusterQueues {
		if c.hm.ClusterQueue(name) == nil {
			return false
		}
	}
	for _, name := range cohorts {
		cohort := c.hm.Cohort(name)
		if cohort == nil || !cohort.IsExplicit() {
			return false
		}
	}
	for _, name := range flavors {
		if _, ok := c.resourceFlavors[name]; !ok {
			return false
		}
	}
	for _, name := range topologies {
		if !c.tasCache.hasTopology(name) {
			return false
		}
	}
	for _, key := range reservedWorkloads {
		if _, ok := c.workloadAssignedQueues[key]; !ok {
			return false
		}
	}
	return true
}
