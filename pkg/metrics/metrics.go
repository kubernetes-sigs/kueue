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
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
)

type AdmissionResult string

const (
	subsystemName = "kueue"

	SuccessAdmissionResult      AdmissionResult = "success"
	InadmissibleAdmissionResult AdmissionResult = "inadmissible"
)

var (
	admissionAttemptsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: subsystemName,
			Name:      "admission_attempts_total",
			Help:      "Total number of attempts to admit one or more workloads, broken down by result. `success` means that at least one workload was admitted, `inadmissible` means that no workload was admitted.",
		}, []string{"result"},
	)

	admissionAttemptDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: subsystemName,
			Name:      "admission_attempt_duration_seconds",
			Help:      "Latency of an admission attempt, broken down by result.",
		}, []string{"result"},
	)

	// Metrics tied to the queue system.

	PendingWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: subsystemName,
			Name:      "pending_workloads",
			Help:      "Number of pending workloads, per cluster_queue.",
		}, []string{"cluster_queue"},
	)

	AdmittedWorkloadsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: subsystemName,
			Name:      "admitted_workloads_total",
			Help:      "Total number of admitted workloads per cluster_queue",
		}, []string{"cluster_queue"},
	)

	admissionWaitTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: subsystemName,
			Name:      "admission_wait_time_seconds",
			Help:      "The wait time since a workload was created until it was admitted, per cluster_queue",
		}, []string{"cluster_queue"},
	)

	// Metrics tied to the cache.

	AdmittedActiveWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: subsystemName,
			Name:      "admitted_active_workloads",
			Help:      "Number of admitted workloads that are active (unsuspended and not finished), per cluster_queue",
		}, []string{"cluster_queue"},
	)
)

func AdmissionAttempt(result AdmissionResult, duration time.Duration) {
	admissionAttemptsTotal.WithLabelValues(string(result)).Inc()
	admissionAttemptDuration.WithLabelValues(string(result)).Observe(duration.Seconds())
}

func AdmittedWorkload(cqName kueue.ClusterQueueReference, waitTime time.Duration) {
	AdmittedWorkloadsTotal.WithLabelValues(string(cqName)).Inc()
	admissionWaitTime.WithLabelValues(string(cqName)).Observe(waitTime.Seconds())
}

func ClearQueueSystemMetrics(cqName string) {
	PendingWorkloads.DeleteLabelValues(cqName)
	AdmittedWorkloadsTotal.DeleteLabelValues(cqName)
	admissionWaitTime.DeleteLabelValues(cqName)
}

func Register() {
	metrics.Registry.MustRegister(
		admissionAttemptsTotal,
		admissionAttemptDuration,
		PendingWorkloads,
		AdmittedActiveWorkloads,
		AdmittedWorkloadsTotal,
		admissionWaitTime,
	)
}
