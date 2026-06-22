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

package scheduler

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/kube-scheduler/framework"
	schedulerconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/backend/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/defaultbinder"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/feature"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodeaffinity"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/queuesort"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/tainttoleration"
	fwkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	schedulermetrics "k8s.io/kubernetes/pkg/scheduler/metrics"
	"k8s.io/kubernetes/pkg/scheduler/profile"
	"sigs.k8s.io/scheduler-library/pkg/snapshot"
)

func NewClusterSnapshotFromNodes(nodes []*corev1.Node) (*snapshot.ClusterSnapshot, error) {
	return newClusterSnapshot(nodes)
}

func NewClusterSnapshot(nodes []*nodeInfo) (*snapshot.ClusterSnapshot, error) {
	var v1Nodes []*corev1.Node
	for _, n := range nodes {
		v1Nodes = append(v1Nodes, n.toNode())
	}
	return newClusterSnapshot(v1Nodes)
}

func newClusterSnapshot(v1Nodes []*corev1.Node) (*snapshot.ClusterSnapshot, error) {
	schedulermetrics.Register()
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
		queuesort.Name:     queuesort.New,
		defaultbinder.Name: defaultbinder.New,
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

	fwk, err := fwkruntime.NewFramework(context.Background(), registry, cfg, fwkruntime.WithSnapshotSharedLister(upstreamCache))
	if err != nil {
		return nil, err
	}

	pm := profile.Map{
		"default-scheduler": fwk,
	}
	wasSnapshot := snapshot.NewClusterSnapshot(upstreamCache, pm)

	return wasSnapshot, nil
}
