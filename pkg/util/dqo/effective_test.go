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
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestEffectiveOrchestrator(t *testing.T) {
	effectiveQuotas := &kueue.EffectiveQuotaStatus{
		OrchestratorRef: kueue.EffectiveQuotaStatusOrchestratorRef{
			Name: "dqo-test",
		},
	}
	cases := map[string]struct {
		dynamicQuota    bool
		effectiveQuotas *kueue.EffectiveQuotaStatus
		conditions      []metav1.Condition
		want            kueuealpha.DynamicQuotaOrchestratorReference
	}{
		"DynamicQuotaOrchestration disabled returns empty": {
			dynamicQuota:    false,
			effectiveQuotas: effectiveQuotas,
			want:            "",
		},
		"DynamicQuotaOrchestration enabled returns orchestrator when set": {
			dynamicQuota:    true,
			effectiveQuotas: effectiveQuotas,
			want:            "dqo-test",
		},
		"DynamicQuotaOrchestration enabled returns empty when effectiveQuotas is nil": {
			dynamicQuota:    true,
			effectiveQuotas: nil,
			want:            "",
		},
		"DynamicQuotaOrchestration enabled returns empty when EffectiveQuotaActive is False": {
			dynamicQuota:    true,
			effectiveQuotas: effectiveQuotas,
			conditions: []metav1.Condition{{
				Type:   kueue.EffectiveQuotaActive,
				Status: metav1.ConditionFalse,
				Reason: kueue.EffectiveQuotaActiveReasonExpired,
			}},
			want: "",
		},
		"DynamicQuotaOrchestration enabled returns orchestrator when EffectiveQuotaActive is True": {
			dynamicQuota:    true,
			effectiveQuotas: effectiveQuotas,
			conditions: []metav1.Condition{{
				Type:   kueue.EffectiveQuotaActive,
				Status: metav1.ConditionTrue,
				Reason: kueue.EffectiveQuotaActiveReasonReady,
			}},
			want: "dqo-test",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, tc.dynamicQuota)
			got := EffectiveOrchestrator(tc.effectiveQuotas, tc.conditions)
			if got != tc.want {
				t.Errorf("EffectiveOrchestrator() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEffectiveQuotaTTL(t *testing.T) {
	cases := map[string]struct {
		distribution *kueuealpha.CapacityDistribution
		want         time.Duration
	}{
		"no capacityDistribution returns 0": {},
		"no effectiveQuotaTTLSeconds returns 0": {
			distribution: &kueuealpha.CapacityDistribution{},
		},
		"effectiveQuotaTTLSeconds set returns it": {
			distribution: &kueuealpha.CapacityDistribution{
				EffectiveQuotaTTLSeconds: new(int32(180)),
			},
			want: 3 * time.Minute,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dqo := &kueuealpha.DynamicQuotaOrchestrator{
				Spec: kueuealpha.DynamicQuotaOrchestratorSpec{CapacityDistribution: tc.distribution},
			}
			if got := EffectiveQuotaTTL(dqo); got != tc.want {
				t.Errorf("EffectiveQuotaTTL() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSetActiveCondition(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	const generation = 3

	expired := metav1.Condition{
		Type:   kueue.EffectiveQuotaActive,
		Status: metav1.ConditionFalse,
		Reason: kueue.EffectiveQuotaActiveReasonExpired,
	}

	cases := map[string]struct {
		disableFeatureGate bool
		effectiveQuotas    *kueue.EffectiveQuotaStatus
		conditions         []metav1.Condition
		wantConditions     []metav1.Condition
		wantRemaining      time.Duration
	}{
		"validUntil in the future sets Ready and returns the time left": {
			effectiveQuotas: utiltestingapi.MakeEffectiveQuotaStatus().ValidUntil(now.Add(4 * time.Minute)).Obj(),
			wantConditions: []metav1.Condition{{
				Type:               kueue.EffectiveQuotaActive,
				Status:             metav1.ConditionTrue,
				Reason:             kueue.EffectiveQuotaActiveReasonReady,
				Message:            "status.effectiveQuotas.validUntil has not passed",
				ObservedGeneration: generation,
			}},
			wantRemaining: 4 * time.Minute,
		},
		"validUntil equal to now sets Expired": {
			effectiveQuotas: utiltestingapi.MakeEffectiveQuotaStatus().ValidUntil(now).Obj(),
			wantConditions: []metav1.Condition{{
				Type:               kueue.EffectiveQuotaActive,
				Status:             metav1.ConditionFalse,
				Reason:             kueue.EffectiveQuotaActiveReasonExpired,
				Message:            "status.effectiveQuotas.validUntil has passed",
				ObservedGeneration: generation,
			}},
		},
		"validUntil passed sets Expired": {
			effectiveQuotas: utiltestingapi.MakeEffectiveQuotaStatus().ValidUntil(now.Add(-time.Second)).Obj(),
			wantConditions: []metav1.Condition{{
				Type:               kueue.EffectiveQuotaActive,
				Status:             metav1.ConditionFalse,
				Reason:             kueue.EffectiveQuotaActiveReasonExpired,
				Message:            "status.effectiveQuotas.validUntil has passed",
				ObservedGeneration: generation,
			}},
		},
		"Expired becomes Ready when validUntil moves to the future": {
			effectiveQuotas: utiltestingapi.MakeEffectiveQuotaStatus().ValidUntil(now.Add(time.Minute)).Obj(),
			conditions:      []metav1.Condition{expired},
			wantConditions: []metav1.Condition{{
				Type:               kueue.EffectiveQuotaActive,
				Status:             metav1.ConditionTrue,
				Reason:             kueue.EffectiveQuotaActiveReasonReady,
				Message:            "status.effectiveQuotas.validUntil has not passed",
				ObservedGeneration: generation,
			}},
			wantRemaining: time.Minute,
		},
		"no validUntil sets Ready": {
			effectiveQuotas: utiltestingapi.MakeEffectiveQuotaStatus().Obj(),
			conditions:      []metav1.Condition{expired},
			wantConditions: []metav1.Condition{{
				Type:               kueue.EffectiveQuotaActive,
				Status:             metav1.ConditionTrue,
				Reason:             kueue.EffectiveQuotaActiveReasonReady,
				Message:            "status.effectiveQuotas.validUntil is not set",
				ObservedGeneration: generation,
			}},
		},
		"no effective quota removes the condition": {
			conditions: []metav1.Condition{expired},
		},
		"feature gate disabled removes the condition": {
			disableFeatureGate: true,
			effectiveQuotas:    utiltestingapi.MakeEffectiveQuotaStatus().ValidUntil(now.Add(-time.Second)).Obj(),
			conditions:         []metav1.Condition{expired},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, !tc.disableFeatureGate)
			conditions := append([]metav1.Condition(nil), tc.conditions...)
			nowFn := func() time.Time {
				if tc.effectiveQuotas == nil || tc.effectiveQuotas.ValidUntil == nil || tc.disableFeatureGate {
					t.Error("now called although validUntil is not used")
				}
				return now
			}

			gotRemaining := SetActiveCondition(&conditions, tc.effectiveQuotas, generation, nowFn)
			if gotRemaining != tc.wantRemaining {
				t.Errorf("SetActiveCondition() = %v, want %v", gotRemaining, tc.wantRemaining)
			}
			if diff := cmp.Diff(tc.wantConditions, conditions, cmpopts.EquateEmpty(), cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")); diff != "" {
				t.Errorf("Unexpected conditions (-want,+got):\n%s", diff)
			}

			// A second call with the same input must not change the condition,
			// or every reconcile would write status.
			again := append([]metav1.Condition(nil), conditions...)
			if gotAgain := SetActiveCondition(&again, tc.effectiveQuotas, generation, nowFn); gotAgain != gotRemaining {
				t.Errorf("Second SetActiveCondition() = %v, want %v", gotAgain, gotRemaining)
			}
			if diff := cmp.Diff(conditions, again, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Second call changed the conditions (-first,+second):\n%s", diff)
			}
		})
	}
}

func TestSetActiveConditionKeepsConditionWhenValidUntilIsRenewed(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, true)
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return now }

	var conditions []metav1.Condition
	SetActiveCondition(&conditions, utiltestingapi.MakeEffectiveQuotaStatus().ValidUntil(now.Add(time.Minute)).Obj(), 1, nowFn)
	first := append([]metav1.Condition(nil), conditions...)

	got := SetActiveCondition(&conditions, utiltestingapi.MakeEffectiveQuotaStatus().ValidUntil(now.Add(10*time.Minute)).Obj(), 1, nowFn)
	if got != 10*time.Minute {
		t.Errorf("SetActiveCondition() = %v, want %v", got, 10*time.Minute)
	}
	if diff := cmp.Diff(first, conditions); diff != "" {
		t.Errorf("Renewing validUntil changed the condition (-before,+after):\n%s", diff)
	}
}

func TestIsInactive(t *testing.T) {
	cases := map[string]struct {
		conditions []metav1.Condition
		want       bool
	}{
		"no condition": {},
		"True": {
			conditions: []metav1.Condition{{Type: kueue.EffectiveQuotaActive, Status: metav1.ConditionTrue}},
		},
		"False": {
			conditions: []metav1.Condition{{Type: kueue.EffectiveQuotaActive, Status: metav1.ConditionFalse}},
			want:       true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := IsInactive(tc.conditions); got != tc.want {
				t.Errorf("IsInactive() = %v, want %v", got, tc.want)
			}
		})
	}
}
