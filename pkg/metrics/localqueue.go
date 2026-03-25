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
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
)

type LocalQueueLabelStorage interface {
	GetLocalQueueLabels(cqName kueue.ClusterQueueReference, lqKey utilqueue.LocalQueueReference) (map[string]string, error)
}

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

// ShouldExposeLocalQueueMetricsForWorkload detemines if LocalQueue metric reporting should be made for the associated LocalQueue.
func (cfg *LocalQueueMetricsConfig) ShouldExposeLocalQueueMetricsForWorkload(log logr.Logger, lqLabelStorage LocalQueueLabelStorage, wl *kueue.Workload) bool {
	if !cfg.IsEnabled() {
		return false
	}
	if wl.Status.Admission == nil {
		log.V(5).Info("WARNING: cannot expose local queue metrics of workload without status.Admission", "workload", wl)
		return false
	}
	lqLabels, err := lqLabelStorage.GetLocalQueueLabels(wl.Status.Admission.ClusterQueue, utilqueue.NewLocalQueueReference(wl.Namespace, wl.Spec.QueueName))
	if err != nil {
		log.V(5).Error(err, "Failed to get LocalQueue for metrics", "localQueue", klog.KRef(wl.Namespace, string(wl.Spec.QueueName)))
		return false
	}
	return cfg.ShouldExposeLocalQueueMetrics(lqLabels)
}
