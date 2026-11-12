// Copyright The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package framework

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	configv1 "k8s.io/kube-scheduler/config/v1"
	fwk "k8s.io/kube-scheduler/framework"
	schedulerapi "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/apis/config/scheme"
	frameworkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	"sigs.k8s.io/scheduler-library/pkg/preemption"
)

// UpdateSimulatorConfig returns a copy of cfg (or a defaulted configuration when cfg is nil)
// with internal simulator plugins injected into each profile.
func UpdateSimulatorConfig(cfg *schedulerapi.KubeSchedulerConfiguration) (*schedulerapi.KubeSchedulerConfiguration, error) {
	if cfg == nil {
		var versionedCfg configv1.KubeSchedulerConfiguration
		scheme.Scheme.Default(&versionedCfg)
		cfg = &schedulerapi.KubeSchedulerConfiguration{}
		// ensure defaults are set in the config
		if err := scheme.Scheme.Convert(&versionedCfg, cfg, nil); err != nil {
			return nil, err
		}
	} else {
		cfg = cfg.DeepCopy()
	}
	for i := range cfg.Profiles {
		if cfg.Profiles[i].Plugins == nil {
			cfg.Profiles[i].Plugins = &schedulerapi.Plugins{}
		}
		// Prepend the plugin to the front, since it's typically cheaper than other filter plugins.
		// We're not using MultiPoint to make sure the plugin is not disabled by a wildcard.
		cfg.Profiles[i].Plugins.Filter.Enabled = append([]schedulerapi.Plugin{
			{
				Name: preemption.CommittedVictimPluginName,
			},
		}, cfg.Profiles[i].Plugins.Filter.Enabled...)
	}
	return cfg, nil
}

// SimulatorRegistry builds the out-of-tree plugin registry containing internal simulator plugins.
func SimulatorRegistry() (frameworkruntime.Registry, error) {
	r := frameworkruntime.Registry{}
	err := r.Register(preemption.CommittedVictimPluginName, func(_ context.Context, _ runtime.Object, _ fwk.Handle) (fwk.Plugin, error) {
		return &preemption.CommittedVictimPlugin{}, nil
	})
	if err != nil {
		return nil, err
	}
	return r, nil
}
