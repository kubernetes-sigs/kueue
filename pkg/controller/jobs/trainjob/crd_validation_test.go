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

package trainjob

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestValidateTrainJobCRDVersion_V2_2_Compatible tests validation with a v2.2+ compatible CRD
func TestValidateTrainJobCRDVersion_V2_2_Compatible(t *testing.T) {
	// Create a mock v2.2+ TrainJob CRD with runtimePatches field
	crd := createMockTrainJobCRDv22()

	client := fake.NewClientBuilder().
		WithObjects(crd).
		Build()

	ctx := t.Context()
	err := ValidateTrainJobCRDVersion(ctx, client)

	if err != nil {
		t.Errorf("ValidateTrainJobCRDVersion() with v2.2+ CRD should not error, but got: %v", err)
	}
}

// TestValidateTrainJobCRDVersion_V2_1_Incompatible tests validation with a v2.1 incompatible CRD
func TestValidateTrainJobCRDVersion_V2_1_Incompatible(t *testing.T) {
	// Create a mock v2.1 TrainJob CRD without runtimePatches field
	crd := createMockTrainJobCRDv21()

	client := fake.NewClientBuilder().
		WithObjects(crd).
		Build()

	ctx := t.Context()
	err := ValidateTrainJobCRDVersion(ctx, client)

	if err == nil {
		t.Error("ValidateTrainJobCRDVersion() with v2.1 CRD should error, but got nil")
	}

	// Check error message contains expected guidance
	if err != nil && err.Error() != "" {
		if contains := "v2.2.0"; !containsSubstring(err.Error(), contains) {
			t.Errorf("Expected error to mention v2.2.0, but got: %v", err)
		}
		if contains := "runtimePatches"; !containsSubstring(err.Error(), contains) {
			t.Errorf("Expected error to mention runtimePatches, but got: %v", err)
		}
	}
}

// TestValidateTrainJobCRDVersion_NotInstalled tests validation when CRD is not installed
func TestValidateTrainJobCRDVersion_NotInstalled(t *testing.T) {
	// Create an empty client with no CRDs
	client := fake.NewClientBuilder().Build()

	ctx := t.Context()
	err := ValidateTrainJobCRDVersion(ctx, client)

	if err == nil {
		t.Error("ValidateTrainJobCRDVersion() with missing CRD should error, but got nil")
	}

	// Check error message contains expected guidance
	if err != nil && err.Error() != "" {
		if contains := "Kubeflow Trainer"; !containsSubstring(err.Error(), contains) {
			t.Errorf("Expected error to mention Kubeflow Trainer, but got: %v", err)
		}
	}
}

// TestHasRuntimePatchesField tests the field detection logic
func TestHasRuntimePatchesField(t *testing.T) {
	tests := []struct {
		name     string
		crd      *unstructured.Unstructured
		expected bool
	}{
		{
			name:     "v2.2+ CRD with runtimePatches",
			crd:      createMockTrainJobCRDv22(),
			expected: true,
		},
		{
			name:     "v2.1 CRD without runtimePatches",
			crd:      createMockTrainJobCRDv21(),
			expected: false,
		},
		{
			name:     "malformed CRD",
			crd:      &unstructured.Unstructured{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hasRuntimePatchesField(tt.crd)
			if result != tt.expected {
				t.Errorf("hasRuntimePatchesField() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// Helper functions

// createMockTrainJobCRDv22 creates a mock TrainJob CRD with v2.2+ schema including runtimePatches
func createMockTrainJobCRDv22() *unstructured.Unstructured {
	crd := &unstructured.Unstructured{}
	crd.SetAPIVersion("apiextensions.k8s.io/v1")
	crd.SetKind("CustomResourceDefinition")
	crd.SetName("trainjobs.trainer.kubeflow.org")

	// Set the schema with runtimePatches field (v2.2+)
	_ = unstructured.SetNestedField(crd.Object, []map[string]any{
		{
			"name":    "v1alpha1",
			"served":  true,
			"storage": true,
			"schema": map[string]any{
				"openAPIV3Schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"spec": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"runtimeRef": map[string]any{
									"type": "object",
								},
								"trainer": map[string]any{
									"type": "object",
								},
								// v2.2+ field
								"runtimePatches": map[string]any{
									"type": "array",
									"items": map[string]any{
										"type": "object",
									},
								},
								"suspend": map[string]any{
									"type": "boolean",
								},
							},
						},
					},
				},
			},
		},
	}, "spec", "versions")

	return crd
}

// createMockTrainJobCRDv21 creates a mock TrainJob CRD with v2.1 schema without runtimePatches
func createMockTrainJobCRDv21() *unstructured.Unstructured {
	crd := &unstructured.Unstructured{}
	crd.SetAPIVersion("apiextensions.k8s.io/v1")
	crd.SetKind("CustomResourceDefinition")
	crd.SetName("trainjobs.trainer.kubeflow.org")

	// Set the schema WITHOUT runtimePatches field (v2.1)
	_ = unstructured.SetNestedField(crd.Object, []map[string]any{
		{
			"name":    "v1alpha1",
			"served":  true,
			"storage": true,
			"schema": map[string]any{
				"openAPIV3Schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"spec": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"runtimeRef": map[string]any{
									"type": "object",
								},
								"trainer": map[string]any{
									"type": "object",
								},
								// v2.1 does NOT have runtimePatches
								"suspend": map[string]any{
									"type": "boolean",
								},
							},
						},
					},
				},
			},
		},
	}, "spec", "versions")

	return crd
}

// containsSubstring checks if a string contains a substring (case-sensitive)
func containsSubstring(text, substring string) bool {
	for i := 0; i <= len(text)-len(substring); i++ {
		if text[i:i+len(substring)] == substring {
			return true
		}
	}
	return false
}
