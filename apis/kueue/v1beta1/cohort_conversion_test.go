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

package v1beta1

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func TestCohortConvertFrom(t *testing.T) {
	defaultObjectMeta := metav1.ObjectMeta{
		Name: "test-cohort",
	}

	testCases := map[string]struct {
		input    *v1beta2.Cohort
		expected *Cohort
	}{
		"EffectiveQuotas in v1beta2 status is ignored when converting to v1beta1": {
			input: &v1beta2.Cohort{
				ObjectMeta: defaultObjectMeta,
				Status: v1beta2.CohortStatus{
					FairSharing: &v1beta2.FairSharingStatus{
						WeightedShare: 50,
					},
					EffectiveQuotas: &v1beta2.EffectiveQuotaStatus{
						OrchestratorRef: v1beta2.EffectiveQuotaStatusOrchestratorRef{
							APIGroup: "kueue.x-k8s.io",
							Kind:     "DynamicQuotaOrchestrator",
							Name:     "test-dqo",
						},
					},
				},
			},
			expected: &Cohort{
				ObjectMeta: defaultObjectMeta,
				Status: CohortStatus{
					FairSharing: &FairSharingStatus{
						WeightedShare: 50,
					},
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			result := &Cohort{}
			if err := result.ConvertFrom(tc.input); err != nil {
				t.Fatalf("ConvertFrom failed: %v", err)
			}
			if diff := cmp.Diff(tc.expected, result); diff != "" {
				t.Errorf("unexpected conversion result (-want +got):\n%s", diff)
			}
		})
	}
}
