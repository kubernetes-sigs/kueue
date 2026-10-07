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

	"github.com/google/go-cmp/cmp"
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
				"active/kind1": 1, "active/kind2": 1, "inadmissible/kind1": 0, "inadmissible/kind2": 1,
			},
		},
		"stopped queue merges active and inadmissible workloads with the same labels": {
			wantWorkloads: map[string]float64{
				"active/kind1": 0, "active/kind2": 0, "inadmissible/kind1": 1, "inadmissible/kind2": 2,
			},
		},
		"resumed queue reports requeued workloads as active": {
			active:  true,
			requeue: true,
			wantWorkloads: map[string]float64{
				"active/kind1": 1, "active/kind2": 2, "inadmissible/kind1": 0, "inadmissible/kind2": 0,
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
				if want == 0 {
					if len(got) != 0 {
						t.Errorf("Expected no pending workload metric for %s, got %v", labelValues, got)
					}
					continue
				}
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
	for _, custom := range []bool{false, true} {
		name := "without custom labels"
		if custom {
			name = "with workload custom labels"
		}
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, custom)
			t.Cleanup(func() { metrics.InitMetricVectors(nil) })
			ctx, log := utiltesting.ContextWithLog(t)
			var customLabels *metrics.CustomLabels
			if custom {
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
				if custom {
					filter["custom_wl_kind"] = "kind1"
				}
				got := make(map[string]float64)
				for _, point := range testingmetrics.CollectFilteredGaugeVec(metrics.PendingWorkloads, filter) {
					got[point.Labels["status"]] = point.Value
				}
				want := map[string]float64{metrics.PendingStatusActive: active, metrics.PendingStatusInadmissible: inadmissible}
				if custom {
					for status, count := range want {
						if count == 0 {
							delete(want, status)
						}
					}
				}
				if diff := cmp.Diff(want, got); diff != "" {
					t.Errorf("%s pending metrics mismatch (-want +got):\n%s", phase, diff)
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
