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
					ReserveQuotaAt(admission.DeepCopy(), now).AdmittedAt(true, now).PodsReadyAt(true, now).Obj(),
				utiltestingapi.MakeWorkload("quota-reserved", "ns").Queue("lq").Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(admission.DeepCopy(), now).PodsReadyAt(true, now).Obj(),
				utiltestingapi.MakeWorkload("admitted-lq2", "ns").Queue("lq2").Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(admission.DeepCopy(), now).AdmittedAt(true, now).PodsReadyAt(true, now).Obj(),
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
			gotPodsReady := testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueuePodsReadyActiveWorkloads, map[string]string{"namespace": "ns"})
			if diff := cmp.Diff(tc.want, gotPodsReady, cmpopts.SortSlices(func(a, b testingmetrics.MetricDataPoint) bool { return a.Less(&b) })); diff != "" {
				t.Errorf("Unexpected local queue pods ready active workloads (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestResyncPodsReadyActiveWorkloadsWithWorkloadCustomLabelsAndRoleTransition(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	defer metrics.InitMetricVectors(nil)

	electedChan := make(chan struct{})
	roleTracker := roletracker.NewRoleTracker(electedChan)
	roleTracker.OnElected(func() {
		metrics.ClearGaugeMetricsForRole(roletracker.RoleFollower)
	})
	customLabels := metrics.NewCustomLabels([]configapi.ControllerMetricsCustomLabel{
		utiltestingapi.MakeCustomLabel("team").SourceKind(configapi.SourceKindClusterQueue).Obj(),
		utiltestingapi.MakeCustomLabel("lq_team").SourceKind(configapi.SourceKindLocalQueue).Obj(),
		utiltestingapi.MakeCustomLabel("wl_kind").SourceLabelKey("kind").
			SourceKind(configapi.SourceKindWorkload).TrackedValues("training", "inference").Obj(),
	})
	cache := New(
		utiltesting.NewFakeClient(),
		WithCustomLabels(customLabels),
		WithRoleTracker(roleTracker),
		WithLocalQueueMetrics(&metrics.LocalQueueMetricsConfig{
			Enabled:       true,
			QueueSelector: labels.Everything(),
		}),
	)

	cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
	cq := utiltestingapi.MakeClusterQueue("cq1").
		Label("team", "alpha").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10").Obj()).
		Obj()
	customLabels.CQStore("cq1", cq.GetLabels(), cq.GetAnnotations())
	if err := cache.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Failed to add cluster queue: %v", err)
	}
	lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq1").Label("lq_team", "lq-alpha").Obj()
	customLabels.LQStore(queue.Key(lq), lq.GetLabels(), lq.GetAnnotations())
	if err := cache.AddLocalQueue(lq); err != nil {
		t.Fatalf("Failed to add local queue: %v", err)
	}

	now := time.Now().Truncate(time.Second)
	wlRunning := utiltestingapi.MakeWorkload("wl-running", "ns").Queue("lq").Label("kind", "training").
		Request(corev1.ResourceCPU, "1").SimpleReserveQuota("cq1", "default", now).
		AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
	wlFinishedLater := utiltestingapi.MakeWorkload("wl-finished", "ns").Queue("lq").Label("kind", "inference").
		Request(corev1.ResourceCPU, "1").SimpleReserveQuota("cq1", "default", now).
		AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
	for _, wl := range []*kueue.Workload{wlRunning, wlFinishedLater} {
		if !cache.AddOrUpdateWorkload(ctx, log, wl) {
			t.Fatalf("Workload %q was not added", wl.Name)
		}
	}
	// Finish the inference workload before the HA role transition + relabel resync:
	// the zero-count label set should not be recreated under the new role or queue labels.
	cache.AddOrUpdateWorkload(ctx, log, utiltestingapi.MakeWorkload("wl-finished", "ns").Queue("lq").Label("kind", "inference").
		Request(corev1.ResourceCPU, "1").SimpleReserveQuota("cq1", "default", now).
		AdmittedAt(true, now).PodsReadyAt(true, now).FinishedAt(now).Obj())

	// Simulate HA follower -> leader election.
	close(electedChan)
	roleTracker.Start(ctx, log)
	cache.ResyncGaugeMetrics(log)

	if diff := cmp.Diff(
		[]testingmetrics.MetricDataPoint{},
		testingmetrics.CollectFilteredGaugeVec(metrics.PodsReadyActiveWorkloads, map[string]string{"replica_role": roletracker.RoleFollower}),
		cmpopts.EquateEmpty(),
	); diff != "" {
		t.Errorf("Unexpected follower CQ pods ready series after role transition (-want,+got):\n%s", diff)
	}
	if diff := cmp.Diff(
		[]testingmetrics.MetricDataPoint{},
		testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueuePodsReadyActiveWorkloads, map[string]string{"replica_role": roletracker.RoleFollower}),
		cmpopts.EquateEmpty(),
	); diff != "" {
		t.Errorf("Unexpected follower LQ pods ready series after role transition (-want,+got):\n%s", diff)
	}

	wantCQLeader := []testingmetrics.MetricDataPoint{{
		Labels: map[string]string{
			"cluster_queue":  "cq1",
			"replica_role":   roletracker.RoleLeader,
			"custom_team":    "alpha",
			"custom_wl_kind": "training",
		},
		Value: 1,
	}}
	gotCQLeader := testingmetrics.CollectFilteredGaugeVec(metrics.PodsReadyActiveWorkloads, map[string]string{"cluster_queue": "cq1"})
	if diff := cmp.Diff(wantCQLeader, gotCQLeader); diff != "" {
		t.Errorf("Unexpected CQ pods ready series after HA leader election (-want,+got):\n%s", diff)
	}

	wantLQLeader := []testingmetrics.MetricDataPoint{{
		Labels: map[string]string{
			"name":           "lq",
			"namespace":      "ns",
			"replica_role":   roletracker.RoleLeader,
			"custom_lq_team": "lq-alpha",
			"custom_wl_kind": "training",
		},
		Value: 1,
	}}
	gotLQLeader := testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueuePodsReadyActiveWorkloads, map[string]string{"name": "lq", "namespace": "ns"})
	if diff := cmp.Diff(wantLQLeader, gotLQLeader); diff != "" {
		t.Errorf("Unexpected LQ pods ready series after HA leader election (-want,+got):\n%s", diff)
	}

	// Trigger a normal workload event after the HA role transition and verify the
	// finished "inference" label set is NOT resurrected under RoleLeader.
	wlSecondTraining := utiltestingapi.MakeWorkload("wl-training-2", "ns").Queue("lq").Label("kind", "training").
		Request(corev1.ResourceCPU, "1").SimpleReserveQuota("cq1", "default", now).
		AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
	if !cache.AddOrUpdateWorkload(ctx, log, wlSecondTraining) {
		t.Fatal("Workload wl-training-2 was not added")
	}
	wantCQLeaderAfterAdd := []testingmetrics.MetricDataPoint{{
		Labels: map[string]string{
			"cluster_queue":  "cq1",
			"replica_role":   roletracker.RoleLeader,
			"custom_team":    "alpha",
			"custom_wl_kind": "training",
		},
		Value: 2,
	}}
	if diff := cmp.Diff(wantCQLeaderAfterAdd, testingmetrics.CollectFilteredGaugeVec(metrics.PodsReadyActiveWorkloads, map[string]string{"cluster_queue": "cq1"})); diff != "" {
		t.Errorf("Unexpected CQ pods ready series after subsequent workload event under RoleLeader (-want,+got):\n%s", diff)
	}
	wantLQLeaderAfterAdd := []testingmetrics.MetricDataPoint{{
		Labels: map[string]string{
			"name":           "lq",
			"namespace":      "ns",
			"replica_role":   roletracker.RoleLeader,
			"custom_lq_team": "lq-alpha",
			"custom_wl_kind": "training",
		},
		Value: 2,
	}}
	if diff := cmp.Diff(wantLQLeaderAfterAdd, testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueuePodsReadyActiveWorkloads, map[string]string{"name": "lq", "namespace": "ns"})); diff != "" {
		t.Errorf("Unexpected LQ pods ready series after subsequent workload event under RoleLeader (-want,+got):\n%s", diff)
	}

	// Relabel CQ and LQ, resync, then delete one training workload so reportActiveWorkloads
	// runs again: the historical "inference" label set must still not appear under "beta" or "lq-beta".
	newCQ := cq.DeepCopy()
	newCQ.Labels["team"] = "beta"
	customLabels.CQStore("cq1", newCQ.GetLabels(), newCQ.GetAnnotations())
	if err := cache.UpdateClusterQueue(log, newCQ); err != nil {
		t.Fatalf("Failed to update cluster queue: %v", err)
	}
	metrics.ClearClusterQueueMetrics("cq1")
	metrics.ClearCacheMetrics("cq1")
	metrics.ClearClusterQueueResourceMetrics("cq1")
	cache.ResyncClusterQueueGaugeMetrics("cq1")

	newLQ := lq.DeepCopy()
	newLQ.Labels["lq_team"] = "lq-beta"
	customLabels.LQStore(queue.Key(newLQ), newLQ.GetLabels(), newLQ.GetAnnotations())
	if err := cache.UpdateLocalQueue(lq, newLQ); err != nil {
		t.Fatalf("Failed to update local queue: %v", err)
	}
	cache.ResyncLocalQueueGaugeMetrics("cq1", queue.Key(newLQ))

	if err := cache.DeleteWorkload(log, "ns/wl-training-2"); err != nil {
		t.Fatalf("Deleting wl-training-2 failed: %v", err)
	}

	wantCQRelabeled := []testingmetrics.MetricDataPoint{{
		Labels: map[string]string{
			"cluster_queue":  "cq1",
			"replica_role":   roletracker.RoleLeader,
			"custom_team":    "beta",
			"custom_wl_kind": "training",
		},
		Value: 1,
	}}
	if diff := cmp.Diff(wantCQRelabeled, testingmetrics.CollectFilteredGaugeVec(metrics.PodsReadyActiveWorkloads, map[string]string{"cluster_queue": "cq1"})); diff != "" {
		t.Errorf("Unexpected CQ pods ready series after CQ relabel and workload event (-want,+got):\n%s", diff)
	}

	wantLQRelabeled := []testingmetrics.MetricDataPoint{{
		Labels: map[string]string{
			"name":           "lq",
			"namespace":      "ns",
			"replica_role":   roletracker.RoleLeader,
			"custom_lq_team": "lq-beta",
			"custom_wl_kind": "training",
		},
		Value: 1,
	}}
	gotLQRelabeled := testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueuePodsReadyActiveWorkloads, map[string]string{"name": "lq", "namespace": "ns"})
	if diff := cmp.Diff(wantLQRelabeled, gotLQRelabeled); diff != "" {
		t.Errorf("Unexpected LQ pods ready series after LQ relabel and workload event (-want,+got):\n%s", diff)
	}
}

// The ClusterQueue and LocalQueue resyncs hold only the cache read lock, so
// they can rebuild the same ClusterQueue and LocalQueue series at the same time.
// Their rebuilds overlap only when they run on separate CPUs, so this needs
// GOMAXPROCS >= 2 to catch a regression.
func TestResyncAdmittedActiveWorkloadsConcurrently(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	defer metrics.InitMetricVectors(nil)

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
			ReserveQuotaAt(admission.DeepCopy(), now).AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
		if added := cache.AddOrUpdateWorkload(ctx, log, wl); !added {
			t.Fatalf("Workload %q was not added", wl.Name)
		}
	}

	wantCQ := []testingmetrics.MetricDataPoint{{
		Labels: map[string]string{"cluster_queue": "cq1", "replica_role": roletracker.RoleStandalone},
		Value:  workloads,
	}}
	want := []testingmetrics.MetricDataPoint{{
		Labels: map[string]string{"name": "lq", "namespace": "ns", "replica_role": roletracker.RoleStandalone},
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
		wg.Go(func() {
			<-start
			cache.ResyncLocalQueueGaugeMetrics("cq1", "ns/lq")
		})
		close(start)
		wg.Wait()

		gotCQ := testingmetrics.CollectFilteredGaugeVec(metrics.AdmittedActiveWorkloads, map[string]string{"cluster_queue": "cq1"})
		if diff := cmp.Diff(wantCQ, gotCQ); diff != "" {
			t.Fatalf("Unexpected admitted active workloads after %d concurrent resyncs (-want,+got):\n%s", i+1, diff)
		}
		gotCQPodsReady := testingmetrics.CollectFilteredGaugeVec(metrics.PodsReadyActiveWorkloads, map[string]string{"cluster_queue": "cq1"})
		if diff := cmp.Diff(wantCQ, gotCQPodsReady); diff != "" {
			t.Fatalf("Unexpected pods ready active workloads after %d concurrent resyncs (-want,+got):\n%s", i+1, diff)
		}
		got := testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueueAdmittedActiveWorkloads, map[string]string{"name": "lq", "namespace": "ns"})
		if diff := cmp.Diff(want, got); diff != "" {
			t.Fatalf("Unexpected local queue admitted active workloads after %d concurrent resyncs (-want,+got):\n%s", i+1, diff)
		}
		gotPodsReady := testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueuePodsReadyActiveWorkloads, map[string]string{"name": "lq", "namespace": "ns"})
		if diff := cmp.Diff(want, gotPodsReady); diff != "" {
			t.Fatalf("Unexpected local queue pods ready active workloads after %d concurrent resyncs (-want,+got):\n%s", i+1, diff)
		}
	}
}

func TestResyncCohortGaugeMetricsUsesUpdatedCustomLabels(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	defer metrics.InitMetricVectors(nil)

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

// Replays the ClusterQueueReconciler resync after a custom label change, with a
// Workload removed between the controller's clear and the cache rebuild.
func TestResyncClusterQueueAdmittedActiveWorkloadsAfterRemovalBeforeRebuild(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	defer metrics.InitMetricVectors(nil)

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
		ReserveQuotaAt(admission.DeepCopy(), now).AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
	wl2 := utiltestingapi.MakeWorkload("wl2", "ns").Queue("lq").Request(corev1.ResourceCPU, "1").
		ReserveQuotaAt(admission.DeepCopy(), now).AdmittedAt(true, now).PodsReadyAt(true, now).Obj()
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
	gotPodsReady := testingmetrics.CollectFilteredGaugeVec(metrics.PodsReadyActiveWorkloads, map[string]string{"cluster_queue": "cq1"})
	if diff := cmp.Diff(want, gotPodsReady); diff != "" {
		t.Errorf("Unexpected pods ready active workloads (-want,+got):\n%s", diff)
	}
}
