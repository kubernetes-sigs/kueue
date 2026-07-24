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
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	testingclock "k8s.io/utils/clock/testing"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	kueuemetrics "sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func makeSchedulingHashInfo(now time.Time, name string, hash workload.EquivalenceHash, cpu string) *workload.Info {
	info := workload.NewInfo(utiltestingapi.MakeWorkload(name, defaultNamespace).
		Creation(now).
		Request(corev1.ResourceCPU, cpu).
		Obj())
	info.SchedulingHash = hash
	return info
}

func totalCPURequest(wInfo *workload.Info) int64 {
	var result int64
	for _, ps := range wInfo.TotalRequests {
		result += ps.Requests[corev1.ResourceCPU]
	}
	return result
}

func TestSchedulingHashCounts(t *testing.T) {
	now := time.Now()
	tests := map[string]struct {
		activeHashes       []workload.EquivalenceHash
		inadmissibleHashes []workload.EquivalenceHash
		inflightHash       workload.EquivalenceHash
		hasInflight        bool
		wantActive         int
		wantInadmissible   int
		wantUnion          int
	}{
		"reference counts and bucket overlap": {
			activeHashes:       []workload.EquivalenceHash{"hash-a", "hash-a"},
			inadmissibleHashes: []workload.EquivalenceHash{"hash-a", "hash-b"},
			wantActive:         1,
			wantInadmissible:   2,
			wantUnion:          2,
		},
		"invalid hashes are ignored": {
			activeHashes:       []workload.EquivalenceHash{"", workload.SchedulingHashUnknown},
			inadmissibleHashes: []workload.EquivalenceHash{"", workload.SchedulingHashUnknown},
			hasInflight:        true,
			inflightHash:       workload.SchedulingHashUnknown,
		},
		"unique inflight hash": {
			hasInflight:  true,
			inflightHash: "hash-a",
			wantActive:   1,
			wantUnion:    1,
		},
		"inflight hash shared with active": {
			activeHashes: []workload.EquivalenceHash{"hash-a"},
			hasInflight:  true,
			inflightHash: "hash-a",
			wantActive:   1,
			wantUnion:    1,
		},
		"inflight hash shared with inadmissible": {
			inadmissibleHashes: []workload.EquivalenceHash{"hash-a"},
			hasInflight:        true,
			inflightHash:       "hash-a",
			wantActive:         1,
			wantInadmissible:   1,
			wantUnion:          1,
		},
		"inflight hash distinct from both buckets": {
			activeHashes:       []workload.EquivalenceHash{"hash-a"},
			inadmissibleHashes: []workload.EquivalenceHash{"hash-b"},
			hasInflight:        true,
			inflightHash:       "hash-c",
			wantActive:         2,
			wantInadmissible:   1,
			wantUnion:          3,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			counts := newSchedulingHashCounts()
			activeInfos := make([]*workload.Info, 0, len(tc.activeHashes))
			for i, hash := range tc.activeHashes {
				info := makeSchedulingHashInfo(now, fmt.Sprintf("active-%d", i), hash, "1")
				activeInfos = append(activeInfos, info)
				counts.addActive(info)
			}
			inadmissibleInfos := make([]*workload.Info, 0, len(tc.inadmissibleHashes))
			for i, hash := range tc.inadmissibleHashes {
				info := makeSchedulingHashInfo(now, fmt.Sprintf("inadmissible-%d", i), hash, "1")
				inadmissibleInfos = append(inadmissibleInfos, info)
				counts.addInadmissible(info)
			}

			var inflight *workload.Info
			if tc.hasInflight {
				inflight = makeSchedulingHashInfo(now, "inflight", tc.inflightHash, "1")
			}

			if got := counts.activeLen(inflight); got != tc.wantActive {
				t.Errorf("activeLen() = %d, want %d", got, tc.wantActive)
			}
			if got := counts.inadmissibleLen(); got != tc.wantInadmissible {
				t.Errorf("inadmissibleLen() = %d, want %d", got, tc.wantInadmissible)
			}
			if got := counts.pendingUnionLen(inflight); got != tc.wantUnion {
				t.Errorf("pendingUnionLen() = %d, want %d", got, tc.wantUnion)
			}

			for _, info := range activeInfos {
				counts.removeActive(info)
			}
			for _, info := range inadmissibleInfos {
				counts.removeInadmissible(info)
			}
			if len(counts.active) != 0 || len(counts.inadmissible) != 0 {
				t.Errorf("counts after removals = active:%v inadmissible:%v, want empty", counts.active, counts.inadmissible)
			}
		})
	}
}

func TestPendingSchedulingHashes(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.SchedulingEquivalenceHashing, true)

	ctx, _ := utiltesting.ContextWithLog(t)
	now := time.Now()
	cq := newClusterQueueImpl(ctx, nil, defaultOrdering, testingclock.NewFakeClock(now))

	cq.PushOrUpdate(makeSchedulingHashInfo(now, "active-a", "hash-a", "1"))
	cq.PushOrUpdate(makeSchedulingHashInfo(now, "active-b", "hash-b", "1"))
	cq.PushOrUpdate(makeSchedulingHashInfo(now, "active-unknown", workload.SchedulingHashUnknown, "1"))
	cq.PushOrUpdate(makeSchedulingHashInfo(now, "active-empty", "", "1"))

	if popped := cq.Pop(); popped == nil {
		t.Fatal("expected one workload to be inflight")
	}

	inadmissibleDuplicate := makeSchedulingHashInfo(now, "inadmissible-b", "hash-b", "1")
	cq.insertInadmissible(workloadKey(inadmissibleDuplicate), inadmissibleDuplicate)
	inadmissibleC := makeSchedulingHashInfo(now, "inadmissible-c", "hash-c", "1")
	cq.insertInadmissible(workloadKey(inadmissibleC), inadmissibleC)
	inadmissibleUnknown := makeSchedulingHashInfo(now, "inadmissible-unknown", workload.SchedulingHashUnknown, "1")
	cq.insertInadmissible(workloadKey(inadmissibleUnknown), inadmissibleUnknown)

	active, inadmissible := cq.PendingSchedulingHashes()
	if active != 2 || inadmissible != 2 {
		t.Errorf("PendingSchedulingHashes() active=%d inadmissible=%d, want active=2 inadmissible=2", active, inadmissible)
	}
}

func TestPendingSchedulingHashesFeatureGateDisabled(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.SchedulingEquivalenceHashing, false)

	ctx, _ := utiltesting.ContextWithLog(t)
	now := time.Now()
	cq := newClusterQueueImpl(ctx, nil, defaultOrdering, testingclock.NewFakeClock(now))
	cq.PushOrUpdate(makeSchedulingHashInfo(now, "active", "hash-a", "1"))

	active, inadmissible := cq.PendingSchedulingHashes()
	if active != 0 || inadmissible != 0 {
		t.Errorf("PendingSchedulingHashes() active=%d inadmissible=%d, want active=0 inadmissible=0", active, inadmissible)
	}
	active, inadmissible = cq.pendingSchedulingHashesForInactiveClusterQueue()
	if active != 0 || inadmissible != 0 {
		t.Errorf("pendingSchedulingHashesForInactiveClusterQueue() active=%d inadmissible=%d, want active=0 inadmissible=0", active, inadmissible)
	}
}

func TestPendingSchedulingHashesTracksMutations(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.SchedulingEquivalenceHashing, true)

	ctx, log := utiltesting.ContextWithLog(t)
	now := time.Now()
	tests := map[string]struct {
		mutate                 func(t *testing.T, cq *ClusterQueue)
		wantActiveCounts       map[workload.EquivalenceHash]int
		wantInadmissibleCounts map[workload.EquivalenceHash]int
		wantActive             int
		wantInadmissible       int
		wantUnion              int
	}{
		"updates active hash counts when heap workload hash changes": {
			mutate: func(t *testing.T, cq *ClusterQueue) {
				cq.PushOrUpdate(makeSchedulingHashInfo(now, "active", "old-hash", "1"))
				cq.PushOrUpdate(makeSchedulingHashInfo(now, "active", "new-hash", "1"))
			},
			wantActiveCounts: map[workload.EquivalenceHash]int{"new-hash": 1},
			wantActive:       1,
			wantUnion:        1,
		},
		"updates inadmissible hash counts when workload hash changes in place": {
			mutate: func(t *testing.T, cq *ClusterQueue) {
				oldInfo := makeSchedulingHashInfo(now, "inadmissible", "old-hash", "1")
				cq.insertInadmissible(workloadKey(oldInfo), oldInfo)
				cq.PushOrUpdate(makeSchedulingHashInfo(now, "inadmissible", "new-hash", "1"))
			},
			wantInadmissibleCounts: map[workload.EquivalenceHash]int{"new-hash": 1},
			wantInadmissible:       1,
			wantUnion:              1,
		},
		"deletes hashes after the last workload leaves": {
			mutate: func(t *testing.T, cq *ClusterQueue) {
				cq.PushOrUpdate(makeSchedulingHashInfo(now, "active-a", "shared-hash", "1"))
				cq.PushOrUpdate(makeSchedulingHashInfo(now, "active-b", "shared-hash", "1"))
				cq.PushOrUpdate(makeSchedulingHashInfo(now, "active-c", "other-hash", "1"))
				cq.Delete(log, "default/active-a")
				if got := cq.schedulingHashes.active["shared-hash"]; got != 1 {
					t.Fatalf("shared-hash count after first delete = %d, want 1", got)
				}
				cq.Delete(log, "default/active-b")
			},
			wantActiveCounts: map[workload.EquivalenceHash]int{"other-hash": 1},
			wantActive:       1,
			wantUnion:        1,
		},
		"moves hash counts when equivalent workloads bulk move to inadmissible": {
			mutate: func(t *testing.T, cq *ClusterQueue) {
				cq.queueingStrategy = kueue.BestEffortFIFO
				cq.PushOrUpdate(makeSchedulingHashInfo(now, "active-a", "blocked-hash", "1"))
				cq.PushOrUpdate(makeSchedulingHashInfo(now, "active-b", "blocked-hash", "1"))
				cq.PushOrUpdate(makeSchedulingHashInfo(now, "active-c", "other-hash", "1"))
				if moved := cq.handleInadmissibleHash("blocked-hash", "dummy-reason"); moved != 2 {
					t.Fatalf("handleInadmissibleHash() moved = %d, want 2", moved)
				}
			},
			wantActiveCounts:       map[workload.EquivalenceHash]int{"other-hash": 1},
			wantInadmissibleCounts: map[workload.EquivalenceHash]int{"blocked-hash": 2},
			wantActive:             1,
			wantInadmissible:       1,
			wantUnion:              2,
		},
		"counts inflight hash shared with an inadmissible workload once in the union": {
			mutate: func(t *testing.T, cq *ClusterQueue) {
				cq.PushOrUpdate(makeSchedulingHashInfo(now, "popped", "shared-hash", "1"))
				if popped := cq.Pop(); popped == nil {
					t.Fatal("expected one workload to be inflight")
				}
				inadmissibleWl := makeSchedulingHashInfo(now, "inadmissible", "shared-hash", "1")
				cq.insertInadmissible(workloadKey(inadmissibleWl), inadmissibleWl)
			},
			wantInadmissibleCounts: map[workload.EquivalenceHash]int{"shared-hash": 1},
			wantActive:             1,
			wantInadmissible:       1,
			wantUnion:              1,
		},
		"clears inflight hash when admitted workload is deleted": {
			mutate: func(t *testing.T, cq *ClusterQueue) {
				cq.PushOrUpdate(makeSchedulingHashInfo(now, "only", "hash-a", "1"))
				popped := cq.Pop()
				if popped == nil {
					t.Fatal("expected one workload to be inflight")
				}
				cq.Delete(log, workloadKey(popped))
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cq := newClusterQueueImpl(ctx, nil, defaultOrdering, testingclock.NewFakeClock(now))
			tc.mutate(t, cq)

			if diff := cmp.Diff(tc.wantActiveCounts, cq.schedulingHashes.active, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("active hash counts (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantInadmissibleCounts, cq.schedulingHashes.inadmissible, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("inadmissible hash counts (-want,+got):\n%s", diff)
			}
			active, inadmissible := cq.PendingSchedulingHashes()
			if active != tc.wantActive || inadmissible != tc.wantInadmissible {
				t.Errorf("PendingSchedulingHashes() active=%d inadmissible=%d, want active=%d inadmissible=%d", active, inadmissible, tc.wantActive, tc.wantInadmissible)
			}
			active, inadmissible = cq.pendingSchedulingHashesForInactiveClusterQueue()
			if active != 0 || inadmissible != tc.wantUnion {
				t.Errorf("pendingSchedulingHashesForInactiveClusterQueue() active=%d inadmissible=%d, want active=0 inadmissible=%d", active, inadmissible, tc.wantUnion)
			}
		})
	}
}

func TestReportCQPendingSchedulingHashesInactiveClusterQueue(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.SchedulingEquivalenceHashing, true)

	ctx, _ := utiltesting.ContextWithLog(t)
	now := time.Now()
	cq := newClusterQueueImpl(ctx, nil, defaultOrdering, testingclock.NewFakeClock(now))
	cq.name = "stopped-cq"

	cq.PushOrUpdate(makeSchedulingHashInfo(now, "active-shared", "shared-hash", "1"))
	if popped := cq.Pop(); popped == nil {
		t.Fatal("expected one workload to be inflight")
	}
	cq.PushOrUpdate(makeSchedulingHashInfo(now, "active-only", "active-hash", "1"))
	inadmissibleShared := makeSchedulingHashInfo(now, "inadmissible-shared", "shared-hash", "1")
	cq.insertInadmissible(workloadKey(inadmissibleShared), inadmissibleShared)
	inadmissibleOnly := makeSchedulingHashInfo(now, "inadmissible-only", "inadmissible-hash", "1")
	cq.insertInadmissible(workloadKey(inadmissibleOnly), inadmissibleOnly)

	m := NewManagerForUnitTests(nil, &fakeStatusChecker{}, WithCustomLabels(kueuemetrics.NewCustomLabels(nil)))
	kueuemetrics.ClearClusterQueueMetrics(cq.name)
	t.Cleanup(func() {
		kueuemetrics.ClearClusterQueueMetrics(cq.name)
	})

	reportCQPendingWorkloads(m, cq)

	gotActive := promtestutil.ToFloat64(kueuemetrics.PendingSchedulingHashes.WithLabelValues(string(cq.name), kueuemetrics.PendingStatusActive, roletracker.RoleStandalone))
	if gotActive != 0 {
		t.Fatalf("PendingSchedulingHashes active = %v, want 0", gotActive)
	}
	gotInadmissible := promtestutil.ToFloat64(kueuemetrics.PendingSchedulingHashes.WithLabelValues(string(cq.name), kueuemetrics.PendingStatusInadmissible, roletracker.RoleStandalone))
	if gotInadmissible != 3 {
		t.Fatalf("PendingSchedulingHashes inadmissible = %v, want 3", gotInadmissible)
	}
}

func TestSchedulingHashCountsDuplicateBucketTransitions(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.SchedulingEquivalenceHashing, true)

	ctx, log := utiltesting.ContextWithLog(t)
	now := time.Now()
	tests := map[string]struct {
		transition             func(t *testing.T, cq *ClusterQueue, oldInfo, currentInfo *workload.Info)
		wantActive             int
		wantInadmissible       int
		wantActiveCounts       map[workload.EquivalenceHash]int
		wantInadmissibleCounts map[workload.EquivalenceHash]int
		wantCPU                bool
	}{
		"queue inadmissible workloads": {
			transition: func(t *testing.T, cq *ClusterQueue, _, _ *workload.Info) {
				cq.namespaceSelector = labels.Everything()
				moved := queueInadmissibleWorkloads(ctx, cq, utiltesting.NewFakeClient(utiltesting.MakeNamespace(defaultNamespace)))
				if moved != 1 {
					t.Fatalf("queueInadmissibleWorkloads() = %d, want 1", moved)
				}
			},
			wantActive:       1,
			wantActiveCounts: map[workload.EquivalenceHash]int{"current-hash": 1},
			wantCPU:          true,
		},
		"delete": {
			transition: func(t *testing.T, cq *ClusterQueue, _, currentInfo *workload.Info) {
				cq.Delete(log, workloadKey(currentInfo))
			},
		},
		"bulk move to inadmissible": {
			transition: func(t *testing.T, cq *ClusterQueue, _, _ *workload.Info) {
				cq.queueingStrategy = kueue.BestEffortFIFO
				if moved := cq.handleInadmissibleHash("current-hash", "dummy-reason"); moved != 1 {
					t.Fatalf("handleInadmissibleHash() = %d, want 1", moved)
				}
			},
			wantInadmissible:       1,
			wantInadmissibleCounts: map[workload.EquivalenceHash]int{"current-hash": 1},
			wantCPU:                true,
		},
		"immediate requeue": {
			transition: func(t *testing.T, cq *ClusterQueue, _, currentInfo *workload.Info) {
				if pushed := cq.requeueIfNotPresent(log, currentInfo, true, RequeueReasonGeneric, ""); pushed {
					t.Fatal("requeueIfNotPresent() = true, want false for workload already in heap")
				}
			},
			wantActive:       1,
			wantActiveCounts: map[workload.EquivalenceHash]int{"current-hash": 1},
			wantCPU:          true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cq := newClusterQueueImpl(ctx, nil, defaultOrdering, testingclock.NewFakeClock(now))
			oldInfo := makeSchedulingHashInfo(now, "workload", "old-hash", "1")
			currentInfo := makeSchedulingHashInfo(now, "workload", "current-hash", "2")
			cq.insertInadmissible(workloadKey(oldInfo), oldInfo)
			lq := &LocalQueue{items: map[workload.Reference]*workload.Info{
				workloadKey(currentInfo): currentInfo,
			}}
			if added := cq.AddFromLocalQueue(lq, nil, kueuemetrics.NewCustomLabels(nil)); !added {
				t.Fatal("AddFromLocalQueue() = false, want true")
			}

			tc.transition(t, cq, oldInfo, currentInfo)

			active, inadmissible := cq.Pending()
			if active != tc.wantActive || inadmissible != tc.wantInadmissible {
				t.Errorf("Pending() active=%d inadmissible=%d, want active=%d inadmissible=%d", active, inadmissible, tc.wantActive, tc.wantInadmissible)
			}
			if diff := cmp.Diff(tc.wantActiveCounts, cq.schedulingHashes.active, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("active hash counts (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantInadmissibleCounts, cq.schedulingHashes.inadmissible, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("inadmissible hash counts (-want,+got):\n%s", diff)
			}
			active, inadmissible = cq.PendingSchedulingHashes()
			if active != tc.wantActive || inadmissible != tc.wantInadmissible {
				t.Errorf("PendingSchedulingHashes() active=%d inadmissible=%d, want active=%d inadmissible=%d", active, inadmissible, tc.wantActive, tc.wantInadmissible)
			}
			wantCPU := int64(0)
			if tc.wantCPU {
				wantCPU = totalCPURequest(currentInfo)
			}
			if gotCPU := cq.pendingResources()[corev1.ResourceCPU]; gotCPU != wantCPU {
				t.Errorf("pending CPU = %d, want %d", gotCPU, wantCPU)
			}
		})
	}
}
