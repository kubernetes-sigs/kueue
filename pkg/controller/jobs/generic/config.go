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
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
)

// ConfigManager manages external framework configurations for generic adapters
type ConfigManager struct {
	configs map[string]configapi.ExternalFramework
}

// NewConfigManager creates a new configuration manager
func NewConfigManager() *ConfigManager {
	return &ConfigManager{
		configs: make(map[string]configapi.ExternalFramework),
	}
}

// LoadConfigurations loads and validates external framework configurations
func (cm *ConfigManager) LoadConfigurations(configs []configapi.ExternalFramework) error {
	cm.configs = make(map[string]configapi.ExternalFramework)

	for _, config := range configs {
		if err := cm.validateConfig(config); err != nil {
			klog.Errorf("Invalid external framework configuration: %v", err)
			continue // Skip invalid configurations but continue loading others
		}

		// Apply default values
		config = cm.applyDefaults(config)

		// Store the configuration
		gvk := schema.GroupVersionKind{
			Group:   config.Group,
			Version: config.Version,
			Kind:    config.Kind,
		}
		cm.configs[gvk.String()] = config
	}

	return nil
}

// GetAdapter returns a generic adapter for the given GVK if configured
func (cm *ConfigManager) GetAdapter(gvk schema.GroupVersionKind) *genericAdapter {
	config, exists := cm.configs[gvk.String()]
	if !exists {
		return nil
	}

	return &genericAdapter{
		config: config,
		gvk:    gvk,
	}
}

// GetAllAdapters returns all configured generic adapters
func (cm *ConfigManager) GetAllAdapters() []*genericAdapter {
	adapters := make([]*genericAdapter, 0, len(cm.configs))
	for _, config := range cm.configs {
		gvk := schema.GroupVersionKind{
			Group:   config.Group,
			Version: config.Version,
			Kind:    config.Kind,
		}
		adapters = append(adapters, &genericAdapter{
			config: config,
			gvk:    gvk,
		})
	}
	return adapters
}

// validateConfig validates an external framework configuration
func (cm *ConfigManager) validateConfig(config configapi.ExternalFramework) error {
	if config.Group == "" {
		return fmt.Errorf("group is required")
	}
	if config.Version == "" {
		return fmt.Errorf("version is required")
	}
	if config.Kind == "" {
		return fmt.Errorf("kind is required")
	}

	// Validate managedBy path if provided
	if config.ManagedBy != "" {
		if !strings.HasPrefix(config.ManagedBy, ".") {
			return fmt.Errorf("managedBy path must start with '.'")
		}
	}

	// Validate JSON patches
	if err := cm.validateJsonPatches(config.CreationPatches); err != nil {
		return fmt.Errorf("invalid creation patches: %w", err)
	}
	if err := cm.validateJsonPatches(config.SyncPatches); err != nil {
		return fmt.Errorf("invalid sync patches: %w", err)
	}

	return nil
}

// validateJsonPatches validates a list of JSON patches
func (cm *ConfigManager) validateJsonPatches(patches []configapi.JsonPatch) error {
	for i, patch := range patches {
		if err := cm.validateJsonPatch(patch); err != nil {
			return fmt.Errorf("patch[%d]: %w", i, err)
		}
	}
	return nil
}

// validateJsonPatch validates a single JSON patch
func (cm *ConfigManager) validateJsonPatch(patch configapi.JsonPatch) error {
	// Validate operation
	validOps := map[string]bool{
		"add":     true,
		"remove":  true,
		"replace": true,
		"move":    true,
		"copy":    true,
		"test":    true,
	}
	if !validOps[patch.Op] {
		return fmt.Errorf("invalid operation: %s", patch.Op)
	}

	// Validate path
	if patch.Path == "" {
		return fmt.Errorf("path is required")
	}
	if !strings.HasPrefix(patch.Path, "/") {
		return fmt.Errorf("path must start with '/'")
	}

	// Validate value for add/replace operations
	if (patch.Op == "add" || patch.Op == "replace") && patch.Value == nil {
		return fmt.Errorf("value is required for %s operation", patch.Op)
	}

	// Validate from field for move/copy operations
	if (patch.Op == "move" || patch.Op == "copy") && patch.From == "" {
		return fmt.Errorf("from field is required for %s operation", patch.Op)
	}

	return nil
}

// applyDefaults applies default values to a configuration
func (cm *ConfigManager) applyDefaults(config configapi.ExternalFramework) configapi.ExternalFramework {
	// Set default managedBy path if not provided
	if config.ManagedBy == "" {
		config.ManagedBy = ".spec.managedBy"
	}

	// Set default creation patches if not provided
	if len(config.CreationPatches) == 0 {
		// Default to removing the managedBy field.
		// The path is transformed from ".spec.managedBy" to "/spec/managedBy".
		path := "/" + strings.ReplaceAll(strings.TrimPrefix(config.ManagedBy, "."), ".", "/")
		config.CreationPatches = []configapi.JsonPatch{
			{
				Op:   "remove",
				Path: path,
			},
		}
	}

	// Set default sync patches if not provided
	if len(config.SyncPatches) == 0 {
		config.SyncPatches = []configapi.JsonPatch{
			{
				Op:   "replace",
				Path: "/status",
				From: "/status",
			},
		}
	}

	return config
}
