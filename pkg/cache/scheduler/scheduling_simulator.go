package scheduler

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	schedulerconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/defaultbinder"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodeaffinity"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/queuesort"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/tainttoleration"
	"sigs.k8s.io/scheduler-library/pkg/simulator"
	"sigs.k8s.io/scheduler-library/pkg/upstreamsync/snapshot"
)

type SchedulingSimulator struct {
	sim *simulator.SchedulingSimulator
}

func NewSchedulingSimulator(ctx context.Context, restConfig *rest.Config) (*SchedulingSimulator, error) {
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

	return &SchedulingSimulator{sim: sim}, nil
}

func (s *SchedulingSimulator) NewClusterSnapshot(ctx context.Context, nodes []*nodeInfo) (*snapshot.ClusterSnapshot, error) {
	var v1Nodes []*corev1.Node
	for _, n := range nodes {
		v1Nodes = append(v1Nodes, n.toNode())
	}
	return s.sim.NewClusterSnapshot(ctx, nil, v1Nodes)
}
