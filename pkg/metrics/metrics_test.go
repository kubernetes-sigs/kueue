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
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
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

func expectGaugeValue(t *testing.T, vec prometheus.Collector, wantValue float64, kvs ...string) {
	t.Helper()
	labels := prometheus.Labels{}
	for i := range len(kvs) / 2 {
		labels[kvs[i*2]] = kvs[i*2+1]
	}
	dps := metrics.CollectFilteredGaugeVec(vec, labels)
	if len(dps) != 1 {
		t.Errorf("expected 1 metric with labels %v, got %d", labels, len(dps))
		return
	}
	if dps[0].Value != wantValue {
		t.Errorf("gauge value with labels %v: want %v, got %v", labels, wantValue, dps[0].Value)
	}
}

func TestGenerateExponentialBuckets(t *testing.T) {
	expect := []float64{1, 2.5, 5, 10, 20, 40, 80, 160, 320, 640, 1280, 2560, 5120, 10240}
	result := generateExponentialBuckets(14)
	if diff := cmp.Diff(result, expect); len(diff) != 0 {
		t.Errorf("Unexpected buckets (-want,+got):\n%s", diff)
	}
}

func TestReportAndCleanupClusterQueuePendingResources(t *testing.T) {
	const cqName = "cq-pending"

	ReportClusterQueueResourcePending(cqName, "cpu", 4, nil, nil)
	ReportClusterQueueResourcePending(cqName, "memory", 8589934592, nil, nil)

	expectFilteredMetricsCount(t, ClusterQueueResourcePending, 2, "cluster_queue", cqName)
	expectFilteredMetricsCount(t, ClusterQueueResourcePending, 1, "cluster_queue", cqName, "resource", "cpu")
	expectFilteredMetricsCount(t, ClusterQueueResourcePending, 1, "cluster_queue", cqName, "resource", "memory")

	ClearClusterQueueResourcePendingMetrics(cqName)
	expectFilteredMetricsCount(t, ClusterQueueResourcePending, 0, "cluster_queue", cqName)

	// Verify ClearClusterQueueMetrics (gaugeCleanupScopeClusterQueue) also clears it.
	ReportClusterQueueResourcePending(cqName, "cpu", 2, nil, nil)
	expectFilteredMetricsCount(t, ClusterQueueResourcePending, 1, "cluster_queue", cqName)
	ClearClusterQueueMetrics(cqName)
	expectFilteredMetricsCount(t, ClusterQueueResourcePending, 0, "cluster_queue", cqName)
}

func TestReportAndCleanupClusterQueueMetrics(t *testing.T) {
	ReportClusterQueueQuotas("cohort", "queue", "flavor", "res", 5, 10, 3, nil, nil)
	ReportClusterQueueQuotas("cohort", "queue", "flavor2", "res", 1, 2, 1, nil, nil)

	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 2, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceBorrowingLimit, 2, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceLendingLimit, 2, "cluster_queue", "queue")

	ReportClusterQueueResourceReservations("cohort", "queue", "flavor", "res", 7, nil, nil)
	ReportClusterQueueResourceReservations("cohort", "queue", "flavor2", "res", 3, nil, nil)

	ReportClusterQueueResourceUsage("cohort", "queue", "flavor", "res", 7, nil, nil)
	ReportClusterQueueResourceUsage("cohort", "queue", "flavor2", "res", 3, nil, nil)

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
	ReportClusterQueueQuotas("cohort", "queue", "flavor", "res", 5, 10, 3, nil, nil)
	ReportClusterQueueQuotas("cohort", "queue", "flavor", "res2", 5, 10, 3, nil, nil)
	ReportClusterQueueQuotas("cohort", "queue", "flavor2", "res", 1, 2, 1, nil, nil)
	ReportClusterQueueQuotas("cohort", "queue", "flavor2", "res2", 1, 2, 1, nil, nil)

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
	ReportClusterQueueResourceReservations("cohort", "queue", "flavor", "res", 5, nil, nil)
	ReportClusterQueueResourceReservations("cohort", "queue", "flavor", "res2", 5, nil, nil)
	ReportClusterQueueResourceReservations("cohort", "queue", "flavor2", "res", 1, nil, nil)
	ReportClusterQueueResourceReservations("cohort", "queue", "flavor2", "res2", 1, nil, nil)

	expectFilteredMetricsCount(t, ClusterQueueResourceReservations, 4, "cluster_queue", "queue")

	// drop flavor2
	ClearClusterQueueResourceReservations("queue", "flavor2", "")

	expectFilteredMetricsCount(t, ClusterQueueResourceReservations, 2, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceReservations, 0, "cluster_queue", "queue", "flavor", "flavor2")

	// drop res2
	ClearClusterQueueResourceReservations("queue", "flavor", "res2")

	expectFilteredMetricsCount(t, ClusterQueueResourceReservations, 1, "cluster_queue", "queue")
	expectFilteredMetricsCount(t, ClusterQueueResourceReservations, 0, "cluster_queue", "queue", "flavor", "flavor", "resource", "res2")

	ReportClusterQueueResourceUsage("cohort", "queue", "flavor", "res", 5, nil, nil)
	ReportClusterQueueResourceUsage("cohort", "queue", "flavor", "res2", 5, nil, nil)
	ReportClusterQueueResourceUsage("cohort", "queue", "flavor2", "res", 1, nil, nil)
	ReportClusterQueueResourceUsage("cohort", "queue", "flavor2", "res2", 1, nil, nil)

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

func TestReportAndCleanupWorkloadEvictionLatency(t *testing.T) {
	ReportWorkloadEvictionLatency("cq-preempt-unique", kueue.WorkloadEvictedByPreemption, time.Second, nil, nil)
	n := testutil.CollectAndCount(WorkloadEvictionLatencySeconds)
	if n == 0 {
		t.Fatal("expected workload_eviction_latency_seconds histogram to emit metrics")
	}
	ClearClusterQueueMetrics("cq-preempt-unique")
	nAfter := testutil.CollectAndCount(WorkloadEvictionLatencySeconds)
	if nAfter >= n {
		t.Fatalf("expected fewer histogram metrics after clear, before=%d after=%d", n, nAfter)
	}
}

func TestReportAndCleanupClusterQueueEvictedNumber(t *testing.T) {
	ReportEvictedWorkloads("cluster_queue1", "Preempted", "", "", nil, nil)
	ReportEvictedWorkloads("cluster_queue1", "Evicted", "", "", nil, nil)

	expectFilteredMetricsCount(t, EvictedWorkloadsTotal, 2, "cluster_queue", "cluster_queue1")
	expectFilteredMetricsCount(t, EvictedWorkloadsTotal, 1, "cluster_queue", "cluster_queue1", "reason", "Preempted")
	expectFilteredMetricsCount(t, EvictedWorkloadsTotal, 1, "cluster_queue", "cluster_queue1", "reason", "Evicted")

	ClearClusterQueueMetrics("cluster_queue1")
	expectFilteredMetricsCount(t, EvictedWorkloadsTotal, 0, "cluster_queue", "cluster_queue1")
}

func TestReportAndCleanupClusterQueuePreemptedNumber(t *testing.T) {
	ReportPreemption("cluster_queue1", "InClusterQueue", "cluster_queue1", nil, nil)
	ReportPreemption("cluster_queue1", "InCohortReclamation", "cluster_queue1", nil, nil)
	ReportPreemption("cluster_queue1", "InCohortFairSharing", "cluster_queue1", nil, nil)
	ReportPreemption("cluster_queue1", "InCohortReclaimWhileBorrowing", "cluster_queue1", nil, nil)

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
	ReportLocalQueueEvictedWorkloads(lq, "Preempted", "", "", nil, nil)

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
	ReportLocalQueueAdmissionChecksWaitTime(lq, "p2", 0, nil, nil)
	expectFilteredMetricsCount(t, LocalQueueAdmissionChecksWaitTime, 1, "name", "lq3", "namespace", "ns3")
	ClearLocalQueueMetrics(lq)
	expectFilteredMetricsCount(t, LocalQueueAdmissionChecksWaitTime, 0, "name", "lq3", "namespace", "ns3")
}

func TestReportAndCleanupLocalQueueQuotaReservedNumber(t *testing.T) {
	lq := LocalQueueReference{Name: kueue.LocalQueueName("lq1"), Namespace: "ns1"}
	LocalQueueQuotaReservedWorkload(lq, "", 0, nil, nil)

	expectFilteredMetricsCount(t, LocalQueueQuotaReservedWorkloadsTotal, 1, "name", "lq1", "namespace", "ns1")

	ClearLocalQueueMetrics(lq)
	expectFilteredMetricsCount(t, LocalQueueQuotaReservedWorkloadsTotal, 0, "name", "lq1", "namespace", "ns1")
}

func TestLocalQueueQuotaReservedWaitTimeHasPriorityLabel(t *testing.T) {
	lq := LocalQueueReference{Name: kueue.LocalQueueName("lq2"), Namespace: "ns2"}
	LocalQueueQuotaReservedWorkload(lq, "p1", 0, nil, nil)
	expectFilteredMetricsCount(t, LocalQueueQuotaReservedWaitTime, 1, "name", "lq2", "namespace", "ns2")
	ClearLocalQueueMetrics(lq)
	expectFilteredMetricsCount(t, LocalQueueQuotaReservedWaitTime, 0, "name", "lq2", "namespace", "ns2")
}

func TestMetricsWithReplicaRoleLabel(t *testing.T) {
	ReportClusterQueueQuotas("cohort", "queue_standalone", "flavor", "res", 5, 10, 3, nil, nil)
	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 1, "cluster_queue", "queue_standalone", "replica_role", "standalone")

	ReportEvictedWorkloads("cq_standalone", "Preempted", "", "", nil, nil)
	expectFilteredMetricsCount(t, EvictedWorkloadsTotal, 1, "cluster_queue", "cq_standalone", "replica_role", "standalone")

	ReportPreemption("cq_standalone", "InClusterQueue", "cq_standalone", nil, nil)
	expectFilteredMetricsCount(t, PreemptedWorkloadsTotal, 1, "preempting_cluster_queue", "cq_standalone", "replica_role", "standalone")

	lq := LocalQueueReference{Name: "lq_standalone", Namespace: "ns"}
	ReportLocalQueueEvictedWorkloads(lq, "Preempted", "", "", nil, nil)
	expectFilteredMetricsCount(t, LocalQueueEvictedWorkloadsTotal, 1, "name", "lq_standalone", "replica_role", "standalone")

	ClearClusterQueueResourceMetrics("queue_standalone")
	ClearClusterQueueMetrics("cq_standalone")
	ClearLocalQueueMetrics(lq)
}

func TestStaleMetricsAfterRoleTransition(t *testing.T) {
	followerTracker := roletracker.NewFakeRoleTracker(roletracker.RoleFollower)
	leaderTracker := roletracker.NewFakeRoleTracker(roletracker.RoleLeader)

	ReportClusterQueueQuotas("cohort", "stale-test-cq", "flavor", "cpu", 10, 5, 3, nil, followerTracker)
	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 1, "cluster_queue", "stale-test-cq", "replica_role", "follower")

	ClearGaugeMetricsForRole(roletracker.RoleFollower)

	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 0, "cluster_queue", "stale-test-cq", "replica_role", "follower")

	ReportClusterQueueQuotas("cohort", "stale-test-cq", "flavor", "cpu", 10, 5, 3, nil, leaderTracker)
	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 1, "cluster_queue", "stale-test-cq", "replica_role", "leader")

	ClearClusterQueueResourceMetrics("stale-test-cq")
}

func TestMetricsWithDifferentRoles(t *testing.T) {
	leaderTracker := roletracker.NewFakeRoleTracker(roletracker.RoleLeader)
	followerTracker := roletracker.NewFakeRoleTracker(roletracker.RoleFollower)

	ReportClusterQueueQuotas("cohort", "queue_leader", "flavor", "res", 5, 10, 3, nil, leaderTracker)
	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 1, "cluster_queue", "queue_leader", "replica_role", "leader")

	ReportEvictedWorkloads("cq_leader", "Preempted", "", "", nil, leaderTracker)
	expectFilteredMetricsCount(t, EvictedWorkloadsTotal, 1, "cluster_queue", "cq_leader", "replica_role", "leader")

	ReportClusterQueueQuotas("cohort", "queue_follower", "flavor", "res", 5, 10, 3, nil, followerTracker)
	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 1, "cluster_queue", "queue_follower", "replica_role", "follower")

	ReportEvictedWorkloads("cq_follower", "Preempted", "", "", nil, followerTracker)
	expectFilteredMetricsCount(t, EvictedWorkloadsTotal, 1, "cluster_queue", "cq_follower", "replica_role", "follower")

	ClearClusterQueueResourceMetrics("queue_leader")
	ClearClusterQueueResourceMetrics("queue_follower")
	ClearClusterQueueMetrics("cq_leader")
	ClearClusterQueueMetrics("cq_follower")
}

func TestClearClusterQueueMetricsOnLabelChangeOnlyClearsScopedGaugeMetrics(t *testing.T) {
	const cqName = "cq-label-change"

	ReportPendingWorkloads(cqName, 3, 1, nil, nil)
	ReportClusterQueueWeightedShare(cqName, "cohort", 7, nil, nil)
	ReportReplacedWorkloadSlices(cqName, nil, nil)

	expectFilteredMetricsCount(t, PendingWorkloads, 2, "cluster_queue", cqName)
	expectFilteredMetricsCount(t, ClusterQueueWeightedShare, 1, "cluster_queue", cqName)
	expectFilteredMetricsCount(t, ReplacedWorkloadSlicesTotal, 1, "cluster_queue", cqName)

	ClearClusterQueueMetricsOnLabelChange(cqName)

	expectFilteredMetricsCount(t, PendingWorkloads, 2, "cluster_queue", cqName)
	expectFilteredMetricsCount(t, ClusterQueueWeightedShare, 0, "cluster_queue", cqName)
	expectFilteredMetricsCount(t, ReplacedWorkloadSlicesTotal, 0, "cluster_queue", cqName)

	ClearClusterQueueMetrics(cqName)
}

func TestClearCacheMetricsOnlyClearsCacheScopedGauges(t *testing.T) {
	const cqName = "cq-cache-scope"

	ReportClusterQueueStatus(cqName, CQStatusActive, nil, nil)
	ReportAdmittedActiveWorkloads(cqName, 3, nil, nil)
	ReportReservingActiveWorkloads(cqName, 1, nil, nil)
	ReportClusterQueueQuotas("cohort", cqName, "flavor", "cpu", 10, 5, 3, nil, nil)
	ReportPendingWorkloads(cqName, 4, 2, nil, nil)

	expectFilteredMetricsCount(t, ClusterQueueByStatus, 3, "cluster_queue", cqName)
	expectFilteredMetricsCount(t, AdmittedActiveWorkloads, 1, "cluster_queue", cqName)
	expectFilteredMetricsCount(t, ReservingActiveWorkloads, 1, "cluster_queue", cqName)
	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 1, "cluster_queue", cqName)
	expectFilteredMetricsCount(t, PendingWorkloads, 2, "cluster_queue", cqName)

	ClearCacheMetrics(cqName)

	expectFilteredMetricsCount(t, ClusterQueueByStatus, 0, "cluster_queue", cqName)
	expectFilteredMetricsCount(t, AdmittedActiveWorkloads, 0, "cluster_queue", cqName)
	expectFilteredMetricsCount(t, ReservingActiveWorkloads, 0, "cluster_queue", cqName)
	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 1, "cluster_queue", cqName)
	expectFilteredMetricsCount(t, PendingWorkloads, 2, "cluster_queue", cqName)

	ClearClusterQueueResourceMetrics(cqName)
	ClearClusterQueueMetrics(cqName)
}

func TestClearClusterQueueResourceMetricsOnlyClearsResourceScopedGauges(t *testing.T) {
	const cqName = "cq-resource-scope"

	ReportClusterQueueQuotas("cohort", cqName, "flavor", "cpu", 10, 5, 3, nil, nil)
	ReportClusterQueueResourceReservations("cohort", cqName, "flavor", "cpu", 7, nil, nil)
	ReportClusterQueueResourceUsage("cohort", cqName, "flavor", "cpu", 6, nil, nil)
	ReportClusterQueueResourcePending(cqName, "cpu", 4, nil, nil)
	ReportClusterQueueStatus(cqName, CQStatusActive, nil, nil)

	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 1, "cluster_queue", cqName)
	expectFilteredMetricsCount(t, ClusterQueueResourceBorrowingLimit, 1, "cluster_queue", cqName)
	expectFilteredMetricsCount(t, ClusterQueueResourceLendingLimit, 1, "cluster_queue", cqName)
	expectFilteredMetricsCount(t, ClusterQueueResourceReservations, 1, "cluster_queue", cqName)
	expectFilteredMetricsCount(t, ClusterQueueResourceUsage, 1, "cluster_queue", cqName)
	expectFilteredMetricsCount(t, ClusterQueueResourcePending, 1, "cluster_queue", cqName)
	expectFilteredMetricsCount(t, ClusterQueueByStatus, 3, "cluster_queue", cqName)

	ClearClusterQueueResourceMetrics(cqName)

	expectFilteredMetricsCount(t, ClusterQueueResourceNominalQuota, 0, "cluster_queue", cqName)
	expectFilteredMetricsCount(t, ClusterQueueResourceBorrowingLimit, 0, "cluster_queue", cqName)
	expectFilteredMetricsCount(t, ClusterQueueResourceLendingLimit, 0, "cluster_queue", cqName)
	expectFilteredMetricsCount(t, ClusterQueueResourceReservations, 0, "cluster_queue", cqName)
	expectFilteredMetricsCount(t, ClusterQueueResourceUsage, 0, "cluster_queue", cqName)
	expectFilteredMetricsCount(t, ClusterQueueResourcePending, 0, "cluster_queue", cqName)
	expectFilteredMetricsCount(t, ClusterQueueByStatus, 3, "cluster_queue", cqName)

	ClearCacheMetrics(cqName)
}

func TestClearLocalQueueResourceMetricsOnlyClearsResourceScopedGauges(t *testing.T) {
	lq := LocalQueueReference{Name: "lq-resource-scope", Namespace: "ns-resource-scope"}

	ReportLocalQueueResourceReservations(lq, "flavor", "cpu", 7, nil, nil)
	ReportLocalQueueResourceUsage(lq, "flavor", "cpu", 6, nil, nil)
	ReportLocalQueueStatus(lq, "True", nil, nil)
	ReportLocalQueuePendingWorkloads(lq, 4, 2, nil, nil)

	expectFilteredMetricsCount(t, LocalQueueResourceReservations, 1, "name", string(lq.Name), "namespace", lq.Namespace)
	expectFilteredMetricsCount(t, LocalQueueResourceUsage, 1, "name", string(lq.Name), "namespace", lq.Namespace)
	expectFilteredMetricsCount(t, LocalQueueByStatus, 3, "name", string(lq.Name), "namespace", lq.Namespace)
	expectFilteredMetricsCount(t, LocalQueuePendingWorkloads, 2, "name", string(lq.Name), "namespace", lq.Namespace)

	ClearLocalQueueResourceMetrics(lq)

	expectFilteredMetricsCount(t, LocalQueueResourceReservations, 0, "name", string(lq.Name), "namespace", lq.Namespace)
	expectFilteredMetricsCount(t, LocalQueueResourceUsage, 0, "name", string(lq.Name), "namespace", lq.Namespace)
	expectFilteredMetricsCount(t, LocalQueueByStatus, 3, "name", string(lq.Name), "namespace", lq.Namespace)
	expectFilteredMetricsCount(t, LocalQueuePendingWorkloads, 2, "name", string(lq.Name), "namespace", lq.Namespace)

	ClearLocalQueueCacheMetrics(lq)
	ClearLocalQueueMetrics(lq)
}

func TestCohortMetrics(t *testing.T) {
	leaderTracker := roletracker.NewFakeRoleTracker(roletracker.RoleLeader)
	followerTracker := roletracker.NewFakeRoleTracker(roletracker.RoleFollower)

	ReportCohortSubtreeQuota("cohort", "flavor", "res", 5, nil, leaderTracker)
	expectFilteredMetricsCount(t, CohortSubtreeQuota, 1, "cohort", "cohort", "replica_role", "leader")

	ReportCohortSubtreeQuota("cohort", "flavor", "res", 3, nil, followerTracker)
	expectFilteredMetricsCount(t, CohortSubtreeQuota, 1, "cohort", "cohort", "replica_role", "follower")

	ReportCohortSubtreeQuota("cohort_two", "flavor", "res", 5, nil, leaderTracker)
	expectFilteredMetricsCount(t, CohortSubtreeQuota, 1, "cohort", "cohort_two", "replica_role", "leader")

	ReportCohortSubtreeResourceReservations("cohort", "flavor", "res", 5, nil, leaderTracker)
	expectFilteredMetricsCount(t, CohortSubtreeResourceReservations, 1, "cohort", "cohort", "replica_role", "leader")

	ReportCohortSubtreeResourceReservations("cohort", "flavor", "res", 3, nil, followerTracker)
	expectFilteredMetricsCount(t, CohortSubtreeResourceReservations, 1, "cohort", "cohort", "replica_role", "follower")

	ReportCohortSubtreeResourceReservations("cohort_two", "flavor", "res", 3, nil, leaderTracker)
	expectFilteredMetricsCount(t, CohortSubtreeResourceReservations, 1, "cohort", "cohort_two", "replica_role", "leader")

	ClearCohortSubtreeResourceReservations("cohort", "", "")
	expectFilteredMetricsCount(t, CohortSubtreeResourceReservations, 1, "cohort", "cohort_two", "replica_role", "leader")

	ClearCohortMetrics("cohort")
	expectFilteredMetricsCount(t, CohortSubtreeQuota, 0, "cohort", "cohort")
	expectFilteredMetricsCount(t, CohortSubtreeResourceReservations, 0, "cohort", "cohort")

	expectFilteredMetricsCount(t, CohortSubtreeQuota, 1, "cohort", "cohort_two", "replica_role", "leader")
	expectFilteredMetricsCount(t, CohortSubtreeResourceReservations, 1, "cohort", "cohort_two", "replica_role", "leader")

	ClearCohortMetrics("cohort_two")
	expectFilteredMetricsCount(t, CohortSubtreeQuota, 0, "cohort", "cohort_two")
	expectFilteredMetricsCount(t, CohortSubtreeResourceReservations, 0, "cohort", "cohort_two")
}

func TestClearCohortMetricsOnlyClearsScopedGauges(t *testing.T) {
	const cohortName = "cohort-scope"

	ReportCohortWeightedShare(cohortName, 7, nil, nil)
	ReportCohortSubtreeQuota(cohortName, "flavor", "cpu", 10, nil, nil)
	ReportCohortSubtreeResourceReservations(cohortName, "flavor", "cpu", 6, nil, nil)
	ReportCohortSubtreeAdmittedActiveWorkloads(cohortName, 4, nil, nil)

	expectFilteredMetricsCount(t, CohortWeightedShare, 1, "cohort", cohortName)
	expectFilteredMetricsCount(t, CohortSubtreeQuota, 1, "cohort", cohortName)
	expectFilteredMetricsCount(t, CohortSubtreeResourceReservations, 1, "cohort", cohortName)
	expectFilteredMetricsCount(t, CohortSubtreeAdmittedActiveWorkloads, 1, "cohort", cohortName)

	ClearCohortMetrics(cohortName)

	expectFilteredMetricsCount(t, CohortWeightedShare, 0, "cohort", cohortName)
	expectFilteredMetricsCount(t, CohortSubtreeQuota, 0, "cohort", cohortName)
	expectFilteredMetricsCount(t, CohortSubtreeResourceReservations, 0, "cohort", cohortName)
	expectFilteredMetricsCount(t, CohortSubtreeAdmittedActiveWorkloads, 1, "cohort", cohortName)

	ClearCohortAdmittedWorkloadsMetrics(cohortName)
}

func TestTASDomainUsage(t *testing.T) {
	const rackLabel = "cloud.com/rack"

	type gaugeOp struct {
		sub      bool
		levels   []string
		values   []string
		resource corev1.ResourceName
		qty      int64
	}
	type wantGauge struct {
		domain, domainID, resource string
		value                      float64
	}
	tests := []struct {
		name           string
		flavor         kueue.ResourceFlavorReference
		excludedLevels []string
		ops            []gaugeOp
		wantGauges     []wantGauge
		wantNone       bool
	}{
		{
			name:   "add emits gauge at each topology level",
			flavor: "tas-add-levels",
			ops: []gaugeOp{
				{levels: []string{rackLabel, corev1.LabelHostname}, values: []string{"rack-1", "node-1"}, resource: corev1.ResourceCPU, qty: 4000},
			},
			wantGauges: []wantGauge{
				{domain: rackLabel, domainID: "rack-1", resource: string(corev1.ResourceCPU), value: 4000},
				{domain: corev1.LabelHostname, domainID: "node-1", resource: string(corev1.ResourceCPU), value: 4000},
			},
		},
		{
			name:   "subtract decrements gauge at each topology level",
			flavor: "tas-sub-levels",
			ops: []gaugeOp{
				{levels: []string{rackLabel, corev1.LabelHostname}, values: []string{"rack-1", "node-1"}, resource: corev1.ResourceCPU, qty: 6000},
				{sub: true, levels: []string{rackLabel, corev1.LabelHostname}, values: []string{"rack-1", "node-1"}, resource: corev1.ResourceCPU, qty: 2000},
			},
			wantGauges: []wantGauge{
				{domain: rackLabel, domainID: "rack-1", resource: string(corev1.ResourceCPU), value: 4000},
				{domain: corev1.LabelHostname, domainID: "node-1", resource: string(corev1.ResourceCPU), value: 4000},
			},
		},
		{
			name:   "accumulates across workloads sharing a topology domain",
			flavor: "tas-accumulate",
			ops: []gaugeOp{
				{levels: []string{rackLabel, corev1.LabelHostname}, values: []string{"rack-1", "node-1"}, resource: corev1.ResourceCPU, qty: 2000},
				{levels: []string{rackLabel, corev1.LabelHostname}, values: []string{"rack-1", "node-2"}, resource: corev1.ResourceCPU, qty: 3000},
			},
			wantGauges: []wantGauge{
				{domain: corev1.LabelHostname, domainID: "node-1", resource: string(corev1.ResourceCPU), value: 2000},
				{domain: corev1.LabelHostname, domainID: "node-2", resource: string(corev1.ResourceCPU), value: 3000},
				{domain: rackLabel, domainID: "rack-1", resource: string(corev1.ResourceCPU), value: 5000},
			},
		},
		{
			name:   "ignores ResourcePods",
			flavor: "tas-pods-skip",
			ops: []gaugeOp{
				{levels: []string{corev1.LabelHostname}, values: []string{"node-1"}, resource: corev1.ResourcePods, qty: 5},
				{sub: true, levels: []string{corev1.LabelHostname}, values: []string{"node-1"}, resource: corev1.ResourcePods, qty: 5},
			},
			wantNone: true,
		},
		{
			name:           "add skips excluded level",
			flavor:         "tas-add-excluded",
			excludedLevels: []string{corev1.LabelHostname},
			ops: []gaugeOp{
				{levels: []string{rackLabel, corev1.LabelHostname}, values: []string{"rack-1", "node-1"}, resource: corev1.ResourceCPU, qty: 4000},
			},
			wantGauges: []wantGauge{
				{domain: rackLabel, domainID: "rack-1", resource: string(corev1.ResourceCPU), value: 4000},
			},
		},
		{
			name:           "subtract skips excluded level",
			flavor:         "tas-sub-excluded",
			excludedLevels: []string{corev1.LabelHostname},
			ops: []gaugeOp{
				{levels: []string{rackLabel, corev1.LabelHostname}, values: []string{"rack-1", "node-1"}, resource: corev1.ResourceCPU, qty: 6000},
				{sub: true, levels: []string{rackLabel, corev1.LabelHostname}, values: []string{"rack-1", "node-1"}, resource: corev1.ResourceCPU, qty: 2000},
			},
			wantGauges: []wantGauge{
				{domain: rackLabel, domainID: "rack-1", resource: string(corev1.ResourceCPU), value: 4000},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() { ClearTASFlavorUsage(tc.flavor) })
			if len(tc.excludedLevels) > 0 {
				InitTASMetricsConfig(&configapi.TASMetrics{ExcludedTopologyLevels: tc.excludedLevels})
				t.Cleanup(func() { InitTASMetricsConfig(nil) })
			}
			for _, op := range tc.ops {
				if op.sub {
					SubTASDomainUsage(tc.flavor, op.levels, op.values, op.resource, op.qty)
				} else {
					AddTASDomainUsage(tc.flavor, op.levels, op.values, op.resource, op.qty)
				}
			}
			if tc.wantNone {
				expectFilteredMetricsCount(t, TASDomainUsage, 0, "flavor", string(tc.flavor))
				return
			}
			for _, g := range tc.wantGauges {
				expectGaugeValue(t, TASDomainUsage, g.value,
					"flavor", string(tc.flavor), "domain", g.domain, "domain_id", g.domainID, "resource", g.resource)
			}
			for _, excluded := range tc.excludedLevels {
				expectFilteredMetricsCount(t, TASDomainUsage, 0, "flavor", string(tc.flavor), "domain", excluded)
			}
		})
	}
}

func TestClearTASFlavorUsage(t *testing.T) {
	const (
		flavor1 = kueue.ResourceFlavorReference("tas-clear-1")
		flavor2 = kueue.ResourceFlavorReference("tas-clear-2")
	)
	t.Cleanup(func() {
		ClearTASFlavorUsage(flavor1)
		ClearTASFlavorUsage(flavor2)
	})

	AddTASDomainUsage(flavor1, []string{corev1.LabelHostname}, []string{"node-a"}, corev1.ResourceCPU, 1000)
	AddTASDomainUsage(flavor2, []string{corev1.LabelHostname}, []string{"node-b"}, corev1.ResourceCPU, 2000)

	ClearTASFlavorUsage(flavor1)

	expectFilteredMetricsCount(t, TASDomainUsage, 0, "flavor", string(flavor1))
	expectFilteredMetricsCount(t, TASDomainUsage, 1, "flavor", string(flavor2))
}

func TestInitTASMetricsConfig(t *testing.T) {
	t.Cleanup(func() { InitTASMetricsConfig(nil) })

	tests := []struct {
		name         string
		cfg          *configapi.TASMetrics
		wantExcluded map[string]bool
	}{
		{
			name:         "nil config sets no filter",
			cfg:          nil,
			wantExcluded: nil,
		},
		{
			name:         "empty ExcludedTopologyLevels sets no filter",
			cfg:          &configapi.TASMetrics{},
			wantExcluded: nil,
		},
		{
			name:         "single excluded level",
			cfg:          &configapi.TASMetrics{ExcludedTopologyLevels: []string{corev1.LabelHostname}},
			wantExcluded: map[string]bool{corev1.LabelHostname: true},
		},
		{
			name: "multiple excluded levels",
			cfg: &configapi.TASMetrics{ExcludedTopologyLevels: []string{
				corev1.LabelHostname,
				"cloud.com/rack",
			}},
			wantExcluded: map[string]bool{
				corev1.LabelHostname: true,
				"cloud.com/rack":     true,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			InitTASMetricsConfig(tc.cfg)
			if diff := cmp.Diff(tc.wantExcluded, tasExcludedLevels); diff != "" {
				t.Errorf("unexpected tasExcludedLevels (-want +got):\n%s", diff)
			}
		})
	}
}
