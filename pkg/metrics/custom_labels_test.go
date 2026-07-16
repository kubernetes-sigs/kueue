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
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/utils/ptr"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
)

func TestCustomLabels(t *testing.T) {
	tests := map[string]struct {
		entries     []configapi.ControllerMetricsCustomLabel
		labels      map[string]string
		annotations map[string]string
		sourceKinds []configapi.SourceKind
		want        []string
		wantNames   []string
	}{
		"nil CustomLabels returns nil": {
			entries:     nil,
			labels:      map[string]string{"team": "infra"},
			sourceKinds: []configapi.SourceKind{configapi.SourceKindClusterQueue},
			want:        nil,
			wantNames:   nil,
		},
		"no entries configured": {
			entries:     []configapi.ControllerMetricsCustomLabel{},
			labels:      map[string]string{"team": "infra"},
			sourceKinds: []configapi.SourceKind{configapi.SourceKindClusterQueue},
			want:        nil,
			wantNames:   nil,
		},
		"name only (defaults to label key)": {
			entries: []configapi.ControllerMetricsCustomLabel{
				{Name: "team"},
			},
			labels:      map[string]string{"team": "infra"},
			sourceKinds: []configapi.SourceKind{configapi.SourceKindClusterQueue},
			want:        []string{"infra"},
			wantNames:   []string{"custom_team"},
		},
		"sourceLabelKey": {
			entries: []configapi.ControllerMetricsCustomLabel{
				{Name: "team", SourceLabelKey: "org/team"},
			},
			labels:      map[string]string{"org/team": "platform"},
			sourceKinds: []configapi.SourceKind{configapi.SourceKindClusterQueue},
			want:        []string{"platform"},
			wantNames:   []string{"custom_team"},
		},
		"sourceAnnotationKey": {
			entries: []configapi.ControllerMetricsCustomLabel{
				{Name: "cost_center", SourceAnnotationKey: "billing/cost-center"},
			},
			annotations: map[string]string{"billing/cost-center": "cc-123"},
			sourceKinds: []configapi.SourceKind{configapi.SourceKindClusterQueue},
			want:        []string{"cc-123"},
			wantNames:   []string{"custom_cost_center"},
		},
		"missing key returns empty string": {
			entries: []configapi.ControllerMetricsCustomLabel{
				{Name: "team"},
			},
			labels:      map[string]string{"other": "value"},
			sourceKinds: []configapi.SourceKind{configapi.SourceKindClusterQueue},
			want:        []string{""},
			wantNames:   []string{"custom_team"},
		},
		"multiple entries": {
			entries: []configapi.ControllerMetricsCustomLabel{
				{Name: "team"},
				{Name: "env", SourceLabelKey: "environment"},
				{Name: "cost", SourceAnnotationKey: "billing/cost"},
			},
			labels:      map[string]string{"team": "ml", "environment": "prod"},
			annotations: map[string]string{"billing/cost": "high"},
			sourceKinds: []configapi.SourceKind{configapi.SourceKindClusterQueue},
			want:        []string{"ml", "prod", "high"},
			wantNames:   []string{"custom_team", "custom_env", "custom_cost"},
		},
		"entries with specified source kinds": {
			entries: []configapi.ControllerMetricsCustomLabel{
				{Name: "cohort", SourceAnnotationKey: "cohort", SourceKind: ptr.To(configapi.SourceKindCohort)},
				{Name: "team", SourceKind: ptr.To(configapi.SourceKindClusterQueue)},
				{Name: "env", SourceLabelKey: "environment", SourceKind: ptr.To(configapi.SourceKindLocalQueue)},
				{Name: "cost", SourceAnnotationKey: "billing/cost", SourceKind: ptr.To(configapi.SourceKindClusterQueue)},
			},
			labels:      map[string]string{"team": "ml", "environment": "prod"},
			annotations: map[string]string{"billing/cost": "high", "cohort": "c1"},
			sourceKinds: []configapi.SourceKind{configapi.SourceKindClusterQueue, configapi.SourceKindCohort},
			want:        []string{"ml", "high", "c1"},
			wantNames:   []string{"custom_team", "custom_cost", "custom_cohort"},
		},
		"all values tracked": {
			entries: []configapi.ControllerMetricsCustomLabel{
				{Name: "team", TrackedValues: []string{"infra", "platform"}},
				{Name: "env", SourceLabelKey: "environment", TrackedValues: []string{"prod", "staging"}},
				{Name: "cost", TrackedValues: []string{}},
			},
			labels:      map[string]string{"team": "infra", "environment": "prod", "cost": "high"},
			sourceKinds: []configapi.SourceKind{configapi.SourceKindClusterQueue},
			want:        []string{"infra", "prod", "high"},
			wantNames:   []string{"custom_team", "custom_env", "custom_cost"},
		},
		"some values not tracked": {
			entries: []configapi.ControllerMetricsCustomLabel{
				{Name: "team", TrackedValues: []string{"infra", "platform"}},
				{Name: "env", SourceLabelKey: "environment", TrackedValues: []string{"prod", "staging"}},
				{Name: "cost", TrackedValues: []string{}},
			},
			labels:      map[string]string{"team": "other", "environment": "prod", "cost": "high"},
			sourceKinds: []configapi.SourceKind{configapi.SourceKindClusterQueue},
			want:        []string{"kueue.x-k8s.io/_UNTRACKED_VALUE_", "prod", "high"},
			wantNames:   []string{"custom_team", "custom_env", "custom_cost"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var cl *CustomLabels
			if tc.entries != nil {
				// Create the CustomLabels objetc. Defer cleanup.
				cl = NewCustomLabels(tc.entries)
				t.Cleanup(func() {
					InitMetricVectors(nil)
				})
			}

			sourcesMap := make(map[configapi.SourceKind]string)
			for _, kind := range tc.sourceKinds {
				sourcesMap[kind] = "object_" + string(kind)
			}

			for kind, ref := range sourcesMap {
				cl.Store(kind, ref, tc.labels, tc.annotations)
			}

			gotNames := cl.LabelNames(tc.sourceKinds...)
			if diff := cmp.Diff(tc.wantNames, gotNames); diff != "" {
				t.Errorf("CustomLabels.LabelNames() mismatch (-want +got):\n%s", diff)
			}

			gotValues := cl.GetFor(sourcesMap)
			if diff := cmp.Diff(tc.want, gotValues); diff != "" {
				t.Errorf("CustomLabels.GetFor() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestStoreCustomLabels(t *testing.T) {
	tests := map[string]struct {
		setup              func(*CustomLabels) (store func(map[string]string, map[string]string) bool, get func() []string, clear func())
		entries            []configapi.ControllerMetricsCustomLabel
		initialLabels      map[string]string
		initialAnnotations map[string]string
		wantInitial        []string
		changedLabels      map[string]string
		changedAnnotations map[string]string
		wantChanged        []string
	}{
		"CustomLabelStore": {
			setup: func(cl *CustomLabels) (func(map[string]string, map[string]string) bool, func() []string, func()) {
				return func(labels, annotations map[string]string) bool {
						return cl.Store(configapi.SourceKindClusterQueue, "cq1", labels, annotations)
					}, func() []string {
						return cl.Get(configapi.SourceKindClusterQueue, "cq1")
					}, func() {
						cl.Delete(configapi.SourceKindClusterQueue, "cq1")
					}
			},
			entries: []configapi.ControllerMetricsCustomLabel{
				{Name: "a"},
				{Name: "b"},
				{Name: "c", SourceAnnotationKey: "c"},
				{Name: "d", SourceAnnotationKey: "d"},
			},
			initialLabels: map[string]string{
				"a": "a",
				"b": "b",
			},
			initialAnnotations: map[string]string{
				"c": "c",
				"d": "d",
			},
			wantInitial: []string{"a", "b", "c", "d"},
			changedLabels: map[string]string{
				"a": "a2",
				"b": "b",
			},
			changedAnnotations: map[string]string{
				"c": "c2",
				"d": "d",
			},
			wantChanged: []string{"a2", "b", "c2", "d"},
		},
		"Multi-SourceKind": {
			setup: func(cl *CustomLabels) (func(map[string]string, map[string]string) bool, func() []string, func()) {
				return func(labels, annotations map[string]string) bool {
						cqStore := cl.Store(configapi.SourceKindClusterQueue, "cq1", labels, annotations)
						wlStore := cl.Store(configapi.SourceKindWorkload, "wl1", labels, annotations)
						return cqStore || wlStore
					}, func() []string {
						return cl.GetFor(map[configapi.SourceKind]string{
							configapi.SourceKindWorkload:     "wl1",
							configapi.SourceKindClusterQueue: "cq1",
						})
					}, func() {
						cl.Delete(configapi.SourceKindClusterQueue, "cq1")
						cl.Delete(configapi.SourceKindWorkload, "wl1")
					}
			},
			entries: []configapi.ControllerMetricsCustomLabel{
				{Name: "cq-label"},
				{Name: "wl-label", SourceKind: ptr.To(configapi.SourceKindWorkload)},
				{Name: "cq-annotation", SourceAnnotationKey: "cq-annotation"},
				{Name: "wl-annotation", SourceAnnotationKey: "wl-annotation", SourceKind: ptr.To(configapi.SourceKindWorkload)},
			},
			initialLabels: map[string]string{
				"cq-label":    "cq-label-value",
				"wl-label":    "wl-label-value",
				"other-label": "other",
			},
			initialAnnotations: map[string]string{
				"cq-annotation":    "cq-annot-value",
				"wl-annotation":    "wl-annot-value",
				"other-annotation": "other",
			},
			wantInitial: []string{"cq-label-value", "cq-annot-value", "wl-label-value", "wl-annot-value"},
			changedLabels: map[string]string{
				"cq-label":    "cq-label-value2",
				"wl-label":    "wl-label-value",
				"other-label": "other",
			},
			changedAnnotations: map[string]string{
				"cq-annotation":    "cq-annot-value",
				"wl-annotation":    "wl-annot-value2",
				"other-annotation": "other",
			},
			wantChanged: []string{"cq-label-value2", "cq-annot-value", "wl-label-value", "wl-annot-value2"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cl := NewCustomLabels(tc.entries)
			t.Cleanup(func() {
				InitMetricVectors(nil)
			})
			store, get, clear := tc.setup(cl)

			if store(tc.initialLabels, tc.initialAnnotations) {
				t.Error("expected false for first store")
			}
			if store(tc.initialLabels, tc.initialAnnotations) {
				t.Error("expected false for same values")
			}
			if diff := cmp.Diff(tc.wantInitial, get()); diff != "" {
				t.Errorf("Get() mismatch (-want +got):\n%s", diff)
			}

			if !store(tc.changedLabels, tc.changedAnnotations) {
				t.Error("expected true for changed values")
			}
			if diff := cmp.Diff(tc.wantChanged, get()); diff != "" {
				t.Errorf("Get() mismatch (-want +got):\n%s", diff)
			}

			clear()
			if got := get(); !slices.Equal(slices.Compact(got), []string{""}) {
				t.Errorf("expected empty slice after clear, got %v", got)
			}
			if store(tc.changedLabels, tc.changedAnnotations) {
				t.Error("expected false after clear+re-store")
			}
		})
	}
}
