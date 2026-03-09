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

	"github.com/google/go-cmp/cmp"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func TestExtractValues(t *testing.T) {
	tests := map[string]struct {
		entries     []configapi.ControllerMetricsCustomLabel
		labels      map[string]string
		annotations map[string]string
		want        []string
	}{
		"nil CustomLabels returns nil": {
			entries: nil,
			labels:  map[string]string{"team": "infra"},
			want:    nil,
		},
		"no entries configured": {
			entries: []configapi.ControllerMetricsCustomLabel{},
			labels:  map[string]string{"team": "infra"},
			want:    nil,
		},
		"name only (defaults to label key)": {
			entries: []configapi.ControllerMetricsCustomLabel{
				{Name: "team"},
			},
			labels: map[string]string{"team": "infra"},
			want:   []string{"infra"},
		},
		"sourceLabelKey": {
			entries: []configapi.ControllerMetricsCustomLabel{
				{Name: "team", SourceLabelKey: "org/team"},
			},
			labels: map[string]string{"org/team": "platform"},
			want:   []string{"platform"},
		},
		"sourceAnnotationKey": {
			entries: []configapi.ControllerMetricsCustomLabel{
				{Name: "cost_center", SourceAnnotationKey: "billing/cost-center"},
			},
			annotations: map[string]string{"billing/cost-center": "cc-123"},
			want:        []string{"cc-123"},
		},
		"missing key returns empty string": {
			entries: []configapi.ControllerMetricsCustomLabel{
				{Name: "team"},
			},
			labels: map[string]string{"other": "value"},
			want:   []string{""},
		},
		"multiple entries": {
			entries: []configapi.ControllerMetricsCustomLabel{
				{Name: "team"},
				{Name: "env", SourceLabelKey: "environment"},
				{Name: "cost", SourceAnnotationKey: "billing/cost"},
			},
			labels:      map[string]string{"team": "ml", "environment": "prod"},
			annotations: map[string]string{"billing/cost": "high"},
			want:        []string{"ml", "prod", "high"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var cl *CustomLabels
			if tc.entries != nil {
				cl = NewCustomLabels(tc.entries)
				t.Cleanup(func() {
					InitMetricVectors(nil)
				})
			}
			got := cl.ExtractValues(tc.labels, tc.annotations)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ExtractValues() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestStoreCustomLabels(t *testing.T) {
	tests := map[string]struct {
		setup   func(*CustomLabels) (store func([]string) bool, get func() []string, clear func())
		initial []string
		changed []string
	}{
		"CustomLabelStore": {
			setup: func(cl *CustomLabels) (func([]string) bool, func() []string, func()) {
				return func(vals []string) bool {
						return cl.cq.Store(kueue.ClusterQueueReference("cq1"), vals)
					}, func() []string {
						return cl.cq.Get(kueue.ClusterQueueReference("cq1"))
					}, func() {
						cl.cq.Delete(kueue.ClusterQueueReference("cq1"))
					}
			},
			initial: []string{"a", "b"},
			changed: []string{"a", "c"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cl := NewCustomLabels(nil)
			store, get, clear := tc.setup(cl)

			if store(tc.initial) {
				t.Error("expected false for first store")
			}
			if store(tc.initial) {
				t.Error("expected false for same values")
			}
			if !store(tc.changed) {
				t.Error("expected true for changed values")
			}
			if diff := cmp.Diff(tc.changed, get()); diff != "" {
				t.Errorf("Get() mismatch (-want +got):\n%s", diff)
			}

			clear()
			if got := get(); got != nil {
				t.Errorf("expected nil after clear, got %v", got)
			}
			if store(tc.changed) {
				t.Error("expected false after clear+re-store")
			}
		})
	}
}
