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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
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
			features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, true)
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
func TestPendingDRADevicesMetrics(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.KueueDRAIntegration, true)
	t.Cleanup(func() { metrics.ClearClusterQueueDRADevicesPendingMetrics("cq") })

	ctx, log := utiltesting.ContextWithLog(t)
	cq := utiltestingapi.MakeClusterQueue("cq").Obj()
	lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue(cq.Name).Obj()

	wl := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").
		PodSets(*utiltestingapi.MakePodSet("main", 2).Obj()).Obj()
	cl := utiltesting.NewFakeClient(utiltesting.MakeNamespace("ns"), wl)
	checker := &pendingWorkloadsStatusChecker{active: true}
	m := NewManagerForUnitTests(cl, checker, WithPreemptionExpectations(preemptexpectations.New()))

	if err := m.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Adding ClusterQueue: %v", err)
	}
	if err := m.AddLocalQueue(ctx, lq); err != nil {
		t.Fatalf("Adding LocalQueue: %v", err)
	}

	reqs := []workload.DRADeviceRequest{
		{
			PodSet:          "main",
			DeviceClass:     "gpu.example.com",
			LogicalResource: "example.com/gpu",
			CountPerPod:     2,
		},
	}
	if err := m.AddOrUpdateWorkload(ctx, log, wl, workload.WithDRADeviceRequests(reqs)); err != nil {
		t.Fatalf("Adding Workload: %v", err)
	}

	reportPendingWorkloads(m, "cq")

	got := testingmetrics.CollectFilteredGaugeVec(metrics.ClusterQueueDRADevicesPending, map[string]string{
		"cluster_queue": "cq",
		"device_class":  "gpu.example.com",
		"replica_role":  roletracker.RoleStandalone,
	})
	if len(got) != 1 {
		t.Fatalf("Expected one pending DRA device metric, got %v", got)
	}
	if got[0].Value != 4 {
		t.Errorf("Pending DRA device metric = %g, want 4", got[0].Value)
	}

	// Delete workload and verify pending count clears to 0
	m.DeleteWorkload(log, workload.Key(wl))
	reportPendingWorkloads(m, "cq")

	got = testingmetrics.CollectFilteredGaugeVec(metrics.ClusterQueueDRADevicesPending, map[string]string{
		"cluster_queue": "cq",
		"device_class":  "gpu.example.com",
		"replica_role":  roletracker.RoleStandalone,
	})
	if len(got) != 1 {
		t.Fatalf("Expected one pending DRA device metric, got %v", got)
	}
	if got[0].Value != 0 {
		t.Errorf("Pending DRA device metric after deletion = %g, want 0", got[0].Value)
	}
}
