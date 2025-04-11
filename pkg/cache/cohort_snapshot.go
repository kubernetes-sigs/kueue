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
	"k8s.io/apimachinery/pkg/api/resource"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/hierarchy"
)

type CohortSnapshot struct {
	Name kueue.CohortReference

	ResourceNode resourceNode
	hierarchy.Cohort[*ClusterQueueSnapshot, *CohortSnapshot]

	FairWeight resource.Quantity
}

func (c *CohortSnapshot) GetName() kueue.CohortReference {
	return c.Name
}

// Root returns the root of the Cohort Tree. It expects that no cycles
// exist in the Cohort graph.
func (c *CohortSnapshot) Root() *CohortSnapshot {
	if !c.HasParent() {
		return c
	}
	return c.Parent().Root()
}

// SubtreeClusterQueues returns all of the ClusterQueues in the
// subtree starting at the given Cohort. It expects that no cycles
// exist in the Cohort graph.
func (c *CohortSnapshot) SubtreeClusterQueues() []*ClusterQueueSnapshot {
	return c.subtreeClusterQueuesHelper(make([]*ClusterQueueSnapshot, 0, c.subtreeClusterQueueCount()))
}

func (c *CohortSnapshot) subtreeClusterQueuesHelper(cqs []*ClusterQueueSnapshot) []*ClusterQueueSnapshot {
	cqs = append(cqs, c.ChildCQs()...)
	for _, cohort := range c.ChildCohorts() {
		cqs = cohort.subtreeClusterQueuesHelper(cqs)
	}
	return cqs
}

func (c *CohortSnapshot) subtreeClusterQueueCount() int {
	count := len(c.ChildCQs())
	for _, cohort := range c.ChildCohorts() {
		count += cohort.subtreeClusterQueueCount()
	}
	return count
}

func (c *CohortSnapshot) DominantResourceShare() int {
	share, _ := dominantResourceShare(c, nil)
	return share
}

// The methods below implement hierarchicalResourceNode interface.

func (c *CohortSnapshot) getResourceNode() resourceNode {
	return c.ResourceNode
}

func (c *CohortSnapshot) parentHRN() hierarchicalResourceNode {
	return c.Parent()
}

// Implements dominantResourceShareNode interface.

func (c *CohortSnapshot) fairWeight() *resource.Quantity {
	return &c.FairWeight
}
