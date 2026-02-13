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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	queueafs "sigs.k8s.io/kueue/pkg/cache/queue/afs"
	"sigs.k8s.io/kueue/pkg/features"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	defaultNamespace = "default"
)

const (
	resourceGPU corev1.ResourceName = "example.com/gpu"
)

const (
	lowPriority  int32 = 0
	highPriority int32 = 1000
)

var (
	defaultOrdering = workload.Ordering{
		PodsReadyRequeuingTimestamp: config.EvictionTimestamp,
	}
)

func Test_PushOrUpdate(t *testing.T) {
	now := time.Now()
	minuteLater := now.Add(time.Minute)
	fakeClock := testingclock.NewFakeClock(now)
	cmpOpts := cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
	}
	wlBase := utiltestingapi.MakeWorkload("workload-1", defaultNamespace).Clone()

	cases := map[string]struct {
		workload                  *utiltestingapi.WorkloadWrapper
		wantWorkload              *workload.Info
		wantInAdmissibleWorkloads inadmissibleWorkloads
	}{
		"workload doesn't have re-queue state": {
			workload:     wlBase.Clone(),
			wantWorkload: workload.NewInfo(wlBase.Clone().ResourceVersion("1").Obj()),
		},
		"workload is still under the backoff waiting time": {
			workload: wlBase.Clone().
				RequeueState(ptr.To[int32](10), ptr.To(metav1.NewTime(minuteLater))).
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Reason: kueue.WorkloadEvictedByPodsReadyTimeout,
					Status: metav1.ConditionTrue,
				}).
				Condition(metav1.Condition{
					Type:   kueue.WorkloadRequeued,
					Status: metav1.ConditionFalse,
				}),
			wantInAdmissibleWorkloads: inadmissibleWorkloads{
				"default/workload-1": workload.NewInfo(wlBase.Clone().
					ResourceVersion("1").
					RequeueState(ptr.To[int32](10), ptr.To(metav1.NewTime(minuteLater))).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadEvicted,
						Reason: kueue.WorkloadEvictedByPodsReadyTimeout,
						Status: metav1.ConditionTrue,
					}).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadRequeued,
						Status: metav1.ConditionFalse,
					}).
					Obj()),
			},
		},
		"should wait for Requeued=true after backoff waiting time before push to heap": {
			workload: wlBase.Clone().
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Reason: kueue.WorkloadEvictedByPodsReadyTimeout,
					Status: metav1.ConditionTrue,
				}).
				Condition(metav1.Condition{
					Type:   kueue.WorkloadRequeued,
					Status: metav1.ConditionFalse,
				}),
			wantInAdmissibleWorkloads: inadmissibleWorkloads{
				"default/workload-1": workload.NewInfo(wlBase.Clone().
					ResourceVersion("1").
					Condition(metav1.Condition{
						Type:   kueue.WorkloadEvicted,
						Reason: kueue.WorkloadEvictedByPodsReadyTimeout,
						Status: metav1.ConditionTrue,
					}).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadRequeued,
						Status: metav1.ConditionFalse,
					}).
					Obj()),
			},
		},
		"should push workload to heap after Requeued=true": {
			workload: wlBase.Clone().
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Reason: kueue.WorkloadEvictedByPodsReadyTimeout,
					Status: metav1.ConditionTrue,
				}).
				Condition(metav1.Condition{
					Type:   kueue.WorkloadRequeued,
					Status: metav1.ConditionTrue,
				}),
			wantWorkload: workload.NewInfo(wlBase.Clone().
				ResourceVersion("1").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Reason: kueue.WorkloadEvictedByPodsReadyTimeout,
					Status: metav1.ConditionTrue,
				}).
				Condition(metav1.Condition{
					Type:   kueue.WorkloadRequeued,
					Status: metav1.ConditionTrue,
				}).
				Obj()),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			cq := newClusterQueueImpl(ctx, nil, defaultOrdering, fakeClock)

			if cq.PendingTotal() != 0 {
				t.Error("ClusterQueue should be empty")
			}
			cq.PushOrUpdate(workload.NewInfo(tc.workload.Clone().Obj()))
			if cq.PendingTotal() != 1 {
				t.Error("ClusterQueue should have one workload")
			}

			// Just used to validate the update operation.
			updatedWl := tc.workload.Clone().ResourceVersion("1").Obj()
			cq.PushOrUpdate(workload.NewInfo(updatedWl))
			newWl := cq.Pop()
			if newWl != nil && cq.PendingTotal() != 1 {
				t.Errorf("unexpected count of pending workloads (want=%d, got=%d)", 1, cq.PendingTotal())
			}
			if diff := cmp.Diff(tc.wantWorkload, newWl, cmpOpts...); len(diff) != 0 {
				t.Errorf("Unexpected workloads in heap (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantInAdmissibleWorkloads, cq.inadmissibleWorkloads, cmpOpts...); len(diff) != 0 {
				t.Errorf("Unexpected inadmissibleWorkloads (-want,+got):\n%s", diff)
			}
		})
	}
}

func Test_Pop(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	now := time.Now()
	cq := newClusterQueueImpl(ctx, nil, defaultOrdering, testingclock.NewFakeClock(now))
	wl1 := workload.NewInfo(utiltestingapi.MakeWorkload("workload-1", defaultNamespace).Creation(now).Obj())
	wl2 := workload.NewInfo(utiltestingapi.MakeWorkload("workload-2", defaultNamespace).Creation(now.Add(time.Second)).Obj())
	if cq.Pop() != nil {
		t.Error("ClusterQueue should be empty")
	}
	cq.PushOrUpdate(wl1)
	cq.PushOrUpdate(wl2)
	newWl := cq.Pop()
	if newWl == nil || newWl.Obj.Name != "workload-1" {
		t.Error("failed to Pop workload")
	}
	newWl = cq.Pop()
	if newWl == nil || newWl.Obj.Name != "workload-2" {
		t.Error("failed to Pop workload")
	}
	if cq.Pop() != nil {
		t.Error("ClusterQueue should be empty")
	}
}

func Test_Delete(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	cq := newClusterQueueImpl(ctx, nil, defaultOrdering, testingclock.NewFakeClock(time.Now()))
	wl1 := utiltestingapi.MakeWorkload("workload-1", defaultNamespace).Obj()
	wl2 := utiltestingapi.MakeWorkload("workload-2", defaultNamespace).Obj()
	cq.PushOrUpdate(workload.NewInfo(wl1))
	cq.PushOrUpdate(workload.NewInfo(wl2))
	if cq.PendingTotal() != 2 {
		t.Error("ClusterQueue should have two workload")
	}
	cq.Delete(log, workload.Key(wl1))
	if cq.PendingTotal() != 1 {
		t.Error("ClusterQueue should have only one workload")
	}
	// Change workload item, ClusterQueue.Delete should only care about the namespace and name.
	wl2.Spec = kueue.WorkloadSpec{QueueName: "default"}
	cq.Delete(log, workload.Key(wl2))
	if cq.PendingTotal() != 0 {
		t.Error("ClusterQueue should have be empty")
	}
}

func Test_Info(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	cq := newClusterQueueImpl(ctx, nil, defaultOrdering, testingclock.NewFakeClock(time.Now()))
	wl := utiltestingapi.MakeWorkload("workload-1", defaultNamespace).Obj()
	if info := cq.Info(workload.Key(wl)); info != nil {
		t.Error("Workload should not exist")
	}
	cq.PushOrUpdate(workload.NewInfo(wl))
	if info := cq.Info(workload.Key(wl)); info == nil {
		t.Error("Expected workload to exist")
	}
}

func Test_AddFromLocalQueue(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	cq := newClusterQueueImpl(ctx, nil, defaultOrdering, testingclock.NewFakeClock(time.Now()))
	wl := utiltestingapi.MakeWorkload("workload-1", defaultNamespace).Obj()
	queue := &LocalQueue{
		items: map[workload.Reference]*workload.Info{
			workload.Reference(wl.Name): workload.NewInfo(wl),
		},
	}
	cq.PushOrUpdate(workload.NewInfo(wl))
	if added := cq.AddFromLocalQueue(queue, nil); added {
		t.Error("expected workload not to be added")
	}
	cq.Delete(log, workload.Key(wl))
	if added := cq.AddFromLocalQueue(queue, nil); !added {
		t.Error("workload should be added to the ClusterQueue")
	}
}

func TestSnapshotDeterministicOrder(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	backoffUntil := now.Add(time.Hour)
	lqName := kueue.LocalQueueName("foo")

	cases := map[string]struct {
		workloads             []*kueue.Workload
		inadmissibleWorkloads []*kueue.Workload
	}{
		"heap only": {
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("wl1", defaultNamespace).Queue(lqName).Creation(now).UID(types.UID("uid-1")).Obj(),
				utiltestingapi.MakeWorkload("wl2", defaultNamespace).Queue(lqName).Creation(now).UID(types.UID("uid-2")).Obj(),
				utiltestingapi.MakeWorkload("wl3", defaultNamespace).Queue(lqName).Creation(now).UID(types.UID("uid-3")).Obj(),
			},
		},
		"heap and inadmissible": {
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("wl1", defaultNamespace).Queue(lqName).Creation(now).UID(types.UID("uid-1")).Obj(),
				utiltestingapi.MakeWorkload("wl2", defaultNamespace).Queue(lqName).Creation(now).UID(types.UID("uid-2")).Obj(),
			},
			inadmissibleWorkloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload("wl3", defaultNamespace).Queue(lqName).Creation(now).UID(types.UID("uid-3")).
					RequeueState(ptr.To[int32](1), ptr.To(metav1.NewTime(backoffUntil))).
					Condition(metav1.Condition{Type: kueue.WorkloadRequeued, Status: metav1.ConditionFalse}).
					Obj(),
				utiltestingapi.MakeWorkload("wl4", defaultNamespace).Queue(lqName).Creation(now).UID(types.UID("uid-4")).
					RequeueState(ptr.To[int32](1), ptr.To(metav1.NewTime(backoffUntil))).
					Condition(metav1.Condition{Type: kueue.WorkloadRequeued, Status: metav1.ConditionFalse}).
					Obj(),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			cq := newClusterQueueImpl(ctx, nil, defaultOrdering, testingclock.NewFakeClock(now))

			for _, w := range tc.workloads {
				cq.PushOrUpdate(workload.NewInfo(w))
			}
			for _, w := range tc.inadmissibleWorkloads {
				cq.requeueIfNotPresent(log, workload.NewInfo(w), false)
			}

			firstSnap := cq.Snapshot()
			for i := 1; i < 10; i++ {
				if diff := cmp.Diff(firstSnap, cq.Snapshot()); diff != "" {
					t.Errorf("Snapshot order changed on call %d (-first,+got):\n%s", i+1, diff)
				}
			}
		})
	}
}

func Test_DeleteFromLocalQueue(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	cq := newClusterQueueImpl(ctx, nil, defaultOrdering, testingclock.NewFakeClock(time.Now()))
	q := utiltestingapi.MakeLocalQueue("foo", "").ClusterQueue("cq").Obj()
	qImpl := newLocalQueue(q)
	wl1 := utiltestingapi.MakeWorkload("wl1", "").Queue(kueue.LocalQueueName(q.Name)).Obj()
	wl2 := utiltestingapi.MakeWorkload("wl2", "").Queue(kueue.LocalQueueName(q.Name)).Obj()
	wl3 := utiltestingapi.MakeWorkload("wl3", "").Queue(kueue.LocalQueueName(q.Name)).Obj()
	wl4 := utiltestingapi.MakeWorkload("wl4", "").Queue(kueue.LocalQueueName(q.Name)).Obj()
	admissibleworkloads := []*kueue.Workload{wl1, wl2}
	inadmissibleWorkloads := []*kueue.Workload{wl3, wl4}

	for _, w := range admissibleworkloads {
		wInfo := workload.NewInfo(w)
		cq.PushOrUpdate(wInfo)
		qImpl.AddOrUpdate(wInfo)
	}

	for _, w := range inadmissibleWorkloads {
		wInfo := workload.NewInfo(w)
		cq.requeueIfNotPresent(log, wInfo, false)
		qImpl.AddOrUpdate(wInfo)
	}

	wantPending := len(admissibleworkloads) + len(inadmissibleWorkloads)
	if cq.PendingTotal() != wantPending {
		t.Errorf("clusterQueue's workload number not right, want %v, got %v", wantPending, cq.PendingTotal())
	}
	if cq.inadmissibleWorkloads.len() != len(inadmissibleWorkloads) {
		t.Errorf("clusterQueue's workload number in inadmissibleWorkloads not right, want %v, got %v", len(inadmissibleWorkloads), cq.inadmissibleWorkloads.len())
	}

	cq.DeleteFromLocalQueue(log, qImpl, nil)
	if cq.PendingTotal() != 0 {
		t.Error("clusterQueue should be empty")
	}
}

func TestClusterQueueImpl(t *testing.T) {
	cl := utiltesting.NewFakeClient(
		utiltesting.MakeNamespaceWrapper("ns1").Label("dep", "eng").Obj(),
		utiltesting.MakeNamespaceWrapper("ns2").Label("dep", "sales").Obj(),
		utiltesting.MakeNamespaceWrapper("ns3").Label("dep", "marketing").Obj(),
	)

	now := time.Now()
	minuteLater := now.Add(time.Minute)
	fakeClock := testingclock.NewFakeClock(now)

	var workloads = []*kueue.Workload{
		utiltestingapi.MakeWorkload("w1", "ns1").Queue("q1").Obj(),
		utiltestingapi.MakeWorkload("w2", "ns2").Queue("q2").Obj(),
		utiltestingapi.MakeWorkload("w3", "ns3").Queue("q3").Obj(),
		utiltestingapi.MakeWorkload("w4-requeue-state", "ns1").
			RequeueState(ptr.To[int32](1), ptr.To(metav1.NewTime(minuteLater))).
			Queue("q1").
			Condition(metav1.Condition{
				Type:   kueue.WorkloadEvicted,
				Reason: kueue.WorkloadEvictedByPodsReadyTimeout,
				Status: metav1.ConditionTrue,
			}).
			Obj(),
	}
	var updatedWorkloads = make([]*kueue.Workload, len(workloads))

	updatedWorkloads[0] = workloads[0].DeepCopy()
	updatedWorkloads[0].Spec.QueueName = "q2"
	updatedWorkloads[1] = workloads[1].DeepCopy()
	updatedWorkloads[1].Spec.QueueName = "q1"

	tests := map[string]struct {
		workloadsToAdd                    []*kueue.Workload
		inadmissibleWorkloadsToRequeue    []*workload.Info
		admissibleWorkloadsToRequeue      []*workload.Info
		workloadsToUpdate                 []*kueue.Workload
		workloadsToDelete                 []*kueue.Workload
		queueInadmissibleWorkloads        bool
		wantActiveWorkloads               []workload.Reference
		wantPending                       int
		wantInadmissibleWorkloadsRequeued bool
	}{
		"add, update, delete workload": {
			workloadsToAdd:                 []*kueue.Workload{workloads[0], workloads[1], workloads[3]},
			inadmissibleWorkloadsToRequeue: []*workload.Info{},
			workloadsToUpdate:              []*kueue.Workload{updatedWorkloads[0]},
			workloadsToDelete:              []*kueue.Workload{workloads[0]},
			wantActiveWorkloads:            []workload.Reference{workload.Key(workloads[1])},
			wantPending:                    2,
		},
		"re-queue inadmissible workload; workloads with requeueState can't re-queue": {
			workloadsToAdd:                 []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToRequeue: []*workload.Info{workload.NewInfo(workloads[1]), workload.NewInfo(workloads[3])},
			wantActiveWorkloads:            []workload.Reference{workload.Key(workloads[0])},
			wantPending:                    3,
		},
		"re-queue admissible workload that was inadmissible": {
			workloadsToAdd:                 []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToRequeue: []*workload.Info{workload.NewInfo(workloads[1]), workload.NewInfo(workloads[3])},
			admissibleWorkloadsToRequeue:   []*workload.Info{workload.NewInfo(workloads[1]), workload.NewInfo(workloads[3])},
			wantActiveWorkloads:            []workload.Reference{workload.Key(workloads[0]), workload.Key(workloads[1])},
			wantPending:                    3,
		},
		"re-queue inadmissible workload and flush": {
			workloadsToAdd:                    []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToRequeue:    []*workload.Info{workload.NewInfo(workloads[1]), workload.NewInfo(workloads[3])},
			queueInadmissibleWorkloads:        true,
			wantActiveWorkloads:               []workload.Reference{workload.Key(workloads[0]), workload.Key(workloads[1])},
			wantPending:                       3,
			wantInadmissibleWorkloadsRequeued: true,
		},
		"avoid re-queueing inadmissible workloads not matching namespace selector": {
			workloadsToAdd:                 []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToRequeue: []*workload.Info{workload.NewInfo(workloads[2])},
			queueInadmissibleWorkloads:     true,
			wantActiveWorkloads:            []workload.Reference{workload.Key(workloads[0])},
			wantPending:                    2,
		},
		"update inadmissible workload": {
			workloadsToAdd:                 []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToRequeue: []*workload.Info{workload.NewInfo(workloads[1])},
			workloadsToUpdate:              []*kueue.Workload{updatedWorkloads[1]},
			wantActiveWorkloads:            []workload.Reference{workload.Key(workloads[0]), workload.Key(workloads[1])},
			wantPending:                    2,
		},
		"delete inadmissible workload": {
			workloadsToAdd:                 []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToRequeue: []*workload.Info{workload.NewInfo(workloads[1])},
			workloadsToDelete:              []*kueue.Workload{workloads[1]},
			queueInadmissibleWorkloads:     true,
			wantActiveWorkloads:            []workload.Reference{workload.Key(workloads[0])},
			wantPending:                    1,
		},
		"update inadmissible workload without changes": {
			inadmissibleWorkloadsToRequeue: []*workload.Info{workload.NewInfo(workloads[1])},
			workloadsToUpdate:              []*kueue.Workload{workloads[1]},
			wantPending:                    1,
		},
		"requeue inadmissible workload twice": {
			inadmissibleWorkloadsToRequeue: []*workload.Info{workload.NewInfo(workloads[1]), workload.NewInfo(workloads[1])},
			wantPending:                    1,
		},
		"update reclaimable pods in inadmissible": {
			inadmissibleWorkloadsToRequeue: []*workload.Info{
				workload.NewInfo(utiltestingapi.MakeWorkload("w", "").PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).Obj()),
			},
			workloadsToUpdate: []*kueue.Workload{
				utiltestingapi.MakeWorkload("w", "").PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).
					ReclaimablePods(kueue.ReclaimablePod{Name: kueue.DefaultPodSetName, Count: 1}).
					Obj(),
			},
			wantActiveWorkloads: []workload.Reference{"/w"},
			wantPending:         1,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			cq := newClusterQueueImpl(ctx, nil, defaultOrdering, fakeClock)
			err := cq.Update(utiltestingapi.MakeClusterQueue("cq").
				NamespaceSelector(&metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      "dep",
							Operator: metav1.LabelSelectorOpIn,
							Values:   []string{"eng", "sales"},
						},
					},
				}).Obj())
			if err != nil {
				t.Fatalf("Failed updating clusterQueue: %v", err)
			}

			for _, w := range test.workloadsToAdd {
				cq.PushOrUpdate(workload.NewInfo(w))
			}

			for _, w := range test.inadmissibleWorkloadsToRequeue {
				cq.requeueIfNotPresent(log, w, false)
			}
			for _, w := range test.admissibleWorkloadsToRequeue {
				cq.requeueIfNotPresent(log, w, true)
			}

			for _, w := range test.workloadsToUpdate {
				cq.PushOrUpdate(workload.NewInfo(w))
			}

			for _, w := range test.workloadsToDelete {
				cq.Delete(log, workload.Key(w))
			}

			if test.queueInadmissibleWorkloads {
				if diff := cmp.Diff(test.wantInadmissibleWorkloadsRequeued,
					queueInadmissibleWorkloads(ctx, cq, cl)); diff != "" {
					t.Errorf("Unexpected requeuing of inadmissible workloads (-want,+got):\n%s", diff)
				}
			}

			gotWorkloads, _ := cq.Dump()
			if diff := cmp.Diff(test.wantActiveWorkloads, gotWorkloads, cmpDump...); diff != "" {
				t.Errorf("Unexpected active workloads in cluster foo (-want,+got):\n%s", diff)
			}
			if cq.PendingTotal() != test.wantPending {
				t.Errorf("Got %d pending workloads, want %d", cq.PendingTotal(), test.wantPending)
			}
		})
	}
}

func TestQueueInadmissibleWorkloadsDuringScheduling(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	cq := newClusterQueueImpl(ctx, nil, defaultOrdering, testingclock.NewFakeClock(time.Now()))
	cq.namespaceSelector = labels.Everything()
	wl := utiltestingapi.MakeWorkload("workload-1", defaultNamespace).Obj()
	cl := utiltesting.NewFakeClient(wl, utiltesting.MakeNamespace(defaultNamespace))
	cq.PushOrUpdate(workload.NewInfo(wl))

	wantActiveWorkloads := []workload.Reference{workload.Key(wl)}

	activeWorkloads, _ := cq.Dump()
	if diff := cmp.Diff(wantActiveWorkloads, activeWorkloads, cmpDump...); diff != "" {
		t.Errorf("Unexpected active workloads before events (-want,+got):\n%s", diff)
	}

	// Simulate requeuing during scheduling attempt.
	head := cq.Pop()
	queueInadmissibleWorkloads(ctx, cq, cl)
	cq.requeueIfNotPresent(log, head, false)

	activeWorkloads, _ = cq.Dump()
	wantActiveWorkloads = []workload.Reference{workload.Key(wl)}
	if diff := cmp.Diff(wantActiveWorkloads, activeWorkloads, cmpDump...); diff != "" {
		t.Errorf("Unexpected active workloads after scheduling with requeuing (-want,+got):\n%s", diff)
	}

	// Simulating scheduling again without requeuing.
	head = cq.Pop()
	cq.requeueIfNotPresent(log, head, false)
	activeWorkloads, _ = cq.Dump()
	wantActiveWorkloads = nil
	if diff := cmp.Diff(wantActiveWorkloads, activeWorkloads, cmpDump...); diff != "" {
		t.Errorf("Unexpected active workloads after scheduling (-want,+got):\n%s", diff)
	}
}

func TestBackoffWaitingTimeExpired(t *testing.T) {
	now := time.Now()
	minuteLater := now.Add(time.Minute)
	minuteAgo := now.Add(-time.Minute)
	fakeClock := testingclock.NewFakeClock(now)

	cases := map[string]struct {
		workloadInfo *workload.Info
		want         bool
	}{
		"workload still have Requeued=false": {
			workloadInfo: workload.NewInfo(utiltestingapi.MakeWorkload("wl", "ns").Condition(metav1.Condition{
				Type:   kueue.WorkloadRequeued,
				Status: metav1.ConditionFalse,
			}).Obj()),
			want: false,
		},
		"workload doesn't have requeueState": {
			workloadInfo: workload.NewInfo(utiltestingapi.MakeWorkload("wl", "ns").Obj()),
			want:         true,
		},
		"workload doesn't have an evicted condition with reason=PodsReadyTimeout": {
			workloadInfo: workload.NewInfo(utiltestingapi.MakeWorkload("wl", "ns").
				RequeueState(ptr.To[int32](10), nil).Obj()),
			want: true,
		},
		"now already has exceeded requeueAt": {
			workloadInfo: workload.NewInfo(utiltestingapi.MakeWorkload("wl", "ns").
				RequeueState(ptr.To[int32](10), ptr.To(metav1.NewTime(minuteAgo))).
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Status: metav1.ConditionTrue,
					Reason: kueue.WorkloadEvictedByPodsReadyTimeout,
				}).Obj()),
			want: true,
		},
		"now hasn't yet exceeded requeueAt": {
			workloadInfo: workload.NewInfo(utiltestingapi.MakeWorkload("wl", "ns").
				RequeueState(ptr.To[int32](10), ptr.To(metav1.NewTime(minuteLater))).
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Status: metav1.ConditionTrue,
					Reason: kueue.WorkloadEvictedByPodsReadyTimeout,
				}).Obj()),
			want: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			cq := newClusterQueueImpl(ctx, nil, defaultOrdering, fakeClock)
			got := cq.backoffWaitingTimeExpired(tc.workloadInfo)
			if tc.want != got {
				t.Errorf("Unexpected result from backoffWaitingTimeExpired\nwant: %v\ngot: %v\n", tc.want, got)
			}
		})
	}
}

func TestBestEffortFIFORequeueIfNotPresent(t *testing.T) {
	tests := map[string]struct {
		reason           RequeueReason
		lastAssignment   *workload.AssignmentClusterQueueState
		wantInadmissible bool
	}{
		"failure after nomination": {
			reason:           RequeueReasonFailedAfterNomination,
			wantInadmissible: false,
		},
		"namespace doesn't match": {
			reason:           RequeueReasonNamespaceMismatch,
			wantInadmissible: true,
		},
		"didn't fit and no pending flavors": {
			reason: RequeueReasonGeneric,
			lastAssignment: &workload.AssignmentClusterQueueState{
				LastTriedFlavorIdx: []map[corev1.ResourceName]int{
					{
						corev1.ResourceMemory: -1,
					},
					{
						corev1.ResourceCPU:    -1,
						corev1.ResourceMemory: -1,
					},
				},
			},
			wantInadmissible: true,
		},
		"didn't fit but pending flavors": {
			reason: RequeueReasonGeneric,
			lastAssignment: &workload.AssignmentClusterQueueState{
				LastTriedFlavorIdx: []map[corev1.ResourceName]int{
					{
						corev1.ResourceCPU:    -1,
						corev1.ResourceMemory: 0,
					},
					{
						corev1.ResourceMemory: 1,
					},
				},
			},
			wantInadmissible: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			cq, _ := newClusterQueue(ctx, nil,
				&kueue.ClusterQueue{
					Spec: kueue.ClusterQueueSpec{
						QueueingStrategy: kueue.BestEffortFIFO,
					},
				},
				workload.Ordering{PodsReadyRequeuingTimestamp: config.EvictionTimestamp},
				nil, nil, nil)
			wl := utiltestingapi.MakeWorkload("workload-1", defaultNamespace).Obj()
			info := workload.NewInfo(wl)
			info.LastAssignment = tc.lastAssignment
			if ok := cq.RequeueIfNotPresent(ctx, info, tc.reason); !ok {
				t.Error("failed to requeue nonexistent workload")
			}

			gotInadmissible := cq.inadmissibleWorkloads.hasKey(workload.Key(wl))
			if diff := cmp.Diff(tc.wantInadmissible, gotInadmissible); diff != "" {
				t.Errorf("Unexpected inadmissible status (-want,+got):\n%s", diff)
			}

			if ok := cq.RequeueIfNotPresent(ctx, workload.NewInfo(wl), tc.reason); ok {
				t.Error("Re-queued a workload that was already present")
			}
		})
	}
}

func TestFIFOClusterQueue(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	q, err := newClusterQueue(ctx, nil,
		&kueue.ClusterQueue{
			Spec: kueue.ClusterQueueSpec{
				QueueingStrategy: kueue.StrictFIFO,
			},
		},
		workload.Ordering{
			PodsReadyRequeuingTimestamp: config.EvictionTimestamp,
		}, nil, nil, nil)
	if err != nil {
		t.Fatalf("Failed creating ClusterQueue %v", err)
	}
	now := metav1.Now()
	ws := []*kueue.Workload{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "now",
				CreationTimestamp: now,
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "before",
				CreationTimestamp: metav1.NewTime(now.Add(-time.Second)),
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "after",
				CreationTimestamp: metav1.NewTime(now.Add(time.Second)),
			},
		},
	}
	for _, w := range ws {
		q.PushOrUpdate(workload.NewInfo(w))
	}
	got := q.Pop()
	if got == nil {
		t.Fatal("Queue is empty")
	}
	if got.Obj.Name != "before" {
		t.Errorf("Popped workload %q want %q", got.Obj.Name, "before")
	}
	wlInfo := workload.NewInfo(&kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "after",
			CreationTimestamp: metav1.NewTime(now.Add(-time.Minute)),
		},
	})
	q.PushOrUpdate(wlInfo)
	got = q.Pop()
	if got == nil {
		t.Fatal("Queue is empty")
	}
	if got.Obj.Name != "after" {
		t.Errorf("Popped workload %q want %q", got.Obj.Name, "after")
	}

	q.Delete(log, workload.NewReference("", "now"))
	got = q.Pop()
	if got != nil {
		t.Errorf("Queue is not empty, popped workload %q", got.Obj.Name)
	}
}

func TestStrictFIFO(t *testing.T) {
	t1 := time.Now()
	t2 := t1.Add(time.Second)
	t3 := t2.Add(time.Second)
	for _, tt := range []struct {
		name             string
		w1               *kueue.Workload
		w2               *kueue.Workload
		workloadOrdering *workload.Ordering
		expected         string
	}{
		{
			name: "w1.priority is higher than w2.priority",
			w1: utiltestingapi.MakeWorkload("w1", "").
				Creation(t1).
				PodPriorityClassRef("highPriority").
				Priority(highPriority).
				Obj(),
			w2: utiltestingapi.MakeWorkload("w2", "").
				Creation(t2).
				PodPriorityClassRef("lowPriority").
				Priority(lowPriority).
				Obj(),
			expected: "w1",
		},
		{
			name: "w1.priority equals w2.priority and w1.create time is earlier than w2.create time",
			w1: utiltestingapi.MakeWorkload("w1", "").
				Creation(t1).
				Obj(),
			w2: utiltestingapi.MakeWorkload("w2", "").
				Creation(t2).
				Obj(),
			expected: "w1",
		},
		{
			name: "w1.priority equals w2.priority and w1.create time is earlier than w2.create time but w1 was evicted",
			w1: utiltestingapi.MakeWorkload("w1", "").
				Creation(t1).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadEvicted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(t3),
					Reason:             kueue.WorkloadEvictedByPodsReadyTimeout,
					Message:            "by test",
				}).
				Obj(),
			w2: utiltestingapi.MakeWorkload("w2", "").
				Creation(t2).
				Obj(),
			expected: "w2",
		},
		{
			name: "w1.priority equals w2.priority and w1.create time is earlier than w2.create time and w1 was evicted but kueue is configured to always use the creation timestamp",
			w1: utiltestingapi.MakeWorkload("w1", "").
				Creation(t1).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadEvicted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(t3),
					Reason:             kueue.WorkloadEvictedByPodsReadyTimeout,
					Message:            "by test",
				}).
				Obj(),
			w2: utiltestingapi.MakeWorkload("w2", "").
				Creation(t2).
				Obj(),
			workloadOrdering: &workload.Ordering{
				PodsReadyRequeuingTimestamp: config.CreationTimestamp,
			},
			expected: "w1",
		},
		{
			name: "p1.priority is lower than p2.priority and w1.create time is earlier than w2.create time",
			w1: utiltestingapi.MakeWorkload("w1", "").
				Creation(t1).
				PodPriorityClassRef("lowPriority").
				Priority(lowPriority).
				Obj(),
			w2: utiltestingapi.MakeWorkload("w2", "").
				Creation(t2).
				PodPriorityClassRef("highPriority").
				Priority(highPriority).
				Obj(),
			expected: "w2",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.workloadOrdering == nil {
				// The default ordering:
				tt.workloadOrdering = &workload.Ordering{PodsReadyRequeuingTimestamp: config.EvictionTimestamp}
			}
			ctx, _ := utiltesting.ContextWithLog(t)
			q, err := newClusterQueue(ctx, nil,
				&kueue.ClusterQueue{
					Spec: kueue.ClusterQueueSpec{
						QueueingStrategy: kueue.StrictFIFO,
					},
				},
				*tt.workloadOrdering,
				nil, nil, nil)
			if err != nil {
				t.Fatalf("Failed creating ClusterQueue %v", err)
			}

			q.PushOrUpdate(workload.NewInfo(tt.w1))
			q.PushOrUpdate(workload.NewInfo(tt.w2))

			got := q.Pop()
			if got == nil {
				t.Fatal("Queue is empty")
			}
			if got.Obj.Name != tt.expected {
				t.Errorf("Popped workload %q want %q", got.Obj.Name, tt.expected)
			}
		})
	}
}

func TestStrictFIFORequeueIfNotPresent(t *testing.T) {
	tests := map[RequeueReason]struct {
		wantInadmissible bool
	}{
		RequeueReasonFailedAfterNomination: {
			wantInadmissible: false,
		},
		RequeueReasonNamespaceMismatch: {
			wantInadmissible: true,
		},
		RequeueReasonGeneric: {
			wantInadmissible: false,
		},
	}

	for reason, test := range tests {
		t.Run(string(reason), func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			cq, _ := newClusterQueue(ctx, nil,
				&kueue.ClusterQueue{
					Spec: kueue.ClusterQueueSpec{
						QueueingStrategy: kueue.StrictFIFO,
					},
				},
				workload.Ordering{PodsReadyRequeuingTimestamp: config.EvictionTimestamp},
				nil, nil, nil)
			wl := utiltestingapi.MakeWorkload("workload-1", defaultNamespace).Obj()
			if ok := cq.RequeueIfNotPresent(ctx, workload.NewInfo(wl), reason); !ok {
				t.Error("failed to requeue nonexistent workload")
			}

			gotInadmissible := cq.inadmissibleWorkloads.hasKey(workload.Key(wl))
			if test.wantInadmissible != gotInadmissible {
				t.Errorf("Got inadmissible after requeue %t, want %t", gotInadmissible, test.wantInadmissible)
			}

			if ok := cq.RequeueIfNotPresent(ctx, workload.NewInfo(wl), reason); ok {
				t.Error("Re-queued a workload that was already present")
			}
		})
	}
}

func TestFsAdmission(t *testing.T) {
	wlCmpOpts := []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime"),
	}

	cases := map[string]struct {
		cq                    *kueue.ClusterQueue
		lqs                   []kueue.LocalQueue
		afsConfig             *config.AdmissionFairSharing
		wls                   []kueue.Workload
		wantWl                kueue.Workload
		initConsumedResources map[string]corev1.ResourceList
	}{
		"workloads are ordered by LQ usage, instead of priorities": {
			cq: utiltestingapi.MakeClusterQueue("cq").
				AdmissionMode(kueue.UsageBasedAdmissionFairSharing).
				Obj(),
			lqs: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lqA", "default").
					FairSharing(&kueue.FairSharing{
						Weight: ptr.To(resource.MustParse("1")),
					}).
					Obj(),
				*utiltestingapi.MakeLocalQueue("lqB", "default").
					FairSharing(&kueue.FairSharing{
						Weight: ptr.To(resource.MustParse("1")),
					}).Obj(),
			},
			afsConfig: &config.AdmissionFairSharing{},
			initConsumedResources: map[string]corev1.ResourceList{
				"default/lqA": {corev1.ResourceCPU: resource.MustParse("2")},
				"default/lqB": {corev1.ResourceCPU: resource.MustParse("1")},
			},
			wls: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wlA-high", "default").Queue("lqA").Priority(2).Obj(),
				*utiltestingapi.MakeWorkload("wlB-low", "default").Queue("lqB").Priority(1).Obj(),
			},
			wantWl: *utiltestingapi.MakeWorkload("wlB-low", "default").Queue("lqB").Priority(1).Obj(),
		},
		"workloads are ordered by LQ usage with respect to resource weights": {
			cq: utiltestingapi.MakeClusterQueue("cq").
				AdmissionMode(kueue.UsageBasedAdmissionFairSharing).
				Obj(),
			lqs: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lqA", "default").
					FairSharing(&kueue.FairSharing{
						Weight: ptr.To(resource.MustParse("1")),
					}).
					Obj(),
				*utiltestingapi.MakeLocalQueue("lqB", "default").
					FairSharing(&kueue.FairSharing{
						Weight: ptr.To(resource.MustParse("1")),
					}).Obj(),
			},
			afsConfig: &config.AdmissionFairSharing{
				ResourceWeights: map[corev1.ResourceName]float64{
					corev1.ResourceCPU: 0,
					resourceGPU:        1,
				},
			},
			initConsumedResources: map[string]corev1.ResourceList{
				"default/lqA": {corev1.ResourceCPU: resource.MustParse("1"), resourceGPU: resource.MustParse("10")},
				"default/lqB": {corev1.ResourceCPU: resource.MustParse("1000"), resourceGPU: resource.MustParse("1")},
			},
			wls: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wlA-high", "default").Queue("lqA").Priority(2).Obj(),
				*utiltestingapi.MakeWorkload("wlB-low", "default").Queue("lqB").Priority(1).Obj(),
			},
			wantWl: *utiltestingapi.MakeWorkload("wlB-low", "default").Queue("lqB").Priority(1).Obj(),
		},
		"workloads are ordered by LQ usage with respect to LQs' fair sharing weights": {
			cq: utiltestingapi.MakeClusterQueue("cq").
				AdmissionMode(kueue.UsageBasedAdmissionFairSharing).
				Obj(),
			lqs: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lqA", "default").
					FairSharing(&kueue.FairSharing{
						Weight: ptr.To(resource.MustParse("1")),
					}).
					Obj(),
				*utiltestingapi.MakeLocalQueue("lqB", "default").
					FairSharing(&kueue.FairSharing{
						Weight: ptr.To(resource.MustParse("2")),
					}).Obj(),
			},
			afsConfig: &config.AdmissionFairSharing{},
			initConsumedResources: map[string]corev1.ResourceList{
				"default/lqA": {corev1.ResourceCPU: resource.MustParse("10")},
				"default/lqB": {corev1.ResourceCPU: resource.MustParse("6")},
			},
			wls: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wlA-high", "default").Queue("lqA").Priority(2).Obj(),
				*utiltestingapi.MakeWorkload("wlB-low", "default").Queue("lqB").Priority(1).Obj(),
			},
			wantWl: *utiltestingapi.MakeWorkload("wlB-low", "default").Queue("lqB").Priority(1).Obj(),
		},
		"workloads with the same LQ usage are ordered by priority": {
			cq: utiltestingapi.MakeClusterQueue("cq").
				AdmissionMode(kueue.UsageBasedAdmissionFairSharing).
				Obj(),
			lqs: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lqA", "default").
					FairSharing(&kueue.FairSharing{
						Weight: ptr.To(resource.MustParse("1")),
					}).Obj(),
			},
			afsConfig: &config.AdmissionFairSharing{},
			initConsumedResources: map[string]corev1.ResourceList{
				"default/lqA": {corev1.ResourceCPU: resource.MustParse("10")},
			},
			wls: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wlA-low", "default").Queue("lqA").Priority(1).Obj(),
				*utiltestingapi.MakeWorkload("wlA-high", "default").Queue("lqA").Priority(2).Obj(),
			},
			wantWl: *utiltestingapi.MakeWorkload("wlA-high", "default").Queue("lqA").Priority(2).Obj(),
		},
		"workloads with NoFairSharing CQ are ordered by priority": {
			cq: utiltestingapi.MakeClusterQueue("cq").
				AdmissionMode(kueue.NoAdmissionFairSharing).
				Obj(),
			lqs: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lqA", "default").Obj(),
			},
			afsConfig: &config.AdmissionFairSharing{},
			wls: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wlA-low", "default").Queue("lqA").Priority(1).Obj(),
				*utiltestingapi.MakeWorkload("wlA-high", "default").Queue("lqA").Priority(2).Obj(),
			},
			wantWl: *utiltestingapi.MakeWorkload("wlA-high", "default").Queue("lqA").Priority(2).Obj(),
		},
		"workloads with no FS config are ordered by priority": {
			cq: utiltestingapi.MakeClusterQueue("cq").
				AdmissionMode(kueue.NoAdmissionFairSharing).
				Obj(),
			lqs: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("lqA", "default").Obj(),
			},
			wls: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wlA-low", "default").Queue("lqA").Priority(1).Obj(),
				*utiltestingapi.MakeWorkload("wlA-high", "default").Queue("lqA").Priority(2).Obj(),
			},
			wantWl: *utiltestingapi.MakeWorkload("wlA-high", "default").Queue("lqA").Priority(2).Obj(),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.AdmissionFairSharing, true)
			builder := utiltesting.NewClientBuilder()
			for _, lq := range tc.lqs {
				builder = builder.WithObjects(&lq)
			}
			client := builder.Build()
			ctx := context.Background()

			afsConsumedResources := queueafs.NewAfsConsumedResources()
			for lqKey, consumedResources := range tc.initConsumedResources {
				afsConsumedResources.Set(utilqueue.LocalQueueReference(lqKey), consumedResources, time.Now())
			}

			cq, _ := newClusterQueue(ctx, client, tc.cq, defaultOrdering, tc.afsConfig, nil, afsConsumedResources)
			for _, wl := range tc.wls {
				cq.PushOrUpdate(workload.NewInfo(&wl))
			}

			gotWl := cq.Pop()
			if diff := cmp.Diff(tc.wantWl, *gotWl.Obj, wlCmpOpts...); diff != "" {
				t.Errorf("Unexpected workloads on top of the heap (-want,+got):\n%s", diff)
			}
		})
	}
}
