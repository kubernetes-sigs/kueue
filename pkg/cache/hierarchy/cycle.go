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

import (
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

type CycleCheckable interface {
	GetName() kueue.CohortReference
	HasParent() bool
	CCParent() CycleCheckable
}

func HasCycle(cohort CycleCheckable) bool {
	return hasCycle(cohort, sets.New[kueue.CohortReference]())
}

func hasCycle(cohort CycleCheckable, seen sets.Set[kueue.CohortReference]) bool {
	if !cohort.HasParent() {
		return false
	}
	if seen.Has(cohort.GetName()) {
		return true
	}
	seen.Insert(cohort.GetName())
	return hasCycle(cohort.CCParent(), seen)
}

// RootCohortName returns the name of the root of the Cohort tree that cohort
// belongs to. It returns false if cohort is nil or the tree has a cycle.
func RootCohortName[C interface {
	CycleCheckable
	comparable
}](cohort C) (kueue.CohortReference, bool) {
	var zero C
	if cohort == zero || HasCycle(cohort) {
		return "", false
	}
	var node CycleCheckable = cohort
	for node.HasParent() {
		node = node.CCParent()
	}
	return node.GetName(), true
}
