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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestHasReclaimTargetCount(t *testing.T) {
	makeWorkload := func(admitted bool, target *int32) *kueue.Workload {
		wl := utiltesting.MakeWorkload("wl", "ns").
			PodSets(*utiltesting.MakePodSet("executor", 5).
				Request(corev1.ResourceCPU, "1").
				SetMinimumCount(1).
				Obj()).
			Obj()
		if admitted {
			wl.Status.Admission = utiltesting.MakeAdmission("cq").
				PodSets(utiltesting.MakePodSetAssignment("executor").
					Assignment(corev1.ResourceCPU, "default", "1").
					Count(5).
					Obj()).
				Obj()
		}
		if target != nil {
			wl.Status.Admission.PodSetAssignments[0].ReclaimTargetCount = target
		}
		return wl
	}

	cases := map[string]struct {
		admitted bool
		target   *int32
		want     bool
	}{
		"no admission":                    {want: false},
		"admission without target":        {admitted: true, want: false},
		"admission with target":           {admitted: true, target: ptr.To[int32](2), want: true},
		"admission with satisfied target": {admitted: true, target: ptr.To[int32](5), want: false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := HasReclaimTargetCount(makeWorkload(tc.admitted, tc.target)); got != tc.want {
				t.Fatalf("HasReclaimTargetCount() = %t, want %t", got, tc.want)
			}
		})
	}
}
