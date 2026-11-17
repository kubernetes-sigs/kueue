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
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

func TestAssignmentClusterQueueState(t *testing.T) {
	cases := map[string]struct {
		state              *FlavorScanState
		wantPendingFlavors bool
	}{
		"no info": {
			wantPendingFlavors: false,
		},
		"all done": {
			state: &FlavorScanState{
				TriedFlavors: []map[corev1.ResourceName]sets.Set[kueue.ResourceFlavorReference]{
					{
						corev1.ResourceCPU:    nil,
						corev1.ResourceMemory: nil,
					},
					{
						corev1.ResourceMemory: nil,
					},
				},
			},
			wantPendingFlavors: false,
		},
		"some pending": {
			state: &FlavorScanState{
				TriedFlavors: []map[corev1.ResourceName]sets.Set[kueue.ResourceFlavorReference]{
					{
						corev1.ResourceCPU:    sets.New[kueue.ResourceFlavorReference]("flavor-0"),
						corev1.ResourceMemory: nil,
					},
					{
						corev1.ResourceMemory: sets.New[kueue.ResourceFlavorReference]("flavor-0", "flavor-1"),
					},
				},
			},
			wantPendingFlavors: true,
		},
		"all pending": {
			state: &FlavorScanState{
				TriedFlavors: []map[corev1.ResourceName]sets.Set[kueue.ResourceFlavorReference]{
					{
						corev1.ResourceCPU:    sets.New[kueue.ResourceFlavorReference]("flavor-0", "flavor-1"),
						corev1.ResourceMemory: sets.New[kueue.ResourceFlavorReference]("flavor-0"),
					},
					{
						corev1.ResourceMemory: sets.New[kueue.ResourceFlavorReference]("flavor-0", "flavor-1"),
					},
				},
			},
			wantPendingFlavors: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := tc.state.PendingFlavors()
			if got != tc.wantPendingFlavors {
				t.Errorf("state.PendingFlavors() = %t, want %t", got, tc.wantPendingFlavors)
			}
		})
	}
}

func TestFlavorScanStateOutdated(t *testing.T) {
	type args struct {
		currentSchedulingCycle int64
		last                   *FlavorScanState
		currentCQGeneration    int64
		currentSchedulingHash  EquivalenceHash
	}
	tests := []struct {
		name string
		// preserveProgress enables features.FlavorFungibilityPreserveScanProgress.
		preserveProgress bool
		args             args
		want             bool
	}{
		{
			name: "Cluster queue allocatableResourceIncreasedGen increased",
			args: args{
				currentSchedulingCycle: 5,
				last: &FlavorScanState{
					AllocatableResourceGeneration: 0,
					SchedulingCycle:               1,
				},
				currentCQGeneration: 1,
			},
			want: true,
		},
		{
			name: "AllocatableResourceGeneration not increased",
			args: args{
				currentSchedulingCycle: 5,
				last: &FlavorScanState{
					AllocatableResourceGeneration: 0,
					SchedulingCycle:               1,
				},
				currentCQGeneration: 0,
			},
			want: false,
		},
		{
			name:             "assignment from the immediately preceding cycle survives a generation bump",
			preserveProgress: true,
			args: args{
				currentSchedulingCycle: 5,
				last: &FlavorScanState{
					AllocatableResourceGeneration: 0,
					SchedulingCycle:               4,
				},
				currentCQGeneration: 1,
			},
			want: false,
		},
		{
			name:             "assignment from the current cycle survives a generation bump",
			preserveProgress: true,
			args: args{
				currentSchedulingCycle: 5,
				last: &FlavorScanState{
					AllocatableResourceGeneration: 0,
					SchedulingCycle:               5,
				},
				currentCQGeneration: 1,
			},
			want: false,
		},
		{
			name:             "assignment older than one cycle falls back to the generation check",
			preserveProgress: true,
			args: args{
				currentSchedulingCycle: 5,
				last: &FlavorScanState{
					AllocatableResourceGeneration: 0,
					SchedulingCycle:               3,
				},
				currentCQGeneration: 1,
			},
			want: true,
		},
		{
			name:             "a changed scheduling shape starts the scan over even in the preceding cycle",
			preserveProgress: true,
			args: args{
				currentSchedulingCycle: 5,
				last: &FlavorScanState{
					AllocatableResourceGeneration: 0,
					SchedulingCycle:               4,
					SchedulingHash:                "shape-a",
				},
				currentCQGeneration:   0,
				currentSchedulingHash: "shape-b",
			},
			want: true,
		},
		{
			name:             "an unchanged scheduling shape keeps the preceding cycle's assignment",
			preserveProgress: true,
			args: args{
				currentSchedulingCycle: 5,
				last: &FlavorScanState{
					AllocatableResourceGeneration: 0,
					SchedulingCycle:               4,
					SchedulingHash:                "shape-a",
				},
				currentCQGeneration:   1,
				currentSchedulingHash: "shape-a",
			},
			want: false,
		},
		{
			name:             "an unknown hash is not treated as a shape change",
			preserveProgress: true,
			args: args{
				currentSchedulingCycle: 5,
				last: &FlavorScanState{
					AllocatableResourceGeneration: 0,
					SchedulingCycle:               4,
					SchedulingHash:                SchedulingHashUnknown,
				},
				currentCQGeneration:   1,
				currentSchedulingHash: "shape-b",
			},
			want: false,
		},
		{
			name:             "without the gate a changed scheduling shape is ignored",
			preserveProgress: false,
			args: args{
				currentSchedulingCycle: 5,
				last: &FlavorScanState{
					AllocatableResourceGeneration: 0,
					SchedulingCycle:               4,
					SchedulingHash:                "shape-a",
				},
				currentCQGeneration:   0,
				currentSchedulingHash: "shape-b",
			},
			want: false,
		},
		{
			name:             "without the gate the preceding cycle gets no special treatment",
			preserveProgress: false,
			args: args{
				currentSchedulingCycle: 5,
				last: &FlavorScanState{
					AllocatableResourceGeneration: 0,
					SchedulingCycle:               4,
				},
				currentCQGeneration: 1,
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.FlavorFungibilityPreserveScanProgress, tt.preserveProgress)
			if got := tt.args.last.IsOutdated(tt.args.currentCQGeneration, tt.args.currentSchedulingCycle, tt.args.currentSchedulingHash); got != tt.want {
				t.Errorf("IsOutdated() = %v, want %v", got, tt.want)
			}
		})
	}
}
