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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	schedulerconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/defaultbinder"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodeaffinity"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodeunschedulable"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/queuesort"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/tainttoleration"
	schedLibSimulator "sigs.k8s.io/scheduler-library/pkg/simulator"

	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
)

var _ simulator.Factory = (*wasSimulatorFactory)(nil)

type wasSimulatorFactory struct {
	sim *schedLibSimulator.SchedulingSimulator
}

func newWASSchedulerConfig() *schedulerconfig.KubeSchedulerConfiguration {
	return &schedulerconfig.KubeSchedulerConfiguration{
		Profiles: []schedulerconfig.KubeSchedulerProfile{
			{
				SchedulerName: corev1.DefaultSchedulerName,
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
							{Name: nodeunschedulable.Name},
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
}

func NewWASSimulatorFactory(ctx context.Context, restConfig *rest.Config) (simulator.Factory, error) {
	cfg := newWASSchedulerConfig()

	roClient, err := schedLibSimulator.NewReadonlyClient(restConfig)
	if err != nil {
		return nil, err
	}

	// Use a fake client to not maintain any informer stores for the integration,
	// as the plugins that are currently in use do not require any state.
	// In the future, when using plugins like DRA, which rely on the informers,
	// the InformerFactory has to be properly populated (for example by passing `nil`
	// to `NewSchedulingSimulator`, which instantiates the default informers).
	fakeClient := fake.NewSimpleClientset()
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 0)

	sim, err := schedLibSimulator.NewSchedulingSimulator(ctx, cfg, roClient, informerFactory)
	if err != nil {
		return nil, err
	}

	return &wasSimulatorFactory{sim: sim}, nil
}

// NewWASSimulatorFactoryForTest creates a WAS simulator factory backed by a fake client,
// suitable for unit tests that need the full filter plugin pipeline.
// It wraps ctx with a discard logger to prevent background informer goroutines
// from racing with test teardown when t.Context() carries a test logger.
func NewWASSimulatorFactoryForTest(ctx context.Context) (simulator.Factory, error) {
	return NewWASSimulatorFactory(klog.NewContext(ctx, logr.Discard()), &rest.Config{})
}

func (s *wasSimulatorFactory) NewSimulator(ctx context.Context, nodes []*corev1.Node) (simulator.SchedulerSimulator, error) {
	clusterSnap, err := s.sim.NewClusterSnapshot(ctx, nil, nodes)
	if err != nil {
		return nil, err
	}
	return &wasSimulator{snap: clusterSnap}, nil
}
