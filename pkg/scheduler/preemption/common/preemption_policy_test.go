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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/featuregate"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestSatisfiesPreemptionPolicy(t *testing.T) {
	now := time.Now()
	nowPlus1Min := now.Add(time.Minute)
	nowPlus6Min := now.Add(6 * time.Minute)

	preemptor := utiltestingapi.MakeWorkload("preemptor", metav1.NamespaceDefault)
	candidate := utiltestingapi.MakeWorkload("candidate", metav1.NamespaceDefault)

	testCases := map[string]struct {
		featureGates map[featuregate.Feature]bool
		preemptor    *kueue.Workload
		candidate    *kueue.Workload
		policy       kueue.PreemptionPolicy
		want         bool
	}{
		"LowerPriority: preemptor has higher priority": {
			preemptor: preemptor.Clone().Priority(10).Obj(),
			candidate: candidate.Clone().Priority(5).Obj(),
			policy:    kueue.PreemptionPolicyLowerPriority,
			want:      true,
		},
		"LowerPriority: preemptor has same priority": {
			preemptor: preemptor.Clone().Priority(10).Obj(),
			candidate: candidate.Clone().Priority(10).Obj(),
			policy:    kueue.PreemptionPolicyLowerPriority,
			want:      false,
		},
		"LowerOrNewerEqualPriority: preemptor has same priority, same timestamp": {
			preemptor: preemptor.Clone().Priority(10).Creation(now).Obj(),
			candidate: candidate.Clone().Priority(10).Creation(now).Obj(),
			policy:    kueue.PreemptionPolicyLowerOrNewerEqualPriority,
			want:      false,
		},
		"LowerOrNewerEqualPriority: preemptor has same priority, newer timestamp (within 5min buffer)": {
			preemptor: preemptor.Clone().Priority(10).Creation(nowPlus1Min).Obj(),
			candidate: candidate.Clone().Priority(10).Creation(now).Obj(),
			policy:    kueue.PreemptionPolicyLowerOrNewerEqualPriority,
			want:      false, // candidate is older, so candidate is NOT newer
		},
		"LowerOrNewerEqualPriority: preemptor has same priority, older timestamp (within 5min buffer)": {
			preemptor: preemptor.Clone().Priority(10).Creation(now).Obj(),
			candidate: candidate.Clone().Priority(10).Creation(nowPlus1Min).Obj(),
			policy:    kueue.PreemptionPolicyLowerOrNewerEqualPriority,
			want:      true, // candidate is newer
		},
		"LowerOrNewerEqualPriority with SchedulerTimestampPreemptionBuffer: preemptor has same priority, older timestamp (within 5min buffer)": {
			featureGates: map[featuregate.Feature]bool{
				features.SchedulerTimestampPreemptionBuffer: true,
			},
			preemptor: preemptor.Clone().Priority(10).Creation(now).Obj(),
			candidate: candidate.Clone().Priority(10).Creation(nowPlus1Min).Obj(),
			policy:    kueue.PreemptionPolicyLowerOrNewerEqualPriority,
			want:      false, // candidate is newer but within buffer
		},
		"LowerOrNewerEqualPriority with SchedulerTimestampPreemptionBuffer: preemptor has same priority, older timestamp (outside 5min buffer)": {
			featureGates: map[featuregate.Feature]bool{
				features.SchedulerTimestampPreemptionBuffer: true,
			},
			preemptor: preemptor.Clone().Priority(10).Creation(now).Obj(),
			candidate: candidate.Clone().Priority(10).Creation(nowPlus6Min).Obj(),
			policy:    kueue.PreemptionPolicyLowerOrNewerEqualPriority,
			want:      true, // candidate is newer and outside buffer
		},
		"PreemptionPolicyAny": {
			preemptor: preemptor.Clone().Priority(10).Creation(now).Obj(),
			candidate: candidate.Clone().Priority(10).Creation(nowPlus6Min).Obj(),
			policy:    kueue.PreemptionPolicyAny,
			want:      true,
		},
		"PreemptionPolicyNever": {
			preemptor: preemptor.Clone().Priority(10).Creation(now).Obj(),
			candidate: candidate.Clone().Priority(10).Creation(nowPlus6Min).Obj(),
			policy:    kueue.PreemptionPolicyNever,
			want:      false,
		},
		"LowerPriority: boost makes low-priority immune to medium-priority preemption": {
			featureGates: map[featuregate.Feature]bool{
				features.PriorityBoost: true,
			},
			preemptor: preemptor.Clone().Priority(200).Obj(),
			candidate: candidate.Clone().Priority(100).
				Annotation(controllerconstants.PriorityBoostAnnotationKey, "150").Obj(),
			policy: kueue.PreemptionPolicyLowerPriority,
			want:   false,
		},
		"LowerPriority: boost on preemptor enables preemption of higher raw priority": {
			featureGates: map[featuregate.Feature]bool{
				features.PriorityBoost: true,
			},
			preemptor: preemptor.Clone().Priority(100).
				Annotation(controllerconstants.PriorityBoostAnnotationKey, "200").Obj(),
			candidate: candidate.Clone().Priority(200).Obj(),
			policy:    kueue.PreemptionPolicyLowerPriority,
			want:      true,
		},
		"LowerOrNewerEqualPriority: boost equalizes priorities, older preemptor can preempt newer candidate": {
			featureGates: map[featuregate.Feature]bool{
				features.PriorityBoost: true,
			},
			preemptor: preemptor.Clone().Priority(100).
				Annotation(controllerconstants.PriorityBoostAnnotationKey, "100").
				Creation(now).Obj(),
			candidate: candidate.Clone().Priority(200).
				Creation(now.Add(time.Second)).Obj(),
			policy: kueue.PreemptionPolicyLowerOrNewerEqualPriority,
			want:   true,
		},
		"LowerOrNewerEqualPriority: equal effective priority, newer preemptor cannot preempt older candidate": {
			featureGates: map[featuregate.Feature]bool{
				features.PriorityBoost: true,
			},
			preemptor: preemptor.Clone().Priority(100).
				Annotation(controllerconstants.PriorityBoostAnnotationKey, "100").
				Creation(now.Add(time.Second)).Obj(),
			candidate: candidate.Clone().Priority(200).
				Creation(now).Obj(),
			policy: kueue.PreemptionPolicyLowerOrNewerEqualPriority,
			want:   false,
		},
		"Any: always satisfies regardless of priority or boost": {
			featureGates: map[featuregate.Feature]bool{
				features.PriorityBoost: true,
			},
			preemptor: preemptor.Clone().Priority(50).Obj(),
			candidate: candidate.Clone().Priority(100).
				Annotation(controllerconstants.PriorityBoostAnnotationKey, "200").Obj(),
			policy: kueue.PreemptionPolicyAny,
			want:   true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ordering := workload.Ordering{}
			got := SatisfiesPreemptionPolicy(log, tc.preemptor, tc.candidate, ordering, tc.policy)
			if got != tc.want {
				t.Errorf("SatisfiesPreemptionPolicy() = %v, want %v", got, tc.want)
			}
		})
	}
}
