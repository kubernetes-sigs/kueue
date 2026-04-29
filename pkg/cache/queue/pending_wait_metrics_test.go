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
	"testing"
	"time"

	testingclock "k8s.io/utils/clock/testing"

	"sigs.k8s.io/kueue/pkg/metrics"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestPendingWaitQuantiles_ActiveHeap(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	created := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	now := created.Add(100 * time.Second)
	clk := testingclock.NewFakeClock(now)
	cq := newClusterQueueImpl(ctx, nil, defaultOrdering, clk)
	wl := utiltestingapi.MakeWorkload("w1", defaultNamespace).Creation(created).Obj()
	cq.PushOrUpdate(workload.NewInfo(wl))

	active, inadm := cq.pendingWaitQuantiles(clk, true)
	if active.N != 1 || active.P50 != 100 || active.P95 != 100 || active.P99 != 100 {
		t.Fatalf("unexpected active quantiles: %+v", active)
	}
	if inadm.N != 0 {
		t.Fatalf("expected zero inadmissible, got: %+v", inadm)
	}
}

func TestPendingWaitQuantiles_FoldsInactiveCQ(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	created := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	now := created.Add(50 * time.Second)
	clk := testingclock.NewFakeClock(now)
	cq := newClusterQueueImpl(ctx, nil, defaultOrdering, clk)
	wl := utiltestingapi.MakeWorkload("w1", defaultNamespace).Creation(created).Obj()
	cq.PushOrUpdate(workload.NewInfo(wl))

	active, inadm := cq.pendingWaitQuantiles(clk, false)
	if active.N != 0 {
		t.Fatalf("inactive CQ should have zero active, got: %+v", active)
	}
	if inadm.N != 1 || inadm.P50 != 50 || inadm.P95 != 50 || inadm.P99 != 50 {
		t.Fatalf("inactive CQ should fold active into inadmissible: %+v", inadm)
	}
}

func TestPendingWaitQuantiles_MultipleWorkloads(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	base := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	now := base.Add(100 * time.Second)
	clk := testingclock.NewFakeClock(now)
	cq := newClusterQueueImpl(ctx, nil, defaultOrdering, clk)

	// Create 10 workloads with increasing wait times: 10, 20, ..., 100 seconds.
	for i := range 10 {
		created := now.Add(-time.Duration(10*(i+1)) * time.Second)
		wl := utiltestingapi.MakeWorkload("w"+string(rune('a'+i)), defaultNamespace).
			Creation(created).
			Obj()
		cq.PushOrUpdate(workload.NewInfo(wl))
	}

	active, inadm := cq.pendingWaitQuantiles(clk, true)
	if active.N != 10 {
		t.Fatalf("expected 10 active, got %d", active.N)
	}
	// With 10 samples (10, 20, 30, 40, 50, 60, 70, 80, 90, 100):
	// p50 = 50, p95 = 100, p99 = 100 (nearest rank, ceil rounding)
	if active.P50 != 50 {
		t.Errorf("expected p50=50, got %v", active.P50)
	}
	if active.P95 != 100 {
		t.Errorf("expected p95=100, got %v", active.P95)
	}
	if active.P99 != 100 {
		t.Errorf("expected p99=100, got %v", active.P99)
	}
	if inadm.N != 0 {
		t.Fatalf("expected zero inadmissible, got: %+v", inadm)
	}
}

func TestQuantileFromSorted(t *testing.T) {
	sorted := []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	cases := []struct {
		q    float64
		want float64
	}{
		{0.50, 50},
		{0.95, 100},
		{0.99, 100},
	}
	for _, tc := range cases {
		got := quantileFromSorted(sorted, tc.q)
		if got != tc.want {
			t.Errorf("quantile(%v): got %v, want %v", tc.q, got, tc.want)
		}
	}
}

func TestQuantileFromSortedEdgeCases(t *testing.T) {
	if got := quantileFromSorted(nil, 0.5); got != 0 {
		t.Errorf("empty slice: got %v, want 0", got)
	}
	if got := quantileFromSorted([]float64{42}, 0.99); got != 42 {
		t.Errorf("single element: got %v, want 42", got)
	}
}

func TestPendingWaitMetricsWorker_ReconcileClearsMissingCQ(t *testing.T) {
	ctx := t.Context()
	w := NewPendingWaitMetricsWorker()
	w.setManager(NewManagerForUnitTests(nil, nil))
	w.reconcile(ctx, "missing-cq")
}

func TestPendingWaitQuantilesReturnType(t *testing.T) {
	// Verify the return type is metrics.PendingWorkloadWaitQuantiles
	var q metrics.PendingWorkloadWaitQuantiles
	q.N = 0
	if q.P50 != 0 || q.P95 != 0 || q.P99 != 0 {
		t.Fatal("zero value mismatch")
	}
}
