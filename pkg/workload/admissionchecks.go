/*
Copyright 2023 The Kubernetes Authors.

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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

// SyncAdmittedCondition sync the state of the Admitted condition
// with the state of QuotaReserved and AdmissionChecks.
// Return true if any change was done.
func SyncAdmittedCondition(w *kueue.Workload) bool {
	hasReservation := HasQuotaReservation(w)
	hasAllChecksReady := HasAllChecksReady(w)
	isAdmitted := IsAdmitted(w)

	if isAdmitted == (hasReservation && hasAllChecksReady) {
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
		newCondition.Reason = "NoReservationNoChecks"
		newCondition.Message = "The workload has no reservation and not all checks ready"
	case !hasReservation:
		newCondition.Status = metav1.ConditionFalse
		newCondition.Reason = "NoReservation"
		newCondition.Message = "The workload has no reservation"
	case !hasAllChecksReady:
		newCondition.Status = metav1.ConditionFalse
		newCondition.Reason = "NoChecks"
		newCondition.Message = "The workload has not all checks ready"
	}

	return apimeta.SetStatusCondition(&w.Status.Conditions, newCondition)
}

// FindAdmissionCheck - returns a pointer to the check identified by checkName if found in checks.
func FindAdmissionCheck(checks []kueue.AdmissionCheckState, checkName string) *kueue.AdmissionCheckState {
	for i := range checks {
		if checks[i].Name == checkName {
			return &checks[i]
		}
	}

	return nil
}

// SetAdmissionCheckState - adds or updates newCheck in the provided checks list.
func SetAdmissionCheckState(checks *[]kueue.AdmissionCheckState, newCheck kueue.AdmissionCheckState) {
	if checks == nil {
		return
	}
	existingCondition := FindAdmissionCheck(*checks, newCheck.Name)
	if existingCondition == nil {
		if newCheck.LastTransitionTime.IsZero() {
			newCheck.LastTransitionTime = metav1.NewTime(time.Now())
		}
		*checks = append(*checks, newCheck)
		return
	}

	if existingCondition.State != newCheck.State {
		existingCondition.State = newCheck.State
		if !newCheck.LastTransitionTime.IsZero() {
			existingCondition.LastTransitionTime = newCheck.LastTransitionTime
		} else {
			existingCondition.LastTransitionTime = metav1.NewTime(time.Now())
		}
	}
	existingCondition.Message = newCheck.Message
	existingCondition.PodSetUpdates = newCheck.PodSetUpdates
}

// GetRejectedChecks returns the list of Rejected admission checks
func GetRejectedChecks(wl *kueue.Workload) []string {
	rejectedChecks := make([]string, 0, len(wl.Status.AdmissionChecks))
	for i := range wl.Status.AdmissionChecks {
		ac := wl.Status.AdmissionChecks[i]
		if ac.State == kueue.CheckStateRejected {
			rejectedChecks = append(rejectedChecks, ac.Name)
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
func HasAllChecks(wl *kueue.Workload, mustHaveChecks sets.Set[string]) bool {
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

// HasRetryOrRejectedChecks returns true if any of the workloads checks are Retry or Rejected
func HasRetryOrRejectedChecks(wl *kueue.Workload) bool {
	for i := range wl.Status.AdmissionChecks {
		state := wl.Status.AdmissionChecks[i].State
		if state == kueue.CheckStateRetry || state == kueue.CheckStateRejected {
			return true
		}
	}
	return false
}
