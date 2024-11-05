/*
Copyright 2024 The Kubernetes Authors.

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

import "k8s.io/apimachinery/pkg/util/sets"

//lint:ignore U1000 due to https://github.com/dominikh/go-tools/issues/1602.
type Cohort[CQ, C nodeBase] struct {
	parent       C
	childCohorts sets.Set[C]
	childCqs     sets.Set[CQ]
	// Indicates whether this Cohort is backed
	// by an API object.
	explicit bool
}

func (c *Cohort[CQ, C]) Parent() C {
	return c.parent
}

func (c *Cohort[CQ, C]) HasParent() bool {
	var zero C
	return c.Parent() != zero
}

func (c *Cohort[CQ, C]) ChildCQs() []CQ {
	return c.childCqs.UnsortedList()
}

func (c *Cohort[CQ, C]) ChildCohorts() []C {
	return c.childCohorts.UnsortedList()
}

func NewCohort[CQ, C nodeBase]() Cohort[CQ, C] {
	return Cohort[CQ, C]{
		childCohorts: sets.New[C](),
		childCqs:     sets.New[CQ](),
	}
}

// implement cohortNode interface

func (c *Cohort[CQ, C]) setParent(cohort C) {
	c.parent = cohort
}

func (c *Cohort[CQ, C]) insertCohortChild(cohort C) {
	c.childCohorts.Insert(cohort)
}

func (c *Cohort[CQ, C]) deleteCohortChild(cohort C) {
	c.childCohorts.Delete(cohort)
}

func (c *Cohort[CQ, C]) insertClusterQueue(cq CQ) {
	c.childCqs.Insert(cq)
}

func (c *Cohort[CQ, C]) deleteClusterQueue(cq CQ) {
	c.childCqs.Delete(cq)
}

func (c *Cohort[CQ, C]) hasChildren() bool {
	return c.childCqs.Len()+c.childCohorts.Len() > 0
}

func (c *Cohort[CQ, C]) isExplicit() bool {
	return c.explicit
}

func (c *Cohort[CQ, C]) markExplicit() {
	c.explicit = true
}
