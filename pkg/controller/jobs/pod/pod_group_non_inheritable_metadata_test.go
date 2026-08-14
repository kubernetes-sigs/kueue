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

package pod

import (
	"maps"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

// A pod group copies its members' metadata after NewWorkload has run, so the
// guard there never sees it. The group also compares the members before it
// settles on one set, so a key neither of them may pass on has to be gone by
// then: two Pods disagreeing about one must not read as two different groups.
func TestConstructComposableWorkloadDropsNonInheritableMetadata(t *testing.T) {
	cases := map[string]struct {
		gate            bool
		labels          []map[string]string
		annotations     []map[string]string
		labelKeys       []string
		annotationKeys  []string
		wantLabels      map[string]string
		wantNoLabels    []string
		wantAnnotations map[string]string
		wantNoAnnots    []string
	}{
		"the origin label is dropped when the pods agree on it": {
			labels: []map[string]string{
				{"team": "alpha", kueue.MultiKueueOriginLabel: "multikueue"},
				{"team": "alpha", kueue.MultiKueueOriginLabel: "multikueue"},
			},
			labelKeys:    []string{"team", kueue.MultiKueueOriginLabel},
			wantLabels:   map[string]string{"team": "alpha"},
			wantNoLabels: []string{kueue.MultiKueueOriginLabel},
		},
		"the origin label is dropped when the pods disagree on it": {
			labels: []map[string]string{
				{"team": "alpha", kueue.MultiKueueOriginLabel: "multikueue"},
				{"team": "alpha", kueue.MultiKueueOriginLabel: "other"},
			},
			labelKeys:    []string{"team", kueue.MultiKueueOriginLabel},
			wantLabels:   map[string]string{"team": "alpha"},
			wantNoLabels: []string{kueue.MultiKueueOriginLabel},
		},
		"non-inheritable annotations are dropped and the group marker is rewritten": {
			gate: true,
			annotations: []map[string]string{
				{
					"team": "alpha",
					controllerconstants.JobOwnerNameAnnotation:     "from-driver",
					controllerconstants.PriorityBoostAnnotationKey: "from-driver",
					kueue.WorkloadSliceNameAnnotation:              "from-driver",
					podconstants.IsGroupWorkloadAnnotationKey:      "from-driver",
				},
				{
					"team": "alpha",
					controllerconstants.JobOwnerNameAnnotation:     "from-worker",
					controllerconstants.PriorityBoostAnnotationKey: "from-worker",
					kueue.WorkloadSliceNameAnnotation:              "from-worker",
					podconstants.IsGroupWorkloadAnnotationKey:      "from-worker",
				},
			},
			annotationKeys: []string{
				"team",
				controllerconstants.JobOwnerNameAnnotation,
				controllerconstants.PriorityBoostAnnotationKey,
				kueue.WorkloadSliceNameAnnotation,
				podconstants.IsGroupWorkloadAnnotationKey,
			},
			wantAnnotations: map[string]string{
				"team": "alpha",
				// Written back after the filtering, so the group still says what it is.
				podconstants.IsGroupWorkloadAnnotationKey: podconstants.IsGroupWorkloadAnnotationValue,
			},
			wantNoAnnots: []string{
				controllerconstants.JobOwnerNameAnnotation,
				controllerconstants.PriorityBoostAnnotationKey,
				kueue.WorkloadSliceNameAnnotation,
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if tc.gate {
				features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, true)
			}
			ctx, _ := utiltesting.ContextWithLog(t)

			pods := make([]corev1.Pod, 2)
			for i, podName := range []string{"driver", "worker"} {
				p := testingpod.MakePod(podName, "test-ns").GroupNameLabel("test-group").GroupTotalCount("2").Obj()
				if tc.labels != nil {
					maps.Copy(p.Labels, tc.labels[i])
				}
				if tc.annotations != nil {
					maps.Copy(p.Annotations, tc.annotations[i])
				}
				pods[i] = *p
			}
			labelKeys, annotationKeys := sets.New(tc.labelKeys...), sets.New(tc.annotationKeys...)
			p := &Pod{pod: pods[0], isGroup: true, list: corev1.PodList{Items: pods}}
			cl := utiltesting.NewClientBuilder().WithObjects(&pods[0], &pods[1]).Build()

			wl, err := p.ConstructComposableWorkload(ctx, cl, &utiltesting.EventRecorder{}, labelKeys, annotationKeys)
			if err != nil {
				t.Fatalf("ConstructComposableWorkload() error = %v", err)
			}

			for key, want := range tc.wantLabels {
				if got := wl.Labels[key]; got != want {
					t.Errorf("label %s = %q, want %q", key, got, want)
				}
			}
			for _, key := range tc.wantNoLabels {
				if got, found := wl.Labels[key]; found {
					t.Errorf("Workload carries label %s from a Pod: %q", key, got)
				}
			}
			for key, want := range tc.wantAnnotations {
				if got := wl.Annotations[key]; got != want {
					t.Errorf("annotation %s = %q, want %q", key, got, want)
				}
			}
			for _, key := range tc.wantNoAnnots {
				if got, found := wl.Annotations[key]; found {
					t.Errorf("Workload carries annotation %s from a Pod: %q", key, got)
				}
			}
			for _, key := range append(tc.labelKeys, tc.annotationKeys...) {
				if !labelKeys.Union(annotationKeys).Has(key) {
					t.Errorf("the caller's key set lost %s", key)
				}
			}
		})
	}
}
