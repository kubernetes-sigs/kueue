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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// AdjustConditionsForDisabledObservability adjusts a slice of workload status conditions
// to match the scenario when the UnadmittedWorkloadsObservability feature gate is disabled.
// Specifically, it performs the following modifications:
//  1. For queueing workloads (not evicted/preempted), it removes the false 'Admitted' condition
//     entirely, as legacy Kueue left it absent during initial queueing.
//  2. For evicted/preempted workloads, it keeps the false 'Admitted' condition but rolls back
//     its reason from 'UnsatisfiedAdmissionChecks' to the legacy 'UnsatisfiedChecks'.
//  3. Removes the false 'Admitted' condition if its reason is 'NoReservation'.
//  4. Rolls back the reason of the 'QuotaReserved: False' condition to the generic "Pending".
func AdjustConditionsForDisabledObservability(conditions []metav1.Condition) []metav1.Condition {
	isEvicted := false
	for _, cond := range conditions {
		if (cond.Type == kueue.WorkloadEvicted || cond.Type == kueue.WorkloadPreempted) && cond.Status == metav1.ConditionTrue {
			isEvicted = true
			break
		}
	}

	var filtered []metav1.Condition
	for _, cond := range conditions {
		if cond.Type == kueue.WorkloadAdmitted && cond.Status == metav1.ConditionFalse {
			if !isEvicted {
				// In the queueing case, legacy Kueue never sets Admitted: False.
				continue
			}
			if cond.Reason == kueue.WorkloadAdmittedReasonNoReservation {
				continue
			}
			if cond.Reason == kueue.WorkloadAdmittedReasonUnsatisfiedAdmissionChecks {
				cond.Reason = "UnsatisfiedChecks"
			}
		}
		if cond.Type == kueue.WorkloadQuotaReserved && cond.Status == metav1.ConditionFalse {
			cond.Reason = "Pending"
		}
		filtered = append(filtered, cond)
	}
	return filtered
}

// AdjustWorkloadsForDisabledObservability adjusts the workload status conditions in-place for
// a slice of workloads, matching the scenario when the UnadmittedWorkloadsObservability
// feature gate is disabled. It delegates the condition adjustments to AdjustConditionsForDisabledObservability.
func AdjustWorkloadsForDisabledObservability(workloads []kueue.Workload) {
	for i := range workloads {
		wl := &workloads[i]
		wl.Status.Conditions = AdjustConditionsForDisabledObservability(wl.Status.Conditions)
	}
}
