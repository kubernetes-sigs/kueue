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

package hierarchy

import kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"

type CycleCheckable[T comparable] interface {
	GetName() T
	HasParent() bool
	CCParent() CycleCheckable[T]
}

// cycleChecker checks for cycles in Cohorts, while memoizing the
// result.
type CycleChecker struct {
	cycles map[kueue.CohortReference]bool
}

func (c *CycleChecker) HasCycle(cohort CycleCheckable[kueue.CohortReference]) bool {
	if cycle, seen := c.cycles[cohort.GetName()]; seen {
		return cycle
	}
	if !cohort.HasParent() {
		c.cycles[cohort.GetName()] = false
		return c.cycles[cohort.GetName()]
	}
	c.cycles[cohort.GetName()] = true
	c.cycles[cohort.GetName()] = c.HasCycle(cohort.CCParent())
	return c.cycles[cohort.GetName()]
}
