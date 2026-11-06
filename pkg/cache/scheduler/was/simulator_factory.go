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
	"sync"

	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	schedulerconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/defaultbinder"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/defaultpreemption"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/gangscheduling"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodeaffinity"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodeports"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/noderesources"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodeunschedulable"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/queuesort"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/tainttoleration"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/topologyaware"
	"sigs.k8s.io/controller-runtime/pkg/client"
	schedLibSimulator "sigs.k8s.io/scheduler-library/pkg/simulator"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/podtracker"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/workload"
)

var _ simulator.Factory = (*wasSimulatorFactory)(nil)

// offlineHost is the API server address used when no rest config is given, e.g. in
// unit tests. The ".invalid" TLD never resolves, and the read-only client rejects
// mutating requests before they leave the process, so nothing reaches a cluster.
const offlineHost = "https://no-apiserver.invalid"

type wasSimulatorFactory struct {
	sim *schedLibSimulator.SchedulingSimulator
	// mu serializes snapshot creation: SchedulingSimulator.NewClusterSnapshot is not
	// safe for concurrent use, while Cache.Snapshot may run concurrently (e.g. the
	// scheduler and the debugger), and the empty cluster is built lazily mid-cycle.
	mu sync.Mutex
}

func newWASSimulatorFactory(
	ctx context.Context,
	cfg *schedulerconfig.KubeSchedulerConfiguration,
	roClient schedLibSimulator.ReadonlyClient,
	opts ...schedLibSimulator.SimulatorOption,
) (*wasSimulatorFactory, error) {
	// The enabled plugins read the cluster from the snapshot rather than from the
	// informers, so back the factory with a fake client to keep the scheduler-library
	// from watching the cluster on its own.
	// TODO(#13534): when DRA plugins are enabled, give them the real cluster state,
	// e.g. through a real-client informer factory or schedLibSimulator.WithSharedDRAManager.
	informerFactory := informers.NewSharedInformerFactory(fake.NewClientset(), 0)
	sim, err := schedLibSimulator.NewSchedulingSimulator(ctx, cfg, roClient, informerFactory, opts...)
	if err != nil {
		return nil, err
	}
	return &wasSimulatorFactory{sim: sim}, nil
}

func (s *wasSimulatorFactory) newSnapshot(
	ctx context.Context,
	pods []*corev1.Pod,
	nodes []*corev1.Node,
	podGroups []*schedulingv1beta1.PodGroup,
) (schedLibSimulator.Simulator, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sim.NewClusterSnapshot(ctx, pods, nodes, podGroups, nil)
}

func newWASSchedulerConfig() *schedulerconfig.KubeSchedulerConfiguration {
	nodeFilters := []schedulerconfig.Plugin{
		{Name: nodeaffinity.Name},
		{Name: nodeports.Name},
	}
	config := []schedulerconfig.PluginConfig{
		{
			Name: nodeaffinity.Name,
			Args: &schedulerconfig.NodeAffinityArgs{},
		},
	}
	var podGroupPostFilter []schedulerconfig.Plugin
	if features.Enabled(features.SchedulerLibraryDeepIntegration) {
		nodeFilters = append(nodeFilters, schedulerconfig.Plugin{Name: noderesources.Name})
		podGroupPostFilter = append(podGroupPostFilter, schedulerconfig.Plugin{Name: defaultpreemption.Name})
		config = append(config,
			schedulerconfig.PluginConfig{
				Name: noderesources.Name,
				Args: &schedulerconfig.NodeResourcesFitArgs{
					ScoringStrategy: &schedulerconfig.ScoringStrategy{
						Type: schedulerconfig.LeastAllocated,
					},
				},
			},
			schedulerconfig.PluginConfig{
				Name: defaultpreemption.Name,
				Args: &schedulerconfig.DefaultPreemptionArgs{
					MinCandidateNodesPercentage: 10,
					MinCandidateNodesAbsolute:   100,
				},
			},
		)
	}
	return &schedulerconfig.KubeSchedulerConfiguration{
		Profiles: []schedulerconfig.KubeSchedulerProfile{
			{
				SchedulerName: corev1.DefaultSchedulerName,
				// https://kubernetes.io/docs/reference/scheduling/config/#scheduling-plugins
				Plugins: &schedulerconfig.Plugins{
					MultiPoint: schedulerconfig.PluginSet{
						Enabled: []schedulerconfig.Plugin{{Name: gangscheduling.Name}},
					},
					QueueSort: schedulerconfig.PluginSet{
						Enabled: []schedulerconfig.Plugin{{Name: queuesort.Name}},
					},
					Bind: schedulerconfig.PluginSet{
						Enabled: []schedulerconfig.Plugin{{Name: defaultbinder.Name}},
					},
					Filter: schedulerconfig.PluginSet{
						Enabled: append([]schedulerconfig.Plugin{
							{Name: nodeunschedulable.Name},
							{Name: tainttoleration.Name},
						}, nodeFilters...),
					},
					PreFilter: schedulerconfig.PluginSet{
						Enabled: nodeFilters,
					},
					PlacementGenerate: schedulerconfig.PluginSet{
						Enabled: []schedulerconfig.Plugin{{Name: topologyaware.Name}},
					},
					PodGroupPostFilter: schedulerconfig.PluginSet{
						Enabled: podGroupPostFilter,
					},
				},
				PluginConfig: config,
			},
		},
	}
}

// NewWASSimulatorFactory creates the scheduler-library backed simulator factory.
// A nil restConfig means there is no API server to talk to, e.g. in unit tests.
func NewWASSimulatorFactory(ctx context.Context, restConfig *rest.Config) (*wasSimulatorFactory, error) {
	if restConfig == nil {
		restConfig = &rest.Config{Host: offlineHost}
	}
	roClient, err := schedLibSimulator.NewReadonlyClient(restConfig)
	if err != nil {
		return nil, err
	}
	return newWASSimulatorFactory(ctx, newWASSchedulerConfig(), roClient)
}

func (s *wasSimulatorFactory) NewSimulator(ctx context.Context, nodes []*corev1.Node, pods *podtracker.Tracker, options ...simulator.Option) (simulator.SchedulerSimulator, error) {
	tracker := pods.Clone()

	for _, wl := range simulator.AssumedWorkloads(options...) {
		vPods := VirtualPodsForWorkload(wl)
		if len(vPods) == 0 {
			continue
		}

		wlKey := workload.Key(wl)
		tracker.RemoveWorkload(wlKey)

		for _, vPod := range vPods {
			tracker.Track(vPod)
		}
	}

	allPods := tracker.Pods()
	clusterSnap, err := s.newSnapshot(ctx, allPods, nodes, nil)
	if err != nil {
		return nil, err
	}
	snapshot := &wasSimulator{
		wasSnapshot:        clusterSnap,
		pods:               tracker,
		podGroups:          make(map[client.ObjectKey]*schedulingv1beta1.PodGroup),
		compositePodGroups: make(map[client.ObjectKey]*schedulingv1alpha3.CompositePodGroup),
	}
	snapshot.emptyCluster.build = func(ctx context.Context) (schedLibSimulator.Simulator, error) {
		return s.newSnapshot(ctx, podsNotManagedByKueue(allPods), nodes, nil)
	}

	return snapshot, nil
}

// podsNotManagedByKueue returns the Pods that belong to no Workload. Preemption
// cannot remove them, so they keep occupying their node even when the caller assumes
// every Workload is gone.
func podsNotManagedByKueue(allPods []*corev1.Pod) []*corev1.Pod {
	var kept []*corev1.Pod
	for _, pod := range allPods {
		if pod.Annotations[kueue.WorkloadAnnotation] == "" {
			kept = append(kept, pod)
		}
	}
	return kept
}
