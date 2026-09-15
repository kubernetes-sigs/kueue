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
