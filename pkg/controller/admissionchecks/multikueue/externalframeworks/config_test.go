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

package externalframeworks

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
)

func TestInitialize(t *testing.T) {
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
			err := Initialize(tt.configs)
			if (err != nil) != tt.wantErr {
				t.Errorf("Initialize() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestGetAllAdapters(t *testing.T) {
	// Load multiple valid configurations
	configs := []configapi.MultiKueueExternalFramework{
		{Name: "PipelineRun.v1.tekton.dev"},
		{Name: "Workflow.v1alpha1.argoproj.io"},
		{Name: "CustomJob.v1alpha1.custom.example.com"},
	}

	if err := Initialize(configs); err != nil {
		t.Fatalf("Failed to load configurations: %v", err)
	}

	adapters := GetAllAdapters()
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
