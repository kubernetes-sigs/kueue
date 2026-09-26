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

package tas

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func TestValidateSpreadingAnnotation(t *testing.T) {
	fldPath := field.NewPath("spec", "template", "metadata", "annotations").
		Key(kueue.PodSetTopologySpreadingAnnotation)

	testCases := map[string]struct {
		value   string
		wantErr field.ErrorList
	}{
		"valid: single rule, omitted selector": {
			value: `{"rules":[{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"0.45"}]}`,
		},
		"valid: single rule with selector": {
			value: `{"workloadLabelSelectors":[{"key":"app","operator":"In","values":["main"]}],"rules":[{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"0.45"}]}`,
		},
		"valid: two rules with explicit enforcement modes": {
			value: `{"workloadLabelSelectors":[{"key":"app","operator":"In","values":["main"]}],"rules":[` +
				`{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"0.45","enforcementMode":"Required"},` +
				`{"topologyKey":"cloud.com/gke-tpu-partition","maxShareAllowingPlacement":"0.22","enforcementMode":"Preferred"}]}`,
		},
		// An explicitly empty array is the same request as omitting it, so it
		// is accepted the same way rather than read as "match everything".
		"valid: selectors explicitly empty": {
			value: `{"workloadLabelSelectors":[],"rules":[{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"0.45"}]}`,
		},
		"invalid: empty annotation": {
			value:   "",
			wantErr: field.ErrorList{&field.Error{Type: field.ErrorTypeInvalid, Field: fldPath.String()}},
		},
		"invalid: malformed JSON": {
			value:   `{"workloadLabelSelectors":`,
			wantErr: field.ErrorList{&field.Error{Type: field.ErrorTypeInvalid, Field: fldPath.String()}},
		},
		"invalid: empty rules": {
			value:   `{"workloadLabelSelectors":[{"key":"app","operator":"In","values":["main"]}],"rules":[]}`,
			wantErr: field.ErrorList{&field.Error{Type: field.ErrorTypeInvalid, Field: fldPath.String()}},
		},
		"invalid: too many rules": {
			value: `{"workloadLabelSelectors":[{"key":"app","operator":"In","values":["main"]}],"rules":[` +
				`{"topologyKey":"a","maxShareAllowingPlacement":"0.1"},{"topologyKey":"b","maxShareAllowingPlacement":"0.1"},{"topologyKey":"c","maxShareAllowingPlacement":"0.1"}]}`,
			wantErr: field.ErrorList{&field.Error{Type: field.ErrorTypeInvalid, Field: fldPath.String()}},
		},
		// A selector that cannot compile at all is rejected by the parse, which
		// reports once against workloadLabelSelectors and stops - the
		// per-requirement checks never run for it.
		"invalid: selector key is not a valid label name": {
			value: `{"workloadLabelSelectors":[{"key":"_bad_","operator":"In","values":["main"]}],` +
				`"rules":[{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"0.45"}]}`,
			wantErr: field.ErrorList{&field.Error{Type: field.ErrorTypeInvalid, Field: fldPath.Child("workloadLabelSelectors").String()}},
		},
		"invalid: unsupported operator and more requirements than alpha allows": {
			value: `{"workloadLabelSelectors":[` +
				`{"key":"app","operator":"In","values":["main"]},` +
				`{"key":"tier","operator":"NotIn","values":["batch"]}],` +
				`"rules":[{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"0.45"}]}`,
			wantErr: field.ErrorList{
				&field.Error{Type: field.ErrorTypeTooMany, Field: fldPath.Child("workloadLabelSelectors").String()},
				&field.Error{Type: field.ErrorTypeNotSupported, Field: fldPath.Child("workloadLabelSelectors").Index(1).Child("operator").String()},
			},
		},
		"invalid: bad topologyKey, out-of-range share, unknown enforcement mode": {
			value: `{"workloadLabelSelectors":[{"key":"app","operator":"In","values":["main"]}],` +
				`"rules":[{"topologyKey":"_bad_","maxShareAllowingPlacement":"1.5","enforcementMode":"Sometimes"}]}`,
			wantErr: field.ErrorList{
				&field.Error{Type: field.ErrorTypeInvalid, Field: fldPath.Child("rules").Index(0).Child("topologyKey").String(), Origin: "format=k8s-label-key"},
				&field.Error{Type: field.ErrorTypeInvalid, Field: fldPath.Child("rules").Index(0).Child("maxShareAllowingPlacement").String()},
				&field.Error{Type: field.ErrorTypeNotSupported, Field: fldPath.Child("rules").Index(0).Child("enforcementMode").String()},
			},
		},
		"invalid: share of exactly 1 is out of range": {
			value: `{"workloadLabelSelectors":[{"key":"app","operator":"In","values":["main"]}],` +
				`"rules":[{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"1"}]}`,
			wantErr: field.ErrorList{&field.Error{Type: field.ErrorTypeInvalid, Field: fldPath.Child("rules").Index(0).Child("maxShareAllowingPlacement").String()}},
		},
		"invalid: share of exactly 0 is out of range": {
			value: `{"workloadLabelSelectors":[{"key":"app","operator":"In","values":["main"]}],` +
				`"rules":[{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"0"}]}`,
			wantErr: field.ErrorList{&field.Error{Type: field.ErrorTypeInvalid, Field: fldPath.Child("rules").Index(0).Child("maxShareAllowingPlacement").String()}},
		},
		"invalid: duplicate rule keys": {
			value: `{"workloadLabelSelectors":[{"key":"app","operator":"In","values":["main"]}],"rules":[` +
				`{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"0.45"},` +
				`{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"0.22"}]}`,
			wantErr: field.ErrorList{&field.Error{Type: field.ErrorTypeDuplicate, Field: fldPath.Child("rules").Index(1).Child("topologyKey").String()}},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotErr := ValidateSpreadingAnnotation(fldPath, tc.value)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("ValidateSpreadingAnnotation() error mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestValidateSpreadingSelectors covers empty In values directly because
// the annotation parser rejects them before this helper runs.
func TestValidateSpreadingSelectors(t *testing.T) {
	fldPath := field.NewPath("spec", "template", "metadata", "annotations").
		Key(kueue.PodSetTopologySpreadingAnnotation).Child("workloadLabelSelectors")

	testCases := map[string]struct {
		selectors []metav1.LabelSelectorRequirement
		wantErr   field.ErrorList
	}{
		"no selectors": {},
		"one valid requirement": {
			selectors: []metav1.LabelSelectorRequirement{
				{Key: "app", Operator: metav1.LabelSelectorOpIn, Values: []string{"main"}},
			},
		},
		"more requirements than alpha accepts": {
			selectors: []metav1.LabelSelectorRequirement{
				{Key: "app", Operator: metav1.LabelSelectorOpIn, Values: []string{"main"}},
				{Key: "tier", Operator: metav1.LabelSelectorOpIn, Values: []string{"serving"}},
			},
			wantErr: field.ErrorList{
				&field.Error{Type: field.ErrorTypeTooMany, Field: fldPath.String()},
			},
		},
		"invalid label key": {
			selectors: []metav1.LabelSelectorRequirement{
				{Key: "_bad_", Operator: metav1.LabelSelectorOpIn, Values: []string{"main"}},
			},
			wantErr: field.ErrorList{
				&field.Error{Type: field.ErrorTypeInvalid, Field: fldPath.Index(0).Child("key").String(), Origin: "format=k8s-label-key"},
			},
		},
		// Only In is supported in alpha, and the values check is skipped once
		// the operator is already rejected.
		"unsupported operator": {
			selectors: []metav1.LabelSelectorRequirement{
				{Key: "app", Operator: metav1.LabelSelectorOpExists},
			},
			wantErr: field.ErrorList{
				&field.Error{Type: field.ErrorTypeNotSupported, Field: fldPath.Index(0).Child("operator").String()},
			},
		},
		"In operator with no values": {
			selectors: []metav1.LabelSelectorRequirement{
				{Key: "app", Operator: metav1.LabelSelectorOpIn},
			},
			wantErr: field.ErrorList{
				&field.Error{Type: field.ErrorTypeRequired, Field: fldPath.Index(0).Child("values").String()},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotErr := validateSpreadingSelectors(fldPath, tc.selectors)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("validateSpreadingSelectors() error mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
