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

package testing

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/onsi/gomega/types"
)

func TestMetricsMatcher(t *testing.T) {
	const (
		metricsPage = `# HELP kueue_evicted_workloads_total The number of evicted workloads
# TYPE kueue_evicted_workloads_total counter
kueue_evicted_workloads_total{cluster_queue="cq",reason="Preempted"} 1
kueue_preempted_workloads_total{preempting_cluster_queue="cq",reason="InClusterQueue"} 1
kueue_local_queue_resource_usage{local_queue="lq",namespace="ns",resource="cpu"} 1
`

		// unauthorizedBody is what the metrics endpoint returns while the metrics-reader
		// binding is not yet effective. Callers of ExcludeMetrics must not be handed such a
		// body: it satisfies the matcher without proving anything about the metrics.
		unauthorizedBody = `{"kind":"Status","message":"Unauthorized","code":401}`
	)

	testCases := map[string]struct {
		newMatcher     func([][]string) types.GomegaMatcher
		metrics        [][]string
		actual         any
		want           bool
		wantErr        string
		wantFailureMsg string
	}{
		"contains all expected metrics": {
			newMatcher: ContainMetrics,
			metrics:    [][]string{{"kueue_evicted_workloads_total"}, {"kueue_preempted_workloads_total"}},
			actual:     metricsPage,
			want:       true,
		},
		"contains a metric narrowed by its label values": {
			newMatcher: ContainMetrics,
			metrics:    [][]string{{"kueue_local_queue_resource_usage", "ns", "lq"}},
			actual:     metricsPage,
			want:       true,
		},
		"contains fails when a label value does not match": {
			newMatcher: ContainMetrics,
			metrics:    [][]string{{"kueue_local_queue_resource_usage", "other-ns"}},
			actual:     metricsPage,
			want:       false,
			wantFailureMsg: `Expected to contain metric:
    <[]string | len:2, cap:2>: [
        "kueue_local_queue_resource_usage",
        "other-ns",
    ]

Actual:
    <string>: kueue_evicted_workloads_total{cluster_queue="cq",reason="Preempted"} 1
    kueue_preempted_workloads_total{preempting_cluster_queue="cq",reason="InClusterQueue"} 1
    kueue_local_queue_resource_usage{local_queue="lq",namespace="ns",resource="cpu"} 1`,
		},
		"excludes succeeds once the metrics are gone": {
			newMatcher: ExcludeMetrics,
			metrics:    [][]string{{"kueue_evicted_workloads_total"}},
			actual:     "kueue_pending_workloads{cluster_queue=\"cq\"} 0\n",
			want:       true,
		},
		"excludes fails while the metric is still reported": {
			newMatcher: ExcludeMetrics,
			metrics:    [][]string{{"kueue_evicted_workloads_total"}},
			actual:     metricsPage,
			want:       false,
			wantFailureMsg: `Expected not to contain metric:
    <[]string | len:1, cap:1>: [
        "kueue_evicted_workloads_total",
    ]

Actual:
    <string>: kueue_evicted_workloads_total{cluster_queue="cq",reason="Preempted"} 1
    kueue_preempted_workloads_total{preempting_cluster_queue="cq",reason="InClusterQueue"} 1
    kueue_local_queue_resource_usage{local_queue="lq",namespace="ns",resource="cpu"} 1`,
		},
		// Guards the reason util.GetKueueMetrics passes --fail to curl: without it an
		// HTTP error is reported as a successful fetch, and this body then satisfies
		// ExcludeMetrics, so ExpectMetricsNotToBeAvailable would pass for the wrong reason.
		"excludes matches vacuously against an error body": {
			newMatcher: ExcludeMetrics,
			metrics:    [][]string{{"kueue_evicted_workloads_total"}},
			actual:     unauthorizedBody,
			want:       true,
		},
		"non-string input": {
			newMatcher: ContainMetrics,
			metrics:    [][]string{{"kueue_evicted_workloads_total"}},
			actual:     42,
			want:       false,
			wantErr:    "Metrics matcher expects a string. Got:\n    <int>: 42",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			matcher := tc.newMatcher(tc.metrics)
			got, gotErr := matcher.Match(tc.actual)

			var gotErrStr string
			if gotErr != nil {
				gotErrStr = gotErr.Error()
			}

			if diff := cmp.Diff(tc.wantErr, gotErrStr); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Unexpected result (-want,+got):\n%s", diff)
			}

			if !got && gotErr == nil {
				if diff := cmp.Diff(tc.wantFailureMsg, matcher.FailureMessage(tc.actual)); diff != "" {
					t.Errorf("Unexpected failure message (-want,+got):\n%s", diff)
				}
			}
		})
	}
}
