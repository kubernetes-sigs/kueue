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
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
)

func TestNewAdapters(t *testing.T) {
	tests := []struct {
		name    string
		configs []configapi.MultiKueueExternalFramework
		wantErr error
	}{
		{
			name: "valid configurations",
			configs: []configapi.MultiKueueExternalFramework{
				{Name: "PipelineRun.v1.tekton.dev"},
				{Name: "CustomJob.v1alpha1.custom.example.com"},
			},
			wantErr: nil,
		},
		{
			name: "invalid GVK format",
			configs: []configapi.MultiKueueExternalFramework{
				{Name: "invalid-format"},
			},
			wantErr: errors.New("invalid external framework configuration for \"invalid-format\": invalid GVK format 'invalid-format'"),
		},
		{
			name: "empty name",
			configs: []configapi.MultiKueueExternalFramework{
				{Name: ""},
			},
			wantErr: errors.New("invalid external framework configuration for \"\": name is required"),
		},
		{
			name: "duplicate GVK",
			configs: []configapi.MultiKueueExternalFramework{
				{Name: "PipelineRun.v1.tekton.dev"},
				{Name: "PipelineRun.v1.tekton.dev"},
			},
			wantErr: errors.New("duplicate configuration for GVK tekton.dev/v1, Kind=PipelineRun"),
		},
		{
			name: "mixed valid and invalid",
			configs: []configapi.MultiKueueExternalFramework{
				{Name: "PipelineRun.v1.tekton.dev"},
				{Name: "invalid-format"},
				{Name: "Workflow.v1alpha1.argoproj.io"},
			},
			wantErr: errors.New("invalid external framework configuration for \"invalid-format\": invalid GVK format 'invalid-format'"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewAdapters(tt.configs)
			switch {
			case tt.wantErr == nil && err != nil:
				t.Fatalf("NewAdapters() unexpected error = %v", err)
			case tt.wantErr != nil && err == nil:
				t.Fatalf("NewAdapters() expected error %v, got nil", tt.wantErr)
			case tt.wantErr != nil && err.Error() != tt.wantErr.Error():
				t.Errorf("NewAdapters() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNewAdaptersResult(t *testing.T) {
	// Load multiple valid configurations
	configs := []configapi.MultiKueueExternalFramework{
		{Name: "PipelineRun.v1.tekton.dev"},
		{Name: "Workflow.v1alpha1.argoproj.io"},
		{Name: "CustomJob.v1alpha1.custom.example.com"},
	}

	adapters, err := NewAdapters(configs)
	if err != nil {
		t.Fatalf("Failed to get adapters: %v", err)
	}

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
