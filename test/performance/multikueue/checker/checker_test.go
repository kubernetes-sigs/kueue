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

package checker

import (
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	configuration "sigs.k8s.io/kueue/test/performance/multikueue/config"
	"sigs.k8s.io/kueue/test/performance/multikueue/report"
)

var (
	summaryFile      = flag.String("summary", "", "the MultiKueue benchmark summary")
	configFile       = flag.String("config", "", "the configuration used by the benchmark")
	expectationsFile = flag.String("expectations", "", "the expected performance bounds")
)

// expectations contains only performance assertions. The separately loaded configuration
// drives the runner and identifies the scenario these bounds apply to.
type expectations struct {
	MinThroughputPerSecond   float64 `json:"minThroughputPerSecond"`
	MaxAdmissionP95Ms        int64   `json:"maxAdmissionP95Ms"`
	MaxQuotaReservationP95Ms int64   `json:"maxQuotaReservationP95Ms"`
	// MaxWatchGaps tolerates the occasional lost watch while the separately checked latency and
	// throughput metrics remain in range, and fails a run that lost the event stream repeatedly.
	MaxWatchGaps int `json:"maxWatchGaps"`
}

func TestPerformance(t *testing.T) {
	if *summaryFile == "" && *configFile == "" && *expectationsFile == "" {
		t.Skip("summary, config and expectations flags are only supplied by the performance target")
	}
	if *summaryFile == "" || *configFile == "" || *expectationsFile == "" {
		t.Fatal("--summary, --config and --expectations are required")
	}

	summaryBytes, err := os.ReadFile(*summaryFile)
	if err != nil {
		t.Fatalf("Read summary: %v", err)
	}
	summary, err := decodeBenchmarkSummary(summaryBytes)
	if err != nil {
		t.Fatalf("Decode summary: %v", err)
	}

	cfg, err := configuration.Load(*configFile)
	if err != nil {
		t.Fatalf("Read configuration: %v", err)
	}
	expectationsBytes, err := os.ReadFile(*expectationsFile)
	if err != nil {
		t.Fatalf("Read expectations: %v", err)
	}
	expected, err := decodeExpectations(expectationsBytes)
	if err != nil {
		t.Fatalf("Decode expectations: %v", err)
	}

	for _, failure := range checkSummary(summary, cfg.Scenario(), expected) {
		t.Error(failure)
	}
}

// decodeBenchmarkSummary decodes a run's report. It is strict because it decodes the runner's own
// type, so an unknown field means the two have diverged.
func decodeBenchmarkSummary(data []byte) (report.Summary, error) {
	var summary report.Summary
	if err := yaml.UnmarshalStrict(data, &summary); err != nil {
		return report.Summary{}, err
	}
	var presence struct {
		WatchGaps *int `json:"watchGaps"`
	}
	if err := yaml.Unmarshal(data, &presence); err != nil {
		return report.Summary{}, err
	}
	if presence.WatchGaps == nil {
		return report.Summary{}, errors.New("watchGaps is missing")
	}
	if err := validateSummary(summary); err != nil {
		return report.Summary{}, err
	}
	return summary, nil
}

func decodeExpectations(data []byte) (expectations, error) {
	var expected expectations
	if err := yaml.UnmarshalStrict(data, &expected); err != nil {
		return expectations{}, err
	}
	if err := expected.validate(); err != nil {
		return expectations{}, err
	}
	return expected, nil
}

// validateSummary rejects a report that would satisfy a bound only because a measurement is
// missing. decodeBenchmarkSummary separately presence-checks required fields whose zero value is
// valid.
func validateSummary(s report.Summary) error {
	switch {
	case s.ThroughputPerSecond <= 0:
		return errors.New("throughputPerSecond is missing or not positive")
	case s.Latencies.AdmissionMs.P95Ms <= 0:
		return errors.New("latencies.admissionMs.p95Ms is missing or not positive")
	case s.Latencies.QuotaReservationMs.P95Ms <= 0:
		return errors.New("latencies.quotaReservationMs.p95Ms is missing or not positive")
	case s.WorkerDistribution == nil:
		return errors.New("workerDistribution is missing")
	default:
		return nil
	}
}

func (r expectations) validate() error {
	switch {
	case r.MinThroughputPerSecond <= 0:
		return errors.New("minThroughputPerSecond must be positive")
	case r.MaxAdmissionP95Ms <= 0:
		return errors.New("maxAdmissionP95Ms must be positive")
	case r.MaxQuotaReservationP95Ms <= 0:
		return errors.New("maxQuotaReservationP95Ms must be positive")
	case r.MaxWatchGaps < 0:
		return errors.New("maxWatchGaps must not be negative")
	default:
		return nil
	}
}

func checkSummary(summary report.Summary, expectedScenario report.Scenario, expected expectations) []string {
	var failures []string
	got, want := summary.Scenario, expectedScenario
	if diff := cmp.Diff(want, got); diff != "" {
		failures = append(failures, "scenario mismatch (-want,+got):\n"+diff)
	}
	if summary.ThroughputPerSecond < expected.MinThroughputPerSecond {
		failures = append(failures, fmt.Sprintf(
			"throughput %.3f/s is less than minimum %.3f/s",
			summary.ThroughputPerSecond,
			expected.MinThroughputPerSecond,
		))
	}
	if summary.Latencies.AdmissionMs.P95Ms > expected.MaxAdmissionP95Ms {
		failures = append(failures, fmt.Sprintf(
			"admission P95 %dms is greater than maximum %dms",
			summary.Latencies.AdmissionMs.P95Ms,
			expected.MaxAdmissionP95Ms,
		))
	}
	if summary.Latencies.QuotaReservationMs.P95Ms > expected.MaxQuotaReservationP95Ms {
		failures = append(failures, fmt.Sprintf(
			"quota reservation P95 %dms is greater than maximum %dms",
			summary.Latencies.QuotaReservationMs.P95Ms,
			expected.MaxQuotaReservationP95Ms,
		))
	}
	if summary.WatchGaps > expected.MaxWatchGaps {
		failures = append(failures, fmt.Sprintf(
			"the run re-established its workload watch %d times, more than the %d tolerated; "+
				"the timing and throughput measurements may be distorted by those gaps",
			summary.WatchGaps,
			expected.MaxWatchGaps,
		))
	}
	for metric, count := range map[string]int{
		"admission":         summary.Latencies.AdmissionMs.Count,
		"quota reservation": summary.Latencies.QuotaReservationMs.Count,
	} {
		if count != expectedScenario.WorkloadCount {
			failures = append(failures, fmt.Sprintf(
				"%s sample count = %d, want %d",
				metric,
				count,
				expectedScenario.WorkloadCount,
			))
		}
	}

	assigned := 0
	for i := range expectedScenario.WorkerClusters {
		name := fmt.Sprintf("worker-%d", i+1)
		count, found := summary.WorkerDistribution[name]
		if !found {
			failures = append(failures, fmt.Sprintf("worker distribution is missing %q", name))
			continue
		}
		if count == 0 {
			failures = append(failures, fmt.Sprintf("worker %q did not admit any workloads", name))
		}
		assigned += count
	}
	if assigned != expectedScenario.WorkloadCount {
		failures = append(failures, fmt.Sprintf(
			"worker assignments = %d, want %d",
			assigned,
			expectedScenario.WorkloadCount,
		))
	}
	return failures
}

func TestCheckSummary(t *testing.T) {
	expectedScenario := report.Scenario{
		RemoteClientRateLimitScope: "worker-cluster",
		WorkloadCount:              100,
		WorkerClusters:             3,
		CreationWorkers:            20,
		CPURequest:                 "1m",
		Dispatcher:                 "kueue.x-k8s.io/multikueue-dispatcher-all-at-once",
		WorkloadConcurrency:        10,
		GCInterval:                 "1m0s",
		WorkerLostTimeout:          "15m0s",
		EventsBatchPeriod:          "1s",
		LocalClientQPS:             300,
		LocalClientBurst:           500,
		RemoteClientQPS:            5,
		RemoteClientBurst:          10,
	}
	expected := expectations{
		MinThroughputPerSecond:   0.8,
		MaxAdmissionP95Ms:        150_000,
		MaxQuotaReservationP95Ms: 500,
		MaxWatchGaps:             1,
	}
	testCases := map[string]struct {
		summary report.Summary
		want    string
	}{
		"valid": {summary: makeSummary().Obj()},
		"throughput regression": {
			summary: makeSummary().Throughput(0.7).Obj(),
			want:    "throughput",
		},
		"admission regression": {
			summary: makeSummary().AdmissionP95(160_000).Obj(),
			want:    "admission P95",
		},
		"quota reservation regression": {
			summary: makeSummary().QuotaReservationP95(600).Obj(),
			want:    "quota reservation P95",
		},
		"incomplete samples": {
			summary: makeSummary().AdmissionSamples(99).Obj(),
			want:    "sample count",
		},
		"idle worker": {
			summary: makeSummary().WorkerAssignment("worker-3", 0).Obj(),
			want:    "did not admit",
		},
		"generator concurrency mismatch": {
			summary: makeSummary().CreationWorkers(10).Obj(),
			want:    "CreationWorkers",
		},
		"local rate limit mismatch": {
			summary: makeSummary().LocalClientQPS(1000).Obj(),
			want:    "LocalClientQPS",
		},
		"remote rate limit mismatch": {
			summary: makeSummary().RemoteClientQPS(300).Obj(),
			want:    "RemoteClientQPS",
		},
		"reconcile concurrency mismatch": {
			summary: makeSummary().WorkloadConcurrency(1).Obj(),
			want:    "WorkloadConcurrency",
		},
		"batch period mismatch": {
			summary: makeSummary().EventsBatchPeriod("3s").Obj(),
			want:    "EventsBatchPeriod",
		},
		"one watch gap is tolerated": {summary: makeSummary().WatchGaps(1).Obj()},
		"reject summary from before shared worker budget": {
			summary: makeSummary().RemoteClientRateLimitScope("").Obj(),
			want:    "RemoteClientRateLimitScope",
		},
		"repeated watch gaps": {
			summary: makeSummary().WatchGaps(2).Obj(),
			want:    "re-established its workload watch",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			failures := checkSummary(tc.summary, expectedScenario, expected)
			if tc.want == "" && len(failures) != 0 {
				t.Fatalf("checkSummary() failures = %v, want none", failures)
			}
			if tc.want != "" && !strings.Contains(strings.Join(failures, "\n"), tc.want) {
				t.Fatalf("checkSummary() failures = %v, want one containing %q", failures, tc.want)
			}
		})
	}
}

// The checker must compare against the configuration selected for this run rather than
// accepting a report that happens to meet the same performance thresholds.
func TestCheckSummaryWithConfigurationOverride(t *testing.T) {
	cfg := configuration.Config{
		WorkloadCount:       100,
		WorkerClusters:      3,
		CreationWorkers:     20,
		CPURequest:          "1m",
		Dispatcher:          configapi.MultiKueueDispatcherModeAllAtOnce,
		WorkloadConcurrency: 10,
		GCInterval:          metav1.Duration{Duration: time.Minute},
		WorkerLostTimeout:   metav1.Duration{Duration: 15 * time.Minute},
		EventsBatchPeriod:   metav1.Duration{Duration: time.Second},
		LocalClientQPS:      300,
		LocalClientBurst:    500,
		RemoteClientQPS:     731.5,
		RemoteClientBurst:   10,
		Timeout:             metav1.Duration{Duration: 10 * time.Minute},
	}
	expected := expectations{
		MinThroughputPerSecond:   0.8,
		MaxAdmissionP95Ms:        150_000,
		MaxQuotaReservationP95Ms: 500,
		MaxWatchGaps:             1,
	}
	summary := makeSummary().RemoteClientQPS(5).Obj()
	failures := checkSummary(summary, cfg.Scenario(), expected)
	if len(failures) != 1 || !strings.Contains(failures[0], "RemoteClientQPS") {
		t.Fatalf("checkSummary() failures = %v, want only remote QPS mismatch", failures)
	}
}

func TestDecodeBenchmarkSummary(t *testing.T) {
	valid := `
scenario:
  workloadCount: 100
  workerClusters: 3
  creationWorkers: 20
  cpuRequest: 1m
  dispatcher: all-at-once
  workloadConcurrency: 10
  gcInterval: 1m
  workerLostTimeout: 15m
  eventsBatchPeriod: 1s
  localClientQPS: 300
  localClientBurst: 500
  remoteClientRateLimitScope: worker-cluster
  remoteClientQPS: 5
  remoteClientBurst: 10
throughputPerSecond: 1.28
latencies:
  admissionMs:
    count: 100
    p95Ms: 90000
  quotaReservationMs:
    count: 100
    p95Ms: 300
watchGaps: 0
workerDistribution:
  worker-1: 60
  worker-2: 25
  worker-3: 15
`
	testCases := map[string]struct {
		summary string
		wantErr string
	}{
		"valid": {
			summary: valid,
		},
		// Fields the checker compares against an upper bound have to be rejected when absent,
		// because a zero value would otherwise satisfy the bound.
		"missing admission P95": {
			summary: strings.Replace(valid, "    p95Ms: 90000\n", "", 1),
			wantErr: "latencies.admissionMs.p95Ms is missing",
		},
		"zero admission P95": {
			summary: strings.Replace(valid, "p95Ms: 90000", "p95Ms: 0", 1),
			wantErr: "latencies.admissionMs.p95Ms is missing",
		},
		"missing throughput": {
			summary: strings.Replace(valid, "throughputPerSecond: 1.28\n", "", 1),
			wantErr: "throughputPerSecond is missing",
		},
		"missing watch gaps": {
			summary: strings.Replace(valid, "watchGaps: 0\n", "", 1),
			wantErr: "watchGaps is missing",
		},
		// Non-finite floats cannot survive the YAML-to-JSON conversion, so they never reach
		// the comparisons where NaN would silently satisfy every bound.
		"non-finite throughput": {
			summary: strings.Replace(valid, "throughputPerSecond: 1.28", "throughputPerSecond: .nan", 1),
			wantErr: "unsupported value",
		},
		// The checker decodes the runner's own type, so an unknown field means one of the two
		// has been changed without the other.
		"unknown field": {
			summary: valid + "\nadmissionsPerWorker: 33\n",
			wantErr: `unknown field "admissionsPerWorker"`,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, err := decodeBenchmarkSummary([]byte(tc.summary))
			if tc.wantErr == "" && err != nil {
				t.Fatalf("decodeBenchmarkSummary() unexpected error: %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("decodeBenchmarkSummary() error = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestCommittedExpectations(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "configs", "baseline", "expectations.yaml"))
	if err != nil {
		t.Fatalf("Read committed expectations: %v", err)
	}
	if _, err := decodeExpectations(data); err != nil {
		t.Fatalf("Decode committed expectations: %v", err)
	}
}

func TestDecodeExpectationsRejectsInvalidInput(t *testing.T) {
	const valid = `minThroughputPerSecond: 75
maxAdmissionP95Ms: 15000
maxQuotaReservationP95Ms: 1000
maxWatchGaps: 1
`
	testCases := map[string]struct {
		data    string
		wantErr string
	}{
		"missing threshold": {
			data:    "maxAdmissionP95Ms: 15000\nmaxQuotaReservationP95Ms: 1000\nmaxWatchGaps: 1\n",
			wantErr: "minThroughputPerSecond must be positive",
		},
		"zero threshold": {
			data:    "minThroughputPerSecond: 0\nmaxAdmissionP95Ms: 15000\nmaxQuotaReservationP95Ms: 1000\nmaxWatchGaps: 1\n",
			wantErr: "minThroughputPerSecond must be positive",
		},
		"configuration is not expectations": {
			data:    valid + "workloadCount: 1000\n",
			wantErr: `unknown field "workloadCount"`,
		},
		"controller configuration is not expectations": {
			data:    valid + "localClientQPS: 1000\n",
			wantErr: `unknown field "localClientQPS"`,
		},
		"unknown field": {data: valid + "unknown: true\n", wantErr: `unknown field "unknown"`},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, err := decodeExpectations([]byte(tc.data))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("decodeExpectations() error = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

type summaryWrapper struct{ summary report.Summary }

func makeSummary() summaryWrapper {
	return summaryWrapper{summary: report.Summary{
		Scenario: report.Scenario{
			RemoteClientRateLimitScope: "worker-cluster",
			WorkloadCount:              100,
			WorkerClusters:             3,
			CreationWorkers:            20,
			CPURequest:                 "1m",
			Dispatcher:                 "kueue.x-k8s.io/multikueue-dispatcher-all-at-once",
			WorkloadConcurrency:        10,
			GCInterval:                 "1m0s",
			WorkerLostTimeout:          "15m0s",
			EventsBatchPeriod:          "1s",
			LocalClientQPS:             300,
			LocalClientBurst:           500,
			RemoteClientQPS:            5,
			RemoteClientBurst:          10,
		},
		ThroughputPerSecond: 1.28,
		WorkerDistribution:  map[string]int{"worker-1": 60, "worker-2": 25, "worker-3": 15},
		Latencies: report.Latencies{
			AdmissionMs:        report.Durations{Count: 100, P95Ms: 90_000},
			QuotaReservationMs: report.Durations{Count: 100, P95Ms: 300},
		},
	}}
}

func (w summaryWrapper) Obj() report.Summary { return w.summary }

func (w summaryWrapper) Throughput(value float64) summaryWrapper {
	w.summary.ThroughputPerSecond = value
	return w
}

func (w summaryWrapper) AdmissionP95(value int64) summaryWrapper {
	w.summary.Latencies.AdmissionMs.P95Ms = value
	return w
}

func (w summaryWrapper) QuotaReservationP95(value int64) summaryWrapper {
	w.summary.Latencies.QuotaReservationMs.P95Ms = value
	return w
}

func (w summaryWrapper) AdmissionSamples(value int) summaryWrapper {
	w.summary.Latencies.AdmissionMs.Count = value
	return w
}

func (w summaryWrapper) CreationWorkers(value int) summaryWrapper {
	w.summary.Scenario.CreationWorkers = value
	return w
}

func (w summaryWrapper) LocalClientQPS(value float32) summaryWrapper {
	w.summary.Scenario.LocalClientQPS = value
	return w
}

func (w summaryWrapper) RemoteClientQPS(value float32) summaryWrapper {
	w.summary.Scenario.RemoteClientQPS = value
	return w
}

func (w summaryWrapper) WorkloadConcurrency(value int) summaryWrapper {
	w.summary.Scenario.WorkloadConcurrency = value
	return w
}

func (w summaryWrapper) EventsBatchPeriod(value string) summaryWrapper {
	w.summary.Scenario.EventsBatchPeriod = value
	return w
}

func (w summaryWrapper) WatchGaps(value int) summaryWrapper {
	w.summary.WatchGaps = value
	return w
}

func (w summaryWrapper) RemoteClientRateLimitScope(value string) summaryWrapper {
	w.summary.Scenario.RemoteClientRateLimitScope = value
	return w
}

func (w summaryWrapper) WorkerAssignment(worker string, count int) summaryWrapper {
	w.summary.WorkerDistribution = maps.Clone(w.summary.WorkerDistribution)
	w.summary.WorkerDistribution[worker] = count
	return w
}
