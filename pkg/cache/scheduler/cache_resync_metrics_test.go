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

	corev1 "k8s.io/api/core/v1"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestResyncClusterQueueGaugeMetricsUsesUpdatedCustomLabels(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	defer metrics.InitMetricVectors(nil)
	features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, true)

	customLabels := metrics.NewCustomLabels([]configapi.ControllerMetricsCustomLabel{{Name: "team"}})
	cache := New(utiltesting.NewFakeClient(), WithCustomLabels(customLabels), WithResourceMetrics(true))

	cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())

	cq := utiltestingapi.MakeClusterQueue("cq1").
		Label("team", "alpha").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "5").
				Obj(),
		).Obj()

	customLabels.CQStore("cq1", cq.GetLabels(), cq.GetAnnotations())
	if err := cache.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Failed to add cluster queue: %v", err)
	}
	cache.ResyncClusterQueueGaugeMetrics("cq1")

	expectStatus := func(team string, count int) {
		t.Helper()
		got := len(testingmetrics.CollectFilteredGaugeVec(metrics.ClusterQueueByStatus, map[string]string{
			"cluster_queue": "cq1",
			"custom_team":   team,
		}))
		if got != count {
			t.Fatalf("Unexpected cluster queue status metric count for team %q: got %d, want %d", team, got, count)
		}
	}
	expectQuota := func(team string, count int) {
		t.Helper()
		got := len(testingmetrics.CollectFilteredGaugeVec(metrics.ClusterQueueResourceNominalQuota, map[string]string{
			"cluster_queue": "cq1",
			"custom_team":   team,
		}))
		if got != count {
			t.Fatalf("Unexpected cluster queue quota metric count for team %q: got %d, want %d", team, got, count)
		}
	}

	expectStatus("alpha", len(metrics.CQStatuses))
	expectQuota("alpha", 1)

	customLabels.CQStore("cq1", map[string]string{"team": "beta"}, nil)
	updatedCQ := cq.DeepCopy()
	updatedCQ.Labels["team"] = "beta"
	if err := cache.UpdateClusterQueue(log, updatedCQ); err != nil {
		t.Fatalf("Failed to update cluster queue: %v", err)
	}

	metrics.ClearClusterQueueMetrics("cq1")
	metrics.ClearClusterQueueMetricsOnLabelChange("cq1")
	metrics.ClearCacheMetrics("cq1")
	metrics.ClearClusterQueueResourceMetrics("cq1")
	cache.ResyncClusterQueueGaugeMetrics("cq1")

	expectStatus("alpha", 0)
	expectQuota("alpha", 0)
	expectStatus("beta", len(metrics.CQStatuses))
	expectQuota("beta", 1)
}

func TestResyncCohortGaugeMetricsUsesUpdatedCustomLabels(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	defer metrics.InitMetricVectors(nil)
	features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, true)

	customLabels := metrics.NewCustomLabels([]configapi.ControllerMetricsCustomLabel{{Name: "team"}})
	cache := New(utiltesting.NewFakeClient(), WithCustomLabels(customLabels), WithFairSharing(true))

	cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())

	cohort := utiltestingapi.MakeCohort("cohort1").Label("team", "alpha").Obj()
	customLabels.CohortStore(kueue.CohortReference("cohort1"), cohort.GetLabels(), cohort.GetAnnotations())
	if err := cache.AddOrUpdateCohort(cohort); err != nil {
		t.Fatalf("Failed to add cohort: %v", err)
	}
	cq := utiltestingapi.MakeClusterQueue("cq1").
		Cohort("cohort1").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "5").
				Obj(),
		).Obj()
	if err := cache.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Failed to add cluster queue: %v", err)
	}

	cache.ResyncCohortGaugeMetrics(log, "cohort1")

	expectQuota := func(team string, count int) {
		t.Helper()
		got := len(testingmetrics.CollectFilteredGaugeVec(metrics.CohortSubtreeQuota, map[string]string{
			"cohort":      "cohort1",
			"custom_team": team,
		}))
		if got != count {
			t.Fatalf("Unexpected cohort subtree quota metric count for team %q: got %d, want %d", team, got, count)
		}
	}
	expectWeightedShare := func(team string, count int) {
		t.Helper()
		got := len(testingmetrics.CollectFilteredGaugeVec(metrics.CohortWeightedShare, map[string]string{
			"cohort":      "cohort1",
			"custom_team": team,
		}))
		if got != count {
			t.Fatalf("Unexpected cohort weighted share metric count for team %q: got %d, want %d", team, got, count)
		}
	}

	expectQuota("alpha", 1)
	expectWeightedShare("alpha", 1)

	customLabels.CohortStore(kueue.CohortReference("cohort1"), map[string]string{"team": "beta"}, nil)
	updatedCohort := cohort.DeepCopy()
	updatedCohort.Labels["team"] = "beta"
	if err := cache.AddOrUpdateCohort(updatedCohort); err != nil {
		t.Fatalf("Failed to update cohort: %v", err)
	}

	metrics.ClearCohortMetrics("cohort1")
	cache.ResyncCohortGaugeMetrics(log, "cohort1")

	expectQuota("alpha", 0)
	expectWeightedShare("alpha", 0)
	expectQuota("beta", 1)
	expectWeightedShare("beta", 1)
}
