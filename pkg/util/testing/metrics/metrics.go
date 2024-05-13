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
	"sort"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"sigs.k8s.io/kueue/pkg/util/maps"
	"sigs.k8s.io/kueue/pkg/util/slices"
)

type MetricDataPoint struct {
	Labels map[string]string
	Value  float64
}

func (a *MetricDataPoint) Less(b *MetricDataPoint) bool {
	keys := maps.Keys(a.Labels)
	sort.Strings(keys)
	for _, k := range keys {
		vb, found := b.Labels[k]
		if !found {
			return false
		}
		va := a.Labels[k]
		if va < vb {
			return true
		}
		if vb < va {
			return false
		}
	}

	la := len(a.Labels)
	lb := len(b.Labels)
	if la < lb {
		return true
	}
	if lb < la {
		return false
	}
	return a.Value < b.Value
}

func CollectFilteredGaugeVec(v prometheus.Collector, labels map[string]string) []MetricDataPoint {
	if v == nil {
		return nil
	}

	ch := make(chan prometheus.Metric)
	ret := []MetricDataPoint{}

	go func() {
		v.Collect(ch)
		close(ch)
	}()
	for m := range ch {
		// check if matches
		dtoMetric := dto.Metric{}
		if m.Write(&dtoMetric) == nil {
			metricLabelsMap := slices.ToMap(dtoMetric.Label, func(i int) (string, string) { return *dtoMetric.Label[i].Name, *dtoMetric.Label[i].Value })
			if maps.Contains(metricLabelsMap, labels) {
				dp := MetricDataPoint{
					Labels: metricLabelsMap,
				}
				if dtoMetric.Gauge != nil {
					dp.Value = *dtoMetric.Gauge.Value
				}
				if dtoMetric.Counter != nil {
					dp.Value = *dtoMetric.Counter.Value
				}
				ret = append(ret, dp)
			}
		}
	}
	return ret
}
