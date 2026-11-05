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

package assignment

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestPlan_CanFit(t *testing.T) {
	assignmentWithMode := func(mode flavorassigner.FlavorAssignmentMode) *flavorassigner.Assignment {
		asgn := &flavorassigner.Assignment{
			PodSets: []flavorassigner.PodSetAssignment{{
				Name: "main",
				Flavors: flavorassigner.ResourceAssignment{
					corev1.ResourceCPU: {Name: "default", Mode: flavorassigner.Fit},
				},
			}},
		}
		asgn.SetRepresentativeMode(mode)
		return asgn
	}

	cases := map[string]struct {
		plan Plan
		want bool
	}{
		"Fit mode: fits": {
			plan: Plan{
				Assignment: assignmentWithMode(flavorassigner.Fit),
			},
			want: true,
		},
		"DeferredFit mode: no fit": {
			plan: Plan{
				Assignment: assignmentWithMode(flavorassigner.DeferredFit),
			},
			want: false,
		},
		"Preempt mode with preemption targets: fits": {
			plan: Plan{
				Assignment:        assignmentWithMode(flavorassigner.Preempt),
				PreemptionTargets: []*preemption.Target{{}},
			},
			want: true,
		},
		"Preempt mode without preemption targets: does not fit": {
			plan: Plan{
				Assignment: assignmentWithMode(flavorassigner.Preempt),
			},
			want: false,
		},
		"NoFit mode: does not fit": {
			plan: Plan{
				Assignment: assignmentWithMode(flavorassigner.NoFit),
			},
			want: false,
		},
		"Fit mode with planning error: does not fit": {
			plan: Plan{
				Assignment: assignmentWithMode(flavorassigner.Fit),
				Error:      errors.New("planning failed"),
			},
			want: false,
		},
		"DeferredFit mode with planning error: does not fit": {
			plan: Plan{
				Assignment: assignmentWithMode(flavorassigner.DeferredFit),
				Error:      errors.New("planning failed"),
			},
			want: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.plan.CanFit(); got != tc.want {
				t.Errorf("Plan.CanFit() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBuildOptions(t *testing.T) {
	wl1Ref := workload.NewReference("ns", "wl-1")
	wl2Ref := workload.NewReference("ns", "wl-2")
	wl1Info := &workload.Info{ClusterQueue: "cq-1"}
	wl2Info := &workload.Info{ClusterQueue: "cq-2"}

	cases := map[string]struct {
		options []PlannerOption
		want    *Options
	}{
		"no options": {
			want: &Options{},
		},
		"nil option is ignored": {
			options: []PlannerOption{
				nil,
				WithCommittedPreemptionVictims(map[workload.Reference]*workload.Info{wl1Ref: wl1Info}),
				nil,
			},
			want: &Options{
				CommittedPreemptionVictims: map[workload.Reference]*workload.Info{wl1Ref: wl1Info},
			},
		},
		"WithCommittedPreemptionVictims sets victims": {
			options: []PlannerOption{
				WithCommittedPreemptionVictims(map[workload.Reference]*workload.Info{
					wl1Ref: wl1Info,
					wl2Ref: wl2Info,
				}),
			},
			want: &Options{
				CommittedPreemptionVictims: map[workload.Reference]*workload.Info{
					wl1Ref: wl1Info,
					wl2Ref: wl2Info,
				},
			},
		},
		"later option overrides earlier option": {
			options: []PlannerOption{
				WithCommittedPreemptionVictims(map[workload.Reference]*workload.Info{wl1Ref: wl1Info}),
				WithCommittedPreemptionVictims(map[workload.Reference]*workload.Info{wl2Ref: wl2Info}),
			},
			want: &Options{
				CommittedPreemptionVictims: map[workload.Reference]*workload.Info{wl2Ref: wl2Info},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := BuildOptions(tc.options...)
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(workload.Info{})); diff != "" {
				t.Errorf("BuildOptions() (-want,+got):\n%s", diff)
			}
		})
	}
}
