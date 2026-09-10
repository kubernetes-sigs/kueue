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

package preemption

import (
	"slices"
	"testing"

	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestPreemptionTargetsInsert(t *testing.T) {
	targetFor := func(name string) *Target {
		return &Target{
			WorkloadInfo: workload.NewInfo(utiltestingapi.MakeWorkload(name, "ns").Obj()),
		}
	}
	oldSlice := targetFor("old-slice")
	// The preemptor works on the same snapshot, so a duplicate selection
	// refers to the same workload key.
	oldSliceFromPreemptor := targetFor("old-slice")
	other := targetFor("other")

	cases := map[string]struct {
		inserts [][]*Target
		want    PreemptionTargets
	}{
		"nothing inserted": {},
		"only preemptor targets": {
			inserts: [][]*Target{nil, {other}},
			want:    PreemptionTargets{other},
		},
		"no overlap": {
			inserts: [][]*Target{{oldSlice}, {other}},
			want:    PreemptionTargets{oldSlice, other},
		},
		"preemptor selected the replaced slice again": {
			inserts: [][]*Target{{oldSlice}, {oldSliceFromPreemptor, other}},
			want:    PreemptionTargets{oldSlice, other},
		},
		"duplicates within a single insert": {
			inserts: [][]*Target{{oldSlice, oldSliceFromPreemptor}},
			want:    PreemptionTargets{oldSlice},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var got PreemptionTargets
			for _, targets := range tc.inserts {
				got.Insert(targets...)
			}
			// Compare by identity: the target inserted first must win over
			// any later duplicate.
			if !slices.Equal(tc.want, got) {
				t.Errorf("unexpected targets: want %v, got %v", tc.want, got)
			}
		})
	}
}
