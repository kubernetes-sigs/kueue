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

package preemptioncommon

import (
	"time"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/workload"
)

const timestampPreemptionBuffer = 5 * time.Minute

// TimeBasedPreemptionOpts carries the config needed for time-based
// preemption protection. Pass nil for cross-CQ preemption calls.
type TimeBasedPreemptionOpts struct {
	WithinCQConfig *kueue.WithinClusterQueueConfig
	Now            time.Time
}

// isTimeProtected checks if a preemption candidate is within its time-based
// protection window. Returns true if the candidate should be protected from
// preemption. Returns false if no protection applies.
//
// A candidate is classified as "opportunistic" if the preemptor was queued
// before the candidate was admitted (i.e., the candidate jumped ahead).
// Otherwise, the candidate is considered "incumbent" (queued and admitted
// before the preemptor arrived).
//
// Opportunistic candidates are checked against OpportunisticMinAdmitDurationSeconds,
// while incumbent candidates are checked against MinAdmitDurationSeconds. If the
// applicable duration is not configured, no protection is applied.
func isTimeProtected(preemptor, candidate *kueue.Workload, workloadOrdering workload.Ordering, opts *TimeBasedPreemptionOpts) bool {
	if opts == nil || opts.WithinCQConfig == nil || !features.Enabled(features.PreemptionGuaranteedRuntimeWithinCQ) {
		return false
	}
	candidateAdmitTime := quotaReservationTime(candidate, opts.Now)
	candidateRunTime := opts.Now.Sub(candidateAdmitTime)
	preemptorTS := workloadOrdering.GetQueueOrderTimestamp(preemptor)
	isOpportunistic := preemptorTS.Time.Before(candidateAdmitTime)

	if isOpportunistic {
		if s := opts.WithinCQConfig.OpportunisticMinAdmitDurationSeconds; s != nil {
			return candidateRunTime <= time.Duration(*s)*time.Second
		}
	} else {
		if s := opts.WithinCQConfig.MinAdmitDurationSeconds; s != nil {
			return candidateRunTime <= time.Duration(*s)*time.Second
		}
	}
	return false
}

func SatisfiesPreemptionPolicy(preemptor, candidate *kueue.Workload, workloadOrdering workload.Ordering, policy kueue.PreemptionPolicy, opts *TimeBasedPreemptionOpts) bool {
	preemptorPriority := priority.Priority(preemptor)
	candidatePriority := priority.Priority(candidate)

	lowerPriority := preemptorPriority > candidatePriority
	if policy == kueue.PreemptionPolicyLowerPriority {
		if lowerPriority {
			return !isTimeProtected(preemptor, candidate, workloadOrdering, opts)
		}
		return false
	}
	if policy == kueue.PreemptionPolicyLowerOrNewerEqualPriority {
		if preemptorPriority < candidatePriority {
			return false
		}
		preemptorTS := workloadOrdering.GetQueueOrderTimestamp(preemptor)
		candidateTS := workloadOrdering.GetQueueOrderTimestamp(candidate)

		newerEqualPriority := (preemptorPriority == candidatePriority) && preemptorTS.Before(candidateTS)
		if newerEqualPriority && features.Enabled(features.SchedulerTimestampPreemptionBuffer) {
			newerEqualPriority = candidateTS.Sub(preemptorTS.Time) > timestampPreemptionBuffer
		}
		if lowerPriority || newerEqualPriority {
			return !isTimeProtected(preemptor, candidate, workloadOrdering, opts)
		}
		return false
	}
	return policy == kueue.PreemptionPolicyAny
}
