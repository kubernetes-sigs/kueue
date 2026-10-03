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
	"slices"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

// initialCachePollPeriod is the gap between apiserver lists while the
// scheduler waits for its cache. The wait ends only when two successive
// lists are equal and the cache already holds that list, so one poll is
// not a sleep that stands in for the check.
const initialCachePollPeriod = 10 * time.Millisecond

// initialObjectSet is one apiserver list of the objects the scheduler must
// see before it admits. Names are sorted so two lists compare equal when
// they contain the same objects, ignoring resource version and order.
type initialObjectSet struct {
	clusterQueues     []kueue.ClusterQueueReference
	cohorts           []kueue.CohortReference
	flavors           []kueue.ResourceFlavorReference
	topologies        []kueue.TopologyReference
	reservedWorkloads []workload.Reference
}

func (s initialObjectSet) equal(other initialObjectSet) bool {
	return slices.Equal(s.clusterQueues, other.clusterQueues) &&
		slices.Equal(s.cohorts, other.cohorts) &&
		slices.Equal(s.flavors, other.flavors) &&
		slices.Equal(s.topologies, other.topologies) &&
		slices.Equal(s.reservedWorkloads, other.reservedWorkloads)
}

// initialCacheReady reports whether the scheduling loop may start.
// previous is the prior list, or nil on the first observation. current is
// the list just read. holds is whether the kueue cache already contains
// every object in current. A single list is never enough: an object created
// between the list and the compare would be missing from both, so the
// caller waits until two successive lists match and the cache holds them.
func initialCacheReady(previous *initialObjectSet, current initialObjectSet, holds bool) bool {
	if previous == nil || !holds {
		return false
	}
	return previous.equal(current)
}

// waitForInitialCache blocks until the kueue cache contains every
// ClusterQueue, Cohort, ResourceFlavor, and Topology from the apiserver,
// and every Workload with an active quota reservation is recorded. The
// lists run without the kueue cache lock. ctx.Done ends the wait.
func (s *Scheduler) waitForInitialCache(ctx context.Context) error {
	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Waiting until the scheduler cache matches the initial list")
	var previous *initialObjectSet
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := s.listInitialObjects(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.V(2).Info("Failed to list objects before the first scheduling cycle", "error", err)
			previous = nil
		} else {
			holds := s.cache.HoldsInitialObjects(
				current.clusterQueues,
				current.cohorts,
				current.flavors,
				current.topologies,
				current.reservedWorkloads,
			)
			if initialCacheReady(previous, current, holds) {
				log.V(2).Info("Scheduler cache matches the initial list")
				return nil
			}
			previous = &current
		}

		timer := time.NewTimer(initialCachePollPeriod)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Scheduler) listInitialObjects(ctx context.Context) (initialObjectSet, error) {
	var clusterQueues kueue.ClusterQueueList
	if err := s.client.List(ctx, &clusterQueues); err != nil {
		return initialObjectSet{}, err
	}
	var cohorts kueue.CohortList
	if err := s.client.List(ctx, &cohorts); err != nil {
		return initialObjectSet{}, err
	}
	var flavors kueue.ResourceFlavorList
	if err := s.client.List(ctx, &flavors); err != nil {
		return initialObjectSet{}, err
	}
	var topologies kueue.TopologyList
	if err := s.client.List(ctx, &topologies); err != nil {
		return initialObjectSet{}, err
	}
	var workloads kueue.WorkloadList
	if err := s.client.List(ctx, &workloads); err != nil {
		return initialObjectSet{}, err
	}
	return newInitialObjectSet(clusterQueues.Items, cohorts.Items, flavors.Items, topologies.Items, workloads.Items), nil
}

func newInitialObjectSet(
	clusterQueues []kueue.ClusterQueue,
	cohorts []kueue.Cohort,
	flavors []kueue.ResourceFlavor,
	topologies []kueue.Topology,
	workloads []kueue.Workload,
) initialObjectSet {
	set := initialObjectSet{
		clusterQueues:     make([]kueue.ClusterQueueReference, len(clusterQueues)),
		cohorts:           make([]kueue.CohortReference, len(cohorts)),
		flavors:           make([]kueue.ResourceFlavorReference, len(flavors)),
		topologies:        make([]kueue.TopologyReference, len(topologies)),
		reservedWorkloads: reservedWorkloadRefs(workloads),
	}
	for i := range clusterQueues {
		set.clusterQueues[i] = kueue.ClusterQueueReference(clusterQueues[i].Name)
	}
	for i := range cohorts {
		set.cohorts[i] = kueue.CohortReference(cohorts[i].Name)
	}
	for i := range flavors {
		set.flavors[i] = kueue.ResourceFlavorReference(flavors[i].Name)
	}
	for i := range topologies {
		set.topologies[i] = kueue.TopologyReference(topologies[i].Name)
	}
	slices.Sort(set.clusterQueues)
	slices.Sort(set.cohorts)
	slices.Sort(set.flavors)
	slices.Sort(set.topologies)
	slices.Sort(set.reservedWorkloads)
	return set
}

func reservedWorkloadRefs(workloads []kueue.Workload) []workload.Reference {
	refs := make([]workload.Reference, 0, len(workloads))
	for i := range workloads {
		if workload.HasActiveQuotaReservation(&workloads[i]) {
			refs = append(refs, workload.Key(&workloads[i]))
		}
	}
	return refs
}
