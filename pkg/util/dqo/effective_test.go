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

package dqo

import (
	"testing"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

func TestEffectiveOrchestrator(t *testing.T) {
	cases := map[string]struct {
		dynamicQuota    bool
		effectiveQuotas *kueue.EffectiveQuotaStatus
		want            kueuealpha.DynamicQuotaOrchestratorReference
	}{
		"DynamicQuotaOrchestration disabled returns empty": {
			dynamicQuota: false,
			effectiveQuotas: &kueue.EffectiveQuotaStatus{
				OrchestratorRef: kueue.EffectiveQuotaStatusOrchestratorRef{
					Name: "dqo-test",
				},
			},
			want: "",
		},
		"DynamicQuotaOrchestration enabled returns orchestrator when set": {
			dynamicQuota: true,
			effectiveQuotas: &kueue.EffectiveQuotaStatus{
				OrchestratorRef: kueue.EffectiveQuotaStatusOrchestratorRef{
					Name: "dqo-test",
				},
			},
			want: "dqo-test",
		},
		"DynamicQuotaOrchestration enabled returns empty when effectiveQuotas is nil": {
			dynamicQuota:    true,
			effectiveQuotas: nil,
			want:            "",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, tc.dynamicQuota)
			got := EffectiveOrchestrator(tc.effectiveQuotas)
			if got != tc.want {
				t.Errorf("EffectiveOrchestrator() = %v, want %v", got, tc.want)
			}
		})
	}
}
