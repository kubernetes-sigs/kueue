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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

func TestValidateSliceRequiredTopologyConstraintsAnnotation(t *testing.T) {
	replicaPath := field.NewPath("spec", "template", "metadata")

	testCases := map[string]struct {
		enableTASMultiLayer bool
		annotations         map[string]string
		wantErrNum          int
	}{
		"valid: single constraint layer": {
			enableTASMultiLayer: true,
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16}]`,
			},
			wantErrNum: 0,
		},
		"valid: two constraint layers": {
			enableTASMultiLayer: true,
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16},{"topology":"kubernetes.io/hostname","size":4}]`,
			},
			wantErrNum: 0,
		},
		"valid: three constraint layers": {
			enableTASMultiLayer: true,
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16},{"topology":"cloud.com/sub-rack","size":4},{"topology":"kubernetes.io/hostname","size":2}]`,
			},
			wantErrNum: 0,
		},
		"invalid: not valid JSON": {
			enableTASMultiLayer: true,
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `invalid-json`,
			},
			wantErrNum: 1, // invalid JSON
		},
		"invalid: empty array": {
			enableTASMultiLayer: true,
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[]`,
			},
			wantErrNum: 1, // must contain at least 1 entry
		},
		"invalid: more than 3 entries": {
			enableTASMultiLayer: true,
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"a","size":64},{"topology":"b","size":16},{"topology":"c","size":4},{"topology":"d","size":1}]`,
			},
			wantErrNum: 1, // more than 3 entries
		},
		"invalid: size less than 1": {
			enableTASMultiLayer: true,
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":0}]`,
			},
			wantErrNum: 1, // size < 1
		},
		"invalid: size does not divide parent": {
			enableTASMultiLayer: true,
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16},{"topology":"kubernetes.io/hostname","size":5}]`,
			},
			wantErrNum: 1, // 16 % 5 != 0
		},
		"invalid: mutual exclusivity with podset-slice-required-topology": {
			enableTASMultiLayer: true,
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyAnnotation:            "cloud.com/rack",
				kueue.PodSetSliceSizeAnnotation:                        "16",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16}]`,
			},
			wantErrNum: 2, // forbidden with slice-required-topology AND slice-size
		},
		"invalid: with podset-group-name": {
			enableTASMultiLayer: true,
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16}]`,
				kueue.PodSetGroupName:                                  "group1",
			},
			wantErrNum: 1, // podset-group-name forbidden with constraints
		},
		"invalid: feature gate disabled": {
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16}]`,
			},
			wantErrNum: 1, // feature gate not enabled
		},
		"invalid: duplicate topology labels": {
			enableTASMultiLayer: true,
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16},{"topology":"cloud.com/rack","size":4}]`,
			},
			wantErrNum: 1,
		},
		"invalid: duplicate topology label among three entries": {
			enableTASMultiLayer: true,
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16},{"topology":"kubernetes.io/hostname","size":4},{"topology":"cloud.com/rack","size":2}]`,
			},
			wantErrNum: 1,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TASMultiLayerTopology, tc.enableTASMultiLayer)

			meta := &metav1.ObjectMeta{
				Annotations: tc.annotations,
			}
			errs := ValidateTASPodSetRequest(replicaPath, meta)
			if got := len(errs); got != tc.wantErrNum {
				t.Errorf("ValidateTASPodSetRequest() returned %d errors, want %d:\n%v", got, tc.wantErrNum, errs)
			}
		})
	}
}

func TestValidateSliceSizeAnnotationUpperBound(t *testing.T) {
	replicaPath := field.NewPath("spec", "template", "metadata")

	testCases := map[string]struct {
		annotations map[string]string
		podSetCount int32
		wantErrNum  int
	}{
		"valid: PodSetSliceSizeAnnotation within bound": {
			annotations: map[string]string{
				kueue.PodSetSliceSizeAnnotation:             "16",
				kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/rack",
				kueue.PodSetRequiredTopologyAnnotation:      "cloud.com/block",
			},
			podSetCount: 20,
			wantErrNum:  0,
		},
		"invalid: PodSetSliceSizeAnnotation exceeds pod count": {
			annotations: map[string]string{
				kueue.PodSetSliceSizeAnnotation:             "20",
				kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/rack",
				kueue.PodSetRequiredTopologyAnnotation:      "cloud.com/block",
			},
			podSetCount: 16,
			wantErrNum:  1,
		},
		"valid: multi-layer outermost size within bound": {
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16},{"topology":"kubernetes.io/hostname","size":4}]`,
			},
			podSetCount: 20,
			wantErrNum:  0,
		},
		"invalid: multi-layer outermost size exceeds pod count": {
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16},{"topology":"kubernetes.io/hostname","size":4}]`,
			},
			podSetCount: 10,
			wantErrNum:  1,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			meta := &metav1.ObjectMeta{
				Annotations: tc.annotations,
			}
			podSet := &kueue.PodSet{Count: tc.podSetCount}
			errs := ValidateSliceSizeAnnotationUpperBound(replicaPath, meta, podSet)
			if got := len(errs); got != tc.wantErrNum {
				t.Errorf("ValidateSliceSizeAnnotationUpperBound() returned %d errors, want %d:\n%v", got, tc.wantErrNum, errs)
			}
		})
	}
}
