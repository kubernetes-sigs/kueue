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

package flavorassigner

import (
	"testing"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
)

func TestCanPreemptWhileBorrowing(t *testing.T) {
	cases := map[string]struct {
		preemption        kueue.ClusterQueuePreemption
		enableFairSharing bool
		reclaimOnly       bool
		want              bool
	}{
		"fair sharing with reclaimWithinCohort": {
			preemption:        kueue.ClusterQueuePreemption{ReclaimWithinCohort: kueue.PreemptionPolicyAny},
			enableFairSharing: true,
			want:              true,
		},
		"fair sharing ReclaimOnly with reclaimWithinCohort": {
			preemption:        kueue.ClusterQueuePreemption{ReclaimWithinCohort: kueue.PreemptionPolicyAny},
			enableFairSharing: true,
			reclaimOnly:       true,
			want:              false,
		},
		"fair sharing with reclaimWithinCohort never": {
			preemption:        kueue.ClusterQueuePreemption{ReclaimWithinCohort: kueue.PreemptionPolicyNever},
			enableFairSharing: true,
			want:              false,
		},
		"classical borrowWithinCohort": {
			preemption: kueue.ClusterQueuePreemption{
				BorrowWithinCohort: &kueue.BorrowWithinCohort{Policy: kueue.BorrowWithinCohortPolicyLowerPriority},
			},
			want: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a := New(nil, &schdcache.ClusterQueueSnapshot{Preemption: tc.preemption}, nil, tc.enableFairSharing, nil, nil, "", nil, 0).
				WithFairSharingReclaimOnly(tc.reclaimOnly)
			if got := a.canPreemptWhileBorrowing(); got != tc.want {
				t.Errorf("canPreemptWhileBorrowing() = %t, want %t", got, tc.want)
			}
		})
	}
}
