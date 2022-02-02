/*
Copyright 2022 Google LLC.

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

package capacity

import (
	corev1 "k8s.io/api/core/v1"

	"gke-internal.googlesource.com/gke-batch/kueue/pkg/workload"
)

type Snapshot struct {
	capacities map[string]*Capacity
}

func (c *Cache) Snapshot() Snapshot {
	c.Lock()
	defer c.Unlock()

	snap := Snapshot{
		capacities: make(map[string]*Capacity, len(c.capacities)),
	}
	for _, cohort := range c.cohorts {
		cohortCopy := newCohort(cohort.name, len(cohort.members))
		for cap := range cohort.members {
			capCopy := cap.snapshot()
			capCopy.Cohort = cohortCopy
			cohortCopy.members[capCopy] = struct{}{}
			snap.capacities[cap.Name] = capCopy
		}
	}
	return snap
}

// Snapshot creates a copy of Capacity that includes references to immutable
// objects and deep copies of changing ones. A reference to the cohort is not included.
func (c *Capacity) snapshot() *Capacity {
	copy := &Capacity{
		Name:                 c.Name,
		RequestableResources: c.RequestableResources, // Shallow copy is enough.
		UsedResources:        make(map[corev1.ResourceName]map[string]int64, len(c.UsedResources)),
		Workloads:            make(map[string]*workload.Info, len(c.Workloads)),
	}
	for res, types := range c.UsedResources {
		typesCopy := make(map[string]int64, len(types))
		for k, v := range types {
			typesCopy[k] = v
		}
		c.UsedResources[res] = typesCopy
	}
	for k, v := range c.Workloads {
		// Shallow copy is enough.
		copy.Workloads[k] = v
	}
	return copy
}
