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
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func TestParseSpreadingAnnotation(t *testing.T) {
	// The selector is orthogonal to what most cases exercise, so they share one.
	const selectorJSON = `{"workloadLabelSelectors":[{"key":"app","operator":"In","values":["main"]}]`
	wantSelectors := []metav1.LabelSelectorRequirement{
		{Key: "app", Operator: metav1.LabelSelectorOpIn, Values: []string{"main"}},
	}

	testCases := map[string]struct {
		value      string
		wantSpec   *SpreadingSpec
		wantErr    error
		wantErrNil bool
	}{
		"valid: single rule, enforcement mode defaulted": {
			value: selectorJSON + `,"rules":[{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"0.45"}]}`,
			wantSpec: &SpreadingSpec{
				WorkloadLabelSelectors: wantSelectors,
				Rules: []SpreadingRule{
					{
						TopologyKey:               "topology.kubernetes.io/zone",
						MaxShareAllowingPlacement: resource.MustParse("0.45"),
						EnforcementMode:           kueue.TopologySpreadingEnforcementModeRequired,
					},
				},
			},
		},
		"valid: two rules, explicit enforcement modes": {
			value: selectorJSON + `,"rules":[` +
				`{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"0.45","enforcementMode":"Required"},` +
				`{"topologyKey":"cloud.com/gke-tpu-partition","maxShareAllowingPlacement":"0.22","enforcementMode":"Preferred"}]}`,
			wantSpec: &SpreadingSpec{
				WorkloadLabelSelectors: wantSelectors,
				Rules: []SpreadingRule{
					{
						TopologyKey:               "topology.kubernetes.io/zone",
						MaxShareAllowingPlacement: resource.MustParse("0.45"),
						EnforcementMode:           kueue.TopologySpreadingEnforcementModeRequired,
					},
					{
						TopologyKey:               "cloud.com/gke-tpu-partition",
						MaxShareAllowingPlacement: resource.MustParse("0.22"),
						EnforcementMode:           kueue.TopologySpreadingEnforcementModePreferred,
					},
				},
			},
		},
		"valid: share in exponent notation scales the same as its decimal form": {
			value: selectorJSON + `,"rules":[{"topologyKey":"a","maxShareAllowingPlacement":"45e-2"}]}`,
			wantSpec: &SpreadingSpec{
				WorkloadLabelSelectors: wantSelectors,
				Rules: []SpreadingRule{
					{
						TopologyKey:               "a",
						MaxShareAllowingPlacement: resource.MustParse("45e-2"),
						EnforcementMode:           kueue.TopologySpreadingEnforcementModeRequired,
					},
				},
			},
		},
		"invalid: malformed JSON": {
			value:   `{"workloadLabelSelectors":`,
			wantErr: ErrParseTopologySpreading,
		},
		"invalid: share is not a quantity": {
			value:   selectorJSON + `,"rules":[{"topologyKey":"a","maxShareAllowingPlacement":"half"}]}`,
			wantErr: ErrParseTopologySpreading,
		},
		"invalid: empty rules": {
			value:   selectorJSON + `,"rules":[]}`,
			wantErr: ErrTopologySpreadingRuleCount,
		},
		"invalid: three rules": {
			value: selectorJSON + `,"rules":[` +
				`{"topologyKey":"a","maxShareAllowingPlacement":"0.1"},` +
				`{"topologyKey":"b","maxShareAllowingPlacement":"0.1"},` +
				`{"topologyKey":"c","maxShareAllowingPlacement":"0.1"}]}`,
			wantErr: ErrTopologySpreadingRuleCount,
		},
		"invalid: selectors omitted": {
			value:   `{"rules":[{"topologyKey":"a","maxShareAllowingPlacement":"0.1"}]}`,
			wantErr: ErrTopologySpreadingSelectorMissing,
		},
		"invalid: selectors empty": {
			value:   `{"workloadLabelSelectors":[],"rules":[{"topologyKey":"a","maxShareAllowingPlacement":"0.1"}]}`,
			wantErr: ErrTopologySpreadingSelectorMissing,
		},
		"invalid: unknown JSON field is ignored, not rejected": {
			value:      selectorJSON + `,"rules":[{"topologyKey":"a","maxShareAllowingPlacement":"0.1"}],"unknown":"field"}`,
			wantErrNil: true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			spec, err := ParseSpreadingAnnotation(tc.value)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ParseSpreadingAnnotation() error = %v, want wrapping %v", err, tc.wantErr)
				}
				if spec != nil {
					t.Errorf("ParseSpreadingAnnotation() spec = %v, want nil on error", spec)
				}
				return
			}

			if err != nil {
				t.Fatalf("ParseSpreadingAnnotation() unexpected error: %v", err)
			}

			if tc.wantErrNil {
				return
			}

			if diff := cmp.Diff(tc.wantSpec, spec, cmpopts.IgnoreUnexported(SpreadingSpec{})); diff != "" {
				t.Errorf("ParseSpreadingAnnotation() spec mismatch (-want +got):\n%s", diff)
			}
			if got := spec.Selector(); got == nil {
				t.Error("ParseSpreadingAnnotation() Selector() = nil, want a compiled selector")
			} else if !got.Matches(labels.Set{"app": "main"}) {
				t.Errorf("ParseSpreadingAnnotation() Selector() = %v, want it to match app=main", got)
			}
		})
	}
}

func TestNewSpreadingSpec(t *testing.T) {
	testCases := map[string]struct {
		selectors   []metav1.LabelSelectorRequirement
		matchLabels labels.Set
		wantMatch   bool
		wantErr     bool
	}{
		"In requirement matches a Workload carrying the label": {
			selectors: []metav1.LabelSelectorRequirement{
				{Key: "app", Operator: metav1.LabelSelectorOpIn, Values: []string{"main"}},
			},
			matchLabels: labels.Set{"app": "main"},
			wantMatch:   true,
		},
		"In requirement does not match a different label value": {
			selectors: []metav1.LabelSelectorRequirement{
				{Key: "app", Operator: metav1.LabelSelectorOpIn, Values: []string{"main"}},
			},
			matchLabels: labels.Set{"app": "other"},
		},
		"In requirement does not match a Workload without the label": {
			selectors: []metav1.LabelSelectorRequirement{
				{Key: "app", Operator: metav1.LabelSelectorOpIn, Values: []string{"main"}},
			},
			matchLabels: labels.Set{"other": "main"},
		},
		"In requirement without values does not compile": {
			selectors: []metav1.LabelSelectorRequirement{
				{Key: "app", Operator: metav1.LabelSelectorOpIn},
			},
			wantErr: true,
		},
		"invalid label key does not compile": {
			selectors: []metav1.LabelSelectorRequirement{
				{Key: "_bad_", Operator: metav1.LabelSelectorOpIn, Values: []string{"main"}},
			},
			wantErr: true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			rules := []SpreadingRule{{TopologyKey: "a"}}

			spec, err := NewSpreadingSpec(tc.selectors, rules)

			if tc.wantErr {
				if !errors.Is(err, ErrTopologySpreadingSelectorInvalid) {
					t.Fatalf("NewSpreadingSpec() error = %v, want wrapping %v", err, ErrTopologySpreadingSelectorInvalid)
				}
				if spec != nil {
					t.Errorf("NewSpreadingSpec() = %v, want nil on error", spec)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewSpreadingSpec() unexpected error: %v", err)
			}
			if diff := cmp.Diff(rules, spec.Rules); diff != "" {
				t.Errorf("NewSpreadingSpec() Rules mismatch (-want +got):\n%s", diff)
			}
			if got := spec.Selector().Matches(tc.matchLabels); got != tc.wantMatch {
				t.Errorf("Selector().Matches(%v) = %t, want %t", tc.matchLabels, got, tc.wantMatch)
			}
		})
	}
}
