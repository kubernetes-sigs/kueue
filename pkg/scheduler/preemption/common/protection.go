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

// withinProtectionWindow reports whether candidate is protected from
// preemption under the given rule at time now.
func withinProtectionWindow(candidate *kueue.Workload, rule *config.PreemptionProtectionPolicy, now time.Time) bool {
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

// ProtectionExpiry returns Admitted + minAdmitDuration, or zero if the
// rule is unset or the candidate is not Admitted.
func ProtectionExpiry(candidate *kueue.Workload, rule *config.PreemptionProtectionPolicy) time.Time {
	if rule == nil || rule.MinAdmitDuration == nil {
		return time.Time{}
	}
	admittedCond := apimeta.FindStatusCondition(candidate.Status.Conditions, kueue.WorkloadAdmitted)
	if admittedCond == nil || admittedCond.Status != metav1.ConditionTrue {
		return time.Time{}
	}
	return admittedCond.LastTransitionTime.Add(rule.MinAdmitDuration.Duration)
}

// ProtectionSkipTracker records skipped candidates and keeps the earliest
// protection expiry for the retry mechanism. Zero value is ready to use.
type ProtectionSkipTracker struct {
	earliestExpiry time.Time
}

// Skip returns true if candidate is within its protection window, recording
// the expiry. A nil tracker still filters but records nothing.
func (t *ProtectionSkipTracker) Skip(candidate *kueue.Workload, rule *config.PreemptionProtectionPolicy, now time.Time) bool {
	if !withinProtectionWindow(candidate, rule, now) {
		return false
	}
	expiry := ProtectionExpiry(candidate, rule)
	if t != nil && (t.earliestExpiry.IsZero() || expiry.Before(t.earliestExpiry)) {
		t.earliestExpiry = expiry
	}
	return true
}

// EarliestExpiry returns the earliest protection expiry among skipped
// candidates, or the zero time if no candidate was skipped.
func (t *ProtectionSkipTracker) EarliestExpiry() time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.earliestExpiry
}
