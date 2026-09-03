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

	"github.com/google/go-cmp/cmp"

	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utilmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
)

type assignmentRecomputationLabels struct {
	clusterQueue string
	beforeMode   string
	afterMode    string
	reason       metrics.AssignmentRecomputationReason
}

func checkAssignmentRecomputations(t *testing.T, want map[assignmentRecomputationLabels]float64) {
	t.Helper()
	if want == nil {
		return
	}
	got := make(map[assignmentRecomputationLabels]float64)
	for _, point := range utilmetrics.CollectFilteredGaugeVec(metrics.SchedulingAssignmentRecomputationsTotal, nil) {
		if role := point.Labels["replica_role"]; role != roletracker.RoleStandalone {
			t.Errorf("Unexpected replica_role: %q", role)
		}
		got[assignmentRecomputationLabels{
			clusterQueue: point.Labels["cluster_queue"],
			beforeMode:   point.Labels["before_mode"],
			afterMode:    point.Labels["after_mode"],
			reason:       metrics.AssignmentRecomputationReason(point.Labels["reason"]),
		}] = point.Value
	}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(assignmentRecomputationLabels{})); diff != "" {
		t.Errorf("Unexpected assignment recomputations (-want/+got):\n%s", diff)
	}
}
