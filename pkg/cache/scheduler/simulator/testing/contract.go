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

// Package testing provides a shared harness for checking that a
// simulator.SchedulerSimulator implementation honors the interface contract.
package testing

import (
	"context"
	"iter"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

// TestReporter is the subset of testing.TB used by the harness. *testing.T
// and *testing.B satisfy it.
type TestReporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// WrapForTest decorates impl so every call asserts the SchedulerSimulator
// contract inline against t. Existing per-impl tests can opt in by wrapping
// their simulator and exercising it as usual; any contract violation is
// reported via t.Errorf.
func WrapForTest(t TestReporter, impl simulator.SchedulerSimulator) simulator.SchedulerSimulator {
	return &contractChecker{t: t, impl: impl}
}

type contractChecker struct {
	t    TestReporter
	impl simulator.SchedulerSimulator
}

var _ simulator.SchedulerSimulator = (*contractChecker)(nil)

// Simulate must execute the provided function.
func (c *contractChecker) Simulate(ctx context.Context, fn func()) error {
	c.t.Helper()
	called := false
	err := c.impl.Simulate(ctx, func() {
		called = true
		fn()
	})
	if !called {
		c.t.Errorf("Simulate: implementation did not execute the provided function")
	}
	return err
}

// FindFeasibleNodes must return a subset of the input candidates (by domain ID).
func (c *contractChecker) FindFeasibleNodes(
	ctx context.Context,
	candidates iter.Seq[simulator.Candidate],
	requirements *simulator.PodRequirements,
	stats *simulator.NodeExclusionStats,
) ([]simulator.MatchedCandidate, error) {
	c.t.Helper()

	// Record IDs lazily as the implementation consumes candidates, so we never
	// buffer or over-consume the sequence (it may be large or unbounded).
	inputIDs := make(map[utiltas.TopologyDomainID]struct{})
	recording := func(yield func(simulator.Candidate) bool) {
		for candidate := range candidates {
			inputIDs[candidate.GetID()] = struct{}{}
			if !yield(candidate) {
				return
			}
		}
	}

	result, err := c.impl.FindFeasibleNodes(ctx, recording, requirements, stats)
	// Validate returned candidates even on error: an implementation may return
	// partial results alongside an error, and those must still be a subset.
	for _, matched := range result {
		if _, ok := inputIDs[matched.GetID()]; !ok {
			c.t.Errorf("FindFeasibleNodes: returned candidate %q not present in inputs", matched.GetID())
		}
	}
	return result, err
}

// PreemptWorkload must return a non-nil revert function when it succeeds.
func (c *contractChecker) PreemptWorkload(ctx context.Context, wlKey client.ObjectKey) (func() error, error) {
	c.t.Helper()
	revert, err := c.impl.PreemptWorkload(ctx, wlKey)
	if err == nil && revert == nil {
		c.t.Errorf("PreemptWorkload(%v): returned nil revert function without an error", wlKey)
	}
	return revert, err
}

// ScheduleWorkload must either fail completely (error + empty placements) or
// return a placement for exactly the input pods, each internally consistent.
func (c *contractChecker) ScheduleWorkload(ctx context.Context, workloadPods []*corev1.Pod, opts ...simulator.ScheduleOption) simulator.SchedulingResult {
	c.t.Helper()
	result := c.impl.ScheduleWorkload(ctx, workloadPods, opts...)

	if result.Error != nil {
		if len(result.PodPlacements) != 0 {
			c.t.Errorf("ScheduleWorkload: returned %d placements alongside an error; expected none", len(result.PodPlacements))
		}
		return result
	}

	inputKeys := make(map[client.ObjectKey]struct{}, len(workloadPods))
	for _, pod := range workloadPods {
		inputKeys[client.ObjectKeyFromObject(pod)] = struct{}{}
	}
	if len(result.PodPlacements) != len(inputKeys) {
		c.t.Errorf("ScheduleWorkload: got %d placements for %d input pods", len(result.PodPlacements), len(inputKeys))
	}
	for key, placement := range result.PodPlacements {
		if _, ok := inputKeys[key]; !ok {
			c.t.Errorf("ScheduleWorkload: placement for pod %v not present in inputs", key)
		}
		checkPlacement(c.t, key, placement)
	}
	return result
}

// checkPlacement enforces: success (Node set) excludes Error/Reasons, and a
// non-success placement has an empty Node.
func checkPlacement(t TestReporter, key client.ObjectKey, placement *simulator.PodPlacement) {
	t.Helper()
	if placement == nil {
		t.Errorf("ScheduleWorkload: nil placement for pod %v", key)
		return
	}
	success := placement.Node != "" && placement.Error == nil && len(placement.Reasons) == 0
	if !success && placement.Node != "" {
		t.Errorf("ScheduleWorkload: pod %v has node %q but is not a clean success (err=%v, reasons=%v)",
			key, placement.Node, placement.Error, placement.Reasons)
	}
}
