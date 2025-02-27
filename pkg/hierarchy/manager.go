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

// Manager stores Cohorts and ClusterQueues, and maintains the edges
// between them.
type Manager[CQ clusterQueueNode[C], C cohortNode[CQ, C]] struct {
	cohorts       map[string]C
	clusterQueues map[string]CQ
	cohortFactory func(string) C
	CycleChecker  CycleChecker
}

// NewManager creates a new Manager. A newCohort function must
// be provided to instantiate Cohorts in the case that a
// ClusterQueue references a Cohort not backed by an API object.
func NewManager[CQ clusterQueueNode[C], C cohortNode[CQ, C]](newCohort func(string) C) Manager[CQ, C] {
	return Manager[CQ, C]{
		make(map[string]C),
		make(map[string]CQ),
		newCohort,
		CycleChecker{make(map[string]bool)},
	}
}

func (m *Manager[CQ, C]) AddClusterQueue(cq CQ) {
	m.clusterQueues[cq.GetName()] = cq
}

func (m *Manager[CQ, C]) GetClusterQueue(name string) CQ {
  return m.clusterQueues[name]
}

func (m *Manager[CQ, C]) GetClusterQueueNames() []string {
	clusterQueueNames := make([]string, 0, len(m.clusterQueues))
	for k := range m.clusterQueues {
		clusterQueueNames = append(clusterQueueNames, k)
	}
	return clusterQueueNames
}

func (m *Manager[CQ, C]) GetClusterQueuesCopy() map[string]CQ {
	clusterQueuesCopy := make(map[string]CQ)
	for k, v := range m.clusterQueues {
		clusterQueuesCopy[k] = v
	}
	return clusterQueuesCopy
}

func (m *Manager[CQ, C]) UpdateClusterQueueEdge(name, parentName string) {
	cq := m.clusterQueues[name]
	m.detachClusterQueueFromParent(cq)
	if parentName != "" {
		parent := m.getOrCreateCohort(parentName)
		parent.insertClusterQueue(cq)
		cq.setParent(parent)
	}
}

func (m *Manager[CQ, C]) DeleteClusterQueue(name string) {
	if cq, ok := m.clusterQueues[name]; ok {
		m.detachClusterQueueFromParent(cq)
		delete(m.clusterQueues, name)
	}
}

func (m *Manager[CQ, C]) AddCohort(cohortName string) {
	oldCohort, ok := m.cohorts[cohortName]
	if ok && oldCohort.isExplicit() {
		return
	}
	if !ok {
		m.cohorts[cohortName] = m.cohortFactory(cohortName)
	}
	m.cohorts[cohortName].markExplicit()
}

func (m *Manager[CQ, C]) GetCohort(name string) C {
  return m.cohorts[name]
}

func (m *Manager[CQ, C]) GetCohortNames() []string {
	cohortNames := make([]string, 0, len(m.cohorts))
	for k := range m.cohorts {
		cohortNames = append(cohortNames, k)
	}
	return cohortNames
}

func (m *Manager[CQ, C]) GetCohortsCopy() map[string]C {
	cohortCopy := make(map[string]C)
	for k, v := range m.cohorts {
		cohortCopy[k] = v
	}
	return cohortCopy
}

func (m *Manager[CQ, C]) UpdateCohortEdge(name, parentName string) {
	m.resetCycleChecker()
	cohort := m.cohorts[name]
	m.detachCohortFromParent(cohort)
	if parentName != "" {
		parent := m.getOrCreateCohort(parentName)
		parent.insertCohortChild(cohort)
		cohort.setParent(parent)
	}
}

func (m *Manager[CQ, C]) DeleteCohort(name string) {
	m.resetCycleChecker()
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

func (m *Manager[CQ, C]) getOrCreateCohort(cohortName string) C {
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

func (m *Manager[CQ, C]) resetCycleChecker() {
	m.CycleChecker = CycleChecker{make(map[string]bool, len(m.cohorts))}
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
