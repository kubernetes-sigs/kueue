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

package core

import (
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestLocalQueueCreateReportsPendingWorkloadsWithCustomLabels(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.LocalQueueMetrics, true)
	features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, true)
	t.Cleanup(func() { metrics.InitMetricVectors(nil) })
	customLabels := metrics.NewCustomLabels([]config.ControllerMetricsCustomLabel{{
		Name: "team", SourceKind: new(config.SourceKindLocalQueue),
	}})
	cq := utiltestingapi.MakeClusterQueue("cq").Obj()
	lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Label("team", "alpha").Obj()
	wl := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").Obj()
	cl := utiltesting.NewClientBuilder().WithObjects(cq, wl).
		WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).
		Build()
	ctx, log := utiltesting.ContextWithLog(t)
	cqCache := schdcache.New(cl, schdcache.WithCustomLabels(customLabels))
	if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Adding ClusterQueue to cache: %v", err)
	}
	qManager := qcache.NewManagerForUnitTests(cl, nil, qcache.WithCustomLabels(customLabels))
	if err := qManager.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Adding ClusterQueue to queue manager: %v", err)
	}
	if err := cl.Create(ctx, lq); err != nil {
		t.Fatalf("Creating LocalQueue: %v", err)
	}
	// Workload events can arrive before the LocalQueue event during startup.
	if err := qManager.AddOrUpdateWorkload(log, wl); !errors.Is(err, qcache.ErrLocalQueueDoesNotExistOrInactive) {
		t.Fatalf("Adding Workload before LocalQueue: got %v, want %v", err, qcache.ErrLocalQueueDoesNotExistOrInactive)
	}
	reconciler := NewLocalQueueReconciler(cl, qManager, cqCache, WithCustomLabels(customLabels))
	if !reconciler.Create(event.TypedCreateEvent[*kueue.LocalQueue]{Object: lq}) {
		t.Fatal("LocalQueue create event was rejected")
	}
	if pending, err := qManager.PendingWorkloads(lq); err != nil || pending != 1 {
		t.Fatalf("Pending workloads after LocalQueue creation: got (%d, %v), want (1, nil)", pending, err)
	}
	got := testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueuePendingWorkloads, map[string]string{
		"name": lq.Name, "namespace": lq.Namespace,
	})
	if len(got) != 2 {
		t.Fatalf("Expected active and inadmissible pending series, got %v", got)
	}
	for _, point := range got {
		if point.Labels["custom_team"] != "alpha" {
			t.Errorf("Pending metric has incorrect custom labels: %v", point)
		}
		var want float64
		switch point.Labels["status"] {
		case metrics.PendingStatusActive:
			want = 1
		case metrics.PendingStatusInadmissible:
		default:
			t.Errorf("Unexpected pending status: %v", point)
		}
		if point.Value != want {
			t.Errorf("Pending metric %v: got %v, want %v", point.Labels, point.Value, want)
		}
	}
}
