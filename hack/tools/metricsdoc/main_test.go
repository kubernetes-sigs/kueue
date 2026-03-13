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

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestExtractMetricsFromPackage(t *testing.T) {
	dir := t.TempDir()

	inline := `package metrics

import "github.com/prometheus/client_golang/prometheus"

const KueueName = "kueue"

var (
	// +metricsdoc:group=health
	// +metricsdoc:labels=result="possible values are success or inadmissible",replica_role="one of leader, follower, or standalone"
	AdmissionAttemptsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: KueueName,
			Name:      "admission_attempts_total",
			Help: "The total number of attempts to admit workloads. " +
				"Each admission attempt might try to admit more than one workload.",
		}, []string{"result", "replica_role"},
	)
)
`

	deferred := `package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	// +metricsdoc:group=clusterqueue
	// +metricsdoc:labels=cluster_queue="the name of the ClusterQueue",status="status label (varies by metric)"
	PendingWorkloads *prometheus.GaugeVec

	// +metricsdoc:group=health
	Duration *prometheus.HistogramVec
)

func initMetrics() {
	PendingWorkloads = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: KueueName,
			Name:      "pending_workloads",
			Help:      "The number of pending workloads, per cluster_queue and status",
		},
		[]string{"cluster_queue", "status"},
	)
	Duration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: KueueName,
			Name:      "admission_cycle_duration_seconds",
			Help:      "The latency of an admission cycle",
		},
		[]string{"operation"},
	)
}
`

	if err := os.WriteFile(filepath.Join(dir, "inline.go"), []byte(inline), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "deferred.go"), []byte(deferred), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := extractMetricsFromPackage(dir)
	if err != nil {
		t.Fatalf("extractMetricsFromPackage: %v", err)
	}

	want := []Metric{
		{
			FullName:  "kueue_admission_attempts_total",
			Type:      "Counter",
			Group:     "health",
			Help:      "The total number of attempts to admit workloads. Each admission attempt might try to admit more than one workload.",
			Labels:    []string{"result", "replica_role"},
			LabelDocs: map[string]string{"result": "possible values are success or inadmissible", "replica_role": "one of leader, follower, or standalone"},
		},
		{
			FullName: "kueue_admission_cycle_duration_seconds",
			Type:     "Histogram",
			Group:    "health",
			Help:     "The latency of an admission cycle",
			Labels:   []string{"operation"},
		},
		{
			FullName:  "kueue_pending_workloads",
			Type:      "Gauge",
			Group:     "clusterqueue",
			Help:      "The number of pending workloads, per cluster_queue and status",
			Labels:    []string{"cluster_queue", "status"},
			LabelDocs: map[string]string{"cluster_queue": "the name of the ClusterQueue", "status": "status label (varies by metric)"},
		},
	}

	slices.SortFunc(got, func(a, b Metric) int { return strings.Compare(a.FullName, b.FullName) })
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("unexpected metrics (-want/+got):\n%s", diff)
	}
}

func TestExtractMetricsMissingGroup(t *testing.T) {
	dir := t.TempDir()
	src := `package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	NoGroup = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "no_group_metric",
			Help: "Missing group marker",
		},
		[]string{"label"},
	)
)
`
	if err := os.WriteFile(filepath.Join(dir, "metrics.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := extractMetricsFromPackage(dir)
	if err == nil {
		t.Fatal("expected error for missing group marker")
	}
	wantErr := "missing metricsdoc:group marker on metrics: NoGroup"
	if err.Error() != wantErr {
		t.Errorf("error = %q, want %q", err.Error(), wantErr)
	}
}

func TestExtractMetricsWrapperAndAppendLabels(t *testing.T) {
	dir := t.TempDir()
	src := `package metrics

import "github.com/prometheus/client_golang/prometheus"

const KueueName = "kueue"

var extraLabels []string

func trackGaugeVec(g *prometheus.GaugeVec) *prometheus.GaugeVec { return g }

var (
	// +metricsdoc:group=clusterqueue
	PendingWorkloads *prometheus.GaugeVec
)

func initMetrics() {
	PendingWorkloads = trackGaugeVec(prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: KueueName,
			Name:      "pending_workloads",
			Help:      "The number of pending workloads",
		},
		append([]string{"cluster_queue", "status"}, extraLabels...),
	))
}
`
	if err := os.WriteFile(filepath.Join(dir, "metrics.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := extractMetricsFromPackage(dir)
	if err != nil {
		t.Fatalf("extractMetricsFromPackage: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(got))
	}
	want := Metric{
		FullName: "kueue_pending_workloads",
		Type:     "Gauge",
		Group:    "clusterqueue",
		Help:     "The number of pending workloads",
		Labels:   []string{"cluster_queue", "status"},
	}
	if diff := cmp.Diff(want, got[0]); diff != "" {
		t.Errorf("unexpected metric (-want/+got):\n%s", diff)
	}
}

func TestExtractMetricsUnresolvedDeferred(t *testing.T) {
	dir := t.TempDir()
	src := `package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	// +metricsdoc:group=health
	Orphan *prometheus.GaugeVec
)
`
	if err := os.WriteFile(filepath.Join(dir, "metrics.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := extractMetricsFromPackage(dir)
	if err == nil {
		t.Fatal("expected error for unresolved deferred var")
	}
	wantErr := "missing assignment for metrics with metricsdoc markers: Orphan"
	if err.Error() != wantErr {
		t.Errorf("error = %q, want %q", err.Error(), wantErr)
	}
}
