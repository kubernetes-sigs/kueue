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

package limits

import (
	"maps"
	"time"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/policy"
)

// EventsByLimit maps each PreemptionLimit to its recorded timestamps by aggregation key.
type EventsByLimit = map[policy.PreemptionLimitReference]AggregationEvents

// NewTrackerForTest returns a PreemptionLimitTracker with preemptionLimits
// synced and any provided events pre-populated.
func NewTrackerForTest(preemptionLimits []kueuealpha.PreemptionLimit, eventsByLimit EventsByLimit) *PreemptionLimitTracker {
	tracker := NewPreemptionLimitTracker()
	for i := range preemptionLimits {
		ref := policy.PreemptionLimitReference(preemptionLimits[i].Name)
		ls := newLimitState(&preemptionLimits[i])
		if limitEvents, ok := eventsByLimit[ref]; ok {
			maps.Copy(ls.eventsByAggregationKey, limitEvents)
		}
		tracker.limits[ref] = ls
	}
	return tracker
}

// SnapshotForTest returns a PreemptionLimitsSnapshot at now of a tracker
// populated with preemptionLimits and eventsByLimit.
func SnapshotForTest(now time.Time, preemptionLimits []kueuealpha.PreemptionLimit, eventsByLimit EventsByLimit) *PreemptionLimitsSnapshot {
	return NewTrackerForTest(preemptionLimits, eventsByLimit).Snapshot(now)
}
