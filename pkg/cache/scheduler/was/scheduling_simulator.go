//go:build !exclude_scheduler_library

package was

import (
	"context"
	"fmt"
	"iter"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	schedulerconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/backend/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/defaultbinder"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodeaffinity"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodeports"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/queuesort"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/tainttoleration"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/scheduler-library/pkg/framework"
	schedLibSimulator "sigs.k8s.io/scheduler-library/pkg/simulator"
	schedLibSnapshot "sigs.k8s.io/scheduler-library/pkg/upstreamsync/snapshot"

	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/features"
)

type snapshotFactory func(ctx context.Context, pods []*corev1.Pod, nodes []*corev1.Node) (*schedLibSnapshot.ClusterSnapshot, error)

// podTracker maintains per-node pod state for scheduler plugins that need
// existing pod information.
type podTracker struct {
	mu sync.RWMutex
	// podsByNode maps node names to the set of tracked pods on that node.
	podsByNode map[string]map[types.UID]*corev1.Pod
	// podIndex maps each tracked pod to its current node so that TrackPod can
	// remove the old entry when a pod migrates between nodes.
	podIndex map[types.NamespacedName]podNodeInfo
}

// podNodeInfo records which node a tracked pod is placed on.
type podNodeInfo struct {
	nodeName string
	podUID   types.UID
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
			podsByNode: make(map[string]map[types.UID]*corev1.Pod),
			podIndex:   make(map[types.NamespacedName]podNodeInfo),
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

func (s *wasSimulator) NewFeasibilityChecker(ctx context.Context, nodes []*corev1.Node) (simulator.NodeFeasibilityChecker, error) {
	pods := s.pods.podsForNodes(nodes)
	clusterSnap, err := s.newSnapshot(ctx, pods, nodes)
	if err != nil {
		return nil, err
	}
	return &wasChecker{snap: clusterSnap}, nil
}

func (s *wasSimulator) TrackPod(pod *corev1.Pod) {
	s.pods.track(pod)
}

func (s *wasSimulator) UntrackPod(key types.NamespacedName) {
	s.pods.untrack(key)
}

func (t *podTracker) podsForNodes(nodes []*corev1.Node) []*corev1.Pod {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var pods []*corev1.Pod
	for _, node := range nodes {
		for _, pod := range t.podsByNode[node.Name] {
			pods = append(pods, pod)
		}
	}
	return pods
}

func (t *podTracker) track(pod *corev1.Pod) {
	t.mu.Lock()
	defer t.mu.Unlock()

	key := client.ObjectKeyFromObject(pod)
	if old, found := t.podIndex[key]; found {
		delete(t.podsByNode[old.nodeName], old.podUID)
		if len(t.podsByNode[old.nodeName]) == 0 {
			delete(t.podsByNode, old.nodeName)
		}
	}
	node := pod.Spec.NodeName
	if t.podsByNode[node] == nil {
		t.podsByNode[node] = make(map[types.UID]*corev1.Pod)
	}
	t.podsByNode[node][pod.UID] = pod
	t.podIndex[key] = podNodeInfo{nodeName: node, podUID: pod.UID}
}

func (t *podTracker) untrack(key types.NamespacedName) {
	t.mu.Lock()
	defer t.mu.Unlock()

	old, found := t.podIndex[key]
	if !found {
		return
	}
	delete(t.podsByNode[old.nodeName], old.podUID)
	if len(t.podsByNode[old.nodeName]) == 0 {
		delete(t.podsByNode, old.nodeName)
	}
	delete(t.podIndex, key)
}

type wasChecker struct {
	snap *schedLibSnapshot.ClusterSnapshot
}

func (c *wasChecker) FindFeasibleNodes(
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
	placement, err := c.snap.MakePlacement(candidateNodeNames)
	if err != nil {
		return nil, err
	}
	feasibleNodeNames, _, err := c.snap.CanSchedulePod(ctx, dummyPod, placement)
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
