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
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	queueafs "sigs.k8s.io/kueue/pkg/cache/queue/afs"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
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
				RequeueState(new(int32(10)), new(metav1.NewTime(minuteLater))).
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
					RequeueState(new(int32(10)), new(metav1.NewTime(minuteLater))).
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
			cq := newClusterQueueImpl(ctx, nil, nil, defaultOrdering, fakeClock)

			if cq.PendingTotal() != 0 {
				t.Error("ClusterQueue should be empty")
			}
			cq.PushOrUpdate(workload.NewInfo(tc.workload.DeepCopy()))
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
			if diff := cmp.Diff(tc.wantInAdmissibleWorkloads, cq.workloads.inadmissible, cmpOpts...); len(diff) != 0 {
				t.Errorf("Unexpected inadmissibleWorkloads (-want,+got):\n%s", diff)
			}
		})
	}
}

func Test_Pop(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	now := time.Now()
	cq := newClusterQueueImpl(ctx, nil, nil, defaultOrdering, testingclock.NewFakeClock(now))
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

func TestPushOrUpdateSkipsInflightWorkload(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	now := time.Now()
	cq := newClusterQueueImpl(ctx, nil, nil, defaultOrdering, testingclock.NewFakeClock(now))

	wl := utiltestingapi.MakeWorkload("workload-1", defaultNamespace).Creation(now).Obj()
	cq.PushOrUpdate(workload.NewInfo(wl))

	// Pop makes the workload inflight.
	head := cq.Pop()
	if head == nil {
		t.Fatal("expected to pop workload")
	}

	// Simulate a concurrent PushOrUpdate while the workload is inflight.
	updatedWl := utiltestingapi.MakeWorkload("workload-1", defaultNamespace).
		Creation(now).ResourceVersion("1").Obj()
	cq.PushOrUpdate(workload.NewInfo(updatedWl))

	// The workload should not be on the heap or in inadmissible.
	activeWorkloads, _ := cq.Dump()
	if len(activeWorkloads) != 0 {
		t.Errorf("expected empty heap while workload is inflight, got %v", activeWorkloads)
	}

	inadmissibleWorkloads, _ := cq.DumpInadmissible()
	if len(inadmissibleWorkloads) != 0 {
		t.Errorf("expected no inadmissible workloads while workload is inflight, got %v", inadmissibleWorkloads)
	}
}

func TestPushOrUpdateGenerationChanged(t *testing.T) {
	now := time.Now()

	cases := map[string]struct {
		updatedWorkload           *kueue.Workload
		wantActiveWorkloads       int
		wantInadmissibleWorkloads int
	}{
		"moves to heap when generation changed": {
			updatedWorkload: utiltestingapi.MakeWorkload("workload-1", defaultNamespace).
				Creation(now).Generation(2).ResourceVersion("2").Priority(300).Obj(),
			wantActiveWorkloads:       1,
			wantInadmissibleWorkloads: 0,
		},
		"stays inadmissible when generation changed but backoff unexpired": {
			updatedWorkload: utiltestingapi.MakeWorkload("workload-1", defaultNamespace).
				Creation(now).Generation(2).ResourceVersion("2").Priority(300).
				RequeueState(new(int32(1)), new(metav1.NewTime(now.Add(time.Hour)))).
				Condition(metav1.Condition{
					Type:   kueue.WorkloadRequeued,
					Status: metav1.ConditionFalse,
					Reason: kueue.WorkloadEvictedByPodsReadyTimeout,
				}).Obj(),
			wantActiveWorkloads:       0,
			wantInadmissibleWorkloads: 1,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			cq := newClusterQueueImpl(ctx, nil, nil, defaultOrdering, testingclock.NewFakeClock(now))

			wl := utiltestingapi.MakeWorkload("workload-1", defaultNamespace).
				Creation(now).Generation(1).Obj()
			cq.PushOrUpdate(workload.NewInfo(wl))

			head := cq.Pop()
			if head == nil {
				t.Fatal("expected to pop workload")
			}

			// Simulate RequeueWorkload with info.Update: inadmissible entry gets new generation.
			updatedInfo := workload.NewInfo(tc.updatedWorkload)
			updatedInfo.LastEvaluatedGeneration = head.LastEvaluatedGeneration
			cq.requeueIfNotPresent(log, updatedInfo, false, RequeueReasonGeneric, "")

			// PushOrUpdate from informer event with the updated workload.
			cq.PushOrUpdate(workload.NewInfo(tc.updatedWorkload))

			if active, _ := cq.Dump(); len(active) != tc.wantActiveWorkloads {
				t.Errorf("got %d active workloads, want %d", len(active), tc.wantActiveWorkloads)
			}
			if inadmissible, _ := cq.DumpInadmissible(); len(inadmissible) != tc.wantInadmissibleWorkloads {
				t.Errorf("got %d inadmissible workloads, want %d", len(inadmissible), tc.wantInadmissibleWorkloads)
			}
		})
	}
}

func Test_Delete(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	cq := newClusterQueueImpl(ctx, nil, nil, defaultOrdering, testingclock.NewFakeClock(time.Now()))
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
	cq := newClusterQueueImpl(ctx, nil, nil, defaultOrdering, testingclock.NewFakeClock(time.Now()))
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
	cq := newClusterQueueImpl(ctx, nil, nil, defaultOrdering, testingclock.NewFakeClock(time.Now()))
	wl := utiltestingapi.MakeWorkload("workload-1", defaultNamespace).Obj()
	queue := &LocalQueue{
		items: map[workload.Reference]*workload.Info{
			workload.Reference(wl.Name): workload.NewInfo(wl),
		},
	}
	cq.PushOrUpdate(workload.NewInfo(wl))
	if added := cq.AddFromLocalQueue(queue, nil, nil); added {
		t.Error("expected workload not to be added")
	}
	cq.Delete(log, workload.Key(wl))
	if added := cq.AddFromLocalQueue(queue, nil, nil); !added {
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
					RequeueState(new(int32(1)), new(metav1.NewTime(backoffUntil))).
					Condition(metav1.Condition{Type: kueue.WorkloadRequeued, Status: metav1.ConditionFalse}).
					Obj(),
				utiltestingapi.MakeWorkload("wl4", defaultNamespace).Queue(lqName).Creation(now).UID(types.UID("uid-4")).
					RequeueState(new(int32(1)), new(metav1.NewTime(backoffUntil))).
					Condition(metav1.Condition{Type: kueue.WorkloadRequeued, Status: metav1.ConditionFalse}).
					Obj(),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			cq := newClusterQueueImpl(ctx, nil, nil, defaultOrdering, testingclock.NewFakeClock(now))

			for _, w := range tc.workloads {
				cq.PushOrUpdate(workload.NewInfo(w))
			}
			for _, w := range tc.inadmissibleWorkloads {
				cq.requeueIfNotPresent(log, workload.NewInfo(w), false, RequeueReasonGeneric, "")
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

func TestSnapshotFallsBackToBaseOrderingOnLocalQueueLookupError(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	ctx, _ := utiltesting.ContextWithLog(t)
	lqLookupErr := errors.New("temporary LocalQueue lookup error")
	cl := utiltesting.NewClientBuilder().
		WithObjects(
			utiltestingapi.MakeLocalQueue("higher-usage", defaultNamespace).Obj(),
			utiltestingapi.MakeLocalQueue("lower-usage", defaultNamespace).Obj(),
		).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if key.Name == "unavailable" {
					return lqLookupErr
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).
		Build()
	afsUsageLedger := queueafs.NewAfsUsageLedger()
	afsUsageLedger.SetForTest("default/higher-usage", corev1.ResourceList{resourceGPU: resource.MustParse("20")}, now)
	afsUsageLedger.SetForTest("default/lower-usage", corev1.ResourceList{resourceGPU: resource.MustParse("10")}, now)

	cq, err := newClusterQueue(
		ctx,
		cl,
		utiltestingapi.MakeClusterQueue("cq").AdmissionMode(kueue.UsageBasedAdmissionFairSharing).Obj(),
		nil,
		defaultOrdering,
		&config.AdmissionFairSharing{ResourceWeights: map[corev1.ResourceName]float64{resourceGPU: 1}},
		afsUsageLedger,
	)
	if err != nil {
		t.Fatalf("failed to create ClusterQueue: %v", err)
	}

	// Put the unavailable LocalQueue last so the usage cache is partially
	// populated before its lookup fails. The priorities reproduce the
	// contradictory ordering from Kueue#12534: fair sharing puts lower-usage
	// before higher-usage, while base ordering puts higher-usage before
	// unavailable and unavailable before lower-usage.
	elements := []*workload.Info{
		workload.NewInfo(utiltestingapi.MakeWorkload("higher-usage", defaultNamespace).
			Queue("higher-usage").Priority(3).Creation(now).UID("uid-2").Obj()),
		workload.NewInfo(utiltestingapi.MakeWorkload("lower-usage", defaultNamespace).
			Queue("lower-usage").Priority(1).Creation(now).UID("uid-1").Obj()),
		workload.NewInfo(utiltestingapi.MakeWorkload("unavailable", defaultNamespace).
			Queue("unavailable").Priority(2).Creation(now).UID("uid-3").Obj()),
	}

	cq.snapshotSort(elements)

	got := make([]string, len(elements))
	for i, wInfo := range elements {
		got[i] = wInfo.Obj.Name
	}
	want := []string{"higher-usage", "unavailable", "lower-usage"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("unexpected base ordering (-want,+got):\n%s", diff)
	}
}

func TestSnapshotStableWithConcurrentFSUpdates(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	builder := utiltesting.NewClientBuilder().WithObjects(
		utiltestingapi.MakeLocalQueue("lq1", defaultNamespace).
			FairSharing(&kueue.FairSharing{Weight: new(resource.MustParse("1"))}).Obj(),
		utiltestingapi.MakeLocalQueue("lq2", defaultNamespace).
			FairSharing(&kueue.FairSharing{Weight: new(resource.MustParse("1"))}).Obj(),
	)

	afsUsageLedger := queueafs.NewAfsUsageLedger()
	afsUsageLedger.SetForTest("default/lq1", corev1.ResourceList{resourceGPU: resource.MustParse("5")}, now)
	afsUsageLedger.SetForTest("default/lq2", corev1.ResourceList{resourceGPU: resource.MustParse("5")}, now)

	ctx, _ := utiltesting.ContextWithLog(t)
	cq, err := newClusterQueue(ctx, builder.Build(),
		utiltestingapi.MakeClusterQueue("cq").AdmissionMode(kueue.UsageBasedAdmissionFairSharing).Obj(),
		nil, defaultOrdering,
		&config.AdmissionFairSharing{ResourceWeights: map[corev1.ResourceName]float64{resourceGPU: 1.0}},
		afsUsageLedger)
	if err != nil {
		t.Fatalf("failed to create ClusterQueue: %v", err)
	}

	for _, w := range []*kueue.Workload{
		utiltestingapi.MakeWorkload("wl1", defaultNamespace).Queue("lq1").Creation(now).UID("uid-1").Obj(),
		utiltestingapi.MakeWorkload("wl2", defaultNamespace).Queue("lq2").Creation(now).UID("uid-2").Obj(),
		utiltestingapi.MakeWorkload("wl3", defaultNamespace).Queue("lq1").Creation(now).UID("uid-3").Obj(),
		utiltestingapi.MakeWorkload("wl4", defaultNamespace).Queue("lq2").Creation(now).UID("uid-4").Obj(),
	} {
		cq.PushOrUpdate(workload.NewInfo(w))
	}

	// Toggle lq1 penalty between 0 and 100 to create mid-sort inconsistency.
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				afsUsageLedger.PushPenalty("default/lq1", "default/toggle", corev1.ResourceList{resourceGPU: resource.MustParse("100")}, now)
				afsUsageLedger.SubPenalty("default/lq1", "default/toggle")
			}
		}
	}()
	defer close(stop)

	// Each snapshot must match one of two valid orderings:
	// equal usage (by UID) or lq1 penalized (lq2 first).
	validA := []string{"lq1", "lq2", "lq1", "lq2"}
	validB := []string{"lq2", "lq2", "lq1", "lq1"}
	for i := range 1000 {
		snap := cq.Snapshot()
		got := make([]string, len(snap))
		for j, wInfo := range snap {
			got[j] = string(wInfo.Obj.Spec.QueueName)
		}
		if !slices.Equal(got, validA) && !slices.Equal(got, validB) {
			t.Fatalf("call %d: invalid ordering %v (expected %v or %v)", i+1, got, validA, validB)
		}
	}
}

func TestSnapshotUsesDefaultWeightForMissingLocalQueue(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	ctx, _ := utiltesting.ContextWithLog(t)
	afsUsageLedger := queueafs.NewAfsUsageLedger()
	afsUsageLedger.SetForTest("default/existing", corev1.ResourceList{resourceGPU: resource.MustParse("10")}, now)
	afsUsageLedger.SetForTest("default/missing", corev1.ResourceList{resourceGPU: resource.MustParse("15")}, now)

	cq, err := newClusterQueue(
		ctx,
		utiltesting.NewClientBuilder().
			WithObjects(utiltestingapi.MakeLocalQueue("existing", defaultNamespace).Obj()).
			Build(),
		utiltestingapi.MakeClusterQueue("cq").AdmissionMode(kueue.UsageBasedAdmissionFairSharing).Obj(),
		nil,
		defaultOrdering,
		&config.AdmissionFairSharing{ResourceWeights: map[corev1.ResourceName]float64{resourceGPU: 1}},
		afsUsageLedger,
	)
	if err != nil {
		t.Fatalf("failed to create ClusterQueue: %v", err)
	}

	// Base ordering favors the missing queue by priority, while fair sharing with
	// the default weight favors the existing queue by usage (10 < 15).
	for _, wl := range []*kueue.Workload{
		utiltestingapi.MakeWorkload("existing-low-priority", defaultNamespace).
			Queue("existing").Priority(1).Creation(now).UID("uid-1").Obj(),
		utiltestingapi.MakeWorkload("missing-high-priority", defaultNamespace).
			Queue("missing").Priority(2).Creation(now).UID("uid-2").Obj(),
	} {
		cq.PushOrUpdate(workload.NewInfo(wl))
	}

	got := cq.Snapshot()
	if got[0].Obj.Name != "existing-low-priority" {
		t.Errorf("workload from missing LocalQueue should use the default weight: got %q first", got[0].Obj.Name)
	}
}

// TestHeapOrderingStableOnLocalQueueLookupError is a regression test for Kueue#13476.
// The two workloads have fair-sharing order opposite to their priority order: wlHigh is
// high priority in a high-usage queue, wlLow is low priority in a low-usage queue. With a
// client that fails every LocalQueue lookup, the old comparator fell back to priority
// ordering and popped wlHigh first. Now it reads the cached weight and stays on
// fair-sharing ordering, popping wlLow first, consistently across repeated comparisons.
func TestHeapOrderingStableOnLocalQueueLookupError(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	failingClient := utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*kueue.LocalQueue); ok {
				return errors.New("transient LocalQueue lookup error")
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}).Build()

	// lq1 high usage (10 GPU), lq2 low usage (1 GPU); both weight 1.0.
	afsUsageLedger := queueafs.NewAfsUsageLedger()
	afsUsageLedger.SetForTest("default/lq1", corev1.ResourceList{resourceGPU: resource.MustParse("10")}, now)
	afsUsageLedger.SetForTest("default/lq2", corev1.ResourceList{resourceGPU: resource.MustParse("1")}, now)

	ctx, _ := utiltesting.ContextWithLog(t)
	cq, err := newClusterQueue(ctx, failingClient,
		utiltestingapi.MakeClusterQueue("cq").AdmissionMode(kueue.UsageBasedAdmissionFairSharing).Obj(),
		nil, defaultOrdering,
		&config.AdmissionFairSharing{ResourceWeights: map[corev1.ResourceName]float64{resourceGPU: 1.0}},
		afsUsageLedger)
	if err != nil {
		t.Fatalf("failed to create ClusterQueue: %v", err)
	}

	// Seed the cached weights the way the manager's LocalQueue hooks do.
	cq.addLocalQueue("default/lq1", 1.0)
	cq.addLocalQueue("default/lq2", 1.0)

	wlHigh := utiltestingapi.MakeWorkload("wl-high", defaultNamespace).
		Queue("lq1").Priority(highPriority).Creation(now).UID("uid-high").Obj()
	wlLow := utiltestingapi.MakeWorkload("wl-low", defaultNamespace).
		Queue("lq2").Priority(lowPriority).Creation(now).UID("uid-low").Obj()

	cq.PushOrUpdate(workload.NewInfo(wlHigh))
	cq.PushOrUpdate(workload.NewInfo(wlLow))

	// wlLow (lower usage) must pop first despite the failing client.
	wantOrder := []workload.Reference{workload.Key(wlLow), workload.Key(wlHigh)}
	var gotOrder []workload.Reference
	for {
		head := cq.Pop()
		if head == nil {
			break
		}
		gotOrder = append(gotOrder, workload.Key(head.Obj))
	}
	if diff := cmp.Diff(wantOrder, gotOrder); diff != "" {
		t.Errorf("unexpected pop order with failing LocalQueue client (-want,+got):\n%s", diff)
	}

	// The comparator must stay consistent and antisymmetric across repeated calls.
	a := workload.NewInfo(wlHigh)
	b := workload.NewInfo(wlLow)
	first := cq.compareFunc(a, b)
	if first <= 0 {
		t.Fatalf("expected wlLow (lower usage) to sort before wlHigh, got compare(high,low)=%d", first)
	}
	for i := range 100 {
		if got := cq.compareFunc(a, b); got != first {
			t.Fatalf("comparator inconsistent on call %d: got %d, want %d", i+1, got, first)
		}
		if got := cq.compareFunc(b, a); got != -first {
			t.Fatalf("comparator not antisymmetric on call %d: compare(low,high)=%d, want %d", i+1, got, -first)
		}
	}
}

// TestSnapshotConcurrentWithRequeueNoDataRace guards against a data race on the
// preemptor workload: Snapshot sorts a copy of the pending workloads through the
// comparator (which reads preemptorWorkload.state) without holding the
// ClusterQueue lock, while RequeueIfNotPresent writes that field during a
// BestEffortFIFO preemption requeue. Run with -race to detect regressions.
func TestSnapshotConcurrentWithRequeueNoDataRace(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	cq, err := newClusterQueue(ctx, nil,
		&kueue.ClusterQueue{
			Spec: kueue.ClusterQueueSpec{
				QueueingStrategy: kueue.BestEffortFIFO,
			},
		}, nil,
		defaultOrdering,
		nil, nil)
	if err != nil {
		t.Fatalf("failed to create ClusterQueue: %v", err)
	}

	// At least two workloads so Snapshot's sort actually invokes the
	// comparator that reads the sticky workload.
	wls := []*kueue.Workload{
		utiltestingapi.MakeWorkload("wl1", defaultNamespace).Obj(),
		utiltestingapi.MakeWorkload("wl2", defaultNamespace).Obj(),
		utiltestingapi.MakeWorkload("wl3", defaultNamespace).Obj(),
	}
	for _, wl := range wls {
		cq.PushOrUpdate(workload.NewInfo(wl))
	}

	// Writer: continuously set the sticky workload via a preemption requeue.
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				for _, wl := range wls {
					cq.RequeueIfNotPresent(ctx, workload.NewInfo(wl), RequeueReasonPendingPreemption, "")
				}
			}
		}
	}()
	defer close(stop)

	// Reader: Snapshot reads the sticky workload through the comparator.
	for range 1000 {
		cq.Snapshot()
	}
}

// TestSnapshotConsistentUnderConcurrentStickyChange guards against Kueue#12740:
// Snapshot sorts a copy of the pending workloads without holding the
// ClusterQueue lock, so the comparator must observe a single, consistent sticky
// workload for the whole sort. If it re-read the sticky workload on every
// comparison, a concurrent change could make the ordering non-transitive and
// corrupt the Snapshot order. This is a logic bug, not a data race: the atomic
// pointer already makes each access memory-safe, so -race does not catch it.
//
// With equal priority and creation timestamp, every valid Snapshot is either the
// pure UID order or exactly one workload (the sticky one) pulled to the front
// followed by the rest in UID order. Any other permutation means the sort
// observed the sticky workload changing mid-sort.
func TestSnapshotConsistentUnderConcurrentStickyChange(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	cq, err := newClusterQueue(ctx, nil,
		&kueue.ClusterQueue{
			Spec: kueue.ClusterQueueSpec{
				QueueingStrategy: kueue.BestEffortFIFO,
			},
		}, nil,
		defaultOrdering,
		nil, nil)
	if err != nil {
		t.Fatalf("failed to create ClusterQueue: %v", err)
	}

	now := time.Now().Truncate(time.Second)
	// Names are listed in UID order; equal priority and creation timestamp make
	// UID the only tiebreak once stickiness is accounted for.
	names := []string{"wl1", "wl2", "wl3", "wl4", "wl5", "wl6"}
	keys := make([]workload.Reference, len(names))
	for i, name := range names {
		wl := utiltestingapi.MakeWorkload(name, defaultNamespace).
			Creation(now).UID(types.UID(fmt.Sprintf("uid-%d", i))).Obj()
		cq.PushOrUpdate(workload.NewInfo(wl))
		keys[i] = workload.Key(wl)
	}

	// Valid orderings: pure UID order, or any single workload pulled to the front.
	valid := [][]string{slices.Clone(names)}
	for i := range names {
		order := []string{names[i]}
		for j := range names {
			if j != i {
				order = append(order, names[j])
			}
		}
		valid = append(valid, order)
	}
	isValid := func(got []string) bool {
		for _, v := range valid {
			if slices.Equal(got, v) {
				return true
			}
		}
		return false
	}

	// Writer: continuously churn the sticky workload so it can change while a
	// Snapshot sort is in flight. It sets the field directly (bypassing the
	// lock) to force a change mid-sort, which the lock-free Snapshot must
	// tolerate; the fix makes it capture the value once per sort.
	stop := make(chan struct{})
	started := make(chan struct{})
	go func() {
		close(started)
		for {
			select {
			case <-stop:
				return
			default:
				for _, k := range keys {
					cq.pw.set(k, true, 0)
				}
				cq.pw.clear()
			}
		}
	}()
	// Block until the writer is scheduled, so the loop below cannot finish before
	// any churn has happened and pass vacuously.
	<-started
	defer close(stop)

	for i := range 2000 {
		snap := cq.Snapshot()
		got := make([]string, len(snap))
		for j, wInfo := range snap {
			got[j] = wInfo.Obj.Name
		}
		if !isValid(got) {
			t.Fatalf("call %d: non-transitive Snapshot ordering %v; sticky workload changed mid-sort", i+1, got)
		}
	}
}

func TestClusterQueueIsPreemptor(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	cq, err := newClusterQueue(ctx, nil, utiltestingapi.MakeClusterQueue("cq").Obj(), nil, defaultOrdering, nil, nil)
	if err != nil {
		t.Fatalf("failed to create ClusterQueue: %v", err)
	}

	wl := utiltestingapi.MakeWorkload("wl", defaultNamespace).Obj()
	wl.Generation = 1
	wInfo := workload.NewInfo(wl)
	wInfo.LastEvaluatedGeneration = 1

	otherWl := utiltestingapi.MakeWorkload("other", defaultNamespace).Obj()
	otherInfo := workload.NewInfo(otherWl)

	if cq.IsPreemptor(wInfo) {
		t.Errorf("IsPreemptor(wInfo) = true, want false before requeue")
	}

	cq.RequeueIfNotPresent(ctx, wInfo, RequeueReasonPendingPreemption, "")

	if !cq.IsPreemptor(wInfo) {
		t.Errorf("IsPreemptor(wInfo) = false, want true after RequeueReasonPendingPreemption")
	}
	if cq.IsPreemptor(otherInfo) {
		t.Errorf("IsPreemptor(otherInfo) = true, want false for non-preemptor workload")
	}

	wlModified := wl.DeepCopy()
	wlModified.Generation = 2
	wInfoModified := workload.NewInfo(wlModified)

	if cq.IsPreemptor(wInfoModified) {
		t.Errorf("IsPreemptor(wInfoModified) = true, want false after workload generation changed")
	}
}

func TestPendingResources(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	now := time.Now()
	cq := newClusterQueueImpl(ctx, nil, nil, defaultOrdering, testingclock.NewFakeClock(now))

	makePodSetWl := func(name string, cpu, memory string) *workload.Info {
		ps := utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			Request(corev1.ResourceCPU, cpu).
			Request(corev1.ResourceMemory, memory)
		return workload.NewInfo(utiltestingapi.MakeWorkload(name, defaultNamespace).
			PodSets(*ps.Obj()).
			Creation(now).Obj())
	}

	// Empty queue returns empty map.
	if got := cq.pendingResources(); len(got) != 0 {
		t.Errorf("expected empty PendingResources on empty queue, got %v", got)
	}

	wl1 := makePodSetWl("wl1", "2", "1Gi")   // heap
	wl2 := makePodSetWl("wl2", "1", "512Mi") // inadmissible
	wl3 := makePodSetWl("wl3", "3", "2Gi")   // will be popped (inflight)

	cq.PushOrUpdate(wl1)
	cq.PushOrUpdate(wl3)
	cq.requeueIfNotPresent(log, wl2, false, RequeueReasonGeneric, "")

	// Pop wl1 or wl3 to make it inflight (heap pops in creation order).
	inflight := cq.Pop()
	if inflight == nil {
		t.Fatal("expected to pop a workload")
	}

	got := cq.pendingResources()

	// All three workloads (heap + inadmissible + inflight) should be counted.
	if got[corev1.ResourceCPU] == 0 {
		t.Errorf("expected non-zero CPU in PendingResources, got %v", got)
	}
	if got[corev1.ResourceMemory] == 0 {
		t.Errorf("expected non-zero memory in PendingResources, got %v", got)
	}

	// Sum should equal wl1 + wl2 + wl3: CPU = 2+1+3 = 6000m, Memory = 1Gi+512Mi+2Gi.
	wantCPU := wl1.TotalRequests[0].Requests.ResourceValue(corev1.ResourceCPU) +
		wl2.TotalRequests[0].Requests.ResourceValue(corev1.ResourceCPU) +
		wl3.TotalRequests[0].Requests.ResourceValue(corev1.ResourceCPU)
	wantMemory := wl1.TotalRequests[0].Requests.ResourceValue(corev1.ResourceMemory) +
		wl2.TotalRequests[0].Requests.ResourceValue(corev1.ResourceMemory) +
		wl3.TotalRequests[0].Requests.ResourceValue(corev1.ResourceMemory)
	if got[corev1.ResourceCPU] != wantCPU {
		t.Errorf("CPU mismatch: want %d, got %d", wantCPU, got[corev1.ResourceCPU])
	}
	if got[corev1.ResourceMemory] != wantMemory {
		t.Errorf("memory mismatch: want %d, got %d", wantMemory, got[corev1.ResourceMemory])
	}
}

// TestPendingResourcesAfterLocalQueueResync verifies the invariant that a
// pending workload is tracked in exactly one bucket: an AddFromLocalQueue
// resync must not push a workload that is already tracked as inadmissible
// into the heap, and pendingResourcesTotal must count it exactly once
// through the follow-up transitions.
func TestPendingResourcesAfterLocalQueueResync(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	now := time.Now()

	tests := map[string]struct {
		beforeResync       func(t *testing.T, cq *ClusterQueue, wInfo *workload.Info)
		afterResync        func(cq *ClusterQueue, wInfo *workload.Info)
		wantInHeap         bool
		wantInInadmissible bool
		wantInInflight     bool
		wantPendingActive  int
		wantCPU            func(wInfo *workload.Info) int64
	}{
		"the workload stays tracked as inadmissible": {
			beforeResync: func(_ *testing.T, cq *ClusterQueue, wInfo *workload.Info) {
				cq.workloads.InsertInadmissible(workloadKey(wInfo), wInfo)
			},
			wantInInadmissible: true,
			wantCPU:            singleWorkloadCPU,
		},
		"requeuing all inadmissible workloads moves it to the heap": {
			beforeResync: func(_ *testing.T, cq *ClusterQueue, wInfo *workload.Info) {
				cq.workloads.InsertInadmissible(workloadKey(wInfo), wInfo)
			},
			afterResync: func(cq *ClusterQueue, _ *workload.Info) {
				cq.namespaceSelector = labels.Everything()
				queueInadmissibleWorkloads(ctx, cq, utiltesting.NewFakeClient(utiltesting.MakeNamespace(defaultNamespace)))
			},
			wantInHeap:        true,
			wantPendingActive: 1,
			wantCPU:           singleWorkloadCPU,
		},
		"deleting the workload removes it and its resources": {
			beforeResync: func(_ *testing.T, cq *ClusterQueue, wInfo *workload.Info) {
				cq.workloads.InsertInadmissible(workloadKey(wInfo), wInfo)
			},
			afterResync: func(cq *ClusterQueue, wInfo *workload.Info) {
				cq.Delete(log, workloadKey(wInfo))
			},
			wantCPU: func(*workload.Info) int64 { return 0 },
		},
		"the workload stays tracked as inflight": {
			beforeResync: func(t *testing.T, cq *ClusterQueue, wInfo *workload.Info) {
				cq.PushOrUpdate(wInfo)
				if popped := cq.Pop(); popped == nil {
					t.Fatal("expected to pop a workload")
				}
			},
			wantInInflight:    true,
			wantPendingActive: 1,
			wantCPU:           singleWorkloadCPU,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cq := newClusterQueueImpl(ctx, nil, nil, defaultOrdering, testingclock.NewFakeClock(now))
			ps := utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
				Request(corev1.ResourceCPU, "1")
			wInfo := workload.NewInfo(utiltestingapi.MakeWorkload("workload", defaultNamespace).
				PodSets(*ps.Obj()).
				Creation(now).Obj())
			key := workloadKey(wInfo)

			tc.beforeResync(t, cq, wInfo)
			lq := &LocalQueue{items: map[workload.Reference]*workload.Info{
				key: wInfo,
			}}
			if added := cq.AddFromLocalQueue(lq, nil, nil); added {
				t.Error("AddFromLocalQueue() = true, want false; the workload is already tracked")
			}

			if tc.afterResync != nil {
				tc.afterResync(cq, wInfo)
			}

			inHeap := cq.workloads.active.GetByKey(key) != nil
			inInadmissible := cq.workloads.inadmissible.hasKey(key)
			_, inInflight := cq.workloads.inflight[key]
			if inHeap != tc.wantInHeap {
				t.Errorf("in heap = %v, want %v", inHeap, tc.wantInHeap)
			}
			if inInadmissible != tc.wantInInadmissible {
				t.Errorf("in inadmissibleWorkloads = %v, want %v", inInadmissible, tc.wantInInadmissible)
			}
			if inInflight != tc.wantInInflight {
				t.Errorf("in inflight = %v, want %v", inInflight, tc.wantInInflight)
			}
			if got := cq.workloads.pendingActive(); got.Total() != tc.wantPendingActive {
				t.Errorf("pending active workloads = %d, want %d", got.Total(), tc.wantPendingActive)
			}
			if gotCPU, wantCPU := cq.pendingResources()[corev1.ResourceCPU], tc.wantCPU(wInfo); gotCPU != wantCPU {
				t.Errorf("pending CPU = %d, want %d", gotCPU, wantCPU)
			}
		})
	}
}

func singleWorkloadCPU(wInfo *workload.Info) int64 {
	return wInfo.TotalRequests[0].Requests.ResourceValue(corev1.ResourceCPU)
}

func TestPendingInLocalQueueCountsInflight(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	now := time.Now()
	cq := newClusterQueueImpl(ctx, nil, nil, defaultOrdering, testingclock.NewFakeClock(now))

	inflightWl := utiltestingapi.MakeWorkload("wl-inflight", defaultNamespace).
		Queue("lq-a").
		Creation(now).
		Obj()
	otherWl := utiltestingapi.MakeWorkload("wl-other", defaultNamespace).
		Queue("lq-b").
		Creation(now.Add(time.Second)).
		Obj()

	cq.PushOrUpdate(workload.NewInfo(inflightWl))
	cq.PushOrUpdate(workload.NewInfo(otherWl))

	popped := cq.Pop()
	if popped == nil {
		t.Fatal("expected to pop a workload")
	}

	lqA := utilqueue.NewLocalQueueReference(defaultNamespace, kueue.LocalQueueName("lq-a"))
	activeA, inadmissibleA := cq.PendingInLocalQueue(lqA)
	if activeA != 1 || inadmissibleA != 0 {
		t.Fatalf("LocalQueue lq-a pending mismatch: active=%d inadmissible=%d, want active=1 inadmissible=0", activeA, inadmissibleA)
	}

	lqB := utilqueue.NewLocalQueueReference(defaultNamespace, kueue.LocalQueueName("lq-b"))
	activeB, inadmissibleB := cq.PendingInLocalQueue(lqB)
	if activeB != 1 || inadmissibleB != 0 {
		t.Fatalf("LocalQueue lq-b pending mismatch: active=%d inadmissible=%d, want active=1 inadmissible=0", activeB, inadmissibleB)
	}
}

func Test_DeleteFromLocalQueue(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	cq := newClusterQueueImpl(ctx, nil, nil, defaultOrdering, testingclock.NewFakeClock(time.Now()))
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
		cq.requeueIfNotPresent(log, wInfo, false, RequeueReasonGeneric, "")
		qImpl.AddOrUpdate(wInfo)
	}

	wantPending := len(admissibleworkloads) + len(inadmissibleWorkloads)
	if cq.PendingTotal() != wantPending {
		t.Errorf("clusterQueue's workload number not right, want %v, got %v", wantPending, cq.PendingTotal())
	}
	if cq.workloads.inadmissible.len() != len(inadmissibleWorkloads) {
		t.Errorf("clusterQueue's workload number in inadmissibleWorkloads not right, want %v, got %v", len(inadmissibleWorkloads), cq.workloads.inadmissible.len())
	}

	cq.DeleteFromLocalQueue(log, qImpl, nil, nil)
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
			RequeueState(new(int32(1)), new(metav1.NewTime(minuteLater))).
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
		wantInadmissibleWorkloadsRequeued int
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
			wantInadmissibleWorkloadsRequeued: 1,
		},
		"re-queue multiple inadmissible workloads and count": {
			inadmissibleWorkloadsToRequeue:    []*workload.Info{workload.NewInfo(workloads[0]), workload.NewInfo(workloads[1])},
			queueInadmissibleWorkloads:        true,
			wantActiveWorkloads:               []workload.Reference{workload.Key(workloads[0]), workload.Key(workloads[1])},
			wantPending:                       2,
			wantInadmissibleWorkloadsRequeued: 2,
		},
		// workloads[1] (ns2/sales) matches the namespace selector and moves
		// to the heap, but workloads[2] (ns3/marketing) does not match and
		// stays inadmissible. Verify the count reflects only the one that moved.
		"count only workloads that actually moved": {
			workloadsToAdd:                    []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToRequeue:    []*workload.Info{workload.NewInfo(workloads[1]), workload.NewInfo(workloads[2])},
			queueInadmissibleWorkloads:        true,
			wantActiveWorkloads:               []workload.Reference{workload.Key(workloads[0]), workload.Key(workloads[1])},
			wantPending:                       3,
			wantInadmissibleWorkloadsRequeued: 1,
		},
		// workloads[1] is already on the heap via PushOrUpdate, so
		// requeueIfNotPresent skips adding it to inadmissible. The flush
		// finds an empty inadmissible map and returns 0.
		"workload already on heap is not made inadmissible": {
			workloadsToAdd:                    []*kueue.Workload{workloads[1]},
			inadmissibleWorkloadsToRequeue:    []*workload.Info{workload.NewInfo(workloads[1])},
			queueInadmissibleWorkloads:        true,
			wantActiveWorkloads:               []workload.Reference{workload.Key(workloads[1])},
			wantPending:                       1,
			wantInadmissibleWorkloadsRequeued: 0,
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
			cq := newClusterQueueImpl(ctx, nil, nil, defaultOrdering, fakeClock)
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
				cq.requeueIfNotPresent(log, w, false, RequeueReasonGeneric, "")
			}
			for _, w := range test.admissibleWorkloadsToRequeue {
				cq.requeueIfNotPresent(log, w, true, RequeueReasonGeneric, "")
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
	cq := newClusterQueueImpl(ctx, nil, nil, defaultOrdering, testingclock.NewFakeClock(time.Now()))
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
	cq.requeueIfNotPresent(log, head, false, RequeueReasonGeneric, "")

	activeWorkloads, _ = cq.Dump()
	wantActiveWorkloads = []workload.Reference{workload.Key(wl)}
	if diff := cmp.Diff(wantActiveWorkloads, activeWorkloads, cmpDump...); diff != "" {
		t.Errorf("Unexpected active workloads after scheduling with requeuing (-want,+got):\n%s", diff)
	}

	// Simulating scheduling again without requeuing.
	head = cq.Pop()
	cq.requeueIfNotPresent(log, head, false, RequeueReasonGeneric, "")
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
				RequeueState(new(int32(10)), nil).Obj()),
			want: true,
		},
		"now already has exceeded requeueAt": {
			workloadInfo: workload.NewInfo(utiltestingapi.MakeWorkload("wl", "ns").
				RequeueState(new(int32(10)), new(metav1.NewTime(minuteAgo))).
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Status: metav1.ConditionTrue,
					Reason: kueue.WorkloadEvictedByPodsReadyTimeout,
				}).Obj()),
			want: true,
		},
		"now hasn't yet exceeded requeueAt": {
			workloadInfo: workload.NewInfo(utiltestingapi.MakeWorkload("wl", "ns").
				RequeueState(new(int32(10)), new(metav1.NewTime(minuteLater))).
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
			cq := newClusterQueueImpl(ctx, nil, nil, defaultOrdering, fakeClock)
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
		wantSticky       bool
	}{
		"failure after nomination": {
			reason:           RequeueReasonFailedAfterNomination,
			wantInadmissible: false,
		},
		"pending preemption": {
			reason:           RequeueReasonPendingPreemption,
			wantInadmissible: false,
			wantSticky:       true,
		},
		"preemption failed": {
			reason:           RequeueReasonPreemptionFailed,
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
		"nofit": {
			reason:           RequeueReasonNoFit,
			wantInadmissible: true,
		},
		"preempt no candidates": {
			reason:           RequeueReasonPreemptionNoCandidates,
			wantInadmissible: true,
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
				}, nil,
				workload.Ordering{PodsReadyRequeuingTimestamp: config.EvictionTimestamp},
				nil, nil)
			wl := utiltestingapi.MakeWorkload("workload-1", defaultNamespace).Obj()
			info := workload.NewInfo(wl)
			info.LastAssignment = tc.lastAssignment
			if ok := cq.RequeueIfNotPresent(ctx, info, tc.reason, ""); !ok {
				t.Error("failed to requeue nonexistent workload")
			}

			gotInadmissible := cq.workloads.inadmissible.hasKey(workload.Key(wl))
			if diff := cmp.Diff(tc.wantInadmissible, gotInadmissible); diff != "" {
				t.Errorf("Unexpected inadmissible status (-want,+got):\n%s", diff)
			}

			gotSticky := cq.pw.stickyMatches(workload.Key(wl))
			if diff := cmp.Diff(tc.wantSticky, gotSticky); diff != "" {
				t.Errorf("Unexpected sticky status (-want,+got):\n%s", diff)
			}

			if ok := cq.RequeueIfNotPresent(ctx, workload.NewInfo(wl), tc.reason, ""); ok {
				t.Error("Re-queued a workload that was already present")
			}
		})
	}
}

func TestBestEffortFIFOFailedPreemptionNotSticky(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	cq, err := newClusterQueue(ctx, nil,
		&kueue.ClusterQueue{
			Spec: kueue.ClusterQueueSpec{
				QueueingStrategy: kueue.BestEffortFIFO,
			},
		}, nil,
		workload.Ordering{PodsReadyRequeuingTimestamp: config.EvictionTimestamp},
		nil, nil)
	if err != nil {
		t.Fatalf("Failed to create ClusterQueue: %v", err)
	}

	lowPriorityWl := utiltestingapi.MakeWorkload("low-wl", defaultNamespace).
		Priority(0).
		Obj()
	highPriorityWl := utiltestingapi.MakeWorkload("high-wl", defaultNamespace).
		Priority(10).
		Obj()

	// When lowPriorityWl is requeued with PendingPreemption, it becomes sticky at the head
	// even if a higher priority workload is pushed.
	cq.PushOrUpdate(workload.NewInfo(lowPriorityWl))
	popped := cq.Pop()
	if popped == nil || popped.Obj.Name != "low-wl" {
		t.Fatalf("Expected low-wl to be popped first, got %v", popped)
	}
	cq.RequeueIfNotPresent(ctx, popped, RequeueReasonPendingPreemption, "")
	cq.PushOrUpdate(workload.NewInfo(highPriorityWl))

	// Because low-wl is sticky, it pops before high-wl despite lower priority.
	poppedLow := cq.Pop()
	if poppedLow == nil || poppedLow.Obj.Name != "low-wl" {
		t.Errorf("Expected sticky low-wl to pop before high-wl, got %v", poppedLow)
	}
	poppedHigh := cq.Pop()
	if poppedHigh == nil || poppedHigh.Obj.Name != "high-wl" {
		t.Errorf("Expected high-wl to pop second, got %v", poppedHigh)
	}

	// When lowPriorityWl is requeued with PreemptionFailed, it does NOT become sticky,
	// and any previous sticky state is cleared.
	// Therefore, highPriorityWl pops before lowPriorityWl according to priority order.
	cq.RequeueIfNotPresent(ctx, poppedLow, RequeueReasonPreemptionFailed, "")
	cq.RequeueIfNotPresent(ctx, poppedHigh, RequeueReasonFailedAfterNomination, "")

	if got := cq.Pop(); got == nil || got.Obj.Name != "high-wl" {
		t.Errorf("Expected non-sticky high-wl to pop before low-wl with failed preemption, got %v", got)
	}
	if got := cq.Pop(); got == nil || got.Obj.Name != "low-wl" {
		t.Errorf("Expected low-wl to pop second, got %v", got)
	}
}

func TestFIFOClusterQueue(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	q, err := newClusterQueue(ctx, nil,
		&kueue.ClusterQueue{
			Spec: kueue.ClusterQueueSpec{
				QueueingStrategy: kueue.StrictFIFO,
			},
		}, nil,
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
				}, nil,
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
			ctx, _ := utiltesting.ContextWithLog(t)
			cq, _ := newClusterQueue(ctx, nil,
				&kueue.ClusterQueue{
					Spec: kueue.ClusterQueueSpec{
						QueueingStrategy: kueue.StrictFIFO,
					},
				}, nil,
				workload.Ordering{PodsReadyRequeuingTimestamp: config.EvictionTimestamp},
				nil, nil)
			wl := utiltestingapi.MakeWorkload("workload-1", defaultNamespace).Obj()
			if ok := cq.RequeueIfNotPresent(ctx, workload.NewInfo(wl), reason, ""); !ok {
				t.Error("failed to requeue nonexistent workload")
			}

			gotInadmissible := cq.workloads.inadmissible.hasKey(workload.Key(wl))
			if test.wantInadmissible != gotInadmissible {
				t.Errorf("Got inadmissible after requeue %t, want %t", gotInadmissible, test.wantInadmissible)
			}

			if ok := cq.RequeueIfNotPresent(ctx, workload.NewInfo(wl), reason, ""); ok {
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
						Weight: new(resource.MustParse("1")),
					}).
					Obj(),
				*utiltestingapi.MakeLocalQueue("lqB", "default").
					FairSharing(&kueue.FairSharing{
						Weight: new(resource.MustParse("1")),
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
						Weight: new(resource.MustParse("1")),
					}).
					Obj(),
				*utiltestingapi.MakeLocalQueue("lqB", "default").
					FairSharing(&kueue.FairSharing{
						Weight: new(resource.MustParse("1")),
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
						Weight: new(resource.MustParse("1")),
					}).
					Obj(),
				*utiltestingapi.MakeLocalQueue("lqB", "default").
					FairSharing(&kueue.FairSharing{
						Weight: new(resource.MustParse("2")),
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
						Weight: new(resource.MustParse("1")),
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
			builder := utiltesting.NewClientBuilder()
			for _, lq := range tc.lqs {
				builder = builder.WithObjects(&lq)
			}
			client := builder.Build()

			afsUsageLedger := queueafs.NewAfsUsageLedger()
			for lqKey, consumedResources := range tc.initConsumedResources {
				afsUsageLedger.SetForTest(utilqueue.LocalQueueReference(lqKey), consumedResources, time.Now())
			}

			cq, _ := newClusterQueue(t.Context(), client, tc.cq, nil, defaultOrdering, tc.afsConfig, afsUsageLedger)
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

func TestRecordInadmissibleHash(t *testing.T) {
	cases := map[string]struct {
		hashToRecord     workload.EquivalenceHash
		heapWorkloads    map[string]workload.EquivalenceHash // name -> schedulingHash
		wantMoved        int
		wantActive       int
		wantInadmissible int
	}{
		"bulk-moves matching workloads": {
			hashToRecord: "gpu-class",
			heapWorkloads: map[string]workload.EquivalenceHash{
				"gpu-1": "gpu-class",
				"gpu-2": "gpu-class",
				"gpu-3": "gpu-class",
				"cpu-1": "cpu-class",
				"cpu-2": "cpu-class",
			},
			wantMoved:        3,
			wantActive:       2,
			wantInadmissible: 3,
		},
		"no-op for empty hash": {
			hashToRecord: "",
			heapWorkloads: map[string]workload.EquivalenceHash{
				"wl-1": "some-hash",
			},
			wantMoved:        0,
			wantActive:       1,
			wantInadmissible: 0,
		},
		"no-op when no workloads match": {
			hashToRecord: "nonexistent",
			heapWorkloads: map[string]workload.EquivalenceHash{
				"wl-1": "hash-a",
				"wl-2": "hash-b",
			},
			wantMoved:        0,
			wantActive:       2,
			wantInadmissible: 0,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			now := time.Now()
			cq := newClusterQueueImpl(ctx, nil, nil, defaultOrdering, testingclock.NewFakeClock(now))
			cq.queueingStrategy = kueue.BestEffortFIFO

			i := 0
			for wlName, hash := range tc.heapWorkloads {
				wl := utiltestingapi.MakeWorkload(wlName, defaultNamespace).
					Creation(now.Add(time.Duration(i)*time.Second)).
					Request(corev1.ResourceCPU, "1").Obj()
				info := workload.NewInfo(wl)
				info.SchedulingHash = hash
				cq.PushOrUpdate(info)
				i++
			}

			moved := cq.handleInadmissibleHash(tc.hashToRecord, "dummy-reason")
			if moved != tc.wantMoved {
				t.Errorf("handleInadmissibleHash moved %d, want %d", moved, tc.wantMoved)
			}

			active, inadmissible := cq.Pending()
			if active != tc.wantActive {
				t.Errorf("active workloads = %d, want %d", active, tc.wantActive)
			}
			if inadmissible != tc.wantInadmissible {
				t.Errorf("inadmissible workloads = %d, want %d", inadmissible, tc.wantInadmissible)
			}
		})
	}
}

func TestPushOrUpdateRespectsInadmissibleHashes(t *testing.T) {
	cases := map[string]struct {
		inadmissibleHashes []workload.EquivalenceHash
		pushHash           workload.EquivalenceHash
		wantActive         int
		wantInadmissible   int
	}{
		"workload with blocked hash goes to inadmissible": {
			inadmissibleHashes: []workload.EquivalenceHash{"blocked"},
			pushHash:           "blocked",
			wantActive:         0,
			wantInadmissible:   1,
		},
		"workload with non-blocked hash goes to heap": {
			inadmissibleHashes: []workload.EquivalenceHash{"blocked"},
			pushHash:           "allowed",
			wantActive:         1,
			wantInadmissible:   0,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			cq := newClusterQueueImpl(ctx, nil, nil, defaultOrdering, testingclock.NewFakeClock(time.Now()))
			cq.queueingStrategy = kueue.BestEffortFIFO

			for _, h := range tc.inadmissibleHashes {
				cq.hashToBulkMoveReason[h] = "dummy-reason"
			}

			wl := utiltestingapi.MakeWorkload("wl", defaultNamespace).
				Request(corev1.ResourceCPU, "1").Obj()
			info := workload.NewInfo(wl)
			info.SchedulingHash = tc.pushHash
			cq.PushOrUpdate(info)

			active, inadmissible := cq.Pending()
			if active != tc.wantActive {
				t.Errorf("active = %d, want %d", active, tc.wantActive)
			}
			if inadmissible != tc.wantInadmissible {
				t.Errorf("inadmissible = %d, want %d", inadmissible, tc.wantInadmissible)
			}
		})
	}
}

func TestQueueInadmissibleWorkloadsClearsHashes(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.SchedulingEquivalenceHashing, true)

	ctx, _ := utiltesting.ContextWithLog(t)
	cq := newClusterQueueImpl(ctx, nil, nil, defaultOrdering, testingclock.NewFakeClock(time.Now()))
	cq.queueingStrategy = kueue.BestEffortFIFO
	cq.namespaceSelector = labels.Everything()

	wl := utiltestingapi.MakeWorkload("wl", defaultNamespace).
		Request(corev1.ResourceCPU, "1").Obj()
	info := workload.NewInfo(wl)
	info.SchedulingHash = "test-hash"
	cq.PushOrUpdate(info)
	cq.handleInadmissibleHash("test-hash", "dummy-reason")

	if _, has := cq.hashToBulkMoveReason["test-hash"]; !has {
		t.Fatal("hash should be recorded before clearing")
	}
	activeHashes, inadmissibleHashes := cq.PendingSchedulingHashes()
	if activeHashes != 0 || inadmissibleHashes != 1 {
		t.Fatalf("before requeue: activeHashes=%d inadmissibleHashes=%d, want activeHashes=0 inadmissibleHashes=1", activeHashes, inadmissibleHashes)
	}

	queueInadmissibleWorkloads(ctx, cq, utiltesting.NewFakeClient(
		wl, utiltesting.MakeNamespace(defaultNamespace),
	))

	if _, has := cq.hashToBulkMoveReason["test-hash"]; has {
		t.Error("hashToBulkMoveReason should be cleared after queueInadmissibleWorkloads")
	}

	active, inadmissible := cq.Pending()
	if active != 1 || inadmissible != 0 {
		t.Errorf("after requeue: active=%d inadmissible=%d, want active=1 inadmissible=0", active, inadmissible)
	}
	activeHashes, inadmissibleHashes = cq.PendingSchedulingHashes()
	if activeHashes != 1 || inadmissibleHashes != 0 {
		t.Errorf("after requeue: activeHashes=%d inadmissibleHashes=%d, want activeHashes=1 inadmissibleHashes=0", activeHashes, inadmissibleHashes)
	}
}

func TestRequeueHashTriggerByReason(t *testing.T) {
	tests := map[string]struct {
		reason   RequeueReason
		wantHash bool
	}{
		"nofit triggers hash": {
			reason:   RequeueReasonNoFit,
			wantHash: true,
		},
		"preempt no candidates triggers hash": {
			reason:   RequeueReasonPreemptionNoCandidates,
			wantHash: true,
		},
		"namespace mismatch does not trigger hash": {
			reason:   RequeueReasonNamespaceMismatch,
			wantHash: false,
		},
		"preemption gated does not trigger hash": {
			reason:   RequeueReasonPreemptionGated,
			wantHash: false,
		},
		"generic does not trigger hash": {
			reason:   RequeueReasonGeneric,
			wantHash: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.SchedulingEquivalenceHashing, true)
			ctx, _ := utiltesting.ContextWithLog(t)
			cq, _ := newClusterQueue(ctx, nil,
				&kueue.ClusterQueue{
					Spec: kueue.ClusterQueueSpec{
						QueueingStrategy: kueue.BestEffortFIFO,
					},
				}, nil,
				workload.Ordering{PodsReadyRequeuingTimestamp: config.EvictionTimestamp},
				nil, nil)

			wl := utiltestingapi.MakeWorkload("workload-1", defaultNamespace).
				Request(corev1.ResourceCPU, "1").Obj()
			info := workload.NewInfo(wl)
			info.SchedulingHash = "test-hash-abc"
			cq.RequeueIfNotPresent(ctx, info, tc.reason, "WaitingForQuota")

			_, gotHash := cq.hashToBulkMoveReason["test-hash-abc"]
			if gotHash != tc.wantHash {
				t.Errorf("hashToBulkMoveReason.Has(hash) = %v, want %v", gotHash, tc.wantHash)
			}
		})
	}
}

func TestGetNoFitReason(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.SchedulingEquivalenceHashing, true)
	cases := map[string]struct {
		conditionReason        string
		requeueReason          RequeueReason
		deleteFromInadmissible bool
		wantReason             string
		wantOk                 bool
	}{
		"records status reason when requeued as NoFit": {
			conditionReason: kueue.WorkloadQuotaReservedReasonExceedsMaxQuota,
			requeueReason:   RequeueReasonNoFit,
			wantReason:      kueue.WorkloadQuotaReservedReasonExceedsMaxQuota,
			wantOk:          true,
		},
		"records status reason when requeued as PreemptionNoCandidates": {
			conditionReason: kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
			requeueReason:   RequeueReasonPreemptionNoCandidates,
			wantReason:      kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
			wantOk:          true,
		},
		"falls back to PendingEvaluation if no status condition is present": {
			conditionReason: "",
			requeueReason:   RequeueReasonNoFit,
			wantReason:      kueue.WorkloadQuotaReservedReasonPendingEvaluation,
			wantOk:          true,
		},
		"returns false if workload is not in inadmissibleWorkloads": {
			conditionReason:        kueue.WorkloadQuotaReservedReasonExceedsMaxQuota,
			requeueReason:          RequeueReasonNoFit,
			deleteFromInadmissible: true,
			wantOk:                 false,
		},
		"returns false if requeued with non-capacity reason": {
			conditionReason: kueue.WorkloadQuotaReservedReasonExceedsMaxQuota,
			requeueReason:   RequeueReasonNamespaceMismatch,
			wantOk:          false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			cq := newClusterQueueImpl(ctx, nil, nil, defaultOrdering, testingclock.NewFakeClock(time.Now()))
			cq.queueingStrategy = kueue.BestEffortFIFO

			wlBuilder := utiltestingapi.MakeWorkload("wl", defaultNamespace)
			if tc.conditionReason != "" {
				wlBuilder.Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  tc.conditionReason,
					Message: "failed",
				})
			}
			wl := wlBuilder.Obj()
			info := workload.NewInfo(wl)
			info.SchedulingHash = "test-hash"

			cq.RequeueIfNotPresent(ctx, info, tc.requeueReason, QuotaReservedReason(tc.conditionReason))

			wlKey := workload.Key(wl)
			if tc.deleteFromInadmissible {
				cq.rwm.Lock()
				cq.workloads.inadmissible.delete(wlKey)
				cq.rwm.Unlock()
			}

			reason, ok := cq.GetNoFitReason(wlKey)
			if ok != tc.wantOk {
				t.Errorf("GetNoFitReason() ok = %v, want %v", ok, tc.wantOk)
			}
			if ok && reason != tc.wantReason {
				t.Errorf("GetNoFitReason() reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

func TestClusterQueuePendingTrackers(t *testing.T) {
	cqLabel := config.ControllerMetricsCustomLabel{
		Name:           "cq-team",
		SourceLabelKey: "team",
		SourceKind:     new(config.SourceKindClusterQueue),
	}
	wlLabel1 := config.ControllerMetricsCustomLabel{
		Name:           "workload-project",
		SourceLabelKey: "project",
		SourceKind:     new(config.SourceKindWorkload),
		TrackedValues:  []string{"project-a", "project-b"},
	}
	wlLabel2 := config.ControllerMetricsCustomLabel{
		Name:           "workload-type",
		SourceLabelKey: "type",
		SourceKind:     new(config.SourceKindWorkload),
		TrackedValues:  []string{"type-a", "type-b"},
	}

	emptyVals := [6]string{}
	labelVals1 := [6]string{"project-a", "type-a"}
	labelVals2 := [6]string{"project-b", "type-b"}
	labelValsUntracked := [6]string{config.UntrackedCustomLabelValue, config.UntrackedCustomLabelValue}

	makeWorkload := func(name string, labels map[string]string) *workload.Info {
		wl := utiltestingapi.MakeWorkload(name, defaultNamespace).
			Labels(labels).
			Obj()
		return workload.NewInfo(wl)
	}

	cases := map[string]struct {
		labels           *[]config.ControllerMetricsCustomLabel
		ops              func(context.Context, logr.Logger, *ClusterQueue)
		wantPending      map[[6]string]int
		wantInadmissible map[[6]string]int
	}{
		"no custom labels defined": {
			labels: &[]config.ControllerMetricsCustomLabel{},
			ops: func(ctx context.Context, log logr.Logger, cq *ClusterQueue) {
				wl1 := makeWorkload("wl1", map[string]string{"project": "project-a"})
				wl2 := makeWorkload("wl2", map[string]string{"project": "project-b"})
				cq.PushOrUpdate(wl1)
				cq.PushOrUpdate(wl2)
				popped := cq.Pop()
				cq.RequeueIfNotPresent(ctx, popped, RequeueReasonNoFit, QuotaReservedReason(""))
			},
			wantPending:      map[[6]string]int{emptyVals: 1},
			wantInadmissible: map[[6]string]int{emptyVals: 1},
		},
		"only ClusterQueue custom label defined": {
			labels: &[]config.ControllerMetricsCustomLabel{cqLabel},
			ops: func(ctx context.Context, log logr.Logger, cq *ClusterQueue) {
				wl1 := makeWorkload("wl1", map[string]string{"project": "project-a"})
				wl2 := makeWorkload("wl2", map[string]string{"project": "project-b"})
				cq.PushOrUpdate(wl1)
				cq.PushOrUpdate(wl2)
				popped := cq.Pop()
				cq.RequeueIfNotPresent(ctx, popped, RequeueReasonNoFit, QuotaReservedReason(""))
			},
			wantPending:      map[[6]string]int{emptyVals: 1},
			wantInadmissible: map[[6]string]int{emptyVals: 1},
		},
		"Workload custom label defined: Push operation": {
			labels: &[]config.ControllerMetricsCustomLabel{wlLabel1, wlLabel2},
			ops: func(ctx context.Context, log logr.Logger, cq *ClusterQueue) {
				wl1 := makeWorkload("wl1", map[string]string{"project": "project-a", "type": "type-a"})
				wl2 := makeWorkload("wl2", map[string]string{"project": "project-a", "type": "type-a"})
				wl3 := makeWorkload("wl3", map[string]string{"project": "project-b", "type": "type-b"})
				cq.PushOrUpdate(wl1)
				cq.PushOrUpdate(wl2)
				cq.PushOrUpdate(wl3)
			},
			wantPending:      map[[6]string]int{labelVals1: 2, labelVals2: 1},
			wantInadmissible: map[[6]string]int{},
		},
		"Workload custom label defined: Pop operation": {
			labels: &[]config.ControllerMetricsCustomLabel{wlLabel1, wlLabel2},
			ops: func(ctx context.Context, log logr.Logger, cq *ClusterQueue) {
				wl1 := makeWorkload("wl1", map[string]string{"project": "project-a", "type": "type-a"})
				wl2 := makeWorkload("wl2", map[string]string{"project": "project-b", "type": "type-b"})
				wl3 := makeWorkload("wl3", map[string]string{"project": "project-b", "type": "type-b"})
				cq.PushOrUpdate(wl1)
				cq.PushOrUpdate(wl2)
				cq.PushOrUpdate(wl3)
				cq.Pop()
				cq.Pop()
			},
			wantPending: map[[6]string]int{
				// Both popped workloads stay inflight: inflight is a map, so a
				// second Pop no longer evicts the first one from the count.
				labelVals1: 1, // wl1, inflight
				labelVals2: 2, // wl3 on heap + wl2 inflight
			},
			wantInadmissible: map[[6]string]int{},
		},
		"Workload custom label defined: ClearInflight and Delete": {
			labels: &[]config.ControllerMetricsCustomLabel{wlLabel1, wlLabel2},
			ops: func(ctx context.Context, log logr.Logger, cq *ClusterQueue) {
				wl1 := makeWorkload("wl1", map[string]string{"project": "project-a", "type": "type-a"})
				wl2 := makeWorkload("wl2", map[string]string{"project": "project-b", "type": "type-b"})
				cq.PushOrUpdate(wl1)
				cq.PushOrUpdate(wl2)
				popped := cq.Pop()
				cq.Delete(log, workload.Key(popped.Obj))
				cq.Delete(log, workload.Key(wl2.Obj))
			},
			wantPending:      map[[6]string]int{labelVals1: 0, labelVals2: 0},
			wantInadmissible: map[[6]string]int{},
		},
		"Workload custom label defined: Requeue to inadmissible and QueueInadmissibleWorkloads": {
			labels: &[]config.ControllerMetricsCustomLabel{wlLabel1, wlLabel2},
			ops: func(ctx context.Context, log logr.Logger, cq *ClusterQueue) {
				wl1 := makeWorkload("wl1", map[string]string{"project": "project-a", "type": "type-a"})
				wl2 := makeWorkload("wl2", map[string]string{"project": "project-b", "type": "type-b"})
				cq.PushOrUpdate(wl1)
				cq.PushOrUpdate(wl2)
				popped1 := cq.Pop()
				cq.RequeueIfNotPresent(ctx, popped1, RequeueReasonNoFit, QuotaReservedReason(""))
				popped2 := cq.Pop()
				cq.RequeueIfNotPresent(ctx, popped2, RequeueReasonNoFit, QuotaReservedReason(""))
				queueInadmissibleWorkloads(ctx, cq, utiltesting.NewFakeClient(
					wl1.Obj, wl2.Obj, utiltesting.MakeNamespace(defaultNamespace),
				))
			},
			wantPending:      map[[6]string]int{labelVals1: 1, labelVals2: 1},
			wantInadmissible: map[[6]string]int{labelVals1: 0, labelVals2: 0},
		},
		"Workload custom label defined: Update workload in queue with modified label": {
			labels: &[]config.ControllerMetricsCustomLabel{wlLabel1, wlLabel2},
			ops: func(ctx context.Context, log logr.Logger, cq *ClusterQueue) {
				wl1 := makeWorkload("wl1", map[string]string{"project": "project-a", "type": "type-a"})
				cq.PushOrUpdate(wl1)
				wl1Updated := makeWorkload("wl1", map[string]string{"project": "project-b", "type": "type-b"})
				cq.PushOrUpdate(wl1Updated)
			},
			wantPending:      map[[6]string]int{labelVals1: 0, labelVals2: 1},
			wantInadmissible: map[[6]string]int{},
		},
		"Workload custom label defined: Delete workload from inadmissible": {
			labels: &[]config.ControllerMetricsCustomLabel{wlLabel1, wlLabel2},
			ops: func(ctx context.Context, log logr.Logger, cq *ClusterQueue) {
				wl1 := makeWorkload("wl1", map[string]string{"project": "project-a", "type": "type-a"})
				wl2 := makeWorkload("wl2", map[string]string{"project": "project-a", "type": "type-a"})
				cq.PushOrUpdate(wl1)
				cq.PushOrUpdate(wl2)
				popped1 := cq.Pop()
				cq.RequeueIfNotPresent(ctx, popped1, RequeueReasonNoFit, QuotaReservedReason(""))
				popped2 := cq.Pop()
				cq.RequeueIfNotPresent(ctx, popped2, RequeueReasonNoFit, QuotaReservedReason(""))
				cq.Delete(log, workload.Key(wl1.Obj))
			},
			wantPending:      map[[6]string]int{labelVals1: 0},
			wantInadmissible: map[[6]string]int{labelVals1: 1},
		},
		"Both ClusterQueue and Workload custom labels defined": {
			labels: &[]config.ControllerMetricsCustomLabel{cqLabel, wlLabel1, wlLabel2},
			ops: func(ctx context.Context, log logr.Logger, cq *ClusterQueue) {
				wl1 := makeWorkload("wl1", map[string]string{"project": "project-a", "type": "type-a"})
				wl2 := makeWorkload("wl2", map[string]string{"project": "project-b", "type": "type-b"})
				cq.PushOrUpdate(wl1)
				cq.PushOrUpdate(wl2)
				popped1 := cq.Pop()
				cq.RequeueIfNotPresent(ctx, popped1, RequeueReasonNoFit, QuotaReservedReason(""))
			},
			wantPending:      map[[6]string]int{labelVals1: 0, labelVals2: 1},
			wantInadmissible: map[[6]string]int{labelVals1: 1},
		},
		"Workload custom label using undefined label values": {
			labels: &[]config.ControllerMetricsCustomLabel{wlLabel1, wlLabel2},
			ops: func(ctx context.Context, log logr.Logger, cq *ClusterQueue) {
				wl1 := makeWorkload("wl1", map[string]string{"project": "project-a", "type": "type-a"})
				wl2 := makeWorkload("wl2", map[string]string{"project": "untracked-value", "type": "untracked-value"})
				cq.PushOrUpdate(wl1)
				cq.PushOrUpdate(wl2)
			},
			wantPending:      map[[6]string]int{labelVals1: 1, labelValsUntracked: 1},
			wantInadmissible: map[[6]string]int{},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(func() { metrics.InitMetricVectors(nil) })
			features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, true)
			features.SetFeatureGateDuringTest(t, features.SchedulingEquivalenceHashing, false)
			ctx, log := utiltesting.ContextWithLog(t)
			cl := metrics.NewCustomLabels(*tc.labels)
			cq := newClusterQueueImpl(ctx, nil, cl, defaultOrdering, testingclock.NewFakeClock(time.Now()))
			cq.queueingStrategy = kueue.BestEffortFIFO
			cq.namespaceSelector = labels.Everything()

			if tc.ops != nil {
				tc.ops(ctx, log, cq)
			}

			gotPendingBreakdown, gotInadmissibleBreakdown := cq.PendingBreakdown()

			gotPending := make(map[[6]string]int, 0)
			for vals, count := range gotPendingBreakdown.Iter() {
				key := [6]string{}
				copy(key[:], vals.OrderedList())
				gotPending[key] = count
			}
			if diff := cmp.Diff(tc.wantPending, gotPending); diff != "" {
				t.Errorf("Unexpected pending tracker (-want +got):\n%s", diff)
			}

			gotInadmissible := make(map[[6]string]int, 0)
			for vals, count := range gotInadmissibleBreakdown.Iter() {
				key := [6]string{}
				copy(key[:], vals.OrderedList())
				gotInadmissible[key] = count
			}
			if diff := cmp.Diff(tc.wantInadmissible, gotInadmissible); diff != "" {
				t.Errorf("Unexpected inadmissible tracker (-want +got):\n%s", diff)
			}
		})
	}
}

// TestPopMidCycleDoesNotConsumeRequeueSignal verifies that a mid-cycle pop
// (fair sharing refill) does not advance the evaluation epoch: an
// inadmissible-requeue event that lands after the cycle's head was popped
// must still send both the head and the mid-cycle popped workload back to
// the active heap when they are requeued non-immediately.
func TestPopMidCycleDoesNotConsumeRequeueSignal(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	now := time.Now()
	cq := newClusterQueueImpl(ctx, nil, nil, defaultOrdering, testingclock.NewFakeClock(now))
	head := workload.NewInfo(utiltestingapi.MakeWorkload("head", defaultNamespace).Creation(now).Obj())
	next := workload.NewInfo(utiltestingapi.MakeWorkload("next", defaultNamespace).Creation(now.Add(time.Second)).Obj())
	cq.PushOrUpdate(head)
	cq.PushOrUpdate(next)

	// Cycle start: the head is popped.
	if got := cq.Pop(); got == nil || got.Obj.Name != "head" {
		t.Fatalf("Pop() = %v, want head", got)
	}
	// A cluster event lands mid-cycle (e.g. capacity was freed). With an
	// empty inadmissible set this only stamps queueInadmissibleCycle.
	queueInadmissibleWorkloads(ctx, cq, nil)
	// Refill pops the next workload mid-cycle; both popped workloads were
	// evaluated against the snapshot taken before the event.
	if got := cq.PopMidCycle(); got == nil || got.Obj.Name != "next" {
		t.Fatalf("PopMidCycle() = %v, want next", got)
	}

	// Both requeue non-immediately (e.g. NoFit). Because the event arrived
	// after they were popped, they must return to the active heap instead of
	// being parked as inadmissible.
	for _, wl := range []*workload.Info{head, next} {
		if !cq.RequeueIfNotPresent(ctx, wl, RequeueReasonNoFit, "") {
			t.Fatalf("RequeueIfNotPresent(%s) returned false", wl.Obj.Name)
		}
	}
	active, _ := cq.Dump()
	if len(active) != 2 {
		inadmissible, _ := cq.DumpInadmissible()
		t.Errorf("expected both workloads back on the active heap, got active %v, inadmissible %v", active, inadmissible)
	}
}
