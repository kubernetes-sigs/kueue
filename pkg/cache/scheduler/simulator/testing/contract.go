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
	"fmt"
	"iter"
	"strings"

	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	"k8s.io/apimachinery/pkg/util/sets"
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

var _ TestReporter = (*Recorder)(nil)

// Recorder captures contract violations for tests that need to inspect them.
type Recorder struct {
	TestReporter
	Messages []string
}

// NewRecorder wraps t and records reported contract violations.
func NewRecorder(t TestReporter) *Recorder {
	return &Recorder{TestReporter: t}
}

func (r *Recorder) Helper() {
	if r.TestReporter != nil {
		r.TestReporter.Helper()
	}
}

func (r *Recorder) Errorf(format string, args ...any) {
	r.Messages = append(r.Messages, fmt.Sprintf(format, args...))
	if r.TestReporter != nil {
		r.TestReporter.Errorf(format, args...)
	}
}

func (r *Recorder) Count() int { return len(r.Messages) }

func (r *Recorder) Contains(sub string) bool {
	for _, message := range r.Messages {
		if strings.Contains(message, sub) {
			return true
		}
	}
	return false
}

// WrapForTest decorates impl so every call asserts the SchedulerSimulator
// contract inline against t. Existing per-impl tests can opt in by wrapping
// their simulator and exercising it as usual; any contract violation is
// reported via t.Errorf.
func WrapForTest(t TestReporter, impl simulator.SchedulerSimulator) (simulator.SchedulerSimulator, *Recorder) {
	recorder := NewRecorder(t)
	return &contractEnforcer{t: recorder, impl: impl}, recorder
}

type contractEnforcer struct {
	t    TestReporter
	impl simulator.SchedulerSimulator
}

var _ simulator.SchedulerSimulator = (*contractEnforcer)(nil)

// Simulate must execute the provided function.
func (c *contractEnforcer) Simulate(ctx context.Context, fn func()) error {
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
func (c *contractEnforcer) FindFeasibleNodes(
	ctx context.Context,
	candidates iter.Seq[simulator.Candidate],
	requirements *simulator.PodRequirements,
	stats *simulator.NodeExclusionStats,
) ([]simulator.MatchedCandidate, error) {
	c.t.Helper()

	// Record IDs lazily as the implementation consumes candidates, so we never
	// buffer or over-consume the sequence (it may be large or unbounded).
	inputIDs := sets.New[utiltas.TopologyDomainID]()
	recordingIterator := func(yield func(simulator.Candidate) bool) {
		for candidate := range candidates {
			inputIDs.Insert(candidate.GetID())
			if !yield(candidate) {
				return
			}
		}
	}

	result, err := c.impl.FindFeasibleNodes(ctx, recordingIterator, requirements, stats)
	// Validate returned candidates even on error: an implementation may return
	// partial results alongside an error, and those must still be a subset.
	for _, matched := range result {
		if !inputIDs.Has(matched.GetID()) {
			c.t.Errorf("FindFeasibleNodes: returned candidate %q not present in inputs", matched.GetID())
		}
	}
	return result, err
}

// PreemptWorkload must return a non-nil revert function when it succeeds.
func (c *contractEnforcer) PreemptWorkload(ctx context.Context, wlKey client.ObjectKey) (func() error, error) {
	c.t.Helper()
	revert, err := c.impl.PreemptWorkload(ctx, wlKey)
	if err == nil && revert == nil {
		c.t.Errorf("PreemptWorkload(%v): returned nil revert function without an error", wlKey)
	}
	return revert, err
}

// ScheduleWorkload must either fail completely (error + empty placements) or
// return a placement for exactly the input pods, each internally consistent.
func (c *contractEnforcer) ScheduleWorkload(ctx context.Context, workloadPods []*corev1.Pod, opts ...simulator.ScheduleOption) simulator.SchedulingResult {
	c.t.Helper()
	result := c.impl.ScheduleWorkload(ctx, workloadPods, opts...)

	if result.Error != nil {
		if len(result.PodPlacements) != 0 {
			c.t.Errorf("ScheduleWorkload: returned %d placements alongside an error; expected none", len(result.PodPlacements))
		}
		return result
	}

	inputKeys := sets.New[client.ObjectKey]()
	for _, pod := range workloadPods {
		inputKeys.Insert(client.ObjectKeyFromObject(pod))
	}
	if len(result.PodPlacements) != len(inputKeys) {
		c.t.Errorf("ScheduleWorkload: got %d placements for %d input pods", len(result.PodPlacements), len(inputKeys))
	}
	for key, placement := range result.PodPlacements {
		if !inputKeys.Has(key) {
			c.t.Errorf("ScheduleWorkload: placement for pod %v not present in inputs", key)
		}
		checkPlacement(c.t, key, placement)
	}
	return result
}

func (c *contractEnforcer) AddPodGroup(ctx context.Context, pg *schedulingv1beta1.PodGroup) error {
	return c.impl.AddPodGroup(ctx, pg)
}

func (c *contractEnforcer) RemovePodGroup(ctx context.Context, pg *schedulingv1beta1.PodGroup) error {
	return c.impl.RemovePodGroup(ctx, pg)
}

func (c *contractEnforcer) AddCompositePodGroup(ctx context.Context, cpg *schedulingv1alpha3.CompositePodGroup) error {
	return c.impl.AddCompositePodGroup(ctx, cpg)
}

func (c *contractEnforcer) RemoveCompositePodGroup(ctx context.Context, cpg *schedulingv1alpha3.CompositePodGroup) error {
	return c.impl.RemoveCompositePodGroup(ctx, cpg)
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
