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

package resourcegroups

import (
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

// EffectiveResourceGroups returns Status.EffectiveQuotas.ResourceGroups when DynamicQuotaOrchestration
// feature gate is enabled and effective quota is set; otherwise returns Spec.ResourceGroups.
func EffectiveResourceGroups(cq *kueue.ClusterQueue) []kueue.ResourceGroup {
	if cq == nil {
		return nil
	}
	if features.Enabled(features.DynamicQuotaOrchestration) && cq.Status.EffectiveQuotas != nil {
		return cq.Status.EffectiveQuotas.ResourceGroups
	}
	return cq.Spec.ResourceGroups
}

// EffectiveCohortResourceGroups returns Status.EffectiveQuotas.ResourceGroups when DynamicQuotaOrchestration
// feature gate is enabled and effective quota is set; otherwise returns Spec.ResourceGroups.
func EffectiveCohortResourceGroups(cohort *kueue.Cohort) []kueue.ResourceGroup {
	if cohort == nil {
		return nil
	}
	if features.Enabled(features.DynamicQuotaOrchestration) && cohort.Status.EffectiveQuotas != nil {
		return cohort.Status.EffectiveQuotas.ResourceGroups
	}
	return cohort.Spec.ResourceGroups
}
