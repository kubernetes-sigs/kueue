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
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
)

func TestCustomLabels(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, true)
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
	features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, true)
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

func TestUpdateRequired(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, true)
	tests := map[string]struct {
		entries     []configapi.ControllerMetricsCustomLabel
		kind        configapi.SourceKind
		ref         string
		storeLabels map[string]string
		storeAnnots map[string]string
		testLabels  map[string]string
		testAnnots  map[string]string
		want        bool
		nilReceiver bool
	}{
		"nil receiver": {
			entries:     []configapi.ControllerMetricsCustomLabel{{Name: "team"}},
			kind:        configapi.SourceKindClusterQueue,
			ref:         "cq1",
			testLabels:  map[string]string{"team": "infra"},
			want:        false,
			nilReceiver: true,
		},
		"unsupported kind": {
			entries:    []configapi.ControllerMetricsCustomLabel{{Name: "team", SourceKind: ptr.To(configapi.SourceKindClusterQueue)}},
			kind:       configapi.SourceKindLocalQueue,
			ref:        "lq1",
			testLabels: map[string]string{"team": "infra"},
			want:       false,
		},
		"not stored, new values empty": {
			entries:    []configapi.ControllerMetricsCustomLabel{{Name: "team"}},
			kind:       configapi.SourceKindClusterQueue,
			ref:        "cq1",
			testLabels: map[string]string{"other": "value"},
			want:       false,
		},
		"not stored, new values not empty": {
			entries:    []configapi.ControllerMetricsCustomLabel{{Name: "team"}},
			kind:       configapi.SourceKindClusterQueue,
			ref:        "cq1",
			testLabels: map[string]string{"team": "infra"},
			want:       true,
		},
		"stored, values equal": {
			entries:     []configapi.ControllerMetricsCustomLabel{{Name: "team"}},
			kind:        configapi.SourceKindClusterQueue,
			ref:         "cq1",
			storeLabels: map[string]string{"team": "infra"},
			testLabels:  map[string]string{"team": "infra"},
			want:        false,
		},
		"stored, values not equal": {
			entries:     []configapi.ControllerMetricsCustomLabel{{Name: "team"}},
			kind:        configapi.SourceKindClusterQueue,
			ref:         "cq1",
			storeLabels: map[string]string{"team": "infra"},
			testLabels:  map[string]string{"team": "platform"},
			want:        true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var cl *CustomLabels
			if !tc.nilReceiver {
				cl = NewCustomLabels(tc.entries)
				t.Cleanup(func() {
					InitMetricVectors(nil)
				})
				if tc.storeLabels != nil || tc.storeAnnots != nil {
					cl.Store(tc.kind, tc.ref, tc.storeLabels, tc.storeAnnots)
				}
			}

			got := cl.UpdateRequired(tc.kind, tc.ref, tc.testLabels, tc.testAnnots)
			if got != tc.want {
				t.Errorf("UpdateRequired() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCustomLabelsDisabled(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, false)
	entries := []configapi.ControllerMetricsCustomLabel{{Name: "team"}}
	cl := NewCustomLabels(entries)
	if cl != nil {
		t.Error("expected nil CustomLabels when feature gate is disabled")
	}

	// Also verify that calling methods on nil receiver returns expected defaults when disabled.
	var nilCl *CustomLabels
	if got := nilCl.LabelNames(configapi.SourceKindClusterQueue); got != nil {
		t.Errorf("expected nil LabelNames, got %v", got)
	}
	if got := nilCl.UpdateRequired(configapi.SourceKindClusterQueue, "cq", nil, nil); got {
		t.Error("expected false for UpdateRequired")
	}
	if got := nilCl.Store(configapi.SourceKindClusterQueue, "cq", nil, nil); got {
		t.Error("expected false for Store")
	}
	if got := nilCl.Get(configapi.SourceKindClusterQueue, "cq"); got != nil {
		t.Errorf("expected nil Get, got %v", got)
	}
	if got := nilCl.GetFor(map[configapi.SourceKind]string{configapi.SourceKindClusterQueue: "cq"}); got != nil {
		t.Errorf("expected nil GetFor, got %v", got)
	}
	// Delete shouldn't panic
	nilCl.Delete(configapi.SourceKindClusterQueue, "cq")

	// Verify XStore, XGet, XDelete
	if got := nilCl.CQStore(kueue.ClusterQueueReference("cq"), nil, nil); got {
		t.Error("expected false for CQStore")
	}
	if got := nilCl.CQGet(kueue.ClusterQueueReference("cq")); got != nil {
		t.Errorf("expected nil CQGet, got %v", got)
	}
	nilCl.CQDelete(kueue.ClusterQueueReference("cq"))

	if got := nilCl.LQStore(utilqueue.LocalQueueReference("lq"), nil, nil); got {
		t.Error("expected false for LQStore")
	}
	if got := nilCl.LQGet(utilqueue.LocalQueueReference("lq")); got != nil {
		t.Errorf("expected nil LQGet, got %v", got)
	}
	nilCl.LQDelete(utilqueue.LocalQueueReference("lq"))

	if got := nilCl.CohortStore(kueue.CohortReference("cohort"), nil, nil); got {
		t.Error("expected false for CohortStore")
	}
	if got := nilCl.CohortGet(kueue.CohortReference("cohort")); got != nil {
		t.Errorf("expected nil CohortGet, got %v", got)
	}
	nilCl.CohortDelete(kueue.CohortReference("cohort"))
}

func TestLabelValueSetCounter(t *testing.T) {
	k1 := labelValSet{
		kind: configapi.SourceKindWorkload,
		vals: [MaxCustomLabelsForSourceKind]string{"v1", "v2"},
		size: 2,
	}
	k2 := labelValSet{
		kind: configapi.SourceKindWorkload,
		vals: [MaxCustomLabelsForSourceKind]string{"v3", "v4"},
		size: 2,
	}
	k3 := labelValSet{
		kind: configapi.SourceKindWorkload,
		vals: [MaxCustomLabelsForSourceKind]string{"v5"},
		size: 1,
	}

	// 1. Test NewLabelSetCount and Empty
	c1 := NewLabelSetCount()
	if c1.Total() != 0 {
		t.Errorf("expected empty counter total to be 0, got %d", c1.Total())
	}
	if got := c1.Get(k1); got != 0 {
		t.Errorf("expected empty counter Get to return 0, got %d", got)
	}

	emptyWl := Empty(configapi.SourceKindWorkload)
	if emptyWl.kind != configapi.SourceKindWorkload {
		t.Errorf("expected Empty(SourceKindWorkload) to have workload kind, got %s", emptyWl.kind)
	}

	// 2. Test Incr and Add
	c1.Incr(k1)
	c1.Add(k1, 2)
	c1.Incr(k2)
	if c1.Total() != 4 {
		t.Errorf("expected total to be 4, got %d", c1.Total())
	}
	if got := c1.Get(k1); got != 3 {
		t.Errorf("expected Get(k1) to be 3, got %d", got)
	}
	if got := c1.Get(k2); got != 1 {
		t.Errorf("expected Get(k2) to be 1, got %d", got)
	}
	if got := c1.Get(k3); got != 0 {
		t.Errorf("expected Get(k3) to be 0, got %d", got)
	}

	// 3. Test merge
	c2 := NewLabelSetCount()
	c2.Incr(k2)
	c2.Incr(k3)

	c1.merge(c2)
	if c1.Total() != 6 {
		t.Errorf("expected total after merge to be 6, got %d", c1.Total())
	}
	if got := c1.Get(k1); got != 3 {
		t.Errorf("expected Get(k1) to be 3, got %d", got)
	}
	if got := c1.Get(k2); got != 2 { // 1 from c1 + 1 from c2
		t.Errorf("expected Get(k2) to be 2, got %d", got)
	}
	if got := c1.Get(k3); got != 1 {
		t.Errorf("expected Get(k3) to be 1, got %d", got)
	}

	// Test merge nil
	c1.merge(nil)
	if c1.Total() != 6 {
		t.Errorf("expected total after merge(nil) to remain 6, got %d", c1.Total())
	}

	// 4. Test CombinedCounters
	a := NewLabelSetCount()
	a.Incr(k1)
	b := NewLabelSetCount()
	b.Incr(k2)
	combined := CombinedCounters(a, b)
	if combined.Total() != 2 {
		t.Errorf("expected combined total to be 2, got %d", combined.Total())
	}
	if combined.Get(k1) != 1 || combined.Get(k2) != 1 {
		t.Errorf("expected combined to have k1 and k2, got k1=%d, k2=%d", combined.Get(k1), combined.Get(k2))
	}

	// 5. Test ParallelIter
	iterResult := make(map[labelValSet][2]int)
	for ls, counts := range ParallelIter(a, b) {
		iterResult[ls] = counts
	}
	wantIterResult := map[labelValSet][2]int{
		k1: {1, 0},
		k2: {0, 1},
	}
	if diff := cmp.Diff(wantIterResult, iterResult); diff != "" {
		t.Errorf("ParallelIter mismatch (-want +got):\n%s", diff)
	}
}

