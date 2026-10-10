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

package metrics

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utilmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestReportAssignmentRecomputation(t *testing.T) {
	cases := map[string]struct {
		beforeMode   string
		afterMode    string
		reason       AssignmentRecomputationReason
		role         string
		customLabels bool
	}{
		"unchanged TAS assignment":    {beforeMode: "Fit", afterMode: "Fit", reason: AssignmentRecomputationReasonNoTAS, role: roletracker.RoleStandalone},
		"failed TAS assignment":       {beforeMode: "Fit", afterMode: "NoFit", reason: AssignmentRecomputationReasonNoTAS, role: roletracker.RoleLeader},
		"deferred overlap assignment": {beforeMode: "Preempt", afterMode: "DeferredFit", reason: AssignmentRecomputationReasonOverlappingPreemptionTargets, role: roletracker.RoleFollower},
		"custom ClusterQueue labels": {
			beforeMode:   "Preempt",
			afterMode:    "Preempt",
			reason:       AssignmentRecomputationReasonOverlappingPreemptionTargets,
			role:         roletracker.RoleLeader,
			customLabels: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, true)
			InitMetricVectors(nil)
			t.Cleanup(func() { InitMetricVectors(nil) })
			var customValues []string
			wantLabels := map[string]string{
				"cluster_queue": "cq",
				"before_mode":   tc.beforeMode,
				"after_mode":    tc.afterMode,
				"reason":        string(tc.reason),
				"replica_role":  tc.role,
			}
			if tc.customLabels {
				NewCustomLabels([]configapi.ControllerMetricsCustomLabel{utiltestingapi.MakeCustomLabel("team").Obj()})
				customValues = []string{"batch"}
				wantLabels["custom_team"] = "batch"
			}
			var tracker *roletracker.RoleTracker
			if tc.role != roletracker.RoleStandalone {
				tracker = roletracker.NewFakeRoleTracker(tc.role)
			}
			for range 2 {
				ReportAssignmentRecomputation("cq", tc.beforeMode, tc.afterMode, tc.reason, customValues, tracker)
			}
			want := []utilmetrics.MetricDataPoint{{Labels: wantLabels, Value: 2}}
			got := utilmetrics.CollectFilteredGaugeVec(SchedulingAssignmentRecomputationsTotal, nil)
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("Unexpected recomputation metric (-want/+got):\n%s", diff)
			}

			// A label update must preserve counters; queue deletion must remove them.
			ClearClusterQueueMetricsOnLabelChange("cq")
			got = utilmetrics.CollectFilteredGaugeVec(SchedulingAssignmentRecomputationsTotal, nil)
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("Counter changed after label cleanup (-want/+got):\n%s", diff)
			}
			ReportAssignmentRecomputation("other-cq", tc.beforeMode, tc.afterMode, tc.reason, customValues, tracker)
			ClearClusterQueueMetrics("cq")
			expectFilteredMetricsCount(t, SchedulingAssignmentRecomputationsTotal, 0, "cluster_queue", "cq")
			expectFilteredMetricsCount(t, SchedulingAssignmentRecomputationsTotal, 1, "cluster_queue", "other-cq")
		})
	}
}
