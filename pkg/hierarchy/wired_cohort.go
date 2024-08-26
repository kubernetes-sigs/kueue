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
