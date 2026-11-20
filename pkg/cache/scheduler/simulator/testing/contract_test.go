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
	"iter"
	"testing"

	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

type recorder = Recorder

func wrapForTest(t TestReporter, impl simulator.SchedulerSimulator) simulator.SchedulerSimulator {
	sim, _ := WrapForTest(t, impl)
	return sim
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

func (f *fakeSimulator) AddPodGroup(context.Context, *schedulingv1beta1.PodGroup) error {
	return nil
}

func (f *fakeSimulator) RemovePodGroup(context.Context, *schedulingv1beta1.PodGroup) error {
	return nil
}

func (f *fakeSimulator) AddCompositePodGroup(context.Context, *schedulingv1alpha3.CompositePodGroup) error {
	return nil
}

func (f *fakeSimulator) RemoveCompositePodGroup(context.Context, *schedulingv1alpha3.CompositePodGroup) error {
	return nil
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
		_ = wrapForTest(r, &fakeSimulator{}).Simulate(ctx, func() { ran = true })
		if !ran {
			t.Fatal("inner fn should have run")
		}
		if r.Count() != 0 {
			t.Errorf("expected no failures, got %d", r.Count())
		}
	})

	t.Run("fn skipped flags", func(t *testing.T) {
		r := &recorder{}
		_ = wrapForTest(r, &fakeSimulator{simulateSkipFn: true}).Simulate(ctx, func() {})
		if r.Count() != 1 || !r.Contains("did not execute the provided function") {
			t.Errorf("expected a single fn-not-executed violation, got %v", r.Messages)
		}
	})
}

func TestFindFeasibleNodesSubset(t *testing.T) {
	ctx := t.Context()
	in := &fakeCandidate{id: "a"}
	foreign := &fakeCandidate{id: "ghost"}
	tests := map[string]struct {
		result []simulator.MatchedCandidate
		err    error
		count  int
		want   string
	}{
		"subset passes": {
			result: []simulator.MatchedCandidate{in},
		},
		"foreign candidate flags": {
			result: []simulator.MatchedCandidate{foreign},
			count:  1, want: `candidate "ghost" not present in inputs`,
		},
		"foreign candidate flags even with error": {
			result: []simulator.MatchedCandidate{foreign},
			err:    errors.New("boom"),
			count:  1, want: `candidate "ghost" not present in inputs`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r := &recorder{}
			impl := &fakeSimulator{findResult: tc.result, findErr: tc.err}
			_, _ = wrapForTest(r, impl).FindFeasibleNodes(ctx, candidatesSeq(in), &simulator.PodRequirements{}, &simulator.NodeExclusionStats{})
			if r.Count() != tc.count {
				t.Errorf("expected %d failures, got %v", tc.count, r.Messages)
			}
			if tc.want != "" && !r.Contains(tc.want) {
				t.Errorf("expected failure containing %q, got %v", tc.want, r.Messages)
			}
		})
	}
}

func TestPreemptWorkloadRevert(t *testing.T) {
	ctx := t.Context()

	t.Run("nil revert without error flags", func(t *testing.T) {
		r := &recorder{}
		_, _ = wrapForTest(r, &fakeSimulator{}).PreemptWorkload(ctx, key("ns", "wl"))
		if r.Count() != 1 || !r.Contains("nil revert function without an error") {
			t.Errorf("expected a single nil-revert violation, got %v", r.Messages)
		}
	})

	t.Run("nil revert with error passes", func(t *testing.T) {
		r := &recorder{}
		impl := &fakeSimulator{preemptErr: errors.New("boom")}
		_, _ = wrapForTest(r, impl).PreemptWorkload(ctx, key("ns", "wl"))
		if r.Count() != 0 {
			t.Errorf("expected no failures, got %d", r.Count())
		}
	})
}

func TestScheduleWorkloadContract(t *testing.T) {
	ctx := t.Context()
	p := pod("ns", "p0")
	tests := map[string]struct {
		pods         []*corev1.Pod
		result       simulator.SchedulingResult
		failures     int
		messageParts []string
	}{
		"matching success passes": {
			pods: []*corev1.Pod{p},
			result: simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				key("ns", "p0"): {Node: types.NodeName("node-1")},
			}},
		},
		"error with placements flags": {
			pods: []*corev1.Pod{p},
			result: simulator.SchedulingResult{
				Error: errors.New("boom"), PodPlacements: simulator.PodPlacements{key("ns", "p0"): {}},
			},
			failures: 1, messageParts: []string{"placements alongside an error"},
		},
		"foreign key and count mismatch flags": {
			pods: []*corev1.Pod{p, pod("ns", "p1")},
			result: simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				key("ns", "other"): {Node: "n"},
			}},
			failures: 2, messageParts: []string{"placements for", "not present in inputs"},
		},
		"node set with reasons flags": {
			pods: []*corev1.Pod{p},
			result: simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				key("ns", "p0"): {Node: "n", Reasons: []string{"dirty"}},
			}},
			failures: 1, messageParts: []string{"not a clean success"},
		},
		"failed placement passes": {
			pods: []*corev1.Pod{p},
			result: simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				key("ns", "p0"): {Error: errors.New("no node")},
			}},
		},
		"nil placement flags": {
			pods: []*corev1.Pod{p},
			result: simulator.SchedulingResult{PodPlacements: simulator.PodPlacements{
				key("ns", "p0"): nil,
			}},
			failures: 1, messageParts: []string{"nil placement for pod"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r := &recorder{}
			impl := &fakeSimulator{scheduleResult: tc.result}
			_ = wrapForTest(r, impl).ScheduleWorkload(ctx, tc.pods)
			if r.Count() != tc.failures {
				t.Errorf("expected %d failures, got %v", tc.failures, r.Messages)
			}
			for _, part := range tc.messageParts {
				if !r.Contains(part) {
					t.Errorf("expected failure containing %q, got %v", part, r.Messages)
				}
			}
		})
	}
}
