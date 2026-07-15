//go:build !exclude_scheduler_library

package scheduler

import (
	"context"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	schedulerconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/defaultbinder"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodeaffinity"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/queuesort"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/tainttoleration"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/scheduler-library/pkg/simulator"
	"sigs.k8s.io/scheduler-library/pkg/upstreamsync/snapshot"
)

type wasSimulator struct {
	sim *simulator.SchedulingSimulator
}

func NewWASSimulator(ctx context.Context, restConfig *rest.Config) (SchedulingSimulator, error) {
	cfg := &schedulerconfig.KubeSchedulerConfiguration{
		Profiles: []schedulerconfig.KubeSchedulerProfile{
			{
				SchedulerName: "default-scheduler",
				// List of plugins available in the Kubernetes scheduler by default:
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
						},
					},
					PreFilter: schedulerconfig.PluginSet{
						Enabled: []schedulerconfig.Plugin{
							{Name: nodeaffinity.Name},
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

	roClient, err := simulator.NewReadonlyClient(restConfig)
	if err != nil {
		return nil, err
	}

	fakeClient := fake.NewSimpleClientset()
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 0)

	sim, err := simulator.NewSchedulingSimulator(ctx, cfg, roClient, informerFactory)
	if err != nil {
		return nil, err
	}

	return &wasSimulator{sim: sim}, nil
}

func (s *wasSimulator) NewFeasibilityChecker(ctx context.Context, nodes []*nodeInfo) (NodeFeasibilityChecker, error) {
	var v1Nodes []*corev1.Node
	for _, n := range nodes {
		v1Nodes = append(v1Nodes, n.toNode())
	}
	clusterSnap, err := s.sim.NewClusterSnapshot(ctx, nil, v1Nodes)
	return &wasChecker{snap: clusterSnap}, err
}

type wasChecker struct {
	snap *snapshot.ClusterSnapshot
}

func (c *wasChecker) FindFeasibleLeaves(ctx context.Context, log logr.Logger, leaves []*leafDomain, reqs *topologyAssignmentPodRequirements, stats *ExclusionStats) ([]*leafDomain, error) {
	var candidateLeaves = make(map[string]*leafDomain)
	var candidateNodeNames []string

	for _, leaf := range leaves {
		stats.TotalNodes++
		candidateNodeNames = append(candidateNodeNames, leaf.node.Name)
		candidateLeaves[leaf.node.Name] = leaf
	}

	dummyPod := &corev1.Pod{
		ObjectMeta: reqs.podTemplate.ObjectMeta,
		Spec:       reqs.podTemplate.Spec,
	}
	placement, err := c.snap.MakePlacement(candidateNodeNames)
	if err != nil {
		return nil, err
	}
	feasibleNodeNames, _, err := c.snap.CanSchedulePod(ctx, dummyPod, placement)
	if err != nil {
		return nil, err
	}

	var feasibleLeaves []*leafDomain
	for _, nodeName := range feasibleNodeNames {
		leaf := candidateLeaves[nodeName]
		nodeObj := leaf.node.toNode()
		feasibleLeaves = append(feasibleLeaves, leaf)
		if features.Enabled(features.TASRespectNodeAffinityPreferred) && reqs.preferredSchedulingTerms != nil {
			leaf.affinityScore += reqs.preferredSchedulingTerms.Score(nodeObj)
		}
	}
	stats.SchedulerLibraryNoFit = len(candidateNodeNames) - len(feasibleNodeNames)

	return feasibleLeaves, nil
}
