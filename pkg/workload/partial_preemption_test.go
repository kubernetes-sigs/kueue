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

package workload

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestPartialPreemptibleCounts(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	// admission builds a single "executor" PodSet admission at the given accounted count.
	admission := func(admittedCount int32) *kueue.Admission {
		return utiltesting.MakeAdmission("cq").
			PodSets(utiltesting.MakePodSetAssignment("executor").
				Assignment(corev1.ResourceCPU, "default", "1").
				Count(admittedCount).
				Obj()).
			Obj()
	}

	cases := map[string]struct {
		specCount     int
		minCount      int32
		admittedCount int32
		want          map[kueue.PodSetReference]int32
	}{
		"steady state: reclaimable is admitted-minCount": {
			specCount:     5,
			minCount:      1,
			admittedCount: 5,
			want:          map[kueue.PodSetReference]int32{"executor": 4},
		},
		"spec.count below admitted: reclaimable follows the used (spec) count": {
			// The driver already scaled spec.count down to 3 while Kueue still accounts 5.
			// Only the 3 currently-occupied pods matter: reclaimable = min(5,3)-1 = 2 (not 4).
			specCount:     3,
			minCount:      1,
			admittedCount: 5,
			want:          map[kueue.PodSetReference]int32{"executor": 2},
		},
		"already at floor: nothing reclaimable": {
			// spec.count already at minCount, so used=min(5,1)=1 is not above minCount.
			specCount:     1,
			minCount:      1,
			admittedCount: 5,
			want:          nil,
		},
		"no minCount: not partial-preemptible": {
			specCount:     5,
			minCount:      0, // treated as unset below
			admittedCount: 5,
			want:          nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ps := utiltesting.MakePodSet("executor", tc.specCount).
				Request(corev1.ResourceCPU, "1")
			if tc.minCount > 0 {
				ps = ps.SetMinimumCount(tc.minCount)
			}
			wl := utiltesting.MakeWorkload("wl", "ns").
				PodSets(*ps.Obj()).
				ReserveQuotaAt(admission(tc.admittedCount), now).
				Obj()

			got := PartialPreemptibleCounts(wl)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("PartialPreemptibleCounts() (-want/+got):\n%s", diff)
			}
		})
	}
}
