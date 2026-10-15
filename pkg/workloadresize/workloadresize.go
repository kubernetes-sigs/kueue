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

// Package workloadresize contains definitions to support in-place elastic
// scaling of admitted workloads (ElasticJobsViaWorkloadResize feature gate).
//
// This package is orthogonal to workloadslicing: it does NOT create new
// Workload objects. Instead it mutates spec.podSets[].count of the existing
// admitted Workload and lets the scheduler grant or release quota in-place.
package workloadresize

import (
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
)

// Enabled returns true if a provided object carries the resize-elastic
// annotation and the ElasticJobsViaWorkloadResize feature gate is on.
// It uses a different annotation from workloadslicing so the two gates are
// fully orthogonal.
func Enabled(object metav1.Object) bool {
	if object == nil {
		return false
	}
	return features.Enabled(features.ElasticJobsViaWorkloadResize) &&
		object.GetAnnotations()[constants.ResizeElasticJobAnnotation] == "true"
}

// IsScaleUp returns true when the workload is admitted and its spec count for
// at least one PodSet is greater than the currently-admitted count.
func IsScaleUp(wl *kueue.Workload) bool {
	return hasScaleDelta(wl, true)
}

// IsScaleDown returns true when the workload is admitted and its spec count
// for at least one PodSet is less than the currently-admitted count.
func IsScaleDown(wl *kueue.Workload) bool {
	return hasScaleDelta(wl, false)
}

// hasScaleDelta returns true when the workload is admitted and at least one
// PodSet's spec count diverges from the admitted count in the given direction
// (up=true for scale-up, up=false for scale-down).
func hasScaleDelta(wl *kueue.Workload, up bool) bool {
	if wl == nil || wl.Status.Admission == nil {
		return false
	}
	if !Enabled(wl) {
		return false
	}
	admittedCounts := AdmittedCounts(wl)
	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		admitted, ok := admittedCounts[ps.Name]
		if !ok {
			continue
		}
		if up && ps.Count > admitted {
			return true
		}
		if !up && ps.Count < admitted {
			return true
		}
	}
	return false
}

// AdmittedCounts returns a map from PodSet name to the admitted replica count
// as recorded in status.admission.podSetAssignments.
func AdmittedCounts(wl *kueue.Workload) map[kueue.PodSetReference]int32 {
	if wl == nil || wl.Status.Admission == nil {
		return nil
	}
	result := make(map[kueue.PodSetReference]int32, len(wl.Status.Admission.PodSetAssignments))
	for _, psa := range wl.Status.Admission.PodSetAssignments {
		result[psa.Name] = ptr.Deref(psa.Count, 0)
	}
	return result
}

// SetScaleDownCondition sets the WorkloadScaleDown condition on the workload.
func SetScaleDownCondition(wl *kueue.Workload, generation int64, reason, message string, clock clock.Clock) bool {
	return apimeta.SetStatusCondition(&wl.Status.Conditions, metav1.Condition{
		Type:               kueue.WorkloadScaleDown,
		Status:             metav1.ConditionTrue,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
		LastTransitionTime: metav1.NewTime(clock.Now()),
	})
}
