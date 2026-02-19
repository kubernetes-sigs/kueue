/*
Copyright The Kubernetes Authors.

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
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

const headsTimeout = 3 * time.Second

var cmpDump = cmp.Options{
	cmpopts.SortSlices(func(a, b workload.Reference) bool { return a < b }),
	cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
}

// TestAddLocalQueueOrphans verifies that pods added before adding the queue are
// present when the queue is added.
func TestAddLocalQueueOrphans(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	kClient := utiltesting.NewFakeClient(
		utiltestingapi.MakeWorkload("a", "earth").Queue("foo").Obj(),
		utiltestingapi.MakeWorkload("b", "earth").Queue("bar").Obj(),
		utiltestingapi.MakeWorkload("c", "earth").Queue("foo").Obj(),
		utiltestingapi.MakeWorkload("d", "earth").Queue("foo").
			ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).Obj(),
		utiltestingapi.MakeWorkload("e", "earth").Queue("foo").Active(false).Obj(),
		utiltestingapi.MakeWorkload("f", "earth").Queue("foo").Finished().Obj(),
		utiltestingapi.MakeWorkload("a", "moon").Queue("foo").Obj(),
	)
	manager := NewManagerForUnitTests(kClient, nil)
	q := utiltestingapi.MakeLocalQueue("foo", "earth").Obj()
	ctx, _ := utiltesting.ContextWithLog(t)
	if err := manager.AddLocalQueue(ctx, q); err != nil {
		t.Fatalf("Failed adding queue: %v", err)
	}
	qImpl := manager.localQueues[queue.Key(q)]
	workloadNames := workloadNamesFromLQ(qImpl)
	if diff := cmp.Diff(sets.New[workload.Reference]("earth/a", "earth/c"), workloadNames); diff != "" {
		t.Errorf("Unexpected items in queue foo (-want,+got):\n%s", diff)
	}
	assignedWorkloads := manager.workloadAssignedQueues
	expectedWorkloads := map[workload.Reference]queue.LocalQueueReference{
		"earth/a": "earth/foo",
		"earth/c": "earth/foo",
		"earth/d": "earth/foo",
		"earth/e": "earth/foo",
		"earth/f": "earth/foo",
	}
	if diff := cmp.Diff(expectedWorkloads, assignedWorkloads); diff != "" {
		t.Errorf("Unexpected assigned workloads (-want,+got):\n%s", diff)
	}
}

// TestAddClusterQueueOrphans verifies that when a ClusterQueue is recreated,
// it adopts the existing workloads.
func TestAddClusterQueueOrphans(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	now := time.Now()
	queues := []*kueue.LocalQueue{
		utiltestingapi.MakeLocalQueue("foo", "").ClusterQueue("cq").Obj(),
		utiltestingapi.MakeLocalQueue("bar", "").ClusterQueue("cq").Obj(),
	}
	kClient := utiltesting.NewFakeClient(
		utiltestingapi.MakeWorkload("a", "").Queue("foo").Creation(now.Add(time.Second)).Obj(),
		utiltestingapi.MakeWorkload("b", "").Queue("bar").Creation(now).Obj(),
		utiltestingapi.MakeWorkload("c", "").Queue("foo").
			ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).Obj(),
		utiltestingapi.MakeWorkload("d", "").Queue("baz").Obj(),
		queues[0],
		queues[1],
	)
	manager := NewManagerForUnitTests(kClient, nil)
	cq := utiltestingapi.MakeClusterQueue("cq").Obj()
	if err := manager.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Failed adding cluster queue %s: %v", cq.Name, err)
	}
	for _, q := range queues {
		if err := manager.AddLocalQueue(ctx, q); err != nil {
			t.Fatalf("Failed adding queue %s: %v", q.Name, err)
		}
	}

	wantActiveWorkloads := map[kueue.ClusterQueueReference][]workload.Reference{
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
	workloads := popNamesFromCQ(manager.hm.ClusterQueue("cq"))
	wantWorkloads := []workload.Reference{"/b", "/a"}
	if diff := cmp.Diff(wantWorkloads, workloads); diff != "" {
		t.Errorf("Workloads popped in the wrong order from clusterQueue:\n%s", diff)
	}
}

// TestUpdateClusterQueue tests that a ClusterQueue transfers cohorts on update.
// Inadmissible workloads should become active.
func TestUpdateClusterQueue(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	clusterQueues := []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("cq1").Cohort("alpha").Obj(),
		utiltestingapi.MakeClusterQueue("cq2").Cohort("beta").Obj(),
	}
	queues := []*kueue.LocalQueue{
		utiltestingapi.MakeLocalQueue("foo", defaultNamespace).ClusterQueue("cq1").Obj(),
		utiltestingapi.MakeLocalQueue("bar", defaultNamespace).ClusterQueue("cq2").Obj(),
	}
	now := time.Now()
	workloads := []*kueue.Workload{
		utiltestingapi.MakeWorkload("a", defaultNamespace).Queue("foo").Creation(now.Add(time.Second)).Obj(),
		utiltestingapi.MakeWorkload("b", defaultNamespace).Queue("bar").Creation(now).Obj(),
	}
	// Setup.
	cl := utiltesting.NewFakeClient(utiltesting.MakeNamespace(defaultNamespace))
	manager := NewManagerForUnitTests(cl, nil)
	for _, cq := range clusterQueues {
		if err := manager.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("Failed adding clusterQueue %s: %v", cq.Name, err)
		}
		// Increase the popCycle to ensure that the workload will be added as inadmissible.
		manager.getClusterQueue(kueue.ClusterQueueReference(cq.Name)).popCycle++
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

	// Verify that all workloads are marked as inadmissible after creation.
	inadmissibleWorkloads := manager.DumpInadmissible()
	wantInadmissibleWorkloads := map[kueue.ClusterQueueReference][]workload.Reference{
		"cq1": {"default/a"},
		"cq2": {"default/b"},
	}
	if diff := cmp.Diff(wantInadmissibleWorkloads, inadmissibleWorkloads); diff != "" {
		t.Errorf("Unexpected set of inadmissible workloads (-want +got):\n%s", diff)
	}
	activeWorkloads := manager.Dump()
	if diff := cmp.Diff(map[kueue.ClusterQueueReference][]workload.Reference(nil), activeWorkloads); diff != "" {
		t.Errorf("Unexpected active workloads (-want +got):\n%s", diff)
	}

	// Put cq2 in the same cohort as cq1.
	clusterQueues[1].Spec.CohortName = clusterQueues[0].Spec.CohortName
	if err := manager.UpdateClusterQueue(ctx, clusterQueues[1], true); err != nil {
		t.Fatalf("Failed to update ClusterQueue: %v", err)
	}

	wantCohorts := map[kueue.CohortReference]sets.Set[kueue.ClusterQueueReference]{
		"alpha": sets.New[kueue.ClusterQueueReference]("cq1", "cq2"),
	}
	gotCohorts := make(map[kueue.CohortReference]sets.Set[kueue.ClusterQueueReference])
	for name, cohort := range manager.hm.Cohorts() {
		gotCohorts[name] = sets.New[kueue.ClusterQueueReference]()
		for _, cq := range cohort.ChildCQs() {
			gotCohorts[name].Insert(cq.GetName())
		}
	}
	if diff := cmp.Diff(gotCohorts, wantCohorts); diff != "" {
		t.Errorf("Unexpected ClusterQueues in cohorts (-want,+got):\n%s", diff)
	}

	// Verify that all workloads are active after the update.
	inadmissibleWorkloads = manager.DumpInadmissible()
	if diff := cmp.Diff(map[kueue.ClusterQueueReference][]workload.Reference(nil), inadmissibleWorkloads); diff != "" {
		t.Errorf("Unexpected set of inadmissible workloads (-want +got):\n%s", diff)
	}
	activeWorkloads = manager.Dump()
	wantActiveWorkloads := map[kueue.ClusterQueueReference][]workload.Reference{
		"cq1": {"default/a"},
		"cq2": {"default/b"},
	}
	if diff := cmp.Diff(wantActiveWorkloads, activeWorkloads); diff != "" {
		t.Errorf("Unexpected active workloads (-want +got):\n%s", diff)
	}
}

func TestRequeueWorkloadsCohortCycle(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	cohorts := []*kueue.Cohort{
		utiltestingapi.MakeCohort("cohort-a").Parent("cohort-b").Obj(),
		utiltestingapi.MakeCohort("cohort-b").Parent("cohort-c").Obj(),
		utiltestingapi.MakeCohort("cohort-c").Parent("cohort-a").Obj(),
	}
	cq := utiltestingapi.MakeClusterQueue("cq1").Cohort("cohort-a").Obj()
	lq := utiltestingapi.MakeLocalQueue("foo", defaultNamespace).ClusterQueue("cq1").Obj()
	wl := utiltestingapi.MakeWorkload("a", defaultNamespace).Queue("foo").Creation(time.Now()).Obj()
	expectedAssigned := map[workload.Reference]queue.LocalQueueReference{defaultNamespace + "/a": defaultNamespace + "/foo"}
	// Setup.
	cl := utiltesting.NewFakeClient(utiltesting.MakeNamespace(defaultNamespace))
	manager := NewManagerForUnitTests(cl, nil)
	for _, cohort := range cohorts {
		manager.AddOrUpdateCohort(ctx, cohort)
	}
	if err := manager.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Failed adding clusterQueue %s: %v", cq.Name, err)
	}
	if err := manager.AddLocalQueue(ctx, lq); err != nil {
		t.Fatalf("Failed adding queue %s: %v", lq.Name, err)
	}
	if err := cl.Create(ctx, wl); err != nil {
		t.Fatalf("Failed adding workload to client: %v", err)
	}
	if diff := cmp.Diff(map[workload.Reference]queue.LocalQueueReference{}, manager.workloadAssignedQueues); diff != "" {
		t.Errorf("Expected no workloads to be assigned (-want,+got):\n%s", diff)
	}
	// This test will pass with the removal of this line.
	// Update once we find a solution to #3066.
	manager.RequeueWorkload(ctx, workload.NewInfo(wl), RequeueReasonGeneric)

	// This method is where we do a cycle check. We call it to ensure
	// it behaves properly when a cycle exists
	if requeueWorkloadsCohort(ctx, manager, manager.hm.Cohort("cohort-a")) {
		t.Fatal("Expected moveWorkloadsCohort to return false")
	}
	if diff := cmp.Diff(expectedAssigned, manager.workloadAssignedQueues); diff != "" {
		t.Errorf("Unexpected assigned workloads (-want,+got):\n%s", diff)
	}
}

func TestQueueInadmissibleWorkloads(t *testing.T) {
	now := time.Now()
	cases := map[string]struct {
		cohorts                   []*kueue.Cohort
		clusterQueues             []*kueue.ClusterQueue
		localQueues               []*kueue.LocalQueue
		workloads                 []*kueue.Workload
		cqNames                   sets.Set[kueue.ClusterQueueReference]
		wantInadmissible          map[kueue.ClusterQueueReference][]workload.Reference
		wantActive                map[kueue.ClusterQueueReference][]workload.Reference
		wantMoveWorkloadsLogCount int
	}{
		"deduplication with shared root": {
			// Tree structure:
			//   root
			//   ├── child1
			//   │   └── cq1
			//   └── child2
			//       └── cq2
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("root").Obj(),
				utiltestingapi.MakeCohort("child1").Parent("root").Obj(),
				utiltestingapi.MakeCohort("child2").Parent("root").Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq1").Cohort("child1").Obj(),
				utiltestingapi.MakeClusterQueue("cq2").Cohort("child2").Obj(),
			},
			localQueues: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("foo", defaultNamespace).ClusterQueue("cq1").Obj(),
				utiltestingapi.MakeLocalQueue("bar", defaultNamespace).ClusterQueue("cq2").Obj(),
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("a", defaultNamespace).Queue("foo").Creation(now).Obj(),
				utiltestingapi.MakeWorkload("b", defaultNamespace).Queue("bar").Creation(now.Add(time.Second)).Obj(),
			},
			cqNames:          sets.New[kueue.ClusterQueueReference]("cq1", "cq2"),
			wantInadmissible: nil,
			wantActive: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq1": {"default/a"},
				"cq2": {"default/b"},
			},
			// Verify deduplication: although cq1 and cq2 share the same root, the
			// "Attempting to move workloads" log should appear only once.
			wantMoveWorkloadsLogCount: 1,
		},
		"cohort cycle": {
			// cohort-a -> cohort-b -> cohort-c -> cohort-a
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("cohort-a").Parent("cohort-b").Obj(),
				utiltestingapi.MakeCohort("cohort-b").Parent("cohort-c").Obj(),
				utiltestingapi.MakeCohort("cohort-c").Parent("cohort-a").Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq1").Cohort("cohort-a").Obj(),
			},
			localQueues: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("foo", defaultNamespace).ClusterQueue("cq1").Obj(),
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("a", defaultNamespace).Queue("foo").Creation(now).Obj(),
			},
			cqNames: sets.New[kueue.ClusterQueueReference]("cq1"),
			wantInadmissible: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq1": {"default/a"},
			},
			wantActive: nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var moveWorkloadsLogCount int
			logger := funcr.New(func(prefix, args string) {
				if strings.Contains(args, "Attempting to move workloads") {
					moveWorkloadsLogCount++
				}
			}, funcr.Options{Verbosity: 2})
			ctx := logr.NewContext(context.Background(), logger)

			cl := utiltesting.NewFakeClient(utiltesting.MakeNamespace(defaultNamespace))
			manager := NewManagerForUnitTests(cl, nil)

			for _, cohort := range tc.cohorts {
				manager.AddOrUpdateCohort(ctx, cohort)
			}
			for _, cq := range tc.clusterQueues {
				if err := manager.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Failed adding clusterQueue %s: %v", cq.Name, err)
				}
				manager.getClusterQueue(kueue.ClusterQueueReference(cq.Name)).popCycle++
			}
			for _, lq := range tc.localQueues {
				if err := manager.AddLocalQueue(ctx, lq); err != nil {
					t.Fatalf("Failed adding queue %s: %v", lq.Name, err)
				}
			}
			for _, wl := range tc.workloads {
				if err := cl.Create(ctx, wl); err != nil {
					t.Fatalf("Failed adding workload to client: %v", err)
				}
				manager.RequeueWorkload(ctx, workload.NewInfo(wl), RequeueReasonGeneric)
			}

			// Reset the counter before testing. Setup operations also trigger the log.
			moveWorkloadsLogCount = 0

			QueueInadmissibleWorkloads(ctx, manager, tc.cqNames)

			if diff := cmp.Diff(tc.wantInadmissible, manager.DumpInadmissible()); diff != "" {
				t.Errorf("Unexpected inadmissible workloads (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantActive, manager.Dump(), cmpDump...); diff != "" {
				t.Errorf("Unexpected active workloads (-want +got):\n%s", diff)
			}
			if moveWorkloadsLogCount != tc.wantMoveWorkloadsLogCount {
				t.Errorf("Expected %d 'Attempting to move workloads' log call(s), got %d", tc.wantMoveWorkloadsLogCount, moveWorkloadsLogCount)
			}
		})
	}
}

// TestClusterQueueToActive tests that managers cond gets a broadcast when
// a cluster queue becomes active.
func TestClusterQueueToActive(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	stoppedCq := utiltestingapi.MakeClusterQueue("cq1").Cohort("alpha").Condition(kueue.ClusterQueueActive, metav1.ConditionFalse, "ByTest", "by test").Obj()
	runningCq := utiltestingapi.MakeClusterQueue("cq1").Cohort("alpha").Condition(kueue.ClusterQueueActive, metav1.ConditionTrue, "ByTest", "by test").Obj()
	cl := utiltesting.NewFakeClient(utiltesting.MakeNamespace(defaultNamespace))
	manager := NewManagerForUnitTests(cl, nil)

	wgCounterStart := sync.WaitGroup{}
	wgCounterStart.Add(1)
	wgCounterEnd := sync.WaitGroup{}
	wgCounterEnd.Add(1)
	condRec := make(chan struct{})
	counterCtx, counterCancel := context.WithCancel(ctx)
	defer counterCancel()
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
		// nothing
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
	ctx, log := utiltesting.ContextWithLog(t)
	clusterQueues := []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("cq1").Obj(),
		utiltestingapi.MakeClusterQueue("cq2").Obj(),
	}
	queues := []*kueue.LocalQueue{
		utiltestingapi.MakeLocalQueue("foo", "").ClusterQueue("cq1").Obj(),
		utiltestingapi.MakeLocalQueue("bar", "").ClusterQueue("cq2").Obj(),
	}
	now := time.Now()
	workloads := []*kueue.Workload{
		utiltestingapi.MakeWorkload("a", "").Queue("foo").Creation(now.Add(time.Second)).Obj(),
		utiltestingapi.MakeWorkload("b", "").Queue("bar").Creation(now).Obj(),
	}
	manager := NewManagerForUnitTests(utiltesting.NewFakeClient(), nil)
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
		if err := manager.AddOrUpdateWorkload(log, w); err != nil {
			t.Errorf("Failed to add or update workload: %v", err)
		}
	}

	// Update cluster queue of first queue.
	queues[0].Spec.ClusterQueue = "cq2"
	if err := manager.UpdateLocalQueue(log, queues[0]); err != nil {
		t.Fatalf("Failed updating queue: %v", err)
	}

	// Verification.
	workloadOrders := make(map[kueue.ClusterQueueReference][]workload.Reference)
	for name, cq := range manager.hm.ClusterQueues() {
		workloadOrders[name] = popNamesFromCQ(cq)
	}
	wantWorkloadOrders := map[kueue.ClusterQueueReference][]workload.Reference{
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
	ctx, log := utiltesting.ContextWithLog(t)
	cq := utiltestingapi.MakeClusterQueue("cq").Obj()
	q := utiltestingapi.MakeLocalQueue("foo", "").ClusterQueue("cq").Obj()
	wl := utiltestingapi.MakeWorkload("a", "").Queue("foo").Obj()

	cl := utiltesting.NewFakeClient(wl)
	manager := NewManagerForUnitTests(cl, nil)

	if err := manager.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Could not create ClusterQueue: %v", err)
	}
	if err := manager.AddLocalQueue(ctx, q); err != nil {
		t.Fatalf("Could not create LocalQueue: %v", err)
	}

	wantActiveWorkloads := map[kueue.ClusterQueueReference][]workload.Reference{
		"cq": {"/a"},
	}
	if diff := cmp.Diff(wantActiveWorkloads, manager.Dump(), cmpDump...); diff != "" {
		t.Errorf("Unexpected workloads after setup (-want,+got):\n%s", diff)
	}

	manager.DeleteLocalQueue(log, q)
	wantActiveWorkloads = nil
	if diff := cmp.Diff(wantActiveWorkloads, manager.Dump(), cmpDump...); diff != "" {
		t.Errorf("Unexpected workloads after deleting LocalQueue (-want,+got):\n%s", diff)
	}
}

func TestAddWorkload(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	ctx, log := utiltesting.ContextWithLog(t)
	manager := NewManagerForUnitTests(utiltesting.NewFakeClient(), nil)
	cq := utiltestingapi.MakeClusterQueue("cq").Obj()
	if err := manager.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Failed adding clusterQueue %s: %v", cq.Name, err)
	}
	queues := []*kueue.LocalQueue{
		utiltestingapi.MakeLocalQueue("foo", "earth").ClusterQueue("cq").Obj(),
		utiltestingapi.MakeLocalQueue("bar", "mars").Obj(),
	}
	for _, q := range queues {
		if err := manager.AddLocalQueue(ctx, q); err != nil {
			t.Fatalf("Failed adding queue %s: %v", q.Name, err)
		}
	}
	cases := []struct {
		workload     *kueue.Workload
		wantErr      error
		wantAssigned map[workload.Reference]queue.LocalQueueReference
	}{
		{
			workload: utiltestingapi.MakeWorkload("finished", "earth").
				Queue("foo").
				Finished().
				Obj(),
			wantErr:      errWorkloadIsInadmissible,
			wantAssigned: map[workload.Reference]queue.LocalQueueReference{},
		},
		{
			workload: utiltestingapi.MakeWorkload("inactive", "earth").
				Queue("foo").
				Active(false).
				Obj(),
			wantErr:      errWorkloadIsInadmissible,
			wantAssigned: map[workload.Reference]queue.LocalQueueReference{},
		},
		{
			workload: utiltestingapi.MakeWorkload("quota_already_reserved", "earth").
				Queue("foo").
				ReserveQuotaAt(&kueue.Admission{
					ClusterQueue:      kueue.ClusterQueueReference(cq.Name),
					PodSetAssignments: nil,
				}, now).
				Obj(),
			wantErr:      errWorkloadIsInadmissible,
			wantAssigned: map[workload.Reference]queue.LocalQueueReference{},
		},
		{
			workload: utiltestingapi.MakeWorkload("existing_queue", "earth").
				Queue("foo").Obj(),
			wantAssigned: map[workload.Reference]queue.LocalQueueReference{"earth/existing_queue": "earth/foo"},
		},
		{
			workload: utiltestingapi.MakeWorkload("non_existing_queue", "earth").
				Queue("baz").
				Obj(),
			wantErr:      ErrLocalQueueDoesNotExistOrInactive,
			wantAssigned: map[workload.Reference]queue.LocalQueueReference{},
		},
		{
			workload: utiltestingapi.MakeWorkload("non_existing_cluster_queue", "mars").
				Queue("bar").
				Obj(),
			wantErr:      ErrClusterQueueDoesNotExist,
			wantAssigned: map[workload.Reference]queue.LocalQueueReference{"mars/non_existing_cluster_queue": "mars/bar"},
		},
		{
			workload: utiltestingapi.MakeWorkload("wrong_namespace", "mars").
				Queue("foo").
				Obj(),
			wantErr:      ErrLocalQueueDoesNotExistOrInactive,
			wantAssigned: map[workload.Reference]queue.LocalQueueReference{},
		},
		{
			workload: utiltestingapi.MakeWorkload("non_existing_local_queue", "earth").
				Queue("baz").
				Obj(),
			wantErr:      ErrLocalQueueDoesNotExistOrInactive,
			wantAssigned: map[workload.Reference]queue.LocalQueueReference{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.workload.Name, func(t *testing.T) {
			manager := NewManagerForUnitTests(utiltesting.NewFakeClient(), nil)
			cq := utiltestingapi.MakeClusterQueue("cq").Obj()
			if err := manager.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("Failed adding clusterQueue %s: %v", cq.Name, err)
			}
			queues := []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("foo", "earth").ClusterQueue("cq").Obj(),
				utiltestingapi.MakeLocalQueue("bar", "mars").Obj(),
			}
			for _, q := range queues {
				if err := manager.AddLocalQueue(ctx, q); err != nil {
					t.Fatalf("Failed adding queue %s: %v", q.Name, err)
				}
			}
			err := manager.AddOrUpdateWorkload(log, tc.workload)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); len(diff) != 0 {
				t.Errorf("Unexpected AddWorkload returned error (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantAssigned, manager.workloadAssignedQueues); diff != "" {
				t.Errorf("Unexpected assigned workloads (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestDeleteWorkload(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	manager := NewManagerForUnitTests(utiltesting.NewFakeClient(), nil)
	cq := utiltestingapi.MakeClusterQueue("cq").Obj()
	if err := manager.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Failed adding clusterQueue %s: %v", cq.Name, err)
	}
	queues := []*kueue.LocalQueue{
		utiltestingapi.MakeLocalQueue("foo", "earth").ClusterQueue("cq").Obj(),
		utiltestingapi.MakeLocalQueue("bar", "mars").Obj(),
	}
	for _, q := range queues {
		if err := manager.AddLocalQueue(ctx, q); err != nil {
			t.Fatalf("Failed adding queue %s: %v", q.Name, err)
		}
	}
	t.Run("workload deleted but not forgotten", func(t *testing.T) {
		wl1 := utiltestingapi.MakeWorkload("wl1", "earth").
			Queue("foo").Obj()
		wl2 := utiltestingapi.MakeWorkload("wl2", "earth").
			Queue("foo").Obj()

		for _, wl := range []*kueue.Workload{wl1, wl2} {
			if err := manager.AddOrUpdateWorkload(log, wl); err != nil {
				t.Fatalf("Failed adding workload %s: %v", wl.Name, err)
			}
		}

		manager.DeleteWorkload(log, workload.Key(wl1))

		q := manager.localQueues[queue.Key(queues[0])]
		if diff := cmp.Diff(map[workload.Reference]*workload.Info{
			workload.Key(wl2): workload.NewInfo(wl2),
		}, q.items); diff != "" {
			t.Errorf("Unexpected workloads found in local queue (-want,+got):\n%s", diff)
		}

		cq := manager.hm.ClusterQueue(q.ClusterQueue)
		if diff := cmp.Diff(1, cq.PendingTotal()); diff != "" {
			t.Errorf("Unexpected number of pending workloads in cluster queue (-want,+got):\n%s", diff)
		}

		if diff := cmp.Diff(map[workload.Reference]queue.LocalQueueReference{
			"earth/wl1": "earth/foo",
			"earth/wl2": "earth/foo",
		}, manager.workloadAssignedQueues); diff != "" {
			t.Errorf("Unexpected assigned workloads (-want,+got):\n%s", diff)
		}
	})
}

func TestDeleteAndForgetWorkload(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	manager := NewManagerForUnitTests(utiltesting.NewFakeClient(), nil)
	cq := utiltestingapi.MakeClusterQueue("cq").Obj()
	if err := manager.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Failed adding clusterQueue %s: %v", cq.Name, err)
	}
	queues := []*kueue.LocalQueue{
		utiltestingapi.MakeLocalQueue("foo", "earth").ClusterQueue("cq").Obj(),
		utiltestingapi.MakeLocalQueue("bar", "mars").Obj(),
	}
	for _, q := range queues {
		if err := manager.AddLocalQueue(ctx, q); err != nil {
			t.Fatalf("Failed adding queue %s: %v", q.Name, err)
		}
	}
	t.Run("workload fully deleted and forgotten", func(t *testing.T) {
		wl1 := utiltestingapi.MakeWorkload("wl1", "earth").
			Queue("foo").Obj()
		wl2 := utiltestingapi.MakeWorkload("wl2", "earth").
			Queue("foo").Obj()

		for _, wl := range []*kueue.Workload{wl1, wl2} {
			if err := manager.AddOrUpdateWorkload(log, wl); err != nil {
				t.Fatalf("Failed adding workload %s: %v", wl.Name, err)
			}
		}

		manager.DeleteAndForgetWorkload(log, workload.Key(wl1))

		q := manager.localQueues[queue.Key(queues[0])]
		if diff := cmp.Diff(map[workload.Reference]*workload.Info{
			workload.Key(wl2): workload.NewInfo(wl2),
		}, q.items); diff != "" {
			t.Errorf("Unexpected workloads found in local queue (-want,+got):\n%s", diff)
		}

		cq := manager.hm.ClusterQueue(q.ClusterQueue)
		if diff := cmp.Diff(1, cq.PendingTotal()); diff != "" {
			t.Errorf("Unexpected number of pending workloads in cluster queue (-want,+got):\n%s", diff)
		}

		if diff := cmp.Diff(map[workload.Reference]queue.LocalQueueReference{
			"earth/wl2": "earth/foo",
		}, manager.workloadAssignedQueues); diff != "" {
			t.Errorf("Unexpected assigned workloads (-want,+got):\n%s", diff)
		}
	})
}

func TestStatus(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
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

	manager := NewManagerForUnitTests(utiltesting.NewFakeClient(), nil)
	for _, q := range queues {
		if err := manager.AddLocalQueue(ctx, &q); err != nil {
			t.Errorf("Failed adding queue: %s", err)
		}
	}
	for _, wl := range workloads {
		// We ignore the ErrClusterQueueDoesNotExist since we never set up ClusterQueue in this test,
		// and the error should be occurred.
		if err := manager.AddOrUpdateWorkload(log, &wl); err != nil && !errors.Is(err, ErrClusterQueueDoesNotExist) {
			t.Fatalf("Failed to add or update workloads: %v", err)
		}
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
			wantErr:    ErrLocalQueueDoesNotExistOrInactive,
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
	cq := utiltestingapi.MakeClusterQueue("cq").Obj()
	queues := []*kueue.LocalQueue{
		utiltestingapi.MakeLocalQueue("foo", "").ClusterQueue("cq").Obj(),
		utiltestingapi.MakeLocalQueue("bar", "").Obj(),
	}
	cases := []struct {
		workload     *kueue.Workload
		inClient     bool
		inQueue      bool
		wantRequeued bool
	}{
		{
			workload: utiltestingapi.MakeWorkload("existing_queue_and_obj", "").
				Queue("foo").
				Obj(),
			inClient:     true,
			wantRequeued: true,
		},
		{
			workload: utiltestingapi.MakeWorkload("non_existing_queue", "").Queue("baz").Obj(),
			inClient: true,
		},
		{
			workload: utiltestingapi.MakeWorkload("non_existing_cluster_queue", "").
				Queue("bar").
				Obj(),
			inClient: true,
		},
		{
			workload: utiltestingapi.MakeWorkload("not_in_client", "").
				Queue("foo").
				Obj(),
		},
		{
			workload: utiltestingapi.MakeWorkload("already_in_queue", "").
				Queue("foo").
				Obj(),
			inClient: true,
			inQueue:  true,
		},
		{
			workload: utiltestingapi.MakeWorkload("already_admitted", "").
				Queue("foo").
				Admission(&kueue.Admission{}).
				Obj(),
			inClient: true,
			inQueue:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.workload.Name, func(t *testing.T) {
			cl := utiltesting.NewFakeClient()
			manager := NewManagerForUnitTests(cl, nil)
			ctx, log := utiltesting.ContextWithLog(t)
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
				_ = manager.AddOrUpdateWorkload(log, tc.workload)
			}
			info := workload.NewInfo(tc.workload)
			if requeued := manager.RequeueWorkload(ctx, info, RequeueReasonGeneric); requeued != tc.wantRequeued {
				t.Errorf("RequeueWorkload returned %t, want %t", requeued, tc.wantRequeued)
			}
		})
	}
}

func TestUpdateWorkload(t *testing.T) {
	now := time.Now()
	cases := map[string]struct {
		clusterQueues    []*kueue.ClusterQueue
		queues           []*kueue.LocalQueue
		workloads        []*kueue.Workload
		assigned         map[workload.Reference]queue.LocalQueueReference
		update           func(*kueue.Workload)
		wantUpdated      bool
		wantQueueOrder   map[kueue.ClusterQueueReference][]workload.Reference
		wantQueueMembers map[queue.LocalQueueReference]sets.Set[workload.Reference]
		wantErr          error
		wantAssigned     map[workload.Reference]queue.LocalQueueReference
	}{
		"in queue": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq").Obj(),
			},
			queues: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("foo", "").ClusterQueue("cq").Obj(),
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("a", "").Queue("foo").Creation(now).Obj(),
				utiltestingapi.MakeWorkload("b", "").Queue("foo").Creation(now.Add(time.Second)).Obj(),
			},
			assigned: map[workload.Reference]queue.LocalQueueReference{
				"/a": "/foo",
				"/b": "/foo",
			},
			update: func(w *kueue.Workload) {
				w.CreationTimestamp = metav1.NewTime(now.Add(time.Minute))
			},
			wantUpdated: true,
			wantQueueOrder: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq": {"/b", "/a"},
			},
			wantQueueMembers: map[queue.LocalQueueReference]sets.Set[workload.Reference]{
				"/foo": sets.New[workload.Reference]("/a", "/b"),
			},
			wantAssigned: map[workload.Reference]queue.LocalQueueReference{
				"/a": "/foo",
				"/b": "/foo",
			},
		},
		"between queues": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq").Obj(),
			},
			queues: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("foo", "").ClusterQueue("cq").Obj(),
				utiltestingapi.MakeLocalQueue("bar", "").ClusterQueue("cq").Obj(),
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("a", "").Queue("foo").Obj(),
			},
			assigned: map[workload.Reference]queue.LocalQueueReference{
				"/a": "/foo",
			},
			update: func(w *kueue.Workload) {
				w.Spec.QueueName = "bar"
			},
			wantUpdated: true,
			wantQueueOrder: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq": {"/a"},
			},
			wantQueueMembers: map[queue.LocalQueueReference]sets.Set[workload.Reference]{
				"/foo": nil,
				"/bar": sets.New[workload.Reference]("/a"),
			},
			wantAssigned: map[workload.Reference]queue.LocalQueueReference{
				"/a": "/bar",
			},
		},
		"between cluster queues": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq1").Obj(),
				utiltestingapi.MakeClusterQueue("cq2").Obj(),
			},
			queues: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("foo", "").ClusterQueue("cq1").Obj(),
				utiltestingapi.MakeLocalQueue("bar", "").ClusterQueue("cq2").Obj(),
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("a", "").Queue("foo").Obj(),
			},
			assigned: map[workload.Reference]queue.LocalQueueReference{
				"/a": "/foo",
			},
			update: func(w *kueue.Workload) {
				w.Spec.QueueName = "bar"
			},
			wantUpdated: true,
			wantQueueOrder: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq1": nil,
				"cq2": {"/a"},
			},
			wantQueueMembers: map[queue.LocalQueueReference]sets.Set[workload.Reference]{
				"/foo": nil,
				"/bar": sets.New[workload.Reference]("/a"),
			},
			wantAssigned: map[workload.Reference]queue.LocalQueueReference{
				"/a": "/bar",
			},
		},
		"to non existent queue": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq").Obj(),
			},
			queues: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("foo", "").ClusterQueue("cq").Obj(),
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("a", "").Queue("foo").Obj(),
			},
			assigned: map[workload.Reference]queue.LocalQueueReference{
				"/a": "/foo",
			},
			update: func(w *kueue.Workload) {
				w.Spec.QueueName = "bar"
			},
			wantQueueOrder: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq": nil,
			},
			wantQueueMembers: map[queue.LocalQueueReference]sets.Set[workload.Reference]{
				"/foo": nil,
			},
			wantErr:      ErrLocalQueueDoesNotExistOrInactive,
			wantAssigned: map[workload.Reference]queue.LocalQueueReference{},
		},
		"from non existing queue": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq").Obj(),
			},
			queues: []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("foo", "").ClusterQueue("cq").Obj(),
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("a", "").Queue("bar").Obj(),
			},
			assigned: map[workload.Reference]queue.LocalQueueReference{},
			update: func(w *kueue.Workload) {
				w.Spec.QueueName = "foo"
			},
			wantUpdated: true,
			wantQueueOrder: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq": {"/a"},
			},
			wantQueueMembers: map[queue.LocalQueueReference]sets.Set[workload.Reference]{
				"/foo": sets.New[workload.Reference]("/a"),
			},
			wantAssigned: map[workload.Reference]queue.LocalQueueReference{
				"/a": "/foo",
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			manager := NewManagerForUnitTests(utiltesting.NewFakeClient(), nil)
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
				_ = manager.AddOrUpdateWorkload(log, w)
			}
			if diff := cmp.Diff(tc.assigned, manager.workloadAssignedQueues); diff != "" {
				t.Errorf("Unexpected initial state of assigned workloads (-want,+got):\n%s", diff)
			}
			wl := tc.workloads[0].DeepCopy()
			tc.update(wl)
			err := manager.UpdateWorkload(log, wl)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); len(diff) != 0 {
				t.Errorf("Unexpected UpdatedWorkload returned error (-want,+got):\n%s", diff)
			}
			q := manager.localQueues[queue.KeyFromWorkload(wl)]
			if q != nil {
				key := workload.Key(wl)
				item := q.items[key]
				if item == nil {
					t.Errorf("Object not stored in queue")
				} else if diff := cmp.Diff(wl, item.Obj); diff != "" {
					t.Errorf("Object stored in queue differs (-want,+got):\n%s", diff)
				}
				cq := manager.hm.ClusterQueue(q.ClusterQueue)
				if cq != nil {
					item := cq.Info(key)
					if item == nil {
						t.Errorf("Object not stored in clusterQueue")
					} else if diff := cmp.Diff(wl, item.Obj); diff != "" {
						t.Errorf("Object stored in clusterQueue differs (-want,+got):\n%s", diff)
					}
				}
			}
			queueOrder := make(map[kueue.ClusterQueueReference][]workload.Reference)
			for name, cq := range manager.hm.ClusterQueues() {
				queueOrder[name] = popNamesFromCQ(cq)
			}
			if diff := cmp.Diff(tc.wantQueueOrder, queueOrder); diff != "" {
				t.Errorf("Elements popped in the wrong order from clusterQueues (-want,+got):\n%s", diff)
			}
			queueMembers := make(map[queue.LocalQueueReference]sets.Set[workload.Reference])
			for name, q := range manager.localQueues {
				queueMembers[name] = workloadNamesFromLQ(q)
			}
			if diff := cmp.Diff(tc.wantQueueMembers, queueMembers); diff != "" {
				t.Errorf("Elements present in wrong queues (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantAssigned, manager.workloadAssignedQueues); diff != "" {
				t.Errorf("Unexpected assigned workloads (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestHeads(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	clusterQueues := []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("active-fooCq").Obj(),
		utiltestingapi.MakeClusterQueue("active-barCq").Obj(),
		utiltestingapi.MakeClusterQueue("pending-bazCq").Obj(),
	}
	queues := []*kueue.LocalQueue{
		utiltestingapi.MakeLocalQueue("foo", "").ClusterQueue("active-fooCq").Obj(),
		utiltestingapi.MakeLocalQueue("bar", "").ClusterQueue("active-barCq").Obj(),
		utiltestingapi.MakeLocalQueue("baz", "").ClusterQueue("pending-bazCq").Obj(),
	}
	tests := []struct {
		name          string
		workloads     []*kueue.Workload
		wantAssigned  map[workload.Reference]queue.LocalQueueReference
		wantWorkloads sets.Set[string]
	}{
		{
			name:          "empty clusterQueues",
			workloads:     []*kueue.Workload{},
			wantAssigned:  map[workload.Reference]queue.LocalQueueReference{},
			wantWorkloads: sets.Set[string]{},
		},
		{
			name: "active clusterQueues",
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("a", "").Creation(now).Queue("foo").Obj(),
				utiltestingapi.MakeWorkload("b", "").Creation(now).Queue("bar").Obj(),
			},
			wantAssigned: map[workload.Reference]queue.LocalQueueReference{
				"/a": "/foo",
				"/b": "/bar",
			},
			wantWorkloads: sets.New("a", "b"),
		},
		{
			name: "active clusterQueues with multiple workloads",
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("a1", "").Creation(now).Queue("foo").Obj(),
				utiltestingapi.MakeWorkload("a2", "").Creation(now.Add(time.Hour)).Queue("foo").Obj(),
				utiltestingapi.MakeWorkload("b", "").Creation(now).Queue("bar").Obj(),
			},
			wantAssigned: map[workload.Reference]queue.LocalQueueReference{
				"/a1": "/foo",
				"/a2": "/foo",
				"/b":  "/bar",
			},
			wantWorkloads: sets.New("a1", "b"),
		},
		{
			name: "inactive clusterQueues",
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("a", "").Creation(now).Queue("foo").Obj(),
				utiltestingapi.MakeWorkload("b", "").Creation(now).Queue("bar").Obj(),
				utiltestingapi.MakeWorkload("c", "").Creation(now.Add(time.Hour)).Queue("baz").Obj(),
			},
			wantAssigned: map[workload.Reference]queue.LocalQueueReference{
				"/a": "/foo",
				"/b": "/bar",
				"/c": "/baz",
			},
			wantWorkloads: sets.New("a", "b"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			ctx, cancel := context.WithTimeout(ctx, headsTimeout)
			defer cancel()
			fakeC := &fakeStatusChecker{}
			manager := NewManagerForUnitTests(utiltesting.NewFakeClient(), fakeC)
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
				if err := manager.AddOrUpdateWorkload(log, wl); err != nil {
					t.Errorf("Failed to add or update workload: %v", err)
				}
			}

			if diff := cmp.Diff(tc.wantAssigned, manager.workloadAssignedQueues); diff != "" {
				t.Errorf("Unexpected assigned workloads before heads retrieved (-want,+got):\n%s", diff)
			}

			wlNames := sets.New[string]()
			heads := manager.Heads(ctx)
			for _, h := range heads {
				wlNames.Insert(h.Obj.Name)
			}
			if diff := cmp.Diff(tc.wantWorkloads, wlNames); diff != "" {
				t.Errorf("GetHeads returned wrong heads (-want,+got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantAssigned, manager.workloadAssignedQueues); diff != "" {
				t.Errorf("Unexpected assigned workloads after heads retrieved (-want,+got):\n%s", diff)
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
		utiltestingapi.MakeClusterQueue("fooCq").Obj(),
		utiltestingapi.MakeClusterQueue("barCq").Obj(),
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
				if err := mgr.AddClusterQueue(ctx, clusterQueues[0]); err != nil {
					t.Errorf("Failed adding clusterQueue: %v", err)
				}
				if err := mgr.AddLocalQueue(ctx, &queues[0]); err != nil {
					t.Errorf("Failed adding queue: %s", err)
				}
				go func() {
					if err := mgr.AddOrUpdateWorkload(logr.FromContextOrDiscard(ctx), &wl); err != nil {
						t.Errorf("Failed to add or update workload: %v", err)
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
					if err := mgr.AddOrUpdateWorkload(logr.FromContextOrDiscard(ctx), &wl); err != nil {
						t.Errorf("Failed to add or update workload: %v", err)
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
		"UpdateWorkload": {
			op: func(ctx context.Context, mgr *Manager) {
				if err := mgr.AddClusterQueue(ctx, clusterQueues[0]); err != nil {
					t.Errorf("Failed adding clusterQueue: %v", err)
				}
				if err := mgr.AddLocalQueue(ctx, &queues[0]); err != nil {
					t.Errorf("Failed adding queue: %s", err)
				}
				go func() {
					log := logr.FromContextOrDiscard(ctx)
					if err := mgr.UpdateWorkload(log, &wl); err != nil {
						t.Errorf("Failed to add or update workload: %v", err)
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
			ctx, _ := utiltesting.ContextWithLog(t)
			ctx, cancel := context.WithTimeout(ctx, headsTimeout)
			defer cancel()
			client := utiltesting.NewFakeClient(tc.initialObjs...)
			manager := NewManagerForUnitTests(client, nil)
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
	ctx, _ := utiltesting.ContextWithLog(t)
	manager := NewManagerForUnitTests(utiltesting.NewFakeClient(), nil)
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		cancel()
	}()
	manager.CleanUpOnContext(ctx)
	heads := manager.Heads(ctx)
	if len(heads) != 0 {
		t.Errorf("GetHeads returned elements, expected none")
	}
}

// TestHeadsCancelledNoLostWakeup verifies that cancellation does not leave Heads
// stuck in cond.Wait due to a missed broadcast from CleanUpOnContext.
func TestHeadsCancelledNoLostWakeup(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	manager := NewManagerForUnitTests(utiltesting.NewFakeClient(), nil)

	const iterations = 50
	for i := range iterations {
		headsCtx, cancel := context.WithCancel(ctx)
		headsDone := make(chan []workload.Info, 1)

		go manager.CleanUpOnContext(headsCtx)
		go func() {
			headsDone <- manager.Heads(headsCtx)
		}()

		// Wait until the Heads goroutine is actually parked in cond.Wait
		// before cancelling, so we deterministically exercise the broadcast
		// wakeup path rather than relying on scheduling jitter.
		waitForGoroutine(t, "sync.(*Cond).Wait", time.Second)
		cancel()

		select {
		case heads := <-headsDone:
			if len(heads) != 0 {
				t.Fatalf("iteration %d: Heads returned elements, expected none", i)
			}
		case <-time.After(time.Second):
			t.Fatalf("iteration %d: Heads got stuck after context cancellation", i)
		}
	}
}

// waitForGoroutine polls runtime.Stack until a goroutine whose stack contains
// the given substring is found, or the timeout expires.
func waitForGoroutine(t *testing.T, substr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		buf := make([]byte, 128*1024)
		n := runtime.Stack(buf, true)
		if strings.Contains(string(buf[:n]), substr) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for goroutine with %q in stack", substr)
		}
		runtime.Gosched()
	}
}

// popNamesFromCQ pops all the workloads from the clusterQueue and returns
// the keyed names in the order they are popped.
func popNamesFromCQ(cq *ClusterQueue) []workload.Reference {
	var names []workload.Reference
	for w := cq.Pop(); w != nil; w = cq.Pop() {
		names = append(names, workload.Key(w.Obj))
	}
	return names
}

// workloadNamesFromLQ returns all the names of the workloads in a localQueue.
func workloadNamesFromLQ(q *LocalQueue) sets.Set[workload.Reference] {
	names := sets.New[workload.Reference]()
	for k := range q.items {
		names.Insert(k)
	}
	return names
}

type fakeStatusChecker struct{}

func (c *fakeStatusChecker) ClusterQueueActive(name kueue.ClusterQueueReference) bool {
	return strings.Contains(string(name), "active-")
}

func TestGetPendingWorkloadsInfo(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	now := time.Now().Truncate(time.Second)

	clusterQueues := []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("cq").Obj(),
	}
	queues := []*kueue.LocalQueue{
		utiltestingapi.MakeLocalQueue("foo", "").ClusterQueue("cq").Obj(),
	}
	workloads := []*kueue.Workload{
		utiltestingapi.MakeWorkload("a", "").Queue("foo").Creation(now).Obj(),
		utiltestingapi.MakeWorkload("b", "").Queue("foo").Creation(now.Add(time.Second)).Obj(),
	}

	manager := NewManagerForUnitTests(utiltesting.NewFakeClient(), nil)
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
		if err := manager.AddOrUpdateWorkload(log, w); err != nil {
			t.Errorf("Failed to add or update workload: %v", err)
		}
	}

	cases := map[string]struct {
		cqName                   kueue.ClusterQueueReference
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

func TestQueueSecondPassIfNeeded(t *testing.T) {
	now := time.Now()

	baseWorkloadBuilder := utiltestingapi.MakeWorkload("foo", "default").
		Queue("tas-main").
		PodSets(*utiltestingapi.MakePodSet("one", 1).
			RequiredTopologyRequest(corev1.LabelHostname).
			Request(corev1.ResourceCPU, "1").
			Obj())

	baseWorkloadNeedingSecondPass := baseWorkloadBuilder.Clone().
		ReserveQuotaAt(
			utiltestingapi.MakeAdmission("tas-main").
				PodSets(
					utiltestingapi.MakePodSetAssignment("one").
						Assignment(corev1.ResourceCPU, "tas-default", "1000m").
						DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
						Obj(),
				).
				Obj(),
			now,
		).
		AdmissionCheck(kueue.AdmissionCheckState{
			Name:  "prov-check",
			State: kueue.CheckStateReady,
		})

	baseWorkloadNotNeedingSecondPass := baseWorkloadBuilder.Clone()

	cases := map[string]struct {
		workloads []*kueue.Workload
		update    func(*kueue.Workload)
		passTime  time.Duration
		wantReady sets.Set[workload.Reference]
	}{
		"single queued workload checked immediately": {
			workloads: []*kueue.Workload{
				baseWorkloadNeedingSecondPass.Obj(),
			},
		},
		"single queued workload checked after 1s": {
			workloads: []*kueue.Workload{
				baseWorkloadNeedingSecondPass.DeepCopy(),
				baseWorkloadNotNeedingSecondPass.DeepCopy(),
			},
			passTime:  time.Second,
			wantReady: sets.New(workload.Key(baseWorkloadNeedingSecondPass.Obj())),
		},
		"workload stops needing second pass after being queued": {
			workloads: []*kueue.Workload{
				baseWorkloadNeedingSecondPass.DeepCopy(),
			},
			update: func(wl *kueue.Workload) {
				wl.Status.Admission = nil
			},
			passTime:  time.Second,
			wantReady: nil,
		},
		"two queued workloads, one updated to no longer need second pass": {
			workloads: []*kueue.Workload{
				baseWorkloadNeedingSecondPass.Clone().Name("first").Obj(),
				baseWorkloadNeedingSecondPass.Clone().Name("second").Obj(),
			},
			update: func(wl *kueue.Workload) {
				wl.Status.Admission = nil
			},
			passTime:  time.Second,
			wantReady: sets.New(workload.NewReference("default", "second")),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)

			fakeClock := testingclock.NewFakeClock(now)
			manager := NewManagerForUnitTests(
				utiltesting.NewFakeClient(),
				nil,
				WithClock(fakeClock),
			)

			for _, wl := range tc.workloads {
				manager.QueueSecondPassIfNeeded(ctx, wl, 0)
			}

			if tc.update != nil {
				wl := tc.workloads[0]
				tc.update(wl)
				manager.QueueSecondPassIfNeeded(ctx, wl, 1)
			}

			fakeClock.Step(tc.passTime)

			gotReady := sets.New[workload.Reference]()
			for _, wlInfo := range manager.secondPassQueue.takeAllReady() {
				gotReady.Insert(workload.Key(wlInfo.Obj))
			}

			if diff := cmp.Diff(tc.wantReady, gotReady); diff != "" {
				t.Errorf("Unexpected ready workloads returned (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestGetWorkloadFromCache(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)

	cqToDelete := utiltestingapi.MakeClusterQueue("deleted-cq").Obj()
	clusterQueues := []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("cq").Obj(),
		cqToDelete,
	}

	lqToDelete := utiltestingapi.MakeLocalQueue("deleted-lq", "").ClusterQueue("deleted-cq").Obj()
	queues := []*kueue.LocalQueue{
		utiltestingapi.MakeLocalQueue("lq", "").ClusterQueue("cq").Obj(),
		utiltestingapi.MakeLocalQueue("lq-with-deleted-cq", "").ClusterQueue("deleted-cq").Obj(),
		lqToDelete,
	}

	cases := map[string]struct {
		wl           *kueue.Workload
		deleteFromLq queue.LocalQueueReference
		deleteFromCq kueue.ClusterQueueReference
		wantWl       *kueue.Workload
	}{
		"workload fetched from local queue": {
			wl:     utiltestingapi.MakeWorkload("a", "").Queue("lq").Obj(),
			wantWl: utiltestingapi.MakeWorkload("a", "").Queue("lq").Obj(),
		},
		"workload fetched from local queue with deleted cq": {
			wl:     utiltestingapi.MakeWorkload("a", "").Queue("lq-with-deleted-cq").Obj(),
			wantWl: utiltestingapi.MakeWorkload("a", "").Queue("lq-with-deleted-cq").Obj(),
		},
		"workload missing from lq; fetched from cq": {
			wl:           utiltestingapi.MakeWorkload("a", "").Queue("lq").Obj(),
			deleteFromLq: "/lq",
			wantWl:       utiltestingapi.MakeWorkload("a", "").Queue("lq").Obj(),
		},
		"workload missing from lq; cq deleted": {
			wl:           utiltestingapi.MakeWorkload("a", "").Queue("lq-with-deleted-cq").Obj(),
			deleteFromLq: "/lq-with-deleted-cq",
			wantWl:       nil,
		},
		"workload missing from both queues": {
			wl:           utiltestingapi.MakeWorkload("a", "").Queue("lq").Obj(),
			deleteFromLq: "/lq",
			deleteFromCq: "cq",
			wantWl:       nil,
		},
		"workload missing": {
			wl:     nil,
			wantWl: nil,
		},
		"missing local queue": {
			wl:     utiltestingapi.MakeWorkload("a", "").Queue("deleted-lq").Obj(),
			wantWl: nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			manager := NewManagerForUnitTests(utiltesting.NewFakeClient(), nil)
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

			wlRef := workload.NewReference("", "non-existent-wl")
			if tc.wl != nil {
				wlRef = workload.Key(tc.wl)
				if err := manager.AddOrUpdateWorkload(log, tc.wl); err != nil {
					t.Errorf("Failed to add or update workload: %v", err)
				}
				if tc.deleteFromLq != "" {
					delete(manager.localQueues[tc.deleteFromLq].items, wlRef)
				}
				if tc.deleteFromCq != "" {
					manager.hm.ClusterQueue(tc.deleteFromCq).heap.Delete(wlRef)
				}
			}

			manager.DeleteClusterQueue(cqToDelete)
			manager.DeleteLocalQueue(log, lqToDelete)

			gotWl := manager.GetWorkloadFromCache(wlRef)
			if diff := cmp.Diff(tc.wantWl, gotWl); diff != "" {
				t.Errorf("GetWorkloadFromCache returned wrong workload (-want,+got):\n%s", diff)
			}
		})
	}
}
