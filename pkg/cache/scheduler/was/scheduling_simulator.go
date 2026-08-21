//go:build !exclude_scheduler_library

package was

import (
	"context"
	"fmt"
	"iter"
	"maps"
	"slices"
	"sync"

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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/scheduler-library/pkg/framework"
	schedLibSimulator "sigs.k8s.io/scheduler-library/pkg/simulator"
	schedLibSnapshot "sigs.k8s.io/scheduler-library/pkg/upstreamsync/snapshot"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
)

var PodWorkloadAnnotations = []string{
	kueue.WorkloadAnnotation,
	kueue.WorkloadSliceNameAnnotation,
	controllerconstants.PrebuiltWorkloadAnnotation,
	podconstants.GroupNameAnnotation,
}

type snapshotFactory func(ctx context.Context, pods []*corev1.Pod, nodes []*corev1.Node) (*schedLibSnapshot.ClusterSnapshot, error)
type podSet map[client.ObjectKey]*corev1.Pod
type podsBreakdown map[client.ObjectKey]podSet

// podTracker maintains pod state for scheduler plugins that need
// existing pod information.
type podTracker struct {
	sync.RWMutex
	pods           podSet
	workloadPods   podsBreakdown
	unassignedPods podSet
}

type wasSimulator struct {
	newSnapshot snapshotFactory
	pods        podTracker
}

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

func newWASSimulator(ctx context.Context, client kubernetes.Interface) (simulator.SchedulingSimulator, error) {
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
			pods:           make(podSet),
			workloadPods:   make(podsBreakdown),
			unassignedPods: make(podSet),
		},
	}, nil
}

// NewWASSimulatorForTest creates a WAS simulator backed by a fake client,
// suitable for unit tests that need the full production plugin pipeline.
func NewWASSimulatorForTest(ctx context.Context) (simulator.SchedulingSimulator, error) {
	return newWASSimulator(ctx, fake.NewSimpleClientset())
}

func NewWASSimulator(ctx context.Context, restConfig *rest.Config) (simulator.SchedulingSimulator, error) {
	// TODO(#13534): when DRA plugins are added, use a real client here
	// instead of the fake so the informer factory is populated.
	if _, err := schedLibSimulator.NewReadonlyClient(restConfig); err != nil {
		return nil, err
	}
	return newWASSimulator(ctx, fake.NewSimpleClientset())
}

type wasSimulatorSnapshot struct {
	wasSnapshot    *schedLibSnapshot.ClusterSnapshot
	podsByWorkload *podsBreakdown
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

func (s *wasSimulator) TrackPod(pod *corev1.Pod) {
	s.pods.track(pod)
}

func (s *wasSimulator) UntrackPod(key client.ObjectKey) {
	s.pods.untrack(key)
}

func (m podsBreakdown) getPodsForWorkload(wlKey client.ObjectKey) []*corev1.Pod {
	podSet, ok := m[wlKey]
	if !ok {
		return nil
	}
	return slices.Collect(maps.Values(podSet))
}

func (t *podTracker) snapshot() (allPods []*corev1.Pod, podsByWorkload *podsBreakdown) {
	t.RLock()
	defer t.RUnlock()

	allPods = slices.Collect(maps.Values(t.pods))
	if len(t.unassignedPods) > 0 {
		// Determining which pods belong to what workload is impossible
		// if any pod cannot identify its workload.
		return
	}

	podsByWorkload = &podsBreakdown{}
	maps.Copy(*podsByWorkload, t.workloadPods)
	return
}

func (t *podTracker) track(pod *corev1.Pod) {
	t.Lock()
	defer t.Unlock()

	podKey := client.ObjectKeyFromObject(pod)
	t.pods[podKey] = pod

	wl, found := workloadName(pod)
	if !found {
		t.unassignedPods[podKey] = pod
		return
	}

	wlKey := client.ObjectKey{Namespace: pod.Namespace, Name: wl}
	if _, ok := t.workloadPods[wlKey]; !ok {
		t.workloadPods[wlKey] = make(podSet)
	}
	t.workloadPods[wlKey][podKey] = pod
}

func (t *podTracker) untrack(key client.ObjectKey) {
	t.Lock()
	defer t.Unlock()

	pod, ok := t.pods[key]
	if !ok {
		return
	}

	delete(t.pods, key)

	wl, found := workloadName(pod)
	if !found {
		delete(t.unassignedPods, key)
		return
	}

	wlKey := client.ObjectKey{Namespace: pod.Namespace, Name: wl}
	delete(t.workloadPods[wlKey], key)
	if len(t.workloadPods[wlKey]) == 0 {
		delete(t.workloadPods, wlKey)
	}
}

func workloadName(pod *corev1.Pod) (string, bool) {
	for _, annotation := range PodWorkloadAnnotations {
		if wl, ok := pod.Annotations[annotation]; ok {
			return wl, true
		}
	}
	return "", false
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

func (s *wasSimulatorSnapshot) PreemptWorkload(ctx context.Context, wlKey client.ObjectKey) (revertFunc func(), err error) {
	if s.podsByWorkload == nil {
		// Unable to identify which pods belong to any workload.
		return func() {}, nil
	}

	unpreempt, err := s.wasSnapshot.PreemptPods(ctx, s.podsByWorkload.getPodsForWorkload(wlKey))
	if err != nil {
		return nil, err
	}

	return func() {
		s.wasSnapshot.Unpreempt(unpreempt)
	}, nil
}
