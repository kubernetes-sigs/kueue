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
	"context"
	"fmt"
	"maps"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/hierarchy"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	"sigs.k8s.io/kueue/pkg/workload"
)

type Snapshot struct {
	hierarchy.Manager[*ClusterQueueSnapshot, *CohortSnapshot]
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
	for name, cq := range s.ClusterQueues {
		cohortName := "<none>"
		if cq.HasParent() {
			cohortName = cq.Parent().Name
		}

		log.Info("Found ClusterQueue",
			"clusterQueue", klog.KRef("", name),
			"cohort", cohortName,
			"resourceGroups", cq.ResourceGroups,
			"usage", cq.ResourceNode.Usage,
			"workloads", utilmaps.Keys(cq.Workloads),
		)
	}
	for name, cohort := range s.Cohorts {
		log.Info("Found cohort",
			"cohort", name,
			"resources", cohort.ResourceNode.SubtreeQuota,
			"usage", cohort.ResourceNode.Usage,
		)
	}
}

func (c *Cache) Snapshot(ctx context.Context) (*Snapshot, error) {
	c.RLock()
	defer c.RUnlock()

	snap := Snapshot{
		Manager:                  hierarchy.NewManager(newCohortSnapshot),
		ResourceFlavors:          make(map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor, len(c.resourceFlavors)),
		InactiveClusterQueueSets: sets.New[string](),
	}
	for _, cohort := range c.hm.Cohorts {
		if c.hm.CycleChecker.HasCycle(cohort) {
			continue
		}
		snap.AddCohort(cohort.Name)
		snap.Cohorts[cohort.Name].ResourceNode = cohort.resourceNode.Clone()
		if cohort.HasParent() {
			snap.UpdateCohortEdge(cohort.Name, cohort.Parent().Name)
		}
	}
	tasSnapshots := make(map[kueue.ResourceFlavorReference]*TASFlavorSnapshot)
	if features.Enabled(features.TopologyAwareScheduling) {
		for key, cache := range c.tasCache.Clone() {
			s, err := cache.snapshot(ctx)
			if err != nil {
				return nil, fmt.Errorf("%w: failed to construct snapshot for TAS flavor: %q", err, key)
			} else {
				tasSnapshots[key] = s
			}
		}
	}
	for _, cq := range c.hm.ClusterQueues {
		if !cq.Active() || (cq.HasParent() && c.hm.CycleChecker.HasCycle(cq.Parent())) {
			snap.InactiveClusterQueueSets.Insert(cq.Name)
			continue
		}
		cqSnapshot := snapshotClusterQueue(cq)
		snap.AddClusterQueue(cqSnapshot)
		if cq.HasParent() {
			snap.UpdateClusterQueueEdge(cq.Name, cq.Parent().Name)
		}
		if features.Enabled(features.TopologyAwareScheduling) {
			for tasFlv, s := range tasSnapshots {
				if cq.flavorInUse(tasFlv) {
					cqSnapshot.TASFlavors[tasFlv] = s
				}
			}
		}
	}
	for name, rf := range c.resourceFlavors {
		// Shallow copy is enough
		snap.ResourceFlavors[name] = rf
	}
	return &snap, nil
}

// snapshotClusterQueue creates a copy of ClusterQueue that includes
// references to immutable objects and deep copies of changing ones.
func snapshotClusterQueue(c *clusterQueue) *ClusterQueueSnapshot {
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
		TASFlavors:                    make(map[kueue.ResourceFlavorReference]*TASFlavorSnapshot),
	}
	for i, rg := range c.ResourceGroups {
		cc.ResourceGroups[i] = rg.Clone()
	}
	return cc
}

func newCohortSnapshot(name string) *CohortSnapshot {
	return &CohortSnapshot{
		Name:   name,
		Cohort: hierarchy.NewCohort[*ClusterQueueSnapshot, *CohortSnapshot](),
	}
}
