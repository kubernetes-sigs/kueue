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

package report

import (
	"math"
	"slices"
	"time"
)

type Durations struct {
	Count int   `json:"count"`
	MinMs int64 `json:"minMs"`
	AvgMs int64 `json:"avgMs"`
	P50Ms int64 `json:"p50Ms"`
	P95Ms int64 `json:"p95Ms"`
	P99Ms int64 `json:"p99Ms"`
	MaxMs int64 `json:"maxMs"`
}

// SummarizeDurations reduces observations to the percentiles the summary reports, using the
// nearest-rank method.
func SummarizeDurations(values []time.Duration) Durations {
	if len(values) == 0 {
		return Durations{}
	}

	sorted := slices.Clone(values)
	slices.Sort(sorted)

	var total time.Duration
	for _, value := range sorted {
		total += value
	}

	return Durations{
		Count: len(sorted),
		MinMs: sorted[0].Milliseconds(),
		AvgMs: (total / time.Duration(len(sorted))).Milliseconds(),
		P50Ms: nearestRank(sorted, 0.50).Milliseconds(),
		P95Ms: nearestRank(sorted, 0.95).Milliseconds(),
		P99Ms: nearestRank(sorted, 0.99).Milliseconds(),
		MaxMs: sorted[len(sorted)-1].Milliseconds(),
	}
}

func nearestRank(sorted []time.Duration, percentile float64) time.Duration {
	index := int(math.Ceil(percentile*float64(len(sorted)))) - 1
	index = max(0, min(index, len(sorted)-1))
	return sorted[index]
}
