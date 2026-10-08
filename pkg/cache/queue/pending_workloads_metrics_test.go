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
)

type pendingWorkloadsStatusChecker struct {
	active bool
}

func (c *pendingWorkloadsStatusChecker) ClusterQueueActive(kueue.ClusterQueueReference) bool {
	return c.active
}

// A CQ state change must refresh metrics even when the requeuer moves no
// workloads and no subsequent Workload update triggers another report.
func TestPendingWorkloadsMetricsOnClusterQueueStopResume(t *testing.T) {
	for _, custom := range []bool{false, true} {
		name := "without custom labels"
		if custom {
			name = "with clusterqueue custom labels"
		}
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, custom)
			t.Cleanup(func() { metrics.InitMetricVectors(nil) })
			ctx, log := utiltesting.ContextWithLog(t)
			var customLabels *metrics.CustomLabels
			if custom {
				customLabels = metrics.NewCustomLabels([]config.ControllerMetricsCustomLabel{
					utiltestingapi.MakeCustomLabel("team").SourceLabelKey("team").SourceKind(config.SourceKindClusterQueue).Obj(),
				})
			}
			cq := utiltestingapi.MakeClusterQueue("cq").Label("team", "alpha").Condition(kueue.ClusterQueueActive, metav1.ConditionTrue, "Ready", "Ready").Obj()
			lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue(cq.Name).Obj()
			wl := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").Request(corev1.ResourceCPU, "1").Obj()
			cl := utiltesting.NewClientBuilder().WithObjects(utiltesting.MakeNamespace("ns")).
				WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).Build()
			checker := &pendingWorkloadsStatusChecker{active: true}
			m, requeuer := NewManagerForUnitTestsWithRequeuer(cl, checker,
				WithCustomLabels(customLabels), WithPreemptionExpectations(preemptexpectations.New()))
			if custom {
				customLabels.CQStore("cq", cq.Labels, cq.Annotations)
			}
			if err := m.AddClusterQueue(ctx, cq); err != nil {
				t.Fatal(err)
			}
			if err := m.AddLocalQueue(ctx, lq); err != nil {
				t.Fatal(err)
			}
			requeuer.ProcessRequeues(ctx)
			if err := m.AddOrUpdateWorkload(log, wl); err != nil {
				t.Fatal(err)
			}

			checkMetrics := func(phase string, active, inadmissible float64) {
				t.Helper()
				filter := map[string]string{"cluster_queue": cq.Name, "replica_role": roletracker.RoleStandalone}
				if custom {
					filter["custom_team"] = "alpha"
				}
				got := make(map[string]float64)
				for _, point := range testingmetrics.CollectFilteredGaugeVec(metrics.PendingWorkloads, filter) {
					got[point.Labels["status"]] = point.Value
				}
				want := map[string]float64{metrics.PendingStatusActive: active, metrics.PendingStatusInadmissible: inadmissible}
				for status, count := range want {
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
