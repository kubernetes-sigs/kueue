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
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

type pendingWorkloadsStatusChecker struct {
	active bool
}

func (c *pendingWorkloadsStatusChecker) ClusterQueueActive(kueue.ClusterQueueReference) bool {
	return c.active
}

func TestPendingWorkloadsMetricsWithCustomLabels(t *testing.T) {
	cases := map[string]struct {
		active        bool
		requeue       bool
		wantWorkloads map[string]float64
	}{
		"active queue reports active and inadmissible workloads separately": {
			active: true,
			wantWorkloads: map[string]float64{
				"active/kind1": 1, "active/kind2": 1, "inadmissible/kind2": 1,
			},
		},
		"stopped queue merges active and inadmissible workloads with the same labels": {
			wantWorkloads: map[string]float64{
				"inadmissible/kind1": 1, "inadmissible/kind2": 2,
			},
		},
		"resumed queue reports requeued workloads as active": {
			active:  true,
			requeue: true,
			wantWorkloads: map[string]float64{
				"active/kind1": 1, "active/kind2": 2,
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(func() { metrics.InitMetricVectors(nil) })
			ctx, log := utiltesting.ContextWithLog(t)
			customLabels := metrics.NewCustomLabels([]config.ControllerMetricsCustomLabel{
				utiltestingapi.MakeCustomLabel("team_cq").SourceLabelKey("team").SourceKind(config.SourceKindClusterQueue).Obj(),
				utiltestingapi.MakeCustomLabel("wl_kind").SourceLabelKey("workload-kind").SourceKind(config.SourceKindWorkload).TrackedValues("kind1", "kind2").Obj(),
			})
			cq := utiltestingapi.MakeClusterQueue("cq").Label("team", "ml-team").Obj()
			lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue(cq.Name).Obj()
			wl3 := utiltestingapi.MakeWorkload("wl3", "ns").Label("workload-kind", "kind2").Queue("lq").Request(corev1.ResourceCPU, "1").Obj()
			cl := utiltesting.NewFakeClient(utiltesting.MakeNamespace("ns"), wl3)
			checker := &pendingWorkloadsStatusChecker{active: true}
			m, requeuer := NewManagerForUnitTestsWithRequeuer(cl, checker,
				WithCustomLabels(customLabels), WithPreemptionExpectations(preemptexpectations.New()))
			customLabels.CQStore("cq", cq.Labels, cq.Annotations)
			if err := m.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("Adding ClusterQueue: %v", err)
			}
			if err := m.AddLocalQueue(ctx, lq); err != nil {
				t.Fatalf("Adding LocalQueue: %v", err)
			}
			// Finish initialization retries before constructing the inadmissible state.
			requeuer.ProcessRequeues(ctx)
			if err := m.AddOrUpdateWorkload(ctx, log, wl3); err != nil {
				t.Fatalf("Adding workload: %v", err)
			}
			if popped := m.Heads(ctx); len(popped) != 1 {
				t.Fatalf("Expected one workload head, got %d", len(popped))
			}
			if !m.RequeueWorkload(ctx, workload.NewInfo(log, wl3), RequeueReasonGeneric, "") {
				t.Fatal("Expected workload to be requeued")
			}
			for name, kind := range map[string]string{"wl1": "kind1", "wl2": "kind2"} {
				wl := utiltestingapi.MakeWorkload(name, "ns").Label("workload-kind", kind).Queue("lq").Request(corev1.ResourceCPU, "1").Obj()
				if err := cl.Create(ctx, wl); err != nil {
					t.Fatalf("Creating workload: %v", err)
				}
				if err := m.AddOrUpdateWorkload(ctx, log, wl); err != nil {
					t.Fatalf("Adding workload: %v", err)
				}
			}
			if tc.requeue {
				// Report the stopped queue first so the retry must refresh metrics after it resumes.
				checker.active = false
				reportPendingWorkloads(m, "cq")
			}
			checker.active = tc.active
			if tc.requeue {
				requeuer.notifyClusterQueue("cq")
				if moved := requeuer.ProcessRequeues(ctx); moved != 1 {
					t.Fatalf("Expected one inadmissible workload to move, got %d", moved)
				}
			} else {
				reportPendingWorkloads(m, "cq")
			}
			for labelValues, want := range tc.wantWorkloads {
				status, kind, _ := strings.Cut(labelValues, "/")
				got := testingmetrics.CollectFilteredGaugeVec(metrics.PendingWorkloads, map[string]string{
					"cluster_queue": "cq", "replica_role": roletracker.RoleStandalone,
					"custom_team_cq": "ml-team", "status": status, "custom_wl_kind": kind,
				})
				if len(got) != 1 {
					t.Fatalf("Expected one pending workload metric for %s, got %v", labelValues, got)
				}
				if got[0].Value != want {
					t.Errorf("Pending workload metric for %s = %g, want %g", labelValues, got[0].Value, want)
				}
			}
		})
	}
}

// A CQ state change must refresh metrics even when the requeuer moves no
// workloads and no subsequent Workload update triggers another report.
func TestPendingWorkloadsMetricsOnClusterQueueStopResume(t *testing.T) {
	cases := map[string]struct {
		customLabelsEnabled bool
	}{
		"without custom labels": {
			customLabelsEnabled: false,
		},
		"with workload custom labels": {
			customLabelsEnabled: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, tc.customLabelsEnabled)
			t.Cleanup(func() { metrics.InitMetricVectors(nil) })
			ctx, log := utiltesting.ContextWithLog(t)
			var customLabels *metrics.CustomLabels
			if tc.customLabelsEnabled {
				customLabels = metrics.NewCustomLabels([]config.ControllerMetricsCustomLabel{
					utiltestingapi.MakeCustomLabel("wl_kind").SourceLabelKey("workload-kind").SourceKind(config.SourceKindWorkload).TrackedValues("kind1").Obj(),
				})
			}
			cq := utiltestingapi.MakeClusterQueue("cq").Condition(kueue.ClusterQueueActive, metav1.ConditionTrue, "Ready", "Ready").Obj()
			lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue(cq.Name).Obj()
			wl := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").Label("workload-kind", "kind1").Request(corev1.ResourceCPU, "1").Obj()
			cl := utiltesting.NewClientBuilder().WithObjects(utiltesting.MakeNamespace("ns")).
				WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).Build()
			checker := &pendingWorkloadsStatusChecker{active: true}
			m, requeuer := NewManagerForUnitTestsWithRequeuer(cl, checker,
				WithCustomLabels(customLabels), WithPreemptionExpectations(preemptexpectations.New()))
			if err := m.AddClusterQueue(ctx, cq); err != nil {
				t.Fatal(err)
			}
			if err := m.AddLocalQueue(ctx, lq); err != nil {
				t.Fatal(err)
			}
			requeuer.ProcessRequeues(ctx)
			if err := m.AddOrUpdateWorkload(ctx, log, wl); err != nil {
				t.Fatal(err)
			}

			checkMetrics := func(phase string, active, inadmissible float64) {
				t.Helper()
				filter := map[string]string{"cluster_queue": cq.Name, "replica_role": roletracker.RoleStandalone}
				if tc.customLabelsEnabled {
					filter["custom_wl_kind"] = "kind1"
				}
				got := make(map[string]float64)
				for _, point := range testingmetrics.CollectFilteredGaugeVec(metrics.PendingWorkloads, filter) {
					got[point.Labels["status"]] = point.Value
				}
				want := map[string]float64{metrics.PendingStatusActive: active, metrics.PendingStatusInadmissible: inadmissible}
				for status, count := range want {
					// Cleanup of zero-count custom-label series is covered by #16670.
					// Here we verify that CQ updates publish the new positive count.
					if tc.customLabelsEnabled && count == 0 {
						continue
					}
					value, exists := got[status]
					if !exists || value != count {
						t.Errorf("%s pending metrics for %s: got (%g, %t), want %g", phase, status, value, exists, count)
					}
				}
			}
			checkMetrics("initial", 1, 0)
			for _, active := range []bool{false, true} {
				// The CQ controller updates the scheduler cache before the queue manager;
				// the API object's Active condition still reflects the previous state.
				checker.active = active
				policy := kueue.Hold
				condition := metav1.ConditionFalse
				var wantActive, wantInadmissible float64 = 0, 1
				if active {
					policy, condition = kueue.None, metav1.ConditionTrue
					wantActive, wantInadmissible = 1, 0
				}
				cq.Spec.StopPolicy = &policy
				if err := m.UpdateClusterQueue(cq, true); err != nil {
					t.Fatal(err)
				}
				if moved := requeuer.ProcessRequeues(ctx); moved != 0 {
					t.Fatalf("Moved %d workloads, want 0", moved)
				}
				checkMetrics(string(policy)+" spec update", wantActive, wantInadmissible)
				cq.Status.Conditions[0].Status = condition
				if err := m.UpdateClusterQueue(cq, false); err != nil {
					t.Fatal(err)
				}
				checkMetrics(string(policy)+" status update", wantActive, wantInadmissible)
			}
		})
	}
}

func TestPendingWorkloadsMetricsCountEachParentOnce(t *testing.T) {
	cases := map[string]struct {
		customLabelsEnabled        bool
		disableConcurrentAdmission bool
		want                       float64
	}{
		"without custom labels": {
			want: 1,
		},
		"with workload custom labels": {
			customLabelsEnabled: true,
			want:                1,
		},
		"gate disabled, without custom labels": {
			disableConcurrentAdmission: true,
			want:                       3,
		},
		"gate disabled, with workload custom labels": {
			customLabelsEnabled:        true,
			disableConcurrentAdmission: true,
			want:                       3,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ConcurrentAdmission, !tc.disableConcurrentAdmission)
			features.SetFeatureGateDuringTest(t, features.LocalQueueMetrics, true)
			t.Cleanup(func() { metrics.InitMetricVectors(nil) })
			ctx, log := utiltesting.ContextWithLog(t)
			var customLabels *metrics.CustomLabels
			if tc.customLabelsEnabled {
				customLabels = metrics.NewCustomLabels([]config.ControllerMetricsCustomLabel{
					utiltestingapi.MakeCustomLabel("team").SourceLabelKey("team").SourceKind(config.SourceKindWorkload).TrackedValues("a").Obj(),
				})
			}
			cq := utiltestingapi.MakeClusterQueue("cq").Obj()
			lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue(cq.Name).Obj()
			cl := utiltesting.NewClientBuilder().WithObjects(utiltesting.MakeNamespace("ns"), lq).
				WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).Build()
			m := NewManagerForUnitTests(cl, nil, WithCustomLabels(customLabels), WithPreemptionExpectations(preemptexpectations.New()))
			if err := m.AddClusterQueue(ctx, cq); err != nil {
				t.Fatal(err)
			}
			if err := m.AddLocalQueue(ctx, lq); err != nil {
				t.Fatal(err)
			}
			// One Job with three Variants, as created by the ConcurrentAdmission controller.
			for _, flavor := range []string{"reservation", "on-demand", "spot"} {
				variant := utiltestingapi.MakeWorkload("parent-"+flavor, "ns").
					Queue("lq").
					Label("team", "a").
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent", "parent-uid").
					Request(corev1.ResourceCPU, "1").
					Obj()
				if err := m.AddOrUpdateWorkload(ctx, log, variant); err != nil {
					t.Fatal(err)
				}
			}

			checkMetrics := func(phase string, gauge prometheus.Collector, filter map[string]string, status string, want float64) {
				t.Helper()
				filter["status"] = status
				if tc.customLabelsEnabled {
					filter["custom_team"] = "a"
				}
				got := testingmetrics.CollectFilteredGaugeVec(gauge, filter)
				if len(got) != 1 {
					t.Fatalf("[%s] expected one %s metric, got %v", phase, status, got)
				}
				if got[0].Value != want {
					t.Errorf("[%s] %s metric = %g, want %g", phase, status, got[0].Value, want)
				}
			}
			cqFilter := func() map[string]string { return map[string]string{"cluster_queue": cq.Name} }
			lqFilter := func() map[string]string { return map[string]string{"name": lq.Name, "namespace": lq.Namespace} }

			checkMetrics("ClusterQueue", metrics.PendingWorkloads, cqFilter(), metrics.PendingStatusActive, tc.want)
			checkMetrics("LocalQueue", metrics.LocalQueuePendingWorkloads, lqFilter(), metrics.PendingStatusActive, tc.want)

			// Without the ClusterQueue, the LocalQueue metric counts the LocalQueue items.
			m.DeleteClusterQueue(log, cq)
			checkMetrics("LocalQueue after DeleteClusterQueue", metrics.LocalQueuePendingWorkloads, lqFilter(), metrics.PendingStatusInadmissible, tc.want)
		})
	}
}

// The series of a label value that no pending Variant carries any more must be cleared, also when other Variants of the same Parent are still pending.
func TestPendingWorkloadsMetricsClearSeriesOfRemovedVariantLabels(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.ConcurrentAdmission, true)
	features.SetFeatureGateDuringTest(t, features.LocalQueueMetrics, true)
	t.Cleanup(func() { metrics.InitMetricVectors(nil) })
	ctx, log := utiltesting.ContextWithLog(t)
	customLabels := metrics.NewCustomLabels([]config.ControllerMetricsCustomLabel{
		utiltestingapi.MakeCustomLabel("team").SourceLabelKey("team").SourceKind(config.SourceKindWorkload).TrackedValues("a", "b").Obj(),
	})
	cq := utiltestingapi.MakeClusterQueue("cq").Obj()
	lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue(cq.Name).Obj()
	cl := utiltesting.NewClientBuilder().WithObjects(utiltesting.MakeNamespace("ns"), lq).
		WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).Build()
	m := NewManagerForUnitTests(cl, nil, WithCustomLabels(customLabels), WithPreemptionExpectations(preemptexpectations.New()))
	if err := m.AddClusterQueue(ctx, cq); err != nil {
		t.Fatal(err)
	}
	if err := m.AddLocalQueue(ctx, lq); err != nil {
		t.Fatal(err)
	}
	makeVariant := func(name, team string) *kueue.Workload {
		return utiltestingapi.MakeWorkload(name, "ns").
			Queue("lq").
			Label("team", team).
			ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent", "parent-uid").
			Request(corev1.ResourceCPU, "1").
			Obj()
	}
	addOrUpdate := func(wl *kueue.Workload) {
		t.Helper()
		if err := m.AddOrUpdateWorkload(ctx, log, wl); err != nil {
			t.Fatal(err)
		}
	}
	// checkMetrics checks every series of both gauges, keyed by status and team label.
	checkMetrics := func(phase string, want map[string]float64) {
		t.Helper()
		for gaugeName, tc := range map[string]struct {
			gauge  prometheus.Collector
			filter map[string]string
		}{
			"ClusterQueue": {metrics.PendingWorkloads, map[string]string{"cluster_queue": cq.Name}},
			"LocalQueue":   {metrics.LocalQueuePendingWorkloads, map[string]string{"name": lq.Name, "namespace": lq.Namespace}},
		} {
			got := make(map[string]float64)
			for _, point := range testingmetrics.CollectFilteredGaugeVec(tc.gauge, tc.filter) {
				got[point.Labels["status"]+"/"+point.Labels["custom_team"]] = point.Value
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("[%s] %s pending workload series (-want,+got):\n%s", phase, gaugeName, diff)
			}
		}
	}

	addOrUpdate(makeVariant("parent-0", "a"))
	addOrUpdate(makeVariant("parent-1", "b"))
	// Without the Parent Workload, the Parent counts under the smallest label set of its Variants.
	checkMetrics("two labels", map[string]float64{"active/a": 1})

	addOrUpdate(makeVariant("parent-1", "a"))
	checkMetrics("after label update", map[string]float64{"active/a": 1})

	m.DeleteWorkload(log, workload.NewReference("ns", "parent-0"))
	checkMetrics("after deleting one Variant", map[string]float64{"active/a": 1})

	m.DeleteWorkload(log, workload.NewReference("ns", "parent-1"))
	checkMetrics("after deleting the last Variant", map[string]float64{})
}

// parentLabelsMetricsTest runs a queue Manager with one ClusterQueue, one LocalQueue and Workload custom labels, for the pending metrics of ConcurrentAdmission Parents.
type parentLabelsMetricsTest struct {
	t   *testing.T
	ctx context.Context
	log logr.Logger
	m   *Manager
	cq  *kueue.ClusterQueue
	lq  *kueue.LocalQueue
}

func newParentLabelsMetricsTest(t *testing.T) *parentLabelsMetricsTest {
	t.Helper()
	features.SetFeatureGateDuringTest(t, features.ConcurrentAdmission, true)
	features.SetFeatureGateDuringTest(t, features.LocalQueueMetrics, true)
	t.Cleanup(func() { metrics.InitMetricVectors(nil) })
	ctx, log := utiltesting.ContextWithLog(t)
	customLabels := metrics.NewCustomLabels([]config.ControllerMetricsCustomLabel{
		utiltestingapi.MakeCustomLabel("team").SourceLabelKey("team").SourceKind(config.SourceKindWorkload).TrackedValues("a", "b", "c").Obj(),
	})
	cq := utiltestingapi.MakeClusterQueue("cq").Obj()
	lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue(cq.Name).Obj()
	cl := utiltesting.NewClientBuilder().WithObjects(utiltesting.MakeNamespace("ns"), lq).
		WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).Build()
	m := NewManagerForUnitTests(cl, nil, WithCustomLabels(customLabels), WithPreemptionExpectations(preemptexpectations.New()))
	if err := m.AddClusterQueue(ctx, cq); err != nil {
		t.Fatal(err)
	}
	if err := m.AddLocalQueue(ctx, lq); err != nil {
		t.Fatal(err)
	}
	return &parentLabelsMetricsTest{t: t, ctx: ctx, log: log, m: m, cq: cq, lq: lq}
}

func (*parentLabelsMetricsTest) makeParent(queueName, team string) *kueue.Workload {
	return utiltestingapi.MakeWorkload("parent", "ns").
		Queue(kueue.LocalQueueName(queueName)).
		Label("team", team).
		Label(constants.ConcurrentAdmissionParentLabelKey, "true").
		Request(corev1.ResourceCPU, "1").
		Obj()
}

func (*parentLabelsMetricsTest) makeVariant(name, team string, created time.Time) *kueue.Workload {
	return utiltestingapi.MakeWorkload(name, "ns").
		Queue("lq").
		Label("team", team).
		Creation(created).
		ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent", "parent-uid").
		Request(corev1.ResourceCPU, "1").
		Obj()
}

func (f *parentLabelsMetricsTest) addOrUpdate(wl *kueue.Workload) {
	f.t.Helper()
	if err := f.m.AddOrUpdateWorkload(f.ctx, f.log, wl); err != nil {
		f.t.Fatal(err)
	}
}

// checkMetrics checks every series of both gauges, and that the series of each status add up to the total of that status.
func (f *parentLabelsMetricsTest) checkMetrics(phase string, want map[string]float64) {
	f.t.Helper()
	cqImpl := f.m.getClusterQueueLockless(kueue.ClusterQueueReference(f.cq.Name))
	for gaugeName, tc := range map[string]struct {
		gauge   prometheus.Collector
		filter  map[string]string
		pending func() (int, int)
	}{
		"ClusterQueue": {metrics.PendingWorkloads, map[string]string{"cluster_queue": f.cq.Name}, cqImpl.Pending},
		"LocalQueue": {metrics.LocalQueuePendingWorkloads, map[string]string{"name": f.lq.Name, "namespace": f.lq.Namespace}, func() (int, int) {
			return cqImpl.PendingInLocalQueue(utilqueue.Key(f.lq))
		}},
	} {
		got := make(map[string]float64)
		sums := make(map[string]float64)
		for _, point := range testingmetrics.CollectFilteredGaugeVec(tc.gauge, tc.filter) {
			got[point.Labels["status"]+"/"+point.Labels["custom_team"]] = point.Value
			sums[point.Labels["status"]] += point.Value
		}
		if diff := cmp.Diff(want, got); diff != "" {
			f.t.Errorf("[%s] %s pending workload series (-want,+got):\n%s", phase, gaugeName, diff)
		}
		active, inadmissible := tc.pending()
		if sums[metrics.PendingStatusActive] != float64(active) || sums[metrics.PendingStatusInadmissible] != float64(inadmissible) {
			f.t.Errorf(
				"[%s] %s series add up to (%g, %g), want the totals (%d, %d)",
				phase,
				gaugeName,
				sums[metrics.PendingStatusActive],
				sums[metrics.PendingStatusInadmissible],
				active,
				inadmissible,
			)
		}
	}
}

// A Parent counts once under the label values of the Parent Workload, so the per-label series add up to the totals.
func TestPendingWorkloadsMetricsCountParentUnderItsLabels(t *testing.T) {
	f := newParentLabelsMetricsTest(t)
	ctx, log, m := f.ctx, f.log, f.m
	makeParent, makeVariant, addOrUpdate, checkMetrics := f.makeParent, f.makeVariant, f.addOrUpdate, f.checkMetrics
	now := time.Now()

	// The Parent carries a label value that none of its Variants carry.
	m.UpdateConcurrentAdmissionParentLabels(makeParent("lq", "c"))
	addOrUpdate(makeVariant("parent-0", "a", now))
	addOrUpdate(makeVariant("parent-1", "b", now.Add(time.Second)))
	checkMetrics("mixed labels", map[string]float64{"active/c": 1})

	// The scheduler checks out the oldest Variant, and then admits it while the other one stays pending.
	if heads := m.Heads(ctx); len(heads) != 1 || heads[0].Obj.Name != "parent-0" {
		t.Fatalf("Expected parent-0 as the only head, got %v", heads)
	}
	checkMetrics("one Variant inflight", map[string]float64{"active/c": 1})
	m.DeleteWorkload(log, workload.NewReference("ns", "parent-0"))
	checkMetrics("one Variant admitted", map[string]float64{"active/c": 1})

	addOrUpdate(makeVariant("parent-2", "a", now.Add(2*time.Second)))
	m.DeleteWorkload(log, workload.NewReference("ns", "parent-1"))
	checkMetrics("one Variant deleted", map[string]float64{"active/c": 1})

	// A label change on the Parent moves its count, and clears the series of the old value.
	m.UpdateConcurrentAdmissionParentLabels(makeParent("lq", "b"))
	checkMetrics("Parent label changed", map[string]float64{"active/b": 1})

	// The queues that hold the Variants report the change, also after the Parent moved to another LocalQueue.
	m.UpdateConcurrentAdmissionParentLabels(makeParent("other-lq", "a"))
	checkMetrics("Parent label changed in another LocalQueue", map[string]float64{"active/a": 1})

	// Concurrent label updates of the Parent and its Variants settle on the last labels of the Parent.
	var wg sync.WaitGroup
	for i, team := range []string{"a", "b", "b"} {
		name := fmt.Sprintf("parent-%d", i+2)
		wg.Go(func() {
			for _, flip := range []string{"c", "a", "b", team} {
				if err := m.AddOrUpdateWorkload(ctx, log, makeVariant(name, flip, now.Add(time.Duration(i+2)*time.Second))); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Go(func() {
		for _, team := range []string{"a", "b", "c"} {
			m.UpdateConcurrentAdmissionParentLabels(makeParent("lq", team))
		}
	})
	wg.Wait()
	checkMetrics("after concurrent updates", map[string]float64{"active/c": 1})

	// Without the Parent Workload, the Parent counts under the smallest label set of its Variants.
	m.DeleteAndForgetWorkload(log, workload.NewReference("ns", "parent"))
	checkMetrics("Parent unknown", map[string]float64{"active/a": 1})

	for i := range 3 {
		m.DeleteWorkload(log, workload.NewReference("ns", fmt.Sprintf("parent-%d", i+2)))
	}
	checkMetrics("all Variants deleted", map[string]float64{})
}

// The queues that count a Parent also refresh its series when all its Variants are inflight.
func TestPendingWorkloadsMetricsParentWithOnlyInflightVariants(t *testing.T) {
	f := newParentLabelsMetricsTest(t)
	f.m.UpdateConcurrentAdmissionParentLabels(f.makeParent("lq", "c"))
	f.addOrUpdate(f.makeVariant("parent-0", "a", time.Now()))
	f.checkMetrics("one Variant pending", map[string]float64{"active/c": 1})

	// The scheduler checks out the last Variant, so the LocalQueue no longer holds it, while it still counts as pending.
	if heads := f.m.Heads(f.ctx); len(heads) != 1 || heads[0].Obj.Name != "parent-0" {
		t.Fatalf("Expected parent-0 as the only head, got %v", heads)
	}
	f.checkMetrics("the Variant inflight", map[string]float64{"active/c": 1})

	f.m.UpdateConcurrentAdmissionParentLabels(f.makeParent("lq", "b"))
	f.checkMetrics("Parent label changed", map[string]float64{"active/b": 1})

	// Without the Parent Workload, the Parent counts under the label set of its inflight Variant.
	f.m.DeleteAndForgetWorkload(f.log, workload.NewReference("ns", "parent"))
	f.checkMetrics("Parent deleted", map[string]float64{"active/a": 1})
}

// BenchmarkPendingBreakdown measures one read of the labelled pending counts of a ClusterQueue and of its LocalQueue.
// Each Parent has three pending Variants and its own Parent Workload labels.
// The "noVariants" cases hold the same number of plain workloads instead.
func BenchmarkPendingBreakdown(b *testing.B) {
	for _, parents := range []int{10, 100, 1000} {
		for _, variants := range []bool{true, false} {
			name := fmt.Sprintf("parents=%d", parents)
			if !variants {
				name = fmt.Sprintf("noVariants/workloads=%d", parents*3)
			}
			b.Run(name, func(b *testing.B) {
				features.SetFeatureGateDuringTest(b, features.ConcurrentAdmission, true)
				features.SetFeatureGateDuringTest(b, features.CustomMetricLabels, true)
				b.Cleanup(func() { metrics.InitMetricVectors(nil) })
				ctx, log := utiltesting.ContextWithLog(b)
				teams := []string{"a", "b", "c"}
				customLabels := metrics.NewCustomLabels([]config.ControllerMetricsCustomLabel{
					utiltestingapi.MakeCustomLabel("team").SourceLabelKey("team").SourceKind(config.SourceKindWorkload).TrackedValues(teams...).Obj(),
				})
				cq := utiltestingapi.MakeClusterQueue("cq").Obj()
				lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue(cq.Name).Obj()
				cl := utiltesting.NewClientBuilder().WithObjects(utiltesting.MakeNamespace("ns"), lq).
					WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).Build()
				m := NewManagerForUnitTests(cl, nil, WithCustomLabels(customLabels), WithPreemptionExpectations(preemptexpectations.New()))
				if err := m.AddClusterQueue(ctx, cq); err != nil {
					b.Fatal(err)
				}
				if err := m.AddLocalQueue(ctx, lq); err != nil {
					b.Fatal(err)
				}
				now := time.Now()
				for p := range parents {
					parentName := fmt.Sprintf("parent-%d", p)
					if variants {
						m.UpdateConcurrentAdmissionParentLabels(utiltestingapi.MakeWorkload(parentName, "ns").
							Queue("lq").
							Label("team", teams[p%len(teams)]).
							Label(constants.ConcurrentAdmissionParentLabelKey, "true").
							Obj())
					}
					for v := range 3 {
						wl := utiltestingapi.MakeWorkload(fmt.Sprintf("%s-%d", parentName, v), "ns").
							Queue("lq").
							Label("team", teams[(p+v)%len(teams)]).
							Creation(now.Add(time.Duration(p*3+v)*time.Millisecond)).
							Request(corev1.ResourceCPU, "1")
						if variants {
							wl = wl.ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), parentName, parentName+"-uid")
						}
						if err := m.AddOrUpdateWorkload(ctx, log, wl.Obj()); err != nil {
							b.Fatal(err)
						}
					}
				}
				cqImpl := m.getClusterQueueLockless(kueue.ClusterQueueReference(cq.Name))
				lqRef := utilqueue.Key(lq)
				wantActive := parents * 3
				if variants {
					wantActive = parents
				}
				if active, _ := cqImpl.PendingBreakdown(m.parentLabels); active.Total() != wantActive {
					b.Fatalf("ClusterQueue counts %d active pending workloads, want %d", active.Total(), wantActive)
				}
				if active, _ := cqImpl.PendingBreakdownInLocalQueue(lqRef, m.parentLabels); active.Total() != wantActive {
					b.Fatalf("LocalQueue counts %d active pending workloads, want %d", active.Total(), wantActive)
				}

				b.Run("ClusterQueue", func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						cqImpl.PendingBreakdown(m.parentLabels)
					}
				})
				b.Run("LocalQueue", func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						cqImpl.PendingBreakdownInLocalQueue(lqRef, m.parentLabels)
					}
				})
			})
		}
	}
}
