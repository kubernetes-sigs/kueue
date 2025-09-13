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
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
)

func TestConfigManager_LoadConfigurations(t *testing.T) {
	cm := NewConfigManager()

	tests := []struct {
		name    string
		configs []configapi.MultiKueueExternalFramework
		wantErr bool
	}{
		{
			name: "valid configurations",
			configs: []configapi.MultiKueueExternalFramework{
				{Name: "PipelineRun.v1.tekton.dev"},
				{Name: "CustomJob.v1alpha1.custom.example.com"},
			},
			wantErr: false,
		},
		{
			name: "invalid GVK format",
			configs: []configapi.MultiKueueExternalFramework{
				{Name: "invalid-format"},
			},
			wantErr: true,
		},
		{
			name: "empty name",
			configs: []configapi.MultiKueueExternalFramework{
				{Name: ""},
			},
			wantErr: true,
		},
		{
			name: "duplicate GVK",
			configs: []configapi.MultiKueueExternalFramework{
				{Name: "PipelineRun.v1.tekton.dev"},
				{Name: "PipelineRun.v1.tekton.dev"},
			},
			wantErr: true,
		},
		{
			name: "mixed valid and invalid",
			configs: []configapi.MultiKueueExternalFramework{
				{Name: "PipelineRun.v1.tekton.dev"},
				{Name: "invalid-format"},
				{Name: "Workflow.v1alpha1.argoproj.io"},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := cm.LoadConfigurations(tt.configs)
			if (err != nil) != tt.wantErr {
				t.Errorf("ConfigManager.LoadConfigurations() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestConfigManager_GetAdapter(t *testing.T) {
	cm := NewConfigManager()

	// Load a valid configuration
	configs := []configapi.MultiKueueExternalFramework{
		{Name: "PipelineRun.v1.tekton.dev"},
	}
	if err := cm.LoadConfigurations(configs); err != nil {
		t.Fatalf("Failed to load configurations: %v", err)
	}

	// Test getting adapter for configured GVK
	gvk := schema.GroupVersionKind{
		Group:   "tekton.dev",
		Version: "v1",
		Kind:    "PipelineRun",
	}

	adapter := cm.GetAdapter(gvk)
	if adapter == nil {
		t.Error("Expected adapter to be returned for configured GVK")
	}
	if adapter.GVK() != gvk {
		t.Errorf("Expected GVK %v, got %v", gvk, adapter.GVK())
	}

	// Test getting adapter for unconfigured GVK
	unconfiguredGVK := schema.GroupVersionKind{
		Group:   "example.com",
		Version: "v1",
		Kind:    "Example",
	}

	unconfiguredAdapter := cm.GetAdapter(unconfiguredGVK)
	if unconfiguredAdapter != nil {
		t.Error("Expected nil adapter for unconfigured GVK")
	}
}

func TestConfigManager_GetAllAdapters(t *testing.T) {
	cm := NewConfigManager()

	// Load multiple valid configurations
	configs := []configapi.MultiKueueExternalFramework{
		{Name: "PipelineRun.v1.tekton.dev"},
		{Name: "Workflow.v1alpha1.argoproj.io"},
		{Name: "CustomJob.v1alpha1.custom.example.com"},
	}

	if err := cm.LoadConfigurations(configs); err != nil {
		t.Fatalf("Failed to load configurations: %v", err)
	}

	adapters := cm.GetAllAdapters()
	if len(adapters) != 3 {
		t.Errorf("Expected 3 adapters, got %d", len(adapters))
	}

	// Verify all adapters have valid GVKs
	expectedGVKs := map[schema.GroupVersionKind]bool{
		{Group: "tekton.dev", Version: "v1", Kind: "PipelineRun"}:             true,
		{Group: "argoproj.io", Version: "v1alpha1", Kind: "Workflow"}:         true,
		{Group: "custom.example.com", Version: "v1alpha1", Kind: "CustomJob"}: true,
	}

	for _, adapter := range adapters {
		gvk := adapter.GVK()
		if !expectedGVKs[gvk] {
			t.Errorf("Unexpected GVK in adapter: %v", gvk)
		}
	}
}
