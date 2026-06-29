package scheduler

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler"
	schedulerconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/backend/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/defaultbinder"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/feature"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodeaffinity"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/queuesort"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/tainttoleration"
	fwkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	"sigs.k8s.io/scheduler-library/pkg/snapshot"
	upstreamsync "sigs.k8s.io/scheduler-library/pkg/upstream_sync"
)

func NewClusterSnapshot(nodes []*nodeInfo) (*snapshot.ClusterSnapshot, error) {
	var v1Nodes []*corev1.Node
	for _, n := range nodes {
		v1Nodes = append(v1Nodes, n.toNode())
	}
	upstreamCache := cache.NewSnapshot(nil, v1Nodes)

	registry := fwkruntime.Registry{
		tainttoleration.Name: func(ctx context.Context, obj runtime.Object, handle framework.Handle) (framework.Plugin, error) {
			return tainttoleration.New(ctx, obj, handle, feature.Features{})
		},
		nodeaffinity.Name: func(ctx context.Context, obj runtime.Object, handle framework.Handle) (framework.Plugin, error) {
			var args runtime.Object
			if obj != nil {
				args = obj
			} else {
				args = &schedulerconfig.NodeAffinityArgs{}
			}
			return nodeaffinity.New(ctx, args, handle, feature.Features{})
		},
		queuesort.Name: func(ctx context.Context, obj runtime.Object, handle framework.Handle) (framework.Plugin, error) {
			return queuesort.New(ctx, obj, handle)
		},
		defaultbinder.Name: func(ctx context.Context, obj runtime.Object, handle framework.Handle) (framework.Plugin, error) {
			return defaultbinder.New(ctx, obj, handle)
		},
	}

	cfg := &schedulerconfig.KubeSchedulerProfile{
		SchedulerName: "default-scheduler",
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
	}

	upstreamSched, err := scheduler.New(
		context.Background(),
		nil,
		nil,
		nil,
		nil,
		scheduler.WithProfiles(*cfg),
		scheduler.WithFrameworkOutOfTreeRegistry(registry),
		scheduler.WithNodeInfoSnapshot(upstreamCache),
	)
	if err != nil {
		return nil, err
	}

	sched := upstreamsync.NewScheduler(upstreamSched, upstreamCache)
	wasSnapshot := snapshot.New(upstreamCache, sched)

	return wasSnapshot, nil
}
