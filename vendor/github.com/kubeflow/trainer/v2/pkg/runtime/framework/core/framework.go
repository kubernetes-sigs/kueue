/*
Copyright 2024 The Kubeflow Authors.

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

package core

import (
	"context"
	"errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	trainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	"github.com/kubeflow/trainer/v2/pkg/runtime"
	"github.com/kubeflow/trainer/v2/pkg/runtime/framework"
	fwkplugins "github.com/kubeflow/trainer/v2/pkg/runtime/framework/plugins"
)

var errorTooManyTerminalConditionPlugin = errors.New("too many TerminalCondition plugins are registered")

type Framework struct {
	registry                     fwkplugins.Registry
	plugins                      map[string]framework.Plugin
	enforceMLPlugins             []framework.EnforceMLPolicyPlugin
	enforcePodGroupPolicyPlugins []framework.EnforcePodGroupPolicyPlugin
	customValidationPlugins      []framework.CustomValidationPlugin
	watchExtensionPlugins        []framework.WatchExtensionPlugin
	podNetworkPlugins            []framework.PodNetworkPlugin
	componentBuilderPlugins      []framework.ComponentBuilderPlugin
	terminalConditionPlugins     []framework.TerminalConditionPlugin
}

func New(ctx context.Context, c client.Client, r fwkplugins.Registry, indexer client.FieldIndexer) (*Framework, error) {
	f := &Framework{
		registry: r,
	}
	plugins := make(map[string]framework.Plugin, len(r))

	for name, factory := range r {
		plugin, err := factory(ctx, c, indexer)
		if err != nil {
			return nil, err
		}
		plugins[name] = plugin
		if p, ok := plugin.(framework.EnforceMLPolicyPlugin); ok {
			f.enforceMLPlugins = append(f.enforceMLPlugins, p)
		}
		if p, ok := plugin.(framework.EnforcePodGroupPolicyPlugin); ok {
			f.enforcePodGroupPolicyPlugins = append(f.enforcePodGroupPolicyPlugins, p)
		}
		if p, ok := plugin.(framework.CustomValidationPlugin); ok {
			f.customValidationPlugins = append(f.customValidationPlugins, p)
		}
		if p, ok := plugin.(framework.WatchExtensionPlugin); ok {
			f.watchExtensionPlugins = append(f.watchExtensionPlugins, p)
		}
		if p, ok := plugin.(framework.PodNetworkPlugin); ok {
			f.podNetworkPlugins = append(f.podNetworkPlugins, p)
		}
		if p, ok := plugin.(framework.ComponentBuilderPlugin); ok {
			f.componentBuilderPlugins = append(f.componentBuilderPlugins, p)
		}
		if p, ok := plugin.(framework.TerminalConditionPlugin); ok {
			f.terminalConditionPlugins = append(f.terminalConditionPlugins, p)
		}
	}
	f.plugins = plugins
	return f, nil
}

func (f *Framework) RunEnforceMLPolicyPlugins(info *runtime.Info, trainJob *trainer.TrainJob) error {
	for _, plugin := range f.enforceMLPlugins {
		if err := plugin.EnforceMLPolicy(info, trainJob); err != nil {
			return err
		}
	}
	return nil
}

func (f *Framework) RunEnforcePodGroupPolicyPlugins(info *runtime.Info, trainJob *trainer.TrainJob) error {
	for _, plugin := range f.enforcePodGroupPolicyPlugins {
		if err := plugin.EnforcePodGroupPolicy(info, trainJob); err != nil {
			return err
		}
	}
	return nil
}

func (f *Framework) RunCustomValidationPlugins(ctx context.Context, info *runtime.Info, oldObj, newObj *trainer.TrainJob) (admission.Warnings, field.ErrorList) {
	var aggregatedWarnings admission.Warnings
	var aggregatedErrors field.ErrorList
	for _, plugin := range f.customValidationPlugins {
		warnings, errs := plugin.Validate(ctx, info, oldObj, newObj)
		if len(warnings) != 0 {
			aggregatedWarnings = append(aggregatedWarnings, warnings...)
		}
		if errs != nil {
			aggregatedErrors = append(aggregatedErrors, errs...)
		}
	}
	return aggregatedWarnings, aggregatedErrors
}

func (f *Framework) RunPodNetworkPlugins(info *runtime.Info, trainJob *trainer.TrainJob) error {
	for _, plugin := range f.podNetworkPlugins {
		if err := plugin.IdentifyPodNetwork(info, trainJob); err != nil {
			return err
		}
	}
	return nil
}

func (f *Framework) RunComponentBuilderPlugins(ctx context.Context, info *runtime.Info, trainJob *trainer.TrainJob) ([]any, error) {
	var objs []any
	for _, plugin := range f.componentBuilderPlugins {
		components, err := plugin.Build(ctx, info, trainJob)
		if err != nil {
			return nil, err
		}
		objs = append(objs, components...)
	}
	return objs, nil
}

func (f *Framework) RunTerminalConditionPlugins(ctx context.Context, trainJob *trainer.TrainJob) (*metav1.Condition, error) {
	// TODO (tenzen-y): Once we provide the Configuration API, we should validate which plugin should have terminalCondition execution points.
	if len(f.terminalConditionPlugins) > 1 {
		return nil, errorTooManyTerminalConditionPlugin
	}
	if len(f.terminalConditionPlugins) != 0 {
		return f.terminalConditionPlugins[0].TerminalCondition(ctx, trainJob)
	}
	return nil, nil
}

func (f *Framework) WatchExtensionPlugins() []framework.WatchExtensionPlugin {
	return f.watchExtensionPlugins
}
