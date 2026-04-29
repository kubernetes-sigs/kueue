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

package util

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	gomegatypes "github.com/onsi/gomega/types"
	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/metrics/testutil"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

func ExpectPendingAdmissionAttempts(want int, operation string) {
	expectAdmissionAttempts(want, operation, metrics.AdmissionResultInadmissible)
}

func ExpectSuccessfulAdmissionAttempts(want int, operation string) {
	expectAdmissionAttempts(want, operation, metrics.AdmissionResultSuccess)
}

func expectAdmissionAttempts(want int, operation string, result metrics.AdmissionResult) {
	ginkgo.GinkgoHelper()
	metric := metrics.AdmissionAttemptsTotal.WithLabelValues(string(result), roletracker.RoleStandalone)
	gomega.Eventually(func(g gomega.Gomega) {
		v, err := testutil.GetCounterMetricValue(metric)
		g.Expect(err).ToNot(gomega.HaveOccurred())
		g.Expect(int(v)).Should(gomega.BeNumerically(operation, want), "pending_workloads with status=%s", result)
	}, Timeout, Interval).WithOffset(2).Should(gomega.Succeed())
}

var pendingStatuses = []string{metrics.PendingStatusActive, metrics.PendingStatusInadmissible}

func ExpectLQPendingWorkloadsMetric(lq *kueue.LocalQueue, active, inadmissible int, customLabels ...string) {
	ginkgo.GinkgoHelper()
	vals := []int{active, inadmissible}
	for i, status := range pendingStatuses {
		lvs := append([]string{lq.Name, lq.Namespace, status, roletracker.RoleStandalone}, customLabels...)
		expectGaugeMetric(metrics.LocalQueuePendingWorkloads, lvs, gomega.Equal(float64(vals[i])), "pending_workloads with status=%s", status)
	}
}

func ExpectLQReservingActiveWorkloadsMetric(lq *kueue.LocalQueue, value int) {
	ginkgo.GinkgoHelper()
	lvs := []string{lq.Name, lq.Namespace, roletracker.RoleStandalone}
	expectGaugeMetric(metrics.LocalQueueReservingActiveWorkloads, lvs, gomega.Equal(float64(value)))
}

func ExpectLQAdmittedWorkloadsTotalMetric(lq *kueue.LocalQueue, priorityClass string, value int, customLabels ...string) {
	ginkgo.GinkgoHelper()
	expectCounterMetric(metrics.LocalQueueAdmittedWorkloadsTotal, value,
		append([]string{lq.Name, lq.Namespace, priorityClass, roletracker.RoleStandalone}, customLabels...)...)
}

func ExpectLQQuotaReservedWorkloadsTotalMetric(lq *kueue.LocalQueue, priorityClass string, value int) {
	ginkgo.GinkgoHelper()
	expectCounterMetric(metrics.LocalQueueQuotaReservedWorkloadsTotal, value,
		lq.Name, lq.Namespace, priorityClass, roletracker.RoleStandalone)
}

func ExpectLQByStatusMetric(lq *kueue.LocalQueue, status metav1.ConditionStatus) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		for i, s := range metrics.ConditionStatusValues {
			var wantV float64
			if metrics.ConditionStatusValues[i] == status {
				wantV = 1
			}
			metric := metrics.LocalQueueByStatus.WithLabelValues(lq.Name, lq.Namespace, string(s), roletracker.RoleStandalone)
			v, err := testutil.GetGaugeMetricValue(metric)
			g.Expect(err).ToNot(gomega.HaveOccurred())
			g.Expect(v).Should(gomega.Equal(wantV), "local_queue_status with status=%s", s)
		}
	}, Timeout, Interval).Should(gomega.Succeed())
}

func ExpectPendingWorkloadsMetric(cq *kueue.ClusterQueue, active, inadmissible int, customLabels ...string) {
	ginkgo.GinkgoHelper()
	vals := []int{active, inadmissible}
	for i, status := range pendingStatuses {
		lvs := append([]string{cq.Name, status, roletracker.RoleStandalone}, customLabels...)
		expectGaugeMetric(metrics.PendingWorkloads, lvs, gomega.Equal(float64(vals[i])), "pending_workloads with status=%s", status)
	}
}

func ExpectReservingActiveWorkloadsMetric(cq *kueue.ClusterQueue, value int) {
	ginkgo.GinkgoHelper()
	lvs := []string{cq.Name, roletracker.RoleStandalone}
	expectGaugeMetric(metrics.ReservingActiveWorkloads, lvs, gomega.Equal(float64(value)))
}

func ExpectAdmittedWorkloadsTotalMetric(cq *kueue.ClusterQueue, priorityClass string, v int, customLabels ...string) {
	ginkgo.GinkgoHelper()
	expectCounterMetric(metrics.AdmittedWorkloadsTotal, v,
		append([]string{cq.Name, priorityClass, roletracker.RoleStandalone}, customLabels...)...)
}

func ExpectAdmissionWaitTimeMetric(cq *kueue.ClusterQueue, priorityClass string, count int) {
	ginkgo.GinkgoHelper()
	expectHistogramMetric(metrics.AdmissionWaitTime, gomega.Equal(count), cq.Name, priorityClass, roletracker.RoleStandalone)
}

func ExpectAdmissionChecksWaitTimeMetric(cq *kueue.ClusterQueue, priorityClass string, count int) {
	ginkgo.GinkgoHelper()
	expectHistogramMetric(metrics.AdmissionChecksWaitTime, gomega.Equal(count), cq.Name, priorityClass, roletracker.RoleStandalone)
}

func ExpectLQAdmissionChecksWaitTimeMetric(lq *kueue.LocalQueue, priorityClass string, count int) {
	ginkgo.GinkgoHelper()
	expectHistogramMetric(metrics.LocalQueueAdmissionChecksWaitTime, gomega.Equal(count), lq.Name, lq.Namespace, priorityClass, roletracker.RoleStandalone)
}

func ExpectReadyWaitTimeMetricAtLeast(cq *kueue.ClusterQueue, priorityClass string, minCount int) {
	ginkgo.GinkgoHelper()
	expectHistogramMetric(metrics.QueuedUntilReadyWaitTime, gomega.BeNumerically(">=", minCount), cq.Name, priorityClass, roletracker.RoleStandalone)
}

func ExpectAdmittedUntilReadyWaitTimeMetricAtLeast(cq *kueue.ClusterQueue, priorityClass string, minCount int) {
	ginkgo.GinkgoHelper()
	expectHistogramMetric(metrics.AdmittedUntilReadyWaitTime, gomega.BeNumerically(">=", minCount), cq.Name, priorityClass, roletracker.RoleStandalone)
}

func ExpectLocalQueueReadyWaitTimeMetricAtLeast(lq *kueue.LocalQueue, priorityClass string, minCount int) {
	ginkgo.GinkgoHelper()
	expectHistogramMetric(metrics.LocalQueueQueuedUntilReadyWaitTime, gomega.BeNumerically(">=", minCount), lq.Name, lq.Namespace, priorityClass, roletracker.RoleStandalone)
}

func ExpectLocalQueueAdmittedUntilReadyWaitTimeMetricAtLeast(lq *kueue.LocalQueue, priorityClass string, minCount int) {
	ginkgo.GinkgoHelper()
	expectHistogramMetric(metrics.LocalQueueAdmittedUntilReadyWaitTime, gomega.BeNumerically(">=", minCount), lq.Name, lq.Namespace, priorityClass, roletracker.RoleStandalone)
}

func ExpectLocalQueueReservedWaitTimeMetric(lq *kueue.LocalQueue, priorityClass string, count int) {
	ginkgo.GinkgoHelper()
	expectHistogramMetric(metrics.LocalQueueQuotaReservedWaitTime, gomega.Equal(count), lq.Name, lq.Namespace, priorityClass, roletracker.RoleStandalone)
}

func ExpectEvictedWorkloadsTotalMetric(cqName, reason, underlyingCause, priorityClass string, v int) {
	ginkgo.GinkgoHelper()
	expectCounterMetric(metrics.EvictedWorkloadsTotal, v,
		cqName, reason, underlyingCause, priorityClass, roletracker.RoleStandalone)
}

func ExpectPodsReadyToEvictedTimeSeconds(cqName, reason, underlyingCause string, v int) {
	ginkgo.GinkgoHelper()
	expectHistogramMetric(metrics.PodsReadyToEvictedTimeSeconds, gomega.Equal(v), cqName, reason, underlyingCause, roletracker.RoleStandalone)
}

func ExpectEvictedWorkloadsOnceTotalMetric(cqName string, reason, underlyingCause, priorityClass string, v int) {
	ginkgo.GinkgoHelper()
	expectCounterMetric(metrics.EvictedWorkloadsOnceTotal, v,
		cqName, reason, underlyingCause, priorityClass, roletracker.RoleStandalone)
}

func ExpectLQEvictedWorkloadsTotalMetric(lq *kueue.LocalQueue, reason, underlyingCause, priorityClass string, v int, customLabels ...string) {
	ginkgo.GinkgoHelper()
	expectCounterMetric(metrics.LocalQueueEvictedWorkloadsTotal, v,
		append([]string{lq.Name, lq.Namespace, reason, underlyingCause, priorityClass, roletracker.RoleStandalone}, customLabels...)...)
}

func ExpectPreemptedWorkloadsTotalMetric(preemptorCqName, reason string, v int) {
	ginkgo.GinkgoHelper()
	expectCounterMetric(metrics.PreemptedWorkloadsTotal, v,
		preemptorCqName, reason, roletracker.RoleStandalone)
}

// ExpectWorkloadEvictionLatencyHistogramMetricAtLeast asserts bucket sample count for workload_eviction_latency_seconds.
func ExpectWorkloadEvictionLatencyHistogramMetricAtLeast(cqName kueue.ClusterQueueReference, reason string, minCount int) {
	ginkgo.GinkgoHelper()
	expectHistogramMetric(metrics.WorkloadEvictionLatencySeconds, gomega.BeNumerically(">=", minCount), string(cqName), reason, roletracker.RoleStandalone)
}

func ExpectQuotaReservedWorkloadsTotalMetric(cq *kueue.ClusterQueue, priorityClass string, v int) {
	ginkgo.GinkgoHelper()
	expectCounterMetric(metrics.QuotaReservedWorkloadsTotal, v,
		cq.Name, priorityClass, roletracker.RoleStandalone)
}

func ExpectQuotaReservedWaitTimeMetric(cq *kueue.ClusterQueue, priorityClass string, count int) {
	ginkgo.GinkgoHelper()
	expectHistogramMetric(metrics.QuotaReservedWaitTime, gomega.Equal(count), cq.Name, priorityClass, roletracker.RoleStandalone)
}

func ExpectFinishedWorkloadsTotalMetric(cq *kueue.ClusterQueue, priorityClass string, v int) {
	ginkgo.GinkgoHelper()
	expectCounterMetric(metrics.FinishedWorkloadsTotal, v,
		cq.Name, priorityClass, roletracker.RoleStandalone)
}

func ExpectLQFinishedWorkloadsTotalMetric(lq *kueue.LocalQueue, priorityClass string, value int) {
	ginkgo.GinkgoHelper()
	expectCounterMetric(metrics.LocalQueueFinishedWorkloadsTotal, value,
		lq.Name, lq.Namespace, priorityClass, roletracker.RoleStandalone)
}

func ExpectFinishedWorkloadsGaugeMetric(cq *kueue.ClusterQueue, count int) {
	ginkgo.GinkgoHelper()
	lvs := []string{cq.Name, roletracker.RoleStandalone}
	expectGaugeMetric(metrics.FinishedWorkloads, lvs, gomega.Equal(float64(count)))
}

func ExpectLQFinishedWorkloadsGaugeMetric(lq *kueue.LocalQueue, count int) {
	ginkgo.GinkgoHelper()
	lvs := []string{lq.Name, lq.Namespace, roletracker.RoleStandalone}
	expectGaugeMetric(metrics.LocalQueueFinishedWorkloads, lvs, gomega.Equal(float64(count)))
}

func expectCounterMetric(metric *prometheus.CounterVec, count int, lvs ...string) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		v, err := testutil.GetCounterMetricValue(metric.WithLabelValues(lvs...))
		g.Expect(err).ToNot(gomega.HaveOccurred())
		g.Expect(int(v)).Should(gomega.Equal(count))
	}, Timeout, Interval).Should(gomega.Succeed())
}

func ExpectLQAdmissionWaitTimeMetric(lq *kueue.LocalQueue, priorityClass string, count int) {
	ginkgo.GinkgoHelper()
	expectHistogramMetric(metrics.LocalQueueAdmissionWaitTime, gomega.Equal(count), lq.Name, lq.Namespace, priorityClass, roletracker.RoleStandalone)
}

func ExpectClusterQueueStatusMetric(cq *kueue.ClusterQueue, status metrics.ClusterQueueStatus) {
	ginkgo.GinkgoHelper()
	for i, s := range metrics.CQStatuses {
		var wantV float64
		if metrics.CQStatuses[i] == status {
			wantV = 1
		}
		lvs := []string{cq.Name, string(s), roletracker.RoleStandalone}
		expectGaugeMetric(metrics.ClusterQueueByStatus, lvs, gomega.Equal(wantV), "cluster_queue_status with status=%s", s)
	}
}

func ExpectClusterQueueWeightedShareMetric(cq *kueue.ClusterQueue, value float64) {
	ginkgo.GinkgoHelper()
	lvs := []string{cq.Name, string(cq.Spec.CohortName), roletracker.RoleStandalone}
	expectGaugeMetric(metrics.ClusterQueueWeightedShare, lvs, gomega.Equal(value))
}

func ExpectLocalQueueResourceMetric(queue *kueue.LocalQueue, flavorName, resourceName string, value float64) {
	ginkgo.GinkgoHelper()
	lvs := []string{queue.Name, queue.Namespace, flavorName, resourceName, roletracker.RoleStandalone}
	expectGaugeMetric(metrics.LocalQueueResourceUsage, lvs, gomega.Equal(value))
}

func ExpectLocalQueueResourceReservationsMetric(queue *kueue.LocalQueue, flavorName, resourceName string, value float64) {
	ginkgo.GinkgoHelper()
	lvs := []string{queue.Name, queue.Namespace, flavorName, resourceName, roletracker.RoleStandalone}
	expectGaugeMetric(metrics.LocalQueueResourceReservations, lvs, gomega.Equal(value))
}

func ExpectCQResourceNominalQuota(cq *kueue.ClusterQueue, flavor, resource string, value float64) {
	ginkgo.GinkgoHelper()
	lvs := []string{string(cq.Spec.CohortName), cq.Name, flavor, resource, roletracker.RoleStandalone}
	expectGaugeMetric(metrics.ClusterQueueResourceNominalQuota, lvs, gomega.Equal(value))
}

func ExpectCQResourceBorrowingQuota(cq *kueue.ClusterQueue, flavor, resource string, value float64) {
	ginkgo.GinkgoHelper()
	lvs := []string{string(cq.Spec.CohortName), cq.Name, flavor, resource, roletracker.RoleStandalone}
	expectGaugeMetric(metrics.ClusterQueueResourceBorrowingLimit, lvs, gomega.Equal(value))
}

func ExpectCQResourceReservations(cq *kueue.ClusterQueue, flavor, resource string, value float64) {
	ginkgo.GinkgoHelper()
	lvs := []string{string(cq.Spec.CohortName), cq.Name, flavor, resource, roletracker.RoleStandalone}
	expectGaugeMetric(metrics.ClusterQueueResourceReservations, lvs, gomega.Equal(value))
}

func ExpectCQResourcePendingMetric(cq *kueue.ClusterQueue, resource string, matcher gomegatypes.GomegaMatcher) {
	ginkgo.GinkgoHelper()
	lvs := []string{cq.Name, resource, roletracker.RoleStandalone}
	expectGaugeMetric(metrics.ClusterQueueResourcePending, lvs, matcher)
}

func expectHistogramMetric(metric *prometheus.HistogramVec, matcher gomegatypes.GomegaMatcher, lvs ...string) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		v, err := testutil.GetHistogramMetricCount(metric.WithLabelValues(lvs...))
		g.Expect(err).ToNot(gomega.HaveOccurred())
		g.Expect(int(v)).Should(matcher)
	}, Timeout, Interval).Should(gomega.Succeed())
}

func expectGaugeMetric(metric *prometheus.GaugeVec, lvs []string, matcher gomegatypes.GomegaMatcher, msgAndArgs ...any) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		v, err := testutil.GetGaugeMetricValue(metric.WithLabelValues(lvs...))
		g.Expect(err).ToNot(gomega.HaveOccurred())
		g.Expect(v).Should(matcher, msgAndArgs...)
	}, Timeout, Interval).Should(gomega.Succeed())
}

func ExpectCohortSubtreeQuotaGaugeMetric(cohortName string, flavor, resource string, count float64, customLabels ...string) {
	ginkgo.GinkgoHelper()
	lvs := append([]string{cohortName, flavor, resource, roletracker.RoleStandalone}, customLabels...)
	expectGaugeMetric(metrics.CohortSubtreeQuota, lvs, gomega.Equal(count))
}

func ExpectCohortSubtreeQuotaGaugeMetricCleaned(cohortName, flavor, resource string, customLabels ...string) {
	ginkgo.GinkgoHelper()
	ExpectCohortSubtreeQuotaGaugeMetric(cohortName, flavor, resource, 0, customLabels...)
}

func ExpectCohortSubtreeAdmittedWorkloadsTotalMetric(cohortName kueue.CohortReference, priorityClass string, v int, customLabels ...string) {
	ginkgo.GinkgoHelper()
	expectCounterMetric(metrics.CohortSubtreeAdmittedWorkloadsTotal, v,
		append([]string{string(cohortName), priorityClass, roletracker.RoleStandalone}, customLabels...)...)
}

func ExpectCohortSubtreeResourceReservationsGaugeMetric(cohortName string, flavor, resource string, count float64, customLabels ...string) {
	ginkgo.GinkgoHelper()
	lvs := append([]string{cohortName, flavor, resource, roletracker.RoleStandalone}, customLabels...)
	expectGaugeMetric(metrics.CohortSubtreeResourceReservations, lvs, gomega.Equal(count))
}

func ExpectCohortSubtreeResourceReservationsGaugeMetricCleaned(cohortName, flavor, resource string, customLabels ...string) {
	ginkgo.GinkgoHelper()
	ExpectCohortSubtreeResourceReservationsGaugeMetric(cohortName, flavor, resource, 0, customLabels...)
}

func ExpectAdmittedActiveWorkloadsGaugeMetric(clusterQueue kueue.ClusterQueueReference, count float64) {
	ginkgo.GinkgoHelper()
	lvs := []string{string(clusterQueue), roletracker.RoleStandalone}
	expectGaugeMetric(metrics.AdmittedActiveWorkloads, lvs, gomega.Equal(count))
}

func ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric(cohortName kueue.CohortReference, count float64) {
	ginkgo.GinkgoHelper()
	lvs := []string{string(cohortName), roletracker.RoleStandalone}
	expectGaugeMetric(metrics.CohortSubtreeAdmittedActiveWorkloads, lvs, gomega.Equal(count))
}
