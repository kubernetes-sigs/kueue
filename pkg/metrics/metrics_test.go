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
	ReportClusterQueueQuotas("cohort", "queue", "flavor", "res", 5, 10, 3, nil)
	ReportClusterQueueQuotas("cohort", "queue", "flavor2", "res", 1, 2, 1, nil)

	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 2, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceBorrowingLimit, 2, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceLendingLimit, 2, "cluster_queue", "queue")

	ReportClusterQueueResourceReservations("cohort", "queue", "flavor", "res", 7, nil)
	ReportClusterQueueResourceReservations("cohort", "queue", "flavor2", "res", 3, nil)

	ReportClusterQueueResourceUsage("cohort", "queue", "flavor", "res", 7, nil)
	ReportClusterQueueResourceUsage("cohort", "queue", "flavor2", "res", 3, nil)

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
	ReportClusterQueueQuotas("cohort", "queue", "flavor", "res", 5, 10, 3, nil)
	ReportClusterQueueQuotas("cohort", "queue", "flavor", "res2", 5, 10, 3, nil)
	ReportClusterQueueQuotas("cohort", "queue", "flavor2", "res", 1, 2, 1, nil)
	ReportClusterQueueQuotas("cohort", "queue", "flavor2", "res2", 1, 2, 1, nil)

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
	ReportClusterQueueResourceReservations("cohort", "queue", "flavor", "res", 5, nil)
	ReportClusterQueueResourceReservations("cohort", "queue", "flavor", "res2", 5, nil)
	ReportClusterQueueResourceReservations("cohort", "queue", "flavor2", "res", 1, nil)
	ReportClusterQueueResourceReservations("cohort", "queue", "flavor2", "res2", 1, nil)

	expectFilteredMetricsCount(t, ClusterQueueResourceReservations, 4, "cluster_queue", "queue")

	// drop flavor2
	ClearClusterQueueResourceReservations("queue", "flavor2", "")

	expectFilteredMetricsCount(t, ClusterQueueResourceReservations, 2, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceReservations, 0, "cluster_queue", "queue", "flavor", "flavor2")

	// drop res2
	ClearClusterQueueResourceReservations("queue", "flavor", "res2")

	expectFilteredMetricsCount(t, ClusterQueueResourceReservations, 1, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceReservations, 0, "cluster_queue", "queue", "flavor", "flavor", "resource", "res2")

	ReportClusterQueueResourceUsage("cohort", "queue", "flavor", "res", 5, nil)
	ReportClusterQueueResourceUsage("cohort", "queue", "flavor", "res2", 5, nil)
	ReportClusterQueueResourceUsage("cohort", "queue", "flavor2", "res", 1, nil)
	ReportClusterQueueResourceUsage("cohort", "queue", "flavor2", "res2", 1, nil)

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
	ReportEvictedWorkloads("cluster_queue1", "Preempted", "", "", nil)
	ReportEvictedWorkloads("cluster_queue1", "Evicted", "", "", nil)

	expectFilteredMetricsCount(t, EvictedWorkloadsTotal, 2, "cluster_queue", "cluster_queue1")
	expectFilteredMetricsCount(t, EvictedWorkloadsTotal, 1, "cluster_queue", "cluster_queue1", "reason", "Preempted")
	expectFilteredMetricsCount(t, EvictedWorkloadsTotal, 1, "cluster_queue", "cluster_queue1", "reason", "Evicted")

	ClearClusterQueueMetrics("cluster_queue1")
	expectFilteredMetricsCount(t, EvictedWorkloadsTotal, 0, "cluster_queue", "cluster_queue1")
}

func TestReportAndCleanupClusterQueuePreemptedNumber(t *testing.T) {
	ReportPreemption("cluster_queue1", "InClusterQueue", "cluster_queue1", nil)
	ReportPreemption("cluster_queue1", "InCohortReclamation", "cluster_queue1", nil)
	ReportPreemption("cluster_queue1", "InCohortFairSharing", "cluster_queue1", nil)
	ReportPreemption("cluster_queue1", "InCohortReclaimWhileBorrowing", "cluster_queue1", nil)

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
	ReportLocalQueueEvictedWorkloads(lq, "Preempted", "", "", nil)

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
	ReportLocalQueueAdmissionChecksWaitTime(lq, "p2", 0, nil)
	expectFilteredMetricsCount(t, LocalQueueAdmissionChecksWaitTime, 1, "name", "lq3", "namespace", "ns3")
	ClearLocalQueueMetrics(lq)
	expectFilteredMetricsCount(t, LocalQueueAdmissionChecksWaitTime, 0, "name", "lq3", "namespace", "ns3")
}

func TestReportAndCleanupLocalQueueQuotaReservedNumber(t *testing.T) {
	lq := LocalQueueReference{Name: kueue.LocalQueueName("lq1"), Namespace: "ns1"}
	LocalQueueQuotaReservedWorkload(lq, "", 0, nil)

	expectFilteredMetricsCount(t, LocalQueueQuotaReservedWorkloadsTotal, 1, "name", "lq1", "namespace", "ns1")

	ClearLocalQueueMetrics(lq)
	expectFilteredMetricsCount(t, LocalQueueQuotaReservedWorkloadsTotal, 0, "name", "lq1", "namespace", "ns1")
}

func TestLocalQueueQuotaReservedWaitTimeHasPriorityLabel(t *testing.T) {
	lq := LocalQueueReference{Name: kueue.LocalQueueName("lq2"), Namespace: "ns2"}
	LocalQueueQuotaReservedWorkload(lq, "p1", 0, nil)
	expectFilteredMetricsCount(t, LocalQueueQuotaReservedWaitTime, 1, "name", "lq2", "namespace", "ns2")
	ClearLocalQueueMetrics(lq)
	expectFilteredMetricsCount(t, LocalQueueQuotaReservedWaitTime, 0, "name", "lq2", "namespace", "ns2")
}

func TestMetricsWithReplicaRoleLabel(t *testing.T) {
	ReportClusterQueueQuotas("cohort", "queue_standalone", "flavor", "res", 5, 10, 3, nil)
	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 1, "cluster_queue", "queue_standalone", "replica_role", "standalone")

	ReportEvictedWorkloads("cq_standalone", "Preempted", "", "", nil)
	expectFilteredMetricsCount(t, EvictedWorkloadsTotal, 1, "cluster_queue", "cq_standalone", "replica_role", "standalone")

	ReportPreemption("cq_standalone", "InClusterQueue", "cq_standalone", nil)
	expectFilteredMetricsCount(t, PreemptedWorkloadsTotal, 1, "preempting_cluster_queue", "cq_standalone", "replica_role", "standalone")

	lq := LocalQueueReference{Name: "lq_standalone", Namespace: "ns"}
	ReportLocalQueueEvictedWorkloads(lq, "Preempted", "", "", nil)
	expectFilteredMetricsCount(t, LocalQueueEvictedWorkloadsTotal, 1, "name", "lq_standalone", "replica_role", "standalone")

	ClearClusterQueueResourceMetrics("queue_standalone")
	ClearClusterQueueMetrics("cq_standalone")
	ClearLocalQueueMetrics(lq)
}

func TestMetricsWithDifferentRoles(t *testing.T) {
	leaderTracker := roletracker.NewFakeRoleTracker(roletracker.RoleLeader)
	followerTracker := roletracker.NewFakeRoleTracker(roletracker.RoleFollower)

	ReportClusterQueueQuotas("cohort", "queue_leader", "flavor", "res", 5, 10, 3, leaderTracker)
	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 1, "cluster_queue", "queue_leader", "replica_role", "leader")

	ReportEvictedWorkloads("cq_leader", "Preempted", "", "", leaderTracker)
	expectFilteredMetricsCount(t, EvictedWorkloadsTotal, 1, "cluster_queue", "cq_leader", "replica_role", "leader")

	ReportClusterQueueQuotas("cohort", "queue_follower", "flavor", "res", 5, 10, 3, followerTracker)
	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 1, "cluster_queue", "queue_follower", "replica_role", "follower")

	ReportEvictedWorkloads("cq_follower", "Preempted", "", "", followerTracker)
	expectFilteredMetricsCount(t, EvictedWorkloadsTotal, 1, "cluster_queue", "cq_follower", "replica_role", "follower")

	ClearClusterQueueResourceMetrics("queue_leader")
	ClearClusterQueueResourceMetrics("queue_follower")
	ClearClusterQueueMetrics("cq_leader")
	ClearClusterQueueMetrics("cq_follower")
}

func TestReportAndCleanupLocalQueueResourceReservations(t *testing.T) {
	lq := LocalQueueReference{Name: kueue.LocalQueueName("lq1"), Namespace: "ns1"}

	// Report reservations with different priority classes
	ReportLocalQueueResourceReservations(lq, "flavor1", "cpu", 10.0, "high-priority", nil)
	ReportLocalQueueResourceReservations(lq, "flavor1", "memory", 20.0, "high-priority", nil)
	ReportLocalQueueResourceReservations(lq, "flavor1", "cpu", 5.0, "low-priority", nil)
	ReportLocalQueueResourceReservations(lq, "flavor2", "cpu", 3.0, "", nil) // empty priority class

	// Verify metrics were created with correct labels
	expectFilteredMetricsCount(t, LocalQueueResourceReservations, 4, "name", "lq1", "namespace", "ns1")
	expectFilteredMetricsCount(t, LocalQueueResourceReservations, 2, "name", "lq1", "namespace", "ns1", "priority_class", "high-priority")
	expectFilteredMetricsCount(t, LocalQueueResourceReservations, 1, "name", "lq1", "namespace", "ns1", "priority_class", "low-priority")
	expectFilteredMetricsCount(t, LocalQueueResourceReservations, 1, "name", "lq1", "namespace", "ns1", "priority_class", "")
	expectFilteredMetricsCount(t, LocalQueueResourceReservations, 3, "name", "lq1", "namespace", "ns1", "flavor", "flavor1")
	expectFilteredMetricsCount(t, LocalQueueResourceReservations, 1, "name", "lq1", "namespace", "ns1", "flavor", "flavor2")

	// Clean up and verify
	ClearLocalQueueResourceMetrics(lq)
	expectFilteredMetricsCount(t, LocalQueueResourceReservations, 0, "name", "lq1", "namespace", "ns1")
}
