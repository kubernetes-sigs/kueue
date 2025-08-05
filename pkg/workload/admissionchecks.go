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

package workload

import (
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/clock"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

// SyncAdmittedCondition sync the state of the Admitted condition based on the
// state of QuotaReserved, AdmissionChecks and DelayedTopologyRequests.
// Return true if any change was done.
func SyncAdmittedCondition(w *kueue.Workload, now time.Time) bool {
	hasReservation := HasQuotaReservation(w)
	hasAllChecksReady := HasAllChecksReady(w)
	isAdmitted := IsAdmitted(w)
	hasAllTopologyAssignmentsReady := !HasTopologyAssignmentsPending(w)

	if isAdmitted == (hasReservation && hasAllChecksReady && hasAllTopologyAssignmentsReady) {
		return false
	}
	newCondition := metav1.Condition{
		Type:               kueue.WorkloadAdmitted,
		Status:             metav1.ConditionTrue,
		Reason:             "Admitted",
		Message:            "The workload is admitted",
		ObservedGeneration: w.Generation,
	}
	switch {
	case !hasReservation && !hasAllChecksReady:
		newCondition.Status = metav1.ConditionFalse
		newCondition.Reason = "NoReservationUnsatisfiedChecks"
		newCondition.Message = "The workload has no reservation and not all checks ready"
	case !hasReservation:
		newCondition.Status = metav1.ConditionFalse
		newCondition.Reason = "NoReservation"
		newCondition.Message = "The workload has no reservation"
	case !hasAllChecksReady:
		newCondition.Status = metav1.ConditionFalse
		newCondition.Reason = "UnsatisfiedChecks"
		newCondition.Message = "The workload has not all checks ready"
	case !hasAllTopologyAssignmentsReady:
		newCondition.Status = metav1.ConditionFalse
		newCondition.Reason = "PendingDelayedTopologyRequests"
		newCondition.Message = "There are pending delayed topology requests"
	}

	// Accumulate the admitted time if needed
	if isAdmitted && newCondition.Status == metav1.ConditionFalse {
		oldCondition := apimeta.FindStatusCondition(w.Status.Conditions, kueue.WorkloadAdmitted)
		// in practice the oldCondition cannot be nil, however we should try to avoid nil ptr deref.
		if oldCondition != nil {
			d := int32(now.Sub(oldCondition.LastTransitionTime.Time).Seconds())
			if w.Status.AccumulatedPastExexcutionTimeSeconds != nil {
				*w.Status.AccumulatedPastExexcutionTimeSeconds += d
			} else {
				w.Status.AccumulatedPastExexcutionTimeSeconds = &d
			}
		}
	}
	return apimeta.SetStatusCondition(&w.Status.Conditions, newCondition)
}

// FindAdmissionCheck - returns a pointer to the check identified by checkName if found in checks.
func FindAdmissionCheck(checks []kueue.AdmissionCheckState, checkName kueue.AdmissionCheckReference) *kueue.AdmissionCheckState {
	for i := range checks {
		if checks[i].Name == checkName {
			return &checks[i]
		}
	}

	return nil
}

// resetChecksOnEviction sets all AdmissionChecks to Pending
func resetChecksOnEviction(w *kueue.Workload, now time.Time) {
	checks := w.Status.AdmissionChecks
	for i := range checks {
		if checks[i].State == kueue.CheckStatePending {
			continue
		}
		checks[i] = kueue.AdmissionCheckState{
			Name:               checks[i].Name,
			State:              kueue.CheckStatePending,
			LastTransitionTime: metav1.NewTime(now),
			Message:            "Reset to Pending after eviction. Previously: " + string(checks[i].State),
		}
	}
}

// SetAdmissionCheckState - adds or updates newCheck in the provided checks list.
func SetAdmissionCheckState(checks *[]kueue.AdmissionCheckState, newCheck kueue.AdmissionCheckState, clock clock.Clock) {
	if checks == nil {
		return
	}
	existingCondition := FindAdmissionCheck(*checks, newCheck.Name)
	if existingCondition == nil {
		if newCheck.LastTransitionTime.IsZero() {
			newCheck.LastTransitionTime = metav1.NewTime(clock.Now())
		}
		*checks = append(*checks, newCheck)
		return
	}

	if existingCondition.State != newCheck.State {
		existingCondition.State = newCheck.State
		if !newCheck.LastTransitionTime.IsZero() {
			existingCondition.LastTransitionTime = newCheck.LastTransitionTime
		} else {
			existingCondition.LastTransitionTime = metav1.NewTime(clock.Now())
		}
	}
	existingCondition.Message = newCheck.Message
	existingCondition.PodSetUpdates = newCheck.PodSetUpdates
}

// RejectedChecks returns the list of Rejected admission checks
func RejectedChecks(wl *kueue.Workload) []kueue.AdmissionCheckState {
	rejectedChecks := make([]kueue.AdmissionCheckState, 0, len(wl.Status.AdmissionChecks))
	for i := range wl.Status.AdmissionChecks {
		ac := wl.Status.AdmissionChecks[i]
		if ac.State == kueue.CheckStateRejected {
			rejectedChecks = append(rejectedChecks, ac)
		}
	}
	return rejectedChecks
}

// HasAllChecksReady returns true if all the checks of the workload are ready.
func HasAllChecksReady(wl *kueue.Workload) bool {
	for i := range wl.Status.AdmissionChecks {
		if wl.Status.AdmissionChecks[i].State != kueue.CheckStateReady {
			return false
		}
	}
	return true
}

// HasAllChecks returns true if all the mustHaveChecks are present in the workload.
func HasAllChecks(wl *kueue.Workload, mustHaveChecks sets.Set[kueue.AdmissionCheckReference]) bool {
	if mustHaveChecks.Len() == 0 {
		return true
	}

	if mustHaveChecks.Len() > len(wl.Status.AdmissionChecks) {
		return false
	}

	mustHaveChecks = mustHaveChecks.Clone()
	for i := range wl.Status.AdmissionChecks {
		mustHaveChecks.Delete(wl.Status.AdmissionChecks[i].Name)
	}
	return mustHaveChecks.Len() == 0
}

// HasRetryChecks returns true if any of the workloads checks is Retry
func HasRetryChecks(wl *kueue.Workload) bool {
	for i := range wl.Status.AdmissionChecks {
		state := wl.Status.AdmissionChecks[i].State
		if state == kueue.CheckStateRetry {
			return true
		}
	}
	return false
}

// HasRejectedChecks returns true if any of the workloads checks is Rejected
func HasRejectedChecks(wl *kueue.Workload) bool {
	for i := range wl.Status.AdmissionChecks {
		state := wl.Status.AdmissionChecks[i].State
		if state == kueue.CheckStateRejected {
			return true
		}
	}
	return false
}
