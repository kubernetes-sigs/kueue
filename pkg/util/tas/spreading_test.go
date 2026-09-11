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
		value string
		// defaultJobUID is the parent job UID an omitted selector resolves to,
		// empty for the Workloads that have no parent job yet.
		defaultJobUID string
		wantSpec      *SpreadingSpec
		wantErr       error
		// matchLabels are the labels the compiled selector is asserted
		// against, defaulting to the shared selector's app=main.
		matchLabels labels.Set
		// wantMatchesNothing expects a spec whose selector matches no Workload,
		// which is what an omitted selector with no job UID resolves to.
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
		// Omitting the selectors is how the user asks for the job-uid default,
		// so it parses and resolves to the parent job's group. The spec keeps
		// no selectors of its own: it stays what the user wrote, so the webhook
		// validates their input and not a value Kueue synthesized.
		"valid: selectors omitted, resolved to the job UID": {
			value:         `{"rules":[{"topologyKey":"a","maxShareAllowingPlacement":"0.1"}]}`,
			defaultJobUID: "job-uid-1",
			wantSpec: &SpreadingSpec{
				Rules: []SpreadingRule{
					{
						TopologyKey:               "a",
						MaxShareAllowingPlacement: resource.MustParse("0.1"),
						EnforcementMode:           kueue.TopologySpreadingEnforcementModeRequired,
					},
				},
			},
			matchLabels: labels.Set{"kueue.x-k8s.io/job-uid": "job-uid-1"},
		},
		"valid: selectors empty, resolved to the job UID": {
			value:         `{"workloadLabelSelectors":[],"rules":[{"topologyKey":"a","maxShareAllowingPlacement":"0.1"}]}`,
			defaultJobUID: "job-uid-1",
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
			matchLabels: labels.Set{"kueue.x-k8s.io/job-uid": "job-uid-1"},
		},
		// The default resolves to another job's group, not to everyone's.
		"valid: selectors omitted, does not reach another job's Workloads": {
			value:         `{"rules":[{"topologyKey":"a","maxShareAllowingPlacement":"0.1"}]}`,
			defaultJobUID: "job-uid-1",
			wantSpec: &SpreadingSpec{
				Rules: []SpreadingRule{
					{
						TopologyKey:               "a",
						MaxShareAllowingPlacement: resource.MustParse("0.1"),
						EnforcementMode:           kueue.TopologySpreadingEnforcementModeRequired,
					},
				},
			},
			matchLabels:        labels.Set{"kueue.x-k8s.io/job-uid": "job-uid-2"},
			wantMatchesNothing: true,
		},
		// With no parent job there is no group to spread within, so the
		// selector matches nothing rather than the labels.Everything() an
		// empty requirement list would otherwise produce - the Workload gets
		// no spreading instead of being spread against the whole namespace.
		"valid: selectors omitted and no job UID, matches nothing": {
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
		// An explicit selector is not overridden by the default.
		"valid: selectors set, job UID ignored": {
			value:         selectorJSON + `,"rules":[{"topologyKey":"a","maxShareAllowingPlacement":"0.1"}]}`,
			defaultJobUID: "job-uid-1",
			wantSpec: &SpreadingSpec{
				WorkloadLabelSelectors: wantSelectors,
				Rules: []SpreadingRule{
					{
						TopologyKey:               "a",
						MaxShareAllowingPlacement: resource.MustParse("0.1"),
						EnforcementMode:           kueue.TopologySpreadingEnforcementModeRequired,
					},
				},
			},
			matchLabels:        labels.Set{"kueue.x-k8s.io/job-uid": "job-uid-1"},
			wantMatchesNothing: true,
		},
		"invalid: unknown JSON field is ignored, not rejected": {
			value:      selectorJSON + `,"rules":[{"topologyKey":"a","maxShareAllowingPlacement":"0.1"}],"unknown":"field"}`,
			wantErrNil: true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			spec, err := ParseSpreadingAnnotation(tc.value, tc.defaultJobUID)

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
			// Asserted against a Workload carrying a label the selector is
			// expected to accept, so a selector matching nothing and one
			// matching those labels are told apart.
			matchLabels := tc.matchLabels
			if matchLabels == nil {
				matchLabels = labels.Set{"app": "main"}
			}
			if matches := got.Matches(matchLabels); matches == tc.wantMatchesNothing {
				t.Errorf("ParseSpreadingAnnotation() Selector() = %v, matches %v = %t, want %t", got, matchLabels, matches, !tc.wantMatchesNothing)
			}
		})
	}
}

func TestNewSpreadingSpec(t *testing.T) {
	testCases := map[string]struct {
		selectors     []metav1.LabelSelectorRequirement
		defaultJobUID string
		matchLabels   labels.Set
		wantMatch     bool
		wantErr       bool
	}{
		"no selectors resolve to the default job UID": {
			defaultJobUID: "job-uid-1",
			matchLabels:   labels.Set{"kueue.x-k8s.io/job-uid": "job-uid-1"},
			wantMatch:     true,
		},
		"no selectors and no default job UID match nothing": {
			matchLabels: labels.Set{"kueue.x-k8s.io/job-uid": "job-uid-1"},
		},
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

			spec, err := NewSpreadingSpec(tc.selectors, rules, tc.defaultJobUID)

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
