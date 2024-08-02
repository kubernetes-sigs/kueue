package hierarchy

import "k8s.io/apimachinery/pkg/util/sets"

type WiredCohort[CQ, Cohort nodeBase] struct {
	members sets.Set[CQ]
}

func (c *WiredCohort[CQ, Cohort]) Members() []CQ {
	return c.members.UnsortedList()
}

func NewWiredCohort[CQ, Cohort nodeBase]() WiredCohort[CQ, Cohort] {
	return WiredCohort[CQ, Cohort]{members: sets.New[CQ]()}
}

// Wired implements interface for Manager
func (c *WiredCohort[CQ, Cohort]) Wired() *WiredCohort[CQ, Cohort] {
	return c
}
