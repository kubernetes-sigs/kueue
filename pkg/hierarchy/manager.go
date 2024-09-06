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

// Manager stores Cohorts and ClusterQueues, and maintains the edges
// between them.
type Manager[CQ clusterQueueNode[C], C cohortNode[CQ, C]] struct {
	Cohorts       map[string]C
	ClusterQueues map[string]CQ
	cohortFactory func(string) C
}

// NewManager creates a new Manager. A newCohort function must
// be provided to instantiate Cohorts in the case that a
// ClusterQueue references a Cohort not backed by an API object.
func NewManager[CQ clusterQueueNode[C], C cohortNode[CQ, C]](newCohort func(string) C) Manager[CQ, C] {
	return Manager[CQ, C]{
		make(map[string]C),
		make(map[string]CQ),
		newCohort,
	}
}

func (c *Manager[CQ, C]) AddClusterQueue(cq CQ) {
	c.ClusterQueues[cq.GetName()] = cq
}

func (c *Manager[CQ, C]) UpdateClusterQueueEdge(name, parentName string) {
	cq := c.ClusterQueues[name]
	c.unwireClusterQueue(cq)
	if parentName != "" {
		parent := c.getOrCreateCohort(parentName)
		parent.insertClusterQueue(cq)
		cq.setParent(parent)
	}
}

func (c *Manager[CQ, C]) DeleteClusterQueue(name string) {
	if cq, ok := c.ClusterQueues[name]; ok {
		c.unwireClusterQueue(cq)
		delete(c.ClusterQueues, name)
	}
}

func (c *Manager[CQ, C]) AddCohort(cohortName string) {
	oldCohort, ok := c.Cohorts[cohortName]
	if ok && oldCohort.isExplicit() {
		return
	}
	if !ok {
		c.Cohorts[cohortName] = c.cohortFactory(cohortName)
	}
	c.Cohorts[cohortName].markExplicit()
}

func (c *Manager[CQ, C]) UpdateCohortEdge(name, parentName string) {
	cohort := c.Cohorts[name]
	c.detachCohortFromParent(cohort)
	if parentName != "" {
		parent := c.getOrCreateCohort(parentName)
		parent.insertCohortChild(cohort)
		cohort.setParent(parent)
	}
}

func (c *Manager[CQ, C]) DeleteCohort(name string) {
	cohort, ok := c.Cohorts[name]
	if !ok {
		return
	}
	delete(c.Cohorts, name)
	c.detachCohortFromParent(cohort)
	if !cohort.hasChildren() {
		return
	}
	implicitCohort := c.cohortFactory(name)
	c.Cohorts[implicitCohort.GetName()] = implicitCohort
	c.transferChildren(cohort, implicitCohort)
}

// transferChildren is used when we are changing a Cohort
// from an explicit to an implicit Cohort.
func (c *Manager[CQ, C]) transferChildren(old, new C) {
	for _, cq := range old.ChildCQs() {
		cq.setParent(new)
		new.insertClusterQueue(cq)
	}
	for _, childCohort := range old.ChildCohorts() {
		childCohort.setParent(new)
		new.insertCohortChild(childCohort)
	}
}

func (c *Manager[CQ, C]) unwireClusterQueue(cq CQ) {
	if cq.HasParent() {
		parent := cq.Parent()
		parent.deleteClusterQueue(cq)
		c.cleanupCohort(parent)
		var zero C
		cq.setParent(zero)
	}
}

func (c *Manager[CQ, C]) detachCohortFromParent(cohort C) {
	if cohort.HasParent() {
		parent := cohort.Parent()
		parent.deleteCohortChild(cohort)
		c.cleanupCohort(parent)
		var zero C
		cohort.setParent(zero)
	}
}

func (c *Manager[CQ, C]) getOrCreateCohort(cohortName string) C {
	if _, ok := c.Cohorts[cohortName]; !ok {
		c.Cohorts[cohortName] = c.cohortFactory(cohortName)
	}
	return c.Cohorts[cohortName]
}

func (c *Manager[CQ, C]) cleanupCohort(cohort C) {
	if !cohort.isExplicit() && !cohort.hasChildren() {
		delete(c.Cohorts, cohort.GetName())
	}
}

type nodeBase interface {
	GetName() string
	comparable
}

type clusterQueueNode[C nodeBase] interface {
	Parent() C
	HasParent() bool
	setParent(C)
	nodeBase
}

type cohortNode[CQ, C nodeBase] interface {
	Parent() C
	HasParent() bool
	setParent(C)
	insertCohortChild(C)
	deleteCohortChild(C)
	ChildCohorts() []C

	insertClusterQueue(CQ)
	deleteClusterQueue(CQ)
	hasChildren() bool
	ChildCQs() []CQ
	isExplicit() bool
	markExplicit()
	nodeBase
}
