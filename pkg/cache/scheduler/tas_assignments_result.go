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

package scheduler

import (
	"maps"
	"slices"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

type FailureInfo struct {
	// PodSetName indicates the name of the PodSet for which computing the
	// TAS assignment failed.
	PodSetName kueue.PodSetReference

	// Reason indicates the reason why computing the TAS assignment failed.
	Reason string

	// Flavor indicates the resource flavor associated with the failure.
	Flavor kueue.ResourceFlavorReference
}

type TASAssignmentsResult map[kueue.PodSetReference]tasPodSetAssignmentResult

// Failure returns the failure of the first failed PodSet by name. Sorting keeps
// the reported PodSet stable across scheduling cycles, as several PodSets can
// fail at once, e.g. a leader and its workers, or PodSets in different flavors.
func (r TASAssignmentsResult) Failure() *FailureInfo {
	for _, psName := range slices.Sorted(maps.Keys(r)) {
		psAssignment := r[psName]
		if psAssignment.FailureReason != "" {
			return &FailureInfo{
				PodSetName: psName,
				Reason:     psAssignment.FailureReason,
				Flavor:     psAssignment.Flavor,
			}
		}
	}
	return nil
}

func (r TASAssignmentsResult) Failures() (failures []*FailureInfo) {
	for psName, psAssignment := range r {
		if psAssignment.FailureReason != "" {
			failures = append(failures, &FailureInfo{
				PodSetName: psName,
				Reason:     psAssignment.FailureReason,
				Flavor:     psAssignment.Flavor,
			})
		}
	}
	return
}

type tasPodSetAssignmentResult struct {
	TopologyAssignment *utiltas.TopologyAssignment
	FailureReason      string
	Flavor             kueue.ResourceFlavorReference
}
