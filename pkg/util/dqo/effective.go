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
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

// EffectiveQuotaTTL returns spec.capacityDistribution.effectiveQuotaTTLSeconds of
// the DynamicQuotaOrchestrator, or 0 when it is not set and the effective quota
// does not expire.
func EffectiveQuotaTTL(dqo *kueuealpha.DynamicQuotaOrchestrator) time.Duration {
	if cd := dqo.Spec.CapacityDistribution; cd != nil && cd.EffectiveQuotaTTLSeconds != nil {
		return time.Duration(*cd.EffectiveQuotaTTLSeconds) * time.Second
	}
	return 0
}

// IsInactive returns true when the EffectiveQuotaActive condition is False.
func IsInactive(conditions []metav1.Condition) bool {
	return apimeta.IsStatusConditionFalse(conditions, kueue.EffectiveQuotaActive)
}

// SetActiveCondition sets the EffectiveQuotaActive condition from
// effectiveQuotas.validUntil, or removes it when the DynamicQuotaOrchestration
// feature gate is disabled or effectiveQuotas is not set. It returns the time
// left until validUntil while the effective quota is active, and 0 otherwise.
// now is called only when validUntil is set. The messages do not include
// validUntil, so renewing it does not rewrite the condition.
func SetActiveCondition(conditions *[]metav1.Condition, effectiveQuotas *kueue.EffectiveQuotaStatus, generation int64, now func() time.Time) time.Duration {
	if !features.Enabled(features.DynamicQuotaOrchestration) || effectiveQuotas == nil {
		apimeta.RemoveStatusCondition(conditions, kueue.EffectiveQuotaActive)
		return 0
	}
	condition := metav1.Condition{
		Type:               kueue.EffectiveQuotaActive,
		Status:             metav1.ConditionTrue,
		Reason:             kueue.EffectiveQuotaActiveReasonReady,
		ObservedGeneration: generation,
	}
	if effectiveQuotas.ValidUntil == nil {
		condition.Message = "status.effectiveQuotas.validUntil is not set"
		apimeta.SetStatusCondition(conditions, condition)
		return 0
	}
	remaining := effectiveQuotas.ValidUntil.Sub(now())
	if remaining <= 0 {
		condition.Status = metav1.ConditionFalse
		condition.Reason = kueue.EffectiveQuotaActiveReasonExpired
		condition.Message = "status.effectiveQuotas.validUntil has passed"
		remaining = 0
	} else {
		condition.Message = "status.effectiveQuotas.validUntil has not passed"
	}
	apimeta.SetStatusCondition(conditions, condition)
	return remaining
}

// EffectiveOrchestrator returns the orchestrator reference when DynamicQuotaOrchestration
// feature gate is enabled and effective quota is set and active; otherwise returns
// an empty reference.
func EffectiveOrchestrator(effectiveQuotas *kueue.EffectiveQuotaStatus, conditions []metav1.Condition) kueuealpha.DynamicQuotaOrchestratorReference {
	if features.Enabled(features.DynamicQuotaOrchestration) && effectiveQuotas != nil && !IsInactive(conditions) {
		return kueuealpha.DynamicQuotaOrchestratorReference(effectiveQuotas.OrchestratorRef.Name)
	}
	return ""
}
