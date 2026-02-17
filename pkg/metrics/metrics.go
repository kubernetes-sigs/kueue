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

	// +metricsdoc:group=health
	// +metricsdoc:labels=result="possible values are `success` or `inadmissible`",replica_role="one of `leader`, `follower`, or `standalone`"
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

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",replica_role="one of `leader`, `follower`, or `standalone`"
	AdmissionCyclePreemptionSkips = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "admission_cycle_preemption_skips",
			Help: "The number of Workloads in the ClusterQueue that got preemption candidates " +
				"but had to be skipped because other ClusterQueues needed the same resources in the same cycle",
		}, []string{"cluster_queue", "replica_role"},
	)

	// Metrics tied to the queue system.

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=git_version="git version",git_commit="git commit",build_date="build date",go_version="go version",compiler="compiler",platform="platform"
	buildInfo = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "build_info",
			Help:      "Kueue build information. 1 labeled by git version, git commit, build date, go version, compiler, platform",
		},
		[]string{"git_version", "git_commit", "build_date", "go_version", "compiler", "platform"},
	)

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",status="status label (varies by metric)",replica_role="one of `leader`, `follower`, or `standalone`"
	PendingWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "pending_workloads",
			Help: `The number of pending workloads, per 'cluster_queue' and 'status'.
'status' can have the following values:
- "active" means that the workloads are in the admission queue.
- "inadmissible" means there was a failed admission attempt for these workloads and they won't be retried until cluster conditions, which could make this workload admissible, change`,
		}, []string{"cluster_queue", "status", "replica_role"},
	)

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",status="status label (varies by metric)",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueuePendingWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_pending_workloads",
			Help: `The number of pending workloads, per 'local_queue' and 'status'.
'status' can have the following values:
- "active" means that the workloads are in the admission queue.
- "inadmissible" means there was a failed admission attempt for these workloads and they won't be retried until cluster conditions, which could make this workload admissible, change`,
		}, []string{"name", "namespace", "status", "replica_role"},
	)

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",replica_role="one of `leader`, `follower`, or `standalone`"
	FinishedWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "finished_workloads",
			Help:      `The number of finished workloads per 'cluster_queue'.`,
		}, []string{"cluster_queue", "replica_role"},
	)

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueFinishedWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_finished_workloads",
			Help:      `The number of finished workloads, per 'local_queue'.`,
		}, []string{"name", "namespace", "replica_role"},
	)

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	QuotaReservedWorkloadsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: constants.KueueName,
			Name:      "quota_reserved_workloads_total",
			Help:      "The total number of quota reserved workloads per 'cluster_queue'",
		}, []string{"cluster_queue", "priority_class", "replica_role"},
	)

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueQuotaReservedWorkloadsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_quota_reserved_workloads_total",
			Help:      "The total number of quota reserved workloads per 'local_queue'",
		}, []string{"name", "namespace", "priority_class", "replica_role"},
	)

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	FinishedWorkloadsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: constants.KueueName,
			Name:      "finished_workloads_total",
			Help:      "The total number of finished workloads per 'cluster_queue'",
		}, []string{"cluster_queue", "priority_class", "replica_role"},
	)

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueFinishedWorkloadsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_finished_workloads_total",
			Help:      "The total number of finished workloads per 'local_queue'",
		}, []string{"name", "namespace", "priority_class", "replica_role"},
	)

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	QuotaReservedWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "quota_reserved_wait_time_seconds",
			Help:      "The time between a workload was created or requeued until it got quota reservation, per 'cluster_queue'",
			Buckets:   generateExponentialBuckets(14),
		}, []string{"cluster_queue", "priority_class", "replica_role"},
	)

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",reason="eviction or preemption reason",underlying_cause="root cause for eviction",replica_role="one of `leader`, `follower`, or `standalone`"
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
		}, []string{"cluster_queue", "reason", "underlying_cause", "replica_role"},
	)

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueQuotaReservedWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_quota_reserved_wait_time_seconds",
			Help:      "The time between a workload was created or requeued until it got quota reservation, per 'local_queue'",
			Buckets:   generateExponentialBuckets(14),
		}, []string{"name", "namespace", "priority_class", "replica_role"},
	)

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	AdmittedWorkloadsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: constants.KueueName,
			Name:      "admitted_workloads_total",
			Help:      "The total number of admitted workloads per 'cluster_queue'",
		}, []string{"cluster_queue", "priority_class", "replica_role"},
	)

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueAdmittedWorkloadsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_admitted_workloads_total",
			Help:      "The total number of admitted workloads per 'local_queue'",
		}, []string{"name", "namespace", "priority_class", "replica_role"},
	)

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	AdmissionWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "admission_wait_time_seconds",
			Help:      "The time between a workload was created or requeued until admission, per 'cluster_queue'",
			Buckets:   generateExponentialBuckets(14),
		}, []string{"cluster_queue", "priority_class", "replica_role"},
	)

	// +metricsdoc:group=optional_wait_for_pods_ready
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	QueuedUntilReadyWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "ready_wait_time_seconds",
			Help:      "The time between a workload was created or requeued until ready, per 'cluster_queue'",
			Buckets:   generateExponentialBuckets(14),
		}, []string{"cluster_queue", "priority_class", "replica_role"},
	)

	// +metricsdoc:group=optional_wait_for_pods_ready
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	AdmittedUntilReadyWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "admitted_until_ready_wait_time_seconds",
			Help:      "The time between a workload was admitted until ready, per 'cluster_queue'",
			Buckets:   generateExponentialBuckets(14),
		}, []string{"cluster_queue", "priority_class", "replica_role"},
	)

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueAdmissionWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_admission_wait_time_seconds",
			Help:      "The time between a workload was created or requeued until admission, per 'local_queue'",
			Buckets:   generateExponentialBuckets(14),
		}, []string{"name", "namespace", "priority_class", "replica_role"},
	)

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	AdmissionChecksWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "admission_checks_wait_time_seconds",
			Help:      "The time from when a workload got the quota reservation until admission, per 'cluster_queue'",
			Buckets:   generateExponentialBuckets(14),
		}, []string{"cluster_queue", "priority_class", "replica_role"},
	)

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueAdmissionChecksWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_admission_checks_wait_time_seconds",
			Help:      "The time from when a workload got the quota reservation until admission, per 'local_queue'",
			Buckets:   generateExponentialBuckets(14),
		}, []string{"name", "namespace", "priority_class", "replica_role"},
	)

	// +metricsdoc:group=optional_wait_for_pods_ready
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueQueuedUntilReadyWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_ready_wait_time_seconds",
			Help:      "The time between a workload was created or requeued until ready, per 'local_queue'",
			Buckets:   generateExponentialBuckets(14),
		}, []string{"name", "namespace", "priority_class", "replica_role"},
	)

	// +metricsdoc:group=optional_wait_for_pods_ready
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueAdmittedUntilReadyWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_admitted_until_ready_wait_time_seconds",
			Help:      "The time between a workload was admitted until ready, per 'local_queue'",
			Buckets:   generateExponentialBuckets(14),
		}, []string{"name", "namespace", "priority_class", "replica_role"},
	)

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",reason="eviction or preemption reason",underlying_cause="root cause for eviction",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
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
		}, []string{"cluster_queue", "reason", "underlying_cause", "priority_class", "replica_role"},
	)

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",replica_role="one of `leader`, `follower`, or `standalone`"
	ReplacedWorkloadSlicesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: constants.KueueName,
			Name:      "replaced_workload_slices_total",
			Help:      `The number of replaced workload slices per 'cluster_queue'`,
		}, []string{"cluster_queue", "replica_role"},
	)

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",reason="eviction or preemption reason",underlying_cause="root cause for eviction",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
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
		}, []string{"name", "namespace", "reason", "underlying_cause", "priority_class", "replica_role"},
	)

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",reason="eviction or preemption reason",detailed_reason="finer-grained eviction cause",priority_class="the priority class name",replica_role="one of `leader`, `follower`, or `standalone`"
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
The label 'detailed_reason' can have the following values:
- "" means that the value in 'reason' label is the root cause for eviction.
- "WaitForStart" means that the pods have not been ready since admission, or the workload is not admitted.
- "WaitForRecovery" means that the Pods were ready since the workload admission, but some pod has failed.
- "AdmissionCheck" means that the workload was evicted by Kueue due to a rejected admission check.
- "MaximumExecutionTimeExceeded" means that the workload was evicted by Kueue due to maximum execution time exceeded.
- "RequeuingLimitExceeded" means that the workload was evicted by Kueue due to requeuing limit exceeded.`,
		}, []string{"cluster_queue", "reason", "detailed_reason", "priority_class", "replica_role"},
	)

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=preempting_cluster_queue="the ClusterQueue executing preemption",reason="eviction or preemption reason",replica_role="one of `leader`, `follower`, or `standalone`"
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
		}, []string{"preempting_cluster_queue", "reason", "replica_role"},
	)

	// Metrics tied to the cache.

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",replica_role="one of `leader`, `follower`, or `standalone`"
	ReservingActiveWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "reserving_active_workloads",
			Help:      "The number of Workloads that are reserving quota, per 'cluster_queue'",
		}, []string{"cluster_queue", "replica_role"},
	)

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueReservingActiveWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_reserving_active_workloads",
			Help:      "The number of Workloads that are reserving quota, per 'localQueue'",
		}, []string{"name", "namespace", "replica_role"},
	)

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",replica_role="one of `leader`, `follower`, or `standalone`"
	AdmittedActiveWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "admitted_active_workloads",
			Help:      "The number of admitted Workloads that are active (unsuspended and not finished), per 'cluster_queue'",
		}, []string{"cluster_queue", "replica_role"},
	)

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueAdmittedActiveWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_admitted_active_workloads",
			Help:      "The number of admitted Workloads that are active (unsuspended and not finished), per 'localQueue'",
		}, []string{"name", "namespace", "replica_role"},
	)

	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",status="one of `pending`, `active`, or `terminated`",replica_role="one of `leader`, `follower`, or `standalone`"
	ClusterQueueByStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "cluster_queue_status",
			Help: `Reports 'cluster_queue' with its 'status' (with possible values 'pending', 'active' or 'terminated').
For a ClusterQueue, the metric only reports a value of 1 for one of the statuses.`,
		}, []string{"cluster_queue", "status", "replica_role"},
	)

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",active="one of `True`, `False`, or `Unknown`",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueByStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_status",
			Help: `Reports 'localQueue' with its 'active' status (with possible values 'True', 'False', or 'Unknown').
For a LocalQueue, the metric only reports a value of 1 for one of the statuses.`,
		}, []string{"name", "namespace", "active", "replica_role"},
	)

	// Optional cluster queue metrics

	// +metricsdoc:group=optional_clusterqueue_resources
	// +metricsdoc:labels=cohort="the name of the Cohort",cluster_queue="the name of the ClusterQueue",flavor="the resource flavor name",resource="the resource name",replica_role="one of `leader`, `follower`, or `standalone`"
	ClusterQueueResourceReservations = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "cluster_queue_resource_reservation",
			Help:      `Reports the cluster_queue's total resource reservation within all the flavors`,
		}, []string{"cohort", "cluster_queue", "flavor", "resource", "replica_role"},
	)

	// +metricsdoc:group=optional_clusterqueue_resources
	// +metricsdoc:labels=cohort="the name of the Cohort",cluster_queue="the name of the ClusterQueue",flavor="the resource flavor name",resource="the resource name",replica_role="one of `leader`, `follower`, or `standalone`"
	ClusterQueueResourceUsage = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "cluster_queue_resource_usage",
			Help:      `Reports the cluster_queue's total resource usage within all the flavors`,
		}, []string{"cohort", "cluster_queue", "flavor", "resource", "replica_role"},
	)

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",flavor="the resource flavor name",resource="the resource name",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueResourceReservations = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_resource_reservation",
			Help:      `Reports the localQueue's total resource reservation within all the flavors`,
		}, []string{"name", "namespace", "flavor", "resource", "replica_role"},
	)

	// +metricsdoc:group=localqueue
	// +metricsdoc:labels=name="the name of the LocalQueue",namespace="the namespace of the LocalQueue",flavor="the resource flavor name",resource="the resource name",replica_role="one of `leader`, `follower`, or `standalone`"
	LocalQueueResourceUsage = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_resource_usage",
			Help:      `Reports the localQueue's total resource usage within all the flavors`,
		}, []string{"name", "namespace", "flavor", "resource", "replica_role"},
	)

	// +metricsdoc:group=optional_clusterqueue_resources
	// +metricsdoc:labels=cohort="the name of the Cohort",cluster_queue="the name of the ClusterQueue",flavor="the resource flavor name",resource="the resource name",replica_role="one of `leader`, `follower`, or `standalone`"
	ClusterQueueResourceNominalQuota = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "cluster_queue_nominal_quota",
			Help:      `Reports the cluster_queue's resource nominal quota within all the flavors`,
		}, []string{"cohort", "cluster_queue", "flavor", "resource", "replica_role"},
	)

	// +metricsdoc:group=optional_clusterqueue_resources
	// +metricsdoc:labels=cohort="the name of the Cohort",cluster_queue="the name of the ClusterQueue",flavor="the resource flavor name",resource="the resource name",replica_role="one of `leader`, `follower`, or `standalone`"
	ClusterQueueResourceBorrowingLimit = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "cluster_queue_borrowing_limit",
			Help:      `Reports the cluster_queue's resource borrowing limit within all the flavors`,
		}, []string{"cohort", "cluster_queue", "flavor", "resource", "replica_role"},
	)

	// +metricsdoc:group=optional_clusterqueue_resources
	// +metricsdoc:labels=cohort="the name of the Cohort",cluster_queue="the name of the ClusterQueue",flavor="the resource flavor name",resource="the resource name",replica_role="one of `leader`, `follower`, or `standalone`"
	ClusterQueueResourceLendingLimit = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "cluster_queue_lending_limit",
			Help:      `Reports the cluster_queue's resource lending limit within all the flavors`,
		}, []string{"cohort", "cluster_queue", "flavor", "resource", "replica_role"},
	)

	// +metricsdoc:group=optional_clusterqueue_resources
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",cohort="the name of the Cohort",replica_role="one of `leader`, `follower`, or `standalone`"
	ClusterQueueWeightedShare = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "cluster_queue_weighted_share",
			Help: `Reports a value that representing the maximum of the ratios of usage above nominal
quota to the lendable resources in the cohort, among all the resources provided by
the ClusterQueue, and divided by the weight.
If zero, it means that the usage of the ClusterQueue is below the nominal quota.
If the ClusterQueue has a weight of zero and is borrowing, this will return NaN.`,
		}, []string{"cluster_queue", "cohort", "replica_role"},
	)

	// +metricsdoc:group=cohort
	// +metricsdoc:labels=cohort="the name of the Cohort",replica_role="one of `leader`, `follower`, or `standalone`"
	CohortWeightedShare = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "cohort_weighted_share",
			Help: `Reports a value that representing the maximum of the ratios of usage above nominal
quota to the lendable resources in the Cohort, among all the resources provided by
the Cohort, and divided by the weight.
If zero, it means that the usage of the Cohort is below the nominal quota.
If the Cohort has a weight of zero and is borrowing, this will return NaN.`,
		}, []string{"cohort", "replica_role"},
	)
)

func init() {
	versionInfo := version.Get()
	buildInfo.WithLabelValues(versionInfo.GitVersion, versionInfo.GitCommit, versionInfo.BuildDate, versionInfo.GoVersion, versionInfo.Compiler, versionInfo.Platform).Set(1)
}

func generateExponentialBuckets(count int) []float64 {
	return append([]float64{1}, prometheus.ExponentialBuckets(2.5, 2, count-1)...)
}

func AdmissionAttempt(result AdmissionResult, duration time.Duration, tracker *roletracker.RoleTracker) {
	role := roletracker.GetRole(tracker)
	AdmissionAttemptsTotal.WithLabelValues(string(result), role).Inc()
	admissionAttemptDuration.WithLabelValues(string(result), role).Observe(duration.Seconds())
}

func QuotaReservedWorkload(cqName kueue.ClusterQueueReference, priorityClass string, waitTime time.Duration, tracker *roletracker.RoleTracker) {
	role := roletracker.GetRole(tracker)
	QuotaReservedWorkloadsTotal.WithLabelValues(string(cqName), priorityClass, role).Inc()
	QuotaReservedWaitTime.WithLabelValues(string(cqName), priorityClass, role).Observe(waitTime.Seconds())
}

func LocalQueueQuotaReservedWorkload(lq LocalQueueReference, priorityClass string, waitTime time.Duration, tracker *roletracker.RoleTracker) {
	role := roletracker.GetRole(tracker)
	LocalQueueQuotaReservedWorkloadsTotal.WithLabelValues(string(lq.Name), lq.Namespace, priorityClass, role).Inc()
	LocalQueueQuotaReservedWaitTime.WithLabelValues(string(lq.Name), lq.Namespace, priorityClass, role).Observe(waitTime.Seconds())
}

// IncrementFinishedWorkloadTotal increases the counter of finished workloads
// for the given ClusterQueue, priority class, and workload role.
func IncrementFinishedWorkloadTotal(cqName kueue.ClusterQueueReference, priorityClass string, tracker *roletracker.RoleTracker) {
	role := roletracker.GetRole(tracker)
	FinishedWorkloadsTotal.WithLabelValues(string(cqName), priorityClass, role).Inc()
}

// IncrementLocalQueueFinishedWorkloadTotal increases the counter of finished workloads
// for the given LocalQueue, priority class, and workload role.
func IncrementLocalQueueFinishedWorkloadTotal(lq LocalQueueReference, priorityClass string, tracker *roletracker.RoleTracker) {
	role := roletracker.GetRole(tracker)
	LocalQueueFinishedWorkloadsTotal.WithLabelValues(string(lq.Name), lq.Namespace, priorityClass, role).Inc()
}

// ReportFinishedWorkloads sets the current total number of finished workloads
// for the given ClusterQueue and workload role (gauge).
func ReportFinishedWorkloads(cqName kueue.ClusterQueueReference, count int, tracker *roletracker.RoleTracker) {
	role := roletracker.GetRole(tracker)
	FinishedWorkloads.WithLabelValues(string(cqName), role).Set(float64(count))
}

// ReportLocalQueueFinishedWorkloads sets the current total number of finished workloads
// for the given LocalQueue and workload role (gauge).
func ReportLocalQueueFinishedWorkloads(lq LocalQueueReference, count int, tracker *roletracker.RoleTracker) {
	role := roletracker.GetRole(tracker)
	LocalQueueFinishedWorkloads.WithLabelValues(string(lq.Name), lq.Namespace, role).Set(float64(count))
}

func AdmittedWorkload(cqName kueue.ClusterQueueReference, priorityClass string, waitTime time.Duration, tracker *roletracker.RoleTracker) {
	role := roletracker.GetRole(tracker)
	AdmittedWorkloadsTotal.WithLabelValues(string(cqName), priorityClass, role).Inc()
	AdmissionWaitTime.WithLabelValues(string(cqName), priorityClass, role).Observe(waitTime.Seconds())
}

func LocalQueueAdmittedWorkload(lq LocalQueueReference, priorityClass string, waitTime time.Duration, tracker *roletracker.RoleTracker) {
	role := roletracker.GetRole(tracker)
	LocalQueueAdmittedWorkloadsTotal.WithLabelValues(string(lq.Name), lq.Namespace, priorityClass, role).Inc()
	LocalQueueAdmissionWaitTime.WithLabelValues(string(lq.Name), lq.Namespace, priorityClass, role).Observe(waitTime.Seconds())
}

func ReportAdmissionChecksWaitTime(cqName kueue.ClusterQueueReference, priorityClass string, waitTime time.Duration, tracker *roletracker.RoleTracker) {
	AdmissionChecksWaitTime.WithLabelValues(string(cqName), priorityClass, roletracker.GetRole(tracker)).Observe(waitTime.Seconds())
}

func ReportLocalQueueAdmissionChecksWaitTime(lq LocalQueueReference, priorityClass string, waitTime time.Duration, tracker *roletracker.RoleTracker) {
	LocalQueueAdmissionChecksWaitTime.WithLabelValues(string(lq.Name), lq.Namespace, priorityClass, roletracker.GetRole(tracker)).Observe(waitTime.Seconds())
}

func ReadyWaitTime(cqName kueue.ClusterQueueReference, priorityClass string, waitTime time.Duration, tracker *roletracker.RoleTracker) {
	QueuedUntilReadyWaitTime.WithLabelValues(string(cqName), priorityClass, roletracker.GetRole(tracker)).Observe(waitTime.Seconds())
}

func LocalQueueReadyWaitTime(lq LocalQueueReference, priorityClass string, waitTime time.Duration, tracker *roletracker.RoleTracker) {
	LocalQueueQueuedUntilReadyWaitTime.WithLabelValues(string(lq.Name), lq.Namespace, priorityClass, roletracker.GetRole(tracker)).Observe(waitTime.Seconds())
}

func ReportAdmittedUntilReadyWaitTime(cqName kueue.ClusterQueueReference, priorityClass string, waitTime time.Duration, tracker *roletracker.RoleTracker) {
	AdmittedUntilReadyWaitTime.WithLabelValues(string(cqName), priorityClass, roletracker.GetRole(tracker)).Observe(waitTime.Seconds())
}

func ReportLocalQueueAdmittedUntilReadyWaitTime(lq LocalQueueReference, priorityClass string, waitTime time.Duration, tracker *roletracker.RoleTracker) {
	LocalQueueAdmittedUntilReadyWaitTime.WithLabelValues(string(lq.Name), lq.Namespace, priorityClass, roletracker.GetRole(tracker)).Observe(waitTime.Seconds())
}

func ReportPendingWorkloads(cqName kueue.ClusterQueueReference, active, inadmissible int, tracker *roletracker.RoleTracker) {
	role := roletracker.GetRole(tracker)
	PendingWorkloads.WithLabelValues(string(cqName), PendingStatusActive, role).Set(float64(active))
	PendingWorkloads.WithLabelValues(string(cqName), PendingStatusInadmissible, role).Set(float64(inadmissible))
}

func ReportLocalQueuePendingWorkloads(lq LocalQueueReference, active, inadmissible int, tracker *roletracker.RoleTracker) {
	role := roletracker.GetRole(tracker)
	LocalQueuePendingWorkloads.WithLabelValues(string(lq.Name), lq.Namespace, PendingStatusActive, role).Set(float64(active))
	LocalQueuePendingWorkloads.WithLabelValues(string(lq.Name), lq.Namespace, PendingStatusInadmissible, role).Set(float64(inadmissible))
}

func ReportEvictedWorkloads(cqName kueue.ClusterQueueReference, evictionReason, underlyingCause, priorityClass string, tracker *roletracker.RoleTracker) {
	EvictedWorkloadsTotal.WithLabelValues(string(cqName), evictionReason, underlyingCause, priorityClass, roletracker.GetRole(tracker)).Inc()
}

func ReportReplacedWorkloadSlices(cqName kueue.ClusterQueueReference, tracker *roletracker.RoleTracker) {
	ReplacedWorkloadSlicesTotal.WithLabelValues(string(cqName), roletracker.GetRole(tracker)).Inc()
}

func ReportLocalQueueEvictedWorkloads(lq LocalQueueReference, reason, underlyingCause string, priorityClass string, tracker *roletracker.RoleTracker) {
	LocalQueueEvictedWorkloadsTotal.WithLabelValues(string(lq.Name), lq.Namespace, reason, underlyingCause, priorityClass, roletracker.GetRole(tracker)).Inc()
}

func ReportEvictedWorkloadsOnce(cqName kueue.ClusterQueueReference, reason, underlyingCause, priorityClass string, tracker *roletracker.RoleTracker) {
	EvictedWorkloadsOnceTotal.WithLabelValues(string(cqName), reason, underlyingCause, priorityClass, roletracker.GetRole(tracker)).Inc()
}

func ReportPreemption(preemptingCqName kueue.ClusterQueueReference, preemptingReason string, targetCqName kueue.ClusterQueueReference, tracker *roletracker.RoleTracker) {
	PreemptedWorkloadsTotal.WithLabelValues(string(preemptingCqName), preemptingReason, roletracker.GetRole(tracker)).Inc()
}

func LQRefFromWorkload(wl *kueue.Workload) LocalQueueReference {
	return LocalQueueReference{
		Name:      wl.Spec.QueueName,
		Namespace: wl.Namespace,
	}
}

func ClearClusterQueueMetrics(cq kueue.ClusterQueueReference) {
	cqName := string(cq)
	AdmissionCyclePreemptionSkips.DeletePartialMatch(prometheus.Labels{"cluster_queue": cqName})
	PendingWorkloads.DeletePartialMatch(prometheus.Labels{"cluster_queue": cqName})
	QuotaReservedWorkloadsTotal.DeletePartialMatch(prometheus.Labels{"cluster_queue": cqName})
	QuotaReservedWaitTime.DeletePartialMatch(prometheus.Labels{"cluster_queue": cqName})
	FinishedWorkloads.DeletePartialMatch(prometheus.Labels{"cluster_queue": cqName})
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

func ClearLocalQueueMetrics(lq LocalQueueReference) {
	LocalQueuePendingWorkloads.DeletePartialMatch(prometheus.Labels{"name": string(lq.Name), "namespace": lq.Namespace})
	LocalQueueQuotaReservedWorkloadsTotal.DeletePartialMatch(prometheus.Labels{"name": string(lq.Name), "namespace": lq.Namespace})
	LocalQueueQuotaReservedWaitTime.DeletePartialMatch(prometheus.Labels{"name": string(lq.Name), "namespace": lq.Namespace})
	LocalQueueFinishedWorkloads.DeletePartialMatch(prometheus.Labels{"name": string(lq.Name), "namespace": lq.Namespace})
	LocalQueueFinishedWorkloadsTotal.DeletePartialMatch(prometheus.Labels{"name": string(lq.Name), "namespace": lq.Namespace})
	LocalQueueAdmittedWorkloadsTotal.DeletePartialMatch(prometheus.Labels{"name": string(lq.Name), "namespace": lq.Namespace})
	LocalQueueAdmissionWaitTime.DeletePartialMatch(prometheus.Labels{"name": string(lq.Name), "namespace": lq.Namespace})
	LocalQueueAdmissionChecksWaitTime.DeletePartialMatch(prometheus.Labels{"name": string(lq.Name), "namespace": lq.Namespace})
	LocalQueueQueuedUntilReadyWaitTime.DeletePartialMatch(prometheus.Labels{"name": string(lq.Name), "namespace": lq.Namespace})
	LocalQueueAdmittedUntilReadyWaitTime.DeletePartialMatch(prometheus.Labels{"name": string(lq.Name), "namespace": lq.Namespace})
	LocalQueueEvictedWorkloadsTotal.DeletePartialMatch(prometheus.Labels{"name": string(lq.Name), "namespace": lq.Namespace})
}

func ReportClusterQueueStatus(cqName kueue.ClusterQueueReference, cqStatus ClusterQueueStatus, tracker *roletracker.RoleTracker) {
	for _, status := range CQStatuses {
		var v float64
		if status == cqStatus {
			v = 1
		}
		ClusterQueueByStatus.WithLabelValues(string(cqName), string(status), roletracker.GetRole(tracker)).Set(v)
	}
}

var (
	ConditionStatusValues = []metav1.ConditionStatus{metav1.ConditionTrue, metav1.ConditionFalse, metav1.ConditionUnknown}
)

func ReportLocalQueueStatus(lq LocalQueueReference, conditionStatus metav1.ConditionStatus, tracker *roletracker.RoleTracker) {
	for _, status := range ConditionStatusValues {
		var v float64
		if status == conditionStatus {
			v = 1
		}
		LocalQueueByStatus.WithLabelValues(string(lq.Name), lq.Namespace, string(status), roletracker.GetRole(tracker)).Set(v)
	}
}

func ClearCacheMetrics(cqName string) {
	ReservingActiveWorkloads.DeletePartialMatch(prometheus.Labels{"cluster_queue": cqName})
	AdmittedActiveWorkloads.DeletePartialMatch(prometheus.Labels{"cluster_queue": cqName})
	ClusterQueueByStatus.DeletePartialMatch(prometheus.Labels{"cluster_queue": cqName})
}

func ClearLocalQueueCacheMetrics(lq LocalQueueReference) {
	LocalQueueReservingActiveWorkloads.DeletePartialMatch(prometheus.Labels{"name": string(lq.Name), "namespace": lq.Namespace})
	LocalQueueAdmittedActiveWorkloads.DeletePartialMatch(prometheus.Labels{"name": string(lq.Name), "namespace": lq.Namespace})
	LocalQueueByStatus.DeletePartialMatch(prometheus.Labels{"name": string(lq.Name), "namespace": lq.Namespace})
}

func ReportClusterQueueQuotas(cohort kueue.CohortReference, queue, flavor, resource string, nominal, borrowing, lending float64, tracker *roletracker.RoleTracker) {
	role := roletracker.GetRole(tracker)
	ClusterQueueResourceNominalQuota.WithLabelValues(string(cohort), queue, flavor, resource, role).Set(nominal)
	ClusterQueueResourceBorrowingLimit.WithLabelValues(string(cohort), queue, flavor, resource, role).Set(borrowing)
	ClusterQueueResourceLendingLimit.WithLabelValues(string(cohort), queue, flavor, resource, role).Set(lending)
}

func ReportClusterQueueResourceReservations(cohort kueue.CohortReference, queue, flavor, resource string, usage float64, tracker *roletracker.RoleTracker) {
	ClusterQueueResourceReservations.WithLabelValues(string(cohort), queue, flavor, resource, roletracker.GetRole(tracker)).Set(usage)
}

func ReportLocalQueueResourceReservations(lq LocalQueueReference, flavor, resource string, usage float64, tracker *roletracker.RoleTracker) {
	LocalQueueResourceReservations.WithLabelValues(string(lq.Name), lq.Namespace, flavor, resource, roletracker.GetRole(tracker)).Set(usage)
}

func ReportClusterQueueResourceUsage(cohort kueue.CohortReference, queue, flavor, resource string, usage float64, tracker *roletracker.RoleTracker) {
	ClusterQueueResourceUsage.WithLabelValues(string(cohort), queue, flavor, resource, roletracker.GetRole(tracker)).Set(usage)
}

func ReportLocalQueueResourceUsage(lq LocalQueueReference, flavor, resource string, usage float64, tracker *roletracker.RoleTracker) {
	LocalQueueResourceUsage.WithLabelValues(string(lq.Name), lq.Namespace, flavor, resource, roletracker.GetRole(tracker)).Set(usage)
}

func ReportClusterQueueWeightedShare(cq, cohort string, weightedShare float64, tracker *roletracker.RoleTracker) {
	ClusterQueueWeightedShare.WithLabelValues(cq, cohort, roletracker.GetRole(tracker)).Set(weightedShare)
}

func ReportCohortWeightedShare(cohort string, weightedShare float64, tracker *roletracker.RoleTracker) {
	CohortWeightedShare.WithLabelValues(cohort, roletracker.GetRole(tracker)).Set(weightedShare)
}

func ClearClusterQueueResourceMetrics(cqName string) {
	lbls := prometheus.Labels{
		"cluster_queue": cqName,
	}
	ClusterQueueResourceNominalQuota.DeletePartialMatch(lbls)
	ClusterQueueResourceBorrowingLimit.DeletePartialMatch(lbls)
	ClusterQueueResourceLendingLimit.DeletePartialMatch(lbls)
	ClusterQueueResourceUsage.DeletePartialMatch(lbls)
	ClusterQueueResourceReservations.DeletePartialMatch(lbls)
}

func ClearLocalQueueResourceMetrics(lq LocalQueueReference) {
	lbls := prometheus.Labels{
		"name":      string(lq.Name),
		"namespace": lq.Namespace,
	}
	LocalQueueResourceReservations.DeletePartialMatch(lbls)
	LocalQueueResourceUsage.DeletePartialMatch(lbls)
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
		ReservingActiveWorkloads,
		AdmittedActiveWorkloads,
		QuotaReservedWorkloadsTotal,
		QuotaReservedWaitTime,
		FinishedWorkloads,
		FinishedWorkloadsTotal,
		PodsReadyToEvictedTimeSeconds,
		AdmittedWorkloadsTotal,
		EvictedWorkloadsTotal,
		EvictedWorkloadsOnceTotal,
		PreemptedWorkloadsTotal,
		AdmissionWaitTime,
		AdmissionChecksWaitTime,
		QueuedUntilReadyWaitTime,
		AdmittedUntilReadyWaitTime,
		ClusterQueueResourceUsage,
		ClusterQueueByStatus,
		ClusterQueueResourceReservations,
		ClusterQueueResourceNominalQuota,
		ClusterQueueResourceBorrowingLimit,
		ClusterQueueResourceLendingLimit,
		ClusterQueueWeightedShare,
		CohortWeightedShare,
	)
	if features.Enabled(features.LocalQueueMetrics) {
		RegisterLQMetrics()
	}
}

func RegisterLQMetrics() {
	metrics.Registry.MustRegister(
		LocalQueuePendingWorkloads,
		LocalQueueReservingActiveWorkloads,
		LocalQueueAdmittedActiveWorkloads,
		LocalQueueQuotaReservedWorkloadsTotal,
		LocalQueueFinishedWorkloads,
		LocalQueueFinishedWorkloadsTotal,
		LocalQueueQuotaReservedWaitTime,
		LocalQueueAdmittedWorkloadsTotal,
		LocalQueueAdmissionWaitTime,
		LocalQueueAdmissionChecksWaitTime,
		LocalQueueQueuedUntilReadyWaitTime,
		LocalQueueAdmittedUntilReadyWaitTime,
		LocalQueueEvictedWorkloadsTotal,
		LocalQueueByStatus,
		LocalQueueResourceReservations,
		LocalQueueResourceUsage,
	)
}
