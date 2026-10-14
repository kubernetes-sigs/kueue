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
	"iter"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
)

var _ SchedulerSimulator = (*CachingSimulator)(nil)

type CachingSimulator struct {
	SchedulerSimulator SchedulerSimulator

}

func (c *CachingSimulator) ScheduleWorkload(ctx context.Context, workloadPods []*corev1.Pod, opts ...simulator.ScheduleOption) simulator.SchedulingResult {

}

func (c *CachingSimulator) Simulate(ctx context.Context, fn func()) error {

}

func (c *CachingSimulator) FindFeasibleNodes(ctx context.Context, candidates iter.Seq[simulator.Candidate], requirements *simulator.PodRequirements, stats *simulator.NodeExclusionStats) ([]simulator.MatchedCandidate, error) {
}

func (c *CachingSimulator) PreemptWorkload(ctx context.Context, wlKey client.ObjectKey) (revert func() error, err error) {
}

func (c *CachingSimulator) ScheduleWorkload(ctx context.Context, workloadPods []*corev1.Pod, opts ...simulator.ScheduleOption) simulator.SchedulingResult {
}

func (c *CachingSimulator) FindFeasibleNodes(ctx context.Context, candidates iter.Seq[simulator.Candidate], requirements *simulator.PodRequirements, stats *simulator.NodeExclusionStats) ([]simulator.MatchedCandidate, error) {
	return c.FindFeasibleNodes(ctx, candidates, requirements, stats)
}

type

type Simulate func(ctx context.Context, fn func()) error
type FindFeasibleNodes func(ctx context.Context, candidates iter.Seq[simulator.Candidate], requirements *simulator.PodRequirements, stats *simulator.NodeExclusionStats) ([]simulator.MatchedCandidate, error)
type PreemptWorkload func(ctx context.Context, wlKey client.ObjectKey) (revert func() error, err error)
type ScheduleWorkload func(ctx context.Context, workloadPods []*corev1.Pod, opts ...simulator.ScheduleOption) simulator.SchedulingResult
