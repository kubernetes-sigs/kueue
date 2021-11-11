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
)

type Cache struct {
	sync.Mutex

	capacities map[string]*Capacity
	groups     map[string]*Group
}

func NewCache() *Cache {
	return &Cache{
		capacities: make(map[string]*Capacity),
		groups:     make(map[string]*Group),
	}
}

// Group is a set of Capacities that can borrow resources from each other.
type Group struct {
	name    string
	members map[*Capacity]struct{}
}

// Capacity is the internal implementation of kueue.QueueCapacity
type Capacity struct {
	Group                *Group
	RequestableResources kueue.ResourceCapacities
	Affinity             *corev1.NodeSelectorTerm
	Tolerations          []corev1.Toleration
}

func (c *Cache) AddCapacity(cap *kueue.QueueCapacity) error {
	c.Lock()
	defer c.Unlock()

	if _, ok := c.capacities[cap.Name]; ok {
		return fmt.Errorf("capacity %q already exists", cap.Name)
	}
	capImpl := &Capacity{
		RequestableResources: cap.Spec.RequestableResources,
		Affinity:             cap.Spec.Affinity,
		Tolerations:          cap.Spec.Tolerations,
	}
	c.addCapacityToGroup(capImpl, cap.Spec.Group)
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
	capImpl.Affinity = cap.Spec.Affinity
	capImpl.Tolerations = cap.Spec.Tolerations
	if capImpl.Group != nil {
		if capImpl.Group.name != cap.Spec.Group {
			c.removeCapacityFromGroup(capImpl)
			c.addCapacityToGroup(capImpl, cap.Spec.Group)
		}
	} else {
		c.addCapacityToGroup(capImpl, cap.Spec.Group)
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
	c.removeCapacityFromGroup(capImpl)
	delete(c.capacities, cap.Name)
}

func (c *Cache) addCapacityToGroup(cap *Capacity, group string) {
	if group == "" {
		return
	}
	g, ok := c.groups[group]
	if !ok {
		g = &Group{
			name:    group,
			members: make(map[*Capacity]struct{}, 1),
		}
		c.groups[group] = g
	}
	g.members[cap] = struct{}{}
	cap.Group = g
}

func (c *Cache) removeCapacityFromGroup(cap *Capacity) {
	if cap.Group == nil {
		return
	}
	delete(cap.Group.members, cap)
	if len(cap.Group.members) == 0 {
		delete(c.groups, cap.Group.name)
	}
	cap.Group = nil
}
