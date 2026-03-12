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
	"sigs.k8s.io/kueue/pkg/features"
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
		features  map[featuregate.Feature]bool
		preemptor *kueue.Workload
		candidate *kueue.Workload
		policy    kueue.PreemptionPolicy
		want      bool
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
			features: map[featuregate.Feature]bool{
				features.SchedulerTimestampPreemptionBuffer: true,
			},
			preemptor: preemptor.Clone().Priority(10).Creation(now).Obj(),
			candidate: candidate.Clone().Priority(10).Creation(nowPlus1Min).Obj(),
			policy:    kueue.PreemptionPolicyLowerOrNewerEqualPriority,
			want:      false, // candidate is newer but within buffer
		},
		"LowerOrNewerEqualPriority with SchedulerTimestampPreemptionBuffer: preemptor has same priority, older timestamp (outside 5min buffer)": {
			features: map[featuregate.Feature]bool{
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
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			for feature, enabled := range tc.features {
				features.SetFeatureGateDuringTest(t, feature, enabled)
			}
			ordering := workload.Ordering{}
			got := SatisfiesPreemptionPolicy(tc.preemptor, tc.candidate, ordering, tc.policy)
			if got != tc.want {
				t.Errorf("SatisfiesPreemptionPolicy() = %v, want %v", got, tc.want)
			}
		})
	}
}
