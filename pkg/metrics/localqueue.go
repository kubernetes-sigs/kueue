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
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"sigs.k8s.io/controller-runtime/pkg/client"
	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

var DefaultLocalQueueMetricsConfig = &LocalQueueMetricsConfig{}

type LocalQueueMetricsConfig struct {
	enabled       bool
	queueSelector labels.Selector
}

func NewLocalQueueMetricsConfig(cfg *configapi.LocalQueueMetrics) *LocalQueueMetricsConfig {
	lqMetricsConfig := &LocalQueueMetricsConfig{
		enabled: cfg.Enable,
	}

	if !cfg.Enable {
		return lqMetricsConfig
	}

	q, err := metav1.LabelSelectorAsSelector(cfg.LocalQueueSelector)
	if err != nil {
		return nil
	}
	if cfg.LocalQueueSelector == nil {
		q = labels.Everything()
	}

	lqMetricsConfig.queueSelector = q

	return lqMetricsConfig
}

// ShouldExposeLocalQueueMetrics determines if a specific LocalQueue should report metrics
// based on the global configuration.
func (cfg LocalQueueMetricsConfig) ShouldExposeLocalQueueMetrics(lqLabels map[string]string) bool {
	return cfg.enabled && cfg.queueSelector.Matches(labels.Set(lqLabels))
}

// LQFromRef retrieves a LocalQueue object from the Kubernetes API based on the provided reference.
func LQFromRef(k8sClient client.Client, lqRef LocalQueueReference) (*kueue.LocalQueue, error) {
	lq := &kueue.LocalQueue{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: string(lqRef.Name), Namespace: lqRef.Namespace}, lq); err != nil {
		return nil, err // TODO: Wrap
	}

	return lq, nil
}
