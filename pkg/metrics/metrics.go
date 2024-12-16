/*
Copyright 2022 The Kubernetes Authors.

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
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
)

type AdmissionResult string
type ClusterQueueStatus string

type LocalQueueReference struct {
	Name      string
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

	AdmissionAttemptsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: constants.KueueName,
			Name:      "admission_attempts_total",
			Help: `The total number of attempts to admit workloads.
Each admission attempt might try to admit more than one workload.
The label 'result' can have the following values:
- 'success' means that at least one workload was admitted.,
- 'inadmissible' means that no workload was admitted.`,
		}, []string{"result"},
	)

	admissionAttemptDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "admission_attempt_duration_seconds",
			Help: `The latency of an admission attempt.
The label 'result' can have the following values:
- 'success' means that at least one workload was admitted.,
- 'inadmissible' means that no workload was admitted.`,
		}, []string{"result"},
	)

	AdmissionCyclePreemptionSkips = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "admission_cycle_preemption_skips",
			Help: "The number of Workloads in the ClusterQueue that got preemption candidates " +
				"but had to be skipped because other ClusterQueues needed the same resources in the same cycle",
		}, []string{"cluster_queue"},
	)

	// Metrics tied to the queue system.

	PendingWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "pending_workloads",
			Help: `The number of pending workloads, per 'cluster_queue' and 'status'.
'status' can have the following values:
- "active" means that the workloads are in the admission queue.
- "inadmissible" means there was a failed admission attempt for these workloads and they won't be retried until cluster conditions, which could make this workload admissible, change`,
		}, []string{"cluster_queue", "status"},
	)

	LocalQueuePendingWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_pending_workloads",
			Help: `The number of pending workloads, per 'local_queue' and 'status'.
'status' can have the following values:
- "active" means that the workloads are in the admission queue.
- "inadmissible" means there was a failed admission attempt for these workloads and they won't be retried until cluster conditions, which could make this workload admissible, change`,
		}, []string{"name", "namespace", "status"},
	)

	QuotaReservedWorkloadsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: constants.KueueName,
			Name:      "quota_reserved_workloads_total",
			Help:      "The total number of quota reserved workloads per 'cluster_queue'",
		}, []string{"cluster_queue"},
	)

	LocalQueueQuotaReservedWorkloadsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_quota_reserved_workloads_total",
			Help:      "The total number of quota reserved workloads per 'local_queue'",
		}, []string{"name", "namespace"},
	)

	quotaReservedWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "quota_reserved_wait_time_seconds",
			Help:      "The time between a workload was created or requeued until it got quota reservation, per 'cluster_queue'",
			Buckets:   generateExponentialBuckets(14),
		}, []string{"cluster_queue"},
	)

	localQueueQuotaReservedWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_quota_reserved_wait_time_seconds",
			Help:      "The time between a workload was created or requeued until it got quota reservation, per 'local_queue'",
			Buckets:   generateExponentialBuckets(14),
		}, []string{"name", "namespace"},
	)

	AdmittedWorkloadsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: constants.KueueName,
			Name:      "admitted_workloads_total",
			Help:      "The total number of admitted workloads per 'cluster_queue'",
		}, []string{"cluster_queue"},
	)

	LocalQueueAdmittedWorkloadsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_admitted_workloads_total",
			Help:      "The total number of admitted workloads per 'local_queue'",
		}, []string{"name", "namespace"},
	)

	admissionWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "admission_wait_time_seconds",
			Help:      "The time between a workload was created or requeued until admission, per 'cluster_queue'",
			Buckets:   generateExponentialBuckets(14),
		}, []string{"cluster_queue"},
	)

	localQueueAdmissionWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_admission_wait_time_seconds",
			Help:      "The time between a workload was created or requeued until admission, per 'local_queue'",
			Buckets:   generateExponentialBuckets(14),
		}, []string{"name", "namespace"},
	)

	admissionChecksWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "admission_checks_wait_time_seconds",
			Help:      "The time from when a workload got the quota reservation until admission, per 'cluster_queue'",
			Buckets:   generateExponentialBuckets(14),
		}, []string{"cluster_queue"},
	)

	localQueueAdmissionChecksWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_admission_checks_wait_time_seconds",
			Help:      "The time from when a workload got the quota reservation until admission, per 'local_queue'",
			Buckets:   generateExponentialBuckets(14),
		}, []string{"name", "namespace"},
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
- "Deactivated" means that the workload was evicted because spec.active is set to false`,
		}, []string{"cluster_queue", "reason"},
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
- "Deactivated" means that the workload was evicted because spec.active is set to false`,
		}, []string{"name", "namespace", "reason"},
	)

	PreemptedWorkloadsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: constants.KueueName,
			Name:      "preempted_workloads_total",
			Help: `The number of preempted workloads per 'preempting_cluster_queue',
The label 'reason' can have the following values:
- "InClusterQueue" means that the workload was preempted by a workload in the same ClusterQueue.
- "InCohortReclamation" means that the workload was preempted by a workload in the same cohort due to reclamation of nominal quota.
- "InCohortFairSharing" means that the workload was preempted by a workload in the same cohort due to fair sharing.
- "InCohortReclaimWhileBorrowing" means that the workload was preempted by a workload in the same cohort due to reclamation of nominal quota while borrowing.`,
		}, []string{"preempting_cluster_queue", "reason"},
	)

	// Metrics tied to the cache.

	ReservingActiveWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "reserving_active_workloads",
			Help:      "The number of Workloads that are reserving quota, per 'cluster_queue'",
		}, []string{"cluster_queue"},
	)

	LocalQueueReservingActiveWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_reserving_active_workloads",
			Help:      "The number of Workloads that are reserving quota, per 'localQueue'",
		}, []string{"name", "namespace"},
	)

	AdmittedActiveWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "admitted_active_workloads",
			Help:      "The number of admitted Workloads that are active (unsuspended and not finished), per 'cluster_queue'",
		}, []string{"cluster_queue"},
	)

	LocalQueueAdmittedActiveWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_admitted_active_workloads",
			Help:      "The number of admitted Workloads that are active (unsuspended and not finished), per 'localQueue'",
		}, []string{"name", "namespace"},
	)

	ClusterQueueByStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "cluster_queue_status",
			Help: `Reports 'cluster_queue' with its 'status' (with possible values 'pending', 'active' or 'terminated').
For a ClusterQueue, the metric only reports a value of 1 for one of the statuses.`,
		}, []string{"cluster_queue", "status"},
	)

	LocalQueueByStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_status",
			Help: `Reports 'localQueue' with its 'active' status (with possible values 'True', 'False', or 'Unknown').
For a LocalQueue, the metric only reports a value of 1 for one of the statuses.`,
		}, []string{"name", "namespace", "active"},
	)

	// Optional cluster queue metrics

	ClusterQueueResourceReservations = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "cluster_queue_resource_reservation",
			Help:      `Reports the cluster_queue's total resource reservation within all the flavors`,
		}, []string{"cohort", "cluster_queue", "flavor", "resource"},
	)

	ClusterQueueResourceUsage = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "cluster_queue_resource_usage",
			Help:      `Reports the cluster_queue's total resource usage within all the flavors`,
		}, []string{"cohort", "cluster_queue", "flavor", "resource"},
	)

	LocalQueueResourceReservations = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_resource_reservation",
			Help:      `Reports the localQueue's total resource reservation within all the flavors`,
		}, []string{"name", "namespace", "flavor", "resource"},
	)

	LocalQueueResourceUsage = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "local_queue_resource_usage",
			Help:      `Reports the localQueue's total resource usage within all the flavors`,
		}, []string{"name", "namespace", "flavor", "resource"},
	)

	ClusterQueueResourceNominalQuota = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "cluster_queue_nominal_quota",
			Help:      `Reports the cluster_queue's resource nominal quota within all the flavors`,
		}, []string{"cohort", "cluster_queue", "flavor", "resource"},
	)

	ClusterQueueResourceBorrowingLimit = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "cluster_queue_borrowing_limit",
			Help:      `Reports the cluster_queue's resource borrowing limit within all the flavors`,
		}, []string{"cohort", "cluster_queue", "flavor", "resource"},
	)

	ClusterQueueResourceLendingLimit = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "cluster_queue_lending_limit",
			Help:      `Reports the cluster_queue's resource lending limit within all the flavors`,
		}, []string{"cohort", "cluster_queue", "flavor", "resource"},
	)

	ClusterQueueWeightedShare = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: constants.KueueName,
			Name:      "cluster_queue_weighted_share",
			Help: `Reports a value that representing the maximum of the ratios of usage above nominal 
quota to the lendable resources in the cohort, among all the resources provided by 
the ClusterQueue, and divided by the weight.
If zero, it means that the usage of the ClusterQueue is below the nominal quota.
If the ClusterQueue has a weight of zero, this will return 9223372036854775807,
the maximum possible share value.`,
		}, []string{"cluster_queue"},
	)
)

func generateExponentialBuckets(count int) []float64 {
	return append([]float64{1}, prometheus.ExponentialBuckets(2.5, 2, count-1)...)
}

func AdmissionAttempt(result AdmissionResult, duration time.Duration) {
	AdmissionAttemptsTotal.WithLabelValues(string(result)).Inc()
	admissionAttemptDuration.WithLabelValues(string(result)).Observe(duration.Seconds())
}

func QuotaReservedWorkload(cqName kueue.ClusterQueueReference, waitTime time.Duration) {
	QuotaReservedWorkloadsTotal.WithLabelValues(string(cqName)).Inc()
	quotaReservedWaitTime.WithLabelValues(string(cqName)).Observe(waitTime.Seconds())
}

func LocalQueueQuotaReservedWorkload(lq LocalQueueReference, waitTime time.Duration) {
	LocalQueueQuotaReservedWorkloadsTotal.WithLabelValues(lq.Name, lq.Namespace).Inc()
	localQueueQuotaReservedWaitTime.WithLabelValues(lq.Name, lq.Namespace).Observe(waitTime.Seconds())
}

func AdmittedWorkload(cqName kueue.ClusterQueueReference, waitTime time.Duration) {
	AdmittedWorkloadsTotal.WithLabelValues(string(cqName)).Inc()
	admissionWaitTime.WithLabelValues(string(cqName)).Observe(waitTime.Seconds())
}

func LocalQueueAdmittedWorkload(lq LocalQueueReference, waitTime time.Duration) {
	LocalQueueAdmittedWorkloadsTotal.WithLabelValues(lq.Name, lq.Namespace).Inc()
	localQueueAdmissionWaitTime.WithLabelValues(lq.Name, lq.Namespace).Observe(waitTime.Seconds())
}

func AdmissionChecksWaitTime(cqName kueue.ClusterQueueReference, waitTime time.Duration) {
	admissionChecksWaitTime.WithLabelValues(string(cqName)).Observe(waitTime.Seconds())
}

func LocalQueueAdmissionChecksWaitTime(lq LocalQueueReference, waitTime time.Duration) {
	localQueueAdmissionChecksWaitTime.WithLabelValues(lq.Name, lq.Namespace).Observe(waitTime.Seconds())
}

func ReportPendingWorkloads(cqName string, active, inadmissible int) {
	PendingWorkloads.WithLabelValues(cqName, PendingStatusActive).Set(float64(active))
	PendingWorkloads.WithLabelValues(cqName, PendingStatusInadmissible).Set(float64(inadmissible))
}

func ReportLocalQueuePendingWorkloads(lq LocalQueueReference, active, inadmissible int) {
	LocalQueuePendingWorkloads.WithLabelValues(lq.Name, lq.Namespace, PendingStatusActive).Set(float64(active))
	LocalQueuePendingWorkloads.WithLabelValues(lq.Name, lq.Namespace, PendingStatusInadmissible).Set(float64(inadmissible))
}

func ReportEvictedWorkloads(cqName, reason string) {
	EvictedWorkloadsTotal.WithLabelValues(cqName, reason).Inc()
}

func ReportLocalQueueEvictedWorkloads(lq LocalQueueReference, reason string) {
	LocalQueueEvictedWorkloadsTotal.WithLabelValues(lq.Name, lq.Namespace, reason).Inc()
}

func ReportPreemption(preemptingCqName, preemptingReason, targetCqName string) {
	PreemptedWorkloadsTotal.WithLabelValues(preemptingCqName, preemptingReason).Inc()
	ReportEvictedWorkloads(targetCqName, kueue.WorkloadEvictedByPreemption)
}

func LQRefFromWorkload(wl *kueue.Workload) LocalQueueReference {
	return LocalQueueReference{
		Name:      wl.Spec.QueueName,
		Namespace: wl.Namespace,
	}
}

func LQRefFromLocalQueueKey(lqKey string) LocalQueueReference {
	split := strings.Split(lqKey, "/")
	return LocalQueueReference{
		Name:      split[1],
		Namespace: split[0],
	}
}

func ClearClusterQueueMetrics(cqName string) {
	AdmissionCyclePreemptionSkips.DeleteLabelValues(cqName)
	PendingWorkloads.DeleteLabelValues(cqName, PendingStatusActive)
	PendingWorkloads.DeleteLabelValues(cqName, PendingStatusInadmissible)
	QuotaReservedWorkloadsTotal.DeleteLabelValues(cqName)
	quotaReservedWaitTime.DeleteLabelValues(cqName)
	AdmittedWorkloadsTotal.DeleteLabelValues(cqName)
	admissionWaitTime.DeleteLabelValues(cqName)
	admissionChecksWaitTime.DeleteLabelValues(cqName)
	EvictedWorkloadsTotal.DeletePartialMatch(prometheus.Labels{"cluster_queue": cqName})
	PreemptedWorkloadsTotal.DeletePartialMatch(prometheus.Labels{"preempting_cluster_queue": cqName})
}

func ClearLocalQueueMetrics(lq LocalQueueReference) {
	LocalQueuePendingWorkloads.DeleteLabelValues(lq.Name, lq.Namespace, PendingStatusActive)
	LocalQueuePendingWorkloads.DeleteLabelValues(lq.Name, lq.Namespace, PendingStatusInadmissible)
	LocalQueueQuotaReservedWorkloadsTotal.DeleteLabelValues(lq.Name, lq.Namespace)
	localQueueQuotaReservedWaitTime.DeleteLabelValues(lq.Name, lq.Namespace)
	LocalQueueAdmittedWorkloadsTotal.DeleteLabelValues(lq.Name, lq.Namespace)
	localQueueAdmissionWaitTime.DeleteLabelValues(lq.Name, lq.Namespace)
	localQueueAdmissionChecksWaitTime.DeleteLabelValues(lq.Name, lq.Namespace)
	LocalQueueEvictedWorkloadsTotal.DeletePartialMatch(prometheus.Labels{"name": lq.Name, "namespace": lq.Namespace})
}

func ReportClusterQueueStatus(cqName string, cqStatus ClusterQueueStatus) {
	for _, status := range CQStatuses {
		var v float64
		if status == cqStatus {
			v = 1
		}
		ClusterQueueByStatus.WithLabelValues(cqName, string(status)).Set(v)
	}
}

var (
	ConditionStatusValues = []metav1.ConditionStatus{metav1.ConditionTrue, metav1.ConditionFalse, metav1.ConditionUnknown}
)

func ReportLocalQueueStatus(lq LocalQueueReference, conditionStatus metav1.ConditionStatus) {
	for _, status := range ConditionStatusValues {
		var v float64
		if status == conditionStatus {
			v = 1
		}
		LocalQueueByStatus.WithLabelValues(lq.Name, lq.Namespace, string(status)).Set(v)
	}
}

func ClearCacheMetrics(cqName string) {
	ReservingActiveWorkloads.DeleteLabelValues(cqName)
	AdmittedActiveWorkloads.DeleteLabelValues(cqName)
	for _, status := range CQStatuses {
		ClusterQueueByStatus.DeleteLabelValues(cqName, string(status))
	}
}

func ClearLocalQueueCacheMetrics(lq LocalQueueReference) {
	LocalQueueReservingActiveWorkloads.DeleteLabelValues(lq.Name, lq.Namespace)
	LocalQueueAdmittedActiveWorkloads.DeleteLabelValues(lq.Name, lq.Namespace)
	for _, status := range ConditionStatusValues {
		LocalQueueByStatus.DeleteLabelValues(lq.Name, lq.Namespace, string(status))
	}
}

func ReportClusterQueueQuotas(cohort, queue, flavor, resource string, nominal, borrowing, lending float64) {
	ClusterQueueResourceNominalQuota.WithLabelValues(cohort, queue, flavor, resource).Set(nominal)
	ClusterQueueResourceBorrowingLimit.WithLabelValues(cohort, queue, flavor, resource).Set(borrowing)
	if features.Enabled(features.LendingLimit) {
		ClusterQueueResourceLendingLimit.WithLabelValues(cohort, queue, flavor, resource).Set(lending)
	}
}

func ReportClusterQueueResourceReservations(cohort, queue, flavor, resource string, usage float64) {
	ClusterQueueResourceReservations.WithLabelValues(cohort, queue, flavor, resource).Set(usage)
}

func ReportLocalQueueResourceReservations(lq LocalQueueReference, flavor, resource string, usage float64) {
	LocalQueueResourceReservations.WithLabelValues(lq.Name, lq.Namespace, flavor, resource).Set(usage)
}

func ReportClusterQueueResourceUsage(cohort, queue, flavor, resource string, usage float64) {
	ClusterQueueResourceUsage.WithLabelValues(cohort, queue, flavor, resource).Set(usage)
}

func ReportLocalQueueResourceUsage(lq LocalQueueReference, flavor, resource string, usage float64) {
	LocalQueueResourceUsage.WithLabelValues(lq.Name, lq.Namespace, flavor, resource).Set(usage)
}

func ReportClusterQueueWeightedShare(cq string, weightedShare int64) {
	ClusterQueueWeightedShare.WithLabelValues(cq).Set(float64(weightedShare))
}

func ClearClusterQueueResourceMetrics(cqName string) {
	lbls := prometheus.Labels{
		"cluster_queue": cqName,
	}
	ClusterQueueResourceNominalQuota.DeletePartialMatch(lbls)
	ClusterQueueResourceBorrowingLimit.DeletePartialMatch(lbls)
	if features.Enabled(features.LendingLimit) {
		ClusterQueueResourceLendingLimit.DeletePartialMatch(lbls)
	}
	ClusterQueueResourceUsage.DeletePartialMatch(lbls)
	ClusterQueueResourceReservations.DeletePartialMatch(lbls)
}

func ClearLocalQueueResourceMetrics(lq LocalQueueReference) {
	lbls := prometheus.Labels{
		"name":      lq.Name,
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
	if features.Enabled(features.LendingLimit) {
		ClusterQueueResourceLendingLimit.DeletePartialMatch(lbls)
	}
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
		AdmissionAttemptsTotal,
		admissionAttemptDuration,
		AdmissionCyclePreemptionSkips,
		PendingWorkloads,
		ReservingActiveWorkloads,
		AdmittedActiveWorkloads,
		QuotaReservedWorkloadsTotal,
		quotaReservedWaitTime,
		AdmittedWorkloadsTotal,
		EvictedWorkloadsTotal,
		PreemptedWorkloadsTotal,
		admissionWaitTime,
		admissionChecksWaitTime,
		ClusterQueueResourceUsage,
		ClusterQueueByStatus,
		ClusterQueueResourceReservations,
		ClusterQueueResourceNominalQuota,
		ClusterQueueResourceBorrowingLimit,
		ClusterQueueResourceLendingLimit,
		ClusterQueueWeightedShare,
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
		localQueueQuotaReservedWaitTime,
		LocalQueueAdmittedWorkloadsTotal,
		localQueueAdmissionWaitTime,
		localQueueAdmissionChecksWaitTime,
		LocalQueueEvictedWorkloadsTotal,
		LocalQueueByStatus,
		LocalQueueResourceReservations,
		LocalQueueResourceUsage,
	)
}
