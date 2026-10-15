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
	"context"

	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
)

type Planner interface {
	// Plan computes the assignment allowing admission, if one exists,
	// alongside the necessary preemption targets.
	// Requires providing the initial flavor assignment.
	Plan(ctx context.Context, initialAssignment *flavorassigner.Assignment, opts ...PlannerOption) Plan
}

// Plan represents the proposed assignment and necessary preemption targets
// allowing for workload admission.
// The whole plan is considered invalid if the Error field is set.
type Plan struct {
	// Assignment - the updated workload assignment.
	// Only valid if Error is nil.
	Assignment *flavorassigner.Assignment
	// PreemptionTargets - the list of preemption targets necessary
	// to make the admission based on the returned assignment possible.
	// Empty unless Assignment.RepresentativeMode() is Preempt
	// and preemptions make the fit feasible.
	// Only valid if Error is nil.
	PreemptionTargets []*preemption.Target

	// Error denotes a fatal failure when trying to build the plan.
	// If present, renders the whole plan invalid.
	Error error
}

func (p *Plan) CanFit() bool {
	if p.Error != nil {
		return false
	}
	arm := p.Assignment.RepresentativeMode()
	return arm == flavorassigner.Fit || (arm == flavorassigner.Preempt && len(p.PreemptionTargets) > 0)
}

type options struct{}

type PlannerOption func(*options)
