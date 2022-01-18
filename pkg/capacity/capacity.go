/*
Copyright 2021 Google LLC.

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
	"fmt"
	"sync"

	kueue "gke-internal.googlesource.com/gke-batch/kueue/api/v1alpha1"
)

type Cache struct {
	sync.Mutex

	capacities map[string]*Capacity
	cohorts    map[string]*Cohort
}

func NewCache() *Cache {
	return &Cache{
		capacities: make(map[string]*Capacity),
		cohorts:    make(map[string]*Cohort),
	}
}

// Cohort is a set of Capacities that can borrow resources from each other.
type Cohort struct {
	name    string
	members map[*Capacity]struct{}
}

// Capacity is the internal implementation of kueue.QueueCapacity
type Capacity struct {
	Cohort               *Cohort
	RequestableResources []kueue.Resource
}

func (c *Cache) AddCapacity(cap *kueue.QueueCapacity) error {
	c.Lock()
	defer c.Unlock()

	if _, ok := c.capacities[cap.Name]; ok {
		return fmt.Errorf("capacity %q already exists", cap.Name)
	}
	capImpl := &Capacity{
		RequestableResources: cap.Spec.RequestableResources,
	}
	c.addCapacityToCohort(capImpl, cap.Spec.Cohort)
	c.capacities[cap.Name] = capImpl
	return nil
}

func (c *Cache) UpdateCapacity(cap *kueue.QueueCapacity) error {
	c.Lock()
	defer c.Unlock()
	capImpl, ok := c.capacities[cap.Name]
	if !ok {
		return fmt.Errorf("capacity %q doesn't exist", cap.Name)
	}
	capImpl.RequestableResources = cap.Spec.RequestableResources
	if capImpl.Cohort != nil {
		if capImpl.Cohort.name != cap.Spec.Cohort {
			c.removeCapacityFromCohort(capImpl)
			c.addCapacityToCohort(capImpl, cap.Spec.Cohort)
		}
	} else {
		c.addCapacityToCohort(capImpl, cap.Spec.Cohort)
	}
	return nil
}

func (c *Cache) DeleteCapacity(cap *kueue.QueueCapacity) {
	c.Lock()
	defer c.Unlock()
	capImpl, ok := c.capacities[cap.Name]
	if !ok {
		return
	}
	c.removeCapacityFromCohort(capImpl)
	delete(c.capacities, cap.Name)
}

func (c *Cache) addCapacityToCohort(cap *Capacity, cohort string) {
	if cohort == "" {
		return
	}
	g, ok := c.cohorts[cohort]
	if !ok {
		g = &Cohort{
			name:    cohort,
			members: make(map[*Capacity]struct{}, 1),
		}
		c.cohorts[cohort] = g
	}
	g.members[cap] = struct{}{}
	cap.Cohort = g
}

func (c *Cache) removeCapacityFromCohort(cap *Capacity) {
	if cap.Cohort == nil {
		return
	}
	delete(cap.Cohort.members, cap)
	if len(cap.Cohort.members) == 0 {
		delete(c.cohorts, cap.Cohort.name)
	}
	cap.Cohort = nil
}
