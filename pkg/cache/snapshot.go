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
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/hierarchy"
	afs "sigs.k8s.io/kueue/pkg/util/admissionfairsharing"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/workload"
)

type Snapshot struct {
	hierarchy.Manager[*ClusterQueueSnapshot, *CohortSnapshot]
	ResourceFlavors          map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor
	InactiveClusterQueueSets sets.Set[kueue.ClusterQueueReference]
}

// RemoveWorkload removes a workload from its corresponding ClusterQueue and
// updates resource usage.
func (s *Snapshot) RemoveWorkload(wl *workload.Info) {
	cq := s.ClusterQueue(wl.ClusterQueue)
	delete(cq.Workloads, workload.Key(wl.Obj))
	cq.RemoveUsage(wl.Usage())
}

// AddWorkload adds a workload from its corresponding ClusterQueue and
// updates resource usage.
func (s *Snapshot) AddWorkload(wl *workload.Info) {
	cq := s.ClusterQueue(wl.ClusterQueue)
	cq.Workloads[workload.Key(wl.Obj)] = wl
	cq.AddUsage(wl.Usage())
}

func (s *Snapshot) Log(log logr.Logger) {
	for name, cq := range s.ClusterQueues() {
		cohortName := "<none>"
		if cq.HasParent() {
			cohortName = string(cq.Parent().Name)
		}

		log.Info("Found ClusterQueue",
			"clusterQueue", klog.KRef("", string(name)),
			"cohort", cohortName,
			"resourceGroups", cq.ResourceGroups,
			"usage", cq.ResourceNode.Usage,
			"workloads", slices.Collect(maps.Keys(cq.Workloads)),
		)
	}
	for name, cohort := range s.Cohorts() {
		log.Info("Found cohort",
			"cohort", name,
			"resources", cohort.ResourceNode.SubtreeQuota,
			"usage", cohort.ResourceNode.Usage,
		)
	}

	// Dump TAS snapshots if the feature is enabled
	if features.Enabled(features.TopologyAwareScheduling) {
		for cqName, cq := range s.ClusterQueues() {
			for tasFlavor, tasSnapshot := range cq.TASFlavors {
				freeCapacityPerDomain, err := tasSnapshot.SerializeFreeCapacityPerDomain()
				if err != nil {
					log.Error(err, "Failed to serialize TAS snapshot free capacity",
						"clusterQueue", cqName,
						"resourceFlavor", tasFlavor,
					)
					continue
				}

				log.Info("TAS Snapshot Free Capacity",
					"clusterQueue", cqName,
					"resourceFlavor", tasFlavor,
					"freeCapacityPerDomain", freeCapacityPerDomain,
				)
			}
		}
	}
}

type snapshotOption struct {
	afsEntryPenalties *utilmaps.SyncMap[utilqueue.LocalQueueReference, corev1.ResourceList]
}

type SnapshotOption func(*snapshotOption)

func WithAfsEntryPenalties(penalties *utilmaps.SyncMap[utilqueue.LocalQueueReference, corev1.ResourceList]) SnapshotOption {
	return func(o *snapshotOption) {
		o.afsEntryPenalties = penalties
	}
}

func (c *Cache) Snapshot(ctx context.Context, options ...SnapshotOption) (*Snapshot, error) {
	c.RLock()
	defer c.RUnlock()

	opts := &snapshotOption{}
	for _, option := range options {
		option(opts)
	}

	snap := Snapshot{
		Manager:                  hierarchy.NewManager(newCohortSnapshot),
		ResourceFlavors:          make(map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor, len(c.resourceFlavors)),
		InactiveClusterQueueSets: sets.New[kueue.ClusterQueueReference](),
	}
	for _, cohort := range c.hm.Cohorts() {
		if hierarchy.HasCycle(cohort) {
			continue
		}
		snap.AddCohort(cohort.Name)
		snap.Cohort(cohort.Name).ResourceNode = cohort.resourceNode.Clone()
		snap.Cohort(cohort.Name).FairWeight = cohort.FairWeight
		if cohort.HasParent() {
			snap.UpdateCohortEdge(cohort.Name, cohort.Parent().Name)
		}
	}
	tasSnapshots := make(map[kueue.ResourceFlavorReference]*TASFlavorSnapshot)
	if features.Enabled(features.TopologyAwareScheduling) {
		for flavor, cache := range c.tasCache.Clone() {
			s, err := cache.snapshot(ctx)
			if err != nil {
				return nil, fmt.Errorf("%w: failed to construct snapshot for TAS flavor: %q", err, flavor)
			} else {
				tasSnapshots[flavor] = s
			}
		}
	}
	for _, cq := range c.hm.ClusterQueues() {
		if !cq.Active() || (cq.HasParent() && hierarchy.HasCycle(cq.Parent())) {
			snap.InactiveClusterQueueSets.Insert(cq.Name)
			continue
		}
		cqSnapshot, err := c.snapshotClusterQueue(ctx, cq, opts.afsEntryPenalties)
		if err != nil {
			return nil, err
		}
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
	// Shallow copy is enough
	maps.Copy(snap.ResourceFlavors, c.resourceFlavors)
	return &snap, nil
}

// snapshotClusterQueue creates a copy of ClusterQueue that includes
// references to immutable objects and deep copies of changing ones.
func (c *Cache) snapshotClusterQueue(ctx context.Context, cq *clusterQueue, afsEntryPenalties *utilmaps.SyncMap[utilqueue.LocalQueueReference, corev1.ResourceList]) (*ClusterQueueSnapshot, error) {
	log := log.FromContext(ctx)
	cc := &ClusterQueueSnapshot{
		Name:                          cq.Name,
		ResourceGroups:                make([]ResourceGroup, len(cq.ResourceGroups)),
		FlavorFungibility:             cq.FlavorFungibility,
		FairWeight:                    cq.FairWeight,
		AllocatableResourceGeneration: cq.AllocatableResourceGeneration,
		Workloads:                     maps.Clone(cq.Workloads),
		Preemption:                    cq.Preemption,
		NamespaceSelector:             cq.NamespaceSelector,
		Status:                        cq.Status,
		AdmissionChecks:               utilmaps.DeepCopySets(cq.AdmissionChecks),
		ResourceNode:                  cq.resourceNode.Clone(),
		TASFlavors:                    make(map[kueue.ResourceFlavorReference]*TASFlavorSnapshot),
		tasOnly:                       cq.isTASOnly(),
		flavorsForProvReqACs:          cq.flavorsWithProvReqAdmissionCheck(),
	}
	for i, rg := range cq.ResourceGroups {
		cc.ResourceGroups[i] = rg.Clone()
	}
	if features.Enabled(features.AdmissionFairSharing) {
		if cq.AdmissionScope != nil {
			cc.AdmissionScope = *cq.AdmissionScope.DeepCopy()
		}
		afsEnabled, resourceWeights := afs.ResourceWeights(&cc.AdmissionScope, c.admissionFairSharing)
		if !afsEnabled {
			return cc, nil
		}
		for _, wl := range cc.Workloads {
			usage, err := wl.CalcLocalQueueFSUsage(ctx, c.client, resourceWeights, afsEntryPenalties)
			if err != nil {
				return nil, fmt.Errorf("failed to calculate LocalQueue FS usage for LocalQueue %v", client.ObjectKey{Namespace: wl.Obj.Namespace, Name: string(wl.Obj.Spec.QueueName)})
			}
			wl.LocalQueueFSUsage = &usage
			log.V(3).Info("Calculated LocalQueueFSUsage for workload", "workload", klog.KObj(wl.Obj), "queue", wl.Obj.Spec.QueueName, "usage", usage)
		}
	}
	return cc, nil
}

func newCohortSnapshot(name kueue.CohortReference) *CohortSnapshot {
	return &CohortSnapshot{
		Name:   name,
		Cohort: hierarchy.NewCohort[*ClusterQueueSnapshot](),
	}
}
