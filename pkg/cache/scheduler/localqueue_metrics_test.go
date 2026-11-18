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

package scheduler

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/labels"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestRecordLocalQueueMetricsForWorkload(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	reserved := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now)
	lqRef := metrics.LocalQueueReference{Name: "lq", Namespace: "ns"}
	cases := map[string]struct {
		disableLQMetrics   bool
		lqMetrics          *metrics.LocalQueueMetricsConfig
		workload           *kueue.Workload
		deleteClusterQueue bool
		deleteLocalQueue   bool
		recreateLocalQueue bool
		wantRecorded       bool
		wantSeries         int
	}{
		"LocalQueueMetrics feature gate disabled": {
			disableLQMetrics: true,
			workload:         reserved.Clone().Obj(),
		},
		"Workload without admission": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").Obj(),
		},
		"unknown LocalQueue": {
			workload: reserved.Clone().Queue("other").Obj(),
		},
		"LocalQueue not matched by localQueueSelector": {
			lqMetrics: &metrics.LocalQueueMetricsConfig{Enabled: true, QueueSelector: labels.SelectorFromSet(labels.Set{"metrics": "true"})},
			workload:  reserved.Clone().Obj(),
		},
		"known LocalQueue": {
			workload:     reserved.Clone().Obj(),
			wantRecorded: true,
			wantSeries:   1,
		},
		"LocalQueue deleted": {
			workload:         reserved.Clone().Obj(),
			deleteLocalQueue: true,
		},
		"ClusterQueue deleted, then LocalQueue deleted": {
			workload:           reserved.Clone().Obj(),
			deleteClusterQueue: true,
			deleteLocalQueue:   true,
		},
		"LocalQueue deleted and created again": {
			workload:           reserved.Clone().Obj(),
			deleteLocalQueue:   true,
			recreateLocalQueue: true,
			wantRecorded:       true,
			wantSeries:         1,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.LocalQueueMetrics, !tc.disableLQMetrics)
			metrics.ClearLocalQueueMetrics(lqRef)
			t.Cleanup(func() { metrics.ClearLocalQueueMetrics(lqRef) })
			ctx, log := utiltesting.ContextWithLog(t)
			lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj()
			cache := New(utiltesting.NewFakeClient(lq), WithLocalQueueMetrics(tc.lqMetrics))
			cq := utiltestingapi.MakeClusterQueue("cq").Obj()
			if err := cache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("Failed to add ClusterQueue: %v", err)
			}
			record := func() {
				if cache.TryLock() {
					cache.Unlock()
					t.Error("record runs without the cache read lock")
				}
				metrics.LocalQueueQuotaReservedWorkload(lqRef, "", time.Second, nil, nil)
			}

			// The delete has to clear what was recorded before it.
			cache.RecordLocalQueueMetricsForWorkload(log, tc.workload, record)
			if tc.deleteClusterQueue {
				cache.DeleteClusterQueue(cq)
			}
			if tc.deleteLocalQueue {
				cache.DeleteLocalQueue(lq)
			}
			if tc.recreateLocalQueue {
				if err := cache.AddLocalQueue(lq); err != nil {
					t.Fatalf("Failed to add LocalQueue: %v", err)
				}
			}
			var recorded bool
			cache.RecordLocalQueueMetricsForWorkload(log, tc.workload, func() {
				recorded = true
				record()
			})
			if recorded != tc.wantRecorded {
				t.Errorf("RecordLocalQueueMetricsForWorkload called record = %v, want %v", recorded, tc.wantRecorded)
			}
			if got := testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueueQuotaReservedWorkloadsTotal, map[string]string{"name": "lq", "namespace": "ns"}); len(got) != tc.wantSeries {
				t.Errorf("LocalQueue series = %v, want %d", got, tc.wantSeries)
			}
		})
	}
}
