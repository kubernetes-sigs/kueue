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
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/workload"
)

func SatisfiesPreemptionPolicy(preemptor, candidate *kueue.Workload, workloadOrdering workload.Ordering, policy kueue.PreemptionPolicy) bool {
	preemptorPriority := priority.Priority(preemptor)
	candidatePriority := priority.Priority(candidate)

	lowerPriority := preemptorPriority > candidatePriority
	if policy == kueue.PreemptionPolicyLowerPriority {
		return lowerPriority
	}
	if policy == kueue.PreemptionPolicyLowerOrNewerEqualPriority {
		preemptorTS := workloadOrdering.GetQueueOrderTimestamp(preemptor)
		candidateTS := workloadOrdering.GetQueueOrderTimestamp(candidate)
		newerEqualPriority := (preemptorPriority == candidatePriority) && preemptorTS.Before(candidateTS)
		return lowerPriority || newerEqualPriority
	}
	return policy == kueue.PreemptionPolicyAny
}
