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

	"github.com/go-logr/logr"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

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
// rule's minAdmitDuration. Mirroring WithinProtectionWindow, it returns the
// zero time if the rule is unset or the candidate's Admitted condition is not
// True.
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

// ProtectionSkipTracker records preemption candidates skipped because they
// are within their preemption-protection window, keeping the earliest
// protection expiry among them for the protection-expiry retry mechanism.
// The zero value is ready to use; one tracker is shared per target-selection
// cycle across the classical and fair sharing paths.
type ProtectionSkipTracker struct {
	earliestExpiry time.Time
}

// Skip reports whether candidate is within its protection window under rule
// at time now. When it is, Skip records the candidate's protection expiry
// (keeping the earliest across calls) and logs the skip at V(4) with the
// preemption reason the candidate would otherwise have been preempted with.
// Callers must evaluate all other preemption criteria first, so that a skip
// means protection was the sole blocker. A nil tracker still filters, but
// records no expiry.
func (t *ProtectionSkipTracker) Skip(log logr.Logger, candidate *kueue.Workload, rule *config.PreemptionProtectionPolicy, reason string, now time.Time) bool {
	if !WithinProtectionWindow(candidate, rule, now) {
		return false
	}
	expiry := ProtectionExpiry(candidate, rule)
	if t != nil && (t.earliestExpiry.IsZero() || expiry.Before(t.earliestExpiry)) {
		t.earliestExpiry = expiry
	}
	log.V(4).Info("Skipping preemption candidate within its protection window",
		"workload", klog.KObj(candidate),
		"reason", reason,
		"remainingProtection", expiry.Sub(now))
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
