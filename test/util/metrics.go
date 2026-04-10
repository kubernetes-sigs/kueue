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

func ExpectLQPendingWorkloadsMetric(lq *kueue.LocalQueue, active, inadmissible int) {
	ginkgo.GinkgoHelper()
	vals := []int{active, inadmissible}
	for i, status := range pendingStatuses {
		metric := metrics.LocalQueuePendingWorkloads.WithLabelValues(lq.Name, lq.Namespace, status, roletracker.RoleStandalone)
		expectGaugeMetric(metric, gomega.Equal(float64(vals[i])), "pending_workloads with status=%s", status)
	}
}

func ExpectLQReservingActiveWorkloadsMetric(lq *kueue.LocalQueue, value int) {
	ginkgo.GinkgoHelper()
	metric := metrics.LocalQueueReservingActiveWorkloads.WithLabelValues(lq.Name, lq.Namespace, roletracker.RoleStandalone)
	expectGaugeMetric(metric, gomega.Equal(float64(value)))
}

func ExpectLQAdmittedWorkloadsTotalMetric(lq *kueue.LocalQueue, priorityClass string, value int) {
	ginkgo.GinkgoHelper()
	expectCounterMetric(metrics.LocalQueueAdmittedWorkloadsTotal, value,
		lq.Name, lq.Namespace, priorityClass, roletracker.RoleStandalone)
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

func ExpectPendingWorkloadsMetric(cq *kueue.ClusterQueue, active, inadmissible int) {
	ginkgo.GinkgoHelper()
	vals := []int{active, inadmissible}
	for i, status := range pendingStatuses {
		metric := metrics.PendingWorkloads.WithLabelValues(cq.Name, status, roletracker.RoleStandalone)
		expectGaugeMetric(metric, gomega.Equal(float64(vals[i])), "pending_workloads with status=%s", status)
	}
}

func ExpectReservingActiveWorkloadsMetric(cq *kueue.ClusterQueue, value int) {
	ginkgo.GinkgoHelper()
	metric := metrics.ReservingActiveWorkloads.WithLabelValues(cq.Name, roletracker.RoleStandalone)
	expectGaugeMetric(metric, gomega.Equal(float64(value)))
}

func ExpectAdmittedWorkloadsTotalMetric(cq *kueue.ClusterQueue, priorityClass string, v int) {
	ginkgo.GinkgoHelper()
	expectCounterMetric(metrics.AdmittedWorkloadsTotal, v,
		cq.Name, priorityClass, roletracker.RoleStandalone)
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

func ExpectLQEvictedWorkloadsTotalMetric(lq *kueue.LocalQueue, reason, underlyingCause, priorityClass string, v int) {
	ginkgo.GinkgoHelper()
	expectCounterMetric(metrics.LocalQueueEvictedWorkloadsTotal, v,
		lq.Name, lq.Namespace, reason, underlyingCause, priorityClass, roletracker.RoleStandalone)
}

func ExpectPreemptedWorkloadsTotalMetric(preemptorCqName, reason string, v int) {
	ginkgo.GinkgoHelper()
	expectCounterMetric(metrics.PreemptedWorkloadsTotal, v,
		preemptorCqName, reason, roletracker.RoleStandalone)
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
	metric := metrics.FinishedWorkloads.WithLabelValues(cq.Name, roletracker.RoleStandalone)
	expectGaugeMetric(metric, gomega.Equal(float64(count)))
}

func ExpectLQFinishedWorkloadsGaugeMetric(lq *kueue.LocalQueue, count int) {
	ginkgo.GinkgoHelper()
	metric := metrics.LocalQueueFinishedWorkloads.WithLabelValues(lq.Name, lq.Namespace, roletracker.RoleStandalone)
	expectGaugeMetric(metric, gomega.Equal(float64(count)))
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
		metric := metrics.ClusterQueueByStatus.WithLabelValues(cq.Name, string(s), roletracker.RoleStandalone)
		expectGaugeMetric(metric, gomega.Equal(wantV), "cluster_queue_status with status=%s", s)
	}
}

func ExpectClusterQueueWeightedShareMetric(cq *kueue.ClusterQueue, value float64) {
	ginkgo.GinkgoHelper()
	metric := metrics.ClusterQueueWeightedShare.WithLabelValues(cq.Name, string(cq.Spec.CohortName), roletracker.RoleStandalone)
	expectGaugeMetric(metric, gomega.Equal(value))
}

func ExpectLocalQueueResourceMetric(queue *kueue.LocalQueue, flavorName, resourceName string, value float64) {
	ginkgo.GinkgoHelper()
	metric := metrics.LocalQueueResourceUsage.WithLabelValues(queue.Name, queue.Namespace, flavorName, resourceName, roletracker.RoleStandalone)
	expectGaugeMetric(metric, gomega.Equal(value))
}

func ExpectLocalQueueResourceReservationsMetric(queue *kueue.LocalQueue, flavorName, resourceName string, value float64) {
	ginkgo.GinkgoHelper()
	metric := metrics.LocalQueueResourceReservations.WithLabelValues(queue.Name, queue.Namespace, flavorName, resourceName, roletracker.RoleStandalone)
	expectGaugeMetric(metric, gomega.Equal(value))
}

func ExpectCQResourceNominalQuota(cq *kueue.ClusterQueue, flavor, resource string, value float64) {
	ginkgo.GinkgoHelper()
	metric := metrics.ClusterQueueResourceNominalQuota.WithLabelValues(string(cq.Spec.CohortName), cq.Name, flavor, resource, roletracker.RoleStandalone)
	expectGaugeMetric(metric, gomega.Equal(value))
}

func ExpectCQResourceBorrowingQuota(cq *kueue.ClusterQueue, flavor, resource string, value float64) {
	ginkgo.GinkgoHelper()
	metric := metrics.ClusterQueueResourceBorrowingLimit.WithLabelValues(string(cq.Spec.CohortName), cq.Name, flavor, resource, roletracker.RoleStandalone)
	expectGaugeMetric(metric, gomega.Equal(value))
}

func ExpectCQResourceReservations(cq *kueue.ClusterQueue, flavor, resource string, value float64) {
	ginkgo.GinkgoHelper()
	metric := metrics.ClusterQueueResourceReservations.WithLabelValues(string(cq.Spec.CohortName), cq.Name, flavor, resource, roletracker.RoleStandalone)
	expectGaugeMetric(metric, gomega.Equal(value))
}

func expectHistogramMetric(metric *prometheus.HistogramVec, matcher gomegatypes.GomegaMatcher, lvs ...string) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		v, err := testutil.GetHistogramMetricCount(metric.WithLabelValues(lvs...))
		g.Expect(err).ToNot(gomega.HaveOccurred())
		g.Expect(int(v)).Should(matcher)
	}, Timeout, Interval).Should(gomega.Succeed())
}

func expectGaugeMetric(metric prometheus.Gauge, matcher gomegatypes.GomegaMatcher, msgAndArgs ...any) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		v, err := testutil.GetGaugeMetricValue(metric)
		g.Expect(err).ToNot(gomega.HaveOccurred())
		g.Expect(v).Should(matcher, msgAndArgs...)
	}, Timeout, Interval).Should(gomega.Succeed())
}
