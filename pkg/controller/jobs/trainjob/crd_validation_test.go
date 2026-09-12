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

// TestValidateTrainJobCRDVersion_V2_2_Compatible tests validation with v2.2.0 compatible CRD
func TestValidateTrainJobCRDVersion_V2_2_Compatible(t *testing.T) {
	crd := createMockTrainJobCRDv22()
	client := fake.NewClientBuilder().WithObjects(crd).Build()
	ctx := t.Context()

	err := ValidateTrainJobCRDVersion(ctx, client)
	if err != nil {
		t.Errorf("ValidateTrainJobCRDVersion() with v2.2.0 CRD should not error, got: %v", err)
	}
}

// TestValidateTrainJobCRDVersion_V2_1_Incompatible tests validation with v2.1 incompatible CRD
func TestValidateTrainJobCRDVersion_V2_1_Incompatible(t *testing.T) {
	crd := createMockTrainJobCRDv21()
	client := fake.NewClientBuilder().WithObjects(crd).Build()
	ctx := t.Context()

	err := ValidateTrainJobCRDVersion(ctx, client)
	if err == nil {
		t.Error("ValidateTrainJobCRDVersion() with v2.1 CRD should error, got nil")
	}

	if err != nil {
		errMsg := err.Error()
		if !containsSubstring(errMsg, "v2.2.0") {
			t.Errorf("Error should mention v2.2.0, got: %v", err)
		}
		if !containsSubstring(errMsg, "runtimePatches") {
			t.Errorf("Error should mention runtimePatches, got: %v", err)
		}
	}
}

// TestValidateTrainJobCRDVersion_NotInstalled tests validation when CRD is not installed
// When CRD is not found, it's OK - it might be installed later
func TestValidateTrainJobCRDVersion_NotInstalled(t *testing.T) {
	client := fake.NewClientBuilder().Build()
	ctx := t.Context()

	err := ValidateTrainJobCRDVersion(ctx, client)

	// Missing CRD is OK - return nil, don't fail
	if err != nil {
		t.Errorf("ValidateTrainJobCRDVersion() with missing CRD should return nil (graceful), got: %v", err)
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
			name:     "v2.2.0 CRD with runtimePatches",
			crd:      createMockTrainJobCRDv22(),
			expected: true,
		},
		{
			name:     "v2.1 CRD without runtimePatches",
			crd:      createMockTrainJobCRDv21(),
			expected: false,
		},
		{
			name:     "empty CRD",
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

func createMockTrainJobCRDv22() *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "apiextensions.k8s.io/v1",
			"kind":       "CustomResourceDefinition",
			"metadata": map[string]any{
				"name": "trainjobs.trainer.kubeflow.org",
			},
			"spec": map[string]any{
				"versions": []any{
					map[string]any{
						"name":    "v1",
						"served":  true,
						"storage": true,
						"schema": map[string]any{
							"openAPIV3Schema": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"spec": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"runtimePatches": map[string]any{
												"type": "array",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func createMockTrainJobCRDv21() *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "apiextensions.k8s.io/v1",
			"kind":       "CustomResourceDefinition",
			"metadata": map[string]any{
				"name": "trainjobs.trainer.kubeflow.org",
			},
			"spec": map[string]any{
				"versions": []any{
					map[string]any{
						"name":    "v1",
						"served":  true,
						"storage": true,
						"schema": map[string]any{
							"openAPIV3Schema": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"spec": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"suspend": map[string]any{
												"type": "boolean",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func containsSubstring(text, substring string) bool {
	for i := 0; i <= len(text)-len(substring); i++ {
		if text[i:i+len(substring)] == substring {
			return true
		}
	}
	return false
}

// TestHasRuntimePatchesField_MixedVersions tests with both served and unserved versions
func TestHasRuntimePatchesField_MixedVersions(t *testing.T) {
	// Create CRD with unserved v1alpha1 (has runtimePatches) and served v1 (no runtimePatches)
	crd := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "apiextensions.k8s.io/v1",
			"kind":       "CustomResourceDefinition",
			"metadata": map[string]any{
				"name": "trainjobs.trainer.kubeflow.org",
			},
			"spec": map[string]any{
				"versions": []any{
					// Unserved version with runtimePatches
					map[string]any{
						"name":    "v1alpha1",
						"served":  false, // NOT SERVED
						"storage": false,
						"schema": map[string]any{
							"openAPIV3Schema": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"spec": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"runtimePatches": map[string]any{
												"type": "array",
											},
										},
									},
								},
							},
						},
					},
					// Served version WITHOUT runtimePatches
					map[string]any{
						"name":    "v1",
						"served":  true, // SERVED
						"storage": true,
						"schema": map[string]any{
							"openAPIV3Schema": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"spec": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"suspend": map[string]any{
												"type": "boolean",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	result := hasRuntimePatchesField(crd)
	if result {
		t.Errorf("hasRuntimePatchesField() should return false for CRD with unserved runtimePatches and served without it, got true")
	}
}
