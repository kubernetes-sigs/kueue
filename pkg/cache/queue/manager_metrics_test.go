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
	"errors"
	"testing"

	gocmp "github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
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

func TestShouldExposeLocalQueueMetrics(t *testing.T) {
	cases := map[string]struct {
		disableLQMetrics bool
		lqMetrics        *metrics.LocalQueueMetricsConfig
		localQueue       *kueue.LocalQueue
		want             bool
	}{
		"known LocalQueue": {
			localQueue: utiltestingapi.MakeLocalQueue("lq", "ns").Obj(),
			want:       true,
		},
		"unknown LocalQueue": {},
		"LocalQueue matched by localQueueSelector": {
			lqMetrics:  &metrics.LocalQueueMetricsConfig{Enabled: true, QueueSelector: labels.SelectorFromSet(labels.Set{"metrics": "true"})},
			localQueue: utiltestingapi.MakeLocalQueue("lq", "ns").Label("metrics", "true").Obj(),
			want:       true,
		},
		"LocalQueue not matched by localQueueSelector": {
			lqMetrics:  &metrics.LocalQueueMetricsConfig{Enabled: true, QueueSelector: labels.SelectorFromSet(labels.Set{"metrics": "true"})},
			localQueue: utiltestingapi.MakeLocalQueue("lq", "ns").Obj(),
		},
		"LocalQueue metrics disabled in the configuration": {
			lqMetrics:  &metrics.LocalQueueMetricsConfig{},
			localQueue: utiltestingapi.MakeLocalQueue("lq", "ns").Obj(),
		},
		"LocalQueueMetrics feature gate disabled": {
			disableLQMetrics: true,
			localQueue:       utiltestingapi.MakeLocalQueue("lq", "ns").Obj(),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.LocalQueueMetrics, !tc.disableLQMetrics)
			ctx, _ := utiltesting.ContextWithLog(t)
			manager := NewManagerForUnitTests(utiltesting.NewClientBuilder().Build(), nil, WithLocalQueueMetrics(tc.lqMetrics))
			if tc.localQueue != nil {
				if err := manager.AddLocalQueue(ctx, tc.localQueue); err != nil {
					t.Fatal(err)
				}
			}
			if got := manager.ShouldExposeLocalQueueMetrics("ns/lq"); got != tc.want {
				t.Errorf("ShouldExposeLocalQueueMetrics() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUnadmittedWorkloadMetricsAfterClusterQueueRecreate(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.UnadmittedWorkloadsObservability, true)
	cLabels := metrics.NewCustomLabels([]configapi.ControllerMetricsCustomLabel{
		utiltestingapi.MakeCustomLabel("team_cq").SourceLabelKey("team").SourceKind(configapi.SourceKindClusterQueue).Obj(),
	})
	ctx, log := utiltesting.ContextWithLog(t)
	metrics.InitMetricVectors(cLabels)
	t.Cleanup(func() { metrics.InitMetricVectors(nil) })

	cq := utiltestingapi.MakeClusterQueue("cq").Label("team", "alpha").Obj()
	lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue(cq.Name).Obj()
	wl := utiltestingapi.MakeWorkload("wl", "ns").Queue(kueue.LocalQueueName(lq.Name)).Obj()
	cl := utiltesting.NewClientBuilder().WithObjects(utiltesting.MakeNamespace("ns"), lq, wl).
		WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).
		Build()
	manager := NewManagerForUnitTests(cl, nil, WithCustomLabels(cLabels))

	addClusterQueue := func() {
		t.Helper()
		cLabels.CQStore(kueue.ClusterQueueReference(cq.Name), cq.Labels, cq.Annotations)
		if err := manager.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("Adding ClusterQueue: %v", err)
		}
	}
	setUnadmittedCause := func(reason string) {
		t.Helper()
		wl.Status.Conditions = []metav1.Condition{{
			Type:   kueue.WorkloadQuotaReserved,
			Status: metav1.ConditionFalse,
			Reason: reason,
		}}
		manager.UpdateUnadmittedWorkload(log, wl)
	}

	addClusterQueue()
	if err := manager.AddLocalQueue(ctx, lq); err != nil {
		t.Fatalf("Adding LocalQueue: %v", err)
	}
	manager.UpdateUnadmittedWorkload(log, wl)

	// Mirrors the order of the ClusterQueue delete event handler.
	manager.DeleteClusterQueue(log, cq)
	cLabels.CQDelete(kueue.ClusterQueueReference(cq.Name))

	setUnadmittedCause(kueue.WorkloadQuotaReservedReasonMisconfigured)

	addClusterQueue()
	setUnadmittedCause(kueue.WorkloadQuotaReservedReasonPendingEvaluation)

	got := testingmetrics.CollectFilteredGaugeVec(metrics.UnadmittedWorkloads, map[string]string{"cluster_queue": cq.Name})
	want := []testingmetrics.MetricDataPoint{{
		Labels: map[string]string{
			"cluster_queue":    cq.Name,
			"reason":           kueue.WorkloadAdmittedReasonNoReservation,
			"underlying_cause": kueue.WorkloadQuotaReservedReasonPendingEvaluation,
			"replica_role":     "standalone",
			"custom_team_cq":   "alpha",
		},
		Value: 1,
	}}
	if diff := gocmp.Diff(want, got); diff != "" {
		t.Errorf("Unexpected unadmitted workloads series (-want,+got):\n%s", diff)
	}
}

func TestUnadmittedWorkloadMetricsAfterClusterQueueDeleteWithRemainingWorkloads(t *testing.T) {
	retargetLocalQueue := func(cqName kueue.ClusterQueueReference) func(*testing.T, *Manager, client.Client, *kueue.LocalQueue, *kueue.Workload) {
		return func(t *testing.T, manager *Manager, cl client.Client, lq *kueue.LocalQueue, wl *kueue.Workload) {
			ctx, log := utiltesting.ContextWithLog(t)
			lq.Spec.ClusterQueue = cqName
			if err := cl.Update(ctx, lq); err != nil {
				t.Fatalf("Updating LocalQueue: %v", err)
			}
			if err := manager.UpdateLocalQueue(log, lq); err != nil {
				t.Fatalf("Updating LocalQueue in manager: %v", err)
			}
			manager.UpdateUnadmittedWorkload(log, wl)
		}
	}
	cases := map[string]struct {
		moveFirstWorkload func(t *testing.T, manager *Manager, cl client.Client, lq *kueue.LocalQueue, wl *kueue.Workload)
	}{
		"delete workload": {
			moveFirstWorkload: func(t *testing.T, manager *Manager, _ client.Client, _ *kueue.LocalQueue, wl *kueue.Workload) {
				_, log := utiltesting.ContextWithLog(t)
				manager.DeleteAndForgetWorkload(log, workload.Key(wl))
			},
		},
		"reconcile workload not found": {
			moveFirstWorkload: func(t *testing.T, manager *Manager, _ client.Client, _ *kueue.LocalQueue, wl *kueue.Workload) {
				_, log := utiltesting.ContextWithLog(t)
				manager.DeleteWorkload(log, workload.Key(wl))
				manager.RemoveUnadmittedWorkload(log, workload.Key(wl))
			},
		},
		"retarget LocalQueue": {
			moveFirstWorkload: retargetLocalQueue("other-cq"),
		},
		"retarget LocalQueue to a missing ClusterQueue": {
			moveFirstWorkload: retargetLocalQueue("missing-cq"),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.UnadmittedWorkloadsObservability, true)
			cLabels := metrics.NewCustomLabels([]configapi.ControllerMetricsCustomLabel{
				utiltestingapi.MakeCustomLabel("team_cq").SourceLabelKey("team").SourceKind(configapi.SourceKindClusterQueue).Obj(),
			})
			ctx, log := utiltesting.ContextWithLog(t)
			metrics.InitMetricVectors(cLabels)
			t.Cleanup(func() { metrics.InitMetricVectors(nil) })

			cq := utiltestingapi.MakeClusterQueue("cq").Label("team", "alpha").Obj()
			otherCQ := utiltestingapi.MakeClusterQueue("other-cq").Label("team", "beta").Obj()
			lq1 := utiltestingapi.MakeLocalQueue("lq1", "ns").ClusterQueue(cq.Name).Obj()
			lq2 := utiltestingapi.MakeLocalQueue("lq2", "ns").ClusterQueue(cq.Name).Obj()
			wl1 := utiltestingapi.MakeWorkload("wl1", "ns").Queue(kueue.LocalQueueName(lq1.Name)).Obj()
			wl2 := utiltestingapi.MakeWorkload("wl2", "ns").Queue(kueue.LocalQueueName(lq2.Name)).Obj()
			cl := utiltesting.NewClientBuilder().WithObjects(utiltesting.MakeNamespace("ns"), lq1, lq2, wl1, wl2).
				WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).
				Build()
			manager := NewManagerForUnitTests(cl, nil, WithCustomLabels(cLabels))

			addClusterQueue := func(cq *kueue.ClusterQueue) {
				t.Helper()
				cLabels.CQStore(kueue.ClusterQueueReference(cq.Name), cq.Labels, cq.Annotations)
				if err := manager.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Adding ClusterQueue: %v", err)
				}
				manager.ResyncClusterQueueGaugeMetrics(kueue.ClusterQueueReference(cq.Name))
			}
			cqSeries := func() []testingmetrics.MetricDataPoint {
				return testingmetrics.CollectFilteredGaugeVec(metrics.UnadmittedWorkloads, map[string]string{"cluster_queue": cq.Name})
			}

			addClusterQueue(cq)
			addClusterQueue(otherCQ)
			for _, lq := range []*kueue.LocalQueue{lq1, lq2} {
				if err := manager.AddLocalQueue(ctx, lq); err != nil {
					t.Fatalf("Adding LocalQueue: %v", err)
				}
			}
			manager.UpdateUnadmittedWorkload(log, wl1)
			manager.UpdateUnadmittedWorkload(log, wl2)

			// Mirrors the order of the ClusterQueue delete event handler.
			manager.DeleteClusterQueue(log, cq)
			cLabels.CQDelete(kueue.ClusterQueueReference(cq.Name))

			tc.moveFirstWorkload(t, manager, cl, lq1, wl1)

			if got := cqSeries(); len(got) != 0 {
				t.Errorf("Unexpected unadmitted workloads series for the deleted ClusterQueue: %v", got)
			}

			addClusterQueue(cq)

			want := []testingmetrics.MetricDataPoint{{
				Labels: map[string]string{
					"cluster_queue":    cq.Name,
					"reason":           kueue.WorkloadAdmittedReasonNoReservation,
					"underlying_cause": kueue.WorkloadQuotaReservedReasonPendingEvaluation,
					"replica_role":     "standalone",
					"custom_team_cq":   "alpha",
				},
				Value: 1,
			}}
			if diff := gocmp.Diff(want, cqSeries()); diff != "" {
				t.Errorf("Unexpected unadmitted workloads series after ClusterQueue recreation (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestUnadmittedWorkloadMetricsAfterClusterQueueRecreateWithStatusChangedWhileAbsent(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.UnadmittedWorkloadsObservability, true)
	cLabels := metrics.NewCustomLabels([]configapi.ControllerMetricsCustomLabel{
		utiltestingapi.MakeCustomLabel("team_cq").SourceLabelKey("team").SourceKind(configapi.SourceKindClusterQueue).Obj(),
	})
	ctx, log := utiltesting.ContextWithLog(t)
	metrics.InitMetricVectors(cLabels)
	t.Cleanup(func() { metrics.InitMetricVectors(nil) })

	cq := utiltestingapi.MakeClusterQueue("cq-a").Label("team", "alpha").Obj()
	lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue(cq.Name).Obj()
	wl := utiltestingapi.MakeWorkload("wl", "ns").Queue(kueue.LocalQueueName(lq.Name)).Obj()
	cl := utiltesting.NewClientBuilder().WithObjects(utiltesting.MakeNamespace("ns"), lq, wl).
		WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).
		Build()
	manager := NewManagerForUnitTests(cl, nil, WithCustomLabels(cLabels))

	addClusterQueue := func() {
		t.Helper()
		// Mirrors the ClusterQueue create event handler, which stores the custom labels first.
		cLabels.CQStore(kueue.ClusterQueueReference(cq.Name), cq.Labels, cq.Annotations)
		if err := manager.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("Adding ClusterQueue: %v", err)
		}
	}
	cqSeries := func() []testingmetrics.MetricDataPoint {
		return testingmetrics.CollectFilteredGaugeVec(metrics.UnadmittedWorkloads, map[string]string{"cluster_queue": cq.Name})
	}

	addClusterQueue()
	if err := manager.AddLocalQueue(ctx, lq); err != nil {
		t.Fatalf("Adding LocalQueue: %v", err)
	}
	manager.UpdateUnadmittedWorkload(log, wl)

	// Mirrors the order of the ClusterQueue delete event handler.
	manager.DeleteClusterQueue(log, cq)
	cLabels.CQDelete(kueue.ClusterQueueReference(cq.Name))

	// Mirrors the Workload update event handler and reconciler while the ClusterQueue is absent.
	wl.Status.Conditions = []metav1.Condition{{
		Type:   kueue.WorkloadQuotaReserved,
		Status: metav1.ConditionFalse,
		Reason: kueue.WorkloadQuotaReservedReasonMisconfigured,
	}}
	if err := manager.AddOrUpdateWorkload(ctx, log, wl); !errors.Is(err, ErrClusterQueueDoesNotExist) {
		t.Fatalf("Updating Workload while the ClusterQueue is absent: got error %v, want %v", err, ErrClusterQueueDoesNotExist)
	}
	manager.UpdateUnadmittedWorkload(log, wl)
	if got := cqSeries(); len(got) != 0 {
		t.Errorf("Unexpected unadmitted workloads series for the deleted ClusterQueue: %v", got)
	}

	addClusterQueue()

	want := []testingmetrics.MetricDataPoint{{
		Labels: map[string]string{
			"cluster_queue":    cq.Name,
			"reason":           kueue.WorkloadAdmittedReasonNoReservation,
			"underlying_cause": kueue.WorkloadQuotaReservedReasonMisconfigured,
			"replica_role":     "standalone",
			"custom_team_cq":   "alpha",
		},
		Value: 1,
	}}
	if diff := gocmp.Diff(want, cqSeries()); diff != "" {
		t.Errorf("Unexpected unadmitted workloads series after ClusterQueue recreation (-want,+got):\n%s", diff)
	}
}
