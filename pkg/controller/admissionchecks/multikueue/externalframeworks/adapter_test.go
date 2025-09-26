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
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
)

func TestAdapter_IsJobManagedByKueue(t *testing.T) {
	tests := []struct {
		name           string
		object         *unstructured.Unstructured
		featureEnabled bool
		want           bool
		wantReason     string
		wantErr        error
	}{
		{
			name: "feature gate disabled",
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"spec": map[string]any{
						"managedBy": kueue.MultiKueueControllerName,
					},
				},
			},
			featureEnabled: false,
			want:           false,
			wantReason:     "MultiKueueAdaptersForCustomJobs feature gate is disabled",
			wantErr:        nil,
		},
		{
			name: "managed by kueue with default path",
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"spec": map[string]any{
						"managedBy": kueue.MultiKueueControllerName,
					},
				},
			},
			featureEnabled: true,
			want:           true,
			wantReason:     "",
			wantErr:        nil,
		},
		{
			name: "not managed by kueue",
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"spec": map[string]any{
						"managedBy": "other-controller",
					},
				},
			},
			featureEnabled: true,
			want:           false,
			wantReason:     "Expecting .spec.managedBy to be \"kueue.x-k8s.io/multikueue\" not \"other-controller\"",
			wantErr:        nil,
		},
		{
			name: "managedBy field not found",
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"spec": map[string]any{
						"otherField": "value",
					},
				},
			},
			featureEnabled: true,
			want:           false,
			wantReason:     "Expecting .spec.managedBy to be \"kueue.x-k8s.io/multikueue\" not \"\"",
			wantErr:        nil,
		},
		{
			name: "managedBy value is not a string",
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"spec": map[string]any{
						"managedBy": "not-a-string",
					},
				},
			},
			featureEnabled: true,
			want:           false,
			wantReason:     "Expecting .spec.managedBy to be \"kueue.x-k8s.io/multikueue\" not \"not-a-string\"",
			wantErr:        nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.MultiKueueAdaptersForCustomJobs, tt.featureEnabled)

			adapter := &Adapter{
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

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Adapter.IsJobManagedByKueue() error (-want,+got):\n%s", diff)
			}
			if got != tt.want {
				t.Errorf("Adapter.IsJobManagedByKueue() got = %v, want %v", got, tt.want)
			}
			if gotReason != tt.wantReason {
				t.Errorf("Adapter.IsJobManagedByKueue() gotReason = %v, want %v", gotReason, tt.wantReason)
			}
		})
	}
}

func TestAdapter_RemoveManagedByField(t *testing.T) {
	adapter := &Adapter{
		gvk: schema.GroupVersionKind{
			Group:   "test.example.com",
			Version: "v1",
			Kind:    "TestJob",
		},
	}

	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"spec": map[string]any{
				"managedBy":  "kueue.x-k8s.io/multikueue",
				"otherField": "value",
			},
		},
	}

	adapter.removeManagedByField(obj)

	// Check that managedBy field is removed
	if _, exists := obj.Object["spec"].(map[string]any)["managedBy"]; exists {
		t.Error("managedBy field should be removed")
	}

	// Check that other fields are preserved
	if obj.Object["spec"].(map[string]any)["otherField"] != "value" {
		t.Error("otherField should be preserved")
	}
}

func TestAdapter_CopyStatusFromRemote(t *testing.T) {
	adapter := &Adapter{
		gvk: schema.GroupVersionKind{
			Group:   "test.example.com",
			Version: "v1",
			Kind:    "TestJob",
		},
	}

	localObj := &unstructured.Unstructured{
		Object: map[string]any{
			"spec": map[string]any{
				"field": "value",
			},
		},
	}

	remoteObj := &unstructured.Unstructured{
		Object: map[string]any{
			"spec": map[string]any{
				"field": "value",
			},
			"status": map[string]any{
				"phase": "Running",
				"conditions": []any{
					map[string]any{
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
	if localStatus.(map[string]any)["phase"] != "Running" {
		t.Error("status phase should match")
	}
}

func TestAdapter_GetEmptyList(t *testing.T) {
	tests := []struct {
		name string
		gvk  schema.GroupVersionKind
		want schema.GroupVersionKind
	}{
		{
			name: "standard GVK",
			gvk: schema.GroupVersionKind{
				Group:   "test.example.com",
				Version: "v1",
				Kind:    "TestJob",
			},
			want: schema.GroupVersionKind{
				Group:   "test.example.com",
				Version: "v1",
				Kind:    "TestJobList",
			},
		},
		{
			name: "empty GVK",
			gvk:  schema.GroupVersionKind{},
			want: schema.GroupVersionKind{
				Group:   "",
				Version: "",
				Kind:    "List",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := NewAdapter(tt.gvk)
			result := adapter.(*Adapter).GetEmptyList()

			// Verify the result is an UnstructuredList
			unstructuredList, ok := result.(*unstructured.UnstructuredList)
			if !ok {
				t.Errorf("Expected *unstructured.UnstructuredList, got %T", result)
				return
			}

			// Verify the GVK is correct
			gvk := unstructuredList.GroupVersionKind()
			if gvk != tt.want {
				t.Errorf("Expected GVK %v, got %v", tt.want, gvk)
			}

			// Verify the Kind ends with "List"
			if !strings.HasSuffix(gvk.Kind, "List") {
				t.Errorf("Expected Kind to end with 'List', got %s", gvk.Kind)
			}
		})
	}
}

func TestAdapter_WorkloadKeyFor(t *testing.T) {
	tests := []struct {
		name       string
		gvk        schema.GroupVersionKind
		object     *unstructured.Unstructured
		want       types.NamespacedName
		wantErrMsg string
	}{
		{
			name: "valid object with prebuilt workload label",
			gvk: schema.GroupVersionKind{
				Group:   "test.example.com",
				Version: "v1",
				Kind:    "TestJob",
			},
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"metadata": map[string]any{
						"name":      "test-job",
						"namespace": "test-ns",
						"labels": map[string]any{
							constants.PrebuiltWorkloadLabel: "test-workload",
						},
					},
				},
			},
			want: types.NamespacedName{
				Name:      "test-workload",
				Namespace: "test-ns",
			},
		},
		{
			name: "object without prebuilt workload label",
			gvk: schema.GroupVersionKind{
				Group:   "test.example.com",
				Version: "v1",
				Kind:    "TestJob",
			},
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"metadata": map[string]any{
						"name":      "test-job",
						"namespace": "test-ns",
						"labels": map[string]any{
							"other-label": "value",
						},
					},
				},
			},
			want:       types.NamespacedName{},
			wantErrMsg: "no prebuilt workload found for TestJob: test-ns/test-job",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := NewAdapter(tt.gvk)
			tt.object.SetGroupVersionKind(tt.gvk)
			tt.object.SetName("test-job")
			tt.object.SetNamespace("test-ns")

			result, err := adapter.(*Adapter).WorkloadKeyFor(tt.object)

			if tt.wantErrMsg != "" {
				if err == nil {
					t.Errorf("Expected error but got none")
				} else if !strings.Contains(err.Error(), tt.wantErrMsg) {
					t.Errorf("Expected error to contain '%s', got '%s'", tt.wantErrMsg, err.Error())
				}
			} else if err != nil {
				t.Errorf("Expected no error but got: %v", err)
			}

			if tt.wantErrMsg == "" {
				if diff := cmp.Diff(tt.want, result); diff != "" {
					t.Errorf("Adapter.WorkloadKeyFor() result (-want,+got):\n%s", diff)
				}
			}
		})
	}
}
