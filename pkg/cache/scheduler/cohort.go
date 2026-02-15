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
	"iter"

	"sigs.k8s.io/controller-runtime/pkg/log"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	"sigs.k8s.io/kueue/pkg/metrics"
)

// cohort is a set of ClusterQueues that can borrow resources from each other.
type cohort struct {
	Name kueue.CohortReference
	hierarchy.Cohort[*clusterQueue, *cohort]

	resourceNode resourceNode

	FairWeight float64
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

	c.resourceNode.Quotas = createResourceQuotas(apiCohort.Spec.ResourceGroups)
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

// Returns all ancestors starting with self and ending with root
func (c *cohort) PathSelfToRoot() iter.Seq[*cohort] {
	return func(yield func(*cohort) bool) {
		cohort := c
		for cohort != nil {
			if !yield(cohort) {
				return
			}
			cohort = cohort.Parent()
		}
	}
}

func (c *Cache) RecordCohortMetrics(ctx context.Context, cohortName kueue.CohortReference) {
	cohortFound := false
	cqCohortFound := false
	for _, cohort := range c.hm.Cohorts() {
		if cohort.Name != cohortName {
			continue
		}
		cohortFound = true

		c.Lock()
		rn := cohort.resourceNode.Clone()
		c.Unlock()
		log.FromContext(ctx).V(4).Info("Recording nominal quotas for cohort", "cohort", cohort.Name, "subTreeQuotas", rn)
		for flr, qty := range rn.SubtreeQuota {
			metrics.ReportCohortNominalQuotas(cohort.Name, string(flr.Flavor), string(flr.Resource), float64(qty), c.roleTracker)
		}

		if cohort.HasParent() && !hierarchy.HasCycle(cohort) {
			// if cohort has parent we should trigger recording metrics for the parent cohort as well
			c.RecordCohortMetrics(ctx, cohort.Parent().GetName())
			return
		}
	}

	for _, cq := range c.hm.ClusterQueues() {
		if !cq.HasParent() || cq.Parent().GetName() != cohortName {
			// if the cluster queue doesn't belong to the cohort, we skip it.
			// If the cluster queue belongs to the cohort, but the cohort is not found in cache, we also skip it.
			continue
		}
		cqCohortFound = true

		c.Lock()
		rn := cq.resourceNode.Clone()
		c.Unlock()
		for flr, qty := range rn.SubtreeQuota {
			if cohortFound {
				// If the cohort is found, the metric for the cluster queue will be already recorded as part of the cohort subtree quota
				break
			}
			// we increment quota instead of reporting it directly because the cluster queue doesn't have to be a part of the cohort subtree quota
			log.FromContext(ctx).V(4).Info("Recording nominal quotas for cluster queue in cohort", "clusterQueue", cq.Name, "cohort", cq.Parent().GetName(), "subTreeQuotas", rn)
			metrics.IncrementCohortNominalQuotas(cq.Parent().GetName(), string(flr.Flavor), string(flr.Resource), float64(qty), c.roleTracker)
		}
	}

	if !cohortFound && !cqCohortFound {
		log.FromContext(ctx).V(4).Info("Cohort not found in cache, clearing metrics", "cohort", cohortName)
		metrics.ClearCohortNominalQuotas(cohortName)
	}
}

func (c *Cache) ClearCohortMetrics(ctx context.Context, cohortName kueue.CohortReference) {
	for _, cohort := range c.hm.Cohorts() {
		if cohort.Name != cohortName {
			continue
		}
		metrics.ClearCohortNominalQuotas(cohort.Name)
	}
}
