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

package scheduler

import (
	"slices"
	"testing"

	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestMergeWithReplacedSliceTargets(t *testing.T) {
	targetFor := func(name string) *preemption.Target {
		return &preemption.Target{
			WorkloadInfo: workload.NewInfo(utiltestingapi.MakeWorkload(name, "ns").Obj()),
		}
	}
	oldSlice := targetFor("old-slice")
	// The preemptor works on the same snapshot, so a duplicate selection
	// refers to the same workload key.
	oldSliceFromPreemptor := targetFor("old-slice")
	other := targetFor("other")

	cases := map[string]struct {
		sliceTargets     []*preemption.Target
		preemptorTargets []*preemption.Target
		want             []*preemption.Target
	}{
		"no slice targets": {
			preemptorTargets: []*preemption.Target{other},
			want:             []*preemption.Target{other},
		},
		"no overlap": {
			sliceTargets:     []*preemption.Target{oldSlice},
			preemptorTargets: []*preemption.Target{other},
			want:             []*preemption.Target{oldSlice, other},
		},
		"preemptor selected the replaced slice again": {
			sliceTargets:     []*preemption.Target{oldSlice},
			preemptorTargets: []*preemption.Target{oldSliceFromPreemptor, other},
			want:             []*preemption.Target{oldSlice, other},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := mergeWithReplacedSliceTargets(tc.sliceTargets, tc.preemptorTargets)
			// Compare by identity: the slice-target instance must win over
			// the preemptor's duplicate.
			if !slices.Equal(tc.want, got) {
				t.Errorf("unexpected merged targets: want %v, got %v", tc.want, got)
			}
		})
	}
}
