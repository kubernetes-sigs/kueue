package queue

import "sigs.k8s.io/kueue/pkg/hierarchy"

// cohort is a set of ClusterQueues that can borrow resources from
// each other.
type cohort struct {
	Name string
	hierarchy.WiredCohort[*ClusterQueue, *cohort]
}

func cohortFactory(name string) *cohort {
	return &cohort{
		name,
		hierarchy.NewWiredCohort[*ClusterQueue, *cohort](),
	}
}

func (c *cohort) GetName() string {
	return c.Name
}
