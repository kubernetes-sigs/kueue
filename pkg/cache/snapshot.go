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
	cq.removeUsage(wl.FlavorResourceUsage())
}

// AddWorkload adds a workload from its corresponding ClusterQueue and
// updates resource usage.
func (s *Snapshot) AddWorkload(wl *workload.Info) {
	cq := s.ClusterQueues[wl.ClusterQueue]
	cq.Workloads[workload.Key(wl.Obj)] = wl
	cq.AddUsage(wl.FlavorResourceUsage())
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
			"usage", cq.ResourceNode.Usage,
			"workloads", utilmaps.Keys(cq.Workloads),
		)
	}
	for name, cohort := range cohorts {
		log.Info("Found cohort",
			"cohort", name,
			"resources", cohort.ResourceNode.SubtreeQuota,
			"usage", cohort.ResourceNode.Usage,
		)
	}
}

func (c *Cache) Snapshot() Snapshot {
	c.RLock()
	defer c.RUnlock()

	snap := Snapshot{
		ClusterQueues:            make(map[string]*ClusterQueueSnapshot, len(c.hm.ClusterQueues)),
		ResourceFlavors:          make(map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor, len(c.resourceFlavors)),
		InactiveClusterQueueSets: sets.New[string](),
	}
	for _, cq := range c.hm.ClusterQueues {
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
	for _, cohort := range c.hm.Cohorts {
		cohort.snapshotInto(snap.ClusterQueues)
	}
	return snap
}

// snapshot creates a copy of ClusterQueue that includes references to immutable
// objects and deep copies of changing ones. A reference to the cohort is not included.
func (c *clusterQueue) snapshot() *ClusterQueueSnapshot {
	cc := &ClusterQueueSnapshot{
		Name:                          c.Name,
		ResourceGroups:                make([]ResourceGroup, len(c.ResourceGroups)),
		FlavorFungibility:             c.FlavorFungibility,
		FairWeight:                    c.FairWeight,
		AllocatableResourceGeneration: c.AllocatableResourceGeneration,
		Workloads:                     maps.Clone(c.Workloads),
		Preemption:                    c.Preemption,
		NamespaceSelector:             c.NamespaceSelector,
		Status:                        c.Status,
		AdmissionChecks:               utilmaps.DeepCopySets[kueue.ResourceFlavorReference](c.AdmissionChecks),
		ResourceNode:                  c.resourceNode.Clone(),
	}
	for i, rg := range c.ResourceGroups {
		cc.ResourceGroups[i] = rg.Clone()
	}
	return cc
}

func (c *cohort) snapshotInto(cqs map[string]*ClusterQueueSnapshot) {
	cohortSnap := &CohortSnapshot{
		Name:         c.Name,
		Members:      make(sets.Set[*ClusterQueueSnapshot], len(c.ChildCQs())),
		ResourceNode: c.resourceNode.Clone(),
	}
	cohortSnap.AllocatableResourceGeneration = 0
	for _, cq := range c.ChildCQs() {
		if cq.Active() {
			cqSnap := cqs[cq.Name]
			cqSnap.Cohort = cohortSnap
			cohortSnap.Members.Insert(cqSnap)
			cohortSnap.AllocatableResourceGeneration += cqSnap.AllocatableResourceGeneration
		}
	}
}
