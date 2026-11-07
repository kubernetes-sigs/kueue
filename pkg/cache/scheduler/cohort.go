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
	"slices"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/dqo"
	"sigs.k8s.io/kueue/pkg/util/resourcegroups"
)

// cohort is a set of ClusterQueues that can borrow resources from each other.
type cohort struct {
	Name kueue.CohortReference
	hierarchy.Cohort[*clusterQueue, *cohort]

	resourceNode resourceNode

	FairWeight float64

	// admittedWorkloadsCount counts admitted Workloads in the subtree. Workload
	// events only adjust it along the current path to the root, so it is
	// recomputed from the children whenever the tree is rebuilt.
	admittedWorkloadsCount int

	// admittedLabelCounts tracks admitted Workloads by ClusterQueue custom-label
	// values. Workload events and in-place relabels update it along the path to
	// the root. Tree rebuilds replace it from the children so an implicit Cohort
	// recreation does not start from an empty map.
	admittedLabelCounts *metrics.LabelValsTracker

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

func (c *cohort) updateAdmittedWorkloadsCount(delta int, labelVals []string) {
	if c == nil || hierarchy.HasCycle(c) {
		return
	}
	trackLabels := features.Enabled(features.CustomMetricLabels) && len(labelVals) > 0
	labelSet := metrics.ValsSetFrom(labelVals)
	for ancestor := range c.PathSelfToRoot() {
		ancestor.admittedWorkloadsCount += delta
		if trackLabels {
			ancestor.ensureAdmittedLabelCounts().Add(labelSet, delta)
		}
	}
}

func (c *cohort) moveAdmittedLabelCounts(oldVals, newVals []string, delta int) {
	if c == nil || hierarchy.HasCycle(c) || delta == 0 || slices.Equal(oldVals, newVals) {
		return
	}
	if !features.Enabled(features.CustomMetricLabels) {
		return
	}
	oldLS := metrics.ValsSetFrom(oldVals)
	newLS := metrics.ValsSetFrom(newVals)
	for ancestor := range c.PathSelfToRoot() {
		counts := ancestor.ensureAdmittedLabelCounts()
		counts.Add(oldLS, -delta)
		counts.Add(newLS, delta)
	}
}

func (c *cohort) ensureAdmittedLabelCounts() *metrics.LabelValsTracker {
	if c.admittedLabelCounts == nil {
		c.admittedLabelCounts = metrics.NewLabelValsTracker()
	}
	return c.admittedLabelCounts
}

func (c *cohort) admittedCountForLabels(labelVals []string) int {
	if len(labelVals) == 0 || !features.Enabled(features.CustomMetricLabels) {
		return c.admittedWorkloadsCount
	}
	if c.admittedLabelCounts == nil {
		return 0
	}
	return c.admittedLabelCounts.Count(metrics.ValsSetFrom(labelVals))
}
