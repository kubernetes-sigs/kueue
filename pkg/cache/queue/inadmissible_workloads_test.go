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
	"maps"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/component-base/featuregate"
	"k8s.io/utils/clock"
	testingclock "k8s.io/utils/clock/testing"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestNewRequeuer(t *testing.T) {
	type args struct {
		featureGates map[featuregate.Feature]bool
		opts         []RequeuerOption
	}

	type want struct {
		batchPeriod time.Duration
	}

	testCases := map[string]struct {
		args args
		want want
	}{
		"custom batch period": {
			args: args{
				opts: []RequeuerOption{
					WithBatchPeriod(10 * time.Millisecond),
				},
			},
			want: want{
				batchPeriod: 10 * time.Millisecond,
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.args.featureGates)
			requeuer := NewRequeuer(tc.args.opts...)
			if diff := cmp.Diff(tc.want.batchPeriod, requeuer.batchPeriod); len(diff) != 0 {
				t.Errorf("Unexpected requeue batch period (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestInadmissibleWorkloads_Get(t *testing.T) {
	_, log := utiltesting.ContextWithLog(t)
	wl1 := workload.NewInfo(log, utiltestingapi.MakeWorkload("wl1", "ns1").Obj())
	wl2 := workload.NewInfo(log, utiltestingapi.MakeWorkload("wl2", "ns2").Obj())
	key1 := workload.Key(wl1.Obj)
	key2 := workload.Key(wl2.Obj)

	testcases := []struct {
		name         string
		initial      map[workload.Reference]*workload.Info
		key          workload.Reference
		wantWorkload *workload.Info
	}{
		{
			name:         "returns nil for non-existent workload",
			initial:      nil,
			key:          key1,
			wantWorkload: nil,
		},
		{
			name: "returns workload when exists",
			initial: map[workload.Reference]*workload.Info{
				key1: wl1,
			},
			key:          key1,
			wantWorkload: wl1,
		},
		{
			name: "returns nil for different key",
			initial: map[workload.Reference]*workload.Info{
				key1: wl1,
			},
			key:          key2,
			wantWorkload: nil,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			iw := make(inadmissibleWorkloads)
			maps.Copy(iw, tc.initial)

			got := iw.get(tc.key)
			if got != tc.wantWorkload {
				t.Errorf("get() = %v, want %v", got, tc.wantWorkload)
			}
		})
	}
}

func TestInadmissibleWorkloads_Insert(t *testing.T) {
	_, log := utiltesting.ContextWithLog(t)
	wl1 := workload.NewInfo(log, utiltestingapi.MakeWorkload("wl1", "ns1").Obj())
	wl2 := workload.NewInfo(log, utiltestingapi.MakeWorkload("wl2", "ns2").Obj())
	key1 := workload.Key(wl1.Obj)
	key2 := workload.Key(wl2.Obj)

	testcases := []struct {
		name    string
		initial map[workload.Reference]*workload.Info
		key     workload.Reference
		value   *workload.Info
		wantLen int
	}{
		{
			name:    "insert into empty map",
			initial: nil,
			key:     key1,
			value:   wl1,
			wantLen: 1,
		},
		{
			name: "insert new workload",
			initial: map[workload.Reference]*workload.Info{
				key1: wl1,
			},
			key:     key2,
			value:   wl2,
			wantLen: 2,
		},
		{
			name: "overwrite existing workload",
			initial: map[workload.Reference]*workload.Info{
				key1: wl1,
			},
			key:     key1,
			value:   wl2,
			wantLen: 1,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			iw := make(inadmissibleWorkloads)
			maps.Copy(iw, tc.initial)

			iw.insert(tc.key, tc.value)

			if got := iw.len(); got != tc.wantLen {
				t.Errorf("after insert, len() = %d, want %d", got, tc.wantLen)
			}
			if got := iw.get(tc.key); got != tc.value {
				t.Errorf("after insert, get() = %v, want %v", got, tc.value)
			}
		})
	}
}

func TestInadmissibleWorkloads_Delete(t *testing.T) {
	_, log := utiltesting.ContextWithLog(t)
	wl1 := workload.NewInfo(log, utiltestingapi.MakeWorkload("wl1", "ns1").Obj())
	wl2 := workload.NewInfo(log, utiltestingapi.MakeWorkload("wl2", "ns2").Obj())
	key1 := workload.Key(wl1.Obj)
	key2 := workload.Key(wl2.Obj)

	testcases := []struct {
		name    string
		initial map[workload.Reference]*workload.Info
		key     workload.Reference
		wantLen int
	}{
		{
			name:    "delete from empty map",
			initial: nil,
			key:     key1,
			wantLen: 0,
		},
		{
			name: "delete existing workload",
			initial: map[workload.Reference]*workload.Info{
				key1: wl1,
			},
			key:     key1,
			wantLen: 0,
		},
		{
			name: "delete non-existent workload",
			initial: map[workload.Reference]*workload.Info{
				key1: wl1,
			},
			key:     key2,
			wantLen: 1,
		},
		{
			name: "delete one of multiple workloads",
			initial: map[workload.Reference]*workload.Info{
				key1: wl1,
				key2: wl2,
			},
			key:     key1,
			wantLen: 1,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			iw := make(inadmissibleWorkloads)
			maps.Copy(iw, tc.initial)

			iw.delete(tc.key)

			if got := iw.len(); got != tc.wantLen {
				t.Errorf("after delete, len() = %d, want %d", got, tc.wantLen)
			}
			if got := iw.get(tc.key); got != nil {
				t.Errorf("after delete, get() = %v, want nil", got)
			}
		})
	}
}

func TestInadmissibleWorkloads_Len(t *testing.T) {
	_, log := utiltesting.ContextWithLog(t)
	wl1 := workload.NewInfo(log, utiltestingapi.MakeWorkload("wl1", "ns1").Obj())
	wl2 := workload.NewInfo(log, utiltestingapi.MakeWorkload("wl2", "ns2").Obj())
	key1 := workload.Key(wl1.Obj)
	key2 := workload.Key(wl2.Obj)

	testcases := []struct {
		name    string
		initial map[workload.Reference]*workload.Info
		wantLen int
	}{
		{
			name:    "empty map",
			initial: nil,
			wantLen: 0,
		},
		{
			name: "single workload",
			initial: map[workload.Reference]*workload.Info{
				key1: wl1,
			},
			wantLen: 1,
		},
		{
			name: "multiple workloads",
			initial: map[workload.Reference]*workload.Info{
				key1: wl1,
				key2: wl2,
			},
			wantLen: 2,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			iw := make(inadmissibleWorkloads)
			maps.Copy(iw, tc.initial)

			if got := iw.len(); got != tc.wantLen {
				t.Errorf("len() = %d, want %d", got, tc.wantLen)
			}
		})
	}
}

func TestInadmissibleWorkloads_Empty(t *testing.T) {
	_, log := utiltesting.ContextWithLog(t)
	wl1 := workload.NewInfo(log, utiltestingapi.MakeWorkload("wl1", "ns1").Obj())
	key1 := workload.Key(wl1.Obj)

	testcases := []struct {
		name      string
		initial   map[workload.Reference]*workload.Info
		wantEmpty bool
	}{
		{
			name:      "empty map",
			initial:   nil,
			wantEmpty: true,
		},
		{
			name: "non-empty map",
			initial: map[workload.Reference]*workload.Info{
				key1: wl1,
			},
			wantEmpty: false,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			iw := make(inadmissibleWorkloads)
			maps.Copy(iw, tc.initial)

			if got := iw.empty(); got != tc.wantEmpty {
				t.Errorf("empty() = %v, want %v", got, tc.wantEmpty)
			}
		})
	}
}

type requeueCall struct {
	Req   requeueRequest
	Delay time.Duration
}

type recordingDelayingQueue struct {
	workqueue.TypedDelayingInterface[requeueRequest]
	calls []requeueCall
}

func (q *recordingDelayingQueue) AddAfter(item requeueRequest, duration time.Duration) {
	q.calls = append(q.calls, requeueCall{Req: item, Delay: duration})
}

func TestNotifyRetryInadmissibleAfter(t *testing.T) {
	const batchPeriod = time.Second

	cases := map[string]struct {
		cqName             kueue.ClusterQueueReference
		notifyWithoutDelay bool
		delay              time.Duration
		wantCalls          []requeueCall
	}{
		"standalone ClusterQueue with delay exceeding batchPeriod": {
			cqName: "standalone-cq",
			delay:  5 * time.Second,
			wantCalls: []requeueCall{
				{Req: requeueRequest{ClusterQueue: "standalone-cq"}, Delay: 5 * time.Second},
			},
		},
		"nested cohort ClusterQueue routes to root cohort with delay": {
			cqName: "cohort-cq",
			delay:  5 * time.Second,
			wantCalls: []requeueCall{
				{Req: requeueRequest{Cohort: "root-cohort"}, Delay: 5 * time.Second},
			},
		},
		"delay shorter than batchPeriod is clamped to batchPeriod": {
			cqName: "standalone-cq",
			delay:  200 * time.Millisecond,
			wantCalls: []requeueCall{
				{Req: requeueRequest{ClusterQueue: "standalone-cq"}, Delay: batchPeriod},
			},
		},
		"negative delay is clamped to batchPeriod": {
			cqName: "standalone-cq",
			delay:  -2 * time.Second,
			wantCalls: []requeueCall{
				{Req: requeueRequest{ClusterQueue: "standalone-cq"}, Delay: batchPeriod},
			},
		},
		"NotifyRetryInadmissible uses batchPeriod": {
			cqName:             "standalone-cq",
			notifyWithoutDelay: true,
			wantCalls: []requeueCall{
				{Req: requeueRequest{ClusterQueue: "standalone-cq"}, Delay: batchPeriod},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			rec := &recordingDelayingQueue{}
			requeuer := &workqueueRequeuer{
				queue:       rec,
				batchPeriod: batchPeriod,
			}
			cl := utiltesting.NewClientBuilder().Build()
			m := NewManager(cl, nil, requeuer, WithPreemptionExpectations(preemptexpectations.New()))
			m.AddOrUpdateCohort(ctx, utiltestingapi.MakeCohort("root-cohort").Obj())
			m.AddOrUpdateCohort(ctx, utiltestingapi.MakeCohort("child-cohort").Parent("root-cohort").Obj())
			for _, cq := range []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("standalone-cq").Obj(),
				utiltestingapi.MakeClusterQueue("cohort-cq").Cohort("child-cohort").Obj(),
			} {
				if err := m.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Adding ClusterQueue %q: %v", cq.Name, err)
				}
			}
			rec.calls = nil

			if tc.notifyWithoutDelay {
				NotifyRetryInadmissible(m, sets.New(tc.cqName))
			} else {
				NotifyRetryInadmissibleAfter(m, sets.New(tc.cqName), tc.delay)
			}

			if diff := cmp.Diff(tc.wantCalls, rec.calls, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected requeue calls (-want,+got):\n%s", diff)
			}
		})
	}
}

type timerSignalClock struct {
	*testingclock.FakeClock
	timerCh chan time.Duration
}

func (c *timerSignalClock) NewTimer(d time.Duration) clock.Timer {
	timer := c.FakeClock.NewTimer(d)
	c.timerCh <- d
	return timer
}

type trackedDelayingQueue struct {
	workqueue.TypedDelayingInterface[requeueRequest]
	doneCh chan requeueRequest
}

func (q *trackedDelayingQueue) Done(item requeueRequest) {
	q.TypedDelayingInterface.Done(item)
	q.doneCh <- item
}

func waitSignal[T any](t *testing.T, ch <-chan T, desc string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(wait.ForeverTestTimeout):
		t.Fatalf("Timed out waiting for %s", desc)
		var zero T
		return zero
	}
}

func TestWorkqueueRequeuerStart(t *testing.T) {
	const (
		batchPeriod = time.Second
		delay       = 5 * time.Second
	)
	ctx, log := utiltesting.ContextWithLog(t)
	workerCtx, cancelWorker := context.WithCancel(ctx)
	fakeClock := &timerSignalClock{
		FakeClock: testingclock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		timerCh:   make(chan time.Duration, 16),
	}
	dq := &trackedDelayingQueue{
		TypedDelayingInterface: workqueue.NewTypedDelayingQueueWithConfig(workqueue.TypedDelayingQueueConfig[requeueRequest]{
			Clock: fakeClock,
		}),
		doneCh: make(chan requeueRequest, 16),
	}
	requeuer := &workqueueRequeuer{
		queue:       dq,
		batchPeriod: batchPeriod,
	}

	wl := utiltestingapi.MakeWorkload("wl-1", "ns").Queue("lq-1").Request(corev1.ResourceCPU, "1").Obj()
	cl := utiltesting.NewClientBuilder().WithObjects(utiltesting.MakeNamespace("ns"), wl).
		WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).Build()
	m := NewManager(cl, nil, requeuer, WithClock(fakeClock), WithPreemptionExpectations(preemptexpectations.New()))

	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		_ = requeuer.Start(workerCtx)
	}()
	t.Cleanup(func() {
		cancelWorker()
		waitSignal(t, workerDone, "requeuer worker shutdown")
	})

	if err := m.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue("cq-1").Obj()); err != nil {
		t.Fatalf("Adding cq-1: %v", err)
	}
	waitSignal(t, fakeClock.timerCh, "initial cq-1 timer")
	if err := m.AddLocalQueue(ctx, utiltestingapi.MakeLocalQueue("lq-1", "ns").ClusterQueue("cq-1").Obj()); err != nil {
		t.Fatalf("Adding lq-1: %v", err)
	}
	fakeClock.Step(batchPeriod)
	waitSignal(t, dq.doneCh, "initial cq-1 requeue")

	if err := m.AddOrUpdateWorkload(ctx, log, wl); err != nil {
		t.Fatalf("Adding workload: %v", err)
	}
	popped := m.Heads(ctx)
	if len(popped) != 1 {
		t.Fatalf("Heads() popped %d workloads, want 1", len(popped))
	}
	if !m.RequeueWorkload(ctx, &popped[0].Info, RequeueReasonPreemptionNoCandidates, "") {
		t.Fatal("Expected workload to be requeued as inadmissible")
	}

	wantWorkloads := map[kueue.ClusterQueueReference][]workload.Reference{
		"cq-1": {"ns/wl-1"},
	}
	NotifyRetryInadmissibleAfter(m, sets.New[kueue.ClusterQueueReference]("cq-1"), delay)
	if gotDelay := waitSignal(t, fakeClock.timerCh, "retry timer"); gotDelay != delay {
		t.Fatalf("armed timer = %v, want %v", gotDelay, delay)
	}

	fakeClock.Step(delay - time.Nanosecond)
	if diff := cmp.Diff(wantWorkloads, m.DumpInadmissible(), cmpopts.EquateEmpty()); diff != "" {
		t.Fatalf("Before delay elapses, unexpected inadmissible workloads (-want,+got):\n%s", diff)
	}

	fakeClock.Step(time.Nanosecond)
	waitSignal(t, dq.doneCh, "delayed requeue")
	if diff := cmp.Diff(map[kueue.ClusterQueueReference][]workload.Reference(nil), m.DumpInadmissible(), cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("At delay, unexpected inadmissible workloads (-want,+got):\n%s", diff)
	}
	if diff := cmp.Diff(wantWorkloads, m.Dump(), cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("At delay, unexpected active workloads (-want,+got):\n%s", diff)
	}
}
