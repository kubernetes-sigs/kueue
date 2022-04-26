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
)

type AdmissionResult string

const (
	subsystemName = "kueue"

	SuccessAdmissionResult      AdmissionResult = "success"
	InadmissibleAdmissionResult AdmissionResult = "inadmissible"
)

var (
	admissionAttempts = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: subsystemName,
			Name:      "admission_attempts_total",
			Help:      "Number of attempts to admit pods, by result. `success` means that at least one workload was admitted, `inadmissible` means that no workload was admitted.",
		}, []string{"result"},
	)

	admissionAttemptLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: subsystemName,
			Name:      "admission_attempt_duration_seconds",
			Help:      "Latency of an admission attempt",
		}, []string{"result"},
	)
)

func AdmissionAttempt(result AdmissionResult, duration time.Duration) {
	admissionAttempts.WithLabelValues(string(result)).Inc()
	admissionAttemptLatency.WithLabelValues(string(result)).Observe(duration.Seconds())
}

func init() {
	metrics.Registry.MustRegister(
		admissionAttempts,
		admissionAttemptLatency,
	)
}
