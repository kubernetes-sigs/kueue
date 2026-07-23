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

package testing

import (
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// AdjustConditionsForDisabledObservabilityInWorkloadController adjusts a slice of workload status conditions
// to match the scenario when the UnadmittedWorkloadsObservability feature gate is disabled,
// specifically from the perspective of the workload controller.
func AdjustConditionsForDisabledObservabilityInWorkloadController(conditions []metav1.Condition, wasAdmitted bool) []metav1.Condition {
	var filtered []metav1.Condition
	for _, cond := range conditions {
		if cond.Type == kueue.WorkloadAdmitted && cond.Status == metav1.ConditionFalse {
			if !wasAdmitted {
				// In legacy Kueue, if the workload was never admitted, we never write Admitted: False.
				continue
			}
			if cond.Reason == kueue.WorkloadAdmittedReasonNoReservation || cond.Reason == "NoReservationUnsatisfiedChecks" {
				continue
			}
			if cond.Reason == kueue.WorkloadAdmittedReasonUnsatisfiedAdmissionChecks {
				cond.Reason = "UnsatisfiedChecks"
			}
		}
		if cond.Type == kueue.WorkloadQuotaReserved && cond.Status == metav1.ConditionFalse {
			switch cond.Reason {
			case kueue.WorkloadQuotaReservedReasonWaitingForPodsReady:
				cond.Reason = kueue.WorkloadWaiting //nolint:staticcheck // SA1019: legacy reason
			case kueue.WorkloadAdmissionGated:
				// Keep as is
			case kueue.WorkloadQuotaReservedReasonMisconfigured,
				kueue.WorkloadQuotaReservedReasonSuspended,
				kueue.WorkloadInadmissible:
				cond.Reason = kueue.WorkloadInadmissible
			default:
				cond.Reason = kueue.WorkloadPending //nolint:staticcheck // SA1019: legacy reason
			}
		}
		filtered = append(filtered, cond)
	}
	return filtered
}

// AdjustWorkloadsForDisabledObservabilityInWorkloadController adjusts the workload status conditions in-place for
// a slice of workloads, matching the scenario when the UnadmittedWorkloadsObservability
// feature gate is disabled. It delegates the condition adjustments to AdjustConditionsForDisabledObservabilityInWorkloadController.
func AdjustWorkloadsForDisabledObservabilityInWorkloadController(workloads []kueue.Workload) {
	for i := range workloads {
		wl := &workloads[i]
		wasAdmitted := false
		if cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadAdmitted); cond != nil {
			if cond.Status == metav1.ConditionTrue {
				wasAdmitted = true
			} else if cond.Reason != kueue.WorkloadAdmittedReasonNoReservation &&
				cond.Reason != "NoReservationUnsatisfiedChecks" {
				wasAdmitted = true
			}
		}
		if !wasAdmitted && (apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadEvicted) ||
			apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadFinished)) {
			wasAdmitted = true
		}
		wl.Status.Conditions = AdjustConditionsForDisabledObservabilityInWorkloadController(wl.Status.Conditions, wasAdmitted)
	}
}

// AdjustConditionsForDisabledObservabilityInScheduler adjusts a slice of workload status conditions
// to match the scenario when the UnadmittedWorkloadsObservability feature gate is disabled,
// specifically from the perspective of the scheduler.
func AdjustConditionsForDisabledObservabilityInScheduler(conditions []metav1.Condition) []metav1.Condition {
	var filtered []metav1.Condition
	for _, cond := range conditions {
		if cond.Type == kueue.WorkloadAdmitted && cond.Status == metav1.ConditionFalse {
			// The scheduler never writes or manages Admitted: False.
			continue
		}
		if cond.Type == kueue.WorkloadQuotaReserved && cond.Status == metav1.ConditionFalse {
			switch cond.Reason {
			case kueue.WorkloadQuotaReservedReasonWaitingForPodsReady:
				cond.Reason = kueue.WorkloadWaiting //nolint:staticcheck // SA1019: legacy reason
			default:
				cond.Reason = kueue.WorkloadPending //nolint:staticcheck // SA1019: legacy reason
			}
		}
		filtered = append(filtered, cond)
	}
	return filtered
}

// AdjustWorkloadsForDisabledObservabilityInScheduler adjusts the workload status conditions in-place for
// a slice of workloads, matching the scenario when the UnadmittedWorkloadsObservability
// feature gate is disabled. It delegates the condition adjustments to AdjustConditionsForDisabledObservabilityInScheduler.
func AdjustWorkloadsForDisabledObservabilityInScheduler(workloads []kueue.Workload) {
	for i := range workloads {
		wl := &workloads[i]
		wl.Status.Conditions = AdjustConditionsForDisabledObservabilityInScheduler(wl.Status.Conditions)
	}
}

// AdjustEventsForDisabledObservabilityInScheduler adjusts the event reasons in-place for
// a slice of events, matching the scenario when the UnadmittedWorkloadsObservability
// feature gate is disabled.
func AdjustEventsForDisabledObservabilityInScheduler(events []EventRecord) {
	for i := range events {
		if events[i].EventType == corev1.EventTypeWarning {
			switch events[i].Reason {
			case kueue.WorkloadQuotaReservedReasonWaitingForPodsReady:
				events[i].Reason = kueue.WorkloadWaiting //nolint:staticcheck // SA1019: legacy reason
			case kueue.WorkloadAdmissionGated, "SecondPassFailed", "DeprecatedPathUsage", "FailedCreate", "ErrWorkloadCompose", "JobNestingTooDeep":
				// Keep warning events that are not related to QuotaReserved=False unadmitted reasons
			default:
				events[i].Reason = kueue.WorkloadPending //nolint:staticcheck // SA1019: legacy reason
			}
		}
	}
}
