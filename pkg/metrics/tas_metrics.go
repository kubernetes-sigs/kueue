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

package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

var (
	// +metricsdoc:group=tas
	// +metricsdoc:labels=flavor="the resource flavor name",domain="topology level label key (e.g. kubernetes.io/hostname)",domain_id="value of the topology level label",resource="the resource name"
	TASDomainUsage *prometheus.GaugeVec
)

// RegisterTASMetrics registers TAS GaugeVecs with the controller-runtime registry and
// records the initialization timestamp. Called from Register() only when TASNodeMetrics
// feature gate is enabled.
func RegisterTASMetrics() {
	metrics.Registry.MustRegister(TASDomainUsage)
}

// AddTASDomainUsage increments usage for every topology level in a domain path.
// levels[i] is the topology level label key; values[i] is the domain value at that level.
// Skips corev1.ResourcePods which is an internal Kueue tracking resource.
// Must be called while Cache.Lock() is held.
func AddTASDomainUsage(flavorName kueue.ResourceFlavorReference, levels, values []string, resource corev1.ResourceName, qty int64) {
	if resource == corev1.ResourcePods {
		return
	}
	for i, level := range levels {
		if i >= len(values) {
			break
		}
		TASDomainUsage.WithLabelValues(string(flavorName), level, values[i], string(resource)).Add(float64(qty))
	}
}

// SubTASDomainUsage decrements usage for every topology level in a domain path.
// Must be called while Cache.Lock() is held.
func SubTASDomainUsage(flavorName kueue.ResourceFlavorReference, levels, values []string, resource corev1.ResourceName, qty int64) {
	if resource == corev1.ResourcePods {
		return
	}
	for i, level := range levels {
		if i >= len(values) {
			break
		}
		TASDomainUsage.WithLabelValues(string(flavorName), level, values[i], string(resource)).Sub(float64(qty))
	}
}

// ClearTASFlavorUsage removes all usage metric label sets for a flavor.
// Must be called while Cache.Lock() is held.
func ClearTASFlavorUsage(flavorName kueue.ResourceFlavorReference) {
	TASDomainUsage.DeletePartialMatch(prometheus.Labels{"flavor": string(flavorName)})
}
