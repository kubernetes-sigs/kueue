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
)

type profileBuilder struct {
	name    string
	plugins *schedulerconfig.Plugins
	config  []schedulerconfig.PluginConfig
}

func newProfileBuilder(name string) *profileBuilder {
	return &profileBuilder{
		name:    name,
		plugins: &schedulerconfig.Plugins{},
	}
}

func (b *profileBuilder) withQueueSort() *profileBuilder {
	b.plugins.QueueSort.Enabled = append(b.plugins.QueueSort.Enabled, schedulerconfig.Plugin{Name: queuesort.Name})
	return b
}

func (b *profileBuilder) withDefaultBinder() *profileBuilder {
	b.plugins.Bind.Enabled = append(b.plugins.Bind.Enabled, schedulerconfig.Plugin{Name: defaultbinder.Name})
	return b
}

func (b *profileBuilder) withNodeUnschedulable() *profileBuilder {
	b.plugins.Filter.Enabled = append(b.plugins.Filter.Enabled, schedulerconfig.Plugin{Name: nodeunschedulable.Name})
	return b
}

func (b *profileBuilder) withTaintToleration() *profileBuilder {
	b.plugins.Filter.Enabled = append(b.plugins.Filter.Enabled, schedulerconfig.Plugin{Name: tainttoleration.Name})
	return b
}

func (b *profileBuilder) withNodeAffinity() *profileBuilder {
	b.plugins.PreFilter.Enabled = append(b.plugins.PreFilter.Enabled, schedulerconfig.Plugin{Name: nodeaffinity.Name})
	b.plugins.Filter.Enabled = append(b.plugins.Filter.Enabled, schedulerconfig.Plugin{Name: nodeaffinity.Name})
	b.config = append(b.config, schedulerconfig.PluginConfig{
		Name: nodeaffinity.Name,
		Args: &schedulerconfig.NodeAffinityArgs{},
	})
	return b
}

func (b *profileBuilder) withNodePorts() *profileBuilder {
	b.plugins.PreFilter.Enabled = append(b.plugins.PreFilter.Enabled, schedulerconfig.Plugin{Name: nodeports.Name})
	b.plugins.Filter.Enabled = append(b.plugins.Filter.Enabled, schedulerconfig.Plugin{Name: nodeports.Name})
	return b
}

func (b *profileBuilder) withGangScheduling() *profileBuilder {
	b.plugins.MultiPoint.Enabled = append(b.plugins.MultiPoint.Enabled, schedulerconfig.Plugin{Name: gangscheduling.Name})
	return b
}

func (b *profileBuilder) withNodeResourcesFit(scoringStrategyType schedulerconfig.ScoringStrategyType) *profileBuilder {
	b.plugins.PreFilter.Enabled = append(b.plugins.PreFilter.Enabled, schedulerconfig.Plugin{Name: noderesources.Name})
	b.plugins.Filter.Enabled = append(b.plugins.Filter.Enabled, schedulerconfig.Plugin{Name: noderesources.Name})
	b.config = append(b.config, schedulerconfig.PluginConfig{
		Name: noderesources.Name,
		Args: &schedulerconfig.NodeResourcesFitArgs{
			ScoringStrategy: &schedulerconfig.ScoringStrategy{
				Type: scoringStrategyType,
			},
		},
	})
	return b
}

func (b *profileBuilder) withTopologyAware() *profileBuilder {
	b.plugins.PlacementGenerate.Enabled = append(b.plugins.PlacementGenerate.Enabled, schedulerconfig.Plugin{Name: topologyaware.Name})
	return b
}

func (b *profileBuilder) withDefaultPreemption(minCandNodesPercent, minCandNodesAbs int32) *profileBuilder {
	b.plugins.PodGroupPostFilter.Enabled = append(b.plugins.PodGroupPostFilter.Enabled, schedulerconfig.Plugin{Name: defaultpreemption.Name})
	b.config = append(b.config, schedulerconfig.PluginConfig{
		Name: defaultpreemption.Name,
		Args: &schedulerconfig.DefaultPreemptionArgs{
			MinCandidateNodesPercentage: minCandNodesPercent,
			MinCandidateNodesAbsolute:   minCandNodesAbs,
		},
	})
	return b
}

func (b *profileBuilder) build() schedulerconfig.KubeSchedulerProfile {
	return schedulerconfig.KubeSchedulerProfile{
		SchedulerName: b.name,
		Plugins:       b.plugins,
		PluginConfig:  b.config,
	}
}
