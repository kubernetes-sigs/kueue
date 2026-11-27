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
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
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

	teamLabels := func(team string) map[string]string {
		return map[string]string{"cluster_queue": "cq1", "custom_team": team}
	}

	alpha := teamLabels("alpha")
	expectGaugeCount(t, metrics.ClusterQueueByStatus, len(metrics.CQStatuses), alpha)
	expectGaugeCount(t, metrics.ClusterQueueResourceNominalQuota, 1, alpha)

	customLabels.CQStore("cq1", map[string]string{"team": "beta"}, nil)
	updatedCQ := cq.DeepCopy()
	updatedCQ.Labels["team"] = "beta"
	if err := cache.UpdateClusterQueue(log, updatedCQ); err != nil {
		t.Fatalf("Failed to update cluster queue: %v", err)
	}

	metrics.ClearClusterQueueMetrics("cq1")
	metrics.ClearCacheMetrics("cq1")
	metrics.ClearClusterQueueResourceMetrics("cq1")
	cache.ResyncClusterQueueGaugeMetrics("cq1")

	expectGaugeCount(t, metrics.ClusterQueueByStatus, 0, alpha)
	expectGaugeCount(t, metrics.ClusterQueueResourceNominalQuota, 0, alpha)

	beta := teamLabels("beta")
	expectGaugeCount(t, metrics.ClusterQueueByStatus, len(metrics.CQStatuses), beta)
	expectGaugeCount(t, metrics.ClusterQueueResourceNominalQuota, 1, beta)
}

func TestResyncCohortGaugeMetricsUsesUpdatedCustomLabels(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	defer metrics.InitMetricVectors(nil)
	features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, true)

	customLabels := metrics.NewCustomLabels([]configapi.ControllerMetricsCustomLabel{{Name: "team", SourceKind: new(configapi.SourceKindCohort)}})
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

// Two ClusterQueue resyncs hold only the cache read lock, so they can rebuild
// the same ClusterQueue series at the same time. Their rebuilds overlap only
// when they run on separate CPUs, so this needs GOMAXPROCS >= 2 to catch a
// regression.
func TestResyncClusterQueueAdmittedActiveWorkloadsConcurrently(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	defer metrics.InitMetricVectors(nil)

	cache := New(utiltesting.NewFakeClient())

	cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
	cq := utiltestingapi.MakeClusterQueue("cq1").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "100").
				Obj(),
		).Obj()
	if err := cache.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Failed to add cluster queue: %v", err)
	}

	const workloads = 100
	now := time.Now().Truncate(time.Second)
	admission := utiltestingapi.MakeAdmission("cq1").
		PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
			Assignment(corev1.ResourceCPU, "default", "1").
			Obj()).
		Obj()
	for i := range workloads {
		wl := utiltestingapi.MakeWorkload(fmt.Sprintf("wl-%d", i), "ns").Queue("lq").Request(corev1.ResourceCPU, "1").
			ReserveQuotaAt(admission.DeepCopy(), now).AdmittedAt(true, now).Obj()
		if added := cache.AddOrUpdateWorkload(ctx, log, wl); !added {
			t.Fatalf("Workload %q was not added", wl.Name)
		}
	}

	want := []testingmetrics.MetricDataPoint{{
		Labels: map[string]string{"cluster_queue": "cq1", "replica_role": roletracker.RoleStandalone},
		Value:  workloads,
	}}
	for i := range 50 {
		start := make(chan struct{})
		var wg sync.WaitGroup
		for range 2 {
			wg.Go(func() {
				<-start
				cache.ResyncClusterQueueGaugeMetrics("cq1")
			})
		}
		close(start)
		wg.Wait()

		got := testingmetrics.CollectFilteredGaugeVec(metrics.AdmittedActiveWorkloads, map[string]string{"cluster_queue": "cq1"})
		if diff := cmp.Diff(want, got); diff != "" {
			t.Fatalf("Unexpected admitted active workloads after %d concurrent resyncs (-want,+got):\n%s", i+1, diff)
		}
	}
}

// Replays the ClusterQueueReconciler resync after a custom label change, with a
// Workload removed between the controller's clear and the cache rebuild.
func TestResyncClusterQueueAdmittedActiveWorkloadsAfterRemovalBeforeRebuild(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	defer metrics.InitMetricVectors(nil)
	features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, true)

	customLabels := metrics.NewCustomLabels([]configapi.ControllerMetricsCustomLabel{{Name: "team"}})
	cache := New(utiltesting.NewFakeClient(), WithCustomLabels(customLabels))

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

	now := time.Now().Truncate(time.Second)
	admission := utiltestingapi.MakeAdmission("cq1").
		PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
			Assignment(corev1.ResourceCPU, "default", "1").
			Obj()).
		Obj()
	wl1 := utiltestingapi.MakeWorkload("wl1", "ns").Queue("lq").Request(corev1.ResourceCPU, "1").
		ReserveQuotaAt(admission.DeepCopy(), now).AdmittedAt(true, now).Obj()
	wl2 := utiltestingapi.MakeWorkload("wl2", "ns").Queue("lq").Request(corev1.ResourceCPU, "1").
		ReserveQuotaAt(admission.DeepCopy(), now).AdmittedAt(true, now).Obj()
	for _, wl := range []*kueue.Workload{wl1, wl2} {
		if added := cache.AddOrUpdateWorkload(ctx, log, wl); !added {
			t.Fatalf("Workload %q was not added", wl.Name)
		}
	}

	customLabels.CQStore("cq1", map[string]string{"team": "beta"}, nil)
	updatedCQ := cq.DeepCopy()
	updatedCQ.Labels["team"] = "beta"
	if err := cache.UpdateClusterQueue(log, updatedCQ); err != nil {
		t.Fatalf("Failed to update cluster queue: %v", err)
	}
	metrics.ClearClusterQueueMetrics("cq1")
	metrics.ClearCacheMetrics("cq1")
	metrics.ClearClusterQueueResourceMetrics("cq1")
	if _, err := cache.ForgetWorkload(log, wl1); err != nil {
		t.Fatalf("Failed to forget workload: %v", err)
	}
	cache.ResyncClusterQueueGaugeMetrics("cq1")

	want := []testingmetrics.MetricDataPoint{{
		Labels: map[string]string{"cluster_queue": "cq1", "replica_role": roletracker.RoleStandalone, "custom_team": "beta"},
		Value:  1,
	}}
	got := testingmetrics.CollectFilteredGaugeVec(metrics.AdmittedActiveWorkloads, map[string]string{"cluster_queue": "cq1"})
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Unexpected admitted active workloads (-want,+got):\n%s", diff)
	}
}
