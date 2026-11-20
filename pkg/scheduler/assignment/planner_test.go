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

	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
)

func TestPlanCanFit(t *testing.T) {
	cases := map[string]struct {
		mode                      flavorassigner.FlavorAssignmentMode
		waitingForResidualTASPods bool
		targets                   []*preemption.Target
		err                       error
		want                      bool
	}{
		"Fit": {
			mode: flavorassigner.Fit,
			want: true,
		},
		"Fit with an error": {
			mode: flavorassigner.Fit,
			err:  errors.New("failed"),
		},
		"Preempt with targets": {
			mode:    flavorassigner.Preempt,
			targets: []*preemption.Target{{}},
			want:    true,
		},
		"Preempt without targets": {
			mode: flavorassigner.Preempt,
		},
		"DeferredFit waiting for committed victims": {
			mode: flavorassigner.DeferredFit,
			want: true,
		},
		"DeferredFit waiting for residual TAS Pods": {
			mode:                      flavorassigner.DeferredFit,
			waitingForResidualTASPods: true,
		},
		"NoFit": {
			mode: flavorassigner.NoFit,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a := &flavorassigner.Assignment{
				PodSets:                   []flavorassigner.PodSetAssignment{{Name: "main"}},
				WaitingForResidualTASPods: tc.waitingForResidualTASPods,
			}
			a.SetRepresentativeMode(tc.mode)
			p := &Plan{Assignment: a, PreemptionTargets: tc.targets, Error: tc.err}
			if got := p.CanFit(); got != tc.want {
				t.Errorf("CanFit() = %v, want %v", got, tc.want)
			}
		})
	}
}
