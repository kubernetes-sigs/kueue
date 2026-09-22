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

package jobframework

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/component-base/featuregate"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

func TestValidateSliceRequiredTopologyConstraintsAnnotation(t *testing.T) {
	replicaPath := field.NewPath("spec", "template", "metadata")
	annotationsPath := replicaPath.Child("annotations")
	constraintsPath := annotationsPath.Key(kueue.PodSetSliceRequiredTopologyConstraintsAnnotation)

	testCases := map[string]struct {
		featureGates map[featuregate.Feature]bool
		annotations  map[string]string
		wantErr      field.ErrorList
	}{
		"valid: single constraint layer": {
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16}]`,
			},
		},
		"valid: two constraint layers": {
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16},{"topology":"kubernetes.io/hostname","size":4}]`,
			},
		},
		"valid: three constraint layers": {
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16},{"topology":"cloud.com/sub-rack","size":4},{"topology":"kubernetes.io/hostname","size":2}]`,
			},
		},
		"invalid: not valid JSON": {
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `invalid-json`,
			},
			// invalid JSON
			wantErr: field.ErrorList{&field.Error{Type: field.ErrorTypeInvalid, Field: constraintsPath.String()}},
		},
		"invalid: empty array": {
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[]`,
			},
			// must contain at least 1 entry
			wantErr: field.ErrorList{&field.Error{Type: field.ErrorTypeInvalid, Field: constraintsPath.String()}},
		},
		"invalid: more than 3 entries": {
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"a","size":64},{"topology":"b","size":16},{"topology":"c","size":4},{"topology":"d","size":1}]`,
			},
			// more than 3 entries
			wantErr: field.ErrorList{&field.Error{Type: field.ErrorTypeInvalid, Field: constraintsPath.String()}},
		},
		"invalid: size less than 1": {
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":0}]`,
			},
			// size < 1
			wantErr: field.ErrorList{&field.Error{Type: field.ErrorTypeInvalid, Field: constraintsPath.Index(0).Child("size").String()}},
		},
		"invalid: unconstrained topology is false": {
			featureGates: map[featuregate.Feature]bool{
				features.TASRejectFalseUnconstrainedTopology: true,
			},
			annotations: map[string]string{
				kueue.PodSetUnconstrainedTopologyAnnotation: "false",
			},
			wantErr: field.ErrorList{&field.Error{Type: field.ErrorTypeInvalid, Field: annotationsPath.Key(kueue.PodSetUnconstrainedTopologyAnnotation).String()}},
		},
		"valid: unconstrained topology is false when validation is disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.TASRejectFalseUnconstrainedTopology: false,
			},
			annotations: map[string]string{
				kueue.PodSetUnconstrainedTopologyAnnotation: "false",
			},
		},
		"invalid: size does not divide parent": {
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16},{"topology":"kubernetes.io/hostname","size":5}]`,
			},
			// 16 % 5 != 0
			wantErr: field.ErrorList{&field.Error{Type: field.ErrorTypeInvalid, Field: constraintsPath.Index(1).Child("size").String()}},
		},
		"invalid: mutual exclusivity with podset-slice-required-topology": {
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyAnnotation:            "cloud.com/rack",
				kueue.PodSetSliceSizeAnnotation:                        "16",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16}]`,
			},
			// forbidden with slice-required-topology AND slice-size
			wantErr: field.ErrorList{
				&field.Error{Type: field.ErrorTypeForbidden, Field: constraintsPath.String()},
				&field.Error{Type: field.ErrorTypeForbidden, Field: constraintsPath.String()},
			},
		},
		"invalid: with podset-group-name when TASGroupedPodSetSlicing is disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.TASMultiLayerTopology:   true,
				features.TASGroupedPodSetSlicing: false,
			},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16}]`,
				kueue.PodSetGroupName:                                  "group1",
			},
			// podset-group-name forbidden with constraints when feature gate is disabled
			wantErr: field.ErrorList{&field.Error{Type: field.ErrorTypeForbidden, Field: annotationsPath.Key(kueue.PodSetGroupName).String()}},
		},
		"valid: with podset-group-name when TASGroupedPodSetSlicing is enabled": {
			featureGates: map[featuregate.Feature]bool{
				features.TASMultiLayerTopology:   true,
				features.TASGroupedPodSetSlicing: true,
			},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16}]`,
				kueue.PodSetGroupName:                                  "group1",
			},
		},
		"invalid: feature gate disabled": {
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16}]`,
			},
			// feature gate not enabled
			wantErr: field.ErrorList{&field.Error{Type: field.ErrorTypeForbidden, Field: constraintsPath.String()}},
		},
		"invalid: duplicate topology labels": {
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16},{"topology":"cloud.com/rack","size":4}]`,
			},
			wantErr: field.ErrorList{&field.Error{Type: field.ErrorTypeDuplicate, Field: constraintsPath.Index(1).Child("topology").String()}},
		},
		"invalid: duplicate topology label among three entries": {
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16},{"topology":"kubernetes.io/hostname","size":4},{"topology":"cloud.com/rack","size":2}]`,
			},
			wantErr: field.ErrorList{&field.Error{Type: field.ErrorTypeDuplicate, Field: constraintsPath.Index(2).Child("topology").String()}},
		},
		"valid: offset absent": {
			annotations: map[string]string{},
		},
		"valid: non-negative integer offset": {
			annotations: map[string]string{
				kueue.PodIndexOffsetAnnotation: "1",
			},
		},
		"invalid: unparseable offset": {
			annotations: map[string]string{
				kueue.PodIndexOffsetAnnotation: "invalid",
			},
			wantErr: field.ErrorList{&field.Error{Type: field.ErrorTypeInvalid, Field: annotationsPath.Key(kueue.PodIndexOffsetAnnotation).String()}},
		},
		"invalid: negative offset": {
			annotations: map[string]string{
				kueue.PodIndexOffsetAnnotation: "-1",
			},
			wantErr: field.ErrorList{&field.Error{Type: field.ErrorTypeInvalid, Field: annotationsPath.Key(kueue.PodIndexOffsetAnnotation).String()}},
		},
		"invalid: offset set together with podset-group-name": {
			annotations: map[string]string{
				kueue.PodSetPreferredTopologyAnnotation: "cloud.com/rack",
				kueue.PodSetGroupName:                   "group",
				kueue.PodIndexOffsetAnnotation:          "1",
			},
			// offset forbidden when podset-group-name is set
			wantErr: field.ErrorList{&field.Error{Type: field.ErrorTypeForbidden, Field: annotationsPath.Key(kueue.PodIndexOffsetAnnotation).String()}},
		},
		"invalid: unparseable offset together with podset-group-name": {
			annotations: map[string]string{
				kueue.PodSetPreferredTopologyAnnotation: "cloud.com/rack",
				kueue.PodSetGroupName:                   "group",
				kueue.PodIndexOffsetAnnotation:          "invalid",
			},
			// forbidden check short-circuits the value check
			wantErr: field.ErrorList{&field.Error{Type: field.ErrorTypeForbidden, Field: annotationsPath.Key(kueue.PodIndexOffsetAnnotation).String()}},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)

			meta := &metav1.ObjectMeta{
				Annotations: tc.annotations,
			}
			gotErr := ValidateTASPodSetRequest(replicaPath, meta)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateTASPodSetRequest_GroupingWithSlicing(t *testing.T) {
	replicaPath := field.NewPath("spec", "template", "metadata")
	annotationsPath := replicaPath.Child("annotations")

	testCases := map[string]struct {
		featureGates map[featuregate.Feature]bool
		annotations  map[string]string
		wantErr      field.ErrorList
	}{
		"valid: podset grouping with 2-level slicing and required topology": {
			featureGates: map[featuregate.Feature]bool{
				features.TASGroupedPodSetSlicing: true,
			},
			annotations: map[string]string{
				kueue.PodSetGroupName:                       "group1",
				kueue.PodSetRequiredTopologyAnnotation:      "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/rack",
				kueue.PodSetSliceSizeAnnotation:             "4",
			},
		},
		"valid: podset grouping with 2-level slicing and preferred topology": {
			featureGates: map[featuregate.Feature]bool{
				features.TASGroupedPodSetSlicing: true,
			},
			annotations: map[string]string{
				kueue.PodSetGroupName:                       "group1",
				kueue.PodSetPreferredTopologyAnnotation:     "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/rack",
				kueue.PodSetSliceSizeAnnotation:             "4",
			},
		},
		"valid: podset grouping with multi-layer slicing and required topology": {
			featureGates: map[featuregate.Feature]bool{
				features.TASMultiLayerTopology:   true,
				features.TASGroupedPodSetSlicing: true,
			},
			annotations: map[string]string{
				kueue.PodSetGroupName:                                  "group1",
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":4}]`,
			},
		},
		"invalid: podset grouping with 2-level slicing when TASGroupedPodSetSlicing is disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.TASGroupedPodSetSlicing: false,
			},
			annotations: map[string]string{
				kueue.PodSetGroupName:                       "group1",
				kueue.PodSetRequiredTopologyAnnotation:      "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/rack",
				kueue.PodSetSliceSizeAnnotation:             "4",
			},
			wantErr: field.ErrorList{
				&field.Error{Type: field.ErrorTypeForbidden, Field: annotationsPath.Key(kueue.PodSetGroupName).String()},
				&field.Error{Type: field.ErrorTypeForbidden, Field: annotationsPath.Key(kueue.PodSetGroupName).String()},
			},
		},
		"invalid: podset grouping with multi-layer slicing when TASGroupedPodSetSlicing is disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.TASMultiLayerTopology:   true,
				features.TASGroupedPodSetSlicing: false,
			},
			annotations: map[string]string{
				kueue.PodSetGroupName:                                  "group1",
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":4}]`,
			},
			wantErr: field.ErrorList{&field.Error{Type: field.ErrorTypeForbidden, Field: annotationsPath.Key(kueue.PodSetGroupName).String()}},
		},
		"invalid: podset grouping with 2-level slicing but neither required nor preferred topology": {
			featureGates: map[featuregate.Feature]bool{
				features.TASGroupedPodSetSlicing: true,
			},
			annotations: map[string]string{
				kueue.PodSetGroupName:                       "group1",
				kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/rack",
				kueue.PodSetSliceSizeAnnotation:             "4",
			},
			wantErr: field.ErrorList{&field.Error{Type: field.ErrorTypeForbidden, Field: annotationsPath.Key(kueue.PodSetGroupName).String()}},
		},
		"invalid: podset grouping with slice size missing slice required topology": {
			featureGates: map[featuregate.Feature]bool{
				features.TASGroupedPodSetSlicing: true,
			},
			annotations: map[string]string{
				kueue.PodSetGroupName:                  "group1",
				kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
				kueue.PodSetSliceSizeAnnotation:        "4",
			},
			wantErr: field.ErrorList{&field.Error{Type: field.ErrorTypeForbidden, Field: annotationsPath.Key(kueue.PodSetSliceSizeAnnotation).String()}},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			if tc.featureGates != nil {
				features.SetFeatureGatesDuringTest(t, tc.featureGates)
			}
			meta := &metav1.ObjectMeta{
				Annotations: tc.annotations,
			}
			gotErr := ValidateTASPodSetRequest(replicaPath, meta)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateSliceSizeAnnotationUpperBound(t *testing.T) {
	replicaPath := field.NewPath("spec", "template", "metadata")
	annotationsPath := replicaPath.Child("annotations")

	testCases := map[string]struct {
		featureGates  map[featuregate.Feature]bool
		annotations   map[string]string
		podSetCount   int32
		wantErr       field.ErrorList
		wantErrDetail string
	}{
		"valid: PodSetSliceSizeAnnotation within bound": {
			annotations: map[string]string{
				kueue.PodSetSliceSizeAnnotation:             "16",
				kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/rack",
				kueue.PodSetRequiredTopologyAnnotation:      "cloud.com/block",
			},
			podSetCount: 20,
		},
		"invalid: PodSetSliceSizeAnnotation exceeds pod count": {
			annotations: map[string]string{
				kueue.PodSetSliceSizeAnnotation:             "20",
				kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/rack",
				kueue.PodSetRequiredTopologyAnnotation:      "cloud.com/block",
			},
			podSetCount: 16,
			wantErr: field.ErrorList{
				&field.Error{Type: field.ErrorTypeInvalid, Field: annotationsPath.Key(kueue.PodSetSliceSizeAnnotation).String()},
			},
		},
		"valid: multi-layer outermost size within bound": {
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16},{"topology":"kubernetes.io/hostname","size":4}]`,
			},
			podSetCount: 32,
		},
		"invalid: multi-layer outermost size exceeds pod count": {
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16},{"topology":"kubernetes.io/hostname","size":4}]`,
			},
			podSetCount: 10,
			wantErr: field.ErrorList{
				&field.Error{Type: field.ErrorTypeInvalid, Field: annotationsPath.Key(kueue.PodSetSliceRequiredTopologyConstraintsAnnotation).String()},
			},
		},
		// A partial last slice is only supported for a single layer, so
		// with the feature on by default a multi-layer request has to divide evenly.
		"invalid: partial slices, multi-layer outermost size does not divide the pod count": {
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16},{"topology":"kubernetes.io/hostname","size":4}]`,
			},
			podSetCount: 20,
			wantErr: field.ErrorList{
				&field.Error{Type: field.ErrorTypeInvalid, Field: annotationsPath.Key(kueue.PodSetSliceRequiredTopologyConstraintsAnnotation).String()},
			},
			wantErrDetail: "must evenly divide pod set count 20 when more than one layer is specified",
		},
		"valid: partial slices disabled, multi-layer outermost size does not divide the pod count": {
			featureGates: map[featuregate.Feature]bool{features.TASPartialSlices: false},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16},{"topology":"kubernetes.io/hostname","size":4}]`,
			},
			podSetCount: 20,
		},
		"valid: partial slices, a single layer may leave a partial slice": {
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16}]`,
			},
			podSetCount: 20,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)

			meta := &metav1.ObjectMeta{
				Annotations: tc.annotations,
			}
			podSet := &kueue.PodSet{Count: tc.podSetCount}
			gotErr := ValidateSliceSizeAnnotationUpperBound(replicaPath, meta, podSet)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
			if tc.wantErrDetail != "" && !slices.ContainsFunc(gotErr, func(err *field.Error) bool {
				return strings.Contains(err.Detail, tc.wantErrDetail)
			}) {
				t.Errorf("ValidateSliceSizeAnnotationUpperBound() did not report %q:\n%v", tc.wantErrDetail, gotErr)
			}
		})
	}
}
