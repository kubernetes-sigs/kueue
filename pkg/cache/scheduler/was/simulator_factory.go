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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	schedulerconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	schedLibSimulator "sigs.k8s.io/scheduler-library/pkg/simulator"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/podtracker"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/workload"
)

var _ simulator.Factory = (*wasSimulatorFactory)(nil)

// offlineHost is the API server address used when no rest config is given, e.g. in
// unit tests. The ".invalid" TLD never resolves, and the read-only client rejects
// mutating requests before they leave the process, so nothing reaches a cluster.
const offlineHost = "https://no-apiserver.invalid"

type wasSimulatorFactory struct {
	schedSim    *schedLibSimulator.SchedulingSimulator
	mu          sync.Mutex
	virtualPods *virtualPodsPerWorkload
}

const (
	feasibilityProfileScheduler = "feasibilityProfileScheduler"
	placementProfileScheduler   = "placementProfileScheduler"
)

// newWASSimulatorFactory builds the factory for the given scheduler configuration.
// A nil restConfig means there is no API server to talk to, e.g. in unit tests.
func newWASSimulatorFactory(
	ctx context.Context,
	restConfig *rest.Config,
	cfg *schedulerconfig.KubeSchedulerConfiguration,
	opts ...schedLibSimulator.SimulatorOption,
) (*wasSimulatorFactory, error) {
	if restConfig == nil {
		restConfig = &rest.Config{Host: offlineHost}
	}
	roClient, err := schedLibSimulator.NewReadonlyClient(restConfig)
	if err != nil {
		return nil, err
	}
	// The enabled plugins read the cluster from the snapshot rather than from the
	// informers, so back the factory with a fake client to keep the scheduler-library
	// from watching the cluster on its own.
	// TODO(#13534): when DRA plugins are enabled, give them the real cluster state,
	// e.g. through a real-client informer factory or schedLibSimulator.WithSharedDRAManager.
	informerFactory := informers.NewSharedInformerFactory(fake.NewClientset(), 0)
	schedSim, err := schedLibSimulator.NewSchedulingSimulator(withWASLogger(ctx), cfg, roClient, informerFactory, opts...)
	if err != nil {
		return nil, err
	}
	return &wasSimulatorFactory{
		schedSim:    schedSim,
		virtualPods: newVirtualPodsPerWorkload(),
	}, nil
}

func (s *wasSimulatorFactory) newSnapshot(
	ctx context.Context,
	pods []*corev1.Pod,
	nodes []*corev1.Node,
	podGroups []*schedulingv1beta1.PodGroup,
) (schedLibSimulator.Simulator, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.schedSim.NewClusterSnapshot(withWASLogger(ctx), pods, nodes, podGroups, nil)
}

func newWASSchedulerConfig() *schedulerconfig.KubeSchedulerConfiguration {
	feasibilityProfile := newProfileBuilder(feasibilityProfileScheduler).
		withQueueSort().
		withDefaultBinder().
		withNodeUnschedulable().
		withTaintToleration().
		withNodeAffinity().
		withNodePorts().
		withGangScheduling().
		build()

	placementProfile := newProfileBuilder(placementProfileScheduler).
		withQueueSort().
		withDefaultBinder().
		withNodeUnschedulable().
		withTaintToleration().
		withNodeAffinity().
		withNodePorts().
		withGangScheduling().
		withNodeResourcesFit(schedulerconfig.LeastAllocated).
		withTopologyAware().
		withDefaultPreemption(10, 100).
		build()

	return &schedulerconfig.KubeSchedulerConfiguration{
		Profiles: []schedulerconfig.KubeSchedulerProfile{
			feasibilityProfile,
			placementProfile,
		},
	}
}

// NewWASSimulatorFactory creates the scheduler-library backed simulator factory.
// A nil restConfig means there is no API server to talk to, e.g. in unit tests.
func NewWASSimulatorFactory(ctx context.Context, restConfig *rest.Config) (*wasSimulatorFactory, error) {
	return newWASSimulatorFactory(ctx, restConfig, newWASSchedulerConfig())
}

func (s *wasSimulatorFactory) NewSimulator(ctx context.Context, nodes []*corev1.Node, pods *podtracker.Tracker, options ...simulator.Option) (simulator.SchedulerSimulator, error) {
	tracker := pods.Clone()

	assumedWls := simulator.AssumedWorkloads(options...)
	activeUIDs := sets.New[types.UID]()

	for _, wl := range assumedWls {
		activeUIDs.Insert(wl.UID)

		vPods := s.getVirtualPods(wl)
		if len(vPods) == 0 {
			continue
		}

		wlKey := workload.Key(wl)
		tracker.RemoveWorkload(wlKey)

		for _, vPod := range vPods {
			tracker.Track(vPod)
		}
	}

	// Prune workloads that are no longer assumed active
	s.virtualPods.prune(activeUIDs)

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

func (s *wasSimulatorFactory) getVirtualPods(wl *kueue.Workload) []*corev1.Pod {
	if cached, ok := s.virtualPods.get(wl.UID); ok && cached.workloadResourceVersion == wl.ResourceVersion {
		return cached.pods
	}

	vPods := VirtualPodsForWorkload(wl)

	// TODO: Explicit invalidation will be needed for node hotswap (UpdateWorkloadIfUnchanged)
	if vPods.Pods() == nil {
		s.virtualPods.delete(wl.UID)
	} else {
		s.virtualPods.set(wl.UID, vPods)
	}

	return vPods.Pods()
}

func withWASLogger(ctx context.Context) context.Context {
	return ctrl.LoggerInto(ctx, ctrl.LoggerFrom(ctx).WithName("was"))
}
