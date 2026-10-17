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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestAddLocalQueueReportsPendingMetrics(t *testing.T) {
	cases := map[string]struct {
		enabled         bool
		selector        labels.Selector
		wantLocalMetric bool
	}{
		"matching labels":     {enabled: true, selector: labels.SelectorFromSet(labels.Set{"metrics": "true"}), wantLocalMetric: true},
		"non-matching labels": {enabled: true, selector: labels.SelectorFromSet(labels.Set{"metrics": "false"})},
		"metrics disabled":    {selector: labels.Everything()},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.LocalQueueMetrics, tc.enabled)
			ctx, _ := utiltesting.ContextWithLog(t)
			metrics.InitMetricVectors(nil)
			t.Cleanup(func() { metrics.InitMetricVectors(nil) })
			wl := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").Obj()
			cl := utiltesting.NewClientBuilder().WithObjects(utiltesting.MakeNamespace("ns"), wl).
				WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).Build()
			manager := NewManagerForUnitTests(cl, nil, WithLocalQueueMetrics(&metrics.LocalQueueMetricsConfig{Enabled: tc.enabled, QueueSelector: tc.selector}))
			if err := manager.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue("cq").Obj()); err != nil {
				t.Fatal(err)
			}
			lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Label("metrics", "true").Obj()
			if err := manager.AddLocalQueue(ctx, lq); err != nil {
				t.Fatal(err)
			}
			if got := len(manager.PendingWorkloadsInfo("cq")); got != 1 {
				t.Fatalf("pending workloads = %d, want 1", got)
			}
			cqMetrics := testingmetrics.CollectFilteredGaugeVec(metrics.PendingWorkloads, map[string]string{"cluster_queue": "cq", "status": metrics.PendingStatusActive})
			if len(cqMetrics) != 1 || cqMetrics[0].Value != 1 {
				t.Errorf("ClusterQueue pending metrics = %v, want one series with value 1", cqMetrics)
			}
			lqMetrics := testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueuePendingWorkloads, map[string]string{"name": "lq", "namespace": "ns"})
			if !tc.wantLocalMetric {
				if len(lqMetrics) != 0 {
					t.Errorf("LocalQueue pending metrics = %v, want no series", lqMetrics)
				}
				return
			}
			activeMetrics := testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueuePendingWorkloads, map[string]string{"name": "lq", "namespace": "ns", "status": metrics.PendingStatusActive})
			if len(activeMetrics) != 1 || activeMetrics[0].Value != 1 {
				t.Errorf("LocalQueue active pending metrics = %v, want one series with value 1", activeMetrics)
			}
		})
	}
}

func TestLocalQueueDeleteAndMoveReportsPendingMetrics(t *testing.T) {
	cases := map[string]struct {
		move             bool
		inadmissible     bool
		missingOldCQ     bool
		missingNewCQ     bool
		disableLQMetrics bool
	}{
		"delete active workload":                  {},
		"delete inadmissible workload":            {inadmissible: true},
		"delete with LocalQueue metrics disabled": {disableLQMetrics: true},
		"move active workload":                    {move: true},
		"move inadmissible workload":              {move: true, inadmissible: true},
		"move with LocalQueue metrics disabled":   {move: true, disableLQMetrics: true},
		"move to a missing ClusterQueue":          {move: true, missingNewCQ: true},
		"move from a missing ClusterQueue":        {move: true, missingOldCQ: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.LocalQueueMetrics, !tc.disableLQMetrics)
			metrics.InitMetricVectors(nil)
			t.Cleanup(func() { metrics.InitMetricVectors(nil) })
			ctx, log := utiltesting.ContextWithLog(t)
			wl := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").Obj()
			cl := utiltesting.NewClientBuilder().WithObjects(utiltesting.MakeNamespace("ns"), wl).
				WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).Build()
			manager := NewManagerForUnitTests(cl, nil)
			for cqName, missing := range map[string]bool{"old": tc.missingOldCQ, "new": tc.missingNewCQ} {
				if !missing {
					if err := manager.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue(cqName).Obj()); err != nil {
						t.Fatal(err)
					}
				}
			}
			lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("old").Obj()
			if err := manager.AddLocalQueue(ctx, lq); err != nil {
				t.Fatal(err)
			}
			if tc.inadmissible {
				heads := manager.Heads(ctx)
				if len(heads) != 1 {
					t.Fatalf("Heads returned %d workloads, want 1", len(heads))
				}
				if !manager.RequeueWorkload(ctx, &heads[0].Info, RequeueReasonGeneric, "") {
					t.Fatal("Failed to requeue workload")
				}
			}
			if tc.move {
				// spec.clusterQueue is immutable through the API, but the manager
				// also supports moving a LocalQueue internally.
				lq.Spec.ClusterQueue = "new"
				if err := manager.UpdateLocalQueue(log, lq); err != nil {
					t.Fatal(err)
				}
			} else {
				manager.DeleteLocalQueue(log, lq)
			}

			for cqName, missing := range map[string]bool{"old": tc.missingOldCQ, "new": tc.missingNewCQ} {
				if missing {
					continue
				}
				var wantPending int
				if tc.move && cqName == "new" {
					wantPending = 1
				}
				if got := len(manager.PendingWorkloadsInfo(kueue.ClusterQueueReference(cqName))); got != wantPending {
					t.Errorf("ClusterQueue %s pending workloads = %d, want %d", cqName, got, wantPending)
				}
				for _, status := range []string{metrics.PendingStatusActive, metrics.PendingStatusInadmissible} {
					var want float64
					if status == metrics.PendingStatusActive {
						want = float64(wantPending)
					}
					got := testingmetrics.CollectFilteredGaugeVec(metrics.PendingWorkloads, map[string]string{"cluster_queue": cqName, "status": status})
					if len(got) != 1 || got[0].Value != want {
						t.Errorf("ClusterQueue %s %s metrics = %v, want one series with value %v", cqName, status, got, want)
					}
				}
			}
			localMetrics := testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueuePendingWorkloads, map[string]string{"name": "lq", "namespace": "ns"})
			if !tc.move || tc.disableLQMetrics {
				if len(localMetrics) != 0 {
					t.Errorf("LocalQueue pending metrics = %v, want no series", localMetrics)
				}
				return
			}
			if len(localMetrics) != 2 {
				t.Fatalf("LocalQueue pending metrics = %v, want active and inadmissible series", localMetrics)
			}
			for _, point := range localMetrics {
				var want float64
				if point.Labels["status"] == metrics.PendingStatusActive && !tc.missingNewCQ {
					want = 1
				}
				if point.Value != want {
					t.Errorf("LocalQueue pending metric %v = %v, want %v", point.Labels, point.Value, want)
				}
			}
		})
	}
}
