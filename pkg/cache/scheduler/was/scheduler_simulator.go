//go:build !exclude_scheduler_library

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
	"fmt"
	"iter"
	"sync"

	corev1 "k8s.io/api/core/v1"
	schedulerconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/defaultbinder"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodeaffinity"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodeports"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodeunschedulable"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/queuesort"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/tainttoleration"
	"sigs.k8s.io/controller-runtime/pkg/client"
	schedLibSnapshot "sigs.k8s.io/scheduler-library/pkg/upstreamsync/snapshot"

	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/features"
)

var _ simulator.SchedulerSimulator = (*wasSimulator)(nil)

type wasSimulator struct {
	// wasSnapshot is the cluster as it stands, with every tracked Pod on its node.
	wasSnapshot *schedLibSnapshot.ClusterSnapshot
	// podsByWorkload indexes the tracked Pods by the Workload that owns them, which
	// is the only set PreemptWorkload can release.
	podsByWorkload podsByWorkload
	// emptyCluster holds the same nodes with no Pods, for callers asking what would
	// fit if nothing were running.
	emptyCluster lazyCluster
}

// lazyCluster builds its cluster on first use, so cycles that never ask do not
// pay for it.
type lazyCluster struct {
	// build produces the cluster. It runs at most once.
	build func(context.Context) (*schedLibSnapshot.ClusterSnapshot, error)
	once  sync.Once
	// value and err hold what build returned, and are only read after once has run.
	value *schedLibSnapshot.ClusterSnapshot
	err   error
}

func (l *lazyCluster) get(ctx context.Context) (*schedLibSnapshot.ClusterSnapshot, error) {
	l.once.Do(func() {
		l.value, l.err = l.build(ctx)
	})
	return l.value, l.err
}

func newWASSchedulerConfig() *schedulerconfig.KubeSchedulerConfiguration {
	return &schedulerconfig.KubeSchedulerConfiguration{
		Profiles: []schedulerconfig.KubeSchedulerProfile{
			{
				SchedulerName: corev1.DefaultSchedulerName,
				// https://kubernetes.io/docs/reference/scheduling/config/#scheduling-plugins
				Plugins: &schedulerconfig.Plugins{
					QueueSort: schedulerconfig.PluginSet{
						Enabled: []schedulerconfig.Plugin{{Name: queuesort.Name}},
					},
					Bind: schedulerconfig.PluginSet{
						Enabled: []schedulerconfig.Plugin{{Name: defaultbinder.Name}},
					},
					Filter: schedulerconfig.PluginSet{
						Enabled: []schedulerconfig.Plugin{
							{Name: nodeunschedulable.Name},
							{Name: tainttoleration.Name},
							{Name: nodeaffinity.Name},
						},
					},
					PreFilter: schedulerconfig.PluginSet{
						Enabled: []schedulerconfig.Plugin{
							{Name: nodeaffinity.Name},
							{Name: nodeports.Name},
						},
					},
				},
				PluginConfig: []schedulerconfig.PluginConfig{
					{
						Name: nodeaffinity.Name,
						Args: &schedulerconfig.NodeAffinityArgs{},
					},
				},
			},
		},
	}
}

func (s *wasSimulator) FindFeasibleNodes(
	ctx context.Context,
	candidates iter.Seq[simulator.Candidate],
	requirements *simulator.PodRequirements,
	stats *simulator.NodeExclusionStats,
) ([]simulator.MatchedCandidate, error) {
	var candidateLeaves = make(map[string]simulator.MatchedCandidate)
	var candidateNodeNames []string
	var feasibleCandidates []simulator.MatchedCandidate

	for candidate := range candidates {
		matchedCandidate, ok := candidate.(simulator.MatchedCandidate)
		if !ok {
			return nil, fmt.Errorf("failed to cast candidate %T to simulator.MatchedCandidate", candidate)
		}

		stats.TotalNodes++
		nodeObj := candidate.GetNode()
		candidateNodeNames = append(candidateNodeNames, nodeObj.Name)
		candidateLeaves[nodeObj.Name] = matchedCandidate
	}

	dummyPod := &corev1.Pod{
		ObjectMeta: requirements.PodTemplate.ObjectMeta,
		Spec:       requirements.PodTemplate.Spec,
	}
	// The simulator builds one profile, so judge the Pod by it rather than by the scheduler it names.
	dummyPod.Spec.SchedulerName = corev1.DefaultSchedulerName
	cluster := s.wasSnapshot
	if requirements.SimulateEmpty {
		var err error
		if cluster, err = s.emptyCluster.get(ctx); err != nil {
			return nil, err
		}
	}
	placement, err := cluster.MakePlacement(candidateNodeNames)
	if err != nil {
		return nil, err
	}
	feasibleNodeNames, _, err := cluster.CanSchedulePod(ctx, dummyPod, placement)
	if err != nil {
		return nil, err
	}

	for _, nodeName := range feasibleNodeNames {
		leaf := candidateLeaves[nodeName]
		feasibleCandidates = append(feasibleCandidates, leaf)
		if features.Enabled(features.TASRespectNodeAffinityPreferred) && requirements.PreferredSchedulingTerms != nil {
			newAffinityScore := leaf.GetAffinityScore() + requirements.PreferredSchedulingTerms.Score(leaf.GetNode())
			leaf.SetAffinityScore(newAffinityScore)
		}
	}
	stats.SchedulerLibraryNoFit = len(candidateNodeNames) - len(feasibleNodeNames)

	return feasibleCandidates, nil
}

func (s *wasSimulator) PreemptWorkload(ctx context.Context, wlKey client.ObjectKey) (func() error, error) {
	// Pods with indeterminate workloads are not stored in s.podsByWorkload and are omitted from preemptions.
	// This means the simulation may be more restrictive than the real scheduler would be,
	// if the preempted workload has pods that do not identify with it directly.
	unpreempt, err := s.wasSnapshot.PreemptPods(ctx, s.podsByWorkload.getPodsForWorkload(wlKey))
	if err != nil {
		return nil, fmt.Errorf("failed to preempt workload's pods from WAS snapshot: %w", err)
	}

	return func() error {
		_, err := s.wasSnapshot.Unpreempt(unpreempt)
		return err
	}, nil
}

func (s *wasSimulator) Simulate(ctx context.Context, fn func()) error {
	return s.wasSnapshot.Transaction(ctx, func() (schedLibSnapshot.TransactionResult, error) {
		fn()
		return schedLibSnapshot.Revert, nil
	})
}
