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
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/util/wait"
)

// SyncAdmittedCondition sync the state of the Admitted condition based on the
// state of QuotaReserved, AdmissionChecks and DelayedTopologyRequests.
// Return true if any change was done.
func SyncAdmittedCondition(w *kueue.Workload, now time.Time) bool {
	hasReservation := HasQuotaReservation(w)
	hasAllChecksReady := HasAllChecksReady(w)
	isFinished := IsFinished(w)
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
		LastTransitionTime: metav1.NewTime(now),
	}
	switch {
	case isFinished:
		newCondition.Status = metav1.ConditionFalse
		newCondition.Reason = kueue.WorkloadFinished
		newCondition.Message = "Workload has finished"
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
		// in practice the oldCondition cannot be nil; however, we should try to avoid nil ptr deref.
		if oldCondition != nil {
			d := int32(now.Sub(oldCondition.LastTransitionTime.Time).Seconds())
			if w.Status.AccumulatedPastExecutionTimeSeconds != nil {
				*w.Status.AccumulatedPastExecutionTimeSeconds += d
			} else {
				w.Status.AccumulatedPastExecutionTimeSeconds = &d
			}
		}
	}
	return apimeta.SetStatusCondition(&w.Status.Conditions, newCondition)
}

// resetChecksOnEviction sets all AdmissionChecks to Pending
func resetChecksOnEviction(w *kueue.Workload, now time.Time) {
	checks := w.Status.AdmissionChecks
	for i := range checks {
		if checks[i].State == kueue.CheckStatePending {
			continue
		}
		var retryCount *int32
		if checks[i].State == kueue.CheckStateRetry {
			tmpRetryCount := ptr.Deref(checks[i].RetryCount, 0) + 1
			retryCount = ptr.To(tmpRetryCount)
		}
		checks[i] = kueue.AdmissionCheckState{
			Name:                checks[i].Name,
			State:               kueue.CheckStatePending,
			Message:             "Reset to Pending after eviction. Previously: " + string(checks[i].State),
			LastTransitionTime:  metav1.NewTime(now),
			RequeueAfterSeconds: checks[i].RequeueAfterSeconds,
			RetryCount:          retryCount,
		}
	}
}

// SetAdmissionCheckState - adds or updates newCheck in the provided checks list.
func SetAdmissionCheckState(checks *[]kueue.AdmissionCheckState, newCheck kueue.AdmissionCheckState, clock clock.Clock) bool {
	if checks == nil {
		return false
	}
	existingCondition := admissioncheck.FindAdmissionCheck(*checks, newCheck.Name)
	if existingCondition == nil {
		if newCheck.LastTransitionTime.IsZero() {
			newCheck.LastTransitionTime = metav1.NewTime(clock.Now())
		}
		*checks = append(*checks, newCheck)
		return true
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
	existingCondition.RequeueAfterSeconds = newCheck.RequeueAfterSeconds
	return true
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

// GetMaxRetryTime returns the max retry time from all the admission checks.
// It will return a zero time if no retry time is found.
func GetMaxRetryTime(wl *kueue.Workload) metav1.Time {
	var max time.Time
	for i := range wl.Status.AdmissionChecks {
		if wl.Status.AdmissionChecks[i].RequeueAfterSeconds == nil {
			continue
		}
		// This should never happen, but prevents panics
		if wl.Status.AdmissionChecks[i].LastTransitionTime.IsZero() {
			continue
		}
		if wl.Status.AdmissionChecks[i].State != kueue.CheckStateRetry {
			continue
		}

		retryTime := wl.Status.AdmissionChecks[i].LastTransitionTime.Add(time.Duration(*wl.Status.AdmissionChecks[i].RequeueAfterSeconds) * time.Second)
		if max.Before(retryTime) {
			max = retryTime
		}
	}
	return metav1.NewTime(max)
}

// NeedsRequeueAtUpdate checks if the workload needs its RequeueAt time updated
// based on admission check retry times. It returns the target requeue time if an
// update should be performed, or nil if no update is needed.
func NeedsRequeueAtUpdate(wl *kueue.Workload, clock clock.Clock) *metav1.Time {
	maxTime := GetMaxRetryTime(wl)
	// No retry time set
	if maxTime.IsZero() {
		return nil
	}
	// Retry time is in the past
	if !maxTime.After(clock.Now()) {
		return nil
	}
	// Check if we need to update RequeueState
	if wl.Status.RequeueState != nil {
		currentRequeueAt := ptr.Deref(wl.Status.RequeueState.RequeueAt, metav1.NewTime(time.Time{}))
		if !currentRequeueAt.Before(&maxTime) || currentRequeueAt.Equal(&maxTime) {
			// Current time is already >= maxTime, no update needed
			return nil
		}
	}
	return &maxTime
}

// UpdateAdmissionCheckRequeueState calculates the RequeueAfterSeconds based on the backoff and requeuingCount
func UpdateAdmissionCheckRequeueState(acState *kueue.AdmissionCheckState, backoffBaseSeconds int32, backoffMaxSeconds int32, clock clock.Clock) {
	requeuingCount := ptr.Deref(acState.RetryCount, 0) + 1

	// Every backoff duration is about "60s*2^(n-1)+Rand" where:
	// - "n" represents the "requeuingCount",
	// - "Rand" represents the random jitter.
	// During this time, the workload is treated as inadmissible and other
	// workloads will have a chance to be admitted.
	backoff := wait.NewBackoff(time.Duration(backoffBaseSeconds)*time.Second, time.Duration(backoffMaxSeconds)*time.Second, 2, 0.0001)
	waitDuration := backoff.WaitTime(int(requeuingCount))

	acState.RequeueAfterSeconds = ptr.To(int32(waitDuration.Truncate(time.Second).Seconds()))
}
