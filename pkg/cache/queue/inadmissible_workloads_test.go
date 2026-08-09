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
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"

	"sigs.k8s.io/kueue/pkg/features"
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
		batchPeriod         time.Duration
		periodicRetryPeriod time.Duration
	}

	testCases := map[string]struct {
		args args
		want want
	}{
		"SchedulerLongRequeueInterval feature disabled": {
			args: args{
				featureGates: map[featuregate.Feature]bool{
					features.SchedulerLongRequeueInterval: false,
				},
			},
			want: want{
				batchPeriod:         time.Second,
				periodicRetryPeriod: 5 * time.Minute,
			},
		},
		"SchedulerLongRequeueInterval feature enabled": {
			args: args{
				featureGates: map[featuregate.Feature]bool{
					features.SchedulerLongRequeueInterval: true,
				},
			},
			want: want{
				batchPeriod:         10 * time.Second,
				periodicRetryPeriod: 5 * time.Minute,
			},
		},
		"custom batch period": {
			args: args{
				opts: []RequeuerOption{
					WithBatchPeriod(10 * time.Millisecond),
				},
			},
			want: want{
				batchPeriod:         10 * time.Millisecond,
				periodicRetryPeriod: 5 * time.Minute,
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
			if diff := cmp.Diff(tc.want.periodicRetryPeriod, requeuer.periodicRetryPeriod); len(diff) != 0 {
				t.Errorf("Unexpected periodic retry period (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestWorkqueueRequeuer_PeriodicRetry(t *testing.T) {
	const retryPeriod = time.Minute
	ctx, _ := utiltesting.ContextWithLog(t)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cl := utiltesting.NewFakeClient(utiltesting.MakeNamespace("ns"))
	manager, _ := NewManagerForUnitTestsWithRequeuer(cl, nil)
	cq := utiltestingapi.MakeClusterQueue("cq").Obj()
	if err := manager.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Failed adding ClusterQueue: %v", err)
	}
	lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj()
	if err := manager.AddLocalQueue(ctx, lq); err != nil {
		t.Fatalf("Failed adding LocalQueue: %v", err)
	}
	wl := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").Creation(time.Now()).Obj()
	if err := cl.Create(ctx, wl); err != nil {
		t.Fatalf("Failed adding Workload to client: %v", err)
	}
	manager.getClusterQueue("cq").popCycle++
	manager.RequeueWorkload(ctx, workload.NewInfo(wl), RequeueReasonGeneric, "")
	if got := manager.DumpInadmissible(); len(got["cq"]) != 1 {
		t.Fatalf("Workload is not in the inadmissible queue: %v", got)
	}

	fakeClock := testingclock.NewFakeClock(time.Now())
	requeuer := NewRequeuer(WithBatchPeriod(0))
	requeuer.clock = fakeClock
	requeuer.periodicRetryPeriod = retryPeriod
	requeuer.setManager(manager)
	manager.requeuer = requeuer

	errCh := make(chan error, 1)
	go func() {
		errCh <- requeuer.Start(ctx)
	}()
	if err := wait.PollUntilContextTimeout(t.Context(), time.Millisecond, time.Second, true, func(context.Context) (bool, error) {
		return fakeClock.HasWaiters(), nil
	}); err != nil {
		t.Fatalf("Periodic retry did not start: %v", err)
	}

	fakeClock.Step(retryPeriod - time.Nanosecond)
	if got := manager.DumpInadmissible(); len(got["cq"]) != 1 {
		t.Fatalf("Workload retried before the periodic interval elapsed: %v", got)
	}

	fakeClock.Step(time.Nanosecond)
	if err := wait.PollUntilContextTimeout(t.Context(), time.Millisecond, time.Second, true, func(context.Context) (bool, error) {
		return len(manager.Dump()["cq"]) == 1, nil
	}); err != nil {
		t.Fatalf("Periodic retry did not move the Workload to the active heap: %v", err)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Requeuer returned an error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Requeuer did not stop after context cancellation")
	}
}

func TestInadmissibleWorkloads_Get(t *testing.T) {
	wl1 := workload.NewInfo(utiltestingapi.MakeWorkload("wl1", "ns1").Obj())
	wl2 := workload.NewInfo(utiltestingapi.MakeWorkload("wl2", "ns2").Obj())
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
	wl1 := workload.NewInfo(utiltestingapi.MakeWorkload("wl1", "ns1").Obj())
	wl2 := workload.NewInfo(utiltestingapi.MakeWorkload("wl2", "ns2").Obj())
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
	wl1 := workload.NewInfo(utiltestingapi.MakeWorkload("wl1", "ns1").Obj())
	wl2 := workload.NewInfo(utiltestingapi.MakeWorkload("wl2", "ns2").Obj())
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
	wl1 := workload.NewInfo(utiltestingapi.MakeWorkload("wl1", "ns1").Obj())
	wl2 := workload.NewInfo(utiltestingapi.MakeWorkload("wl2", "ns2").Obj())
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
	wl1 := workload.NewInfo(utiltestingapi.MakeWorkload("wl1", "ns1").Obj())
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
