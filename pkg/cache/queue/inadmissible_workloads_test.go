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
	"maps"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/component-base/featuregate"

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

func TestNotifyRetryInadmissibleAfter(t *testing.T) {
	const batchPeriod = time.Second

	workloadMap := func(cqs ...kueue.ClusterQueueReference) map[kueue.ClusterQueueReference][]workload.Reference {
		if len(cqs) == 0 {
			return nil
		}
		res := make(map[kueue.ClusterQueueReference][]workload.Reference, len(cqs))
		for _, cq := range cqs {
			res[cq] = []workload.Reference{workload.Reference("ns/wl-" + string(cq))}
		}
		return res
	}

	cases := map[string]struct {
		notifyCQ            kueue.ClusterQueueReference
		delay               time.Duration
		inadmissibleCQs     []kueue.ClusterQueueReference
		wantWait            time.Duration
		wantActiveCQs       []kueue.ClusterQueueReference
		wantInadmissibleCQs []kueue.ClusterQueueReference
	}{
		"standalone ClusterQueue moves inadmissible workload to active after delay": {
			notifyCQ:        "standalone-cq",
			delay:           5 * time.Second,
			inadmissibleCQs: []kueue.ClusterQueueReference{"standalone-cq"},
			wantWait:        5 * time.Second,
			wantActiveCQs:   []kueue.ClusterQueueReference{"standalone-cq"},
		},
		"nested cohort ClusterQueue moves cohort tree inadmissible workloads to active after delay": {
			notifyCQ:            "child-cohort-cq",
			delay:               5 * time.Second,
			inadmissibleCQs:     []kueue.ClusterQueueReference{"child-cohort-cq", "root-cohort-cq", "standalone-cq"},
			wantWait:            5 * time.Second,
			wantActiveCQs:       []kueue.ClusterQueueReference{"child-cohort-cq", "root-cohort-cq"},
			wantInadmissibleCQs: []kueue.ClusterQueueReference{"standalone-cq"},
		},
		"delay shorter than batchPeriod waits batchPeriod": {
			notifyCQ:        "standalone-cq",
			delay:           200 * time.Millisecond,
			inadmissibleCQs: []kueue.ClusterQueueReference{"standalone-cq"},
			wantWait:        batchPeriod,
			wantActiveCQs:   []kueue.ClusterQueueReference{"standalone-cq"},
		},
		"negative delay waits batchPeriod": {
			notifyCQ:        "child-cohort-cq",
			delay:           -2 * time.Second,
			inadmissibleCQs: []kueue.ClusterQueueReference{"child-cohort-cq"},
			wantWait:        batchPeriod,
			wantActiveCQs:   []kueue.ClusterQueueReference{"child-cohort-cq"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, log := utiltesting.ContextWithLog(t)
				requeuer := NewRequeuer(WithBatchPeriod(batchPeriod))

				workloads := make([]*kueue.Workload, 0, len(tc.inadmissibleCQs))
				builder := utiltesting.NewClientBuilder().WithObjects(utiltesting.MakeNamespace("ns")).
					WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType)
				for _, cqName := range tc.inadmissibleCQs {
					wl := utiltestingapi.MakeWorkload("wl-"+string(cqName), "ns").
						Queue(kueue.LocalQueueName("lq-"+string(cqName))).
						Request(corev1.ResourceCPU, "1").
						Obj()
					workloads = append(workloads, wl)
					builder = builder.WithObjects(wl)
				}
				cl := builder.Build()
				m := NewManager(cl, nil, requeuer, WithPreemptionExpectations(preemptexpectations.New()))

				go func() {
					_ = requeuer.Start(ctx)
				}()

				m.AddOrUpdateCohort(ctx, utiltestingapi.MakeCohort("root-cohort").Obj())
				m.AddOrUpdateCohort(ctx, utiltestingapi.MakeCohort("child-cohort").Parent("root-cohort").Obj())
				for _, cq := range []*kueue.ClusterQueue{
					utiltestingapi.MakeClusterQueue("standalone-cq").Obj(),
					utiltestingapi.MakeClusterQueue("root-cohort-cq").Cohort("root-cohort").Obj(),
					utiltestingapi.MakeClusterQueue("child-cohort-cq").Cohort("child-cohort").Obj(),
				} {
					if err := m.AddClusterQueue(ctx, cq); err != nil {
						t.Fatalf("Adding ClusterQueue %q: %v", cq.Name, err)
					}
					lq := utiltestingapi.MakeLocalQueue("lq-"+cq.Name, "ns").ClusterQueue(cq.Name).Obj()
					if err := m.AddLocalQueue(ctx, lq); err != nil {
						t.Fatalf("Adding LocalQueue %q: %v", lq.Name, err)
					}
				}
				// Adding the queues schedules requeues, which would absorb a later requeue with a longer delay.
				time.Sleep(batchPeriod)
				synctest.Wait()

				for _, wl := range workloads {
					if err := m.AddOrUpdateWorkload(ctx, log, wl); err != nil {
						t.Fatalf("Adding workload %q: %v", wl.Name, err)
					}
				}
				popped := m.Heads(ctx)
				if len(popped) != len(workloads) {
					t.Fatalf("Heads() popped %d workloads, want %d", len(popped), len(workloads))
				}
				for i := range popped {
					if !m.RequeueWorkload(ctx, &popped[i].Info, RequeueReasonPreemptionNoCandidates, "") {
						t.Fatalf("Expected workload %q to be requeued as inadmissible", popped[i].Obj.Name)
					}
				}

				initialInadmissible := workloadMap(tc.inadmissibleCQs...)
				if diff := cmp.Diff(initialInadmissible, m.DumpInadmissible(), cmpopts.EquateEmpty()); diff != "" {
					t.Fatalf("Initial inadmissible workloads (-want,+got):\n%s", diff)
				}

				NotifyRetryInadmissibleAfter(m, sets.New(tc.notifyCQ), tc.delay)

				time.Sleep(tc.wantWait - time.Nanosecond)
				synctest.Wait()
				if diff := cmp.Diff(initialInadmissible, m.DumpInadmissible(), cmpopts.EquateEmpty()); diff != "" {
					t.Fatalf("Before wait elapses, unexpected inadmissible workloads (-want,+got):\n%s", diff)
				}

				time.Sleep(time.Nanosecond)
				synctest.Wait()
				if diff := cmp.Diff(workloadMap(tc.wantInadmissibleCQs...), m.DumpInadmissible(), cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("At wait, unexpected inadmissible workloads (-want,+got):\n%s", diff)
				}
				if diff := cmp.Diff(workloadMap(tc.wantActiveCQs...), m.Dump(), cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("At wait, unexpected active workloads (-want,+got):\n%s", diff)
				}
			})
		})
	}
}
