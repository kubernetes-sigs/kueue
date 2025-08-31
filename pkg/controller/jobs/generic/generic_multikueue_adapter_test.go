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
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
)

func TestGenericAdapter_IsJobManagedByKueue(t *testing.T) {
	tests := []struct {
		name           string
		config         configapi.ExternalFramework
		object         *unstructured.Unstructured
		featureEnabled bool
		want           bool
		wantReason     string
		wantErr        bool
	}{
		{
			name: "feature gate disabled",
			config: configapi.ExternalFramework{
				Group:   "test.example.com",
				Version: "v1",
				Kind:    "TestJob",
			},
			object: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"managedBy": kueue.MultiKueueControllerName,
					},
				},
			},
			featureEnabled: false,
			want:           false,
			wantReason:     "MultiKueueAdaptersForCustomJobs feature gate is disabled",
			wantErr:        false,
		},
		{
			name: "managed by kueue with default path",
			config: configapi.ExternalFramework{
				Group:   "test.example.com",
				Version: "v1",
				Kind:    "TestJob",
			},
			object: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"managedBy": kueue.MultiKueueControllerName,
					},
				},
			},
			featureEnabled: true,
			want:           true,
			wantReason:     "",
			wantErr:        false,
		},
		{
			name: "managed by kueue with custom path",
			config: configapi.ExternalFramework{
				Group:     "test.example.com",
				Version:   "v1",
				Kind:      "TestJob",
				ManagedBy: ".metadata.labels.managedBy",
			},
			object: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": map[string]interface{}{
							"managedBy": kueue.MultiKueueControllerName,
						},
					},
				},
			},
			featureEnabled: true,
			want:           true,
			wantReason:     "",
			wantErr:        false,
		},
		{
			name: "not managed by kueue",
			config: configapi.ExternalFramework{
				Group:   "test.example.com",
				Version: "v1",
				Kind:    "TestJob",
			},
			object: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"managedBy": "other-controller",
					},
				},
			},
			featureEnabled: true,
			want:           false,
			wantReason:     "Expecting .spec.managedBy to be \"kueue.x-k8s.io/multikueue\" not \"other-controller\"",
			wantErr:        false,
		},
		{
			name: "managedBy field not found",
			config: configapi.ExternalFramework{
				Group:   "test.example.com",
				Version: "v1",
				Kind:    "TestJob",
			},
			object: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"otherField": "value",
					},
				},
			},
			featureEnabled: true,
			want:           false,
			wantReason:     "Expecting .spec.managedBy to be \"kueue.x-k8s.io/multikueue\" not \"\"",
			wantErr:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set feature gate
			originalValue := features.Enabled(features.MultiKueueAdaptersForCustomJobs)
			features.SetEnable(features.MultiKueueAdaptersForCustomJobs, tt.featureEnabled)
			defer features.SetEnable(features.MultiKueueAdaptersForCustomJobs, originalValue)

			adapter := NewGenericAdapter(tt.config)
			client := fake.NewClientBuilder().Build()

			// Create the object in the fake client
			tt.object.SetName("test-job")
			tt.object.SetNamespace("default")
			tt.object.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   tt.config.Group,
				Version: tt.config.Version,
				Kind:    tt.config.Kind,
			})
			// Set the APIVersion for the unstructured object
			tt.object.SetAPIVersion(tt.config.Group + "/" + tt.config.Version)
			if err := client.Create(context.Background(), tt.object); err != nil {
				t.Fatalf("Failed to create test object: %v", err)
			}

			got, gotReason, err := adapter.IsJobManagedByKueue(context.Background(), client, types.NamespacedName{
				Name:      "test-job",
				Namespace: "default",
			})

			if (err != nil) != tt.wantErr {
				t.Errorf("IsJobManagedByKueue() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("IsJobManagedByKueue() got = %v, want %v", got, tt.want)
			}
			if gotReason != tt.wantReason {
				t.Errorf("IsJobManagedByKueue() gotReason = %v, want %v", gotReason, tt.wantReason)
			}
		})
	}
}

func TestGenericAdapter_GVK(t *testing.T) {
	config := configapi.ExternalFramework{
		Group:   "test.example.com",
		Version: "v1",
		Kind:    "TestJob",
	}

	adapter := NewGenericAdapter(config)
	gvk := adapter.GVK()

	expectedGVK := schema.GroupVersionKind{
		Group:   "test.example.com",
		Version: "v1",
		Kind:    "TestJob",
	}

	if gvk != expectedGVK {
		t.Errorf("GVK() = %v, want %v", gvk, expectedGVK)
	}
}

func TestGenericAdapter_KeepAdmissionCheckPending(t *testing.T) {
	config := configapi.ExternalFramework{
		Group:   "test.example.com",
		Version: "v1",
		Kind:    "TestJob",
	}

	adapter := NewGenericAdapter(config)
	result := adapter.KeepAdmissionCheckPending()

	if result != false {
		t.Errorf("KeepAdmissionCheckPending() = %v, want false", result)
	}
}

func TestConfigManager_LoadConfigurations(t *testing.T) {
	tests := []struct {
		name    string
		configs []configapi.ExternalFramework
		wantErr bool
	}{
		{
			name: "valid configuration",
			configs: []configapi.ExternalFramework{
				{
					Group:   "test.example.com",
					Version: "v1",
					Kind:    "TestJob",
				},
			},
			wantErr: false,
		},
		{
			name: "invalid configuration - missing group",
			configs: []configapi.ExternalFramework{
				{
					Version: "v1",
					Kind:    "TestJob",
				},
			},
			wantErr: false, // Should log error but continue
		},
		{
			name: "invalid configuration - missing version",
			configs: []configapi.ExternalFramework{
				{
					Group: "test.example.com",
					Kind:  "TestJob",
				},
			},
			wantErr: false, // Should log error but continue
		},
		{
			name: "invalid configuration - missing kind",
			configs: []configapi.ExternalFramework{
				{
					Group:   "test.example.com",
					Version: "v1",
				},
			},
			wantErr: false, // Should log error but continue
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cm := NewConfigManager()
			err := cm.LoadConfigurations(tt.configs)

			if (err != nil) != tt.wantErr {
				t.Errorf("LoadConfigurations() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestConfigManager_GetAdapter(t *testing.T) {
	cm := NewConfigManager()
	configs := []configapi.ExternalFramework{
		{
			Group:   "test.example.com",
			Version: "v1",
			Kind:    "TestJob",
		},
	}

	err := cm.LoadConfigurations(configs)
	if err != nil {
		t.Fatalf("Failed to load configurations: %v", err)
	}

	// Test getting existing adapter
	gvk := schema.GroupVersionKind{
		Group:   "test.example.com",
		Version: "v1",
		Kind:    "TestJob",
	}
	adapter := cm.GetAdapter(gvk)
	if adapter == nil {
		t.Error("GetAdapter() returned nil for existing GVK")
	}

	// Test getting non-existing adapter
	nonExistingGVK := schema.GroupVersionKind{
		Group:   "other.example.com",
		Version: "v1",
		Kind:    "OtherJob",
	}
	adapter = cm.GetAdapter(nonExistingGVK)
	if adapter != nil {
		t.Error("GetAdapter() returned non-nil for non-existing GVK")
	}
}

func TestConfigManager_ApplyDefaults(t *testing.T) {
	cm := NewConfigManager()

	// Test minimal configuration
	config := configapi.ExternalFramework{
		Group:   "test.example.com",
		Version: "v1",
		Kind:    "TestJob",
	}

	configWithDefaults := cm.applyDefaults(config)

	// Check that defaults were applied
	if configWithDefaults.ManagedBy != ".spec.managedBy" {
		t.Errorf("Expected default managedBy path, got %s", configWithDefaults.ManagedBy)
	}

	if len(configWithDefaults.CreationPatches) != 1 {
		t.Errorf("Expected 1 default creation patch, got %d", len(configWithDefaults.CreationPatches))
	}

	if len(configWithDefaults.SyncPatches) != 1 {
		t.Errorf("Expected 1 default sync patch, got %d", len(configWithDefaults.SyncPatches))
	}

	// Test configuration with custom values
	customConfig := configapi.ExternalFramework{
		Group:     "test.example.com",
		Version:   "v1",
		Kind:      "TestJob",
		ManagedBy: ".metadata.labels.managedBy",
		CreationPatches: []configapi.JsonPatch{
			{
				Op:    "replace",
				Path:  "/spec/customField",
				Value: "customValue",
			},
		},
		SyncPatches: []configapi.JsonPatch{
			{
				Op:   "replace",
				Path: "/status/customStatus",
				From: "/status/customStatus",
			},
		},
	}

	customConfigWithDefaults := cm.applyDefaults(customConfig)

	// Check that custom values were preserved
	if customConfigWithDefaults.ManagedBy != ".metadata.labels.managedBy" {
		t.Errorf("Expected custom managedBy path, got %s", customConfigWithDefaults.ManagedBy)
	}

	if len(customConfigWithDefaults.CreationPatches) != 1 {
		t.Errorf("Expected 1 custom creation patch, got %d", len(customConfigWithDefaults.CreationPatches))
	}

	if len(customConfigWithDefaults.SyncPatches) != 1 {
		t.Errorf("Expected 1 custom sync patch, got %d", len(customConfigWithDefaults.SyncPatches))
	}
}
