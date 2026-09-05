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

package queue

import (
	"iter"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
)

// cohort is a set of ClusterQueues that can borrow resources from
// each other.
type cohort struct {
	Name kueue.CohortReference
	hierarchy.Cohort[*ClusterQueue, *cohort]

	// pendingActiveCount and pendingInadmissibleCount are aggregated pending
	// workload counts across this cohort's entire subtree, updated via
	// updatePendingWorkloadsCount whenever a descendant ClusterQueue changes.
	pendingActiveCount       int
	pendingInadmissibleCount int
}

func newCohort(name kueue.CohortReference) *cohort {
	return &cohort{
		Name:   name,
		Cohort: hierarchy.NewCohort[*ClusterQueue](),
	}
}

func (c *cohort) GetName() kueue.CohortReference {
	return c.Name
}

// CCParent satisfies the CycleCheckable interface.
func (c *cohort) CCParent() hierarchy.CycleCheckable {
	return c.Parent()
}

// PathSelfToRoot returns all ancestors starting with self and ending with root.
func (c *cohort) PathSelfToRoot() iter.Seq[*cohort] {
	return func(yield func(*cohort) bool) {
		node := c
		for node != nil {
			if !yield(node) {
				return
			}
			node = node.Parent()
		}
	}
}

// updatePendingWorkloadsCount adjusts pending counters for this cohort and all
// ancestor cohorts by the given deltas.
func (c *cohort) updatePendingWorkloadsCount(activeDelta, inadmissibleDelta int) {
	if c == nil || hierarchy.HasCycle(c) {
		return
	}
	for ancestor := range c.PathSelfToRoot() {
		ancestor.pendingActiveCount += activeDelta
		ancestor.pendingInadmissibleCount += inadmissibleDelta
	}
}

func (c *cohort) getRootUnsafe() *cohort {
	if !c.HasParent() {
		return c
	}
	return c.Parent().getRootUnsafe()
}
