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

package testing

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

// recorder captures the contract violations reported by the checker so tests
// can assert the specific message, not only that some violation occurred.
type recorder struct{ messages []string }

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.messages = append(r.messages, fmt.Sprintf(format, args...))
}

func (r *recorder) count() int { return len(r.messages) }

// contains reports whether any recorded violation message contains sub.
func (r *recorder) contains(sub string) bool {
	for _, m := range r.messages {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

// fakeCandidate implements simulator.MatchedCandidate.
type fakeCandidate struct {
	id    utiltas.TopologyDomainID
	node  *corev1.Node
	score int64
}

func (f *fakeCandidate) GetID() utiltas.TopologyDomainID { return f.id }
func (f *fakeCandidate) GetNode() *corev1.Node           { return f.node }
func (f *fakeCandidate) GetAffinityScore() int64         { return f.score }
func (f *fakeCandidate) SetAffinityScore(s int64)        { f.score = s }

// fakeSimulator is a programmable implementation used to exercise the checker.
type fakeSimulator struct {
	simulateSkipFn bool
	findResult     []simulator.MatchedCandidate
	findErr        error
	preemptRevert  func() error
	preemptErr     error
	scheduleResult simulator.SchedulingResult
}

func (f *fakeSimulator) Simulate(_ context.Context, fn func()) error {
	if !f.simulateSkipFn {
		fn()
	}
	return nil
}

func (f *fakeSimulator) FindFeasibleNodes(
	_ context.Context,
	candidates iter.Seq[simulator.Candidate],
	_ *simulator.PodRequirements,
	_ *simulator.NodeExclusionStats,
) ([]simulator.MatchedCandidate, error) {
	// Drain like a real implementation so the checker records every input ID.
	for range candidates {
	}
	return f.findResult, f.findErr
}

func (f *fakeSimulator) PreemptWorkload(context.Context, client.ObjectKey) (func() error, error) {
	return f.preemptRevert, f.preemptErr
}

func (f *fakeSimulator) ScheduleWorkload(context.Context, []*corev1.Pod, ...simulator.ScheduleOption) simulator.SchedulingResult {
	return f.scheduleResult
}

func candidatesSeq(cs ...simulator.Candidate) iter.Seq[simulator.Candidate] {
	return func(yield func(simulator.Candidate) bool) {
		for _, c := range cs {
			if !yield(c) {
				return
			}
		}
	}
}

func pod(ns, name string) *corev1.Pod {
	return testingpod.MakePod(name, ns).Obj()
}

func key(ns, name string) client.ObjectKey {
	return client.ObjectKey{Namespace: ns, Name: name}
}

func TestSimulateMustRunFn(t *testing.T) {
	ctx := t.Context()

	t.Run("fn executed passes", func(t *testing.T) {
		r := &recorder{}
		ran := false
		_ = WrapForTest(r, &fakeSimulator{}).Simulate(ctx, func() { ran = true })
		if !ran {
			t.Fatal("inner fn should have run")
		}
		if r.count() != 0 {
			t.Errorf("expected no failures, got %d", r.count())
		}
	})

	t.Run("fn skipped flags", func(t *testing.T) {
		r := &recorder{}
		_ = WrapForTest(r, &fakeSimulator{simulateSkipFn: true}).Simulate(ctx, func() {})
		if r.count() != 1 || !r.contains("did not execute the provided function") {
			t.Errorf("expected a single fn-not-executed violation, got %v", r.messages)
		}
	})
}

func TestFindFeasibleNodesSubset(t *testing.T) {
	ctx := t.Context()
	in := &fakeCandidate{id: "a"}
	foreign := &fakeCandidate{id: "ghost"}

	t.Run("subset passes", func(t *testing.T) {
		r := &recorder{}
		impl := &fakeSimulator{findResult: []simulator.MatchedCandidate{in}}
		_, _ = WrapForTest(r, impl).FindFeasibleNodes(ctx, candidatesSeq(in), &simulator.PodRequirements{}, &simulator.NodeExclusionStats{})
		if r.count() != 0 {
			t.Errorf("expected no failures, got %d", r.count())
		}
	})

	t.Run("foreign candidate flags", func(t *testing.T) {
		r := &recorder{}
		impl := &fakeSimulator{findResult: []simulator.MatchedCandidate{foreign}}
		_, _ = WrapForTest(r, impl).FindFeasibleNodes(ctx, candidatesSeq(in), &simulator.PodRequirements{}, &simulator.NodeExclusionStats{})
		if r.count() != 1 || !r.contains(`candidate "ghost" not present in inputs`) {
			t.Errorf("expected a single not-present-in-inputs violation, got %v", r.messages)
		}
	})

	t.Run("foreign candidate flags even with error", func(t *testing.T) {
		r := &recorder{}
		impl := &fakeSimulator{findResult: []simulator.MatchedCandidate{foreign}, findErr: errors.New("boom")}
		_, _ = WrapForTest(r, impl).FindFeasibleNodes(ctx, candidatesSeq(in), &simulator.PodRequirements{}, &simulator.NodeExclusionStats{})
		if r.count() != 1 || !r.contains(`candidate "ghost" not present in inputs`) {
			t.Errorf("expected a single not-present-in-inputs violation, got %v", r.messages)
		}
	})
}

func TestPreemptWorkloadRevert(t *testing.T) {
	ctx := t.Context()

	t.Run("nil revert without error flags", func(t *testing.T) {
		r := &recorder{}
		_, _ = WrapForTest(r, &fakeSimulator{}).PreemptWorkload(ctx, key("ns", "wl"))
		if r.count() != 1 || !r.contains("nil revert function without an error") {
			t.Errorf("expected a single nil-revert violation, got %v", r.messages)
		}
	})

	t.Run("nil revert with error passes", func(t *testing.T) {
		r := &recorder{}
		impl := &fakeSimulator{preemptErr: errors.New("boom")}
		_, _ = WrapForTest(r, impl).PreemptWorkload(ctx, key("ns", "wl"))
		if r.count() != 0 {
			t.Errorf("expected no failures, got %d", r.count())
		}
	})
}

func TestScheduleWorkloadContract(t *testing.T) {
	ctx := t.Context()
	p := pod("ns", "p0")

	t.Run("matching success passes", func(t *testing.T) {
		r := &recorder{}
		impl := &fakeSimulator{scheduleResult: simulator.SchedulingResult{
			PodPlacements: simulator.PodPlacements{
				key("ns", "p0"): {Node: types.NodeName("node-1")},
			},
		}}
		_ = WrapForTest(r, impl).ScheduleWorkload(ctx, []*corev1.Pod{p})
		if r.count() != 0 {
			t.Errorf("expected no failures, got %d", r.count())
		}
	})

	t.Run("error with placements flags", func(t *testing.T) {
		r := &recorder{}
		impl := &fakeSimulator{scheduleResult: simulator.SchedulingResult{
			Error:         errors.New("boom"),
			PodPlacements: simulator.PodPlacements{key("ns", "p0"): {}},
		}}
		_ = WrapForTest(r, impl).ScheduleWorkload(ctx, []*corev1.Pod{p})
		if r.count() != 1 || !r.contains("placements alongside an error") {
			t.Errorf("expected a single placements-with-error violation, got %v", r.messages)
		}
	})

	t.Run("foreign key and count mismatch flags", func(t *testing.T) {
		r := &recorder{}
		impl := &fakeSimulator{scheduleResult: simulator.SchedulingResult{
			PodPlacements: simulator.PodPlacements{key("ns", "other"): {Node: "n"}},
		}}
		_ = WrapForTest(r, impl).ScheduleWorkload(ctx, []*corev1.Pod{p, pod("ns", "p1")})
		// count mismatch (1 vs 2) + foreign key => 2 failures.
		if r.count() != 2 || !r.contains("placements for") || !r.contains("not present in inputs") {
			t.Errorf("expected count-mismatch and foreign-key violations, got %v", r.messages)
		}
	})

	t.Run("node set with reasons flags", func(t *testing.T) {
		r := &recorder{}
		impl := &fakeSimulator{scheduleResult: simulator.SchedulingResult{
			PodPlacements: simulator.PodPlacements{
				key("ns", "p0"): {Node: "n", Reasons: []string{"dirty"}},
			},
		}}
		_ = WrapForTest(r, impl).ScheduleWorkload(ctx, []*corev1.Pod{p})
		if r.count() != 1 || !r.contains("not a clean success") {
			t.Errorf("expected a single not-a-clean-success violation, got %v", r.messages)
		}
	})
}
