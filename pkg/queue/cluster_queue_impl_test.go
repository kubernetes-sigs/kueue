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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	defaultNamespace = "default"
)

var (
	defaultQueueOrderingFunc = queueOrderingFunc(workload.Ordering{
		PodsReadyRequeuingTimestamp: config.EvictionTimestamp,
	})
)

func Test_PushOrUpdate(t *testing.T) {
	now := time.Now()
	minuteLater := now.Add(time.Minute)
	fakeClock := testingclock.NewFakeClock(now)
	cmpOpts := []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
	}
	wlBase := utiltesting.MakeWorkload("workload-1", defaultNamespace).Clone()

	cases := map[string]struct {
		workload                  *utiltesting.WorkloadWrapper
		wantWorkload              *workload.Info
		wantInAdmissibleWorkloads map[string]*workload.Info
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
				}),
			wantInAdmissibleWorkloads: map[string]*workload.Info{
				"default/workload-1": workload.NewInfo(wlBase.Clone().
					ResourceVersion("1").
					RequeueState(ptr.To[int32](10), ptr.To(metav1.NewTime(minuteLater))).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadEvicted,
						Reason: kueue.WorkloadEvictedByPodsReadyTimeout,
						Status: metav1.ConditionTrue,
					}).
					Obj()),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cq := newClusterQueueImpl(defaultQueueOrderingFunc, fakeClock)

			if cq.Pending() != 0 {
				t.Error("ClusterQueue should be empty")
			}
			cq.PushOrUpdate(workload.NewInfo(tc.workload.Clone().Obj()))
			if cq.Pending() != 1 {
				t.Error("ClusterQueue should have one workload")
			}

			// Just used to validate the update operation.
			updatedWl := tc.workload.Clone().ResourceVersion("1").Obj()
			cq.PushOrUpdate(workload.NewInfo(updatedWl))
			newWl := cq.Pop()
			if newWl != nil && cq.Pending() != 0 {
				t.Error("failed to update a workload in ClusterQueue")
			}
			if diff := cmp.Diff(tc.wantWorkload, newWl, cmpOpts...); len(diff) != 0 {
				t.Errorf("Unexpectd workloads in heap (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantInAdmissibleWorkloads, cq.inadmissibleWorkloads, cmpOpts...); len(diff) != 0 {
				t.Errorf("Unexpectd inadmissibleWorkloads (-want,+got):\n%s", diff)
			}
		})
	}
}

func Test_Pop(t *testing.T) {
	now := time.Now()
	cq := newClusterQueueImpl(defaultQueueOrderingFunc, testingclock.NewFakeClock(now))
	wl1 := workload.NewInfo(utiltesting.MakeWorkload("workload-1", defaultNamespace).Creation(now).Obj())
	wl2 := workload.NewInfo(utiltesting.MakeWorkload("workload-2", defaultNamespace).Creation(now.Add(time.Second)).Obj())
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
	cq := newClusterQueueImpl(defaultQueueOrderingFunc, testingclock.NewFakeClock(time.Now()))
	wl1 := utiltesting.MakeWorkload("workload-1", defaultNamespace).Obj()
	wl2 := utiltesting.MakeWorkload("workload-2", defaultNamespace).Obj()
	cq.PushOrUpdate(workload.NewInfo(wl1))
	cq.PushOrUpdate(workload.NewInfo(wl2))
	if cq.Pending() != 2 {
		t.Error("ClusterQueue should have two workload")
	}
	cq.Delete(wl1)
	if cq.Pending() != 1 {
		t.Error("ClusterQueue should have only one workload")
	}
	// Change workload item, ClusterQueue.Delete should only care about the namespace and name.
	wl2.Spec = kueue.WorkloadSpec{QueueName: "default"}
	cq.Delete(wl2)
	if cq.Pending() != 0 {
		t.Error("ClusterQueue should have be empty")
	}
}

func Test_Info(t *testing.T) {
	cq := newClusterQueueImpl(defaultQueueOrderingFunc, testingclock.NewFakeClock(time.Now()))
	wl := utiltesting.MakeWorkload("workload-1", defaultNamespace).Obj()
	if info := cq.Info(workload.Key(wl)); info != nil {
		t.Error("Workload should not exist")
	}
	cq.PushOrUpdate(workload.NewInfo(wl))
	if info := cq.Info(workload.Key(wl)); info == nil {
		t.Error("Expected workload to exist")
	}
}

func Test_AddFromLocalQueue(t *testing.T) {
	cq := newClusterQueueImpl(defaultQueueOrderingFunc, testingclock.NewFakeClock(time.Now()))
	wl := utiltesting.MakeWorkload("workload-1", defaultNamespace).Obj()
	queue := &LocalQueue{
		items: map[string]*workload.Info{
			wl.Name: workload.NewInfo(wl),
		},
	}
	cq.PushOrUpdate(workload.NewInfo(wl))
	if added := cq.AddFromLocalQueue(queue); added {
		t.Error("expected workload not to be added")
	}
	cq.Delete(wl)
	if added := cq.AddFromLocalQueue(queue); !added {
		t.Error("workload should be added to the ClusterQueue")
	}
}

func Test_DeleteFromLocalQueue(t *testing.T) {
	cq := newClusterQueueImpl(defaultQueueOrderingFunc, testingclock.NewFakeClock(time.Now()))
	q := utiltesting.MakeLocalQueue("foo", "").ClusterQueue("cq").Obj()
	qImpl := newLocalQueue(q)
	wl1 := utiltesting.MakeWorkload("wl1", "").Queue(q.Name).Obj()
	wl2 := utiltesting.MakeWorkload("wl2", "").Queue(q.Name).Obj()
	wl3 := utiltesting.MakeWorkload("wl3", "").Queue(q.Name).Obj()
	wl4 := utiltesting.MakeWorkload("wl4", "").Queue(q.Name).Obj()
	admissibleworkloads := []*kueue.Workload{wl1, wl2}
	inadmissibleWorkloads := []*kueue.Workload{wl3, wl4}

	for _, w := range admissibleworkloads {
		wInfo := workload.NewInfo(w)
		cq.PushOrUpdate(wInfo)
		qImpl.AddOrUpdate(wInfo)
	}

	for _, w := range inadmissibleWorkloads {
		wInfo := workload.NewInfo(w)
		cq.requeueIfNotPresent(wInfo, false)
		qImpl.AddOrUpdate(wInfo)
	}

	wantPending := len(admissibleworkloads) + len(inadmissibleWorkloads)
	if pending := cq.Pending(); pending != wantPending {
		t.Errorf("clusterQueue's workload number not right, want %v, got %v", wantPending, pending)
	}
	if len(cq.inadmissibleWorkloads) != len(inadmissibleWorkloads) {
		t.Errorf("clusterQueue's workload number in inadmissibleWorkloads not right, want %v, got %v", len(inadmissibleWorkloads), len(cq.inadmissibleWorkloads))
	}

	cq.DeleteFromLocalQueue(qImpl)
	if cq.Pending() != 0 {
		t.Error("clusterQueue should be empty")
	}
}

func TestClusterQueueImpl(t *testing.T) {
	cl := utiltesting.NewFakeClient(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns1", Labels: map[string]string{"dep": "eng"}}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns2", Labels: map[string]string{"dep": "sales"}}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns3", Labels: map[string]string{"dep": "marketing"}}},
	)

	now := time.Now()
	minuteLater := now.Add(time.Minute)
	fakeClock := testingclock.NewFakeClock(now)

	var workloads = []*kueue.Workload{
		utiltesting.MakeWorkload("w1", "ns1").Queue("q1").Obj(),
		utiltesting.MakeWorkload("w2", "ns2").Queue("q2").Obj(),
		utiltesting.MakeWorkload("w3", "ns3").Queue("q3").Obj(),
		utiltesting.MakeWorkload("w4-requeue-state", "ns1").
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
		wantActiveWorkloads               []string
		wantPending                       int
		wantInadmissibleWorkloadsRequeued bool
	}{
		"add, update, delete workload": {
			workloadsToAdd:                 []*kueue.Workload{workloads[0], workloads[1], workloads[3]},
			inadmissibleWorkloadsToRequeue: []*workload.Info{},
			workloadsToUpdate:              []*kueue.Workload{updatedWorkloads[0]},
			workloadsToDelete:              []*kueue.Workload{workloads[0]},
			wantActiveWorkloads:            []string{workload.Key(workloads[1])},
			wantPending:                    2,
		},
		"re-queue inadmissible workload; workloads with requeueState can't re-queue": {
			workloadsToAdd:                 []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToRequeue: []*workload.Info{workload.NewInfo(workloads[1]), workload.NewInfo(workloads[3])},
			wantActiveWorkloads:            []string{workload.Key(workloads[0])},
			wantPending:                    3,
		},
		"re-queue admissible workload that was inadmissible": {
			workloadsToAdd:                 []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToRequeue: []*workload.Info{workload.NewInfo(workloads[1]), workload.NewInfo(workloads[3])},
			admissibleWorkloadsToRequeue:   []*workload.Info{workload.NewInfo(workloads[1]), workload.NewInfo(workloads[3])},
			wantActiveWorkloads:            []string{workload.Key(workloads[0]), workload.Key(workloads[1])},
			wantPending:                    3,
		},
		"re-queue inadmissible workload and flush": {
			workloadsToAdd:                    []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToRequeue:    []*workload.Info{workload.NewInfo(workloads[1]), workload.NewInfo(workloads[3])},
			queueInadmissibleWorkloads:        true,
			wantActiveWorkloads:               []string{workload.Key(workloads[0]), workload.Key(workloads[1])},
			wantPending:                       3,
			wantInadmissibleWorkloadsRequeued: true,
		},
		"avoid re-queueing inadmissible workloads not matching namespace selector": {
			workloadsToAdd:                 []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToRequeue: []*workload.Info{workload.NewInfo(workloads[2])},
			queueInadmissibleWorkloads:     true,
			wantActiveWorkloads:            []string{workload.Key(workloads[0])},
			wantPending:                    2,
		},
		"update inadmissible workload": {
			workloadsToAdd:                 []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToRequeue: []*workload.Info{workload.NewInfo(workloads[1])},
			workloadsToUpdate:              []*kueue.Workload{updatedWorkloads[1]},
			wantActiveWorkloads:            []string{workload.Key(workloads[0]), workload.Key(workloads[1])},
			wantPending:                    2,
		},
		"delete inadmissible workload": {
			workloadsToAdd:                 []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToRequeue: []*workload.Info{workload.NewInfo(workloads[1])},
			workloadsToDelete:              []*kueue.Workload{workloads[1]},
			queueInadmissibleWorkloads:     true,
			wantActiveWorkloads:            []string{workload.Key(workloads[0])},
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
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			cq := newClusterQueueImpl(defaultQueueOrderingFunc, fakeClock)
			err := cq.Update(utiltesting.MakeClusterQueue("cq").
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
				cq.requeueIfNotPresent(w, false)
			}
			for _, w := range test.admissibleWorkloadsToRequeue {
				cq.requeueIfNotPresent(w, true)
			}

			for _, w := range test.workloadsToUpdate {
				cq.PushOrUpdate(workload.NewInfo(w))
			}

			for _, w := range test.workloadsToDelete {
				cq.Delete(w)
			}

			if test.queueInadmissibleWorkloads {
				if diff := cmp.Diff(test.wantInadmissibleWorkloadsRequeued,
					cq.QueueInadmissibleWorkloads(context.Background(), cl)); diff != "" {
					t.Errorf("Unexpected requeuing of inadmissible workloads (-want,+got):\n%s", diff)
				}
			}

			gotWorkloads, _ := cq.Dump()
			if diff := cmp.Diff(test.wantActiveWorkloads, gotWorkloads, cmpDump...); diff != "" {
				t.Errorf("Unexpected active workloads in cluster foo (-want,+got):\n%s", diff)
			}
			if got := cq.Pending(); got != test.wantPending {
				t.Errorf("Got %d pending workloads, want %d", got, test.wantPending)
			}
		})
	}
}

func TestQueueInadmissibleWorkloadsDuringScheduling(t *testing.T) {
	cq := newClusterQueueImpl(defaultQueueOrderingFunc, testingclock.NewFakeClock(time.Now()))
	cq.namespaceSelector = labels.Everything()
	wl := utiltesting.MakeWorkload("workload-1", defaultNamespace).Obj()
	cl := utiltesting.NewFakeClient(
		wl,
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: defaultNamespace},
		},
	)
	ctx := context.Background()
	cq.PushOrUpdate(workload.NewInfo(wl))

	wantActiveWorkloads := []string{workload.Key(wl)}

	activeWorkloads, _ := cq.Dump()
	if diff := cmp.Diff(wantActiveWorkloads, activeWorkloads, cmpDump...); diff != "" {
		t.Errorf("Unexpected active workloads before events (-want,+got):\n%s", diff)
	}

	// Simulate requeuing during scheduling attempt.
	head := cq.Pop()
	cq.QueueInadmissibleWorkloads(ctx, cl)
	cq.requeueIfNotPresent(head, false)

	activeWorkloads, _ = cq.Dump()
	wantActiveWorkloads = []string{workload.Key(wl)}
	if diff := cmp.Diff(wantActiveWorkloads, activeWorkloads, cmpDump...); diff != "" {
		t.Errorf("Unexpected active workloads after scheduling with requeuing (-want,+got):\n%s", diff)
	}

	// Simulating scheduling again without requeuing.
	head = cq.Pop()
	cq.requeueIfNotPresent(head, false)
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
		"workload doesn't have requeueState": {
			workloadInfo: workload.NewInfo(utiltesting.MakeWorkload("wl", "ns").Obj()),
			want:         true,
		},
		"workload doesn't have an evicted condition with reason=PodsReadyTimeout": {
			workloadInfo: workload.NewInfo(utiltesting.MakeWorkload("wl", "ns").
				RequeueState(ptr.To[int32](10), nil).Obj()),
			want: true,
		},
		"now already has exceeded requeueAt": {
			workloadInfo: workload.NewInfo(utiltesting.MakeWorkload("wl", "ns").
				RequeueState(ptr.To[int32](10), ptr.To(metav1.NewTime(minuteAgo))).
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Status: metav1.ConditionTrue,
					Reason: kueue.WorkloadEvictedByPodsReadyTimeout,
				}).Obj()),
			want: true,
		},
		"now hasn't yet exceeded requeueAt": {
			workloadInfo: workload.NewInfo(utiltesting.MakeWorkload("wl", "ns").
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
			cq := newClusterQueueImpl(defaultQueueOrderingFunc, fakeClock)
			got := cq.backoffWaitingTimeExpired(tc.workloadInfo)
			if tc.want != got {
				t.Errorf("Unexpected result from backoffWaitingTimeExpired\nwant: %v\ngot: %v\n", tc.want, got)
			}
		})
	}
}
