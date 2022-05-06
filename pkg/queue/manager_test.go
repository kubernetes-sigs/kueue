/*
Copyright 2022 The Kubernetes Authors.

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

package queue

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

const headsTimeout = 3 * time.Second

// TestAddQueueOrphans verifies that pods added before adding the queue are
// present when the queue is added.
func TestAddQueueOrphans(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kueue.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding kueue scheme: %s", err)
	}
	kClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		utiltesting.MakeWorkload("a", "earth").Queue("foo").Obj(),
		utiltesting.MakeWorkload("b", "earth").Queue("bar").Obj(),
		utiltesting.MakeWorkload("c", "earth").Queue("foo").Obj(),
		utiltesting.MakeWorkload("d", "earth").Queue("foo").
			Admit(utiltesting.MakeAdmission("cq").Obj()).Obj(),
		utiltesting.MakeWorkload("a", "moon").Queue("foo").Obj(),
	).Build()
	manager := NewManager(kClient)
	q := utiltesting.MakeQueue("foo", "earth").Obj()
	if err := manager.AddQueue(context.Background(), q); err != nil {
		t.Fatalf("Failed adding queue: %v", err)
	}
	qImpl := manager.queues[Key(q)]
	workloadNames := workloadNamesFromQ(qImpl)
	if diff := cmp.Diff(sets.NewString("earth/a", "earth/c"), workloadNames); diff != "" {
		t.Errorf("Unexpected items in queue foo (-want,+got):\n%s", diff)
	}
}

// TestAddClusterQueueOrphans verifies that pods added before adding the
// clusterQueue are present when the clusterQueue is added.
func TestAddClusterQueueOrphans(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kueue.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding kueue scheme: %v", err)
	}
	now := time.Now()
	queues := []*kueue.Queue{
		utiltesting.MakeQueue("foo", "").ClusterQueue("cq").Obj(),
		utiltesting.MakeQueue("bar", "").ClusterQueue("cq").Obj(),
	}
	kClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		utiltesting.MakeWorkload("a", "").Queue("foo").Creation(now.Add(time.Second)).Obj(),
		utiltesting.MakeWorkload("b", "").Queue("bar").Creation(now).Obj(),
		utiltesting.MakeWorkload("c", "").Queue("foo").
			Admit(utiltesting.MakeAdmission("cq").Obj()).Obj(),
		utiltesting.MakeWorkload("d", "").Queue("baz").Obj(),
		queues[0],
		queues[1],
	).Build()
	ctx := context.Background()
	manager := NewManager(kClient)
	for _, q := range queues {
		if err := manager.AddQueue(ctx, q); err != nil {
			t.Fatalf("Failed adding queue %s: %v", q.Name, err)
		}
	}
	cq := utiltesting.MakeClusterQueue("cq").Obj()
	if err := manager.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Failed adding cluster queue %s: %v", cq.Name, err)
	}
	workloads := popNamesFromCQ(manager.clusterQueues[cq.Name])
	wantWorkloads := []string{"/b", "/a"}
	if diff := cmp.Diff(wantWorkloads, workloads); diff != "" {
		t.Errorf("Workloads popped in the wrong order from clusterQueue:\n%s", diff)
	}
}

// TestUpdateQueue tests that workloads are transferred between clusterQueues
// when the queue points to a different clusterQueue.
func TestUpdateQueue(t *testing.T) {
	clusterQueues := []*kueue.ClusterQueue{
		utiltesting.MakeClusterQueue("cq1").Obj(),
		utiltesting.MakeClusterQueue("cq2").Obj(),
	}
	queues := []*kueue.Queue{
		utiltesting.MakeQueue("foo", "").ClusterQueue("cq1").Obj(),
		utiltesting.MakeQueue("bar", "").ClusterQueue("cq2").Obj(),
	}
	now := time.Now()
	workloads := []*kueue.Workload{
		utiltesting.MakeWorkload("a", "").Queue("foo").Creation(now.Add(time.Second)).Obj(),
		utiltesting.MakeWorkload("b", "").Queue("bar").Creation(now).Obj(),
	}
	// Setup.
	scheme := runtime.NewScheme()
	if err := kueue.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding kueue scheme: %s", err)
	}
	ctx := context.Background()
	manager := NewManager(fake.NewClientBuilder().WithScheme(scheme).Build())
	for _, cq := range clusterQueues {
		if err := manager.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("Failed adding clusterQueue %s: %v", cq.Name, err)
		}
	}
	for _, q := range queues {
		if err := manager.AddQueue(ctx, q); err != nil {
			t.Fatalf("Failed adding queue %s: %v", q.Name, err)
		}
	}
	for _, w := range workloads {
		manager.AddOrUpdateWorkload(w)
	}

	// Update cluster queue of first queue.
	queues[0].Spec.ClusterQueue = "cq2"
	if err := manager.UpdateQueue(queues[0]); err != nil {
		t.Fatalf("Failed updating queue: %v", err)
	}

	// Verification.
	workloadOrders := make(map[string][]string)
	for name, cq := range manager.clusterQueues {
		workloadOrders[name] = popNamesFromCQ(cq)
	}
	wantWorkloadOrders := map[string][]string{
		"cq1": nil,
		"cq2": {"/b", "/a"},
	}
	if diff := cmp.Diff(wantWorkloadOrders, workloadOrders); diff != "" {
		t.Errorf("workloads popped in the wrong order from clusterQueues:\n%s", diff)
	}
}

func TestAddWorkload(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kueue.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding kueue scheme: %s", err)
	}
	manager := NewManager(fake.NewClientBuilder().WithScheme(scheme).Build())
	cq := utiltesting.MakeClusterQueue("cq").Obj()
	if err := manager.AddClusterQueue(context.Background(), cq); err != nil {
		t.Fatalf("Failed adding clusterQueue %s: %v", cq.Name, err)
	}
	queues := []*kueue.Queue{
		utiltesting.MakeQueue("foo", "earth").ClusterQueue("cq").Obj(),
		utiltesting.MakeQueue("bar", "mars").Obj(),
	}
	for _, q := range queues {
		if err := manager.AddQueue(context.Background(), q); err != nil {
			t.Fatalf("Failed adding queue %s: %v", q.Name, err)
		}
	}
	cases := []struct {
		workload  *kueue.Workload
		wantAdded bool
	}{
		{
			workload: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "earth",
					Name:      "existing_queue",
				},
				Spec: kueue.WorkloadSpec{QueueName: "foo"},
			},
			wantAdded: true,
		},
		{
			workload: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "earth",
					Name:      "non_existing_queue",
				},
				Spec: kueue.WorkloadSpec{QueueName: "baz"},
			},
		},
		{
			workload: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "mars",
					Name:      "non_existing_cluster_queue",
				},
				Spec: kueue.WorkloadSpec{QueueName: "bar"},
			},
		},
		{
			workload: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "mars",
					Name:      "wrong_namespace",
				},
				Spec: kueue.WorkloadSpec{QueueName: "foo"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.workload.Name, func(t *testing.T) {
			if added := manager.AddOrUpdateWorkload(tc.workload); added != tc.wantAdded {
				t.Errorf("AddWorkload returned %t, want %t", added, tc.wantAdded)
			}
		})
	}
}

func TestStatus(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := kueue.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding kueue scheme: %s", err)
	}
	now := time.Now().Truncate(time.Second)

	queues := []kueue.Queue{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "foo"},
			Spec: kueue.QueueSpec{
				ClusterQueue: "fooCq",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "bar"},
			Spec: kueue.QueueSpec{
				ClusterQueue: "barCq",
			},
		},
	}
	workloads := []kueue.Workload{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "a",
				CreationTimestamp: metav1.NewTime(now.Add(time.Hour)),
			},
			Spec: kueue.WorkloadSpec{QueueName: "foo"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "b",
				CreationTimestamp: metav1.NewTime(now),
			},
			Spec: kueue.WorkloadSpec{QueueName: "bar"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "c",
				CreationTimestamp: metav1.NewTime(now),
			},
			Spec: kueue.WorkloadSpec{QueueName: "foo"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "d",
				CreationTimestamp: metav1.NewTime(now),
			},
			Spec: kueue.WorkloadSpec{QueueName: "foo"},
		},
	}

	manager := NewManager(fake.NewClientBuilder().WithScheme(scheme).Build())
	for _, q := range queues {
		if err := manager.AddQueue(ctx, &q); err != nil {
			t.Errorf("Failed adding queue: %s", err)
		}
	}
	for _, wl := range workloads {
		wl := wl
		manager.AddOrUpdateWorkload(&wl)
	}

	cases := map[string]struct {
		queue      *kueue.Queue
		wantStatus int32
		wantErr    error
	}{
		"foo": {
			queue:      &queues[0],
			wantStatus: 3,
			wantErr:    nil,
		},
		"bar": {
			queue:      &queues[1],
			wantStatus: 1,
			wantErr:    nil,
		},
		"fake": {
			queue:      &kueue.Queue{ObjectMeta: metav1.ObjectMeta{Name: "fake"}},
			wantStatus: 0,
			wantErr:    errQueueDoesNotExist,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			status, err := manager.PendingWorkloads(tc.queue)
			if err != tc.wantErr {
				t.Errorf("Should have failed with: %s", err)
			}
			if diff := cmp.Diff(tc.wantStatus, status); diff != "" {
				t.Errorf("Status func returned wrong queue status: %s", diff)
			}
		})
	}
}

func TestRequeueWorkloadStrictFIFO(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kueue.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding kueue scheme: %s", err)
	}
	cq := utiltesting.MakeClusterQueue("cq").Obj()
	queues := []*kueue.Queue{
		utiltesting.MakeQueue("foo", "").ClusterQueue("cq").Obj(),
		utiltesting.MakeQueue("bar", "").Obj(),
	}
	cases := []struct {
		workload     *kueue.Workload
		inClient     bool
		inQueue      bool
		wantRequeued bool
	}{
		{
			workload: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{Name: "existing_queue_and_obj"},
				Spec:       kueue.WorkloadSpec{QueueName: "foo"},
			},
			inClient:     true,
			wantRequeued: true,
		},
		{
			workload: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{Name: "non_existing_queue"},
				Spec:       kueue.WorkloadSpec{QueueName: "baz"},
			},
			inClient: true,
		},
		{
			workload: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{Name: "non_existing_cluster_queue"},
				Spec:       kueue.WorkloadSpec{QueueName: "bar"},
			},
			inClient: true,
		},
		{
			workload: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{Name: "not_in_client"},
				Spec:       kueue.WorkloadSpec{QueueName: "foo"},
			},
		},
		{
			workload: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{Name: "already_in_queue"},
				Spec:       kueue.WorkloadSpec{QueueName: "foo"},
			},
			inClient: true,
			inQueue:  true,
		},
		{
			workload: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{Name: "already_admitted"},
				Spec: kueue.WorkloadSpec{
					QueueName: "foo",
					Admission: &kueue.Admission{},
				},
			},
			inClient: true,
			inQueue:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.workload.Name, func(t *testing.T) {
			cl := fake.NewClientBuilder().WithScheme(scheme).Build()
			manager := NewManager(cl)
			ctx := context.Background()
			if err := manager.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("Failed adding cluster queue %s: %v", cq.Name, err)
			}
			for _, q := range queues {
				if err := manager.AddQueue(ctx, q); err != nil {
					t.Fatalf("Failed adding queue %s: %v", q.Name, err)
				}
			}
			// Adding workload to client after the queues are created, otherwise it
			// will be in the queue.
			if tc.inClient {
				if err := cl.Create(ctx, tc.workload); err != nil {
					t.Fatalf("Failed adding workload to client: %v", err)
				}
			}
			if tc.inQueue {
				_ = manager.AddOrUpdateWorkload(tc.workload)
			}
			info := workload.NewInfo(tc.workload)
			if requeued := manager.RequeueWorkload(ctx, info, true); requeued != tc.wantRequeued {
				t.Errorf("RequeueWorkload returned %t, want %t", requeued, tc.wantRequeued)
			}
		})
	}
}

func TestUpdateWorkload(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kueue.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding kueue scheme: %s", err)
	}
	now := time.Now()
	cases := map[string]struct {
		clusterQueues    []*kueue.ClusterQueue
		queues           []*kueue.Queue
		workloads        []*kueue.Workload
		update           func(*kueue.Workload)
		wantUpdated      bool
		wantQueueOrder   map[string][]string
		wantQueueMembers map[string]sets.String
	}{
		"in queue": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("cq").Obj(),
			},
			queues: []*kueue.Queue{
				utiltesting.MakeQueue("foo", "").ClusterQueue("cq").Obj(),
			},
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("a", "").Queue("foo").Creation(now).Obj(),
				utiltesting.MakeWorkload("b", "").Queue("foo").Creation(now.Add(time.Second)).Obj(),
			},
			update: func(w *kueue.Workload) {
				w.CreationTimestamp = metav1.NewTime(now.Add(time.Minute))
			},
			wantUpdated: true,
			wantQueueOrder: map[string][]string{
				"cq": {"/b", "/a"},
			},
			wantQueueMembers: map[string]sets.String{
				"/foo": sets.NewString("/a", "/b"),
			},
		},
		"between queues": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("cq").Obj(),
			},
			queues: []*kueue.Queue{
				utiltesting.MakeQueue("foo", "").ClusterQueue("cq").Obj(),
				utiltesting.MakeQueue("bar", "").ClusterQueue("cq").Obj(),
			},
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("a", "").Queue("foo").Obj(),
			},
			update: func(w *kueue.Workload) {
				w.Spec.QueueName = "bar"
			},
			wantUpdated: true,
			wantQueueOrder: map[string][]string{
				"cq": {"/a"},
			},
			wantQueueMembers: map[string]sets.String{
				"/foo": sets.NewString(),
				"/bar": sets.NewString("/a"),
			},
		},
		"between cluster queues": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("cq1").Obj(),
				utiltesting.MakeClusterQueue("cq2").Obj(),
			},
			queues: []*kueue.Queue{
				utiltesting.MakeQueue("foo", "").ClusterQueue("cq1").Obj(),
				utiltesting.MakeQueue("bar", "").ClusterQueue("cq2").Obj(),
			},
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("a", "").Queue("foo").Obj(),
			},
			update: func(w *kueue.Workload) {
				w.Spec.QueueName = "bar"
			},
			wantUpdated: true,
			wantQueueOrder: map[string][]string{
				"cq1": nil,
				"cq2": {"/a"},
			},
			wantQueueMembers: map[string]sets.String{
				"/foo": sets.NewString(),
				"/bar": sets.NewString("/a"),
			},
		},
		"to non existent queue": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("cq").Obj(),
			},
			queues: []*kueue.Queue{
				utiltesting.MakeQueue("foo", "").ClusterQueue("cq").Obj(),
			},
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("a", "").Queue("foo").Obj(),
			},
			update: func(w *kueue.Workload) {
				w.Spec.QueueName = "bar"
			},
			wantQueueOrder: map[string][]string{
				"cq": nil,
			},
			wantQueueMembers: map[string]sets.String{
				"/foo": sets.NewString(),
			},
		},
		"from non existing queue": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("cq").Obj(),
			},
			queues: []*kueue.Queue{
				utiltesting.MakeQueue("foo", "").ClusterQueue("cq").Obj(),
			},
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("a", "").Queue("bar").Obj(),
			},
			update: func(w *kueue.Workload) {
				w.Spec.QueueName = "foo"
			},
			wantUpdated: true,
			wantQueueOrder: map[string][]string{
				"cq": {"/a"},
			},
			wantQueueMembers: map[string]sets.String{
				"/foo": sets.NewString("/a"),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			manager := NewManager(fake.NewClientBuilder().WithScheme(scheme).Build())
			ctx := context.Background()
			for _, cq := range tc.clusterQueues {
				if err := manager.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Adding cluster queue %s: %v", cq.Name, err)
				}
			}
			for _, q := range tc.queues {
				if err := manager.AddQueue(ctx, q); err != nil {
					t.Fatalf("Adding queue %q: %v", q.Name, err)
				}
			}
			for _, w := range tc.workloads {
				manager.AddOrUpdateWorkload(w)
			}
			wl := tc.workloads[0].DeepCopy()
			tc.update(wl)
			if updated := manager.UpdateWorkload(tc.workloads[0], wl); updated != tc.wantUpdated {
				t.Errorf("UpdatedWorkload returned %t, want %t", updated, tc.wantUpdated)
			}
			q := manager.queues[queueKeyForWorkload(wl)]
			if q != nil {
				key := workload.Key(wl)
				item := q.items[key]
				if item == nil {
					t.Errorf("Object not stored in queue")
				} else if diff := cmp.Diff(wl, item.Obj); diff != "" {
					t.Errorf("Object stored in queue differs (-want,+got):\n%s", diff)
				}
				cq := manager.clusterQueues[q.ClusterQueue]
				if cq != nil {
					item := cq.Info(key)
					if item == nil {
						t.Errorf("Object not stored in clusterQueue")
					} else if diff := cmp.Diff(wl, item.Obj); diff != "" {
						t.Errorf("Object stored in clusterQueue differs (-want,+got):\n%s", diff)
					}
				}
			}
			queueOrder := make(map[string][]string)
			for name, cq := range manager.clusterQueues {
				queueOrder[name] = popNamesFromCQ(cq)
			}
			if diff := cmp.Diff(tc.wantQueueOrder, queueOrder); diff != "" {
				t.Errorf("Elements popped in the wrong order from clusterQueues (-want,+got):\n%s", diff)
			}
			queueMembers := make(map[string]sets.String)
			for name, q := range manager.queues {
				queueMembers[name] = workloadNamesFromQ(q)
			}
			if diff := cmp.Diff(tc.wantQueueMembers, queueMembers); diff != "" {
				t.Errorf("Elements present in wrong queues (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestHeads(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), headsTimeout)
	defer cancel()
	scheme := runtime.NewScheme()
	if err := kueue.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding kueue scheme: %s", err)
	}
	now := time.Now().Truncate(time.Second)

	clusterQueues := []kueue.ClusterQueue{
		*utiltesting.MakeClusterQueue("fooCq").Obj(),
		*utiltesting.MakeClusterQueue("barCq").Obj(),
	}
	queues := []kueue.Queue{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "foo"},
			Spec: kueue.QueueSpec{
				ClusterQueue: "fooCq",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "bar"},
			Spec: kueue.QueueSpec{
				ClusterQueue: "barCq",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "baz"},
			Spec: kueue.QueueSpec{
				ClusterQueue: "bazCq",
			},
		},
	}
	workloads := []kueue.Workload{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "a",
				CreationTimestamp: metav1.NewTime(now.Add(time.Hour)),
			},
			Spec: kueue.WorkloadSpec{QueueName: "foo"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "b",
				CreationTimestamp: metav1.NewTime(now),
			},
			Spec: kueue.WorkloadSpec{QueueName: "bar"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "c",
				CreationTimestamp: metav1.NewTime(now),
			},
			Spec: kueue.WorkloadSpec{QueueName: "foo"},
		},
	}
	manager := NewManager(fake.NewClientBuilder().WithScheme(scheme).Build())
	for _, cq := range clusterQueues {
		if err := manager.AddClusterQueue(ctx, &cq); err != nil {
			t.Fatalf("Failed adding clusterQueue %s: %v", cq.Name, err)
		}
	}
	for _, q := range queues {
		if err := manager.AddQueue(ctx, &q); err != nil {
			t.Fatalf("Failed adding queue %s: %s", q.Name, err)
		}
	}
	go manager.CleanUpOnContext(ctx)
	for _, wl := range workloads {
		wl := wl
		manager.AddOrUpdateWorkload(&wl)
	}
	wantHeads := []workload.Info{
		{
			Obj:          &workloads[1],
			ClusterQueue: "barCq",
		},
		{
			Obj:          &workloads[2],
			ClusterQueue: "fooCq",
		},
	}

	heads := manager.Heads(ctx)
	sort.Slice(heads, func(i, j int) bool {
		return heads[i].Obj.Name < heads[j].Obj.Name
	})
	if diff := cmp.Diff(wantHeads, heads); diff != "" {
		t.Errorf("GetHeads returned wrong heads (-want,+got):\n%s", diff)
	}
}

var ignoreTypeMeta = cmpopts.IgnoreTypes(metav1.TypeMeta{})

// TestHeadAsync ensures that Heads call is blocked until the queues are filled
// asynchronously.
func TestHeadsAsync(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kueue.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding kueue scheme: %s", err)
	}
	now := time.Now().Truncate(time.Second)
	clusterQueues := []*kueue.ClusterQueue{
		utiltesting.MakeClusterQueue("fooCq").Obj(),
		utiltesting.MakeClusterQueue("barCq").Obj(),
	}
	wl := kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "a",
			CreationTimestamp: metav1.NewTime(now),
		},
		Spec: kueue.WorkloadSpec{QueueName: "foo"},
	}
	var newWl kueue.Workload
	queues := []kueue.Queue{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "foo"},
			Spec: kueue.QueueSpec{
				ClusterQueue: "fooCq",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "bar"},
			Spec: kueue.QueueSpec{
				ClusterQueue: "barCq",
			},
		},
	}
	cases := map[string]struct {
		initialObjs []client.Object
		op          func(context.Context, *Manager)
		wantHeads   []workload.Info
	}{
		"AddClusterQueue": {
			initialObjs: []client.Object{&wl, &queues[0]},
			op: func(ctx context.Context, mgr *Manager) {
				if err := mgr.AddQueue(ctx, &queues[0]); err != nil {
					t.Errorf("Failed adding queue: %s", err)
				}
				mgr.AddOrUpdateWorkload(&wl)
				go func() {
					if err := mgr.AddClusterQueue(ctx, clusterQueues[0]); err != nil {
						t.Errorf("Failed adding clusterQueue: %v", err)
					}
				}()
			},
			wantHeads: []workload.Info{
				{
					Obj:          &wl,
					ClusterQueue: "fooCq",
				},
			},
		},
		"AddQueue": {
			initialObjs: []client.Object{&wl},
			op: func(ctx context.Context, mgr *Manager) {
				if err := mgr.AddClusterQueue(ctx, clusterQueues[0]); err != nil {
					t.Errorf("Failed adding clusterQueue: %v", err)
				}
				go func() {
					if err := mgr.AddQueue(ctx, &queues[0]); err != nil {
						t.Errorf("Failed adding queue: %s", err)
					}
				}()
			},
			wantHeads: []workload.Info{
				{
					Obj:          &wl,
					ClusterQueue: "fooCq",
				},
			},
		},
		"AddWorkload": {
			op: func(ctx context.Context, mgr *Manager) {
				if err := mgr.AddClusterQueue(ctx, clusterQueues[0]); err != nil {
					t.Errorf("Failed adding clusterQueue: %v", err)
				}
				if err := mgr.AddQueue(ctx, &queues[0]); err != nil {
					t.Errorf("Failed adding queue: %s", err)
				}
				go func() {
					mgr.AddOrUpdateWorkload(&wl)
				}()
			},
			wantHeads: []workload.Info{
				{
					Obj:          &wl,
					ClusterQueue: "fooCq",
				},
			},
		},
		"UpdateWorkload": {
			op: func(ctx context.Context, mgr *Manager) {
				if err := mgr.AddClusterQueue(ctx, clusterQueues[0]); err != nil {
					t.Errorf("Failed adding clusterQueue: %v", err)
				}
				if err := mgr.AddQueue(ctx, &queues[0]); err != nil {
					t.Errorf("Failed adding queue: %s", err)
				}
				go func() {
					wlCopy := wl.DeepCopy()
					wlCopy.ResourceVersion = "old"
					mgr.UpdateWorkload(wlCopy, &wl)
				}()
			},
			wantHeads: []workload.Info{
				{
					Obj:          &wl,
					ClusterQueue: "fooCq",
				},
			},
		},
		"RequeueWorkload": {
			initialObjs: []client.Object{&wl},
			op: func(ctx context.Context, mgr *Manager) {
				if err := mgr.AddClusterQueue(ctx, clusterQueues[0]); err != nil {
					t.Errorf("Failed adding clusterQueue: %v", err)
				}
				if err := mgr.AddQueue(ctx, &queues[0]); err != nil {
					t.Errorf("Failed adding queue: %s", err)
				}
				// Remove the initial workload from the manager.
				mgr.Heads(ctx)
				go func() {
					mgr.RequeueWorkload(ctx, workload.NewInfo(&wl), true)
				}()
			},
			wantHeads: []workload.Info{
				{
					Obj:          &wl,
					ClusterQueue: "fooCq",
				},
			},
		},
		"RequeueWithOutOfDateWorkload": {
			initialObjs: []client.Object{&wl},
			op: func(ctx context.Context, mgr *Manager) {
				if err := mgr.AddClusterQueue(ctx, clusterQueues[0]); err != nil {
					t.Errorf("Failed adding clusterQueue: %v", err)
				}
				if err := mgr.AddQueue(ctx, &queues[0]); err != nil {
					t.Errorf("Failed adding queue: %s", err)
				}

				newWl = wl
				newWl.Annotations = map[string]string{"foo": "bar"}
				if err := mgr.client.Update(ctx, &newWl, &client.UpdateOptions{}); err != nil {
					t.Errorf("Failed to update the workload; %s", err)
				}
				// Remove the initial workload from the manager.
				mgr.Heads(ctx)
				go func() {
					mgr.RequeueWorkload(ctx, workload.NewInfo(&wl), true)
				}()
			},
			wantHeads: []workload.Info{
				{
					Obj:          &newWl,
					ClusterQueue: "fooCq",
				},
			},
		},
		"RequeueWithQueueChangedWorkload": {
			initialObjs: []client.Object{&wl},
			op: func(ctx context.Context, mgr *Manager) {
				for _, cq := range clusterQueues {
					if err := mgr.AddClusterQueue(ctx, cq); err != nil {
						t.Errorf("Failed adding clusterQueue: %v", err)
					}
				}
				for _, q := range queues {
					if err := mgr.AddQueue(ctx, &q); err != nil {
						t.Errorf("Failed adding queue: %s", err)
					}
				}

				newWl = wl
				newWl.Spec.QueueName = "bar"
				if err := mgr.client.Update(ctx, &newWl, &client.UpdateOptions{}); err != nil {
					t.Errorf("Failed to update the workload; %s", err)
				}
				// Remove the initial workload from the manager.
				mgr.Heads(ctx)
				go func() {
					mgr.RequeueWorkload(ctx, workload.NewInfo(&wl), true)
				}()
			},
			wantHeads: []workload.Info{
				{
					Obj:          &newWl,
					ClusterQueue: "barCq",
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), headsTimeout)
			defer cancel()
			client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tc.initialObjs...).Build()
			manager := NewManager(client)
			go manager.CleanUpOnContext(ctx)
			tc.op(ctx, manager)
			heads := manager.Heads(ctx)
			if diff := cmp.Diff(tc.wantHeads, heads, ignoreTypeMeta); diff != "" {
				t.Errorf("GetHeads returned wrong heads (-want,+got):\n%s", diff)
			}
		})
	}
}

// TestHeadsCancelled ensures that the Heads call returns when the context is closed.
func TestHeadsCancelled(t *testing.T) {
	manager := NewManager(fake.NewClientBuilder().Build())
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		cancel()
	}()
	manager.CleanUpOnContext(ctx)
	heads := manager.Heads(ctx)
	if len(heads) != 0 {
		t.Errorf("GetHeads returned elements, expected none")
	}
}

// popNamesFromCQ pops all the workloads from the clusterQueue and returns
// the keyed names in the order they are popped.
func popNamesFromCQ(cq ClusterQueue) []string {
	var names []string
	for w := cq.Pop(); w != nil; w = cq.Pop() {
		names = append(names, workload.Key(w.Obj))
	}
	return names
}

// workloadNamesFromQ returns all the names of the workloads in a queue.
func workloadNamesFromQ(q *Queue) sets.String {
	names := sets.NewString()
	for k := range q.items {
		names.Insert(k)
	}
	return names
}
