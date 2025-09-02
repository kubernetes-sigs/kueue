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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
)

func TestGenericAdapter_IsJobManagedByKueue(t *testing.T) {
	tests := []struct {
		name           string
		object         *unstructured.Unstructured
		featureEnabled bool
		want           bool
		wantReason     string
		wantErr        bool
	}{
		{
			name: "feature gate disabled",
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
			name: "not managed by kueue",
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
		{
			name: "managedBy value is not a string",
			object: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"managedBy": "not-a-string",
					},
				},
			},
			featureEnabled: true,
			want:           false,
			wantReason:     "Expecting .spec.managedBy to be \"kueue.x-k8s.io/multikueue\" not \"not-a-string\"",
			wantErr:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save original feature gate state
			originalValue := features.Enabled(features.MultiKueueAdaptersForCustomJobs)
			features.SetEnable(features.MultiKueueAdaptersForCustomJobs, tt.featureEnabled)
			defer features.SetEnable(features.MultiKueueAdaptersForCustomJobs, originalValue)

			adapter := &genericAdapter{
				gvk: schema.GroupVersionKind{
					Group:   "test.example.com",
					Version: "v1",
					Kind:    "TestJob",
				},
			}

			// Set GVK on test object
			tt.object.SetGroupVersionKind(adapter.gvk)
			tt.object.SetName("test-job")
			tt.object.SetNamespace("default")

			client := fake.NewClientBuilder().WithObjects(tt.object).Build()
			key := types.NamespacedName{Name: "test-job", Namespace: "default"}

			got, gotReason, err := adapter.IsJobManagedByKueue(context.Background(), client, key)

			if (err != nil) != tt.wantErr {
				t.Errorf("genericAdapter.IsJobManagedByKueue() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("genericAdapter.IsJobManagedByKueue() got = %v, want %v", got, tt.want)
			}
			if gotReason != tt.wantReason {
				t.Errorf("genericAdapter.IsJobManagedByKueue() gotReason = %v, want %v", gotReason, tt.wantReason)
			}
		})
	}
}

func TestGenericAdapter_RemoveManagedByField(t *testing.T) {
	adapter := &genericAdapter{
		gvk: schema.GroupVersionKind{
			Group:   "test.example.com",
			Version: "v1",
			Kind:    "TestJob",
		},
	}

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"spec": map[string]interface{}{
				"managedBy":  "kueue.x-k8s.io/multikueue",
				"otherField": "value",
			},
		},
	}

	adapter.removeManagedByField(obj)

	// Check that managedBy field is removed
	if _, exists := obj.Object["spec"].(map[string]interface{})["managedBy"]; exists {
		t.Error("managedBy field should be removed")
	}

	// Check that other fields are preserved
	if obj.Object["spec"].(map[string]interface{})["otherField"] != "value" {
		t.Error("otherField should be preserved")
	}
}

func TestGenericAdapter_CopyStatusFromRemote(t *testing.T) {
	adapter := &genericAdapter{
		gvk: schema.GroupVersionKind{
			Group:   "test.example.com",
			Version: "v1",
			Kind:    "TestJob",
		},
	}

	localObj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"spec": map[string]interface{}{
				"field": "value",
			},
		},
	}

	remoteObj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"spec": map[string]interface{}{
				"field": "value",
			},
			"status": map[string]interface{}{
				"phase": "Running",
				"conditions": []interface{}{
					map[string]interface{}{
						"type":   "Ready",
						"status": "True",
					},
				},
			},
		},
	}

	adapter.copyStatusFromRemote(localObj, remoteObj)

	// Check that status is copied
	localStatus, exists := localObj.Object["status"]
	if !exists {
		t.Error("status should be copied to local object")
	}

	// Check that status content matches
	if localStatus.(map[string]interface{})["phase"] != "Running" {
		t.Error("status phase should match")
	}
}

func TestGenericAdapter_SplitJsonPath(t *testing.T) {
	adapter := &genericAdapter{
		gvk: schema.GroupVersionKind{
			Group:   "test.example.com",
			Version: "v1",
			Kind:    "TestJob",
		},
	}

	tests := []struct {
		path     string
		expected []string
	}{
		{
			path:     ".spec.managedBy",
			expected: []string{"spec", "managedBy"},
		},
		{
			path:     "spec.managedBy",
			expected: []string{"spec", "managedBy"},
		},
		{
			path:     "",
			expected: []string{},
		},
		{
			path:     "single",
			expected: []string{"single"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := adapter.splitJsonPath(tt.path)
			if len(result) != len(tt.expected) {
				t.Errorf("Expected %d parts, got %d", len(tt.expected), len(result))
				return
			}
			for i, part := range tt.expected {
				if result[i] != part {
					t.Errorf("Part %d: expected %s, got %s", i, part, result[i])
				}
			}
		})
	}
}
