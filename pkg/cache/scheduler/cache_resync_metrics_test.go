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

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/queue"
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
	metrics.ClearCacheMetrics("cq1")
	metrics.ClearClusterQueueResourceMetrics("cq1")
	cache.ResyncClusterQueueGaugeMetrics("cq1")

	expectStatus("alpha", 0)
	expectQuota("alpha", 0)
	expectStatus("beta", len(metrics.CQStatuses))
	expectQuota("beta", 1)
}

func TestResyncLocalQueueAdmittedActiveWorkloads(t *testing.T) {
	testCases := map[string]struct {
		// queueSelector selects the LocalQueues that expose metrics. Nil selects
		// all of them.
		queueSelector labels.Selector
		resync        func(logr.Logger, *Cache, *metrics.CustomLabels) error
		want          []testingmetrics.MetricDataPoint
	}{
		// As after a ClusterQueue custom label change.
		"ClusterQueue resync": {
			resync: func(_ logr.Logger, cache *Cache, _ *metrics.CustomLabels) error {
				metrics.ClearClusterQueueMetrics("cq1")
				metrics.ClearCacheMetrics("cq1")
				metrics.ClearClusterQueueResourceMetrics("cq1")
				cache.ResyncClusterQueueGaugeMetrics("cq1")
				return nil
			},
			want: []testingmetrics.MetricDataPoint{
				{
					Labels: map[string]string{"name": "lq", "namespace": "ns", "replica_role": roletracker.RoleStandalone, "custom_team": "alpha"},
					Value:  1,
				},
				{
					Labels: map[string]string{"name": "lq2", "namespace": "ns", "replica_role": roletracker.RoleStandalone, "custom_team": ""},
					Value:  1,
				},
			},
		},
		"LocalQueue resync after a custom label change": {
			resync: func(_ logr.Logger, cache *Cache, customLabels *metrics.CustomLabels) error {
				oldLQ := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq1").Label("team", "alpha").Obj()
				newLQ := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq1").Label("team", "beta").Obj()
				customLabels.LQStore(queue.Key(newLQ), newLQ.GetLabels(), newLQ.GetAnnotations())
				if err := cache.UpdateLocalQueue(oldLQ, newLQ); err != nil {
					return err
				}
				lqRef := metrics.LocalQueueReference{Name: "lq", Namespace: "ns"}
				metrics.ClearLocalQueueMetrics(lqRef)
				metrics.ClearLocalQueueCacheMetrics(lqRef)
				metrics.ClearLocalQueueResourceMetrics(lqRef)
				cache.ResyncLocalQueueGaugeMetrics("cq1", queue.Key(newLQ))
				return nil
			},
			want: []testingmetrics.MetricDataPoint{
				{
					Labels: map[string]string{"name": "lq", "namespace": "ns", "replica_role": roletracker.RoleStandalone, "custom_team": "beta"},
					Value:  1,
				},
				{
					Labels: map[string]string{"name": "lq2", "namespace": "ns", "replica_role": roletracker.RoleStandalone, "custom_team": ""},
					Value:  1,
				},
			},
		},
		// As after a leader election: no series exist under the reported role.
		"global resync with no series": {
			resync: func(log logr.Logger, cache *Cache, _ *metrics.CustomLabels) error {
				metrics.ClearGaugeMetricsForRole(roletracker.RoleStandalone)
				cache.ResyncGaugeMetrics(log)
				return nil
			},
			want: []testingmetrics.MetricDataPoint{
				{
					Labels: map[string]string{"name": "lq", "namespace": "ns", "replica_role": roletracker.RoleStandalone, "custom_team": "alpha"},
					Value:  1,
				},
				{
					Labels: map[string]string{"name": "lq2", "namespace": "ns", "replica_role": roletracker.RoleStandalone, "custom_team": ""},
					Value:  1,
				},
			},
		},
		"LocalQueue resync after the LocalQueue starts to match the metrics selector": {
			queueSelector: labels.SelectorFromSet(labels.Set{"metrics": "on"}),
			resync: func(_ logr.Logger, cache *Cache, customLabels *metrics.CustomLabels) error {
				oldLQ := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq1").Label("team", "alpha").Obj()
				newLQ := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq1").Label("team", "alpha").Label("metrics", "on").Obj()
				customLabels.LQStore(queue.Key(newLQ), newLQ.GetLabels(), newLQ.GetAnnotations())
				if err := cache.UpdateLocalQueue(oldLQ, newLQ); err != nil {
					return err
				}
				lqRef := metrics.LocalQueueReference{Name: "lq", Namespace: "ns"}
				metrics.ClearLocalQueueMetrics(lqRef)
				metrics.ClearLocalQueueCacheMetrics(lqRef)
				metrics.ClearLocalQueueResourceMetrics(lqRef)
				cache.ResyncLocalQueueGaugeMetrics("cq1", queue.Key(newLQ))
				return nil
			},
			// lq2 still does not match the selector, so it has no series.
			want: []testingmetrics.MetricDataPoint{
				{
					Labels: map[string]string{"name": "lq", "namespace": "ns", "replica_role": roletracker.RoleStandalone, "custom_team": "alpha"},
					Value:  1,
				},
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			defer metrics.InitMetricVectors(nil)
			features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, true)
			features.SetFeatureGateDuringTest(t, features.LocalQueueMetrics, true)

			queueSelector := tc.queueSelector
			if queueSelector == nil {
				queueSelector = labels.Everything()
			}
			customLabels := metrics.NewCustomLabels([]configapi.ControllerMetricsCustomLabel{{Name: "team", SourceKind: new(configapi.SourceKindLocalQueue)}})
			cache := New(
				utiltesting.NewFakeClient(),
				WithCustomLabels(customLabels),
				WithLocalQueueMetrics(&metrics.LocalQueueMetricsConfig{
					Enabled:       true,
					QueueSelector: queueSelector,
				}),
			)

			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
			cq := utiltestingapi.MakeClusterQueue("cq1").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Obj()
			if err := cache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("Failed to add cluster queue: %v", err)
			}
			for _, lq := range []*kueue.LocalQueue{
				utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq1").Label("team", "alpha").Obj(),
				utiltestingapi.MakeLocalQueue("lq2", "ns").ClusterQueue("cq1").Obj(),
			} {
				customLabels.LQStore(queue.Key(lq), lq.GetLabels(), lq.GetAnnotations())
				if err := cache.AddLocalQueue(lq); err != nil {
					t.Fatalf("Failed to add local queue: %v", err)
				}
			}

			now := time.Now().Truncate(time.Second)
			admission := utiltestingapi.MakeAdmission("cq1").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "default", "1").
					Obj()).
				Obj()
			for _, wl := range []*kueue.Workload{
				utiltestingapi.MakeWorkload("admitted", "ns").Queue("lq").Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(admission.DeepCopy(), now).AdmittedAt(true, now).Obj(),
				utiltestingapi.MakeWorkload("quota-reserved", "ns").Queue("lq").Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(admission.DeepCopy(), now).Obj(),
				utiltestingapi.MakeWorkload("admitted-lq2", "ns").Queue("lq2").Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(admission.DeepCopy(), now).AdmittedAt(true, now).Obj(),
			} {
				if added := cache.AddOrUpdateWorkload(ctx, log, wl); !added {
					t.Fatalf("Workload %q was not added", wl.Name)
				}
			}

			if err := tc.resync(log, cache, customLabels); err != nil {
				t.Fatalf("Failed to resync: %v", err)
			}

			got := testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueueAdmittedActiveWorkloads, map[string]string{"namespace": "ns"})
			if diff := cmp.Diff(tc.want, got, cmpopts.SortSlices(func(a, b testingmetrics.MetricDataPoint) bool { return a.Less(&b) })); diff != "" {
				t.Errorf("Unexpected local queue admitted active workloads (-want,+got):\n%s", diff)
			}
		})
	}
}

// The ClusterQueue and LocalQueue resyncs hold only the cache read lock, so
// they can rebuild the same LocalQueue series at the same time. Their rebuilds
// overlap only when they run on separate CPUs, so this needs GOMAXPROCS >= 2
// to catch a regression.
func TestResyncLocalQueueAdmittedActiveWorkloadsConcurrently(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	defer metrics.InitMetricVectors(nil)
	features.SetFeatureGateDuringTest(t, features.LocalQueueMetrics, true)

	cache := New(
		utiltesting.NewFakeClient(),
		WithLocalQueueMetrics(&metrics.LocalQueueMetricsConfig{
			Enabled:       true,
			QueueSelector: labels.Everything(),
		}),
	)

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
	if err := cache.AddLocalQueue(utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq1").Obj()); err != nil {
		t.Fatalf("Failed to add local queue: %v", err)
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
		Labels: map[string]string{"name": "lq", "namespace": "ns", "replica_role": roletracker.RoleStandalone},
		Value:  workloads,
	}}
	for i := range 50 {
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Go(func() {
			<-start
			cache.ResyncClusterQueueGaugeMetrics("cq1")
		})
		wg.Go(func() {
			<-start
			cache.ResyncLocalQueueGaugeMetrics("cq1", "ns/lq")
		})
		close(start)
		wg.Wait()

		got := testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueueAdmittedActiveWorkloads, map[string]string{"name": "lq", "namespace": "ns"})
		if diff := cmp.Diff(want, got); diff != "" {
			t.Fatalf("Unexpected local queue admitted active workloads after %d concurrent resyncs (-want,+got):\n%s", i+1, diff)
		}
	}
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
