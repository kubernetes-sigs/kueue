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
	"maps"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/kubernetes/pkg/scheduler/backend/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/scheduler-library/pkg/framework"
	schedLibSimulator "sigs.k8s.io/scheduler-library/pkg/simulator"
	schedLibSnapshot "sigs.k8s.io/scheduler-library/pkg/upstreamsync/snapshot"
)

var _ simulator.Factory = (*wasSimulatorFactory)(nil)

type wasSimulatorFactory struct {
	newSnapshot snapshotFactory
	pods        podTracker
}

type snapshotFactory func(ctx context.Context, pods []*corev1.Pod, nodes []*corev1.Node) (*schedLibSnapshot.ClusterSnapshot, error)

func newWASSimulatorFactory(ctx context.Context, client kubernetes.Interface) (*wasSimulatorFactory, error) {
	cfg := newWASSchedulerConfig()

	snapshotFn := func(ctx context.Context, pods []*corev1.Pod, nodes []*corev1.Node) (*schedLibSnapshot.ClusterSnapshot, error) {
		// Building the framework registers a DRA index on the factory it is given, so it
		// cannot be shared across snapshots, and the enabled plugins read the snapshot
		// rather than the informers, so it is not needed once the framework is built.
		buildCtx, cancelBuild := context.WithCancel(ctx)
		informerFactory := informers.NewSharedInformerFactory(client, 0)
		// Without the wait the goroutines outlive the call and keep logging through
		// the caller's context. Shutdown blocks, so it must follow cancelBuild.
		defer func() {
			cancelBuild()
			informerFactory.Shutdown()
		}()

		// Register node and pod informers with the factory; sync errors are caught by AsError() below.
		_ = informerFactory.Core().V1().Nodes().Informer()
		_ = informerFactory.Core().V1().Pods().Informer()
		informerFactory.StartWithContext(buildCtx)
		if err := informerFactory.WaitForCacheSyncWithContext(buildCtx).AsError(); err != nil {
			return nil, err
		}
		snap := cache.NewSnapshot(pods, nodes)
		profiles, err := framework.NewProfileMap(buildCtx, client, informerFactory, snap, cfg)
		if err != nil {
			return nil, err
		}
		return schedLibSnapshot.New(snap, profiles), nil
	}

	return &wasSimulatorFactory{
		newSnapshot: snapshotFn,
		pods: podTracker{
			pods:         make(podsByKey),
			workloadPods: make(podsByWorkload),
		},
	}, nil
}

func NewWASSimulatorFactory(ctx context.Context, restConfig *rest.Config) (*wasSimulatorFactory, error) {
	if restConfig != nil {
		// TODO(#13534): when DRA plugins are added, use a real client here
		// instead of the fake so the informer factory is populated.
		if _, err := schedLibSimulator.NewReadonlyClient(restConfig); err != nil {
			return nil, err
		}
	}
	return newWASSimulatorFactory(ctx, fake.NewSimpleClientset())
}

func (s *wasSimulatorFactory) NewSimulator(ctx context.Context, nodes []*corev1.Node, options ...simulator.Option) (simulator.SchedulerSimulator, error) {
	tracker := s.pods.copy()

	for _, wl := range simulator.AssumedWorkloads(options...) {
		vPods := VirtualPodsForWorkload(wl)
		if len(vPods) == 0 {
			continue
		}

		wlKey := client.ObjectKeyFromObject(wl)
		tracker.clearWorkload(wlKey)

		for _, vPod := range vPods {
			tracker.savePod(client.ObjectKeyFromObject(vPod), vPod)
		}
	}

	allPods := tracker.pods.toSlice()
	clusterSnap, err := s.newSnapshot(ctx, allPods, nodes)
	if err != nil {
		return nil, err
	}
	snapshot := &wasSimulator{
		wasSnapshot:    clusterSnap,
		podsByWorkload: tracker.workloadPods,
	}
	snapshot.emptyCluster.build = func(ctx context.Context) (*schedLibSnapshot.ClusterSnapshot, error) {
		return s.newSnapshot(ctx, podsNotManagedByKueue(allPods, tracker.workloadPods), nodes)
	}

	return snapshot, nil
}

// podsNotManagedByKueue returns the Pods that belong to no Workload. Preemption
// cannot remove them, so they keep occupying their node even when the caller assumes
// every Workload is gone.
func podsNotManagedByKueue(allPods []*corev1.Pod, byWorkload podsByWorkload) []*corev1.Pod {
	managed := sets.New[client.ObjectKey]()
	for _, pods := range byWorkload {
		managed.Insert(slices.Collect(maps.Keys(pods))...)
	}
	var kept []*corev1.Pod
	for _, pod := range allPods {
		if !managed.Has(client.ObjectKeyFromObject(pod)) {
			kept = append(kept, pod)
		}
	}
	return kept
}

func (s *wasSimulatorFactory) TrackPod(ctx context.Context, pod *corev1.Pod) {
	if _, ok := pod.Annotations[kueue.WorkloadAnnotation]; !ok {
		ctrl.LoggerFrom(ctx).V(1).Info(
			"Missing annotation on Pod object; Quality of WAS simulation may be degraded.",
			"pod", client.ObjectKeyFromObject(pod).String(),
			"missing annotation", kueue.WorkloadAnnotation,
		)
	}
	s.pods.track(pod)
}

func (s *wasSimulatorFactory) UntrackPod(_ context.Context, key client.ObjectKey) {
	s.pods.untrack(key)
}
