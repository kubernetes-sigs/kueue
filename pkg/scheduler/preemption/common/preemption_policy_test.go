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
	"testing"
	"time"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestSatisfiesPreemptionPolicy(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.PriorityBoost, true)

	now := time.Now()
	ordering := workload.Ordering{}

	tests := map[string]struct {
		preemptor *kueue.Workload
		candidate *kueue.Workload
		policy    kueue.PreemptionPolicy
		want      bool
	}{
		"LowerPriority: higher raw priority can preempt lower": {
			preemptor: utiltestingapi.MakeWorkload("preemptor", "ns").Priority(200).Obj(),
			candidate: utiltestingapi.MakeWorkload("candidate", "ns").Priority(100).Obj(),
			policy:    kueue.PreemptionPolicyLowerPriority,
			want:      true,
		},
		"LowerPriority: same raw priority cannot preempt": {
			preemptor: utiltestingapi.MakeWorkload("preemptor", "ns").Priority(100).Obj(),
			candidate: utiltestingapi.MakeWorkload("candidate", "ns").Priority(100).Obj(),
			policy:    kueue.PreemptionPolicyLowerPriority,
			want:      false,
		},
		"LowerPriority: boost makes low-priority immune to medium-priority preemption": {
			preemptor: utiltestingapi.MakeWorkload("preemptor", "ns").Priority(200).Obj(),
			candidate: utiltestingapi.MakeWorkload("candidate", "ns").Priority(100).
				Annotation(controllerconstants.PriorityBoostAnnotationKey, "150").Obj(),
			policy: kueue.PreemptionPolicyLowerPriority,
			want:   false,
		},
		"LowerPriority: boost on preemptor enables preemption of higher raw priority": {
			preemptor: utiltestingapi.MakeWorkload("preemptor", "ns").Priority(100).
				Annotation(controllerconstants.PriorityBoostAnnotationKey, "200").Obj(),
			candidate: utiltestingapi.MakeWorkload("candidate", "ns").Priority(200).Obj(),
			policy:    kueue.PreemptionPolicyLowerPriority,
			want:      true,
		},
		"LowerOrNewerEqualPriority: boost equalizes priorities, older preemptor can preempt newer candidate": {
			preemptor: utiltestingapi.MakeWorkload("preemptor", "ns").Priority(100).
				Annotation(controllerconstants.PriorityBoostAnnotationKey, "100").
				Creation(now).Obj(),
			candidate: utiltestingapi.MakeWorkload("candidate", "ns").Priority(200).
				Creation(now.Add(time.Second)).Obj(),
			policy: kueue.PreemptionPolicyLowerOrNewerEqualPriority,
			want:   true,
		},
		"LowerOrNewerEqualPriority: equal effective priority, newer preemptor cannot preempt older candidate": {
			preemptor: utiltestingapi.MakeWorkload("preemptor", "ns").Priority(100).
				Annotation(controllerconstants.PriorityBoostAnnotationKey, "100").
				Creation(now.Add(time.Second)).Obj(),
			candidate: utiltestingapi.MakeWorkload("candidate", "ns").Priority(200).
				Creation(now).Obj(),
			policy: kueue.PreemptionPolicyLowerOrNewerEqualPriority,
			want:   false,
		},
		"Any: always satisfies regardless of priority or boost": {
			preemptor: utiltestingapi.MakeWorkload("preemptor", "ns").Priority(50).Obj(),
			candidate: utiltestingapi.MakeWorkload("candidate", "ns").Priority(100).
				Annotation(controllerconstants.PriorityBoostAnnotationKey, "200").Obj(),
			policy: kueue.PreemptionPolicyAny,
			want:   true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := SatisfiesPreemptionPolicy(tc.preemptor, tc.candidate, ordering, tc.policy)
			if got != tc.want {
				t.Errorf("SatisfiesPreemptionPolicy() = %v, want %v", got, tc.want)
			}
		})
	}
}
