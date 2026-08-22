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
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"sigs.k8s.io/yaml"

	"sigs.k8s.io/kueue/test/performance/multikueue/report"
)

var (
	summaryFile = flag.String("summary", "", "the MultiKueue benchmark summary")
	rangeFile   = flag.String("range", "", "the expected performance range")
)

// rangeSpec is the committed expectation a run is compared against. It embeds the runner's own
// Scenario so that a new dimension of the benchmark cannot be added without the range spec
// gaining the field too, and so the exact-match comparison stays exhaustive.
type rangeSpec struct {
	report.Scenario          `json:",inline"`
	MinThroughputPerSecond   float64 `json:"minThroughputPerSecond"`
	MaxAdmissionP95Ms        int64   `json:"maxAdmissionP95Ms"`
	MaxQuotaReservationP95Ms int64   `json:"maxQuotaReservationP95Ms"`
	// MaxWatchGaps tolerates the occasional lost watch while the separately checked latency and
	// throughput metrics remain in range, and fails a run that lost the event stream repeatedly.
	MaxWatchGaps int `json:"maxWatchGaps"`
}

func TestPerformance(t *testing.T) {
	if *summaryFile == "" && *rangeFile == "" {
		t.Skip("summary and range flags are only supplied by the performance target")
	}
	if *summaryFile == "" || *rangeFile == "" {
		t.Fatal("both --summary and --range are required")
	}

	summaryBytes, err := os.ReadFile(*summaryFile)
	if err != nil {
		t.Fatalf("Read summary: %v", err)
	}
	summary, err := decodeBenchmarkSummary(summaryBytes)
	if err != nil {
		t.Fatalf("Decode summary: %v", err)
	}

	rangeBytes, err := os.ReadFile(*rangeFile)
	if err != nil {
		t.Fatalf("Read range: %v", err)
	}
	expected, err := decodeRangeSpec(rangeBytes)
	if err != nil {
		t.Fatalf("Decode range: %v", err)
	}

	for _, failure := range checkSummary(summary, expected) {
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

func decodeRangeSpec(data []byte) (rangeSpec, error) {
	var expected rangeSpec
	if err := yaml.UnmarshalStrict(data, &expected); err != nil {
		return rangeSpec{}, err
	}
	if err := expected.validate(); err != nil {
		return rangeSpec{}, err
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

func (r rangeSpec) validate() error {
	switch {
	case r.WorkloadCount <= 0:
		return errors.New("workloadCount must be positive")
	case r.WorkerClusters <= 0:
		return errors.New("workerClusters must be positive")
	case r.CreationWorkers <= 0:
		return errors.New("creationWorkers must be positive")
	case r.CPURequest == "":
		return errors.New("cpuRequest must not be empty")
	case r.Dispatcher == "":
		return errors.New("dispatcher must not be empty")
	case r.WorkloadConcurrency <= 0:
		return errors.New("workloadConcurrency must be positive")
	case !validPositiveDuration(r.GCInterval):
		return errors.New("gcInterval must be a positive duration")
	case !validPositiveDuration(r.WorkerLostTimeout):
		return errors.New("workerLostTimeout must be a positive duration")
	case !validPositiveDuration(r.EventsBatchPeriod):
		return errors.New("eventsBatchPeriod must be a positive duration")
	case r.LocalClientQPS <= 0:
		return errors.New("localClientQPS must be positive")
	case r.LocalClientBurst <= 0:
		return errors.New("localClientBurst must be positive")
	case r.RemoteClientQPS <= 0:
		return errors.New("remoteClientQPS must be positive")
	case r.RemoteClientBurst <= 0:
		return errors.New("remoteClientBurst must be positive")
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

func validPositiveDuration(value string) bool {
	duration, err := time.ParseDuration(value)
	return err == nil && duration > 0
}

func checkSummary(summary report.Summary, expected rangeSpec) []string {
	var failures []string
	got, want := summary.Scenario, expected.Scenario
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
		if count != expected.WorkloadCount {
			failures = append(failures, fmt.Sprintf(
				"%s sample count = %d, want %d",
				metric,
				count,
				expected.WorkloadCount,
			))
		}
	}

	assigned := 0
	for i := range expected.WorkerClusters {
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
	if assigned != expected.WorkloadCount {
		failures = append(failures, fmt.Sprintf(
			"worker assignments = %d, want %d",
			assigned,
			expected.WorkloadCount,
		))
	}
	return failures
}

func TestCheckSummary(t *testing.T) {
	validScenario := report.Scenario{
		WorkloadCount:       100,
		WorkerClusters:      3,
		CreationWorkers:     20,
		CPURequest:          "1m",
		Dispatcher:          "all-at-once",
		WorkloadConcurrency: 10,
		GCInterval:          "1m",
		WorkerLostTimeout:   "15m",
		EventsBatchPeriod:   "1s",
		LocalClientQPS:      300,
		LocalClientBurst:    500,
		RemoteClientQPS:     5,
		RemoteClientBurst:   10,
	}
	validSummary := report.Summary{
		Scenario:            validScenario,
		ThroughputPerSecond: 1.28,
		WorkerDistribution: map[string]int{
			"worker-1": 60,
			"worker-2": 25,
			"worker-3": 15,
		},
	}
	validSummary.Latencies.AdmissionMs = report.Durations{Count: 100, P95Ms: 90_000}
	validSummary.Latencies.QuotaReservationMs = report.Durations{Count: 100, P95Ms: 300}

	validRange := rangeSpec{
		Scenario:                 validScenario,
		MinThroughputPerSecond:   0.8,
		MaxAdmissionP95Ms:        150_000,
		MaxQuotaReservationP95Ms: 500,
		MaxWatchGaps:             1,
	}

	testCases := map[string]struct {
		mutate func(*report.Summary)
		want   string
	}{
		"valid": {},
		"throughput regression": {
			mutate: func(summary *report.Summary) {
				summary.ThroughputPerSecond = 0.7
			},
			want: "throughput",
		},
		"admission regression": {
			mutate: func(summary *report.Summary) {
				summary.Latencies.AdmissionMs.P95Ms = 160_000
			},
			want: "admission P95",
		},
		"quota reservation regression": {
			mutate: func(summary *report.Summary) {
				summary.Latencies.QuotaReservationMs.P95Ms = 600
			},
			want: "quota reservation P95",
		},
		"incomplete samples": {
			mutate: func(summary *report.Summary) {
				summary.Latencies.AdmissionMs.Count = 99
			},
			want: "sample count",
		},
		"idle worker": {
			mutate: func(summary *report.Summary) {
				summary.WorkerDistribution["worker-3"] = 0
			},
			want: "did not admit",
		},
		"scenario mismatch": {
			mutate: func(summary *report.Summary) {
				summary.Scenario.CreationWorkers = 10
			},
			want: "CreationWorkers",
		},
		"remote rate limit mismatch": {
			mutate: func(summary *report.Summary) {
				summary.Scenario.RemoteClientQPS = 300
			},
			want: "RemoteClientQPS",
		},
		"reconcile concurrency mismatch": {
			mutate: func(summary *report.Summary) {
				summary.Scenario.WorkloadConcurrency = 1
			},
			want: "WorkloadConcurrency",
		},
		"one watch gap is tolerated": {
			mutate: func(summary *report.Summary) {
				summary.WatchGaps = 1
			},
		},
		"repeated watch gaps": {
			mutate: func(summary *report.Summary) {
				summary.WatchGaps = 2
			},
			want: "re-established its workload watch",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			summary := validSummary
			summary.WorkerDistribution = make(map[string]int, len(validSummary.WorkerDistribution))
			maps.Copy(summary.WorkerDistribution, validSummary.WorkerDistribution)
			if tc.mutate != nil {
				tc.mutate(&summary)
			}

			failures := checkSummary(summary, validRange)
			if tc.want == "" && len(failures) != 0 {
				t.Fatalf("checkSummary() failures = %v, want none", failures)
			}
			if tc.want != "" && !strings.Contains(strings.Join(failures, "\n"), tc.want) {
				t.Fatalf("checkSummary() failures = %v, want one containing %q", failures, tc.want)
			}
		})
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

func TestCommittedRangeSpec(t *testing.T) {
	data, err := os.ReadFile(committedRangePath())
	if err != nil {
		t.Fatalf("Read committed range: %v", err)
	}
	if _, err := decodeRangeSpec(data); err != nil {
		t.Fatalf("Decode committed range: %v", err)
	}
}

func TestDecodeRangeSpecRejectsInvalidInput(t *testing.T) {
	data, err := os.ReadFile(committedRangePath())
	if err != nil {
		t.Fatalf("Read committed range: %v", err)
	}

	testCases := map[string]struct {
		rangeData string
		wantErr   string
	}{
		"missing threshold": {
			rangeData: rewriteRangeField(t, string(data), "minThroughputPerSecond", ""),
			wantErr:   "minThroughputPerSecond must be positive",
		},
		"zero threshold": {
			rangeData: rewriteRangeField(t, string(data), "minThroughputPerSecond", "minThroughputPerSecond: 0\n"),
			wantErr:   "minThroughputPerSecond must be positive",
		},
		"unknown field": {
			rangeData: string(data) + "\nunknown: true\n",
			wantErr:   `unknown field "unknown"`,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, err := decodeRangeSpec([]byte(tc.rangeData))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("decodeRangeSpec() error = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

func committedRangePath() string {
	return filepath.Join("..", "configs", "baseline", "rangespec.yaml")
}

// rewriteRangeField replaces the line defining a top-level range field, or drops it when
// replacement is empty. It keys on the field name rather than its committed value so that
// recalibrating a threshold cannot silently turn a mutation into a no-op.
func rewriteRangeField(t *testing.T, data, field, replacement string) string {
	t.Helper()
	line := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(field) + `:.*\n`)
	if !line.MatchString(data) {
		t.Fatalf("Field %q not found in committed range", field)
	}
	return line.ReplaceAllString(data, replacement)
}
