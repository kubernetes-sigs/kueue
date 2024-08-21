package hierarchy

type WiredClusterQueue[CQ, Cohort nodeBase] struct {
	cohort Cohort
}

func (c *WiredClusterQueue[CQ, Cohort]) Parent() Cohort {
	return c.cohort
}

func (c *WiredClusterQueue[CQ, Cohort]) HasParent() bool {
	var zero Cohort
	return c.Parent() != zero
}

// Wired implements interface for Manager
func (c *WiredClusterQueue[CQ, Cohort]) Wired() *WiredClusterQueue[CQ, Cohort] {
	return c
}
