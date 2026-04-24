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
	"time"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/version"
)

type AdmissionResult string
type ClusterQueueStatus string

type LocalQueueReference struct {
	Name      kueue.LocalQueueName
	Namespace string
}

const (
	AdmissionResultSuccess      AdmissionResult = "success"
	AdmissionResultInadmissible AdmissionResult = "inadmissible"

	PendingStatusActive       = "active"
	PendingStatusInadmissible = "inadmissible"

	// CQStatusPending means the ClusterQueue is accepted but not yet active,
	// this can be because of:
	// - a missing ResourceFlavor referenced by the ClusterQueue
	// - a missing or inactive AdmissionCheck referenced by the ClusterQueue
	// - the ClusterQueue is stopped
	// In this state, the ClusterQueue can't admit new workloads and its quota can't be borrowed
	// by other active ClusterQueues in the cohort.
	CQStatusPending ClusterQueueStatus = "pending"
	// CQStatusActive means the ClusterQueue can admit new workloads and its quota
	// can be borrowed by other ClusterQueues in the cohort.
	CQStatusActive ClusterQueueStatus = "active"
	// CQStatusTerminating means the clusterQueue is in pending deletion.
	CQStatusTerminating ClusterQueueStatus = "terminating"
)

var (
	CQStatuses = []ClusterQueueStatus{CQStatusPending, CQStatusActive, CQStatusTerminating}

	// Metrics tied to the scheduler

	// +metricsdoc:group=health
	// +metricsdoc:labels=result="possible values are `success` or `inadmissible`",replica_role="one of `leader`, `follower`, or `standalone`"
	AdmissionAttemptsTotal *prometheus.CounterVec

	// +metricsdoc:group=health
	// +metricsdoc:labels=result="possible values are `success` or `inadmissible`",replica_role="one of `leader`, `follower`, or `standalone`"
	admissionAttemptDuration *prometheus.HistogramVec

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",replica_role="one of `leader`, `follower`, or `standalone`"
	AdmissionCyclePreemptionSkips *prometheus.GaugeVec

	// Metrics tied to the queue system.

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=git_version="git version",git_commit="git commit",build_date="build date",go_version="go version",compiler="compiler",platform="platform"
	buildInfo *prometheus.GaugeVec

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",status="status label (varies by metric)",replica_role="one of `leader`, `follower`, or `standalone`"
	PendingWorkloads *prometheus.GaugeVec

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",status="status label (varies by metric)",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueuePendingWorkloads *prometheus.GaugeVec

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",replica_role="one of `leader`, `follower`, or `standalone`"
	FinishedWorkloads *prometheus.GaugeVec

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueFinishedWorkloads *prometheus.GaugeVec

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	QuotaReservedWorkloadsTotal *prometheus.CounterVec

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueQuotaReservedWorkloadsTotal *prometheus.CounterVec

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	FinishedWorkloadsTotal *prometheus.CounterVec

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueFinishedWorkloadsTotal *prometheus.CounterVec

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	QuotaReservedWaitTime *prometheus.HistogramVec

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",reason="eviction or preemption reason",underlying_cause="root cause for eviction",replica_role="one of `leader`, `follower`, or `standalone`"
	PodsReadyToEvictedTimeSeconds *prometheus.HistogramVec

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueQuotaReservedWaitTime *prometheus.HistogramVec

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	AdmittedWorkloadsTotal *prometheus.CounterVec

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueAdmittedWorkloadsTotal *prometheus.CounterVec

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	AdmissionWaitTime *prometheus.HistogramVec

	// +metricsdoc:group=optional_wait_for_pods_ready
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	QueuedUntilReadyWaitTime *prometheus.HistogramVec

	// +metricsdoc:group=optional_wait_for_pods_ready
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	AdmittedUntilReadyWaitTime *prometheus.HistogramVec

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueAdmissionWaitTime *prometheus.HistogramVec

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	AdmissionChecksWaitTime *prometheus.HistogramVec

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueAdmissionChecksWaitTime *prometheus.HistogramVec

	// +metricsdoc:group=optional_wait_for_pods_ready
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueQueuedUntilReadyWaitTime *prometheus.HistogramVec

	// +metricsdoc:group=optional_wait_for_pods_ready
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueAdmittedUntilReadyWaitTime *prometheus.HistogramVec

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",reason="eviction or preemption reason",underlying_cause="root cause for eviction",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	EvictedWorkloadsTotal *prometheus.CounterVec

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",replica_role="one of `leader`, `follower`, or `standalone`"
	ReplacedWorkloadSlicesTotal *prometheus.CounterVec

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",reason="eviction or preemption reason",underlying_cause="root cause for eviction",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueEvictedWorkloadsTotal *prometheus.CounterVec

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",reason="eviction or preemption reason",underlying_cause="root cause for eviction",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	EvictedWorkloadsOnceTotal *prometheus.CounterVec

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=preempting_cluster_queue="the ClusterQueue executing preemption",reason="eviction or preemption reason",replica_role="one of `leader`, `follower`, or `standalone`"
	PreemptedWorkloadsTotal *prometheus.CounterVec

	// Metrics tied to the cache.

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",replica_role="one of `leader`, `follower`, or `standalone`"
	ReservingActiveWorkloads *prometheus.GaugeVec

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueReservingActiveWorkloads *prometheus.GaugeVec

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",replica_role="one of `leader`, `follower`, or `standalone`"
	AdmittedActiveWorkloads *prometheus.GaugeVec

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueAdmittedActiveWorkloads *prometheus.GaugeVec

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",status="one of `pending`, `active`, or `terminated`",replica_role="one of `leader`, `follower`, or `standalone`"
	ClusterQueueByStatus *prometheus.GaugeVec

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",active="one of `True`, `False`, or `Unknown`",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueByStatus *prometheus.GaugeVec

	// Optional cluster queue metrics

	// +metricsdoc:group=optional_clusterqueue_resources
	// +metricsdoc:labels=cohort="the name of the Cohort",cluster_queue="the name of the ClusterQueue",flavor="the resource flavor name",resource="the resource name",replica_role="one of `leader`, `follower`, or `standalone`"
	ClusterQueueResourceReservations *prometheus.GaugeVec

	// +metricsdoc:group=optional_clusterqueue_resources
	// +metricsdoc:labels=cohort="the name of the Cohort",cluster_queue="the name of the ClusterQueue",flavor="the resource flavor name",resource="the resource name",replica_role="one of `leader`, `follower`, or `standalone`"
	ClusterQueueResourceUsage *prometheus.GaugeVec

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",flavor="the resource flavor name",resource="the resource name",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueResourceReservations *prometheus.GaugeVec

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",flavor="the resource flavor name",resource="the resource name",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueResourceUsage *prometheus.GaugeVec

	// +metricsdoc:group=optional_clusterqueue_resources
	// +metricsdoc:labels=cohort="the name of the Cohort",cluster_queue="the name of the ClusterQueue",flavor="the resource flavor name",resource="the resource name",replica_role="one of `leader`, `follower`, or `standalone`"
	ClusterQueueResourceNominalQuota *prometheus.GaugeVec

	// +metricsdoc:group=optional_clusterqueue_resources
	// +metricsdoc:labels=cohort="the name of the Cohort",cluster_queue="the name of the ClusterQueue",flavor="the resource flavor name",resource="the resource name",replica_role="one of `leader`, `follower`, or `standalone`"
	ClusterQueueResourceBorrowingLimit *prometheus.GaugeVec

	// +metricsdoc:group=optional_clusterqueue_resources
	// +metricsdoc:labels=cohort="the name of the Cohort",cluster_queue="the name of the ClusterQueue",flavor="the resource flavor name",resource="the resource name",replica_role="one of `leader`, `follower`, or `standalone`"
	ClusterQueueResourceLendingLimit *prometheus.GaugeVec

	// +metricsdoc:group=optional_clusterqueue_resources
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",cohort="the name of the Cohort",replica_role="one of `leader`, `follower`, or `standalone`"
	ClusterQueueWeightedShare *prometheus.GaugeVec

	// +metricsdoc:group=cohort
	// +metricsdoc:labels=cohort="the name of the Cohort",replica_role="one of `leader`, `follower`, or `standalone`"
	CohortWeightedShare *prometheus.GaugeVec

	// +metricsdoc:group=cohort
	// +metricsdoc:labels=cohort="the name of the Cohort",flavor="the resource flavor name",resource="the resource name",replica_role="one of `leader`, `follower`, or `standalone`"
	CohortSubtreeQuota *prometheus.GaugeVec

	// +metricsdoc:group=cohort
	// +metricsdoc:labels=cohort="the name of the Cohort",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	CohortSubtreeAdmittedWorkloadsTotal *prometheus.CounterVec

	// +metricsdoc:group=cohort
	// +metricsdoc:labels=cohort="the name of the Cohort",flavor="the resource flavor name",resource="the resource name",replica_role="one of `leader`, `follower`, or `standalone`"
	CohortSubtreeResourceReservations *prometheus.GaugeVec

	// +metricsdoc:group=cohort
	// +metricsdoc:labels=cohort="the name of the Cohort",replica_role="one of `leader`, `follower`, or `standalone`"
	CohortSubtreeAdmittedActiveWorkloads *prometheus.GaugeVec
)

type gaugeCleanupScope uint8

const (
	gaugeCleanupScopeRole gaugeCleanupScope = iota
	gaugeCleanupScopeClusterQueue
	gaugeCleanupScopeClusterQueueLabelChange
	gaugeCleanupScopeClusterQueueCache
	gaugeCleanupScopeClusterQueueResource
	gaugeCleanupScopeLocalQueue
	gaugeCleanupScopeLocalQueueCache
	gaugeCleanupScopeLocalQueueResource
	gaugeCleanupScopeCohort
)

var gaugeVecsByScope map[gaugeCleanupScope][]*prometheus.GaugeVec

func trackGaugeVec(g *prometheus.GaugeVec, scopes ...gaugeCleanupScope) *prometheus.GaugeVec {
	gaugeVecsByScope[gaugeCleanupScopeRole] = append(gaugeVecsByScope[gaugeCleanupScopeRole], g)
	for _, scope := range scopes {
		gaugeVecsByScope[scope] = append(gaugeVecsByScope[scope], g)
	}
	return g
}

func InitMetricVectors(extraLabels []string) {
	gaugeVecsByScope = make(map[gaugeCleanupScope][]*prometheus.GaugeVec)

	AdmissionAttemptsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: constants.KueueName,
			Name:      "admission_attempts_total",
			Help: `The total number of attempts to admit workloads.
Each admission attempt might try to admit more than one workload.
The label 'result' can have the following values:
- 'success' means that at least one workload was admitted.,
- 'inadmissible' means that no workload was admitted.`,
		}, []string{"result", "replica_role"},
	)

	admissionAttemptDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "admission_attempt_duration_seconds",
			Help: `The latency of an admission attempt.
The label 'result' can have the following values:
- 'success' means that at least one workload was admitted.,
- 'inadmissible' means that no workload was admitted.`,
		}, []string{"result", "replica_role"},
	)

	AdmissionCyclePreemptionSkips = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "admission_cycle_preemption_skips",
			Help: "The number of Workloads in the ClusterQueue that got preemption candidates " +
				"but had to be skipped because other ClusterQueues needed the same resources in the same cycle",
		}, append([]string{"cluster_queue", "replica_role"}, extraLabels...),
	)
	trackGaugeVec(AdmissionCyclePreemptionSkips, gaugeCleanupScopeClusterQueue)

	buildInfo = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "build_info",
			Help:      "Kueue build information. 1 labeled by git version, git commit, build date, go version, compiler, platform",
		},
		[]string{"git_version", "git_commit", "build_date", "go_version", "compiler", "platform"},
	)
	versionInfo := version.Get()
	buildInfo.WithLabelValues(versionInfo.GitVersion, versionInfo.GitCommit, versionInfo.BuildDate, versionInfo.GoVersion, versionInfo.Compiler, versionInfo.Platform).Set(1)

	PendingWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "pending_workloads",
			Help: `The number of pending workloads, per 'cluster_queue' and 'status'.
'status' can have the following values:
- "active" means that the workloads are in the admission queue.
- "inadmissible" means there was a failed admission attempt for these workloads and they won't be retried until cluster conditions, which could make this workload admissible, change`,
		}, append([]string{"cluster_queue", "status", "replica_role"}, extraLabels...),
	)
	trackGaugeVec(PendingWorkloads, gaugeCleanupScopeClusterQueue)

	LocalQueuePendingWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_pending_workloads",
			Help: `The number of pending workloads, per 'local_queue' and 'status'.
'status' can have the following values:
- "active" means that the workloads are in the admission queue.
- "inadmissible" means there was a failed admission attempt for these workloads and they won't be retried until cluster conditions, which could make this workload admissible, change`,
		}, append([]string{"name", "namespace", "status", "replica_role"}, extraLabels...),
	)
	trackGaugeVec(LocalQueuePendingWorkloads, gaugeCleanupScopeLocalQueue)

	FinishedWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "finished_workloads",
			Help:      `The number of finished workloads per 'cluster_queue'.`,
		}, append([]string{"cluster_queue", "replica_role"}, extraLabels...),
	)
	trackGaugeVec(FinishedWorkloads, gaugeCleanupScopeClusterQueue)

	LocalQueueFinishedWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_finished_workloads",
			Help:      `The number of finished workloads, per 'local_queue'.`,
		}, append([]string{"name", "namespace", "replica_role"}, extraLabels...),
	)
	trackGaugeVec(LocalQueueFinishedWorkloads, gaugeCleanupScopeLocalQueue)

	QuotaReservedWorkloadsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: constants.KueueName,
			Name:      "quota_reserved_workloads_total",
			Help:      "The total number of quota reserved workloads per 'cluster_queue'",
		}, append([]string{"cluster_queue", "priority_class", "replica_role"}, extraLabels...),
	)

	LocalQueueQuotaReservedWorkloadsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_quota_reserved_workloads_total",
			Help:      "The total number of quota reserved workloads per 'local_queue'",
		}, append([]string{"name", "namespace", "priority_class", "replica_role"}, extraLabels...),
	)

	FinishedWorkloadsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: constants.KueueName,
			Name:      "finished_workloads_total",
			Help:      "The total number of finished workloads per 'cluster_queue'",
		}, append([]string{"cluster_queue", "priority_class", "replica_role"}, extraLabels...),
	)

	LocalQueueFinishedWorkloadsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_finished_workloads_total",
			Help:      "The total number of finished workloads per 'local_queue'",
		}, append([]string{"name", "namespace", "priority_class", "replica_role"}, extraLabels...),
	)

	QuotaReservedWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "quota_reserved_wait_time_seconds",
			Help:      "The time between a workload was created or requeued until it got quota reservation, per 'cluster_queue'",
			Buckets:   generateExponentialBuckets(14),
		}, append([]string{"cluster_queue", "priority_class", "replica_role"}, extraLabels...),
	)

	PodsReadyToEvictedTimeSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "pods_ready_to_evicted_time_seconds",
			Help: `The number of seconds between a workload's pods being ready and eviction workloads per 'cluster_queue',
The label 'reason' can have the following values:
- "Preempted" means that the workload was evicted in order to free resources for a workload with a higher priority or reclamation of nominal quota.
- "PodsReadyTimeout" means that the eviction took place due to a PodsReady timeout.
- "AdmissionCheck" means that the workload was evicted because at least one admission check transitioned to False.
- "ClusterQueueStopped" means that the workload was evicted because the ClusterQueue is stopped.
- "LocalQueueStopped" means that the workload was evicted because the LocalQueue is stopped.
- "NodeFailures" means that the workload was evicted due to node failures when using TopologyAwareScheduling.
- "Deactivated" means that the workload was evicted because spec.active is set to false.
The label 'underlying_cause' can have the following values:
- "" means that the value in 'reason' label is the root cause for eviction.
- "AdmissionCheck" means that the workload was evicted by Kueue due to a rejected admission check.
- "MaximumExecutionTimeExceeded" means that the workload was evicted by Kueue due to maximum execution time exceeded.
- "RequeuingLimitExceeded" means that the workload was evicted by Kueue due to requeuing limit exceeded.`,
			Buckets: generateExponentialBuckets(14),
		}, append([]string{"cluster_queue", "reason", "underlying_cause", "replica_role"}, extraLabels...),
	)

	LocalQueueQuotaReservedWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_quota_reserved_wait_time_seconds",
			Help:      "The time between a workload was created or requeued until it got quota reservation, per 'local_queue'",
			Buckets:   generateExponentialBuckets(14),
		}, append([]string{"name", "namespace", "priority_class", "replica_role"}, extraLabels...),
	)

	AdmittedWorkloadsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: constants.KueueName,
			Name:      "admitted_workloads_total",
			Help:      "The total number of admitted workloads per 'cluster_queue'",
		}, append([]string{"cluster_queue", "priority_class", "replica_role"}, extraLabels...),
	)

	LocalQueueAdmittedWorkloadsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_admitted_workloads_total",
			Help:      "The total number of admitted workloads per 'local_queue'",
		}, append([]string{"name", "namespace", "priority_class", "replica_role"}, extraLabels...),
	)

	AdmissionWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "admission_wait_time_seconds",
			Help:      "The time between a workload was created or requeued until admission, per 'cluster_queue'",
			Buckets:   generateExponentialBuckets(16),
		}, append([]string{"cluster_queue", "priority_class", "replica_role"}, extraLabels...),
	)

	QueuedUntilReadyWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "ready_wait_time_seconds",
			Help:      "The time between a workload was created or requeued until ready, per 'cluster_queue'",
			Buckets:   generateExponentialBuckets(14),
		}, append([]string{"cluster_queue", "priority_class", "replica_role"}, extraLabels...),
	)

	AdmittedUntilReadyWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "admitted_until_ready_wait_time_seconds",
			Help:      "The time between a workload was admitted until ready, per 'cluster_queue'",
			Buckets:   generateExponentialBuckets(14),
		}, append([]string{"cluster_queue", "priority_class", "replica_role"}, extraLabels...),
	)

	LocalQueueAdmissionWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_admission_wait_time_seconds",
			Help:      "The time between a workload was created or requeued until admission, per 'local_queue'",
			Buckets:   generateExponentialBuckets(14),
		}, append([]string{"name", "namespace", "priority_class", "replica_role"}, extraLabels...),
	)

	AdmissionChecksWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "admission_checks_wait_time_seconds",
			Help:      "The time from when a workload got the quota reservation until admission, per 'cluster_queue'",
			Buckets:   generateExponentialBuckets(14),
		}, append([]string{"cluster_queue", "priority_class", "replica_role"}, extraLabels...),
	)

	LocalQueueAdmissionChecksWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_admission_checks_wait_time_seconds",
			Help:      "The time from when a workload got the quota reservation until admission, per 'local_queue'",
			Buckets:   generateExponentialBuckets(14),
		}, append([]string{"name", "namespace", "priority_class", "replica_role"}, extraLabels...),
	)

	LocalQueueQueuedUntilReadyWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_ready_wait_time_seconds",
			Help:      "The time between a workload was created or requeued until ready, per 'local_queue'",
			Buckets:   generateExponentialBuckets(14),
		}, append([]string{"name", "namespace", "priority_class", "replica_role"}, extraLabels...),
	)

	LocalQueueAdmittedUntilReadyWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_admitted_until_ready_wait_time_seconds",
			Help:      "The time between a workload was admitted until ready, per 'local_queue'",
			Buckets:   generateExponentialBuckets(14),
		}, append([]string{"name", "namespace", "priority_class", "replica_role"}, extraLabels...),
	)

	EvictedWorkloadsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: constants.KueueName,
			Name:      "evicted_workloads_total",
			Help: `The number of evicted workloads per 'cluster_queue',
The label 'reason' can have the following values:
- "Preempted" means that the workload was evicted in order to free resources for a workload with a higher priority or reclamation of nominal quota.
- "PodsReadyTimeout" means that the eviction took place due to a PodsReady timeout.
- "AdmissionCheck" means that the workload was evicted because at least one admission check transitioned to False.
- "ClusterQueueStopped" means that the workload was evicted because the ClusterQueue is stopped.
- "LocalQueueStopped" means that the workload was evicted because the LocalQueue is stopped.
- "NodeFailures" means that the workload was evicted due to node failures when using TopologyAwareScheduling.
- "Deactivated" means that the workload was evicted because spec.active is set to false.
The label 'underlying_cause' can have the following values:
- "" means that the value in 'reason' label is the root cause for eviction.
- "AdmissionCheck" means that the workload was evicted by Kueue due to a rejected admission check.
- "MaximumExecutionTimeExceeded" means that the workload was evicted by Kueue due to maximum execution time exceeded.
- "RequeuingLimitExceeded" means that the workload was evicted by Kueue due to requeuing limit exceeded.`,
		}, append([]string{"cluster_queue", "reason", "underlying_cause", "priority_class", "replica_role"}, extraLabels...),
	)

	ReplacedWorkloadSlicesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: constants.KueueName,
			Name:      "replaced_workload_slices_total",
			Help:      `The number of replaced workload slices per 'cluster_queue'`,
		}, append([]string{"cluster_queue", "replica_role"}, extraLabels...),
	)

	LocalQueueEvictedWorkloadsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_evicted_workloads_total",
			Help: `The number of evicted workloads per 'local_queue',
The label 'reason' can have the following values:
- "Preempted" means that the workload was evicted in order to free resources for a workload with a higher priority or reclamation of nominal quota.
- "PodsReadyTimeout" means that the eviction took place due to a PodsReady timeout.
- "AdmissionCheck" means that the workload was evicted because at least one admission check transitioned to False.
- "ClusterQueueStopped" means that the workload was evicted because the ClusterQueue is stopped.
- "LocalQueueStopped" means that the workload was evicted because the LocalQueue is stopped.
- "NodeFailures" means that the workload was evicted due to node failures when using TopologyAwareScheduling.
- "Deactivated" means that the workload was evicted because spec.active is set to false.
The label 'underlying_cause' can have the following values:
- "" means that the value in 'reason' label is the root cause for eviction.
- "AdmissionCheck" means that the workload was evicted by Kueue due to a rejected admission check.
- "MaximumExecutionTimeExceeded" means that the workload was evicted by Kueue due to maximum execution time exceeded.
- "RequeuingLimitExceeded" means that the workload was evicted by Kueue due to requeuing limit exceeded.`,
		}, append([]string{"name", "namespace", "reason", "underlying_cause", "priority_class", "replica_role"}, extraLabels...),
	)

	EvictedWorkloadsOnceTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: constants.KueueName,
			Name:      "evicted_workloads_once_total",
			Help: `The number of unique workload evictions per 'cluster_queue',
The label 'reason' can have the following values:
- "Preempted" means that the workload was evicted in order to free resources for a workload with a higher priority or reclamation of nominal quota.
- "PodsReadyTimeout" means that the eviction took place due to a PodsReady timeout.
- "AdmissionCheck" means that the workload was evicted because at least one admission check transitioned to False.
- "ClusterQueueStopped" means that the workload was evicted because the ClusterQueue is stopped.
- "LocalQueueStopped" means that the workload was evicted because the LocalQueue is stopped.
- "NodeFailures" means that the workload was evicted due to node failures when using TopologyAwareScheduling.
- "Deactivated" means that the workload was evicted because spec.active is set to false.
The label 'underlying_cause' can have the following values:
- "" means that the value in 'reason' label is the root cause for eviction.
- "WaitForStart" means that the pods have not been ready since admission, or the workload is not admitted.
- "WaitForRecovery" means that the Pods were ready since the workload admission, but some pod has failed.
- "AdmissionCheck" means that the workload was evicted by Kueue due to a rejected admission check.
- "MaximumExecutionTimeExceeded" means that the workload was evicted by Kueue due to maximum execution time exceeded.
- "RequeuingLimitExceeded" means that the workload was evicted by Kueue due to requeuing limit exceeded.`,
		}, append([]string{"cluster_queue", "reason", "underlying_cause", "priority_class", "replica_role"}, extraLabels...),
	)

	PreemptedWorkloadsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: constants.KueueName,
			Name:      "preempted_workloads_total",
			Help: `The number of preempted workloads per 'preempting_cluster_queue',
The label 'reason' can have the following values:
- "InClusterQueue" means that the workload was preempted by a workload in the same ClusterQueue.
- "InCohortReclamation" means that the workload was preempted by a workload in the same cohort due to reclamation of nominal quota.
- "InCohortFairSharing" means that the workload was preempted by a workload in the same cohort Fair Sharing.
- "InCohortReclaimWhileBorrowing" means that the workload was preempted by a workload in the same cohort due to reclamation of nominal quota while borrowing.`,
		}, append([]string{"preempting_cluster_queue", "reason", "replica_role"}, extraLabels...),
	)

	ReservingActiveWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "reserving_active_workloads",
			Help:      "The number of Workloads that are reserving quota, per 'cluster_queue'",
		}, append([]string{"cluster_queue", "replica_role"}, extraLabels...),
	)
	trackGaugeVec(ReservingActiveWorkloads, gaugeCleanupScopeClusterQueueCache)

	LocalQueueReservingActiveWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_reserving_active_workloads",
			Help:      "The number of Workloads that are reserving quota, per 'localQueue'",
		}, append([]string{"name", "namespace", "replica_role"}, extraLabels...),
	)
	trackGaugeVec(LocalQueueReservingActiveWorkloads, gaugeCleanupScopeLocalQueueCache)

	AdmittedActiveWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "admitted_active_workloads",
			Help:      "The number of admitted Workloads that are active, per 'cluster_queue'",
		}, append([]string{"cluster_queue", "replica_role"}, extraLabels...),
	)
	trackGaugeVec(AdmittedActiveWorkloads, gaugeCleanupScopeClusterQueueCache)

	LocalQueueAdmittedActiveWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_admitted_active_workloads",
			Help:      "The number of admitted Workloads that are active, per 'localQueue'",
		}, append([]string{"name", "namespace", "replica_role"}, extraLabels...),
	)
	trackGaugeVec(LocalQueueAdmittedActiveWorkloads, gaugeCleanupScopeLocalQueueCache)

	ClusterQueueByStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "cluster_queue_status",
			Help: `Reports 'cluster_queue' with its 'status' (with possible values 'pending', 'active' or 'terminated').
For a ClusterQueue, the metric only reports a value of 1 for one of the statuses.`,
		}, append([]string{"cluster_queue", "status", "replica_role"}, extraLabels...),
	)
	trackGaugeVec(ClusterQueueByStatus, gaugeCleanupScopeClusterQueueCache)

	LocalQueueByStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_status",
			Help: `Reports 'localQueue' with its 'active' status (with possible values 'True', 'False', or 'Unknown').
For a LocalQueue, the metric only reports a value of 1 for one of the statuses.`,
		}, append([]string{"name", "namespace", "active", "replica_role"}, extraLabels...),
	)
	trackGaugeVec(LocalQueueByStatus, gaugeCleanupScopeLocalQueueCache)

	ClusterQueueResourceReservations = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "cluster_queue_resource_reservation",
			Help:      `Reports the cluster_queue's total resource reservation within all the flavors`,
		}, append([]string{"cohort", "cluster_queue", "flavor", "resource", "replica_role"}, extraLabels...),
	)
	trackGaugeVec(ClusterQueueResourceReservations, gaugeCleanupScopeClusterQueueResource)

	ClusterQueueResourceUsage = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "cluster_queue_resource_usage",
			Help:      `Reports the cluster_queue's total resource usage within all the flavors`,
		}, append([]string{"cohort", "cluster_queue", "flavor", "resource", "replica_role"}, extraLabels...),
	)
	trackGaugeVec(ClusterQueueResourceUsage, gaugeCleanupScopeClusterQueueResource)

	LocalQueueResourceReservations = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_resource_reservation",
			Help:      `Reports the localQueue's total resource reservation within all the flavors`,
		}, append([]string{"name", "namespace", "flavor", "resource", "replica_role"}, extraLabels...),
	)
	trackGaugeVec(LocalQueueResourceReservations, gaugeCleanupScopeLocalQueueResource)

	LocalQueueResourceUsage = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_resource_usage",
			Help:      `Reports the localQueue's total resource usage within all the flavors`,
		}, append([]string{"name", "namespace", "flavor", "resource", "replica_role"}, extraLabels...),
	)
	trackGaugeVec(LocalQueueResourceUsage, gaugeCleanupScopeLocalQueueResource)

	ClusterQueueResourceNominalQuota = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "cluster_queue_nominal_quota",
			Help:      `Reports the cluster_queue's resource nominal quota within all the flavors`,
		}, append([]string{"cohort", "cluster_queue", "flavor", "resource", "replica_role"}, extraLabels...),
	)
	trackGaugeVec(ClusterQueueResourceNominalQuota, gaugeCleanupScopeClusterQueueResource)

	ClusterQueueResourceBorrowingLimit = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "cluster_queue_borrowing_limit",
			Help:      `Reports the cluster_queue's resource borrowing limit within all the flavors`,
		}, append([]string{"cohort", "cluster_queue", "flavor", "resource", "replica_role"}, extraLabels...),
	)
	trackGaugeVec(ClusterQueueResourceBorrowingLimit, gaugeCleanupScopeClusterQueueResource)

	ClusterQueueResourceLendingLimit = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "cluster_queue_lending_limit",
			Help:      `Reports the cluster_queue's resource lending limit within all the flavors`,
		}, append([]string{"cohort", "cluster_queue", "flavor", "resource", "replica_role"}, extraLabels...),
	)
	trackGaugeVec(ClusterQueueResourceLendingLimit, gaugeCleanupScopeClusterQueueResource)

	ClusterQueueWeightedShare = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "cluster_queue_weighted_share",
			Help: `Reports a value that representing the maximum of the ratios of usage above nominal
quota to the lendable resources in the cohort, among all the resources provided by
the ClusterQueue, and divided by the weight.
If zero, it means that the usage of the ClusterQueue is below the nominal quota.
If the ClusterQueue has a weight of zero and is borrowing, this will return NaN.`,
		}, append([]string{"cluster_queue", "cohort", "replica_role"}, extraLabels...),
	)
	trackGaugeVec(ClusterQueueWeightedShare, gaugeCleanupScopeClusterQueueLabelChange)

	CohortWeightedShare = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "cohort_weighted_share",
			Help: `Reports a value that representing the maximum of the ratios of usage above nominal
quota to the lendable resources in the Cohort, among all the resources provided by
the Cohort, and divided by the weight.
If zero, it means that the usage of the Cohort is below the nominal quota.
If the Cohort has a weight of zero and is borrowing, this will return NaN.`,
		}, append([]string{"cohort", "replica_role"}, extraLabels...),
	)
	trackGaugeVec(CohortWeightedShare, gaugeCleanupScopeCohort)

	CohortSubtreeQuota = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "cohort_subtree_quota",
			Help:      `Reports the cohort's nominal quota aggregated within the cohort's subtree. The values are reported per resource and flavor`,
		}, append([]string{"cohort", "flavor", "resource", "replica_role"}, extraLabels...),
	)
	trackGaugeVec(CohortSubtreeQuota, gaugeCleanupScopeCohort)

	CohortSubtreeAdmittedWorkloadsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: constants.KueueName,
			Name:      "cohort_subtree_admitted_workloads_total",
			Help:      "The total number of admitted workloads per cohort's subtree",
		}, append([]string{"cohort", "priority_class", "replica_role"}, extraLabels...),
	)

	CohortSubtreeResourceReservations = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "cohort_subtree_resource_reservations",
			Help:      `Reports the cohort's resource reservations aggregated within the cohort's subtree. The values are reported per resource and flavor`,
		}, append([]string{"cohort", "flavor", "resource", "replica_role"}, extraLabels...),
	)
	trackGaugeVec(CohortSubtreeResourceReservations, gaugeCleanupScopeCohort)

	CohortSubtreeAdmittedActiveWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "cohort_subtree_admitted_active_workloads",
			Help:      "The number of admitted Workloads that are active, per cohort's subtree",
		}, append([]string{"cohort", "replica_role"}, extraLabels...),
	)
	trackGaugeVec(CohortSubtreeAdmittedActiveWorkloads)
}

func init() {
	InitMetricVectors(nil)
}

func generateExponentialBuckets(count int) []float64 {
	return append([]float64{1}, prometheus.ExponentialBuckets(2.5, 2, count-1)...)
}

func AdmissionAttempt(result AdmissionResult, duration time.Duration, tracker *roletracker.RoleTracker) {
	role := roletracker.GetRole(tracker)
	AdmissionAttemptsTotal.WithLabelValues(string(result), role).Inc()
	admissionAttemptDuration.WithLabelValues(string(result), role).Observe(duration.Seconds())
}

func QuotaReservedWorkload(cqName kueue.ClusterQueueReference, priorityClass string, waitTime time.Duration, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(cqName), priorityClass, roletracker.GetRole(tracker)}, customLabelValues...)
	QuotaReservedWorkloadsTotal.WithLabelValues(labels...).Inc()
	QuotaReservedWaitTime.WithLabelValues(labels...).Observe(waitTime.Seconds())
}

func LocalQueueQuotaReservedWorkload(lq LocalQueueReference, priorityClass string, waitTime time.Duration, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(lq.Name), lq.Namespace, priorityClass, roletracker.GetRole(tracker)}, customLabelValues...)
	LocalQueueQuotaReservedWorkloadsTotal.WithLabelValues(labels...).Inc()
	LocalQueueQuotaReservedWaitTime.WithLabelValues(labels...).Observe(waitTime.Seconds())
}

// IncrementFinishedWorkloadTotal increases the counter of finished workloads
// for the given ClusterQueue, priority class, and workload role.
func IncrementFinishedWorkloadTotal(cqName kueue.ClusterQueueReference, priorityClass string, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(cqName), priorityClass, roletracker.GetRole(tracker)}, customLabelValues...)
	FinishedWorkloadsTotal.WithLabelValues(labels...).Inc()
}

// IncrementLocalQueueFinishedWorkloadTotal increases the counter of finished workloads
// for the given LocalQueue, priority class, and workload role.
func IncrementLocalQueueFinishedWorkloadTotal(lq LocalQueueReference, priorityClass string, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(lq.Name), lq.Namespace, priorityClass, roletracker.GetRole(tracker)}, customLabelValues...)
	LocalQueueFinishedWorkloadsTotal.WithLabelValues(labels...).Inc()
}

// ReportFinishedWorkloads sets the current total number of finished workloads
// for the given ClusterQueue and workload role (gauge).
func ReportFinishedWorkloads(cqName kueue.ClusterQueueReference, count int, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(cqName), roletracker.GetRole(tracker)}, customLabelValues...)
	FinishedWorkloads.WithLabelValues(labels...).Set(float64(count))
}

// ReportLocalQueueFinishedWorkloads sets the current total number of finished workloads
// for the given LocalQueue and workload role (gauge).
func ReportLocalQueueFinishedWorkloads(lq LocalQueueReference, count int, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(lq.Name), lq.Namespace, roletracker.GetRole(tracker)}, customLabelValues...)
	LocalQueueFinishedWorkloads.WithLabelValues(labels...).Set(float64(count))
}

func AdmittedWorkload(cqName kueue.ClusterQueueReference, priorityClass string, waitTime time.Duration, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(cqName), priorityClass, roletracker.GetRole(tracker)}, customLabelValues...)
	AdmittedWorkloadsTotal.WithLabelValues(labels...).Inc()
	AdmissionWaitTime.WithLabelValues(labels...).Observe(waitTime.Seconds())
}

func LocalQueueAdmittedWorkload(lq LocalQueueReference, priorityClass string, waitTime time.Duration, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(lq.Name), lq.Namespace, priorityClass, roletracker.GetRole(tracker)}, customLabelValues...)
	LocalQueueAdmittedWorkloadsTotal.WithLabelValues(labels...).Inc()
	LocalQueueAdmissionWaitTime.WithLabelValues(labels...).Observe(waitTime.Seconds())
}

func ReportAdmissionChecksWaitTime(cqName kueue.ClusterQueueReference, priorityClass string, waitTime time.Duration, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(cqName), priorityClass, roletracker.GetRole(tracker)}, customLabelValues...)
	AdmissionChecksWaitTime.WithLabelValues(labels...).Observe(waitTime.Seconds())
}

func ReportLocalQueueAdmissionChecksWaitTime(lq LocalQueueReference, priorityClass string, waitTime time.Duration, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(lq.Name), lq.Namespace, priorityClass, roletracker.GetRole(tracker)}, customLabelValues...)
	LocalQueueAdmissionChecksWaitTime.WithLabelValues(labels...).Observe(waitTime.Seconds())
}

func ReadyWaitTime(cqName kueue.ClusterQueueReference, priorityClass string, waitTime time.Duration, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(cqName), priorityClass, roletracker.GetRole(tracker)}, customLabelValues...)
	QueuedUntilReadyWaitTime.WithLabelValues(labels...).Observe(waitTime.Seconds())
}

func LocalQueueReadyWaitTime(lq LocalQueueReference, priorityClass string, waitTime time.Duration, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(lq.Name), lq.Namespace, priorityClass, roletracker.GetRole(tracker)}, customLabelValues...)
	LocalQueueQueuedUntilReadyWaitTime.WithLabelValues(labels...).Observe(waitTime.Seconds())
}

func ReportAdmittedUntilReadyWaitTime(cqName kueue.ClusterQueueReference, priorityClass string, waitTime time.Duration, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(cqName), priorityClass, roletracker.GetRole(tracker)}, customLabelValues...)
	AdmittedUntilReadyWaitTime.WithLabelValues(labels...).Observe(waitTime.Seconds())
}

func ReportLocalQueueAdmittedUntilReadyWaitTime(lq LocalQueueReference, priorityClass string, waitTime time.Duration, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(lq.Name), lq.Namespace, priorityClass, roletracker.GetRole(tracker)}, customLabelValues...)
	LocalQueueAdmittedUntilReadyWaitTime.WithLabelValues(labels...).Observe(waitTime.Seconds())
}

func ReportPendingWorkloads(cqName kueue.ClusterQueueReference, active, inadmissible int, customLabelValues []string, tracker *roletracker.RoleTracker) {
	role := roletracker.GetRole(tracker)
	activeLabels := append([]string{string(cqName), PendingStatusActive, role}, customLabelValues...)
	inadmissibleLabels := append([]string{string(cqName), PendingStatusInadmissible, role}, customLabelValues...)
	PendingWorkloads.WithLabelValues(activeLabels...).Set(float64(active))
	PendingWorkloads.WithLabelValues(inadmissibleLabels...).Set(float64(inadmissible))
}

func ReportLocalQueuePendingWorkloads(lq LocalQueueReference, active, inadmissible int, customLabelValues []string, tracker *roletracker.RoleTracker) {
	role := roletracker.GetRole(tracker)
	activeLabels := append([]string{string(lq.Name), lq.Namespace, PendingStatusActive, role}, customLabelValues...)
	inadmissibleLabels := append([]string{string(lq.Name), lq.Namespace, PendingStatusInadmissible, role}, customLabelValues...)
	LocalQueuePendingWorkloads.WithLabelValues(activeLabels...).Set(float64(active))
	LocalQueuePendingWorkloads.WithLabelValues(inadmissibleLabels...).Set(float64(inadmissible))
}

func ReportEvictedWorkloads(cqName kueue.ClusterQueueReference, evictionReason, underlyingCause, priorityClass string, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(cqName), evictionReason, underlyingCause, priorityClass, roletracker.GetRole(tracker)}, customLabelValues...)
	EvictedWorkloadsTotal.WithLabelValues(labels...).Inc()
}

func ReportReplacedWorkloadSlices(cqName kueue.ClusterQueueReference, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(cqName), roletracker.GetRole(tracker)}, customLabelValues...)
	ReplacedWorkloadSlicesTotal.WithLabelValues(labels...).Inc()
}

func ReportLocalQueueEvictedWorkloads(lq LocalQueueReference, reason, underlyingCause string, priorityClass string, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(lq.Name), lq.Namespace, reason, underlyingCause, priorityClass, roletracker.GetRole(tracker)}, customLabelValues...)
	LocalQueueEvictedWorkloadsTotal.WithLabelValues(labels...).Inc()
}

func ReportEvictedWorkloadsOnce(cqName kueue.ClusterQueueReference, reason, underlyingCause, priorityClass string, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(cqName), reason, underlyingCause, priorityClass, roletracker.GetRole(tracker)}, customLabelValues...)
	EvictedWorkloadsOnceTotal.WithLabelValues(labels...).Inc()
}

func ReportPreemption(preemptingCqName kueue.ClusterQueueReference, preemptingReason string, targetCqName kueue.ClusterQueueReference, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(preemptingCqName), preemptingReason, roletracker.GetRole(tracker)}, customLabelValues...)
	PreemptedWorkloadsTotal.WithLabelValues(labels...).Inc()
}

func LQRefFromWorkload(wl *kueue.Workload) LocalQueueReference {
	return LocalQueueReference{
		Name:      wl.Spec.QueueName,
		Namespace: wl.Namespace,
	}
}

func ClearClusterQueueMetrics(cq kueue.ClusterQueueReference) {
	cqName := string(cq)
	clearScopedGaugeMetrics(gaugeCleanupScopeClusterQueue, prometheus.Labels{"cluster_queue": cqName})
	QuotaReservedWorkloadsTotal.DeletePartialMatch(prometheus.Labels{"cluster_queue": cqName})
	QuotaReservedWaitTime.DeletePartialMatch(prometheus.Labels{"cluster_queue": cqName})
	FinishedWorkloadsTotal.DeletePartialMatch(prometheus.Labels{"cluster_queue": cqName})
	PodsReadyToEvictedTimeSeconds.DeletePartialMatch(prometheus.Labels{"cluster_queue": cqName})
	AdmittedWorkloadsTotal.DeletePartialMatch(prometheus.Labels{"cluster_queue": cqName})
	AdmissionWaitTime.DeletePartialMatch(prometheus.Labels{"cluster_queue": cqName})
	AdmissionChecksWaitTime.DeletePartialMatch(prometheus.Labels{"cluster_queue": cqName})
	QueuedUntilReadyWaitTime.DeletePartialMatch(prometheus.Labels{"cluster_queue": cqName})
	AdmittedUntilReadyWaitTime.DeletePartialMatch(prometheus.Labels{"cluster_queue": cqName})
	EvictedWorkloadsTotal.DeletePartialMatch(prometheus.Labels{"cluster_queue": cqName})
	EvictedWorkloadsOnceTotal.DeletePartialMatch(prometheus.Labels{"cluster_queue": cqName})
	PreemptedWorkloadsTotal.DeletePartialMatch(prometheus.Labels{"preempting_cluster_queue": cqName})
}

func ClearClusterQueueMetricsOnLabelChange(cq kueue.ClusterQueueReference) {
	cqName := string(cq)
	ReplacedWorkloadSlicesTotal.DeletePartialMatch(prometheus.Labels{"cluster_queue": cqName})
	clearScopedGaugeMetrics(gaugeCleanupScopeClusterQueueLabelChange, prometheus.Labels{"cluster_queue": cqName})
}

func ClearLocalQueueMetrics(lq LocalQueueReference) {
	lbls := prometheus.Labels{"name": string(lq.Name), "namespace": lq.Namespace}
	clearScopedGaugeMetrics(gaugeCleanupScopeLocalQueue, lbls)
	LocalQueueQuotaReservedWorkloadsTotal.DeletePartialMatch(lbls)
	LocalQueueQuotaReservedWaitTime.DeletePartialMatch(lbls)
	LocalQueueFinishedWorkloadsTotal.DeletePartialMatch(lbls)
	LocalQueueAdmittedWorkloadsTotal.DeletePartialMatch(lbls)
	LocalQueueAdmissionWaitTime.DeletePartialMatch(lbls)
	LocalQueueAdmissionChecksWaitTime.DeletePartialMatch(lbls)
	LocalQueueQueuedUntilReadyWaitTime.DeletePartialMatch(lbls)
	LocalQueueAdmittedUntilReadyWaitTime.DeletePartialMatch(lbls)
	LocalQueueEvictedWorkloadsTotal.DeletePartialMatch(lbls)
}

func ClearCohortMetrics(cohortName kueue.CohortReference) {
	clearScopedGaugeMetrics(gaugeCleanupScopeCohort, prometheus.Labels{"cohort": string(cohortName)})
}

func ClearCohortAdmittedWorkloadsMetrics(cohortName kueue.CohortReference) {
	CohortSubtreeAdmittedWorkloadsTotal.DeletePartialMatch(prometheus.Labels{"cohort": string(cohortName)})
	CohortSubtreeAdmittedActiveWorkloads.DeletePartialMatch(prometheus.Labels{"cohort": string(cohortName)})
}

func ReportClusterQueueStatus(cqName kueue.ClusterQueueReference, cqStatus ClusterQueueStatus, customLabelValues []string, tracker *roletracker.RoleTracker) {
	role := roletracker.GetRole(tracker)
	for _, status := range CQStatuses {
		var v float64
		if status == cqStatus {
			v = 1
		}
		labels := append([]string{string(cqName), string(status), role}, customLabelValues...)
		ClusterQueueByStatus.WithLabelValues(labels...).Set(v)
	}
}

var (
	ConditionStatusValues = []metav1.ConditionStatus{metav1.ConditionTrue, metav1.ConditionFalse, metav1.ConditionUnknown}
)

func ReportLocalQueueStatus(lq LocalQueueReference, conditionStatus metav1.ConditionStatus, customLabelValues []string, tracker *roletracker.RoleTracker) {
	role := roletracker.GetRole(tracker)
	for _, status := range ConditionStatusValues {
		var v float64
		if status == conditionStatus {
			v = 1
		}
		labels := append([]string{string(lq.Name), lq.Namespace, string(status), role}, customLabelValues...)
		LocalQueueByStatus.WithLabelValues(labels...).Set(v)
	}
}

func ClearCacheMetrics(cqName string) {
	clearScopedGaugeMetrics(gaugeCleanupScopeClusterQueueCache, prometheus.Labels{"cluster_queue": cqName})
}

func ClearLocalQueueCacheMetrics(lq LocalQueueReference) {
	clearScopedGaugeMetrics(gaugeCleanupScopeLocalQueueCache, prometheus.Labels{"name": string(lq.Name), "namespace": lq.Namespace})
}

func ReportClusterQueueQuotas(cohort kueue.CohortReference, queue, flavor, resource string, nominal, borrowing, lending float64, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(cohort), queue, flavor, resource, roletracker.GetRole(tracker)}, customLabelValues...)
	ClusterQueueResourceNominalQuota.WithLabelValues(labels...).Set(nominal)
	ClusterQueueResourceBorrowingLimit.WithLabelValues(labels...).Set(borrowing)
	ClusterQueueResourceLendingLimit.WithLabelValues(labels...).Set(lending)
}

func ReportCohortSubtreeQuota(
	cohort kueue.CohortReference,
	flavor kueue.ResourceFlavorReference,
	resource corev1.ResourceName,
	quota float64,
	customLabelValues []string,
	tracker *roletracker.RoleTracker,
) {
	labels := append([]string{string(cohort), string(flavor), string(resource), roletracker.GetRole(tracker)}, customLabelValues...)
	CohortSubtreeQuota.WithLabelValues(labels...).Set(quota)
}

func ReportCohortSubtreeAdmittedWorkload(cohort kueue.CohortReference, priorityClass string, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(cohort), priorityClass, roletracker.GetRole(tracker)}, customLabelValues...)
	CohortSubtreeAdmittedWorkloadsTotal.WithLabelValues(labels...).Inc()
}

func cohortPartialMatchLabels(cohort kueue.CohortReference, flavor, resource string) prometheus.Labels {
	lbls := prometheus.Labels{"cohort": string(cohort)}
	if len(flavor) != 0 {
		lbls["flavor"] = flavor
	}
	if len(resource) != 0 {
		lbls["resource"] = resource
	}
	return lbls
}

func ClearCohortSubtreeQuota(cohort kueue.CohortReference, flavor kueue.ResourceFlavorReference, resource corev1.ResourceName) {
	lbls := cohortPartialMatchLabels(cohort, string(flavor), string(resource))
	CohortSubtreeQuota.DeletePartialMatch(lbls)
}

func ReportCohortSubtreeResourceReservations(
	cohort kueue.CohortReference,
	flavor kueue.ResourceFlavorReference,
	resource corev1.ResourceName,
	usage float64,
	customLabelValues []string,
	tracker *roletracker.RoleTracker,
) {
	labels := append([]string{string(cohort), string(flavor), string(resource), roletracker.GetRole(tracker)}, customLabelValues...)
	CohortSubtreeResourceReservations.WithLabelValues(labels...).Set(usage)
}

func ClearCohortSubtreeResourceReservations(cohort kueue.CohortReference, flavor kueue.ResourceFlavorReference, resource corev1.ResourceName) {
	lbls := cohortPartialMatchLabels(cohort, string(flavor), string(resource))
	CohortSubtreeResourceReservations.DeletePartialMatch(lbls)
}

func ReportClusterQueueResourceReservations(cohort kueue.CohortReference, queue, flavor, resource string, usage float64, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(cohort), queue, flavor, resource, roletracker.GetRole(tracker)}, customLabelValues...)
	ClusterQueueResourceReservations.WithLabelValues(labels...).Set(usage)
}

func ReportLocalQueueResourceReservations(lq LocalQueueReference, flavor, resource string, usage float64, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(lq.Name), lq.Namespace, flavor, resource, roletracker.GetRole(tracker)}, customLabelValues...)
	LocalQueueResourceReservations.WithLabelValues(labels...).Set(usage)
}

func ReportClusterQueueResourceUsage(cohort kueue.CohortReference, queue, flavor, resource string, usage float64, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(cohort), queue, flavor, resource, roletracker.GetRole(tracker)}, customLabelValues...)
	ClusterQueueResourceUsage.WithLabelValues(labels...).Set(usage)
}

func ReportLocalQueueResourceUsage(lq LocalQueueReference, flavor, resource string, usage float64, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(lq.Name), lq.Namespace, flavor, resource, roletracker.GetRole(tracker)}, customLabelValues...)
	LocalQueueResourceUsage.WithLabelValues(labels...).Set(usage)
}

func ReportClusterQueueWeightedShare(cq kueue.ClusterQueueReference, cohort kueue.CohortReference, weightedShare float64, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(cq), string(cohort), roletracker.GetRole(tracker)}, customLabelValues...)
	ClusterQueueWeightedShare.WithLabelValues(labels...).Set(weightedShare)
}

func ReportCohortWeightedShare(cohort kueue.CohortReference, weightedShare float64, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(cohort), roletracker.GetRole(tracker)}, customLabelValues...)
	CohortWeightedShare.WithLabelValues(labels...).Set(weightedShare)
}

func ReportCohortSubtreeAdmittedActiveWorkloads(cohort kueue.CohortReference, count int, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(cohort), roletracker.GetRole(tracker)}, customLabelValues...)
	CohortSubtreeAdmittedActiveWorkloads.WithLabelValues(labels...).Set(float64(count))
}

func ReportAdmittedActiveWorkloads(cqName kueue.ClusterQueueReference, count int, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(cqName), roletracker.GetRole(tracker)}, customLabelValues...)
	AdmittedActiveWorkloads.WithLabelValues(labels...).Set(float64(count))
}

func ReportReservingActiveWorkloads(cqName kueue.ClusterQueueReference, count int, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(cqName), roletracker.GetRole(tracker)}, customLabelValues...)
	ReservingActiveWorkloads.WithLabelValues(labels...).Set(float64(count))
}

func ReportLocalQueueAdmittedActiveWorkloads(lq LocalQueueReference, count int, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(lq.Name), lq.Namespace, roletracker.GetRole(tracker)}, customLabelValues...)
	LocalQueueAdmittedActiveWorkloads.WithLabelValues(labels...).Set(float64(count))
}

func ReportLocalQueueReservingActiveWorkloads(lq LocalQueueReference, count int, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(lq.Name), lq.Namespace, roletracker.GetRole(tracker)}, customLabelValues...)
	LocalQueueReservingActiveWorkloads.WithLabelValues(labels...).Set(float64(count))
}

func ReportPodsReadyToEvictedTimeSeconds(cqName kueue.ClusterQueueReference, reason, underlyingCause string, waitTime time.Duration, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(cqName), reason, underlyingCause, roletracker.GetRole(tracker)}, customLabelValues...)
	PodsReadyToEvictedTimeSeconds.WithLabelValues(labels...).Observe(waitTime.Seconds())
}

func ReportAdmissionCyclePreemptionSkips(cqName kueue.ClusterQueueReference, count int, customLabelValues []string, tracker *roletracker.RoleTracker) {
	labels := append([]string{string(cqName), roletracker.GetRole(tracker)}, customLabelValues...)
	AdmissionCyclePreemptionSkips.WithLabelValues(labels...).Set(float64(count))
}

func clearScopedGaugeMetrics(scope gaugeCleanupScope, lbls prometheus.Labels) {
	for _, g := range gaugeVecsByScope[scope] {
		g.DeletePartialMatch(lbls)
	}
}

// ClearGaugeMetricsForRole deletes all gauge metric time series matching
// replica_role=role. Called during HA role transitions to remove stale
// time series reported under the old role.
func ClearGaugeMetricsForRole(role string) {
	clearScopedGaugeMetrics(gaugeCleanupScopeRole, prometheus.Labels{"replica_role": role})
}

func ClearClusterQueueResourceMetrics(cqName string) {
	clearScopedGaugeMetrics(gaugeCleanupScopeClusterQueueResource, prometheus.Labels{"cluster_queue": cqName})
}

func ClearLocalQueueResourceMetrics(lq LocalQueueReference) {
	clearScopedGaugeMetrics(gaugeCleanupScopeLocalQueueResource, prometheus.Labels{
		"name":      string(lq.Name),
		"namespace": lq.Namespace,
	})
}

func ClearClusterQueueResourceQuotas(cqName, flavor, resource string) {
	lbls := prometheus.Labels{
		"cluster_queue": cqName,
		"flavor":        flavor,
	}

	if len(resource) != 0 {
		lbls["resource"] = resource
	}

	ClusterQueueResourceNominalQuota.DeletePartialMatch(lbls)
	ClusterQueueResourceBorrowingLimit.DeletePartialMatch(lbls)
	ClusterQueueResourceLendingLimit.DeletePartialMatch(lbls)
}

func ClearClusterQueueResourceUsage(cqName, flavor, resource string) {
	lbls := prometheus.Labels{
		"cluster_queue": cqName,
		"flavor":        flavor,
	}

	if len(resource) != 0 {
		lbls["resource"] = resource
	}

	ClusterQueueResourceUsage.DeletePartialMatch(lbls)
}

func ClearClusterQueueResourceReservations(cqName, flavor, resource string) {
	lbls := prometheus.Labels{
		"cluster_queue": cqName,
		"flavor":        flavor,
	}

	if len(resource) != 0 {
		lbls["resource"] = resource
	}

	ClusterQueueResourceReservations.DeletePartialMatch(lbls)
}

func Register() {
	metrics.Registry.MustRegister(
		buildInfo,
		AdmissionAttemptsTotal,
		admissionAttemptDuration,
		AdmissionCyclePreemptionSkips,
		PendingWorkloads,
		FinishedWorkloads,
		QuotaReservedWorkloadsTotal,
		FinishedWorkloadsTotal,
		QuotaReservedWaitTime,
		PodsReadyToEvictedTimeSeconds,
		AdmittedWorkloadsTotal,
		AdmissionWaitTime,
		AdmissionChecksWaitTime,
		QueuedUntilReadyWaitTime,
		AdmittedUntilReadyWaitTime,
		EvictedWorkloadsTotal,
		EvictedWorkloadsOnceTotal,
		PreemptedWorkloadsTotal,
		ReservingActiveWorkloads,
		AdmittedActiveWorkloads,
		ClusterQueueByStatus,
		ClusterQueueResourceReservations,
		ClusterQueueResourceUsage,
		ClusterQueueResourceNominalQuota,
		ClusterQueueResourceBorrowingLimit,
		ClusterQueueResourceLendingLimit,
		ClusterQueueWeightedShare,
		CohortWeightedShare,
		CohortSubtreeQuota,
		CohortSubtreeAdmittedWorkloadsTotal,
		CohortSubtreeResourceReservations,
		CohortSubtreeAdmittedActiveWorkloads,
	)
	if features.Enabled(features.LocalQueueMetrics) {
		RegisterLQMetrics()
	}
}

func RegisterLQMetrics() {
	metrics.Registry.MustRegister(
		LocalQueuePendingWorkloads,
		LocalQueueFinishedWorkloads,
		LocalQueueQuotaReservedWorkloadsTotal,
		LocalQueueFinishedWorkloadsTotal,
		LocalQueueQuotaReservedWaitTime,
		LocalQueueAdmittedWorkloadsTotal,
		LocalQueueAdmissionWaitTime,
		LocalQueueAdmissionChecksWaitTime,
		LocalQueueQueuedUntilReadyWaitTime,
		LocalQueueAdmittedUntilReadyWaitTime,
		LocalQueueEvictedWorkloadsTotal,
		LocalQueueReservingActiveWorkloads,
		LocalQueueAdmittedActiveWorkloads,
		LocalQueueByStatus,
		LocalQueueResourceReservations,
		LocalQueueResourceUsage,
	)
}
