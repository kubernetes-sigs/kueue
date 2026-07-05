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

package common

import (
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
)

// WithinProtectionWindow reports whether candidate is protected from preemption
// at time now under the given rule: the PreemptionProtection feature gate is on,
// the rule is set, the candidate is Admitted and not already Evicted, and its
// runtime since the Admitted transition is strictly less than minAdmitDuration.
// A candidate whose runtime is exactly equal to minAdmitDuration is eligible
// for preemption. Already-evicted candidates are never protected: they are the
// preferred zero-cost preemption victims.
func WithinProtectionWindow(candidate *kueue.Workload, rule *config.PreemptionProtectionPolicy, now time.Time) bool {
	if rule == nil || rule.MinAdmitDuration == nil {
		return false
	}
	if !features.Enabled(features.PreemptionProtection) {
		return false
	}
	if workloadevict.IsEvicted(candidate) {
		return false
	}
	admittedCond := apimeta.FindStatusCondition(candidate.Status.Conditions, kueue.WorkloadAdmitted)
	if admittedCond == nil || admittedCond.Status != metav1.ConditionTrue {
		return false
	}
	return now.Sub(admittedCond.LastTransitionTime.Time) < rule.MinAdmitDuration.Duration
}

// ProtectionExpiry returns the time at which the candidate's protection under
// the given rule expires: the Admitted condition transition time plus the
// rule's minAdmitDuration. It returns the zero time if the rule is unset or
// the candidate has no Admitted condition.
func ProtectionExpiry(candidate *kueue.Workload, rule *config.PreemptionProtectionPolicy) time.Time {
	if rule == nil || rule.MinAdmitDuration == nil {
		return time.Time{}
	}
	admittedCond := apimeta.FindStatusCondition(candidate.Status.Conditions, kueue.WorkloadAdmitted)
	if admittedCond == nil {
		return time.Time{}
	}
	return admittedCond.LastTransitionTime.Add(rule.MinAdmitDuration.Duration)
}
