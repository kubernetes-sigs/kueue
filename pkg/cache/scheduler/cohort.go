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
	"maps"

	"github.com/go-logr/logr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
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

func (c *Cache) RecordCohortMetrics(log logr.Logger, cohortName kueue.CohortReference) {
	log.V(4).Info("Recording metrics for cohort", "cohort", cohortName)
	cohortFound := false
	cqCohortFound := false
	if cohort := cohortByName(c.hm.Cohorts(), cohortName); cohort != nil {
		cohortFound = true
		subtreeQuotas := c.getResourceNodeSubTreeQuota(cohort.resourceNode)
		for flr, qty := range subtreeQuotas {
			log.V(4).Info("Recording subtree quotas for cohort", "cohort", cohort.Name, "Flavor", flr.Flavor, "Resource", flr.Resource, "qty", qty, "roleTracker", c.roleTracker)
			metrics.ReportCohortNominalQuotas(cohort.Name, string(flr.Flavor), string(flr.Resource), float64(qty), c.roleTracker)
		}

		if cohort.HasParent() && !hierarchy.HasCycle(cohort) {
			log.V(4).Info("Parent cohort detected", "cohort", cohortName, "cohortParent", cohort.Parent().GetName())
			c.RecordCohortMetrics(log, cohort.Parent().GetName())
		}
	} else {
		subtreeQuotas := make(map[resources.FlavorResource]int64)
		for _, cq := range c.hm.ClusterQueues() {
			if !cq.HasParent() || cq.Parent().GetName() != cohortName {
				continue
			}
			cqCohortFound = true
			for flr, qty := range c.getResourceNodeSubTreeQuota(cq.resourceNode) {
				subtreeQuotas[flr] += qty
			}
		}
		if cqCohortFound {
			for flr, qty := range subtreeQuotas {
				log.V(4).Info("Recording subtree quotas for implicit cohort", "Flavor", flr.Flavor, "Resource", flr.Resource, "qty", qty, "roleTracker", c.roleTracker)
				metrics.ReportCohortNominalQuotas(cohortName, string(flr.Flavor), string(flr.Resource), float64(qty), c.roleTracker)
			}
		}
	}

	if !cohortFound && !cqCohortFound {
		log.V(4).Info("Cohort not found in cache, clearing metrics", "cohort", cohortName)
		metrics.ClearCohortNominalQuotas(cohortName, "", "")
	}
}

// TBD:
// main question, clearing or subtracting
// if we clear, then we need to record the metrics for the parent cohort, if exists, to update the parent cohort metrics with the new values
// let's go with clearing , we can trigger metrics recording for the parent cohort in the same way as we do now,
// by calling RecordCohortMetrics for the parent cohort,
// which will recalculate the metrics based on the current state of the cache and update the metrics accordingly
func (c *Cache) ClearCohortMetrics(log logr.Logger, cohortName kueue.CohortReference) {
	log.V(4).Info("Clearing metrics for cohort", "cohort", cohortName)
	metrics.ClearCohortNominalQuotas(cohortName, "", "")
	if cohort := cohortByName(c.hm.Cohorts(), cohortName); cohort != nil {
		if cohort.HasParent() && !hierarchy.HasCycle(cohort) {
			c.ClearCohortMetrics(log, cohort.Parent().GetName())
		}
	}
}

func (c *Cache) getResourceNodeSubTreeQuota(rn resourceNode) map[resources.FlavorResource]int64 {
	c.RLock()
	defer c.RUnlock()
	return maps.Clone(rn.SubtreeQuota)
}

func cohortByName(cohorts map[kueue.CohortReference]*cohort, name kueue.CohortReference) *cohort {
	if cohort, exists := cohorts[name]; exists {
		return cohort
	}
	return nil
}
