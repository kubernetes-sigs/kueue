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
	"iter"

	corev1 "k8s.io/api/core/v1"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/dqo"
	"sigs.k8s.io/kueue/pkg/util/resourcegroups"
)

// cohort is a set of ClusterQueues that can borrow resources from each other.
type cohort struct {
	Name kueue.CohortReference
	hierarchy.Cohort[*clusterQueue, *cohort]

	resourceNode resourceNode

	// lendable is the capacity this Cohort can lend per resource. It derives
	// only from SubtreeQuota and the tree shape, so updateCohortLendable
	// rebuilds it in the same pass that rebuilds SubtreeQuota.
	//
	// It lives here rather than on resourceNode because only Cohorts ever have
	// it read. dominantResourceShare returns early at a parentless node and
	// otherwise asks the parent, which is always a Cohort, so a field on
	// resourceNode would be written and never read for every ClusterQueue.
	//
	// Served directly rather than copied, so callers must not mutate it.
	lendable map[corev1.ResourceName]resources.Amount

	FairWeight float64

	admittedWorkloadsCount int

	DynamicQuotaOrchestrator kueuealpha.DynamicQuotaOrchestratorReference
}

func newCohort(name kueue.CohortReference) *cohort {
	return &cohort{
		Name:         name,
		Cohort:       hierarchy.NewCohort[*clusterQueue](),
		resourceNode: NewResourceNode(),
	}
}

func (c *cohort) updateCohort(apiCohort *kueue.Cohort, oldParent *cohort) error {
	c.FairWeight = parseFairWeight(apiCohort.Spec.FairSharing)

	c.DynamicQuotaOrchestrator = dqo.EffectiveOrchestrator(apiCohort.Status.EffectiveQuotas)

	c.resourceNode.Quotas = createResourceQuotas(resourcegroups.EffectiveCohortResourceGroups(apiCohort))
	if oldParent != nil && oldParent != c.Parent() {
		updateCohortTreeResourcesIfNoCycle(oldParent)
	}
	return updateCohortTreeResources(c)
}

func (c *cohort) GetName() kueue.CohortReference {
	return c.Name
}

func (c *cohort) getRootUnsafe() *cohort {
	if !c.HasParent() {
		return c
	}
	return c.Parent().getRootUnsafe()
}

// implement flatResourceNode/hierarchicalResourceNode interfaces

func (c *cohort) getResourceNode() resourceNode {
	return c.resourceNode
}

// cachedLendable implements lendableCohort.
func (c *cohort) cachedLendable() map[corev1.ResourceName]resources.Amount {
	return c.lendable
}

func (c *cohort) parentHRN() hierarchicalResourceNode {
	return c.Parent()
}

// implement hierarchy.CycleCheckable interface

func (c *cohort) CCParent() hierarchy.CycleCheckable {
	return c.Parent()
}

// Implements dominantResourceShareNode interface.

func (c *cohort) fairWeight() float64 {
	return c.FairWeight
}

// PathSelfToRoot returns all ancestors starting with self and ending with root,
// or stops when it detects a cycle.
func (c *cohort) PathSelfToRoot() iter.Seq[*cohort] {
	return func(yield func(*cohort) bool) {
		cur := c
		seen := make(map[*cohort]struct{})
		for cur != nil {
			if _, ok := seen[cur]; ok {
				return
			}
			seen[cur] = struct{}{}
			if !yield(cur) {
				return
			}
			cur = cur.Parent()
		}
	}
}

func (c *cohort) updateAdmittedWorkloadsCount(delta int) {
	if c == nil || hierarchy.HasCycle(c) {
		return
	}
	for ancestor := range c.PathSelfToRoot() {
		ancestor.admittedWorkloadsCount += delta
	}
}
