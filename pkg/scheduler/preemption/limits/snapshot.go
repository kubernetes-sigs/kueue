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
	"time"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/policy"
)

// preemptionCounts holds a number of preemptions per PreemptionLimit and aggregation key.
// A missing entry counts as 0.
type preemptionCounts map[policy.PreemptionLimitReference]policy.PreemptionLimitAggregationCounts

// add adds n preemptions for limit and aggregationKey and returns the new count.
func (c preemptionCounts) add(limit policy.PreemptionLimitReference, aggregationKey policy.PreemptionLimitAggregationKey, n int32) int32 {
	if c[limit] == nil {
		c[limit] = make(policy.PreemptionLimitAggregationCounts)
	}
	c[limit][aggregationKey] += n
	return c[limit][aggregationKey]
}

// PreemptionLimitsSnapshot captures PreemptionLimits and the current number of
// preemptions against them at a single point in time. It allows to reason about
// the current limits state in one scheduling cycle without danger of concurrent
// modification of PreemptionLimits or currently tracked preemptions, caused by
// user actions (e.g. delete of a limit resource).
//
// Moreover, snapshot allows to update the underlying captured state via Record
// method, to take into account additional preemptions that were issued after it
// was captured, for example to track preemptions already issued in the same
// scheduling cycle.
//
// PreemptionLimitsSnapshot is not safe for concurrent use, and expects
// ConsumedLimits resolved against the PreemptionLimits it captured.
type PreemptionLimitsSnapshot struct {
	limitByName map[policy.PreemptionLimitReference]*kueuealpha.PreemptionLimit
	// recorded holds the preemptions recorded within the window of each limit when
	// the snapshot was taken, plus ones added with `Record` method.
	recorded preemptionCounts
	// earliestEvent holds the earliest timestamp among the recorded preemptions
	// of each limit and aggregation key.
	earliestEvent map[policy.PreemptionLimitReference]map[policy.PreemptionLimitAggregationKey]time.Time
}

// Record updates the snapshot by recording a preemption at ts for every
// PreemptionLimit and aggregation key in consumedLimits.
func (s *PreemptionLimitsSnapshot) Record(ts time.Time, consumedLimits policy.ConsumedLimits) {
	for limit, consumed := range consumedLimits {
		s.record(limit, consumed.AggregationKey, 1, ts)
	}
}

func (s *PreemptionLimitsSnapshot) record(
	limit policy.PreemptionLimitReference,
	aggregationKey policy.PreemptionLimitAggregationKey,
	count int32,
	earliest time.Time,
) {
	s.recorded.add(limit, aggregationKey, count)
	keys, found := s.earliestEvent[limit]
	if !found {
		keys = make(map[policy.PreemptionLimitAggregationKey]time.Time)
		s.earliestEvent[limit] = keys
	}
	if existing, found := keys[aggregationKey]; !found || earliest.Before(existing) {
		keys[aggregationKey] = earliest
	}
}

// WithinLimits reports whether preemptions fit together within the remaining
// limits in the snapshot. A nil PreemptionLimitsSnapshot allows no preemptions.
func (s *PreemptionLimitsSnapshot) WithinLimits(preemptions []policy.ConsumedLimits) bool {
	if s == nil {
		return len(preemptions) == 0
	}
	taken := preemptionCounts{}
	for _, consumedLimits := range preemptions {
		for limit, consumed := range consumedLimits {
			if taken.add(limit, consumed.AggregationKey, 1) > s.allowance(limit, consumed.AggregationKey) {
				return false
			}
		}
	}
	return true
}

// aggregation returns the aggregation of limit in the snapshot, or an empty string if limit
// is not in the snapshot.
func (s *PreemptionLimitsSnapshot) aggregation(limit policy.PreemptionLimitReference) kueuealpha.PreemptionLimitAggregation {
	preemptionLimit, found := s.limitByName[limit]
	if !found {
		return ""
	}
	return preemptionLimit.Spec.Aggregation
}

// allowance returns how many more preemptions are allowed for limit and
// aggregationKey in the snapshot, or 0 if limit is not in the snapshot.
func (s *PreemptionLimitsSnapshot) allowance(limit policy.PreemptionLimitReference, aggregationKey policy.PreemptionLimitAggregationKey) int32 {
	preemptionLimit, found := s.limitByName[limit]
	if !found {
		return 0
	}
	return max(0, preemptionLimit.Spec.Limit-s.recorded[limit][aggregationKey])
}

// nextTimeSlot returns the time when the earliest recorded preemption for limit
// and aggregationKey expires, or nil if none is recorded or limit allows no preemptions.
func (s *PreemptionLimitsSnapshot) nextTimeSlot(limit policy.PreemptionLimitReference, aggregationKey policy.PreemptionLimitAggregationKey) *time.Time {
	preemptionLimit, found := s.limitByName[limit]
	if !found || preemptionLimit.Spec.Limit <= 0 {
		return nil
	}
	earliest, found := s.earliestEvent[limit][aggregationKey]
	if !found {
		return nil
	}
	return new(earliest.Add(time.Duration(preemptionLimit.Spec.WindowSeconds) * time.Second))
}
