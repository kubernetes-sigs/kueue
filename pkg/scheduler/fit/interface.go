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

package fit

import (
	"context"

	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
)

type Finder interface {
	// FindFit computes the assignment allowing admission, if one exists,
	// alongisde the necessary preemption targets.
	FindFit(ctx context.Context, initialAssignment *flavorassigner.Assignment, opts ...option) Result
}

type Result struct {
	// Assignment - the updated workload asssignment.
	Assignment *flavorassigner.Assignment
	// PreemptionTargets - the list of preemption targets necessary
	// to make the admission based on the returned assignment possible.
	// Empty unless Assignment.RepresentativeMode() is Preempt
	// and preemptions make the fit feasible.
	PreemptionTargets []*preemption.Target
}

func (r *Result) CanFit() bool {
	arm := r.Assignment.RepresentativeMode()
	return arm == flavorassigner.Fit || (arm == flavorassigner.Preempt && len(r.PreemptionTargets) > 0)
}

type options struct{}

type option func(*options)
