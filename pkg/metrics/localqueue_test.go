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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

func TestNewLocalQueueMetricsConfig(t *testing.T) {
	testCases := map[string]struct {
		cfg     *configapi.LocalQueueMetrics
		labels  map[string]string
		want    bool
		wantErr bool
	}{
		"no configuration exposes all queues": {
			labels: map[string]string{"env": "dev"},
			want:   true,
		},
		"enabled without selector exposes all queues": {
			cfg:    &configapi.LocalQueueMetrics{Enable: true},
			labels: map[string]string{"env": "dev"},
			want:   true,
		},
		"valid selector filters queues": {
			cfg: &configapi.LocalQueueMetrics{
				Enable:             true,
				LocalQueueSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"env": "prod"}},
			},
			labels: map[string]string{"env": "dev"},
		},
		"disabled metrics ignore selector": {
			cfg: &configapi.LocalQueueMetrics{
				LocalQueueSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{Key: "env", Operator: metav1.LabelSelectorOperator("InvalidOp")},
					},
				},
			},
			labels: map[string]string{"env": "prod"},
		},
		"invalid selector reports error": {
			cfg: &configapi.LocalQueueMetrics{
				Enable: true,
				LocalQueueSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{Key: "env", Operator: metav1.LabelSelectorOperator("InvalidOp")},
					},
				},
			},
			wantErr: true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.LocalQueueMetrics, true)
			cfg, err := NewLocalQueueMetricsConfig(tc.cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("NewLocalQueueMetricsConfig() error = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil {
				if got := cfg.ShouldExposeLocalQueueMetrics(tc.labels); got != tc.want {
					t.Errorf("ShouldExposeLocalQueueMetrics() = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestShouldExposeLocalQueueMetrics(t *testing.T) {
	testCases := map[string]struct {
		cfg      *LocalQueueMetricsConfig
		lqLabels map[string]string
		want     bool
	}{
		"should not expose metrics; disabled": {
			cfg: &LocalQueueMetricsConfig{
				Enabled:       false,
				QueueSelector: labels.SelectorFromSet(map[string]string{"env": "prod"}),
			},
			lqLabels: map[string]string{"env": "prod"},
			want:     false,
		},
		"should expose metrics; enabled with no selector": {
			cfg: &LocalQueueMetricsConfig{
				Enabled:       true,
				QueueSelector: labels.Everything(),
			},
			lqLabels: map[string]string{"env": "prod"},
			want:     true,
		},
		"should expose metrics; enabled with matching selector": {
			cfg: &LocalQueueMetricsConfig{
				Enabled:       true,
				QueueSelector: labels.SelectorFromSet(map[string]string{"env": "prod"}),
			},
			lqLabels: map[string]string{"env": "prod"},
			want:     true,
		},
		"should not expose metrics; enabled with selector that does not match": {
			cfg: &LocalQueueMetricsConfig{
				Enabled:       true,
				QueueSelector: labels.SelectorFromSet(map[string]string{"env": "prod"}),
			},
			lqLabels: map[string]string{"env": "dev"},
			want:     false,
		},
	}

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.LocalQueueMetrics, true)
			if got := testCase.cfg.ShouldExposeLocalQueueMetrics(testCase.lqLabels); got != testCase.want {
				t.Errorf("ShouldExposeLocalQueueMetrics() = %v, want %v", got, testCase.want)
			}
		})
	}
}
