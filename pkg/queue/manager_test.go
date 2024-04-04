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
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

const headsTimeout = 3 * time.Second

var cmpDump = []cmp.Option{
	cmpopts.SortSlices(func(a, b string) bool { return a < b }),
	cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
}

// TestAddLocalQueueOrphans verifies that pods added before adding the queue are
// present when the queue is added.
func TestAddLocalQueueOrphans(t *testing.T) {
	kClient := utiltesting.NewFakeClient(
		utiltesting.MakeWorkload("a", "earth").Queue("foo").Obj(),
		utiltesting.MakeWorkload("b", "earth").Queue("bar").Obj(),
		utiltesting.MakeWorkload("c", "earth").Queue("foo").Obj(),
		utiltesting.MakeWorkload("d", "earth").Queue("foo").
			ReserveQuota(utiltesting.MakeAdmission("cq").Obj()).Obj(),
		utiltesting.MakeWorkload("a", "moon").Queue("foo").Obj(),
	)
	manager := NewManager(kClient, nil)
	q := utiltesting.MakeLocalQueue("foo", "earth").Obj()
	if err := manager.AddLocalQueue(context.Background(), q); err != nil {
		t.Fatalf("Failed adding queue: %v", err)
	}
	qImpl := manager.localQueues[Key(q)]
	workloadNames := workloadNamesFromLQ(qImpl)
	if diff := cmp.Diff(sets.New("earth/a", "earth/c"), workloadNames); diff != "" {
		t.Errorf("Unexpected items in queue foo (-want,+got):\n%s", diff)
	}
}

// TestAddClusterQueueOrphans verifies that when a ClusterQueue is recreated,
// it adopts the existing workloads.
func TestAddClusterQueueOrphans(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kueue.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding kueue scheme: %v", err)
	}
	now := time.Now()
	queues := []*kueue.LocalQueue{
		utiltesting.MakeLocalQueue("foo", "").ClusterQueue("cq").Obj(),
		utiltesting.MakeLocalQueue("bar", "").ClusterQueue("cq").Obj(),
	}
	kClient := utiltesting.NewFakeClient(
		utiltesting.MakeWorkload("a", "").Queue("foo").Creation(now.Add(time.Second)).Obj(),
		utiltesting.MakeWorkload("b", "").Queue("bar").Creation(now).Obj(),
		utiltesting.MakeWorkload("c", "").Queue("foo").
			ReserveQuota(utiltesting.MakeAdmission("cq").Obj()).Obj(),
		utiltesting.MakeWorkload("d", "").Queue("baz").Obj(),
		queues[0],
		queues[1],
	)
	ctx := context.Background()
	manager := NewManager(kClient, nil)
	cq := utiltesting.MakeClusterQueue("cq").Obj()
	if err := manager.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Failed adding cluster queue %s: %v", cq.Name, err)
	}
	for _, q := range queues {
		if err := manager.AddLocalQueue(ctx, q); err != nil {
			t.Fatalf("Failed adding queue %s: %v", q.Name, err)
		}
	}

	wantActiveWorkloads := map[string][]string{
		"cq": {"/a", "/b"},
	}
	if diff := cmp.Diff(wantActiveWorkloads, manager.Dump(), cmpDump...); diff != "" {
		t.Errorf("Unexpected active workloads after creating all objects (-want,+got):\n%s", diff)
	}

	// Recreating the ClusterQueue.
	manager.DeleteClusterQueue(cq)
	wantActiveWorkloads = nil
	if diff := cmp.Diff(wantActiveWorkloads, manager.Dump(), cmpDump...); diff != "" {
		t.Errorf("Unexpected active workloads after deleting ClusterQueue (-want,+got):\n%s", diff)
	}

	if err := manager.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Could not re-add ClusterQueue: %v", err)
	}
	workloads := popNamesFromCQ(manager.clusterQueues["cq"])
	wantWorkloads := []string{"/b", "/a"}
	if diff := cmp.Diff(wantWorkloads, workloads); diff != "" {
		t.Errorf("Workloads popped in the wrong order from clusterQueue:\n%s", diff)
	}
}

// TestUpdateClusterQueue tests that a ClusterQueue transfers cohorts on update.
// Inadmissible workloads should become active.
func TestUpdateClusterQueue(t *testing.T) {
	clusterQueues := []*kueue.ClusterQueue{
		utiltesting.MakeClusterQueue("cq1").Cohort("alpha").Obj(),
		utiltesting.MakeClusterQueue("cq2").Cohort("beta").Obj(),
	}
	queues := []*kueue.LocalQueue{
		utiltesting.MakeLocalQueue("foo", defaultNamespace).ClusterQueue("cq1").Obj(),
		utiltesting.MakeLocalQueue("bar", defaultNamespace).ClusterQueue("cq2").Obj(),
	}
	now := time.Now()
	workloads := []*kueue.Workload{
		utiltesting.MakeWorkload("a", defaultNamespace).Queue("foo").Creation(now.Add(time.Second)).Obj(),
		utiltesting.MakeWorkload("b", defaultNamespace).Queue("bar").Creation(now).Obj(),
	}
	// Setup.
	ctx := context.Background()
	cl := utiltesting.NewFakeClient(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: defaultNamespace}},
	)
	manager := NewManager(cl, nil)
	for _, cq := range clusterQueues {
		if err := manager.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("Failed adding clusterQueue %s: %v", cq.Name, err)
		}
	}
	for _, q := range queues {
		if err := manager.AddLocalQueue(ctx, q); err != nil {
			t.Fatalf("Failed adding queue %s: %v", q.Name, err)
		}
	}
	// Add inadmissible workloads.
	for _, w := range workloads {
		if err := cl.Create(ctx, w); err != nil {
			t.Fatalf("Failed adding workload to client: %v", err)
		}
		manager.RequeueWorkload(ctx, workload.NewInfo(w), RequeueReasonGeneric)
	}

	// Put cq2 in the same cohort as cq1.
	clusterQueues[1].Spec.Cohort = clusterQueues[0].Spec.Cohort
	if err := manager.UpdateClusterQueue(ctx, clusterQueues[1], true); err != nil {
		t.Fatalf("Failed to update ClusterQueue: %v", err)
	}

	wantCohorts := map[string]sets.Set[string]{
		"alpha": sets.New("cq1", "cq2"),
	}
	if diff := cmp.Diff(manager.cohorts, wantCohorts); diff != "" {
		t.Errorf("Unexpected ClusterQueues in cohorts (-want,+got):\n%s", diff)
	}

	// Verify all workloads are active after the update.
	activeWorkloads := manager.Dump()
	wantActiveWorkloads := map[string][]string{
		"cq1": []string{"default/a"},
		"cq2": []string{"default/b"},
	}
	if diff := cmp.Diff(wantActiveWorkloads, activeWorkloads); diff != "" {
		t.Errorf("Unexpected active workloads (-want +got):\n%s", diff)
	}
}

// TestClusterQueueToActive tests that managers cond gets a broadcast when
// a cluster queue becomes active.
func TestClusterQueueToActive(t *testing.T) {
	stoppedCq := utiltesting.MakeClusterQueue("cq1").Cohort("alpha").Condition(kueue.ClusterQueueActive, metav1.ConditionFalse, "ByTest", "by test").Obj()
	runningCq := utiltesting.MakeClusterQueue("cq1").Cohort("alpha").Condition(kueue.ClusterQueueActive, metav1.ConditionTrue, "ByTest", "by test").Obj()
	ctx := context.Background()
	cl := utiltesting.NewFakeClient(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: defaultNamespace}},
	)
	manager := NewManager(cl, nil)

	wgCounterStart := sync.WaitGroup{}
	wgCounterStart.Add(1)
	wgCounterEnd := sync.WaitGroup{}
	wgCounterEnd.Add(1)
	condRec := make(chan struct{})
	counterCtx, counterCancel := context.WithCancel(ctx)
	go func() {
		manager.cond.L.Lock()
		defer manager.cond.L.Unlock()
		wgCounterStart.Done()
		defer wgCounterEnd.Done()
		manager.cond.Wait()
		select {
		case <-counterCtx.Done():
			// the context was canceled before cond.Wait()
		default:
			condRec <- struct{}{}
		}
	}()
	wgCounterStart.Wait()
	go manager.CleanUpOnContext(counterCtx)

	if err := manager.AddClusterQueue(ctx, stoppedCq); err != nil {
		t.Fatalf("Failed adding clusterQueue %v", err)
	}

	if err := manager.UpdateClusterQueue(ctx, runningCq, false); err != nil {
		t.Fatalf("Failed to update ClusterQueue: %v", err)
	}

	gotCondBeforeCleanup := false
	select {
	case <-condRec:
		gotCondBeforeCleanup = true
	case <-time.After(100 * time.Millisecond):
		//nothing
	}

	counterCancel()
	wgCounterEnd.Wait()

	if !gotCondBeforeCleanup {
		t.Fatalf("m.Broadcast was not called before cleanup")
	}
}

// TestUpdateLocalQueue tests that workloads are transferred between clusterQueues
// when the queue points to a different clusterQueue.
func TestUpdateLocalQueue(t *testing.T) {
	clusterQueues := []*kueue.ClusterQueue{
		utiltesting.MakeClusterQueue("cq1").Obj(),
		utiltesting.MakeClusterQueue("cq2").Obj(),
	}
	queues := []*kueue.LocalQueue{
		utiltesting.MakeLocalQueue("foo", "").ClusterQueue("cq1").Obj(),
		utiltesting.MakeLocalQueue("bar", "").ClusterQueue("cq2").Obj(),
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
	manager := NewManager(utiltesting.NewFakeClient(), nil)
	for _, cq := range clusterQueues {
		if err := manager.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("Failed adding clusterQueue %s: %v", cq.Name, err)
		}
	}
	for _, q := range queues {
		if err := manager.AddLocalQueue(ctx, q); err != nil {
			t.Fatalf("Failed adding queue %s: %v", q.Name, err)
		}
	}
	for _, w := range workloads {
		manager.AddOrUpdateWorkload(w)
	}

	// Update cluster queue of first queue.
	queues[0].Spec.ClusterQueue = "cq2"
	if err := manager.UpdateLocalQueue(queues[0]); err != nil {
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

// TestDeleteLocalQueue tests that when a LocalQueue is deleted, all its
// workloads are not listed in the ClusterQueue.
func TestDeleteLocalQueue(t *testing.T) {
	cq := utiltesting.MakeClusterQueue("cq").Obj()
	q := utiltesting.MakeLocalQueue("foo", "").ClusterQueue("cq").Obj()
	wl := utiltesting.MakeWorkload("a", "").Queue("foo").Obj()

	ctx := context.Background()
	cl := utiltesting.NewFakeClient(wl)
	manager := NewManager(cl, nil)

	if err := manager.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Could not create ClusterQueue: %v", err)
	}
	if err := manager.AddLocalQueue(ctx, q); err != nil {
		t.Fatalf("Could not create LocalQueue: %v", err)
	}

	wantActiveWorkloads := map[string][]string{
		"cq": {"/a"},
	}
	if diff := cmp.Diff(wantActiveWorkloads, manager.Dump(), cmpDump...); diff != "" {
		t.Errorf("Unexpected workloads after setup (-want,+got):\n%s", diff)
	}

	manager.DeleteLocalQueue(q)
	wantActiveWorkloads = nil
	if diff := cmp.Diff(wantActiveWorkloads, manager.Dump(), cmpDump...); diff != "" {
		t.Errorf("Unexpected workloads after deleting LocalQueue (-want,+got):\n%s", diff)
	}
}

func TestAddWorkload(t *testing.T) {
	manager := NewManager(utiltesting.NewFakeClient(), nil)
	cq := utiltesting.MakeClusterQueue("cq").Obj()
	if err := manager.AddClusterQueue(context.Background(), cq); err != nil {
		t.Fatalf("Failed adding clusterQueue %s: %v", cq.Name, err)
	}
	queues := []*kueue.LocalQueue{
		utiltesting.MakeLocalQueue("foo", "earth").ClusterQueue("cq").Obj(),
		utiltesting.MakeLocalQueue("bar", "mars").Obj(),
	}
	for _, q := range queues {
		if err := manager.AddLocalQueue(context.Background(), q); err != nil {
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

	queues := []kueue.LocalQueue{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "foo"},
			Spec: kueue.LocalQueueSpec{
				ClusterQueue: "fooCq",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "bar"},
			Spec: kueue.LocalQueueSpec{
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

	manager := NewManager(utiltesting.NewFakeClient(), nil)
	for _, q := range queues {
		if err := manager.AddLocalQueue(ctx, &q); err != nil {
			t.Errorf("Failed adding queue: %s", err)
		}
	}
	for _, wl := range workloads {
		wl := wl
		manager.AddOrUpdateWorkload(&wl)
	}

	cases := map[string]struct {
		queue      *kueue.LocalQueue
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
			queue:      &kueue.LocalQueue{ObjectMeta: metav1.ObjectMeta{Name: "fake"}},
			wantStatus: 0,
			wantErr:    errQueueDoesNotExist,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			status, err := manager.PendingWorkloads(tc.queue)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("Should have failed with: %s", err)
			}
			if diff := cmp.Diff(tc.wantStatus, status); diff != "" {
				t.Errorf("Status func returned wrong queue status: %s", diff)
			}
		})
	}
}

func TestRequeueWorkloadStrictFIFO(t *testing.T) {
	cq := utiltesting.MakeClusterQueue("cq").Obj()
	queues := []*kueue.LocalQueue{
		utiltesting.MakeLocalQueue("foo", "").ClusterQueue("cq").Obj(),
		utiltesting.MakeLocalQueue("bar", "").Obj(),
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
				},
				Status: kueue.WorkloadStatus{
					Admission: &kueue.Admission{},
				},
			},
			inClient: true,
			inQueue:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.workload.Name, func(t *testing.T) {
			cl := utiltesting.NewFakeClient()
			manager := NewManager(cl, nil)
			ctx := context.Background()
			if err := manager.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("Failed adding cluster queue %s: %v", cq.Name, err)
			}
			for _, q := range queues {
				if err := manager.AddLocalQueue(ctx, q); err != nil {
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
			if requeued := manager.RequeueWorkload(ctx, info, RequeueReasonGeneric); requeued != tc.wantRequeued {
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
		queues           []*kueue.LocalQueue
		workloads        []*kueue.Workload
		update           func(*kueue.Workload)
		wantUpdated      bool
		wantQueueOrder   map[string][]string
		wantQueueMembers map[string]sets.Set[string]
	}{
		"in queue": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("cq").Obj(),
			},
			queues: []*kueue.LocalQueue{
				utiltesting.MakeLocalQueue("foo", "").ClusterQueue("cq").Obj(),
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
			wantQueueMembers: map[string]sets.Set[string]{
				"/foo": sets.New("/a", "/b"),
			},
		},
		"between queues": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("cq").Obj(),
			},
			queues: []*kueue.LocalQueue{
				utiltesting.MakeLocalQueue("foo", "").ClusterQueue("cq").Obj(),
				utiltesting.MakeLocalQueue("bar", "").ClusterQueue("cq").Obj(),
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
			wantQueueMembers: map[string]sets.Set[string]{
				"/foo": nil,
				"/bar": sets.New("/a"),
			},
		},
		"between cluster queues": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("cq1").Obj(),
				utiltesting.MakeClusterQueue("cq2").Obj(),
			},
			queues: []*kueue.LocalQueue{
				utiltesting.MakeLocalQueue("foo", "").ClusterQueue("cq1").Obj(),
				utiltesting.MakeLocalQueue("bar", "").ClusterQueue("cq2").Obj(),
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
			wantQueueMembers: map[string]sets.Set[string]{
				"/foo": nil,
				"/bar": sets.New("/a"),
			},
		},
		"to non existent queue": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("cq").Obj(),
			},
			queues: []*kueue.LocalQueue{
				utiltesting.MakeLocalQueue("foo", "").ClusterQueue("cq").Obj(),
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
			wantQueueMembers: map[string]sets.Set[string]{
				"/foo": nil,
			},
		},
		"from non existing queue": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("cq").Obj(),
			},
			queues: []*kueue.LocalQueue{
				utiltesting.MakeLocalQueue("foo", "").ClusterQueue("cq").Obj(),
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
			wantQueueMembers: map[string]sets.Set[string]{
				"/foo": sets.New("/a"),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			manager := NewManager(utiltesting.NewFakeClient(), nil)
			ctx := context.Background()
			for _, cq := range tc.clusterQueues {
				if err := manager.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Adding cluster queue %s: %v", cq.Name, err)
				}
			}
			for _, q := range tc.queues {
				if err := manager.AddLocalQueue(ctx, q); err != nil {
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
			q := manager.localQueues[workload.QueueKey(wl)]
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
			queueMembers := make(map[string]sets.Set[string])
			for name, q := range manager.localQueues {
				queueMembers[name] = workloadNamesFromLQ(q)
			}
			if diff := cmp.Diff(tc.wantQueueMembers, queueMembers); diff != "" {
				t.Errorf("Elements present in wrong queues (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestHeads(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kueue.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding kueue scheme: %s", err)
	}
	now := time.Now().Truncate(time.Second)

	clusterQueues := []*kueue.ClusterQueue{
		utiltesting.MakeClusterQueue("active-fooCq").Obj(),
		utiltesting.MakeClusterQueue("active-barCq").Obj(),
		utiltesting.MakeClusterQueue("pending-bazCq").Obj(),
	}
	queues := []*kueue.LocalQueue{
		utiltesting.MakeLocalQueue("foo", "").ClusterQueue("active-fooCq").Obj(),
		utiltesting.MakeLocalQueue("bar", "").ClusterQueue("active-barCq").Obj(),
		utiltesting.MakeLocalQueue("baz", "").ClusterQueue("pending-bazCq").Obj(),
	}
	tests := []struct {
		name          string
		workloads     []*kueue.Workload
		wantWorkloads sets.Set[string]
	}{
		{
			name:          "empty clusterQueues",
			workloads:     []*kueue.Workload{},
			wantWorkloads: sets.Set[string]{},
		},
		{
			name: "active clusterQueues",
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("a", "").Creation(now).Queue("foo").Obj(),
				utiltesting.MakeWorkload("b", "").Creation(now).Queue("bar").Obj(),
			},
			wantWorkloads: sets.New("a", "b"),
		},
		{
			name: "active clusterQueues with multiple workloads",
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("a1", "").Creation(now).Queue("foo").Obj(),
				utiltesting.MakeWorkload("a2", "").Creation(now.Add(time.Hour)).Queue("foo").Obj(),
				utiltesting.MakeWorkload("b", "").Creation(now).Queue("bar").Obj(),
			},
			wantWorkloads: sets.New("a1", "b"),
		},
		{
			name: "inactive clusterQueues",
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("a", "").Creation(now).Queue("foo").Obj(),
				utiltesting.MakeWorkload("b", "").Creation(now).Queue("bar").Obj(),
				utiltesting.MakeWorkload("c", "").Creation(now.Add(time.Hour)).Queue("baz").Obj(),
			},
			wantWorkloads: sets.New("a", "b"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), headsTimeout)
			defer cancel()
			fakeC := &fakeStatusChecker{}
			manager := NewManager(utiltesting.NewFakeClient(), fakeC)
			for _, cq := range clusterQueues {
				if err := manager.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Failed adding clusterQueue %s to manager: %v", cq.Name, err)
				}
			}
			for _, q := range queues {
				if err := manager.AddLocalQueue(ctx, q); err != nil {
					t.Fatalf("Failed adding queue %s: %s", q.Name, err)
				}
			}

			go manager.CleanUpOnContext(ctx)
			for _, wl := range tc.workloads {
				manager.AddOrUpdateWorkload(wl)
			}

			wlNames := sets.New[string]()
			heads := manager.Heads(ctx)
			for _, h := range heads {
				wlNames.Insert(h.Obj.Name)
			}
			if diff := cmp.Diff(tc.wantWorkloads, wlNames); diff != "" {
				t.Errorf("GetHeads returned wrong heads (-want,+got):\n%s", diff)
			}
		})
	}
}

var ignoreTypeMeta = cmpopts.IgnoreTypes(metav1.TypeMeta{})

// TestHeadAsync ensures that Heads call is blocked until the queues are filled
// asynchronously.
func TestHeadsAsync(t *testing.T) {
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
	queues := []kueue.LocalQueue{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "foo"},
			Spec: kueue.LocalQueueSpec{
				ClusterQueue: "fooCq",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "bar"},
			Spec: kueue.LocalQueueSpec{
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
				if err := mgr.AddLocalQueue(ctx, &queues[0]); err != nil {
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
		"AddLocalQueue": {
			initialObjs: []client.Object{&wl},
			op: func(ctx context.Context, mgr *Manager) {
				if err := mgr.AddClusterQueue(ctx, clusterQueues[0]); err != nil {
					t.Errorf("Failed adding clusterQueue: %v", err)
				}
				go func() {
					if err := mgr.AddLocalQueue(ctx, &queues[0]); err != nil {
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
				if err := mgr.AddLocalQueue(ctx, &queues[0]); err != nil {
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
				if err := mgr.AddLocalQueue(ctx, &queues[0]); err != nil {
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
				if err := mgr.AddLocalQueue(ctx, &queues[0]); err != nil {
					t.Errorf("Failed adding queue: %s", err)
				}
				// Remove the initial workload from the manager.
				mgr.Heads(ctx)
				go func() {
					mgr.RequeueWorkload(ctx, workload.NewInfo(&wl), RequeueReasonFailedAfterNomination)
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
				if err := mgr.AddLocalQueue(ctx, &queues[0]); err != nil {
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
					mgr.RequeueWorkload(ctx, workload.NewInfo(&wl), RequeueReasonFailedAfterNomination)
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
					if err := mgr.AddLocalQueue(ctx, &q); err != nil {
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
					mgr.RequeueWorkload(ctx, workload.NewInfo(&wl), RequeueReasonFailedAfterNomination)
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
			client := utiltesting.NewFakeClient(tc.initialObjs...)
			manager := NewManager(client, nil)
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
	manager := NewManager(utiltesting.NewFakeClient(), nil)
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
func popNamesFromCQ(cq *ClusterQueue) []string {
	var names []string
	for w := cq.Pop(); w != nil; w = cq.Pop() {
		names = append(names, workload.Key(w.Obj))
	}
	return names
}

// workloadNamesFromLQ returns all the names of the workloads in a localQueue.
func workloadNamesFromLQ(q *LocalQueue) sets.Set[string] {
	names := sets.New[string]()
	for k := range q.items {
		names.Insert(k)
	}
	return names
}

type fakeStatusChecker struct{}

func (c *fakeStatusChecker) ClusterQueueActive(name string) bool {
	return strings.Contains(name, "active-")
}

func TestGetPendingWorkloadsInfo(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	clusterQueues := []*kueue.ClusterQueue{
		utiltesting.MakeClusterQueue("cq").Obj(),
	}
	queues := []*kueue.LocalQueue{
		utiltesting.MakeLocalQueue("foo", "").ClusterQueue("cq").Obj(),
	}
	workloads := []*kueue.Workload{
		utiltesting.MakeWorkload("a", "").Queue("foo").Creation(now).Obj(),
		utiltesting.MakeWorkload("b", "").Queue("foo").Creation(now.Add(time.Second)).Obj(),
	}

	// Setup.
	scheme := runtime.NewScheme()
	if err := kueue.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding kueue scheme: %s", err)
	}
	ctx := context.Background()
	manager := NewManager(utiltesting.NewFakeClient(), nil)
	for _, cq := range clusterQueues {
		if err := manager.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("Failed adding clusterQueue %s: %v", cq.Name, err)
		}
	}
	for _, q := range queues {
		if err := manager.AddLocalQueue(ctx, q); err != nil {
			t.Fatalf("Failed adding queue %s: %v", q.Name, err)
		}
	}
	for _, w := range workloads {
		manager.AddOrUpdateWorkload(w)
	}

	cases := map[string]struct {
		cqName                   string
		wantPendingWorkloadsInfo []*workload.Info
	}{
		"Invalid ClusterQueue name": {
			cqName:                   "invalid",
			wantPendingWorkloadsInfo: nil,
		},
		"ClusterQueue with 2 pending workloads": {
			cqName: "cq",
			wantPendingWorkloadsInfo: []*workload.Info{
				{
					Obj: &kueue.Workload{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "a",
							Namespace: "",
						},
						Spec: kueue.WorkloadSpec{
							QueueName: "foo",
						},
					},
				},
				{
					Obj: &kueue.Workload{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "b",
							Namespace: "",
						},
						Spec: kueue.WorkloadSpec{
							QueueName: "foo",
						},
					},
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			pendingWorkloadsInfo := manager.PendingWorkloadsInfo(tc.cqName)
			if diff := cmp.Diff(tc.wantPendingWorkloadsInfo, pendingWorkloadsInfo,
				ignoreTypeMeta,
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "CreationTimestamp"),
				cmpopts.IgnoreFields(kueue.WorkloadSpec{}, "PodSets"),
				cmpopts.IgnoreFields(workload.Info{}, "TotalRequests"),
			); diff != "" {
				t.Errorf("GetPendingWorkloadsInfo returned wrong heads (-want,+got):\n%s", diff)
			}
		})
	}
}
