/*
Copyright 2023 The Kubernetes Authors.

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
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/prometheus/client_golang/prometheus"
)

func getTestGaugeVec() *prometheus.GaugeVec {
	ret := prometheus.NewGaugeVec(prometheus.GaugeOpts{}, []string{"l1", "l2"})
	ret.With(prometheus.Labels{"l1": "l1v1", "l2": "l2v1"}).Set(1)
	ret.With(prometheus.Labels{"l1": "l1v1", "l2": "l2v2"}).Set(2)
	ret.With(prometheus.Labels{"l1": "l1v1", "l2": "l2v3"}).Set(3)
	ret.With(prometheus.Labels{"l1": "l1v2", "l2": "l2v1"}).Set(4)
	ret.With(prometheus.Labels{"l1": "l1v2", "l2": "l2v2"}).Set(5)
	return ret
}

func getTestCounterVec() *prometheus.CounterVec {
	ret := prometheus.NewCounterVec(prometheus.CounterOpts{}, []string{"c1", "c2"})
	ret.With(prometheus.Labels{"c1": "l1v1", "c2": "l2v1"}).Inc()
	ret.With(prometheus.Labels{"c1": "l1v1", "c2": "l2v2"}).Inc()
	ret.With(prometheus.Labels{"c1": "l1v1", "c2": "l2v3"}).Inc()
	ret.With(prometheus.Labels{"c1": "l1v2", "c2": "l2v1"}).Inc()
	ret.With(prometheus.Labels{"c1": "l1v2", "c2": "l2v2"}).Inc()
	return ret
}

func TestCollect(t *testing.T) {
	cases := map[string]struct {
		vec    prometheus.Collector
		labels map[string]string
		want   []MetricDataPoint
	}{
		"nil": {
			vec:    nil,
			labels: nil,
			want:   nil,
		},
		"empty": {
			vec:    prometheus.NewGaugeVec(prometheus.GaugeOpts{}, nil),
			labels: nil,
			want:   []MetricDataPoint{},
		},
		"filter l1": {
			vec:    getTestGaugeVec(),
			labels: map[string]string{"l1": "l1v1"},
			want: []MetricDataPoint{
				{Labels: map[string]string{"l1": "l1v1", "l2": "l2v1"}, Value: 1},
				{Labels: map[string]string{"l1": "l1v1", "l2": "l2v2"}, Value: 2},
				{Labels: map[string]string{"l1": "l1v1", "l2": "l2v3"}, Value: 3},
			},
		},
		"filter l2": {
			vec:    getTestGaugeVec(),
			labels: map[string]string{"l2": "l2v1"},
			want: []MetricDataPoint{
				{Labels: map[string]string{"l1": "l1v1", "l2": "l2v1"}, Value: 1},
				{Labels: map[string]string{"l1": "l1v2", "l2": "l2v1"}, Value: 4},
			},
		},
		"filter both": {
			vec:    getTestGaugeVec(),
			labels: map[string]string{"l1": "l1v2", "l2": "l2v1"},
			want: []MetricDataPoint{
				{Labels: map[string]string{"l1": "l1v2", "l2": "l2v1"}, Value: 4},
			},
		},
		"filter no match": {
			vec:    getTestGaugeVec(),
			labels: map[string]string{"l3": "l3v1"},
			want:   []MetricDataPoint{},
		},
		"empty filter": {
			vec:    getTestGaugeVec(),
			labels: nil,
			want: []MetricDataPoint{
				{Labels: map[string]string{"l1": "l1v2", "l2": "l2v1"}, Value: 4},
				{Labels: map[string]string{"l1": "l1v2", "l2": "l2v2"}, Value: 5},
				{Labels: map[string]string{"l1": "l1v1", "l2": "l2v1"}, Value: 1},
				{Labels: map[string]string{"l1": "l1v1", "l2": "l2v2"}, Value: 2},
				{Labels: map[string]string{"l1": "l1v1", "l2": "l2v3"}, Value: 3},
			},
		},
		"empty filter for counter metrics": {
			vec:    getTestCounterVec(),
			labels: nil,
			want: []MetricDataPoint{
				{Labels: map[string]string{"c1": "l1v1", "c2": "l2v1"}, Value: 1},
				{Labels: map[string]string{"c1": "l1v1", "c2": "l2v2"}, Value: 1},
				{Labels: map[string]string{"c1": "l1v1", "c2": "l2v3"}, Value: 1},
				{Labels: map[string]string{"c1": "l1v2", "c2": "l2v1"}, Value: 1},
				{Labels: map[string]string{"c1": "l1v2", "c2": "l2v2"}, Value: 1},
			},
		},
		"filter c1 for counter metrics": {
			vec:    getTestCounterVec(),
			labels: map[string]string{"c1": "l1v1"},
			want: []MetricDataPoint{
				{Labels: map[string]string{"c1": "l1v1", "c2": "l2v1"}, Value: 1},
				{Labels: map[string]string{"c1": "l1v1", "c2": "l2v2"}, Value: 1},
				{Labels: map[string]string{"c1": "l1v1", "c2": "l2v3"}, Value: 1},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := CollectFilteredGaugeVec(tc.vec, tc.labels)
			if diff := cmp.Diff(tc.want, got, cmpopts.SortSlices(func(a, b MetricDataPoint) bool { return a.Less(&b) })); len(diff) != 0 {
				t.Errorf("Unexpected data points (-want,+got):\n%s", diff)
			}
		})
	}
}
