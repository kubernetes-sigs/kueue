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
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/hierarchy"
)

// cohort is a set of ClusterQueues that can borrow resources from
// each other.
type cohort struct {
	Name kueue.CohortReference
	hierarchy.Cohort[*ClusterQueue, *cohort]
}

func newCohort(name kueue.CohortReference) *cohort {
	return &cohort{
		name,
		hierarchy.NewCohort[*ClusterQueue, *cohort](),
	}
}

func (c *cohort) GetName() kueue.CohortReference {
	return c.Name
}

// CCParent satisfies the CycleCheckable interface.
func (c *cohort) CCParent() hierarchy.CycleCheckable {
	return c.Parent()
}

func (c *cohort) getRootUnsafe() *cohort {
	if !c.HasParent() {
		return c
	}
	return c.Parent().getRootUnsafe()
}
