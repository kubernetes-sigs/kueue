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

package generic

import (
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
	k8serrors "k8s.io/apimachinery/pkg/util/errors"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
)

// ConfigManager manages external framework configurations for generic adapters
type ConfigManager struct {
	configs map[schema.GroupVersionKind]configapi.MultiKueueExternalFramework
}

// NewConfigManager creates a new configuration manager
func NewConfigManager() *ConfigManager {
	return &ConfigManager{
		configs: make(map[schema.GroupVersionKind]configapi.MultiKueueExternalFramework),
	}
}

// LoadConfigurations loads and validates external framework configurations
func (cm *ConfigManager) LoadConfigurations(configs []configapi.MultiKueueExternalFramework) error {
	cm.configs = make(map[schema.GroupVersionKind]configapi.MultiKueueExternalFramework)
	var errs []error

	for _, config := range configs {
		gvk, err := parseGVK(config.Name)
		if err != nil {
			errs = append(errs, fmt.Errorf("invalid external framework configuration for %q: %w", config.Name, err))
			continue
		}

		if _, exists := cm.configs[*gvk]; exists {
			errs = append(errs, fmt.Errorf("duplicate configuration for GVK %s", gvk))
			continue
		}

		cm.configs[*gvk] = config
	}

	return k8serrors.NewAggregate(errs)
}

// GetAdapter returns a generic adapter for the given GVK if configured
func (cm *ConfigManager) GetAdapter(gvk schema.GroupVersionKind) *genericAdapter {
	if _, exists := cm.configs[gvk]; !exists {
		return nil
	}

	return &genericAdapter{
		gvk: gvk,
	}
}

// GetAllAdapters returns all configured generic adapters
func (cm *ConfigManager) GetAllAdapters() []*genericAdapter {
	adapters := make([]*genericAdapter, 0, len(cm.configs))
	for gvk := range cm.configs {
		adapters = append(adapters, &genericAdapter{
			gvk: gvk,
		})
	}
	return adapters
}

// parseGVK parses a string to a GVK
func parseGVK(name string) (*schema.GroupVersionKind, error) {
	if name == "" {
		return nil, errors.New("name is required")
	}

	gvk, _ := schema.ParseKindArg(name)
	if gvk == nil {
		return nil, fmt.Errorf("invalid GVK format '%s'", name)
	}
	return gvk, nil
}
