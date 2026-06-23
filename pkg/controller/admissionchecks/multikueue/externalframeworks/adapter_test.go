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
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
)

func TestAdapter_IsJobManagedByKueue(t *testing.T) {
	tests := []struct {
		name         string
		object       *unstructured.Unstructured
		featureGates map[featuregate.Feature]bool
		want         bool
		wantReason   string
		wantErr      error
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
			featureGates: map[featuregate.Feature]bool{features.MultiKueueAdaptersForCustomJobs: false},
			want:         false,
			wantReason:   "MultiKueueAdaptersForCustomJobs feature gate is disabled",
			wantErr:      nil,
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
			featureGates: map[featuregate.Feature]bool{features.MultiKueueAdaptersForCustomJobs: true},
			want:         true,
			wantReason:   "",
			wantErr:      nil,
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
			featureGates: map[featuregate.Feature]bool{features.MultiKueueAdaptersForCustomJobs: true},
			want:         false,
			wantReason:   "Expecting .spec.managedBy to be \"kueue.x-k8s.io/multikueue\" not \"other-controller\"",
			wantErr:      nil,
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
			featureGates: map[featuregate.Feature]bool{features.MultiKueueAdaptersForCustomJobs: true},
			want:         false,
			wantReason:   "Expecting .spec.managedBy to be \"kueue.x-k8s.io/multikueue\" not \"\"",
			wantErr:      nil,
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
			featureGates: map[featuregate.Feature]bool{features.MultiKueueAdaptersForCustomJobs: true},
			want:         false,
			wantReason:   "Expecting .spec.managedBy to be \"kueue.x-k8s.io/multikueue\" not \"not-a-string\"",
			wantErr:      nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tt.featureGates)

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

			got, gotReason, err := adapter.IsJobManagedByKueue(t.Context(), client, key)

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

func TestCloneUnstructuredForCreation(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "test.example.com", Version: "v1", Kind: "TestJob"}

	cases := map[string]struct {
		metadata map[string]any
		spec     any
		want     *unstructured.Unstructured
	}{
		"strips ownerReferences, uid, resourceVersion, finalizers and managedFields": {
			metadata: map[string]any{
				"name":            "job1",
				"namespace":       "ns1",
				"uid":             "local-uid",
				"resourceVersion": "123",
				"labels":          map[string]any{"app": "foo"},
				"annotations":     map[string]any{"k": "v"},
				"ownerReferences": []any{map[string]any{"name": "owner", "uid": "owner-uid"}},
				"finalizers":      []any{"some/finalizer"},
				"managedFields":   []any{map[string]any{"manager": "kueue"}},
			},
			spec: map[string]any{"replicas": int64(3)},
			want: &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "test.example.com/v1",
				"kind":       "TestJob",
				"metadata": map[string]any{
					"name":        "job1",
					"namespace":   "ns1",
					"labels":      map[string]any{"app": "foo"},
					"annotations": map[string]any{"k": "v"},
				},
				"spec": map[string]any{"replicas": int64(3)},
			}},
		},
		"keeps only name, namespace and spec when no labels or annotations": {
			metadata: map[string]any{
				"name":            "job1",
				"namespace":       "ns1",
				"ownerReferences": []any{map[string]any{"name": "owner", "uid": "owner-uid"}},
			},
			spec: map[string]any{"replicas": int64(1)},
			want: &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "test.example.com/v1",
				"kind":       "TestJob",
				"metadata": map[string]any{
					"name":      "job1",
					"namespace": "ns1",
				},
				"spec": map[string]any{"replicas": int64(1)},
			}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			obj := &unstructured.Unstructured{Object: map[string]any{
				"metadata": tc.metadata,
				"spec":     tc.spec,
				"status":   map[string]any{"phase": "Running"},
			}}
			obj.SetGroupVersionKind(gvk)

			got, err := cloneUnstructuredForCreation(obj)
			if err != nil {
				t.Fatalf("cloneUnstructuredForCreation() error: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("cloneUnstructuredForCreation() (-want,+got):\n%s", diff)
			}

			// The returned spec must be an independent copy of the source.
			if err := unstructured.SetNestedField(got.Object, int64(99), "spec", "replicas"); err != nil {
				t.Fatalf("SetNestedField: %v", err)
			}
			if replicas, _, _ := unstructured.NestedInt64(obj.Object, "spec", "replicas"); replicas == 99 {
				t.Errorf("mutating the clone's spec must not affect the source object")
			}
		})
	}
}

func TestAdapter_CreateRemoteObject(t *testing.T) {
	gvk := schema.GroupVersionKind{
		Group:   "test.example.com",
		Version: "v1",
		Kind:    "TestJob",
	}
	adapter := &Adapter{gvk: gvk}

	localObj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name":            "test-job",
			"namespace":       "default",
			"uid":             "local-uid",
			"resourceVersion": "999",
			"labels":          map[string]any{"app": "foo"},
			"annotations":     map[string]any{"key": "value"},
			"ownerReferences": []any{
				map[string]any{
					"apiVersion": "batch/v1",
					"kind":       "Job",
					"name":       "owner",
					"uid":        "owner-uid",
				},
			},
			"finalizers": []any{"some/finalizer"},
		},
		"spec": map[string]any{
			"managedBy": kueue.MultiKueueControllerName,
			"replicas":  int64(3),
		},
		"status": map[string]any{
			"phase": "Running",
		},
	}}
	localObj.SetGroupVersionKind(gvk)

	remoteClient := fake.NewClientBuilder().Build()

	if err := adapter.createRemoteObject(t.Context(), remoteClient, localObj, "test-workload", "origin1"); err != nil {
		t.Fatalf("createRemoteObject() error = %v", err)
	}

	created := &unstructured.Unstructured{}
	created.SetGroupVersionKind(gvk)
	key := types.NamespacedName{Name: "test-job", Namespace: "default"}
	if err := remoteClient.Get(t.Context(), key, created); err != nil {
		t.Fatalf("Get created remote object error = %v", err)
	}

	// ownerReferences must be dropped, else the remote GC would delete the object.
	if refs := created.GetOwnerReferences(); len(refs) != 0 {
		t.Errorf("remote object should have no ownerReferences, got %v", refs)
	}
	if uid := created.GetUID(); uid != "" {
		t.Errorf("remote object should have no uid carried over, got %q", uid)
	}
	if finalizers := created.GetFinalizers(); len(finalizers) != 0 {
		t.Errorf("remote object should have no finalizers, got %v", finalizers)
	}
	if _, exists, _ := unstructured.NestedMap(created.Object, "status"); exists {
		t.Errorf("remote object should not carry over local status")
	}

	if got := created.GetAnnotations(); got["key"] != "value" {
		t.Errorf("annotations should be preserved, got %v", got)
	}
	labels := created.GetLabels()
	if labels["app"] != "foo" {
		t.Errorf("labels should be preserved, got %v", labels)
	}
	if labels[kueue.MultiKueueOriginLabel] != "origin1" {
		t.Errorf("MultiKueue origin label should be set, got %v", labels)
	}
	// The prebuilt name lands in a label or annotation per WorkloadIdentifierAnnotations.
	if labels[constants.PrebuiltWorkloadLabel] != "test-workload" &&
		created.GetAnnotations()[constants.PrebuiltWorkloadAnnotation] != "test-workload" {
		t.Errorf("prebuilt workload name should be set, labels=%v annotations=%v", labels, created.GetAnnotations())
	}
	if _, exists, _ := unstructured.NestedString(created.Object, "spec", "managedBy"); exists {
		t.Errorf("spec.managedBy should be removed")
	}
	if replicas, _, _ := unstructured.NestedInt64(created.Object, "spec", "replicas"); replicas != 3 {
		t.Errorf("spec.replicas should be preserved, got %d", replicas)
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

func TestAdapter_WorkloadKeysFor(t *testing.T) {
	tests := []struct {
		name       string
		gvk        schema.GroupVersionKind
		object     *unstructured.Unstructured
		want       []types.NamespacedName
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
			want: []types.NamespacedName{{
				Name:      "test-workload",
				Namespace: "test-ns",
			}},
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
			want:       nil,
			wantErrMsg: "no prebuilt workload found for TestJob: test-ns/test-job",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := NewAdapter(tt.gvk)
			tt.object.SetGroupVersionKind(tt.gvk)
			tt.object.SetName("test-job")
			tt.object.SetNamespace("test-ns")

			result, err := adapter.(*Adapter).WorkloadKeysFor(tt.object)

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
					t.Errorf("Adapter.WorkloadKeysFor() result (-want,+got):\n%s", diff)
				}
			}
		})
	}
}
