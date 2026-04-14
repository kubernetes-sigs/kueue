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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

type LocalQueueMetricsConfig struct {
	Enabled       bool
	QueueSelector labels.Selector
}

func NewLocalQueueMetricsConfig(cfg *configapi.LocalQueueMetrics) *LocalQueueMetricsConfig {
	if cfg == nil {
		return nil
	}

	lqMetricsConfig := &LocalQueueMetricsConfig{
		Enabled:       cfg.Enable,
		QueueSelector: labels.Everything(),
	}

	if !cfg.Enable {
		return lqMetricsConfig
	}

	if cfg.LocalQueueSelector != nil {
		q, err := metav1.LabelSelectorAsSelector(cfg.LocalQueueSelector)
		if err != nil {
			return nil
		}

		lqMetricsConfig.QueueSelector = q
	}

	return lqMetricsConfig
}

// IsEnabled reports whether LocalQueue metric reporting is enabled or not,
// regardless of label configuration.
func (cfg *LocalQueueMetricsConfig) IsEnabled() bool {
	return features.Enabled(features.LocalQueueMetrics) && (cfg == nil || cfg.Enabled)
}

// ShouldExposeLocalQueueMetrics determines if a specific LocalQueue should report metrics
// based on the global configuration.
func (cfg *LocalQueueMetricsConfig) ShouldExposeLocalQueueMetrics(lqLabels map[string]string) bool {
	return cfg.IsEnabled() && (cfg == nil || cfg.QueueSelector.Matches(labels.Set(lqLabels)))
}
