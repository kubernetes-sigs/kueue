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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	schedulerconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/backend/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/defaultbinder"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodeaffinity"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodeports"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodeunschedulable"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/queuesort"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/tainttoleration"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/scheduler-library/pkg/framework"
	schedLibSimulator "sigs.k8s.io/scheduler-library/pkg/simulator"
	schedLibSnapshot "sigs.k8s.io/scheduler-library/pkg/upstreamsync/snapshot"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/features"
)

type snapshotFactory func(ctx context.Context, pods []*corev1.Pod, nodes []*corev1.Node) (*schedLibSnapshot.ClusterSnapshot, error)

type wasSimulator struct {
	newSnapshot snapshotFactory
	pods        podTracker
}

type wasSimulatorSnapshot struct {
	wasSnapshot    *schedLibSnapshot.ClusterSnapshot
	podsByWorkload podsByWorkload
}

var _ simulator.SimulatorSnapshot = (*wasSimulatorSnapshot)(nil)

func newWASSchedulerConfig() *schedulerconfig.KubeSchedulerConfiguration {
	return &schedulerconfig.KubeSchedulerConfiguration{
		Profiles: []schedulerconfig.KubeSchedulerProfile{
			{
				SchedulerName: "default-scheduler",
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
							{Name: nodeports.Name},
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

func newWASSimulator(ctx context.Context, client kubernetes.Interface) (*wasSimulator, error) {
	cfg := newWASSchedulerConfig()
	informerFactory := informers.NewSharedInformerFactory(client, 0)

	// Register node and pod informers with the factory; sync errors are caught by AsError() below.
	_ = informerFactory.Core().V1().Nodes().Informer()
	_ = informerFactory.Core().V1().Pods().Informer()
	informerFactory.StartWithContext(ctx)
	if err := informerFactory.WaitForCacheSyncWithContext(ctx).AsError(); err != nil {
		return nil, err
	}

	snapshotFn := func(ctx context.Context, pods []*corev1.Pod, nodes []*corev1.Node) (*schedLibSnapshot.ClusterSnapshot, error) {
		snap := cache.NewSnapshot(pods, nodes)
		profiles, err := framework.NewProfileMap(ctx, client, informerFactory, snap, cfg)
		if err != nil {
			return nil, err
		}
		return schedLibSnapshot.New(snap, profiles), nil
	}

	return &wasSimulator{
		newSnapshot: snapshotFn,
		pods: podTracker{
			pods:         make(podsByKey),
			workloadPods: make(podsByWorkload),
		},
	}, nil
}

func NewWASSimulator(ctx context.Context, restConfig *rest.Config) (*wasSimulator, error) {
	if restConfig != nil {
		// TODO(#13534): when DRA plugins are added, use a real client here
		// instead of the fake so the informer factory is populated.
		if _, err := schedLibSimulator.NewReadonlyClient(restConfig); err != nil {
			return nil, err
		}
	}
	return newWASSimulator(ctx, fake.NewSimpleClientset())
}

func (s *wasSimulator) Snapshot(ctx context.Context, nodes []*corev1.Node) (simulator.SimulatorSnapshot, error) {
	allPods, podsByWorkload := s.pods.snapshot()
	clusterSnap, err := s.newSnapshot(ctx, allPods, nodes)
	if err != nil {
		return nil, err
	}
	return &wasSimulatorSnapshot{
		wasSnapshot:    clusterSnap,
		podsByWorkload: podsByWorkload,
	}, nil
}

func (s *wasSimulator) TrackPod(ctx context.Context, pod *corev1.Pod) {
	if _, ok := pod.Annotations[kueue.WorkloadAnnotation]; !ok {
		ctrl.LoggerFrom(ctx).V(1).Info(
			"Missing annotation on Pod object; Quality of WAS simulation may be degraded.",
			"pod", client.ObjectKeyFromObject(pod).String(),
			"missing annotation", kueue.WorkloadAnnotation,
		)
	}
	s.pods.track(pod)
}

func (s *wasSimulator) UntrackPod(_ context.Context, key client.ObjectKey) {
	s.pods.untrack(key)
}

func (s *wasSimulatorSnapshot) FindFeasibleNodes(
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
	placement, err := s.wasSnapshot.MakePlacement(candidateNodeNames)
	if err != nil {
		return nil, err
	}
	feasibleNodeNames, _, err := s.wasSnapshot.CanSchedulePod(ctx, dummyPod, placement)
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

func (s *wasSimulatorSnapshot) PreemptWorkload(ctx context.Context, wlKey client.ObjectKey) (func() error, error) {
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

func (s *wasSimulatorSnapshot) Simulate(ctx context.Context, fn func()) error {
	return s.wasSnapshot.Transaction(ctx, func() (schedLibSnapshot.TransactionResult, error) {
		fn()
		return schedLibSnapshot.Revert, nil
	})
}
