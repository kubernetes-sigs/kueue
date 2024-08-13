package hierarchy

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/util/sets"
)

func TestManager(t *testing.T) {
	type M = Manager[*testClusterQueue, *testCohort]
	type cqEdge struct {
		cq     string
		cohort string
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
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			mgr := NewManager[*testClusterQueue, *testCohort](cohortFactory)
			tc.operations(mgr)
			t.Run("verify clusterqueues", func(t *testing.T) {
				gotCqs := sets.New[string]()
				gotEdges := make([]cqEdge, 0, len(tc.wantCqEdge))
				for _, cq := range mgr.ClusterQueues {
					gotCqs.Insert(cq.GetName())
					if cq.HasCohort() {
						gotEdges = append(gotEdges, cqEdge{cq.GetName(), cq.Cohort().GetName()})
					}
				}
				if diff := cmp.Diff(tc.wantCqs, gotCqs); diff != "" {
					t.Fatalf("Unexpected cqs -want +got %s", diff)
				}
				if diff := cmp.Diff(sets.New(tc.wantCqEdge...), sets.New(gotEdges...)); diff != "" {
					t.Fatalf("Unexpected CQ->Cohort edges -want +got %s", diff)
				}
			})
			t.Run("verify cohorts", func(t *testing.T) {
				gotCohorts := sets.New[string]()
				gotEdges := make([]cqEdge, 0, len(tc.wantCqEdge))
				for _, cohort := range mgr.Cohorts {
					gotCohorts.Insert(cohort.GetName())
					for _, cq := range cohort.Members() {
						gotEdges = append(gotEdges, cqEdge{cq.GetName(), cohort.GetName()})
					}
				}
				if diff := cmp.Diff(tc.wantCohorts, gotCohorts); diff != "" {
					t.Fatalf("Unexpected cohorts -want +got %s", diff)
				}
				if diff := cmp.Diff(sets.New(tc.wantCqEdge...), sets.New(gotEdges...)); diff != "" {
					t.Fatalf("Unexpected Cohort->CQ edges -want +got %s", diff)
				}
			})
		})
	}
}

type testCohort struct {
	name string
	WiredCohort[*testClusterQueue, *testCohort]
}

func cohortFactory(name string) *testCohort {
	return &testCohort{
		name:        name,
		WiredCohort: NewWiredCohort[*testClusterQueue, *testCohort](),
	}
}

func (t *testCohort) GetName() string {
	return t.name
}

type testClusterQueue struct {
	name string
	WiredClusterQueue[*testClusterQueue, *testCohort]
}

func newCq(name string) *testClusterQueue {
	return &testClusterQueue{name: name}
}

func (t *testClusterQueue) GetName() string {
	return t.name
}
