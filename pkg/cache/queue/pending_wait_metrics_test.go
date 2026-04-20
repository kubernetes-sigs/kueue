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

	testingclock "k8s.io/utils/clock/testing"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestPendingWaitAggregates_ActiveHeap(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	created := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	now := created.Add(100 * time.Second)
	clk := testingclock.NewFakeClock(now)
	cq := newClusterQueueImpl(ctx, nil, defaultOrdering, clk)
	wl := utiltestingapi.MakeWorkload("w1", defaultNamespace).Creation(created).Obj()
	cq.PushOrUpdate(workload.NewInfo(wl))

	activeMax, activeMean, activeN, inadmMax, inadmMean, inadmN := cq.pendingWaitAggregates(clk, true)
	if activeMax != 100 || activeMean != 100 || activeN != 1 || inadmMax != 0 || inadmMean != 0 || inadmN != 0 {
		t.Fatalf("unexpected aggregates: active max=%v mean=%v n=%v inadm max=%v mean=%v n=%v",
			activeMax, activeMean, activeN, inadmMax, inadmMean, inadmN)
	}
}

func TestPendingWaitAggregates_FoldsInactiveCQ(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	created := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	now := created.Add(50 * time.Second)
	clk := testingclock.NewFakeClock(now)
	cq := newClusterQueueImpl(ctx, nil, defaultOrdering, clk)
	wl := utiltestingapi.MakeWorkload("w1", defaultNamespace).Creation(created).Obj()
	cq.PushOrUpdate(workload.NewInfo(wl))

	activeMax, activeMean, activeN, inadmMax, inadmMean, inadmN := cq.pendingWaitAggregates(clk, false)
	if activeMax != 0 || activeMean != 0 || activeN != 0 || inadmMax != 50 || inadmMean != 50 || inadmN != 1 {
		t.Fatalf("inactive CQ should fold active into inadmissible: active max=%v mean=%v n=%v inadm max=%v mean=%v n=%v",
			activeMax, activeMean, activeN, inadmMax, inadmMean, inadmN)
	}
}

func TestPendingWaitMetricsWorker_ReconcileClearsMissingCQ(t *testing.T) {
	ctx := context.Background()
	w := NewPendingWaitMetricsWorker()
	w.setManager(NewManagerForUnitTests(nil, nil))
	w.reconcile(ctx, "missing-cq")
}
