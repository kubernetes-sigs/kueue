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

	corev1 "k8s.io/api/core/v1"

	kueue "gke-internal.googlesource.com/gke-batch/kueue/api/v1alpha1"
	"gke-internal.googlesource.com/gke-batch/kueue/pkg/workload"
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
	Name                 string
	Cohort               *Cohort
	RequestableResources []kueue.Resource
	UsedResources        map[corev1.ResourceName]map[string]int64
	Workloads            map[string]*workload.Info
}

func NewCapacity(cap *kueue.QueueCapacity) *Capacity {
	c := &Capacity{
		Name:                 cap.Name,
		RequestableResources: cap.Spec.RequestableResources,
		UsedResources:        make(map[corev1.ResourceName]map[string]int64, len(cap.Spec.RequestableResources)),
		Workloads:            map[string]*workload.Info{},
	}

	for _, r := range cap.Spec.RequestableResources {
		if len(r.Types) == 0 {
			continue
		}

		ts := make(map[string]int64, len(r.Types))
		for _, t := range r.Types {
			ts[t.Name] = 0
		}
		c.UsedResources[r.Name] = ts
	}
	return c
}

func (c *Capacity) addWorkload(w *kueue.QueuedWorkload) error {
	k := workload.Key(w)
	if _, exist := c.Workloads[k]; exist {
		return fmt.Errorf("workload already exists in capacity")
	}
	wi := workload.NewInfo(w)
	c.Workloads[k] = &wi
	c.updateWorkloadUsage(&wi, 1)
	return nil

}

func (c *Capacity) deleteWorkload(w *kueue.QueuedWorkload) error {
	k := workload.Key(w)
	wi, exist := c.Workloads[k]
	if !exist {
		return fmt.Errorf("workload does not exist in capacity")
	}
	c.updateWorkloadUsage(wi, -1)
	delete(c.Workloads, k)
	return nil
}

func (c *Capacity) updateWorkloadUsage(wi *workload.Info, m int64) {
	for _, ps := range wi.TotalRequests {
		for wlRes, wlResTyp := range ps.Types {
			v, wlResExist := ps.Requests[wlRes]
			capResTyp, capResExist := c.UsedResources[wlRes]
			if capResExist && wlResExist {
				if _, capTypExist := capResTyp[wlResTyp]; capTypExist {
					capResTyp[wlResTyp] += v * m
				}
			}
		}
	}
}

func (c *Cache) AddCapacity(cap *kueue.QueueCapacity) error {
	c.Lock()
	defer c.Unlock()

	if _, ok := c.capacities[cap.Name]; ok {
		return fmt.Errorf("capacity already exists")
	}
	capImpl := NewCapacity(cap)
	c.addCapacityToCohort(capImpl, cap.Spec.Cohort)
	c.capacities[cap.Name] = capImpl
	return nil
}

func (c *Cache) UpdateCapacity(cap *kueue.QueueCapacity) error {
	c.Lock()
	defer c.Unlock()
	capImpl, ok := c.capacities[cap.Name]
	if !ok {
		return fmt.Errorf("capacity doesn't exist")
	}
	capImpl.RequestableResources = cap.Spec.RequestableResources
	if capImpl.Cohort != nil {
		if capImpl.Cohort.name != cap.Spec.Cohort {
			c.deleteCapacityFromCohort(capImpl)
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
	c.deleteCapacityFromCohort(capImpl)
	delete(c.capacities, cap.Name)
}

func (c *Cache) AddWorkload(w *kueue.QueuedWorkload) error {
	c.Lock()
	defer c.Unlock()
	if w.Spec.AssignedCapacity == "" {
		return fmt.Errorf("workload not assigned a capacity")
	}

	cap, ok := c.capacities[string(w.Spec.AssignedCapacity)]
	if !ok {
		return fmt.Errorf("capacity doesn't exist")
	}
	return cap.addWorkload(w)
}

func (c *Cache) UpdateWorkload(oldWl, newWl *kueue.QueuedWorkload) error {
	c.Lock()
	defer c.Unlock()
	if oldWl.Spec.AssignedCapacity != "" {
		cap, ok := c.capacities[string(oldWl.Spec.AssignedCapacity)]
		if !ok {
			return fmt.Errorf("old capacity doesn't exist")
		}

		if err := cap.deleteWorkload(oldWl); err != nil {
			return err
		}
	}
	cap, ok := c.capacities[string(newWl.Spec.AssignedCapacity)]
	if !ok {
		return fmt.Errorf("new capacity doesn't exist")
	}
	return cap.addWorkload(newWl)
}

func (c *Cache) DeleteWorkload(w *kueue.QueuedWorkload) error {
	c.Lock()
	defer c.Unlock()
	if w.Spec.AssignedCapacity == "" {
		return fmt.Errorf("workload not assigned a capacity")
	}

	cap, ok := c.capacities[string(w.Spec.AssignedCapacity)]
	if !ok {
		return fmt.Errorf("capacity doesn't exist")
	}
	return cap.deleteWorkload(w)
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

func (c *Cache) deleteCapacityFromCohort(cap *Capacity) {
	if cap.Cohort == nil {
		return
	}
	delete(cap.Cohort.members, cap)
	if len(cap.Cohort.members) == 0 {
		delete(c.cohorts, cap.Cohort.name)
	}
	cap.Cohort = nil
}
