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
		value    string
		wantSpec *SpreadingSpec
		wantErr  error
		// wantMatchesNothing expects a spec whose selector matches no
		// Workload, which is what an omitted selector resolves to.
		wantMatchesNothing bool
		wantErrNil         bool
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
		// A selector that is present but does not compile is still an error -
		// only omitting it entirely is the request for the default.
		"invalid: selector present but does not compile": {
			value: `{"workloadLabelSelectors":[{"key":"_bad_","operator":"In","values":["main"]}],` +
				`"rules":[{"topologyKey":"a","maxShareAllowingPlacement":"0.1"}]}`,
			wantErr: ErrTopologySpreadingSelectorInvalid,
		},
		"invalid: three rules": {
			value: selectorJSON + `,"rules":[` +
				`{"topologyKey":"a","maxShareAllowingPlacement":"0.1"},` +
				`{"topologyKey":"b","maxShareAllowingPlacement":"0.1"},` +
				`{"topologyKey":"c","maxShareAllowingPlacement":"0.1"}]}`,
			wantErr: ErrTopologySpreadingRuleCount,
		},
		// An omitted selector is how the user asks for the job-uid default, so
		// it parses. It resolves to a selector matching nothing rather than
		// the labels.Everything() an empty requirement list would otherwise
		// produce, so a Workload the default never reached (a prebuilt one)
		// gets no spreading instead of being spread against the whole
		// namespace.
		"valid: selectors omitted, matches nothing": {
			value: `{"rules":[{"topologyKey":"a","maxShareAllowingPlacement":"0.1"}]}`,
			wantSpec: &SpreadingSpec{
				Rules: []SpreadingRule{
					{
						TopologyKey:               "a",
						MaxShareAllowingPlacement: resource.MustParse("0.1"),
						EnforcementMode:           kueue.TopologySpreadingEnforcementModeRequired,
					},
				},
			},
			wantMatchesNothing: true,
		},
		"valid: selectors empty, matches nothing": {
			value: `{"workloadLabelSelectors":[],"rules":[{"topologyKey":"a","maxShareAllowingPlacement":"0.1"}]}`,
			wantSpec: &SpreadingSpec{
				WorkloadLabelSelectors: []metav1.LabelSelectorRequirement{},
				Rules: []SpreadingRule{
					{
						TopologyKey:               "a",
						MaxShareAllowingPlacement: resource.MustParse("0.1"),
						EnforcementMode:           kueue.TopologySpreadingEnforcementModeRequired,
					},
				},
			},
			wantMatchesNothing: true,
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
			got := spec.Selector()
			if got == nil {
				t.Fatal("ParseSpreadingAnnotation() Selector() = nil, want a compiled selector")
			}
			// Asserted against a Workload carrying the shared selector's
			// label, so a selector matching nothing and one matching app=main
			// are told apart.
			if matches := got.Matches(labels.Set{"app": "main"}); matches == tc.wantMatchesNothing {
				t.Errorf("ParseSpreadingAnnotation() Selector() = %v, matches app=main = %t, want %t", got, matches, !tc.wantMatchesNothing)
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

func TestInjectDefaultSpreadingSelector(t *testing.T) {
	const rule = `"rules":[{"topologyKey":"topology.kubernetes.io/zone","maxShareAllowingPlacement":"0.45"}]`

	// The rule survives verbatim - "0.45" is still "0.45", not the equal
	// "450m" a re-encoded resource.Quantity would produce - and only the
	// selector key is added. Keys come out sorted, since the surrounding
	// object is rewritten from a map.
	const wantDefaulted = `{` + rule + `,"workloadLabelSelectors":` +
		`[{"key":"kueue.x-k8s.io/job-uid","operator":"In","values":["job-uid-1"]}]}`

	testCases := map[string]struct {
		value       string
		jobUID      string
		wantValue   string
		wantChanged bool
		wantErr     error
	}{
		"selector omitted: default injected": {
			value:       `{` + rule + `}`,
			jobUID:      "job-uid-1",
			wantValue:   wantDefaulted,
			wantChanged: true,
		},
		"selector explicitly empty: default injected": {
			value:       `{"workloadLabelSelectors":[],` + rule + `}`,
			jobUID:      "job-uid-1",
			wantValue:   wantDefaulted,
			wantChanged: true,
		},
		"selector already set: left alone": {
			value:     `{"workloadLabelSelectors":[{"key":"app","operator":"In","values":["main"]}],` + rule + `}`,
			jobUID:    "job-uid-1",
			wantValue: `{"workloadLabelSelectors":[{"key":"app","operator":"In","values":["main"]}],` + rule + `}`,
		},
		// A prebuilt Workload has no job-uid label at create time, so there is
		// nothing to inject and the annotation must survive untouched.
		"no job UID: left alone": {
			value:     `{` + rule + `}`,
			jobUID:    "",
			wantValue: `{` + rule + `}`,
		},
		// Reusing ParseSpreadingAnnotation means an annotation that is valid
		// JSON but invalid as a spec is left alone too, rather than having a
		// selector injected into something that will be rejected anyway.
		"valid JSON but no rules: left alone and reported": {
			value:     `{"rules":[]}`,
			jobUID:    "job-uid-1",
			wantValue: `{"rules":[]}`,
			wantErr:   ErrTopologySpreadingRuleCount,
		},
		// The validating webhook is what reports a malformed annotation;
		// defaulting must not rewrite or swallow it.
		"not a JSON object: left alone and reported": {
			value:     `not json`,
			jobUID:    "job-uid-1",
			wantValue: `not json`,
			wantErr:   ErrParseTopologySpreading,
		},
		"selector is not an array: left alone and reported": {
			value:     `{"workloadLabelSelectors":"app=main",` + rule + `}`,
			jobUID:    "job-uid-1",
			wantValue: `{"workloadLabelSelectors":"app=main",` + rule + `}`,
			wantErr:   ErrParseTopologySpreading,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got, changed, err := InjectDefaultSpreadingSelector(tc.value, tc.jobUID)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("InjectDefaultSpreadingSelector() error = %v, want wrapping %v", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("InjectDefaultSpreadingSelector() unexpected error: %v", err)
			}

			if got != tc.wantValue {
				t.Errorf("InjectDefaultSpreadingSelector() value = %s, want %s", got, tc.wantValue)
			}
			if changed != tc.wantChanged {
				t.Errorf("InjectDefaultSpreadingSelector() changed = %t, want %t", changed, tc.wantChanged)
			}

			// The injected value must be usable by the scheduler, which parses
			// strictly - that round trip is the point of the whole exercise.
			if tc.wantChanged {
				spec, err := ParseSpreadingAnnotation(got)
				if err != nil {
					t.Fatalf("ParseSpreadingAnnotation() on the defaulted value: %v", err)
				}
				if !spec.Selector().Matches(labels.Set{"kueue.x-k8s.io/job-uid": tc.jobUID}) {
					t.Errorf("defaulted Selector() = %v, want it to match the job UID", spec.Selector())
				}
			}
		})
	}
}
