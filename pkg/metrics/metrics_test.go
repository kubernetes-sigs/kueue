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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/util/testing/metrics"
	"sigs.k8s.io/kueue/pkg/version"
)

var testTracker = roletracker.NewFakeRoleTracker(roletracker.RoleStandalone)

func expectFilteredMetricsCount(t *testing.T, vec prometheus.Collector, count int, kvs ...string) {
	labels := prometheus.Labels{}
	for i := range len(kvs) / 2 {
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
	ReportClusterQueueQuotas("cohort", "queue", "flavor", "res", 5, 10, 3, testTracker)
	ReportClusterQueueQuotas("cohort", "queue", "flavor2", "res", 1, 2, 1, testTracker)

	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 2, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceBorrowingLimit, 2, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceLendingLimit, 2, "cluster_queue", "queue")

	ReportClusterQueueResourceReservations("cohort", "queue", "flavor", "res", 7, testTracker)
	ReportClusterQueueResourceReservations("cohort", "queue", "flavor2", "res", 3, testTracker)

	ReportClusterQueueResourceUsage("cohort", "queue", "flavor", "res", 7, testTracker)
	ReportClusterQueueResourceUsage("cohort", "queue", "flavor2", "res", 3, testTracker)

	expectFilteredMetricsCount(t, ClusterQueueResourceReservations, 2, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceUsage, 2, "cluster_queue", "queue")

	ClearClusterQueueResourceMetrics("queue")

	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 0, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceBorrowingLimit, 0, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceLendingLimit, 0, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceReservations, 0, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceUsage, 0, "cluster_queue", "queue")
}

func TestReportAndCleanupClusterQueueQuotas(t *testing.T) {
	ReportClusterQueueQuotas("cohort", "queue", "flavor", "res", 5, 10, 3, testTracker)
	ReportClusterQueueQuotas("cohort", "queue", "flavor", "res2", 5, 10, 3, testTracker)
	ReportClusterQueueQuotas("cohort", "queue", "flavor2", "res", 1, 2, 1, testTracker)
	ReportClusterQueueQuotas("cohort", "queue", "flavor2", "res2", 1, 2, 1, testTracker)

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
	ReportClusterQueueResourceReservations("cohort", "queue", "flavor", "res", 5, testTracker)
	ReportClusterQueueResourceReservations("cohort", "queue", "flavor", "res2", 5, testTracker)
	ReportClusterQueueResourceReservations("cohort", "queue", "flavor2", "res", 1, testTracker)
	ReportClusterQueueResourceReservations("cohort", "queue", "flavor2", "res2", 1, testTracker)

	expectFilteredMetricsCount(t, ClusterQueueResourceReservations, 4, "cluster_queue", "queue")

	// drop flavor2
	ClearClusterQueueResourceReservations("queue", "flavor2", "")

	expectFilteredMetricsCount(t, ClusterQueueResourceReservations, 2, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceReservations, 0, "cluster_queue", "queue", "flavor", "flavor2")

	// drop res2
	ClearClusterQueueResourceReservations("queue", "flavor", "res2")

	expectFilteredMetricsCount(t, ClusterQueueResourceReservations, 1, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceReservations, 0, "cluster_queue", "queue", "flavor", "flavor", "resource", "res2")

	ReportClusterQueueResourceUsage("cohort", "queue", "flavor", "res", 5, testTracker)
	ReportClusterQueueResourceUsage("cohort", "queue", "flavor", "res2", 5, testTracker)
	ReportClusterQueueResourceUsage("cohort", "queue", "flavor2", "res", 1, testTracker)
	ReportClusterQueueResourceUsage("cohort", "queue", "flavor2", "res2", 1, testTracker)

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
	ReportEvictedWorkloads("cluster_queue1", "Preempted", "", "", testTracker)
	ReportEvictedWorkloads("cluster_queue1", "Evicted", "", "", testTracker)

	expectFilteredMetricsCount(t, EvictedWorkloadsTotal, 2, "cluster_queue", "cluster_queue1")
	expectFilteredMetricsCount(t, EvictedWorkloadsTotal, 1, "cluster_queue", "cluster_queue1", "reason", "Preempted")
	expectFilteredMetricsCount(t, EvictedWorkloadsTotal, 1, "cluster_queue", "cluster_queue1", "reason", "Evicted")

	ClearClusterQueueMetrics("cluster_queue1")
	expectFilteredMetricsCount(t, EvictedWorkloadsTotal, 0, "cluster_queue", "cluster_queue1")
}

func TestReportAndCleanupClusterQueuePreemptedNumber(t *testing.T) {
	ReportPreemption("cluster_queue1", "InClusterQueue", "cluster_queue1", testTracker)
	ReportPreemption("cluster_queue1", "InCohortReclamation", "cluster_queue1", testTracker)
	ReportPreemption("cluster_queue1", "InCohortFairSharing", "cluster_queue1", testTracker)
	ReportPreemption("cluster_queue1", "InCohortReclaimWhileBorrowing", "cluster_queue1", testTracker)

	expectFilteredMetricsCount(t, PreemptedWorkloadsTotal, 4, "preempting_cluster_queue", "cluster_queue1")
	expectFilteredMetricsCount(t, PreemptedWorkloadsTotal, 1, "preempting_cluster_queue", "cluster_queue1", "reason", "InClusterQueue")
	expectFilteredMetricsCount(t, PreemptedWorkloadsTotal, 1, "preempting_cluster_queue", "cluster_queue1", "reason", "InCohortFairSharing")
	expectFilteredMetricsCount(t, PreemptedWorkloadsTotal, 1, "preempting_cluster_queue", "cluster_queue1", "reason", "InCohortReclamation")
	expectFilteredMetricsCount(t, PreemptedWorkloadsTotal, 1, "preempting_cluster_queue", "cluster_queue1", "reason", "InCohortReclaimWhileBorrowing")

	ClearClusterQueueMetrics("cluster_queue1")
	expectFilteredMetricsCount(t, PreemptedWorkloadsTotal, 0, "preempting_cluster_queue", "cluster_queue1")
}

func TestReportAndCleanupLocalQueueEvictedNumber(t *testing.T) {
	lq := LocalQueueReference{Name: kueue.LocalQueueName("lq1"), Namespace: "ns1"}
	ReportLocalQueueEvictedWorkloads(lq, "Preempted", "", "", testTracker)

	expectFilteredMetricsCount(t, LocalQueueEvictedWorkloadsTotal, 1, "name", "lq1", "namespace", "ns1", "reason", "Preempted")

	ClearLocalQueueMetrics(lq)
	expectFilteredMetricsCount(t, LocalQueueEvictedWorkloadsTotal, 0, "name", "lq1", "namespace", "ns1")
}

func TestGitVersionMetric(t *testing.T) {
	versionInfo := version.Get()
	expectFilteredMetricsCount(t, buildInfo, 1, "git_version", versionInfo.GitVersion)
	expectFilteredMetricsCount(t, buildInfo, 1, "git_commit", versionInfo.GitCommit)
	expectFilteredMetricsCount(t, buildInfo, 1, "build_date", versionInfo.BuildDate)
	expectFilteredMetricsCount(t, buildInfo, 1, "go_version", versionInfo.GoVersion)
	expectFilteredMetricsCount(t, buildInfo, 1, "compiler", versionInfo.Compiler)
	expectFilteredMetricsCount(t, buildInfo, 1, "platform", versionInfo.Platform)
}

func TestReportLocalQueueAdmissionChecksWaitTimeHasPriorityLabel(t *testing.T) {
	lq := LocalQueueReference{Name: "lq3", Namespace: "ns3"}
	ReportLocalQueueAdmissionChecksWaitTime(lq, "p2", 0, testTracker)
	expectFilteredMetricsCount(t, LocalQueueAdmissionChecksWaitTime, 1, "name", "lq3", "namespace", "ns3")
	ClearLocalQueueMetrics(lq)
	expectFilteredMetricsCount(t, LocalQueueAdmissionChecksWaitTime, 0, "name", "lq3", "namespace", "ns3")
}

func TestReportAndCleanupLocalQueueQuotaReservedNumber(t *testing.T) {
	lq := LocalQueueReference{Name: kueue.LocalQueueName("lq1"), Namespace: "ns1"}
	LocalQueueQuotaReservedWorkload(lq, "", 0, testTracker)

	expectFilteredMetricsCount(t, LocalQueueQuotaReservedWorkloadsTotal, 1, "name", "lq1", "namespace", "ns1")

	ClearLocalQueueMetrics(lq)
	expectFilteredMetricsCount(t, LocalQueueQuotaReservedWorkloadsTotal, 0, "name", "lq1", "namespace", "ns1")
}

func TestLocalQueueQuotaReservedWaitTimeHasPriorityLabel(t *testing.T) {
	lq := LocalQueueReference{Name: kueue.LocalQueueName("lq2"), Namespace: "ns2"}
	LocalQueueQuotaReservedWorkload(lq, "p1", 0, testTracker)
	expectFilteredMetricsCount(t, LocalQueueQuotaReservedWaitTime, 1, "name", "lq2", "namespace", "ns2")
	ClearLocalQueueMetrics(lq)
	expectFilteredMetricsCount(t, LocalQueueQuotaReservedWaitTime, 0, "name", "lq2", "namespace", "ns2")
}

func TestMetricsWithDifferentRoles(t *testing.T) {
	tests := []struct {
		name         string
		tracker      *roletracker.RoleTracker
		expectedRole string
	}{
		{
			name:         "leader tracker",
			tracker:      roletracker.NewFakeRoleTracker(roletracker.RoleLeader),
			expectedRole: roletracker.RoleLeader,
		},
		{
			name:         "follower tracker",
			tracker:      roletracker.NewFakeRoleTracker(roletracker.RoleFollower),
			expectedRole: roletracker.RoleFollower,
		},
		{
			name:         "standalone tracker",
			tracker:      roletracker.NewFakeRoleTracker(roletracker.RoleStandalone),
			expectedRole: roletracker.RoleStandalone,
		},
		{
			name:         "nil tracker",
			tracker:      nil,
			expectedRole: roletracker.RoleStandalone,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ReportClusterQueueQuotas("test-cohort", "test-queue", "test-flavor", "cpu", 100, 200, 50, tc.tracker)
			expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 1, "cluster_queue", "test-queue", "replica_role", tc.expectedRole)
			ClearClusterQueueResourceMetrics("test-queue")
		})
	}
}

func TestMultipleMetricEmittersWithRoles(t *testing.T) {
	leaderTracker := roletracker.NewFakeRoleTracker(roletracker.RoleLeader)
	followerTracker := roletracker.NewFakeRoleTracker(roletracker.RoleFollower)

	ReportEvictedWorkloads("test-cq", "Preempted", "", "", leaderTracker)
	expectFilteredMetricsCount(t, EvictedWorkloadsTotal, 1, "cluster_queue", "test-cq", "replica_role", roletracker.RoleLeader)
	ClearClusterQueueMetrics("test-cq")

	ReportPreemption("test-cq", "InClusterQueue", "test-cq", followerTracker)
	expectFilteredMetricsCount(t, PreemptedWorkloadsTotal, 1, "preempting_cluster_queue", "test-cq", "replica_role", roletracker.RoleFollower)
	ClearClusterQueueMetrics("test-cq")

	lq := LocalQueueReference{Name: "test-lq", Namespace: "test-ns"}
	LocalQueueQuotaReservedWorkload(lq, "high", 0, leaderTracker)
	expectFilteredMetricsCount(t, LocalQueueQuotaReservedWorkloadsTotal, 1, "name", "test-lq", "replica_role", roletracker.RoleLeader)
	ClearLocalQueueMetrics(lq)

	ReportClusterQueueResourceUsage("test-cohort", "test-cq", "default", "cpu", 50, followerTracker)
	expectFilteredMetricsCount(t, ClusterQueueResourceUsage, 1, "cluster_queue", "test-cq", "replica_role", roletracker.RoleFollower)
	ClearClusterQueueResourceMetrics("test-cq")
}

func TestRoleFlipUpdatesMetricLabels(t *testing.T) {
	followerTracker := roletracker.NewFakeRoleTracker(roletracker.RoleFollower)
	leaderTracker := roletracker.NewFakeRoleTracker(roletracker.RoleLeader)

	ReportClusterQueueQuotas("cohort", "queue-flip", "flavor", "cpu", 100, 200, 50, followerTracker)
	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 1, "cluster_queue", "queue-flip", "replica_role", roletracker.RoleFollower)

	ReportClusterQueueQuotas("cohort", "queue-flip", "flavor", "cpu", 100, 200, 50, leaderTracker)
	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 1, "cluster_queue", "queue-flip", "replica_role", roletracker.RoleLeader)

	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 2, "cluster_queue", "queue-flip")

	ClearClusterQueueResourceMetrics("queue-flip")
}
