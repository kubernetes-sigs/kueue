package hierarchy

// Manager stores Cohorts and ClusterQueues, and maintains the edges
// between them.
type Manager[CQ cqNode[CQ, C], C cohortNode[CQ, C]] struct {
	Cohorts       map[string]C
	ClusterQueues map[string]CQ
	cohortFactory func(string) C
}

// NewManager creates a new Manager.  A Cohort factory
// function must be provided to instantiate Cohorts in the case that a
// ClusterQueue references a Cohort not backed by an API object.
func NewManager[CQ cqNode[CQ, C], C cohortNode[CQ, C]](cohortFactory func(string) C) Manager[CQ, C] {
	return Manager[CQ, C]{
		make(map[string]C),
		make(map[string]CQ),
		cohortFactory,
	}
}

func (c *Manager[CQ, C]) AddClusterQueue(cq CQ) {
	c.ClusterQueues[cq.GetName()] = cq
}

func (c *Manager[CQ, C]) UpdateClusterQueueEdge(name, parentName string) {
	cq := c.ClusterQueues[name]
	c.unwireClusterQueue(cq)
	if parentName != "" {
		cohort := c.getCohort(parentName)
		cohort.Wired().members.Insert(cq)
		cq.Wired().cohort = cohort
	}
}

func (c *Manager[CQ, C]) DeleteClusterQueue(name string) {
	if cq, ok := c.ClusterQueues[name]; ok {
		c.unwireClusterQueue(cq)
		delete(c.ClusterQueues, name)
		return
	}
}

func (c *Manager[CQ, C]) unwireClusterQueue(cq CQ) {
	if cq.Wired().HasParent() {
		cohort := cq.Wired().Parent()
		cohort.Wired().members.Delete(cq)
		c.cleanupCohort(cohort)
		var zero C
		cq.Wired().cohort = zero
	}
}

func (c *Manager[CQ, C]) getCohort(cohortName string) C {
	if _, ok := c.Cohorts[cohortName]; !ok {
		c.Cohorts[cohortName] = c.cohortFactory(cohortName)
	}
	return c.Cohorts[cohortName]
}

func (c *Manager[CQ, C]) cleanupCohort(cohort C) {
	if cohort.Wired().members.Len() == 0 {
		delete(c.Cohorts, cohort.GetName())
	}
}

type nodeBase interface {
	GetName() string
	comparable
}

type cqNode[CQ, C nodeBase] interface {
	Wired() *WiredClusterQueue[CQ, C]
	nodeBase
}

type cohortNode[CQ, C nodeBase] interface {
	Wired() *WiredCohort[CQ, C]
	nodeBase
}
