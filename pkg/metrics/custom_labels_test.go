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

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
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
				utiltestingapi.MakeCustomLabel("team").Obj(),
			},
			labels:      map[string]string{"team": "infra"},
			sourceKinds: []configapi.SourceKind{configapi.SourceKindClusterQueue},
			want:        []string{"infra"},
			wantNames:   []string{"custom_team"},
		},
		"sourceLabelKey": {
			entries: []configapi.ControllerMetricsCustomLabel{
				utiltestingapi.MakeCustomLabel("team").SourceLabelKey("org/team").Obj(),
			},
			labels:      map[string]string{"org/team": "platform"},
			sourceKinds: []configapi.SourceKind{configapi.SourceKindClusterQueue},
			want:        []string{"platform"},
			wantNames:   []string{"custom_team"},
		},
		"sourceAnnotationKey": {
			entries: []configapi.ControllerMetricsCustomLabel{
				utiltestingapi.MakeCustomLabel("cost_center").SourceAnnotationKey("billing/cost-center").Obj(),
			},
			annotations: map[string]string{"billing/cost-center": "cc-123"},
			sourceKinds: []configapi.SourceKind{configapi.SourceKindClusterQueue},
			want:        []string{"cc-123"},
			wantNames:   []string{"custom_cost_center"},
		},
		"missing key returns empty string": {
			entries: []configapi.ControllerMetricsCustomLabel{
				utiltestingapi.MakeCustomLabel("team").Obj(),
			},
			labels:      map[string]string{"other": "value"},
			sourceKinds: []configapi.SourceKind{configapi.SourceKindClusterQueue},
			want:        []string{""},
			wantNames:   []string{"custom_team"},
		},
		"multiple entries": {
			entries: []configapi.ControllerMetricsCustomLabel{
				utiltestingapi.MakeCustomLabel("team").Obj(),
				utiltestingapi.MakeCustomLabel("env").SourceLabelKey("environment").Obj(),
				utiltestingapi.MakeCustomLabel("cost").SourceAnnotationKey("billing/cost").Obj(),
			},
			labels:      map[string]string{"team": "ml", "environment": "prod"},
			annotations: map[string]string{"billing/cost": "high"},
			sourceKinds: []configapi.SourceKind{configapi.SourceKindClusterQueue},
			want:        []string{"ml", "prod", "high"},
			wantNames:   []string{"custom_team", "custom_env", "custom_cost"},
		},
		"entries with specified source kinds": {
			entries: []configapi.ControllerMetricsCustomLabel{
				utiltestingapi.MakeCustomLabel("cohort").SourceAnnotationKey("cohort").SourceKind(configapi.SourceKindCohort).Obj(),
				utiltestingapi.MakeCustomLabel("team").SourceKind(configapi.SourceKindClusterQueue).Obj(),
				utiltestingapi.MakeCustomLabel("env").SourceLabelKey("environment").SourceKind(configapi.SourceKindLocalQueue).Obj(),
				utiltestingapi.MakeCustomLabel("cost").SourceAnnotationKey("billing/cost").SourceKind(configapi.SourceKindClusterQueue).Obj(),
			},
			labels:      map[string]string{"team": "ml", "environment": "prod"},
			annotations: map[string]string{"billing/cost": "high", "cohort": "c1"},
			sourceKinds: []configapi.SourceKind{configapi.SourceKindClusterQueue, configapi.SourceKindCohort},
			want:        []string{"ml", "high", "c1"},
			wantNames:   []string{"custom_team", "custom_cost", "custom_cohort"},
		},
		"all values tracked": {
			entries: []configapi.ControllerMetricsCustomLabel{
				utiltestingapi.MakeCustomLabel("team").TrackedValues("infra", "platform").Obj(),
				utiltestingapi.MakeCustomLabel("env").SourceLabelKey("environment").TrackedValues("prod", "staging").Obj(),
				utiltestingapi.MakeCustomLabel("cost").Obj(),
			},
			labels:      map[string]string{"team": "infra", "environment": "prod", "cost": "high"},
			sourceKinds: []configapi.SourceKind{configapi.SourceKindClusterQueue},
			want:        []string{"infra", "prod", "high"},
			wantNames:   []string{"custom_team", "custom_env", "custom_cost"},
		},
		"some values not tracked": {
			entries: []configapi.ControllerMetricsCustomLabel{
				utiltestingapi.MakeCustomLabel("team").TrackedValues("infra", "platform").Obj(),
				utiltestingapi.MakeCustomLabel("env").SourceLabelKey("environment").TrackedValues("prod", "staging").Obj(),
				utiltestingapi.MakeCustomLabel("cost").Obj(),
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
				utiltestingapi.MakeCustomLabel("a").Obj(),
				utiltestingapi.MakeCustomLabel("b").Obj(),
				utiltestingapi.MakeCustomLabel("c").SourceAnnotationKey("c").Obj(),
				utiltestingapi.MakeCustomLabel("d").SourceAnnotationKey("d").Obj(),
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
				utiltestingapi.MakeCustomLabel("cq_label").SourceLabelKey("cq-label").Obj(),
				utiltestingapi.MakeCustomLabel("wl_label").SourceLabelKey("wl-label").SourceKind(configapi.SourceKindWorkload).TrackedValues("wl-label-value").Obj(),
				utiltestingapi.MakeCustomLabel("cq_annotation").SourceAnnotationKey("cq-annotation").Obj(),
				utiltestingapi.MakeCustomLabel("wl_annotation").SourceAnnotationKey("wl-annotation").SourceKind(configapi.SourceKindWorkload).TrackedValues("wl-annot-value", "wl-annot-value2").Obj(),
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
			entries:     []configapi.ControllerMetricsCustomLabel{utiltestingapi.MakeCustomLabel("team").Obj()},
			kind:        configapi.SourceKindClusterQueue,
			ref:         "cq1",
			testLabels:  map[string]string{"team": "infra"},
			want:        false,
			nilReceiver: true,
		},
		"unsupported kind": {
			entries:    []configapi.ControllerMetricsCustomLabel{utiltestingapi.MakeCustomLabel("team").SourceKind(configapi.SourceKindClusterQueue).Obj()},
			kind:       configapi.SourceKindLocalQueue,
			ref:        "lq1",
			testLabels: map[string]string{"team": "infra"},
			want:       false,
		},
		"not stored, new values empty": {
			entries:    []configapi.ControllerMetricsCustomLabel{utiltestingapi.MakeCustomLabel("team").Obj()},
			kind:       configapi.SourceKindClusterQueue,
			ref:        "cq1",
			testLabels: map[string]string{"other": "value"},
			want:       false,
		},
		"not stored, new values not empty": {
			entries:    []configapi.ControllerMetricsCustomLabel{utiltestingapi.MakeCustomLabel("team").Obj()},
			kind:       configapi.SourceKindClusterQueue,
			ref:        "cq1",
			testLabels: map[string]string{"team": "infra"},
			want:       true,
		},
		"stored, values equal": {
			entries:     []configapi.ControllerMetricsCustomLabel{utiltestingapi.MakeCustomLabel("team").Obj()},
			kind:        configapi.SourceKindClusterQueue,
			ref:         "cq1",
			storeLabels: map[string]string{"team": "infra"},
			testLabels:  map[string]string{"team": "infra"},
			want:        false,
		},
		"stored, values not equal": {
			entries:     []configapi.ControllerMetricsCustomLabel{utiltestingapi.MakeCustomLabel("team").Obj()},
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
	entries := []configapi.ControllerMetricsCustomLabel{utiltestingapi.MakeCustomLabel("team").Obj()}
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

func TestLabelValsTracker(t *testing.T) {
	k1 := labelValsSet{
		vals: [MaxCustomLabelsForSourceKind]string{"v1", "v2"},
		size: 2,
	}
	k2 := labelValsSet{
		vals: [MaxCustomLabelsForSourceKind]string{"v3", "v4"},
		size: 2,
	}
	k3 := labelValsSet{
		vals: [MaxCustomLabelsForSourceKind]string{"v5"},
		size: 1,
	}
	cases := map[string]struct {
		op             func(*LabelValsTracker)
		expectedCounts map[labelValsSet]int
		expectedTotal  int
	}{
		"empty tracker": {
			op:             func(t *LabelValsTracker) {},
			expectedCounts: map[labelValsSet]int{},
			expectedTotal:  0,
		},
		"increment": {
			op: func(t *LabelValsTracker) {
				for range 5 {
					t.Incr(k1)
				}
				t.Incr(k2)
			},
			expectedCounts: map[labelValsSet]int{
				k1: 5,
				k2: 1,
			},
			expectedTotal: 6,
		},
		"decrement": {
			op: func(t *LabelValsTracker) {
				t.Add(k1, 10)
				for range 5 {
					t.Decr(k1)
				}
			},
			expectedCounts: map[labelValsSet]int{
				k1: 5,
			},
			expectedTotal: 5,
		},
		"decrement to 0": {
			op: func(t *LabelValsTracker) {
				t.Add(k1, 2)
				for range 5 {
					t.Decr(k1)
				}
			},
			expectedCounts: map[labelValsSet]int{
				k1: 0,
			},
			expectedTotal: 0,
		},
		"trying to decrement 0 does not affect total": {
			op: func(t *LabelValsTracker) {
				t.Add(k1, 2)
				t.Add(k2, 5)
				for range 5 {
					t.Decr(k1)
				}
			},
			expectedCounts: map[labelValsSet]int{
				k1: 0,
				k2: 5,
			},
			expectedTotal: 5,
		},
		"add new": {
			op: func(t *LabelValsTracker) {
				t.Add(k1, 2)
				t.Add(k2, 5)
			},
			expectedCounts: map[labelValsSet]int{
				k1: 2,
				k2: 5,
			},
			expectedTotal: 7,
		},
		"add to existing": {
			op: func(t *LabelValsTracker) {
				t.Add(k1, 2)
				t.Add(k1, 5)
			},
			expectedCounts: map[labelValsSet]int{
				k1: 7,
			},
			expectedTotal: 7,
		},
		"add negative": {
			op: func(t *LabelValsTracker) {
				t.Add(k1, 5)
				t.Add(k1, -2)
			},
			expectedCounts: map[labelValsSet]int{
				k1: 3,
			},
			expectedTotal: 3,
		},
		"add negative beyond 0": {
			op: func(t *LabelValsTracker) {
				t.Add(k1, 5)
				t.Add(k1, -10)
			},
			expectedCounts: map[labelValsSet]int{
				k1: 0,
			},
			expectedTotal: 0,
		},
		"add negative beyond 0 does not affect total": {
			op: func(t *LabelValsTracker) {
				t.Add(k1, 5)
				t.Add(k2, 7)
				t.Add(k2, -10)
			},
			expectedCounts: map[labelValsSet]int{
				k1: 5,
				k2: 0,
			},
			expectedTotal: 5,
		},
		"merge": {
			op: func(t *LabelValsTracker) {
				t.Add(k1, 2)
				t.Add(k2, 7)
				other := NewLabelValsTracker()
				other.Add(k2, 5)
				other.Add(k3, 10)
				*t = *MergedTracker(t, other)
			},
			expectedCounts: map[labelValsSet]int{
				k1: 2,
				k2: 12,
				k3: 10,
			},
			expectedTotal: 24,
		},
		"merge nil": {
			op: func(t *LabelValsTracker) {
				t.Add(k1, 2)
				t.Add(k2, 7)
				*t = *MergedTracker(t, nil)
			},
			expectedCounts: map[labelValsSet]int{
				k1: 2,
				k2: 7,
			},
			expectedTotal: 9,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			lvt := NewLabelValsTracker()
			tc.op(lvt)
			if diff := cmp.Diff(tc.expectedCounts, lvt.counts); diff != "" {
				t.Errorf("unexpected counts: %s", diff)
			}
			if diff := cmp.Diff(tc.expectedTotal, lvt.Total()); diff != "" {
				t.Errorf("unexpected total: %s", diff)
			}
		})
	}
}

var allSourceKinds = []configapi.SourceKind{
	configapi.SourceKindClusterQueue,
	configapi.SourceKindLocalQueue,
	configapi.SourceKindCohort,
	configapi.SourceKindWorkload,
}

// allSourceKindEntries defines one custom label per SourceKind, so that a test
// can assert that operations on one kind leave the other kinds untouched.
func allSourceKindEntries() []configapi.ControllerMetricsCustomLabel {
	return []configapi.ControllerMetricsCustomLabel{
		utiltestingapi.MakeCustomLabel("cq_team").SourceLabelKey("team").SourceKind(configapi.SourceKindClusterQueue).Obj(),
		utiltestingapi.MakeCustomLabel("lq_team").SourceLabelKey("team").SourceKind(configapi.SourceKindLocalQueue).Obj(),
		utiltestingapi.MakeCustomLabel("cohort_team").SourceLabelKey("team").SourceKind(configapi.SourceKindCohort).Obj(),
		utiltestingapi.MakeCustomLabel("wl_kind").SourceLabelKey("kind").SourceKind(configapi.SourceKindWorkload).TrackedValues("training").Obj(),
	}
}

// TestDeleteClearsStoredLabelValues verifies that Delete drops the cached label
// values of the deleted object, and only of that object. Values sourced from
// user-controlled objects (Workloads above all) would otherwise accumulate in
// the store for the lifetime of the process.
func TestDeleteClearsStoredLabelValues(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, true)

	const (
		keptRef    = "kept"
		deletedRef = "deleted"
	)
	labels := map[string]string{"team": "platform", "kind": "training"}

	tests := map[string]struct {
		kind      configapi.SourceKind
		deleteRef string
		// wantRefs are the references still stored for kind after the delete.
		wantRefs []string
	}{
		"ClusterQueue": {
			kind:      configapi.SourceKindClusterQueue,
			deleteRef: deletedRef,
			wantRefs:  []string{keptRef},
		},
		"LocalQueue": {
			kind:      configapi.SourceKindLocalQueue,
			deleteRef: deletedRef,
			wantRefs:  []string{keptRef},
		},
		"Cohort": {
			kind:      configapi.SourceKindCohort,
			deleteRef: deletedRef,
			wantRefs:  []string{keptRef},
		},
		"Workload": {
			kind:      configapi.SourceKindWorkload,
			deleteRef: deletedRef,
			wantRefs:  []string{keptRef},
		},
		"unknown reference": {
			kind:      configapi.SourceKindWorkload,
			deleteRef: "never-stored",
			wantRefs:  []string{deletedRef, keptRef},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cl := NewCustomLabels(allSourceKindEntries())
			t.Cleanup(func() {
				InitMetricVectors(nil)
			})

			// Store the same reference names under every kind, so that a delete
			// leaking into another kind's store is visible.
			for _, kind := range allSourceKinds {
				cl.Store(kind, keptRef, labels, nil)
				cl.Store(kind, deletedRef, labels, nil)
			}

			cl.Delete(tc.kind, tc.deleteRef)

			if diff := cmp.Diff(tc.wantRefs, sortedRefs(cl, tc.kind)); diff != "" {
				t.Errorf("Unexpected stored references for %s (-want +got):\n%s", tc.kind, diff)
			}
			// A reference with no entry reads back as the zero value list, never
			// as the values it had before the delete.
			if diff := cmp.Diff([]string{""}, cl.Get(tc.kind, tc.deleteRef)); diff != "" {
				t.Errorf("Unexpected Get(%s, %s) (-want +got):\n%s", tc.kind, tc.deleteRef, diff)
			}
			for _, kind := range allSourceKinds {
				if kind == tc.kind {
					continue
				}
				if diff := cmp.Diff([]string{deletedRef, keptRef}, sortedRefs(cl, kind)); diff != "" {
					t.Errorf("Unexpected stored references for %s (-want +got):\n%s", kind, diff)
				}
			}
		})
	}
}

// TestTypedDeleteClearsStoredLabelValues covers the typed convenience wrappers,
// which the controllers call on delete events, and asserts each one addresses
// its own SourceKind store.
func TestTypedDeleteClearsStoredLabelValues(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, true)

	const ref = "obj"
	tests := map[string]struct {
		kind   configapi.SourceKind
		store  func(*CustomLabels)
		delete func(*CustomLabels)
	}{
		"CQDelete": {
			kind:   configapi.SourceKindClusterQueue,
			store:  func(cl *CustomLabels) { cl.CQStore(kueue.ClusterQueueReference(ref), nil, nil) },
			delete: func(cl *CustomLabels) { cl.CQDelete(kueue.ClusterQueueReference(ref)) },
		},
		"LQDelete": {
			kind:   configapi.SourceKindLocalQueue,
			store:  func(cl *CustomLabels) { cl.LQStore(utilqueue.LocalQueueReference(ref), nil, nil) },
			delete: func(cl *CustomLabels) { cl.LQDelete(utilqueue.LocalQueueReference(ref)) },
		},
		"CohortDelete": {
			kind:   configapi.SourceKindCohort,
			store:  func(cl *CustomLabels) { cl.CohortStore(kueue.CohortReference(ref), nil, nil) },
			delete: func(cl *CustomLabels) { cl.CohortDelete(kueue.CohortReference(ref)) },
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cl := NewCustomLabels(allSourceKindEntries())
			t.Cleanup(func() {
				InitMetricVectors(nil)
			})

			// Every other kind is seeded directly; tc.kind is seeded through the
			// typed wrapper, so a wrapper addressing the wrong store shows up as
			// either a surviving entry under tc.kind or a missing one elsewhere.
			for _, kind := range allSourceKinds {
				if kind != tc.kind {
					cl.Store(kind, ref, nil, nil)
				}
			}
			tc.store(cl)
			if diff := cmp.Diff([]string{ref}, sortedRefs(cl, tc.kind)); diff != "" {
				t.Fatalf("Unexpected stored references for %s after the typed store (-want +got):\n%s", tc.kind, diff)
			}

			tc.delete(cl)

			if got := sortedRefs(cl, tc.kind); len(got) != 0 {
				t.Errorf("Expected no stored references for %s, got %v", tc.kind, got)
			}
			for _, kind := range allSourceKinds {
				if kind == tc.kind {
					continue
				}
				if diff := cmp.Diff([]string{ref}, sortedRefs(cl, kind)); diff != "" {
					t.Errorf("Unexpected stored references for %s (-want +got):\n%s", kind, diff)
				}
			}
		})
	}
}

// TestDeleteResetsUpdateRequired verifies that a reference re-added after a
// delete is treated as new: the stale values must not survive the delete and
// suppress the re-report of its metrics.
func TestDeleteResetsUpdateRequired(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, true)

	labels := map[string]string{"team": "platform"}
	cl := NewCustomLabels(allSourceKindEntries())
	t.Cleanup(func() {
		InitMetricVectors(nil)
	})

	cl.CQStore("cq", labels, nil)
	if cl.UpdateRequired(configapi.SourceKindClusterQueue, "cq", labels, nil) {
		t.Error("UpdateRequired() = true for unchanged values, want false")
	}

	cl.CQDelete("cq")

	if !cl.UpdateRequired(configapi.SourceKindClusterQueue, "cq", labels, nil) {
		t.Error("UpdateRequired() = false after delete, want true")
	}
	if cl.CQStore("cq", labels, nil) {
		t.Error("CQStore() = true after delete, want false: the entry should be recorded as new")
	}
}

func sortedRefs(cl *CustomLabels, kind configapi.SourceKind) []string {
	refs := cl.StoredRefsForTest(kind)
	slices.Sort(refs)
	return refs
}

// TestLabelValsTrackerPopZeroCounts covers the mechanism that drops workload
// label value combinations once the last workload carrying them is gone. It
// both bounds the tracker's size and drives the deletion of the corresponding
// pending-workload series.
func TestLabelValsTrackerPopZeroCounts(t *testing.T) {
	k1 := labelValsSet{vals: [MaxCustomLabelsForSourceKind]string{"v1"}, size: 1}
	k2 := labelValsSet{vals: [MaxCustomLabelsForSourceKind]string{"v2"}, size: 1}
	k3 := labelValsSet{vals: [MaxCustomLabelsForSourceKind]string{"v3"}, size: 1}

	cases := map[string]struct {
		op             func(*LabelValsTracker)
		expectedPopped []labelValsSet
		expectedCounts map[labelValsSet]int
		expectedTotal  int
	}{
		"nothing to pop": {
			op: func(tracker *LabelValsTracker) {
				tracker.Incr(k1)
			},
			expectedPopped: nil,
			expectedCounts: map[labelValsSet]int{k1: 1},
			expectedTotal:  1,
		},
		"pops the sets drained to zero": {
			op: func(tracker *LabelValsTracker) {
				tracker.Incr(k1)
				tracker.Incr(k2)
				tracker.Incr(k3)
				tracker.Decr(k1)
				tracker.Decr(k3)
			},
			expectedPopped: []labelValsSet{k1, k3},
			expectedCounts: map[labelValsSet]int{k2: 1},
			expectedTotal:  1,
		},
		"pops sets left at zero by an over-decrement": {
			op: func(tracker *LabelValsTracker) {
				tracker.Incr(k1)
				tracker.Add(k1, -5)
			},
			expectedPopped: []labelValsSet{k1},
			expectedCounts: map[labelValsSet]int{},
			expectedTotal:  0,
		},
		"a popped set is not popped twice": {
			op: func(tracker *LabelValsTracker) {
				tracker.Incr(k1)
				tracker.Decr(k1)
				for range tracker.PopZeroCounts() {
				}
			},
			expectedPopped: nil,
			expectedCounts: map[labelValsSet]int{},
			expectedTotal:  0,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tracker := NewLabelValsTracker()
			tc.op(tracker)

			var popped []labelValsSet
			for ls := range tracker.PopZeroCounts() {
				popped = append(popped, *ls)
			}
			slices.SortFunc(popped, func(a, b labelValsSet) int {
				return slices.Compare(a.OrderedList(), b.OrderedList())
			})

			if diff := cmp.Diff(tc.expectedPopped, popped, cmp.AllowUnexported(labelValsSet{})); diff != "" {
				t.Errorf("unexpected popped sets (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.expectedCounts, tracker.counts); diff != "" {
				t.Errorf("unexpected counts (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.expectedTotal, tracker.Total()); diff != "" {
				t.Errorf("unexpected total (-want +got):\n%s", diff)
			}
		})
	}
}
