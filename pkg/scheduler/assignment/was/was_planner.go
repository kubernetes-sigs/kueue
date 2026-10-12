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

package was

import (
	"context"
	"errors"

	ctrl "sigs.k8s.io/controller-runtime"

	schedcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/scheduler/assignment"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/workload"
)

// NewPlanner returns an implementation of the assignment Planner
// that utilizes the Scheduler Simulation Library to determine Pod placement.
func NewPlanner(
	wl *workload.Info,
	snapshot *schedcache.Snapshot,
) assignment.Planner {
	return &wasPlanner{wl: wl, snapshot: snapshot}
}

var _ assignment.Planner = (*wasPlanner)(nil)

type wasPlanner struct {
	wl       *workload.Info
	snapshot *schedcache.Snapshot
}

// Plan is currently inert and always returns an error.
func (p *wasPlanner) Plan(ctx context.Context, initialAssignment *flavorassigner.Assignment, _ ...assignment.PlannerOption) (plan assignment.Plan) {
	plan = assignment.Plan{
		Assignment: initialAssignment,
	}
	arm := plan.Assignment.RepresentativeMode()
	if arm == flavorassigner.NoFit {
		// NoFit, nothing to do.
		return
	}

	log := ctrl.LoggerFrom(ctx)
	cq := p.snapshot.ClusterQueue(p.wl.ClusterQueue)
	tasRequests := plan.Assignment.WorkloadsTopologyRequests(log, p.wl, cq)
	if arm == flavorassigner.Fit && len(tasRequests) == 0 {
		// Fit without TAS. Nothing to do.
		return
	}

	// Any path that requires additional processing fails as
	// wasPlanner does not support them yet.
	plan.Error = errors.New("was planner unable to process assignment")
	return
}
