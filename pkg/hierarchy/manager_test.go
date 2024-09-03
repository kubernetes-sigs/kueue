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
	type cqEdge struct {
		cq     string
		cohort string
	}
	opts := []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.SortSlices(func(a, b cqEdge) bool {
			return a.cq < b.cq
		}),
		cmp.AllowUnexported(cqEdge{}),
	}
	tests := map[string]struct {
		operations  func(M)
		wantCqs     sets.Set[string]
		wantCohorts sets.Set[string]
		wantCqEdge  []cqEdge
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
			wantCqEdge: []cqEdge{
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
			wantCqEdge: []cqEdge{
				{"queue1", "cohort-b"},
				{"queue2", "cohort-c"},
			},
		},
		"updating idempotent": {
			operations: func(m M) {
				m.AddClusterQueue(newCq("queue1"))
				m.AddClusterQueue(newCq("queue2"))
				m.UpdateClusterQueueEdge("queue1", "cohort-a")
				m.UpdateClusterQueueEdge("queue2", "cohort-b")

				m.UpdateClusterQueueEdge("queue1", "cohort-b")
				m.UpdateClusterQueueEdge("queue2", "cohort-c")
				m.UpdateClusterQueueEdge("queue1", "cohort-b")
				m.UpdateClusterQueueEdge("queue2", "cohort-c")
				m.UpdateClusterQueueEdge("queue2", "cohort-c")
				m.UpdateClusterQueueEdge("queue1", "cohort-b")
			},
			wantCqs:     sets.New("queue1", "queue2"),
			wantCohorts: sets.New("cohort-b", "cohort-c"),
			wantCqEdge: []cqEdge{
				{"queue1", "cohort-b"},
				{"queue2", "cohort-c"},
			},
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
		"cohort remains as long as one of its children remains": {
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
			wantCqEdge:  []cqEdge{{"queue1", "cohort"}},
		},
		"explicit cohort": {
			operations: func(m M) {
				m.AddCohort(newCohort("cohort"))
			},
			wantCohorts: sets.New("cohort"),
		},
		"delete explicit cohort": {
			operations: func(m M) {
				m.AddCohort(newCohort("cohort"))
				m.DeleteCohort("cohort")
			},
			wantCohorts: sets.New[string](),
		},
		"delete explicit cohort idempotent": {
			operations: func(m M) {
				m.DeleteCohort("cohort")
				m.AddCohort(newCohort("cohort"))
				m.DeleteCohort("cohort")
				m.DeleteCohort("cohort")
			},
			wantCohorts: sets.New[string](),
		},
		"explicit cohort persists after child deleted": {
			operations: func(m M) {
				m.AddClusterQueue(newCq("queue"))
				m.UpdateClusterQueueEdge("queue", "cohort")
				m.AddCohort(newCohort("cohort"))
				m.DeleteClusterQueue("queue")
			},
			wantCohorts: sets.New("cohort"),
		},
		"explicit cohort downgraded to implicit cohort": {
			operations: func(m M) {
				m.AddCohort(newCohort("cohort"))
				m.AddClusterQueue(newCq("queue"))
				m.UpdateClusterQueueEdge("queue", "cohort")
				m.DeleteCohort("cohort")
			},
			wantCohorts: sets.New("cohort"),
			wantCqs:     sets.New("queue"),
			wantCqEdge:  []cqEdge{{"queue", "cohort"}},
		},
		"cohort upgraded to explicit then downgraded to implicit then deleted": {
			operations: func(m M) {
				m.AddCohort(newCohort("cohort"))
				m.AddClusterQueue(newCq("queue"))
				m.UpdateClusterQueueEdge("queue", "cohort")
				m.DeleteCohort("cohort")
				m.DeleteClusterQueue("queue")
			},
			wantCohorts: sets.New[string](),
			wantCqs:     sets.New[string](),
			wantCqEdge:  []cqEdge{},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			mgr := NewManager(newCohort)
			tc.operations(mgr)
			t.Run("verify clusterqueues", func(t *testing.T) {
				gotCqs := sets.New[string]()
				gotEdges := make([]cqEdge, 0, len(tc.wantCqEdge))
				for _, cq := range mgr.ClusterQueues {
					gotCqs.Insert(cq.GetName())
					if cq.HasParent() {
						gotEdges = append(gotEdges, cqEdge{cq.GetName(), cq.Parent().GetName()})
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
				gotEdges := make([]cqEdge, 0, len(tc.wantCqEdge))
				for _, cohort := range mgr.Cohorts {
					gotCohorts.Insert(cohort.GetName())
					for _, cq := range cohort.ChildCQs() {
						gotEdges = append(gotEdges, cqEdge{cq.GetName(), cohort.GetName()})
					}
				}
				if diff := cmp.Diff(tc.wantCohorts, gotCohorts); diff != "" {
					t.Fatalf("Unexpected cohorts -want +got %s", diff)
				}
				if diff := cmp.Diff(tc.wantCqEdge, gotEdges, opts...); diff != "" {
					t.Fatalf("Unexpected Cohort->CQ edges -want +got %s", diff)
				}
			})
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
