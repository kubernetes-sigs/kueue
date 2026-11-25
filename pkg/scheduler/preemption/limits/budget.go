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
	"k8s.io/klog/v2"

	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/policy"
	"sigs.k8s.io/kueue/pkg/workload"
)

// PreemptionLimitBudget counts the preemptions taken from the PreemptionLimits
// during one pass of candidate selection.
type PreemptionLimitBudget struct {
	limiter *PreemptionLimiter
	// taken holds the preemptions taken from this budget.
	taken preemptionCounts
}

// NewBudget returns an empty budget for one pass of candidate selection.
// A budget from a nil PreemptionLimiter imposes no limits.
func (l *PreemptionLimiter) NewBudget() *PreemptionLimitBudget {
	return &PreemptionLimitBudget{
		limiter: l,
		taken:   preemptionCounts{},
	}
}

// TryConsume takes one preemption from every limit that preempting target
// consumes, and returns those limits.
//
// If any limit has no budget left, it takes nothing and returns false along
// with the strictest BlockingPreemptionLimit across the exhausted limits on target.
func (b *PreemptionLimitBudget) TryConsume(
	preemptor, target *workload.Info,
	preemptorCQ, targetCQ *schdcache.ClusterQueueSnapshot,
) (policy.ConsumedLimits, *policy.BlockingPreemptionLimit, bool) {
	consumedLimits := b.limiter.consumedLimits(preemptor, target, preemptorCQ, targetCQ)
	if len(consumedLimits) == 0 {
		return nil, nil, true
	}
	var blockingLimit *policy.BlockingPreemptionLimit
	for limit, consumed := range consumedLimits {
		if b.remaining(limit, consumed.AggregationKey) > 0 {
			continue
		}
		slotTime := b.limiter.snapshot.nextTimeSlot(limit, consumed.AggregationKey)
		if slotTime == nil {
			blockingLimit = &policy.BlockingPreemptionLimit{
				Limit:       limit,
				Aggregation: b.limiter.snapshot.aggregation(limit),
			}
			b.logSkipped(target, blockingLimit, consumed.AggregationKey)
			return nil, blockingLimit, false
		}
		if blockingLimit == nil || slotTime.After(*blockingLimit.RetryAt) {
			blockingLimit = &policy.BlockingPreemptionLimit{
				Limit:       limit,
				Aggregation: b.limiter.snapshot.aggregation(limit),
				RetryAt:     slotTime,
			}
		}
	}
	if blockingLimit != nil {
		b.logSkipped(target, blockingLimit, consumedLimits[blockingLimit.Limit].AggregationKey)
		return nil, blockingLimit, false
	}
	for limit, consumed := range consumedLimits {
		b.taken.add(limit, consumed.AggregationKey, 1)
	}
	return consumedLimits, nil, true
}

func (b *PreemptionLimitBudget) logSkipped(
	target *workload.Info,
	blockingLimit *policy.BlockingPreemptionLimit,
	aggregationKey policy.PreemptionLimitAggregationKey,
) {
	log := b.limiter.log.V(5)
	if blockingLimit.RetryAt != nil {
		log = log.WithValues("nextTimeSlot", *blockingLimit.RetryAt)
	}
	log.Info("Skipping preemption candidate exceeding a PreemptionLimit",
		"candidate", klog.KObj(target.Obj),
		"preemptionLimit", blockingLimit.Limit,
		"aggregation", blockingLimit.Aggregation,
		"aggregationKey", aggregationKey,
	)
}

// remaining returns how many more preemptions can be taken from this budget for limit and aggregationKey.
func (b *PreemptionLimitBudget) remaining(limit policy.PreemptionLimitReference, aggregationKey policy.PreemptionLimitAggregationKey) int32 {
	return b.limiter.snapshot.allowance(limit, aggregationKey) - b.taken[limit][aggregationKey]
}
