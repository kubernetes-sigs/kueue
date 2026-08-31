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
)

func TestParseSpreadingAnnotation(t *testing.T) {
	testCases := map[string]struct {
		value      string
		wantSpec   *SpreadingSpec
		wantErr    error
		wantErrNil bool
	}{
		"valid: single rule, type defaulted": {
			value: `{"workloadLabelSelector":"app=main","rules":[{"key":"topology.kubernetes.io/zone","maxDomainPercentage":45}]}`,
			wantSpec: &SpreadingSpec{
				WorkloadLabelSelectorStr: "app=main",
				Rules: []SpreadingRule{
					{Key: "topology.kubernetes.io/zone", MaxDomainPercentage: 45, Type: TopologySpreadingRuleRequired},
				},
			},
		},
		"valid: two rules, explicit types": {
			value: `{"workloadLabelSelector":"app=main","rules":[` +
				`{"key":"topology.kubernetes.io/zone","maxDomainPercentage":45,"type":"Required"},` +
				`{"key":"cloud.com/gke-tpu-partition","maxDomainPercentage":22,"type":"Preferred"}]}`,
			wantSpec: &SpreadingSpec{
				WorkloadLabelSelectorStr: "app=main",
				Rules: []SpreadingRule{
					{Key: "topology.kubernetes.io/zone", MaxDomainPercentage: 45, Type: "Required"},
					{Key: "cloud.com/gke-tpu-partition", MaxDomainPercentage: 22, Type: "Preferred"},
				},
			},
		},
		"invalid: malformed JSON": {
			value:   `{"workloadLabelSelector":`,
			wantErr: ErrParseTopologySpreading,
		},
		"invalid: empty rules": {
			value:   `{"workloadLabelSelector":"app=main","rules":[]}`,
			wantErr: ErrTopologySpreadingRuleCount,
		},
		"invalid: three rules": {
			value: `{"workloadLabelSelector":"app=main","rules":[` +
				`{"key":"a","maxDomainPercentage":10},{"key":"b","maxDomainPercentage":10},{"key":"c","maxDomainPercentage":10}]}`,
			wantErr: ErrTopologySpreadingRuleCount,
		},
		"invalid: unparseable selector": {
			value:   `{"workloadLabelSelector":"app in (","rules":[{"key":"a","maxDomainPercentage":10}]}`,
			wantErr: ErrParseTopologySpreading,
		},
		"invalid: unknown JSON field is ignored, not rejected": {
			value:      `{"workloadLabelSelector":"app=main","rules":[{"key":"a","maxDomainPercentage":10}],"unknown":"field"}`,
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

			if diff := cmp.Diff(tc.wantSpec, spec, cmpopts.IgnoreFields(SpreadingSpec{}, "WorkloadLabelSelector")); diff != "" {
				t.Errorf("ParseSpreadingAnnotation() spec mismatch (-want +got):\n%s", diff)
			}
			if spec.WorkloadLabelSelector == nil || spec.WorkloadLabelSelector.Empty() {
				t.Errorf("ParseSpreadingAnnotation() WorkloadLabelSelector = %v, want a compiled non-empty selector", spec.WorkloadLabelSelector)
			}
		})
	}
}
