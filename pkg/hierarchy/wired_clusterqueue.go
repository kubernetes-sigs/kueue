package hierarchy

type WiredClusterQueue[CQ, Cohort nodeBase] struct {
	cohort Cohort
}

func (c *WiredClusterQueue[CQ, Cohort]) Cohort() Cohort {
	return c.cohort
}

func (c *WiredClusterQueue[CQ, Cohort]) HasCohort() bool {
	var zero Cohort
	return c.Cohort() != zero
}

// Wired implements interface for Manager
func (c *WiredClusterQueue[CQ, Cohort]) Wired() *WiredClusterQueue[CQ, Cohort] {
	return c
}
