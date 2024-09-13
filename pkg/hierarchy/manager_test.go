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

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/apimachinery/pkg/util/sets"
)

func TestManager(t *testing.T) {
	type M = Manager[*testClusterQueue, *testCohort]
	type edge struct {
		child  string
		parent string
	}
	opts := []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.SortSlices(func(a, b edge) bool {
			return a.child < b.child
		}),
		cmp.AllowUnexported(edge{}),
	}
	tests := map[string]struct {
		operations     func(M)
		wantCqs        sets.Set[string]
		wantCohorts    sets.Set[string]
		wantCqEdge     []edge
		wantCohortEdge []edge
	}{
		"create queues": {
			operations: func(m M) {
				m.AddClusterQueue(newCq("queue1"))
				m.AddClusterQueue(newCq("queue2"))
			},
			wantCqs: sets.New("queue1", "queue2"),
		},
		"delete queue": {
			operations: func(m M) {
				m.AddClusterQueue(newCq("queue1"))
				m.AddClusterQueue(newCq("queue2"))
				m.DeleteClusterQueue("queue2")
			},
			wantCqs: sets.New("queue1"),
		},
		"create queues with cohorts": {
			operations: func(m M) {
				m.AddClusterQueue(newCq("queue1"))
				m.AddClusterQueue(newCq("queue2"))
				m.AddClusterQueue(newCq("queue3"))

				m.UpdateClusterQueueEdge("queue1", "cohort-a")
				m.UpdateClusterQueueEdge("queue2", "cohort-a")
				m.UpdateClusterQueueEdge("queue3", "cohort-b")
			},
			wantCqs: sets.New(
				"queue1",
				"queue2",
				"queue3",
			),
			wantCohorts: sets.New(
				"cohort-a",
				"cohort-b",
			),
			wantCqEdge: []edge{
				{"queue1", "cohort-a"},
				{"queue2", "cohort-a"},
				{"queue3", "cohort-b"},
			},
		},
		"update cohorts": {
			operations: func(m M) {
				m.AddClusterQueue(newCq("queue1"))
				m.AddClusterQueue(newCq("queue2"))
				m.UpdateClusterQueueEdge("queue1", "cohort-a")
				m.UpdateClusterQueueEdge("queue2", "cohort-b")

				m.UpdateClusterQueueEdge("queue1", "cohort-b")
				m.UpdateClusterQueueEdge("queue2", "cohort-c")
			},
			wantCqs:     sets.New("queue1", "queue2"),
			wantCohorts: sets.New("cohort-b", "cohort-c"),
			wantCqEdge: []edge{
				{"queue1", "cohort-b"},
				{"queue2", "cohort-c"},
			},
		},
		"updating cqs idempotent": {
			operations: func(m M) {
				m.AddClusterQueue(newCq("queue1"))
				m.AddClusterQueue(newCq("queue2"))
				m.UpdateClusterQueueEdge("queue1", "cohort-a")
				m.UpdateClusterQueueEdge("queue2", "cohort-b")

				m.UpdateClusterQueueEdge("queue1", "cohort-b")
				m.UpdateClusterQueueEdge("queue2", "cohort-c")
				m.UpdateClusterQueueEdge("queue1", "cohort-b")
				m.UpdateClusterQueueEdge("queue2", "cohort-c")
			},
			wantCqs:     sets.New("queue1", "queue2"),
			wantCohorts: sets.New("cohort-b", "cohort-c"),
			wantCqEdge: []edge{
				{"queue1", "cohort-b"},
				{"queue2", "cohort-c"},
			},
		},
		"updating cohort idempotent": {
			operations: func(m M) {
				m.AddCohort("cohort")
				m.UpdateCohortEdge("cohort", "root")
				m.UpdateCohortEdge("cohort", "newroot")
				m.UpdateCohortEdge("cohort", "newroot")
			},
			wantCohorts:    sets.New("cohort", "newroot"),
			wantCohortEdge: []edge{{"cohort", "newroot"}},
		},
		"adding cohort idempotent": {
			operations: func(m M) {
				m.AddCohort("cohort")
				m.AddCohort("cohort")
			},
			wantCohorts: sets.New("cohort"),
		},
		"delete cohorts": {
			operations: func(m M) {
				m.AddClusterQueue(newCq("queue1"))
				m.AddClusterQueue(newCq("queue2"))

				m.UpdateClusterQueueEdge("queue1", "cohort-a")
				m.UpdateClusterQueueEdge("queue2", "cohort-b")

				// Delete cohort-a by deleting edge.
				// Delete cohort-b by deleting cq.
				m.UpdateClusterQueueEdge("queue1", "")
				m.DeleteClusterQueue("queue2")
			},
			wantCqs: sets.New("queue1"),
		},
		"deletion idempotent": {
			operations: func(m M) {
				m.DeleteClusterQueue("doesnt-exist")

				m.AddClusterQueue(newCq("deleted-once"))
				m.AddClusterQueue(newCq("deleted-twice"))

				m.DeleteClusterQueue("doesnt-exist2")
				m.DeleteClusterQueue("deleted-once")
				m.DeleteClusterQueue("deleted-twice")
				m.DeleteClusterQueue("deleted-twice")
			},
			wantCqs: sets.New[string](),
		},
		"cohort remains as long as one of its child cqs remains": {
			operations: func(m M) {
				m.AddClusterQueue(newCq("queue1"))
				m.AddClusterQueue(newCq("queue2"))
				m.AddClusterQueue(newCq("queue3"))
				m.UpdateClusterQueueEdge("queue1", "cohort")
				m.UpdateClusterQueueEdge("queue2", "cohort")
				m.UpdateClusterQueueEdge("queue3", "to-be-deleted-cohort")

				m.DeleteClusterQueue("queue2")
				m.DeleteClusterQueue("queue3")
			},
			wantCqs:     sets.New("queue1"),
			wantCohorts: sets.New("cohort"),
			wantCqEdge:  []edge{{"queue1", "cohort"}},
		},
		"cohort remains as long as one of its child cohorts remains": {
			operations: func(m M) {
				m.AddCohort("cohort1")
				m.AddCohort("cohort2")
				m.AddCohort("cohort3")
				m.UpdateCohortEdge("cohort1", "root")
				m.UpdateCohortEdge("cohort2", "root")
				m.UpdateCohortEdge("cohort3", "root")
				m.DeleteCohort("cohort2")
				m.DeleteCohort("cohort3")
			},
			wantCohorts:    sets.New("cohort1", "root"),
			wantCohortEdge: []edge{{"cohort1", "root"}},
		},
		"explicit cohort": {
			operations: func(m M) {
				m.AddCohort("cohort")
			},
			wantCohorts: sets.New("cohort"),
		},
		"delete explicit cohort": {
			operations: func(m M) {
				m.AddCohort("cohort")
				m.DeleteCohort("cohort")
			},
			wantCohorts: sets.New[string](),
		},
		"delete explicit cohort idempotent": {
			operations: func(m M) {
				m.DeleteCohort("cohort")
				m.AddCohort("cohort")
				m.DeleteCohort("cohort")
				m.DeleteCohort("cohort")
			},
			wantCohorts: sets.New[string](),
		},
		"explicit cohort persists after child deleted": {
			operations: func(m M) {
				m.AddClusterQueue(newCq("queue"))
				m.UpdateClusterQueueEdge("queue", "cohort")
				m.AddCohort("cohort")
				m.DeleteClusterQueue("queue")
			},
			wantCohorts: sets.New("cohort"),
		},
		"explicit cohort downgraded to implicit cohort": {
			operations: func(m M) {
				m.AddCohort("cohort")
				m.AddClusterQueue(newCq("queue"))
				m.UpdateClusterQueueEdge("queue", "cohort")
				m.DeleteCohort("cohort")
			},
			wantCohorts: sets.New("cohort"),
			wantCqs:     sets.New("queue"),
			wantCqEdge:  []edge{{"queue", "cohort"}},
		},
		"cohort upgraded to explicit then downgraded to implicit then deleted": {
			operations: func(m M) {
				m.AddCohort("cohort")
				m.AddClusterQueue(newCq("queue"))
				m.UpdateClusterQueueEdge("queue", "cohort")
				m.DeleteCohort("cohort")
				m.DeleteClusterQueue("queue")
			},
			wantCohorts: sets.New[string](),
			wantCqs:     sets.New[string](),
			wantCqEdge:  []edge{},
		},
		"hierarchical cohorts": {
			//               root
			//              /    \
			//           left    right
			//           /            \
			//  left-left              right-right
			operations: func(m M) {
				m.AddCohort("root")
				m.AddCohort("left")
				m.AddCohort("right")
				m.AddCohort("left-left")
				m.AddCohort("right-right")
				m.UpdateCohortEdge("left", "root")
				m.UpdateCohortEdge("right", "root")
				m.UpdateCohortEdge("left-left", "left")
				m.UpdateCohortEdge("right-right", "right")

				m.AddClusterQueue(newCq("cq"))
				m.UpdateClusterQueueEdge("cq", "right-right")
			},
			wantCqs: sets.New("cq"),
			wantCqEdge: []edge{
				{"cq", "right-right"},
			},
			wantCohorts: sets.New("root", "left", "right", "left-left", "right-right"),
			wantCohortEdge: []edge{
				{"left", "root"},
				{"right", "root"},
				{"left-left", "left"},
				{"right-right", "right"},
			},
		},
		"delete node in middle of hierarchy": {
			// Before: fully connected
			//
			//               root
			//              /    \
			//            left    right
			//           /            \
			//  left-left              right-right
			//
			// After: left is an implicit Cohort
			//
			//               root
			//                   \
			//            left    right
			//           /             \
			//  left-left               right-right
			operations: func(m M) {
				m.AddCohort("root")
				m.AddCohort("left")
				m.AddCohort("right")
				m.AddCohort("left-left")
				m.AddCohort("right-right")
				m.UpdateCohortEdge("left", "root")
				m.UpdateCohortEdge("right", "root")
				m.UpdateCohortEdge("left-left", "left")
				m.UpdateCohortEdge("right-right", "right")

				m.DeleteCohort("left")
			},
			wantCohorts: sets.New("root", "left", "right", "left-left", "right-right"),
			wantCohortEdge: []edge{
				{"right", "root"},
				{"left-left", "left"},
				{"right-right", "right"},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			mgr := NewManager(newCohort)
			tc.operations(mgr)
			t.Run("verify clusterqueues", func(t *testing.T) {
				gotCqs := sets.New[string]()
				gotEdges := make([]edge, 0, len(tc.wantCqEdge))
				for _, cq := range mgr.ClusterQueues {
					gotCqs.Insert(cq.GetName())
					if cq.HasParent() {
						gotEdges = append(gotEdges, edge{cq.GetName(), cq.Parent().GetName()})
					}
				}
				if diff := cmp.Diff(tc.wantCqs, gotCqs); diff != "" {
					t.Fatalf("Unexpected cqs -want +got %s", diff)
				}
				if diff := cmp.Diff(tc.wantCqEdge, gotEdges, opts...); diff != "" {
					t.Fatalf("Unexpected CQ->Cohort edges -want +got %s", diff)
				}
			})
			t.Run("verify cohorts", func(t *testing.T) {
				gotCohorts := sets.New[string]()
				gotCqEdges := make([]edge, 0, len(tc.wantCqEdge))
				gotCohortChildEdges := make([]edge, 0, len(tc.wantCohortEdge))
				gotCohortParentEdges := make([]edge, 0, len(tc.wantCohortEdge))
				for _, cohort := range mgr.Cohorts {
					gotCohorts.Insert(cohort.GetName())
					for _, cq := range cohort.ChildCQs() {
						gotCqEdges = append(gotCqEdges, edge{cq.GetName(), cohort.GetName()})
					}
					for _, childCohort := range cohort.ChildCohorts() {
						gotCohortChildEdges = append(gotCohortChildEdges, edge{childCohort.GetName(), cohort.GetName()})
					}
					if cohort.HasParent() {
						gotCohortParentEdges = append(gotCohortParentEdges, edge{cohort.GetName(), cohort.Parent().GetName()})
					}
				}
				if diff := cmp.Diff(tc.wantCohorts, gotCohorts); diff != "" {
					t.Fatalf("Unexpected cohorts -want +got %s", diff)
				}
				if diff := cmp.Diff(tc.wantCqEdge, gotCqEdges, opts...); diff != "" {
					t.Fatalf("Unexpected CQ<-Cohort edges -want +got %s", diff)
				}
				if diff := cmp.Diff(tc.wantCohortEdge, gotCohortChildEdges, opts...); diff != "" {
					t.Fatalf("Unexpected Cohort<-Cohort edges -want +got %s", diff)
				}
				if diff := cmp.Diff(tc.wantCohortEdge, gotCohortParentEdges, opts...); diff != "" {
					t.Fatalf("Unexpected Cohort->Cohort edges -want +got %s", diff)
				}
			})
		})
	}
}

func TestCycles(t *testing.T) {
	type M = Manager[*testClusterQueue, *testCohort]
	cases := map[string]struct {
		operations func(M)
		wantCycles map[string]bool
	}{
		"no cycles": {
			operations: func(m M) {
				m.AddCohort("root")
				m.AddCohort("left")
				m.AddCohort("right")
				m.UpdateCohortEdge("left", "root")
				m.UpdateCohortEdge("right", "root")
			},
			wantCycles: map[string]bool{
				"root":  false,
				"left":  false,
				"right": false,
			},
		},
		"self-cycle": {
			operations: func(m M) {
				m.AddCohort("root")
				m.UpdateCohortEdge("root", "root")
			},
			wantCycles: map[string]bool{
				"root": true,
			},
		},
		"remove self-cycle": {
			operations: func(m M) {
				m.AddCohort("root")
				m.UpdateCohortEdge("root", "root")
				// we call HasCycle to test invalidation
				m.CycleChecker.HasCycle(m.Cohorts["root"])
				m.UpdateCohortEdge("root", "")
			},
			wantCycles: map[string]bool{
				"root": false,
			},
		},
		"cycle": {
			operations: func(m M) {
				m.AddCohort("cohort-a")
				m.AddCohort("cohort-b")
				m.UpdateCohortEdge("cohort-a", "cohort-b")
				m.UpdateCohortEdge("cohort-b", "cohort-a")
			},
			wantCycles: map[string]bool{
				"cohort-a": true,
				"cohort-b": true,
			},
		},
		"remove cycle via edge update": {
			operations: func(m M) {
				m.AddCohort("cohort-a")
				m.AddCohort("cohort-b")
				m.UpdateCohortEdge("cohort-a", "cohort-b")
				m.UpdateCohortEdge("cohort-b", "cohort-a")

				// we call HasCycle to test invalidation
				m.CycleChecker.HasCycle(m.Cohorts["cohort-a"])

				m.UpdateCohortEdge("cohort-a", "cohort-c")
			},
			wantCycles: map[string]bool{
				"cohort-a": false,
				"cohort-b": false,
				"cohort-c": false,
			},
		},
		"remove cycle via edge deletion": {
			operations: func(m M) {
				m.AddCohort("cohort-a")
				m.AddCohort("cohort-b")
				m.UpdateCohortEdge("cohort-a", "cohort-b")
				m.UpdateCohortEdge("cohort-b", "cohort-a")
				m.CycleChecker.HasCycle(m.Cohorts["cohort-a"])

				m.UpdateCohortEdge("cohort-a", "")
			},
			wantCycles: map[string]bool{
				"cohort-a": false,
				"cohort-b": false,
			},
		},
		"remove cycle via node deletion": {
			operations: func(m M) {
				m.AddCohort("cohort-a")
				m.AddCohort("cohort-b")
				m.UpdateCohortEdge("cohort-a", "cohort-b")
				m.UpdateCohortEdge("cohort-b", "cohort-a")
				m.CycleChecker.HasCycle(m.Cohorts["cohort-a"])
				m.DeleteCohort("cohort-b")
			},
			wantCycles: map[string]bool{
				"cohort-a": false,
				"cohort-b": false,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			mgr := NewManager(newCohort)
			tc.operations(mgr)
			for _, cohort := range mgr.Cohorts {
				got := mgr.CycleChecker.HasCycle(cohort)
				if got != tc.wantCycles[cohort.GetName()] {
					t.Errorf("-want +got: %v %v", tc.wantCycles[cohort.GetName()], got)
				}
			}
			if diff := cmp.Diff(mgr.CycleChecker.cycles, tc.wantCycles); diff != "" {
				t.Errorf("-want +got: %v", diff)
			}
		})
	}
}

type testCohort struct {
	name string
	Cohort[*testClusterQueue, *testCohort]
}

func newCohort(name string) *testCohort {
	return &testCohort{
		name:   name,
		Cohort: NewCohort[*testClusterQueue, *testCohort](),
	}
}

func (t *testCohort) GetName() string {
	return t.name
}

func (t *testCohort) CCParent() CycleCheckable {
	return t.Parent()
}

type testClusterQueue struct {
	name string
	ClusterQueue[*testCohort]
}

func newCq(name string) *testClusterQueue {
	return &testClusterQueue{name: name}
}

func (t *testClusterQueue) GetName() string {
	return t.name
}
