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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

func SatisfiesPreemptionPolicy(policy kueue.PreemptionPolicy, preemptorPriority int32, preemptorTimestamp *metav1.Time, candidatePriority int32, candidateTimestamp *metav1.Time) bool {
	lowerPriority := preemptorPriority > candidatePriority
	if policy == kueue.PreemptionPolicyLowerPriority {
		return lowerPriority
	}
	if policy == kueue.PreemptionPolicyLowerOrNewerEqualPriority {
		newerEqualPriority := (preemptorPriority == candidatePriority) && preemptorTimestamp.Before(candidateTimestamp)
		return lowerPriority || newerEqualPriority
	}
	return policy == kueue.PreemptionPolicyAny
}
