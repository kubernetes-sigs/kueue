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

func TestLocalQueueRecorderForWorkload(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	reserved := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").ReserveQuotaAt(utiltestingapi.MakeAdmission("cq1").Obj(), now)
	cases := map[string]struct {
		disableLQMetrics     bool
		lqMetrics            *metrics.LocalQueueMetricsConfig
		workload             *kueue.Workload
		moveLocalQueue       bool
		recreateClusterQueue bool
		deleteLocalQueue     bool
		recreateLocalQueue   bool
		wantRecorder         bool
		wantRecorded         bool
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
			wantRecorder: true,
			wantRecorded: true,
		},
		"LocalQueue deleted after the check": {
			workload:         reserved.Clone().Obj(),
			deleteLocalQueue: true,
			wantRecorder:     true,
		},
		"LocalQueue moved to another ClusterQueue after the check": {
			workload:       reserved.Clone().Obj(),
			moveLocalQueue: true,
			wantRecorder:   true,
			wantRecorded:   true,
		},
		"LocalQueue moved to another ClusterQueue, then deleted": {
			workload:         reserved.Clone().Obj(),
			moveLocalQueue:   true,
			deleteLocalQueue: true,
			wantRecorder:     true,
		},
		"ClusterQueue re-created after the check": {
			workload:             reserved.Clone().Obj(),
			recreateClusterQueue: true,
			wantRecorder:         true,
			wantRecorded:         true,
		},
		"ClusterQueue re-created, then LocalQueue deleted": {
			workload:             reserved.Clone().Obj(),
			recreateClusterQueue: true,
			deleteLocalQueue:     true,
			wantRecorder:         true,
		},
		"LocalQueue deleted and created again": {
			workload:           reserved.Clone().Obj(),
			deleteLocalQueue:   true,
			recreateLocalQueue: true,
			wantRecorder:       true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.LocalQueueMetrics, !tc.disableLQMetrics)
			ctx, log := utiltesting.ContextWithLog(t)
			cq := utiltestingapi.MakeClusterQueue("cq1").Obj()
			lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq1").Obj()
			cache := New(utiltesting.NewFakeClient(lq), WithLocalQueueMetrics(tc.lqMetrics))
			if err := cache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("Failed to add ClusterQueue: %v", err)
			}
			if err := cache.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue("cq2").Obj()); err != nil {
				t.Fatalf("Failed to add ClusterQueue: %v", err)
			}

			recorder := cache.LocalQueueRecorderForWorkload(log, tc.workload)
			if got := recorder != nil; got != tc.wantRecorder {
				t.Fatalf("LocalQueueRecorderForWorkload() returned a recorder = %v, want %v", got, tc.wantRecorder)
			}
			if tc.moveLocalQueue {
				moved := lq.DeepCopy()
				moved.Spec.ClusterQueue = "cq2"
				if err := cache.UpdateLocalQueue(lq, moved); err != nil {
					t.Fatalf("Failed to update LocalQueue: %v", err)
				}
				lq = moved
			}
			if tc.recreateClusterQueue {
				cache.DeleteClusterQueue(cq)
				if err := cache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Failed to add ClusterQueue: %v", err)
				}
			}
			if tc.deleteLocalQueue {
				cache.DeleteLocalQueue(lq)
			}
			if tc.recreateLocalQueue {
				if err := cache.AddLocalQueue(lq); err != nil {
					t.Fatalf("Failed to add LocalQueue: %v", err)
				}
				if got := cache.LocalQueueRecorderForWorkload(log, tc.workload); got == nil || got == recorder {
					t.Errorf("LocalQueueRecorderForWorkload() after re-creating the LocalQueue = %p, want a new recorder", got)
				}
			}
			var recorded bool
			recorder.Record(func() { recorded = true })
			if recorded != tc.wantRecorded {
				t.Errorf("Record called record = %v, want %v", recorded, tc.wantRecorded)
			}
		})
	}
}

// replaceOnMatchSelector is a localQueueSelector that calls onMatch the first time it
// is evaluated after onMatch is set.
type replaceOnMatchSelector struct {
	labels.Selector
	onMatch func()
}

func (s *replaceOnMatchSelector) Matches(l labels.Labels) bool {
	if onMatch := s.onMatch; onMatch != nil {
		s.onMatch = nil
		onMatch()
	}
	return s.Selector.Matches(l)
}

// The lookup must return the recorder of the LocalQueue whose labels it checked, even
// if the LocalQueue is re-created with labels that the selector rejects meanwhile.
func TestLocalQueueRecorderForWorkloadDuringReplacement(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	features.SetFeatureGateDuringTest(t, features.LocalQueueMetrics, true)
	selector := &replaceOnMatchSelector{Selector: labels.SelectorFromSet(labels.Set{"metrics": "true"})}
	cache := New(utiltesting.NewFakeClient(), WithLocalQueueMetrics(&metrics.LocalQueueMetricsConfig{Enabled: true, QueueSelector: selector}))
	if err := cache.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue("cq1").Obj()); err != nil {
		t.Fatalf("Failed to add ClusterQueue: %v", err)
	}
	lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq1").Label("metrics", "true").Obj()
	if err := cache.AddLocalQueue(lq); err != nil {
		t.Fatalf("Failed to add LocalQueue: %v", err)
	}
	checkedRecorder := cache.localQueueRecorders["ns/lq"]

	replaced := make(chan struct{})
	selector.onMatch = func() {
		if cache.TryLock() {
			cache.Unlock()
			t.Error("localQueueSelector was evaluated without the cache lock")
		}
		if !cache.TryRLock() {
			t.Error("localQueueSelector was evaluated under the cache write lock")
		} else {
			cache.RUnlock()
		}
		go func() {
			defer close(replaced)
			cache.DeleteLocalQueue(lq)
			if err := cache.AddLocalQueue(utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq1").Obj()); err != nil {
				t.Errorf("Failed to add LocalQueue: %v", err)
			}
		}()
		utiltesting.WaitForPendingWriter(t, cache, replaced)
	}
	wl := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").ReserveQuotaAt(utiltestingapi.MakeAdmission("cq1").Obj(), time.Now()).Obj()
	recorder := cache.LocalQueueRecorderForWorkload(log, wl)
	if selector.onMatch != nil {
		t.Fatal("LocalQueueRecorderForWorkload did not evaluate localQueueSelector")
	}
	utiltesting.WaitForClose(t, replaced)

	if recorder != checkedRecorder {
		t.Errorf("LocalQueueRecorderForWorkload() = %p, want the recorder of the checked LocalQueue %p", recorder, checkedRecorder)
	}
	var recorded bool
	recorder.Record(func() { recorded = true })
	if recorded {
		t.Error("Record recorded for the deleted LocalQueue")
	}
	if got := cache.LocalQueueRecorderForWorkload(log, wl); got != nil {
		t.Errorf("LocalQueueRecorderForWorkload() for the re-created LocalQueue = %p, want nil", got)
	}
}

// DeleteLocalQueue holds the cache lock until the metrics are cleared. Otherwise a
// replacement recorder could record before the clear and lose its series to it.
func TestDeleteLocalQueueClearsBeforeReplacement(t *testing.T) {
	cases := map[string]struct {
		recreateClusterQueue bool
	}{
		"replacement from AddLocalQueue": {},
		"replacement from AddClusterQueue": {
			recreateClusterQueue: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			features.SetFeatureGateDuringTest(t, features.LocalQueueMetrics, true)
			lqRef := metrics.LocalQueueReference{Name: "lq", Namespace: "ns"}
			metrics.ClearLocalQueueMetrics(lqRef)
			cq := utiltestingapi.MakeClusterQueue("cq1").Obj()
			lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq1").Obj()
			cache := New(utiltesting.NewFakeClient(lq))
			if err := cache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("Failed to add ClusterQueue: %v", err)
			}
			if tc.recreateClusterQueue {
				cache.DeleteClusterQueue(cq)
			}
			wl := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").ReserveQuotaAt(utiltestingapi.MakeAdmission("cq1").Obj(), time.Now()).Obj()

			replaced := make(chan struct{})
			var cleared bool
			cache.localQueueRecorders["ns/lq"].SetClearMetricsForTest(func(lqRef metrics.LocalQueueReference) {
				cleared = true
				lockFree := cache.TryLock()
				if lockFree {
					cache.Unlock()
				}
				go func() {
					defer close(replaced)
					var err error
					if tc.recreateClusterQueue {
						err = cache.AddClusterQueue(ctx, cq)
					} else {
						err = cache.AddLocalQueue(lq)
					}
					if err != nil {
						t.Errorf("Failed to replace the LocalQueue: %v", err)
					}
					cache.LocalQueueRecorderForWorkload(log, wl).Record(func() {
						metrics.LocalQueueQuotaReservedWorkload(lqRef, "", time.Second, nil, nil)
					})
				}()
				if lockFree {
					// Nothing stops the replacement, so let it record before the clear.
					utiltesting.WaitForClose(t, replaced)
				}
				metrics.ClearLocalQueueMetrics(lqRef)
			})
			cache.DeleteLocalQueue(lq)
			if !cleared {
				t.Fatal("DeleteLocalQueue did not clear the metrics through the recorder")
			}
			utiltesting.WaitForClose(t, replaced)

			lqLabels := map[string]string{"name": "lq", "namespace": "ns"}
			if got := testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueueQuotaReservedWorkloadsTotal, lqLabels); len(got) != 1 {
				t.Errorf("LocalQueue quota reserved workloads series of the replacement = %v, want 1", got)
			}
		})
	}
}
