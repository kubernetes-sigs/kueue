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
	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/testing/metrics"
)

func expectFilteredMetricsCount(t *testing.T, vec prometheus.Collector, count int, kvs ...string) {
	labels := prometheus.Labels{}
	for i := 0; i < len(kvs)/2; i++ {
		labels[kvs[i*2]] = kvs[i*2+1]
	}
	all := metrics.CollectFilteredGaugeVec(vec, labels)
	if len(all) != count {
		t.Helper()
		t.Errorf("Expecting %d metrics got %d, matching labels %v", count, len(all), kvs)
	}
}

func TestGenerateExponentialBuckets(t *testing.T) {
	expect := []float64{1, 2.5, 5, 10, 20, 40, 80, 160, 320, 640, 1280, 2560, 5120, 10240}
	result := generateExponentialBuckets(14)
	if diff := cmp.Diff(result, expect); len(diff) != 0 {
		t.Errorf("Unexpected buckets (-want,+got):\n%s", diff)
	}
}

func TestReportAndCleanupClusterQueueMetrics(t *testing.T) {
	ReportClusterQueueQuotas("cohort", "queue", "flavor", "res", 5, 10, 3)
	ReportClusterQueueQuotas("cohort", "queue", "flavor2", "res", 1, 2, 1)

	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 2, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceBorrowingLimit, 2, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceLendingLimit, 2, "cluster_queue", "queue")

	ReportClusterQueueResourceReservations("cohort", "queue", "flavor", "res", 7)
	ReportClusterQueueResourceReservations("cohort", "queue", "flavor2", "res", 3)

	ReportClusterQueueResourceUsage("cohort", "queue", "flavor", "res", 7)

	ClearClusterQueueResourceMetrics("queue")

	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 0, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceBorrowingLimit, 0, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceLendingLimit, 0, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceReservations, 0, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceUsage, 0, "cluster_queue", "queue")
}

func TestReportAndCleanupClusterQueueQuotas(t *testing.T) {
	ReportClusterQueueQuotas("cohort", "queue", "flavor", "res", 5, 10, 3)
	ReportClusterQueueQuotas("cohort", "queue", "flavor", "res2", 5, 10, 3)
	ReportClusterQueueQuotas("cohort", "queue", "flavor2", "res", 1, 2, 1)
	ReportClusterQueueQuotas("cohort", "queue", "flavor2", "res2", 1, 2, 1)

	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 4, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceBorrowingLimit, 4, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceLendingLimit, 4, "cluster_queue", "queue")

	// drop flavor2
	ClearClusterQueueResourceQuotas("queue", "flavor2", "")

	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 2, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceBorrowingLimit, 2, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceLendingLimit, 2, "cluster_queue", "queue")

	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 0, "cluster_queue", "queue", "flavor", "flavor2")
	expectFilteredMetricsCount(t, ClusterQueueResourceBorrowingLimit, 0, "cluster_queue", "queue", "flavor", "flavor2")
	expectFilteredMetricsCount(t, ClusterQueueResourceLendingLimit, 0, "cluster_queue", "queue", "flavor", "flavor2")

	// drop res2
	ClearClusterQueueResourceQuotas("queue", "flavor", "res2")

	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 1, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceBorrowingLimit, 1, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceLendingLimit, 1, "cluster_queue", "queue")

	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 0, "cluster_queue", "queue", "flavor", "flavor", "resource", "res2")
	expectFilteredMetricsCount(t, ClusterQueueResourceBorrowingLimit, 0, "cluster_queue", "queue", "flavor", "flavor", "resource", "res2")
	expectFilteredMetricsCount(t, ClusterQueueResourceLendingLimit, 0, "cluster_queue", "queue", "flavor", "flavor", "resource", "res2")
}

func TestReportAndCleanupClusterQueueUsage(t *testing.T) {
	ReportClusterQueueResourceReservations("cohort", "queue", "flavor", "res", 5)
	ReportClusterQueueResourceReservations("cohort", "queue", "flavor", "res2", 5)
	ReportClusterQueueResourceReservations("cohort", "queue", "flavor2", "res", 1)
	ReportClusterQueueResourceReservations("cohort", "queue", "flavor2", "res2", 1)

	expectFilteredMetricsCount(t, ClusterQueueResourceReservations, 4, "cluster_queue", "queue")

	// drop flavor2
	ClearClusterQueueResourceReservations("queue", "flavor2", "")

	expectFilteredMetricsCount(t, ClusterQueueResourceReservations, 2, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceReservations, 0, "cluster_queue", "queue", "flavor", "flavor2")

	// drop res2
	ClearClusterQueueResourceReservations("queue", "flavor", "res2")

	expectFilteredMetricsCount(t, ClusterQueueResourceReservations, 1, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceReservations, 0, "cluster_queue", "queue", "flavor", "flavor", "resource", "res2")

	ReportClusterQueueResourceUsage("cohort", "queue", "flavor", "res", 5)
	ReportClusterQueueResourceUsage("cohort", "queue", "flavor", "res2", 5)
	ReportClusterQueueResourceUsage("cohort", "queue", "flavor2", "res", 1)
	ReportClusterQueueResourceUsage("cohort", "queue", "flavor2", "res2", 1)

	expectFilteredMetricsCount(t, ClusterQueueResourceUsage, 4, "cluster_queue", "queue")

	// drop flavor2
	ClearClusterQueueResourceUsage("queue", "flavor2", "")

	expectFilteredMetricsCount(t, ClusterQueueResourceUsage, 2, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceUsage, 0, "cluster_queue", "queue", "flavor", "flavor2")

	// drop res2
	ClearClusterQueueResourceUsage("queue", "flavor", "res2")

	expectFilteredMetricsCount(t, ClusterQueueResourceUsage, 1, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceUsage, 0, "cluster_queue", "queue", "flavor", "flavor", "resource", "res2")
}

func TestReportAndCleanupClusterQueueEvictedNumber(t *testing.T) {
	ReportEvictedWorkloads("cluster_queue1", "Preempted")
	ReportEvictedWorkloads("cluster_queue1", "Evicted")

	expectFilteredMetricsCount(t, EvictedWorkloadsTotal, 2, "cluster_queue", "cluster_queue1")
	expectFilteredMetricsCount(t, EvictedWorkloadsTotal, 1, "cluster_queue", "cluster_queue1", "reason", "Preempted")
	expectFilteredMetricsCount(t, EvictedWorkloadsTotal, 1, "cluster_queue", "cluster_queue1", "reason", "Evicted")

	ClearClusterQueueMetrics("cluster_queue1")
	expectFilteredMetricsCount(t, EvictedWorkloadsTotal, 0, "cluster_queue", "cluster_queue1")
}

func TestReportAndCleanupClusterQueuePreemptedNumber(t *testing.T) {
	ReportPreemption("cluster_queue1", "InClusterQueue", "cluster_queue1")
	ReportPreemption("cluster_queue1", "InCohortReclamation", "cluster_queue1")
	ReportPreemption("cluster_queue1", "InCohortFairSharing", "cluster_queue1")
	ReportPreemption("cluster_queue1", "InCohortReclaimWhileBorrowing", "cluster_queue1")

	expectFilteredMetricsCount(t, PreemptedWorkloadsTotal, 4, "preempting_cluster_queue", "cluster_queue1")
	expectFilteredMetricsCount(t, EvictedWorkloadsTotal, 1, "cluster_queue", "cluster_queue1")
	expectFilteredMetricsCount(t, PreemptedWorkloadsTotal, 1, "preempting_cluster_queue", "cluster_queue1", "reason", "InClusterQueue")
	expectFilteredMetricsCount(t, PreemptedWorkloadsTotal, 1, "preempting_cluster_queue", "cluster_queue1", "reason", "InCohortFairSharing")
	expectFilteredMetricsCount(t, PreemptedWorkloadsTotal, 1, "preempting_cluster_queue", "cluster_queue1", "reason", "InCohortReclamation")
	expectFilteredMetricsCount(t, PreemptedWorkloadsTotal, 1, "preempting_cluster_queue", "cluster_queue1", "reason", "InCohortReclaimWhileBorrowing")

	ClearClusterQueueMetrics("cluster_queue1")
	expectFilteredMetricsCount(t, PreemptedWorkloadsTotal, 0, "preempting_cluster_queue", "cluster_queue1")
	expectFilteredMetricsCount(t, EvictedWorkloadsTotal, 0, "cluster_queue", "cluster_queue1")
}

func TestShouldReportLocalQueueMetrics(t *testing.T) {
	tests := []struct {
		name               string
		config             configapi.LocalQueueMetrics
		testLabels         map[string]string
		expectLabelMatches bool
	}{
		{
			name: "with nil selector",
			config: configapi.LocalQueueMetrics{
				LocalQueueSelector: nil,
			},
			testLabels:         map[string]string{"app": "test"},
			expectLabelMatches: false,
		},
		{
			name: "with empty selector",
			config: configapi.LocalQueueMetrics{
				LocalQueueSelector: &metav1.LabelSelector{},
			},
			testLabels:         map[string]string{"app": "test"},
			expectLabelMatches: true,
		},
		{
			name: "with specific selector that matches",
			config: configapi.LocalQueueMetrics{
				LocalQueueSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": "test"},
				},
			},
			testLabels:         map[string]string{"app": "test"},
			expectLabelMatches: true,
		},
		{
			name: "with specific selector that doesn't match",
			config: configapi.LocalQueueMetrics{
				LocalQueueSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": "test"},
				},
			},
			testLabels:         map[string]string{"app": "other"},
			expectLabelMatches: false,
		},
	}
	features.SetEnable(features.LocalQueueMetrics, true)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset the config singleton for each test
			lqMetricsConfigSingleton = nil

			err := SetLocalQueueMetrics(&tc.config)
			if err != nil {
				t.Fatalf("Expected no error but got: %v", err)
			}

			if ShouldReportLocalMetrics(tc.testLabels) != tc.expectLabelMatches {
				t.Fatalf("Expected enabled=%v but got %v", tc.expectLabelMatches, ShouldReportLocalMetrics(tc.testLabels))
			}
		})
	}
}

func TestLocalQueueMetricsEnabled(t *testing.T) {
	tests := []struct {
		name           string
		featureEnabled bool
		configNil      bool
		expected       bool
	}{
		{
			name:           "feature disable and config not nil",
			featureEnabled: false,
			configNil:      false,
			expected:       false,
		},
		{
			name:           "feature disabled, config nil",
			featureEnabled: false,
			configNil:      false,
			expected:       false,
		},
		{
			name:           "feature enabled, config not nil",
			featureEnabled: true,
			configNil:      false,
			expected:       true,
		},
		{
			name:           "feature enabled, config nil",
			featureEnabled: true,
			configNil:      true,
			expected:       false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.LocalQueueMetrics, tc.featureEnabled)

			// Setup config
			if tc.configNil {
				lqMetricsConfigSingleton = nil
			} else {
				lqMetricsConfigSingleton = &LocalQueueMetricsConfig{
					localQueueSelector: labels.Everything(),
				}
			}

			if result := LocalQueueMetricsEnabled(); result != tc.expected {
				t.Errorf("Expected LocalQueueMetricsEnabled()=%v but got %v", tc.expected, result)
			}
		})
	}
}
