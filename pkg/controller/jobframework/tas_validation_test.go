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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/component-base/featuregate"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

func TestValidateSliceRequiredTopologyConstraintsAnnotation(t *testing.T) {
	replicaPath := field.NewPath("spec", "template", "metadata")

	testCases := map[string]struct {
		featureGates map[featuregate.Feature]bool
		annotations  map[string]string
		wantErrNum   int
	}{
		"valid: single constraint layer": {
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16}]`,
			},
			wantErrNum: 0,
		},
		"valid: two constraint layers": {
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16},{"topology":"kubernetes.io/hostname","size":4}]`,
			},
			wantErrNum: 0,
		},
		"valid: three constraint layers": {
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16},{"topology":"cloud.com/sub-rack","size":4},{"topology":"kubernetes.io/hostname","size":2}]`,
			},
			wantErrNum: 0,
		},
		"invalid: not valid JSON": {
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `invalid-json`,
			},
			wantErrNum: 1, // invalid JSON
		},
		"invalid: empty array": {
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[]`,
			},
			wantErrNum: 1, // must contain at least 1 entry
		},
		"invalid: more than 3 entries": {
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"a","size":64},{"topology":"b","size":16},{"topology":"c","size":4},{"topology":"d","size":1}]`,
			},
			wantErrNum: 1, // more than 3 entries
		},
		"invalid: size less than 1": {
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":0}]`,
			},
			wantErrNum: 1, // size < 1
		},
		"invalid: unconstrained topology is false": {
			featureGates: map[featuregate.Feature]bool{
				features.TASRejectFalseUnconstrainedTopology: true,
			},
			annotations: map[string]string{
				kueue.PodSetUnconstrainedTopologyAnnotation: "false",
			},
			wantErrNum: 1,
		},
		"valid: unconstrained topology is false when validation is disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.TASRejectFalseUnconstrainedTopology: false,
			},
			annotations: map[string]string{
				kueue.PodSetUnconstrainedTopologyAnnotation: "false",
			},
			wantErrNum: 0,
		},
		"invalid: size does not divide parent": {
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16},{"topology":"kubernetes.io/hostname","size":5}]`,
			},
			wantErrNum: 1, // 16 % 5 != 0
		},
		"invalid: mutual exclusivity with podset-slice-required-topology": {
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyAnnotation:            "cloud.com/rack",
				kueue.PodSetSliceSizeAnnotation:                        "16",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16}]`,
			},
			wantErrNum: 2, // forbidden with slice-required-topology AND slice-size
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
			wantErrNum: 1, // podset-group-name forbidden with constraints when feature gate is disabled
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
			wantErrNum: 0,
		},
		"invalid: feature gate disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.TASMultiLayerTopology: false, // explicitly disable since it's now Beta (enabled by default)
			},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16}]`,
			},
			wantErrNum: 1, // feature gate not enabled
		},
		"invalid: duplicate topology labels": {
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16},{"topology":"cloud.com/rack","size":4}]`,
			},
			wantErrNum: 1,
		},
		"invalid: duplicate topology label among three entries": {
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":16},{"topology":"kubernetes.io/hostname","size":4},{"topology":"cloud.com/rack","size":2}]`,
			},
			wantErrNum: 1,
		},
		"valid: offset absent": {
			annotations: map[string]string{},
			wantErrNum:  0,
		},
		"valid: non-negative integer offset": {
			annotations: map[string]string{
				kueue.PodIndexOffsetAnnotation: "1",
			},
			wantErrNum: 0,
		},
		"invalid: unparseable offset": {
			annotations: map[string]string{
				kueue.PodIndexOffsetAnnotation: "invalid",
			},
			wantErrNum: 1,
		},
		"invalid: negative offset": {
			annotations: map[string]string{
				kueue.PodIndexOffsetAnnotation: "-1",
			},
			wantErrNum: 1,
		},
		"invalid: offset set together with podset-group-name": {
			annotations: map[string]string{
				kueue.PodSetPreferredTopologyAnnotation: "cloud.com/rack",
				kueue.PodSetGroupName:                   "group",
				kueue.PodIndexOffsetAnnotation:          "1",
			},
			wantErrNum: 1, // offset forbidden when podset-group-name is set
		},
		"invalid: unparseable offset together with podset-group-name": {
			annotations: map[string]string{
				kueue.PodSetPreferredTopologyAnnotation: "cloud.com/rack",
				kueue.PodSetGroupName:                   "group",
				kueue.PodIndexOffsetAnnotation:          "invalid",
			},
			wantErrNum: 1, // forbidden check short-circuits the value check
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)

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

func TestValidateTASPodSetRequest_GroupingWithSlicing(t *testing.T) {
	replicaPath := field.NewPath("spec", "template", "metadata")

	testCases := map[string]struct {
		featureGates map[featuregate.Feature]bool
		annotations  map[string]string
		wantErrNum   int
	}{
		"valid: podset grouping with 2-level slicing and required topology": {
			annotations: map[string]string{
				kueue.PodSetGroupName:                       "group1",
				kueue.PodSetRequiredTopologyAnnotation:      "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/rack",
				kueue.PodSetSliceSizeAnnotation:             "4",
			},
			wantErrNum: 0,
		},
		"valid: podset grouping with 2-level slicing and preferred topology": {
			annotations: map[string]string{
				kueue.PodSetGroupName:                       "group1",
				kueue.PodSetPreferredTopologyAnnotation:     "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/rack",
				kueue.PodSetSliceSizeAnnotation:             "4",
			},
			wantErrNum: 0,
		},
		"valid: podset grouping with multi-layer slicing and required topology": {
			annotations: map[string]string{
				kueue.PodSetGroupName:                                  "group1",
				kueue.PodSetRequiredTopologyAnnotation:                 "cloud.com/block",
				kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: `[{"topology":"cloud.com/rack","size":4}]`,
			},
			wantErrNum: 0,
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
			wantErrNum: 2,
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
			wantErrNum: 1,
		},
		"invalid: podset grouping with 2-level slicing but neither required nor preferred topology": {
			annotations: map[string]string{
				kueue.PodSetGroupName:                       "group1",
				kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/rack",
				kueue.PodSetSliceSizeAnnotation:             "4",
			},
			wantErrNum: 1,
		},
		"invalid: podset grouping with slice size missing slice required topology": {
			annotations: map[string]string{
				kueue.PodSetGroupName:                  "group1",
				kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
				kueue.PodSetSliceSizeAnnotation:        "4",
			},
			wantErrNum: 1,
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
			errs := ValidateTASPodSetRequest(replicaPath, meta)
			if got := len(errs); got != tc.wantErrNum {
				t.Errorf("ValidateTASPodSetRequest() returned %d errors, want %d:\n%v", got, tc.wantErrNum, errs)
			}
		})
	}
}

func TestValidateTopologySpreadingAnnotation(t *testing.T) {
	replicaPath := field.NewPath("spec", "template", "metadata")

	testCases := map[string]struct {
		featureGates map[featuregate.Feature]bool
		annotations  map[string]string
		wantErrNum   int
	}{
		"valid: single rule with required companion": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:  "cloud.com/block",
				kueue.PodSetTopologySpreadingAnnotation: `{"workloadLabelSelectors":[{"key":"app","operator":"In","values":["main"]}],"rules":[{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"0.45"}]}`,
			},
			wantErrNum: 0,
		},
		"valid: two rules with required companion": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
				kueue.PodSetTopologySpreadingAnnotation: `{"workloadLabelSelectors":[{"key":"app","operator":"In","values":["main"]}],"rules":[` +
					`{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"0.45","enforcementMode":"Required"},` +
					`{"topologyKey":"cloud.com/gke-tpu-partition","maxShareAllowingPlacement":"0.22","enforcementMode":"Preferred"}]}`,
			},
			wantErrNum: 0,
		},
		// The annotation is inert while the gate is off, so it is accepted
		// unvalidated to let operators stage it before enabling the feature.
		"valid: gate off, annotation left unvalidated": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: false},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:  "cloud.com/block",
				kueue.PodSetTopologySpreadingAnnotation: `{"workloadLabelSelectors":[{"key":"app","operator":"In","values":["main"]}],"rules":[{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"0.45"}]}`,
			},
			wantErrNum: 0,
		},
		"valid: gate off, malformed annotation left unvalidated": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: false},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:  "cloud.com/block",
				kueue.PodSetTopologySpreadingAnnotation: `not-json`,
			},
			wantErrNum: 0,
		},
		"valid: gate off, annotation without required companion left unvalidated": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: false},
			annotations: map[string]string{
				kueue.PodSetTopologySpreadingAnnotation: `{"workloadLabelSelectors":[{"key":"app","operator":"In","values":["main"]}],"rules":[{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"0.45"}]}`,
			},
			wantErrNum: 0,
		},
		"invalid: no companion TAS annotation": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			annotations: map[string]string{
				kueue.PodSetTopologySpreadingAnnotation: `{"workloadLabelSelectors":[{"key":"app","operator":"In","values":["main"]}],"rules":[{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"0.45"}]}`,
			},
			wantErrNum: 1,
		},
		"invalid: preferred companion": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			annotations: map[string]string{
				kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block",
				kueue.PodSetTopologySpreadingAnnotation: `{"workloadLabelSelectors":[{"key":"app","operator":"In","values":["main"]}],"rules":[{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"0.45"}]}`,
			},
			wantErrNum: 1,
		},
		"invalid: unconstrained companion": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			annotations: map[string]string{
				kueue.PodSetUnconstrainedTopologyAnnotation: "true",
				kueue.PodSetTopologySpreadingAnnotation:     `{"workloadLabelSelectors":[{"key":"app","operator":"In","values":["main"]}],"rules":[{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"0.45"}]}`,
			},
			wantErrNum: 1,
		},
		"invalid: malformed JSON": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:  "cloud.com/block",
				kueue.PodSetTopologySpreadingAnnotation: `{"workloadLabelSelectors":`,
			},
			wantErrNum: 1,
		},
		"invalid: empty rules": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:  "cloud.com/block",
				kueue.PodSetTopologySpreadingAnnotation: `{"workloadLabelSelectors":[{"key":"app","operator":"In","values":["main"]}],"rules":[]}`,
			},
			wantErrNum: 1,
		},
		"invalid: too many rules": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
				kueue.PodSetTopologySpreadingAnnotation: `{"workloadLabelSelectors":[{"key":"app","operator":"In","values":["main"]}],"rules":[` +
					`{"topologyKey":"a","maxShareAllowingPlacement":"0.1"},{"topologyKey":"b","maxShareAllowingPlacement":"0.1"},{"topologyKey":"c","maxShareAllowingPlacement":"0.1"}]}`,
			},
			wantErrNum: 1,
		},
		// Omitting the selector is how the user asks for the default: it
		// resolves to the parent job's UID when the Workload's spreading
		// configuration is built, which is after this validation runs.
		"valid: selectors omitted, resolved later from the job UID": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation:  "cloud.com/block",
				kueue.PodSetTopologySpreadingAnnotation: `{"rules":[{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"0.45"}]}`,
			},
			wantErrNum: 0,
		},
		// An explicitly empty array is the same request as omitting it, so it
		// is accepted the same way rather than read as "match everything".
		"valid: selectors explicitly empty": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
				kueue.PodSetTopologySpreadingAnnotation: `{"workloadLabelSelectors":[],` +
					`"rules":[{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"0.45"}]}`,
			},
			wantErrNum: 0,
		},
		// A selector that cannot compile at all is rejected by the parse, which
		// reports once against workloadLabelSelectors and stops - the
		// per-requirement checks never run for it.
		"invalid: selector key is not a valid label name": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
				kueue.PodSetTopologySpreadingAnnotation: `{"workloadLabelSelectors":[{"key":"_bad_","operator":"In","values":["main"]}],` +
					`"rules":[{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"0.45"}]}`,
			},
			wantErrNum: 1,
		},
		// These compile fine, so they reach the alpha-restriction checks and
		// each gets its own indexed path.
		"invalid: unsupported operator and more requirements than alpha allows": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
				kueue.PodSetTopologySpreadingAnnotation: `{"workloadLabelSelectors":[` +
					`{"key":"app","operator":"In","values":["main"]},` +
					`{"key":"tier","operator":"NotIn","values":["batch"]}],` +
					`"rules":[{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"0.45"}]}`,
			},
			// too many requirements + unsupported operator = 2
			wantErrNum: 2,
		},
		"invalid: bad topologyKey, out-of-range share, unknown enforcement mode": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
				kueue.PodSetTopologySpreadingAnnotation: `{"workloadLabelSelectors":[{"key":"app","operator":"In","values":["main"]}],` +
					`"rules":[{"topologyKey":"_bad_","maxShareAllowingPlacement":"1.5","enforcementMode":"Sometimes"}]}`,
			},
			wantErrNum: 3,
		},
		"invalid: share of exactly 1 is out of range": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
				kueue.PodSetTopologySpreadingAnnotation: `{"workloadLabelSelectors":[{"key":"app","operator":"In","values":["main"]}],` +
					`"rules":[{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"1"}]}`,
			},
			wantErrNum: 1,
		},
		"invalid: share of exactly 0 is out of range": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
				kueue.PodSetTopologySpreadingAnnotation: `{"workloadLabelSelectors":[{"key":"app","operator":"In","values":["main"]}],` +
					`"rules":[{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"0"}]}`,
			},
			wantErrNum: 1,
		},
		"invalid: duplicate rule keys": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			annotations: map[string]string{
				kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
				kueue.PodSetTopologySpreadingAnnotation: `{"workloadLabelSelectors":[{"key":"app","operator":"In","values":["main"]}],"rules":[` +
					`{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"0.45"},` +
					`{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"0.22"}]}`,
			},
			wantErrNum: 1,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)

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

// TestValidatePodSetGroupingTopologySpreadingConsistency covers the
// requirement that the two PodSets of a group agree on the topology-spreading
// annotation. Spreading counts a group as one unit, so two members asking for
// different rules would have no single answer.
func TestValidatePodSetGroupingTopologySpreadingConsistency(t *testing.T) {
	const (
		groupName  = "group1"
		spreading  = `{"rules":[{"topologyKey":"cloud.com/rack","maxShareAllowingPlacement":"0.45"}]}`
		spreading2 = `{"rules":[{"topologyKey":"cloud.com/block","maxShareAllowingPlacement":"0.45"}]}`
	)
	requiredTopology := "cloud.com/block"

	podSet := func(name kueue.PodSetReference, count int32, spreadingAnnotation string) kueue.PodSet {
		annotations := map[string]string{}
		if spreadingAnnotation != "" {
			annotations[kueue.PodSetTopologySpreadingAnnotation] = spreadingAnnotation
		}
		return kueue.PodSet{
			Name:  name,
			Count: count,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Annotations: annotations},
			},
			TopologyRequest: &kueue.PodSetTopologyRequest{
				Required:        &requiredTopology,
				PodSetGroupName: ptr.To(groupName),
			},
		}
	}

	testCases := map[string]struct {
		gateOff    bool
		leader     string
		workers    string
		wantErrNum int
	}{
		"both PodSets carry the same annotation": {
			leader:  spreading,
			workers: spreading,
		},
		"neither PodSet carries the annotation": {},
		// One error per PodSet, so the user sees both field paths.
		"only the leader carries the annotation": {
			leader:     spreading,
			wantErrNum: 2,
		},
		"only the workers carry the annotation": {
			workers:    spreading,
			wantErrNum: 2,
		},
		"the PodSets carry different annotations": {
			leader:     spreading,
			workers:    spreading2,
			wantErrNum: 2,
		},
		// Inert while the gate is off, so operators can stage the annotation
		// ahead of enabling the feature.
		"gate off, only the leader carries the annotation": {
			gateOff: true,
			leader:  spreading,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TASTopologySpreading, !tc.gateOff)

			podSets := []kueue.PodSet{
				podSet("leader", 1, tc.leader),
				podSet("workers", 3, tc.workers),
			}
			paths := map[kueue.PodSetReference]*field.Path{
				"leader":  field.NewPath("spec", "leaderTemplate", "metadata", "annotations"),
				"workers": field.NewPath("spec", "workerTemplate", "metadata", "annotations"),
			}

			gotErrs := ValidatePodSetGroupingTopology(podSets, paths)
			if len(gotErrs) != tc.wantErrNum {
				t.Errorf("ValidatePodSetGroupingTopology() returned %d errors, want %d: %v",
					len(gotErrs), tc.wantErrNum, gotErrs)
			}
		})
	}
}

// TestValidateSpreadingSelectors covers the requirement-shape rules directly.
// The empty-values case is unreachable through
// validateTopologySpreadingAnnotation, because ParseSpreadingAnnotation fails
// to compile such a selector and returns first, so it is only observable here.
func TestValidateSpreadingSelectors(t *testing.T) {
	fldPath := field.NewPath("spec", "template", "metadata", "annotations").
		Key(kueue.PodSetTopologySpreadingAnnotation).Child("workloadLabelSelectors")

	testCases := map[string]struct {
		selectors  []metav1.LabelSelectorRequirement
		wantErrNum int
	}{
		"no selectors": {},
		"one valid requirement": {
			selectors: []metav1.LabelSelectorRequirement{
				{Key: "app", Operator: metav1.LabelSelectorOpIn, Values: []string{"main"}},
			},
		},
		// Alpha accepts a single requirement only.
		"more requirements than alpha accepts": {
			selectors: []metav1.LabelSelectorRequirement{
				{Key: "app", Operator: metav1.LabelSelectorOpIn, Values: []string{"main"}},
				{Key: "tier", Operator: metav1.LabelSelectorOpIn, Values: []string{"serving"}},
			},
			wantErrNum: 1,
		},
		"invalid label key": {
			selectors: []metav1.LabelSelectorRequirement{
				{Key: "_bad_", Operator: metav1.LabelSelectorOpIn, Values: []string{"main"}},
			},
			wantErrNum: 1,
		},
		// Only In is supported in alpha, and the values check is skipped once
		// the operator is already rejected.
		"unsupported operator": {
			selectors: []metav1.LabelSelectorRequirement{
				{Key: "app", Operator: metav1.LabelSelectorOpExists},
			},
			wantErrNum: 1,
		},
		"In operator with no values": {
			selectors: []metav1.LabelSelectorRequirement{
				{Key: "app", Operator: metav1.LabelSelectorOpIn},
			},
			wantErrNum: 1,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotErrs := validateSpreadingSelectors(fldPath, tc.selectors)
			if len(gotErrs) != tc.wantErrNum {
				t.Errorf("validateSpreadingSelectors() returned %d errors, want %d: %v",
					len(gotErrs), tc.wantErrNum, gotErrs)
			}
		})
	}
}
