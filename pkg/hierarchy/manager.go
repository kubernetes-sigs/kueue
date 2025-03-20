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
	"maps"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

// Manager stores Cohorts and ClusterQueues, and maintains the edges
// between them.
type Manager[CQ clusterQueueNode[C], C cohortNode[CQ, C]] struct {
	cohorts       map[kueue.CohortReference]C
	clusterQueues map[kueue.ClusterQueueReference]CQ
	cohortFactory func(kueue.CohortReference) C
}

// NewManager creates a new Manager. A newCohort function must
// be provided to instantiate Cohorts in the case that a
// ClusterQueue references a Cohort not backed by an API object.
func NewManager[CQ clusterQueueNode[C], C cohortNode[CQ, C]](newCohort func(kueue.CohortReference) C) Manager[CQ, C] {
	return Manager[CQ, C]{
		make(map[kueue.CohortReference]C),
		make(map[kueue.ClusterQueueReference]CQ),
		newCohort,
	}
}

func (m *Manager[CQ, C]) AddClusterQueue(cq CQ) {
	m.clusterQueues[cq.GetName()] = cq
}

func (m *Manager[CQ, C]) ClusterQueue(name kueue.ClusterQueueReference) CQ {
	return m.clusterQueues[name]
}

func (m *Manager[CQ, C]) ClusterQueuesNames() []kueue.ClusterQueueReference {
	clusterQueuesNames := make([]kueue.ClusterQueueReference, 0, len(m.clusterQueues))
	for k := range m.clusterQueues {
		clusterQueuesNames = append(clusterQueuesNames, k)
	}
	return clusterQueuesNames
}

func (m *Manager[CQ, C]) ClusterQueues() map[kueue.ClusterQueueReference]CQ {
	return maps.Clone(m.clusterQueues)
}

func (m *Manager[CQ, C]) UpdateClusterQueueEdge(name kueue.ClusterQueueReference, parentName kueue.CohortReference) {
	cq := m.clusterQueues[name]
	m.detachClusterQueueFromParent(cq)
	if parentName != "" {
		parent := m.getOrCreateCohort(parentName)
		parent.insertClusterQueue(cq)
		cq.setParent(parent)
	}
}

func (m *Manager[CQ, C]) DeleteClusterQueue(name kueue.ClusterQueueReference) {
	if cq, ok := m.clusterQueues[name]; ok {
		m.detachClusterQueueFromParent(cq)
		delete(m.clusterQueues, name)
	}
}

func (m *Manager[CQ, C]) AddCohort(cohortName kueue.CohortReference) {
	oldCohort, ok := m.cohorts[cohortName]
	if ok && oldCohort.isExplicit() {
		return
	}
	if !ok {
		m.cohorts[cohortName] = m.cohortFactory(cohortName)
	}
	m.cohorts[cohortName].markExplicit()
}

func (m *Manager[CQ, C]) Cohort(name kueue.CohortReference) C {
	return m.cohorts[name]
}

func (m *Manager[CQ, C]) Cohorts() map[kueue.CohortReference]C {
	return maps.Clone(m.cohorts)
}

func (m *Manager[CQ, C]) UpdateCohortEdge(name, parentName kueue.CohortReference) {
	cohort := m.cohorts[name]
	m.detachCohortFromParent(cohort)
	if parentName != "" {
		parent := m.getOrCreateCohort(parentName)
		parent.insertCohortChild(cohort)
		cohort.setParent(parent)
	}
}

func (m *Manager[CQ, C]) DeleteCohort(name kueue.CohortReference) {
	cohort, ok := m.cohorts[name]
	if !ok {
		return
	}
	delete(m.cohorts, name)
	m.detachCohortFromParent(cohort)
	if !cohort.hasChildren() {
		return
	}
	implicitCohort := m.cohortFactory(name)
	m.cohorts[implicitCohort.GetName()] = implicitCohort
	m.transferChildren(cohort, implicitCohort)
}

// transferChildren is used when we are changing a Cohort
// from an explicit to an implicit Cohort.
func (m *Manager[CQ, C]) transferChildren(old, new C) {
	for _, cq := range old.ChildCQs() {
		cq.setParent(new)
		new.insertClusterQueue(cq)
	}
	for _, childCohort := range old.ChildCohorts() {
		childCohort.setParent(new)
		new.insertCohortChild(childCohort)
	}
}

func (m *Manager[CQ, C]) detachClusterQueueFromParent(cq CQ) {
	if cq.HasParent() {
		parent := cq.Parent()
		parent.deleteClusterQueue(cq)
		m.cleanupCohort(parent)
		var zero C
		cq.setParent(zero)
	}
}

func (m *Manager[CQ, C]) detachCohortFromParent(cohort C) {
	if cohort.HasParent() {
		parent := cohort.Parent()
		parent.deleteCohortChild(cohort)
		m.cleanupCohort(parent)
		var zero C
		cohort.setParent(zero)
	}
}

func (m *Manager[CQ, C]) getOrCreateCohort(cohortName kueue.CohortReference) C {
	if _, ok := m.cohorts[cohortName]; !ok {
		m.cohorts[cohortName] = m.cohortFactory(cohortName)
	}
	return m.cohorts[cohortName]
}

func (m *Manager[CQ, C]) cleanupCohort(cohort C) {
	if !cohort.isExplicit() && !cohort.hasChildren() {
		delete(m.cohorts, cohort.GetName())
	}
}

// NewManagerForTest is a special constructor for using in tests
func NewManagerForTest[CQ clusterQueueNode[C], C cohortNode[CQ, C]](cohorts map[kueue.CohortReference]C, clusterQueues map[kueue.ClusterQueueReference]CQ) Manager[CQ, C] {
	return Manager[CQ, C]{
		cohorts:       cohorts,
		clusterQueues: clusterQueues,
	}
}

type nodeBase[T comparable] interface {
	GetName() T
	comparable
}

type clusterQueueNode[C nodeBase[kueue.CohortReference]] interface {
	Parent() C
	HasParent() bool
	setParent(C)
	nodeBase[kueue.ClusterQueueReference]
}

type cohortNode[CQ nodeBase[kueue.ClusterQueueReference], C nodeBase[kueue.CohortReference]] interface {
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
	nodeBase[kueue.CohortReference]
}
