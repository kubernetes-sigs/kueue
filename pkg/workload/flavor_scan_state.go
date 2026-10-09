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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
)

type FlavorScanState struct {
	// TriedFlavors records the set of flavors already tried in the current scan pass
	// for each PodSet and resource. An empty/nil set means that pair's scan reached
	// the end (or has not started), so its next attempt considers all flavors.
	TriedFlavors []map[corev1.ResourceName]sets.Set[kueue.ResourceFlavorReference]

	// AllocatableResourceGeneration records the ClusterQueue's allocatable resource
	// generation at the time of the scan, used to check whether the scan progress is stale.
	AllocatableResourceGeneration int64

	// SchedulingCycle is the scheduling cycle that computed this assignment. It lets the
	// assignment from the immediately preceding cycle be treated as current even when the
	// ClusterQueue generation has moved on.
	SchedulingCycle int64

	// SchedulingHash is the scheduling equivalence hash of the Workload this assignment was
	// computed for. TriedFlavors is indexed by PodSet and holds flavors tried
	// for a particular set of requests, so it only carries meaning while the Workload keeps
	// the shape it had then.
	SchedulingHash EquivalenceHash
}

func (s *FlavorScanState) Clone() *FlavorScanState {
	c := FlavorScanState{
		TriedFlavors:                  make([]map[corev1.ResourceName]sets.Set[kueue.ResourceFlavorReference], len(s.TriedFlavors)),
		AllocatableResourceGeneration: s.AllocatableResourceGeneration,
		SchedulingCycle:               s.SchedulingCycle,
		SchedulingHash:                s.SchedulingHash,
	}
	for ps, resFlavors := range s.TriedFlavors {
		if resFlavors != nil {
			c.TriedFlavors[ps] = utilmaps.DeepCopySets(resFlavors)
		}
	}
	return &c
}

// MatchesSchedulingShape reports whether this assignment was computed for the scheduling
// shape the Workload has now. A change to the PodSets or their requests makes the recorded
// flavors describe a search that no longer applies: resuming from them could skip a
// flavor the changed Workload would now fit.
//
// An unknown hash on either side means SchedulingEquivalenceHashing is disabled and there is
// nothing to compare, so the shape is taken to be unchanged. That keeps the two features
// independent, and TriedFlavorsForPodSetResource still bounds-checks the PodSet index.
func (s *FlavorScanState) MatchesSchedulingShape(current EquivalenceHash) bool {
	if s.SchedulingHash == SchedulingHashUnknown || current == SchedulingHashUnknown {
		return true
	}
	return s.SchedulingHash == current
}

// PendingFlavors returns whether there are pending flavors to try
// after the last attempt.
func (s *FlavorScanState) PendingFlavors() bool {
	if s == nil {
		// This is only reached in unit tests.
		return false
	}
	for _, podSetFlavors := range s.TriedFlavors {
		for _, flavors := range podSetFlavors {
			if len(flavors) > 0 {
				return true
			}
		}
	}
	return false
}

func (s *FlavorScanState) TriedFlavorsForPodSetResource(
	ps int,
	res corev1.ResourceName,
) sets.Set[kueue.ResourceFlavorReference] {
	if !features.Enabled(features.FlavorFungibility) {
		return nil
	}
	if s == nil || ps >= len(s.TriedFlavors) {
		return nil
	}
	return s.TriedFlavors[ps][res]
}

func (s *FlavorScanState) TriedFlavorsForGroup(
	psIDs []int,
	resName corev1.ResourceName,
	currentFlavors []kueue.ResourceFlavorReference,
) sets.Set[kueue.ResourceFlavorReference] {
	for _, psID := range psIDs {
		tried := s.TriedFlavorsForPodSetResource(psID, resName)
		if len(tried) == 0 {
			continue
		}
		if !tried.HasAll(currentFlavors...) {
			return tried.Clone()
		}
		break
	}
	return sets.New[kueue.ResourceFlavorReference]()
}

func (s *FlavorScanState) RestoreAfterRecompute(previous FlavorScanState) {
	for psID, recomputed := range s.TriedFlavors {
		for resName, tried := range recomputed {
			if len(tried) > 0 {
				recomputed[resName] =
					previous.TriedFlavorsForPodSetResource(psID, resName).Clone()
			}
		}
	}
}

func (s *FlavorScanState) RecordPodSet(
	psID int,
	progress map[corev1.ResourceName]sets.Set[kueue.ResourceFlavorReference],
) {
	if missing := psID + 1 - len(s.TriedFlavors); missing > 0 {
		s.TriedFlavors = append(s.TriedFlavors, make([]map[corev1.ResourceName]sets.Set[kueue.ResourceFlavorReference], missing)...)
	}
	s.TriedFlavors[psID] = progress
}
