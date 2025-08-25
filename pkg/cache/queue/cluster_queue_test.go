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
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
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
	wlBase := utiltesting.MakeWorkload("workload-1", defaultNamespace).Clone()

	cases := map[string]struct {
		workload                  *utiltesting.WorkloadWrapper
		wantWorkload              *workload.Info
		wantInAdmissibleWorkloads map[workload.Reference]*workload.Info
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
			wantInAdmissibleWorkloads: map[workload.Reference]*workload.Info{
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
			wantInAdmissibleWorkloads: map[workload.Reference]*workload.Info{
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
			cq := newClusterQueueImpl(t.Context(), nil, defaultOrdering, fakeClock, nil, false, nil)

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
			if newWl != nil && cq.Pending() != 1 {
				t.Errorf("unexpected count of pending workloads (want=%d, got=%d)", 1, cq.Pending())
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
	now := time.Now()
	cq := newClusterQueueImpl(t.Context(), nil, defaultOrdering, testingclock.NewFakeClock(now), nil, false, nil)
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
	cq := newClusterQueueImpl(t.Context(), nil, defaultOrdering, testingclock.NewFakeClock(time.Now()), nil, false, nil)
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
	cq := newClusterQueueImpl(t.Context(), nil, defaultOrdering, testingclock.NewFakeClock(time.Now()), nil, false, nil)
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
	cq := newClusterQueueImpl(t.Context(), nil, defaultOrdering, testingclock.NewFakeClock(time.Now()), nil, false, nil)
	wl := utiltesting.MakeWorkload("workload-1", defaultNamespace).Obj()
	queue := &LocalQueue{
		items: map[workload.Reference]*workload.Info{
			workload.Reference(wl.Name): workload.NewInfo(wl),
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
	cq := newClusterQueueImpl(t.Context(), nil, defaultOrdering, testingclock.NewFakeClock(time.Now()), nil, false, nil)
	q := utiltesting.MakeLocalQueue("foo", "").ClusterQueue("cq").Obj()
	qImpl := newLocalQueue(q)
	wl1 := utiltesting.MakeWorkload("wl1", "").Queue(kueue.LocalQueueName(q.Name)).Obj()
	wl2 := utiltesting.MakeWorkload("wl2", "").Queue(kueue.LocalQueueName(q.Name)).Obj()
	wl3 := utiltesting.MakeWorkload("wl3", "").Queue(kueue.LocalQueueName(q.Name)).Obj()
	wl4 := utiltesting.MakeWorkload("wl4", "").Queue(kueue.LocalQueueName(q.Name)).Obj()
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
		utiltesting.MakeNamespaceWrapper("ns1").Label("dep", "eng").Obj(),
		utiltesting.MakeNamespaceWrapper("ns2").Label("dep", "sales").Obj(),
		utiltesting.MakeNamespaceWrapper("ns3").Label("dep", "marketing").Obj(),
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
				workload.NewInfo(utiltesting.MakeWorkload("w", "").PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).Obj()),
			},
			workloadsToUpdate: []*kueue.Workload{
				utiltesting.MakeWorkload("w", "").PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).
					ReclaimablePods(kueue.ReclaimablePod{Name: kueue.DefaultPodSetName, Count: 1}).
					Obj(),
			},
			wantActiveWorkloads: []workload.Reference{"/w"},
			wantPending:         1,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			cq := newClusterQueueImpl(t.Context(), nil, defaultOrdering, fakeClock, nil, false, nil)
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
					cq.QueueInadmissibleWorkloads(t.Context(), cl)); diff != "" {
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
	cq := newClusterQueueImpl(t.Context(), nil, defaultOrdering, testingclock.NewFakeClock(time.Now()), nil, false, nil)
	cq.namespaceSelector = labels.Everything()
	wl := utiltesting.MakeWorkload("workload-1", defaultNamespace).Obj()
	cl := utiltesting.NewFakeClient(wl, utiltesting.MakeNamespace(defaultNamespace))
	ctx := t.Context()
	cq.PushOrUpdate(workload.NewInfo(wl))

	wantActiveWorkloads := []workload.Reference{workload.Key(wl)}

	activeWorkloads, _ := cq.Dump()
	if diff := cmp.Diff(wantActiveWorkloads, activeWorkloads, cmpDump...); diff != "" {
		t.Errorf("Unexpected active workloads before events (-want,+got):\n%s", diff)
	}

	// Simulate requeuing during scheduling attempt.
	head := cq.Pop()
	cq.QueueInadmissibleWorkloads(ctx, cl)
	cq.requeueIfNotPresent(head, false)

	activeWorkloads, _ = cq.Dump()
	wantActiveWorkloads = []workload.Reference{workload.Key(wl)}
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
		"workload still have Requeued=false": {
			workloadInfo: workload.NewInfo(utiltesting.MakeWorkload("wl", "ns").Condition(metav1.Condition{
				Type:   kueue.WorkloadRequeued,
				Status: metav1.ConditionFalse,
			}).Obj()),
			want: false,
		},
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
			cq := newClusterQueueImpl(t.Context(), nil, defaultOrdering, fakeClock, nil, false, nil)
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
			cq, _ := newClusterQueue(t.Context(), nil,
				&kueue.ClusterQueue{
					Spec: kueue.ClusterQueueSpec{
						QueueingStrategy: kueue.BestEffortFIFO,
					},
				},
				workload.Ordering{PodsReadyRequeuingTimestamp: config.EvictionTimestamp},
				nil, nil)
			wl := utiltesting.MakeWorkload("workload-1", defaultNamespace).Obj()
			info := workload.NewInfo(wl)
			info.LastAssignment = tc.lastAssignment
			if ok := cq.RequeueIfNotPresent(info, tc.reason); !ok {
				t.Error("failed to requeue nonexistent workload")
			}

			_, gotInadmissible := cq.inadmissibleWorkloads[workload.Key(wl)]
			if diff := cmp.Diff(tc.wantInadmissible, gotInadmissible); diff != "" {
				t.Errorf("Unexpected inadmissible status (-want,+got):\n%s", diff)
			}

			if ok := cq.RequeueIfNotPresent(workload.NewInfo(wl), tc.reason); ok {
				t.Error("Re-queued a workload that was already present")
			}
		})
	}
}

func TestFIFOClusterQueue(t *testing.T) {
	q, err := newClusterQueue(t.Context(), nil,
		&kueue.ClusterQueue{
			Spec: kueue.ClusterQueueSpec{
				QueueingStrategy: kueue.StrictFIFO,
			},
		},
		workload.Ordering{
			PodsReadyRequeuingTimestamp: config.EvictionTimestamp,
		}, nil, nil)
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

	q.Delete(&kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "now"},
	})
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
			w1: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "w1",
					CreationTimestamp: metav1.NewTime(t1),
				},
				Spec: kueue.WorkloadSpec{
					PriorityClassName: "highPriority",
					Priority:          ptr.To(highPriority),
				},
			},
			w2: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "w2",
					CreationTimestamp: metav1.NewTime(t2),
				},
				Spec: kueue.WorkloadSpec{
					PriorityClassName: "lowPriority",
					Priority:          ptr.To(lowPriority),
				},
			},
			expected: "w1",
		},
		{
			name: "w1.priority equals w2.priority and w1.create time is earlier than w2.create time",
			w1: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "w1",
					CreationTimestamp: metav1.NewTime(t1),
				},
			},
			w2: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "w2",
					CreationTimestamp: metav1.NewTime(t2),
				},
			},
			expected: "w1",
		},
		{
			name: "w1.priority equals w2.priority and w1.create time is earlier than w2.create time but w1 was evicted",
			w1: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "w1",
					CreationTimestamp: metav1.NewTime(t1),
				},
				Status: kueue.WorkloadStatus{
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadEvicted,
							Status:             metav1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(t3),
							Reason:             kueue.WorkloadEvictedByPodsReadyTimeout,
							Message:            "by test",
						},
					},
				},
			},
			w2: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "w2",
					CreationTimestamp: metav1.NewTime(t2),
				},
			},
			expected: "w2",
		},
		{
			name: "w1.priority equals w2.priority and w1.create time is earlier than w2.create time and w1 was evicted but kueue is configured to always use the creation timestamp",
			w1: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "w1",
					CreationTimestamp: metav1.NewTime(t1),
				},
				Status: kueue.WorkloadStatus{
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadEvicted,
							Status:             metav1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(t3),
							Reason:             kueue.WorkloadEvictedByPodsReadyTimeout,
							Message:            "by test",
						},
					},
				},
			},
			w2: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "w2",
					CreationTimestamp: metav1.NewTime(t2),
				},
			},
			workloadOrdering: &workload.Ordering{
				PodsReadyRequeuingTimestamp: config.CreationTimestamp,
			},
			expected: "w1",
		},
		{
			name: "p1.priority is lower than p2.priority and w1.create time is earlier than w2.create time",
			w1: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "w1",
					CreationTimestamp: metav1.NewTime(t1),
				},
				Spec: kueue.WorkloadSpec{
					PriorityClassName: "lowPriority",
					Priority:          ptr.To(lowPriority),
				},
			},
			w2: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "w2",
					CreationTimestamp: metav1.NewTime(t2),
				},
				Spec: kueue.WorkloadSpec{
					PriorityClassName: "highPriority",
					Priority:          ptr.To(highPriority),
				},
			},
			expected: "w2",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.workloadOrdering == nil {
				// The default ordering:
				tt.workloadOrdering = &workload.Ordering{PodsReadyRequeuingTimestamp: config.EvictionTimestamp}
			}
			q, err := newClusterQueue(t.Context(), nil,
				&kueue.ClusterQueue{
					Spec: kueue.ClusterQueueSpec{
						QueueingStrategy: kueue.StrictFIFO,
					},
				},
				*tt.workloadOrdering,
				nil, nil)
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
			cq, _ := newClusterQueue(t.Context(), nil,
				&kueue.ClusterQueue{
					Spec: kueue.ClusterQueueSpec{
						QueueingStrategy: kueue.StrictFIFO,
					},
				},
				workload.Ordering{PodsReadyRequeuingTimestamp: config.EvictionTimestamp},
				nil, nil)
			wl := utiltesting.MakeWorkload("workload-1", defaultNamespace).Obj()
			if ok := cq.RequeueIfNotPresent(workload.NewInfo(wl), reason); !ok {
				t.Error("failed to requeue nonexistent workload")
			}

			_, gotInadmissible := cq.inadmissibleWorkloads[workload.Key(wl)]
			if test.wantInadmissible != gotInadmissible {
				t.Errorf("Got inadmissible after requeue %t, want %t", gotInadmissible, test.wantInadmissible)
			}

			if ok := cq.RequeueIfNotPresent(workload.NewInfo(wl), reason); ok {
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
		cq        *kueue.ClusterQueue
		lqs       []kueue.LocalQueue
		afsConfig *config.AdmissionFairSharing
		wls       []kueue.Workload
		wantWl    kueue.Workload
	}{
		"workloads are ordered by LQ usage, instead of priorities": {
			cq: utiltesting.MakeClusterQueue("cq").
				AdmissionMode(kueue.UsageBasedAdmissionFairSharing).
				Obj(),
			lqs: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("lqA", "default").
					FairSharing(&kueue.FairSharing{
						Weight: ptr.To(resource.MustParse("1")),
					}).
					FairSharingStatus(
						&kueue.FairSharingStatus{
							AdmissionFairSharingStatus: &kueue.AdmissionFairSharingStatus{
								ConsumedResources: map[corev1.ResourceName]resource.Quantity{
									corev1.ResourceCPU: resource.MustParse("2"),
								},
							},
						},
					).
					Obj(),
				*utiltesting.MakeLocalQueue("lqB", "default").
					FairSharing(&kueue.FairSharing{
						Weight: ptr.To(resource.MustParse("1")),
					}).
					FairSharingStatus(
						&kueue.FairSharingStatus{
							AdmissionFairSharingStatus: &kueue.AdmissionFairSharingStatus{
								ConsumedResources: map[corev1.ResourceName]resource.Quantity{
									corev1.ResourceCPU: resource.MustParse("1"),
								},
							},
						},
					).Obj(),
			},
			afsConfig: &config.AdmissionFairSharing{},
			wls: []kueue.Workload{
				*utiltesting.MakeWorkload("wlA-high", "default").Queue("lqA").Priority(2).Obj(),
				*utiltesting.MakeWorkload("wlB-low", "default").Queue("lqB").Priority(1).Obj(),
			},
			wantWl: *utiltesting.MakeWorkload("wlB-low", "default").Queue("lqB").Priority(1).Obj(),
		},
		"workloads are ordered by LQ usage with respect to resource weights": {
			cq: utiltesting.MakeClusterQueue("cq").
				AdmissionMode(kueue.UsageBasedAdmissionFairSharing).
				Obj(),
			lqs: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("lqA", "default").
					FairSharing(&kueue.FairSharing{
						Weight: ptr.To(resource.MustParse("1")),
					}).
					FairSharingStatus(
						&kueue.FairSharingStatus{
							AdmissionFairSharingStatus: &kueue.AdmissionFairSharingStatus{
								ConsumedResources: map[corev1.ResourceName]resource.Quantity{
									corev1.ResourceCPU: resource.MustParse("1"),
									resourceGPU:        resource.MustParse("10"),
								},
							},
						},
					).
					Obj(),
				*utiltesting.MakeLocalQueue("lqB", "default").
					FairSharing(&kueue.FairSharing{
						Weight: ptr.To(resource.MustParse("1")),
					}).
					FairSharingStatus(
						&kueue.FairSharingStatus{
							AdmissionFairSharingStatus: &kueue.AdmissionFairSharingStatus{
								ConsumedResources: map[corev1.ResourceName]resource.Quantity{
									corev1.ResourceCPU: resource.MustParse("1000"),
									resourceGPU:        resource.MustParse("1"),
								},
							},
						},
					).Obj(),
			},
			afsConfig: &config.AdmissionFairSharing{
				ResourceWeights: map[corev1.ResourceName]float64{
					corev1.ResourceCPU: 0,
					resourceGPU:        1,
				},
			},
			wls: []kueue.Workload{
				*utiltesting.MakeWorkload("wlA-high", "default").Queue("lqA").Priority(2).Obj(),
				*utiltesting.MakeWorkload("wlB-low", "default").Queue("lqB").Priority(1).Obj(),
			},
			wantWl: *utiltesting.MakeWorkload("wlB-low", "default").Queue("lqB").Priority(1).Obj(),
		},
		"workloads are ordered by LQ usage with respect to LQs' fair sharing weights": {
			cq: utiltesting.MakeClusterQueue("cq").
				AdmissionMode(kueue.UsageBasedAdmissionFairSharing).
				Obj(),
			lqs: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("lqA", "default").
					FairSharing(&kueue.FairSharing{
						Weight: ptr.To(resource.MustParse("1")),
					}).
					FairSharingStatus(
						&kueue.FairSharingStatus{
							AdmissionFairSharingStatus: &kueue.AdmissionFairSharingStatus{
								ConsumedResources: map[corev1.ResourceName]resource.Quantity{
									corev1.ResourceCPU: resource.MustParse("10"),
								},
							},
						},
					).
					Obj(),
				*utiltesting.MakeLocalQueue("lqB", "default").
					FairSharing(&kueue.FairSharing{
						Weight: ptr.To(resource.MustParse("2")),
					}).
					FairSharingStatus(
						&kueue.FairSharingStatus{
							AdmissionFairSharingStatus: &kueue.AdmissionFairSharingStatus{
								ConsumedResources: map[corev1.ResourceName]resource.Quantity{
									corev1.ResourceCPU: resource.MustParse("6"),
								},
							},
						},
					).Obj(),
			},
			afsConfig: &config.AdmissionFairSharing{},
			wls: []kueue.Workload{
				*utiltesting.MakeWorkload("wlA-high", "default").Queue("lqA").Priority(2).Obj(),
				*utiltesting.MakeWorkload("wlB-low", "default").Queue("lqB").Priority(1).Obj(),
			},
			wantWl: *utiltesting.MakeWorkload("wlB-low", "default").Queue("lqB").Priority(1).Obj(),
		},
		"workloads with the same LQ usage are ordered by priority": {
			cq: utiltesting.MakeClusterQueue("cq").
				AdmissionMode(kueue.UsageBasedAdmissionFairSharing).
				Obj(),
			lqs: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("lqA", "default").
					FairSharing(&kueue.FairSharing{
						Weight: ptr.To(resource.MustParse("1")),
					}).
					FairSharingStatus(
						&kueue.FairSharingStatus{
							AdmissionFairSharingStatus: &kueue.AdmissionFairSharingStatus{
								ConsumedResources: map[corev1.ResourceName]resource.Quantity{
									corev1.ResourceCPU: resource.MustParse("10"),
								},
							},
						},
					).Obj(),
			},
			afsConfig: &config.AdmissionFairSharing{},
			wls: []kueue.Workload{
				*utiltesting.MakeWorkload("wlA-low", "default").Queue("lqA").Priority(1).Obj(),
				*utiltesting.MakeWorkload("wlA-high", "default").Queue("lqA").Priority(2).Obj(),
			},
			wantWl: *utiltesting.MakeWorkload("wlA-high", "default").Queue("lqA").Priority(2).Obj(),
		},
		"workloads with NoFairSharing CQ are ordered by priority": {
			cq: utiltesting.MakeClusterQueue("cq").
				AdmissionMode(kueue.NoAdmissionFairSharing).
				Obj(),
			lqs: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("lqA", "default").Obj(),
			},
			afsConfig: &config.AdmissionFairSharing{},
			wls: []kueue.Workload{
				*utiltesting.MakeWorkload("wlA-low", "default").Queue("lqA").Priority(1).Obj(),
				*utiltesting.MakeWorkload("wlA-high", "default").Queue("lqA").Priority(2).Obj(),
			},
			wantWl: *utiltesting.MakeWorkload("wlA-high", "default").Queue("lqA").Priority(2).Obj(),
		},
		"workloads with no FS config are ordered by priority": {
			cq: utiltesting.MakeClusterQueue("cq").
				AdmissionMode(kueue.NoAdmissionFairSharing).
				Obj(),
			lqs: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("lqA", "default").Obj(),
			},
			wls: []kueue.Workload{
				*utiltesting.MakeWorkload("wlA-low", "default").Queue("lqA").Priority(1).Obj(),
				*utiltesting.MakeWorkload("wlA-high", "default").Queue("lqA").Priority(2).Obj(),
			},
			wantWl: *utiltesting.MakeWorkload("wlA-high", "default").Queue("lqA").Priority(2).Obj(),
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

			cq, _ := newClusterQueue(ctx, client, tc.cq, defaultOrdering, tc.afsConfig, nil)
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
