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

package jobframework

import (
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

func TestCreateWithPreservedSpec(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}
	key := types.NamespacedName{Name: "job", Namespace: "ns"}
	managedBy := "kueue.x-k8s.io/multikueue"
	local := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec:       batchv1.JobSpec{ManagedBy: &managedBy},
	}
	destination := local.DeepCopy()
	destination.Spec.ManagedBy = nil
	rawSource := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata": map[string]any{
			"name":      key.Name,
			"namespace": key.Namespace,
		},
		"spec": map[string]any{
			"managedBy":    managedBy,
			"futureField":  "retained",
			"futureConfig": map[string]any{"enabled": true},
		},
	}}
	rawSource.SetGroupVersionKind(gvk)

	created, err := NewUnstructuredWithPreservedSpec(rawSource, local, destination)
	if err != nil {
		t.Fatalf("NewUnstructuredWithPreservedSpec() error = %v", err)
	}
	if got, found, err := unstructured.NestedString(created.Object, "spec", "futureField"); err != nil || !found || got != "retained" {
		t.Fatalf("futureField = %q, found = %t, err = %v; want retained", got, found, err)
	}
	if got, found, err := unstructured.NestedMap(created.Object, "spec", "futureConfig"); err != nil || !found || got["enabled"] != true {
		t.Fatalf("futureConfig = %#v, found = %t, err = %v; want enabled=true", got, found, err)
	}
	if _, found, err := unstructured.NestedFieldNoCopy(created.Object, "spec", "managedBy"); err != nil || found {
		t.Fatalf("managedBy found = %t, err = %v; want absent", found, err)
	}
}

// TestNewUnstructuredWithPreservedSpec_RetainedArrayElementUnknownFields verifies
// that when a typed destination removes an element from an array, the removed
// element is deleted while unknown/unmodeled fields on retained sibling elements
// are preserved.
func TestNewUnstructuredWithPreservedSpec_RetainedArrayElementUnknownFields(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}
	key := types.NamespacedName{Name: "job", Namespace: "ns"}
	managedBy := "kueue.x-k8s.io/multikueue"

	local := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: batchv1.JobSpec{
			ManagedBy: &managedBy,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "injected-to-remove", Image: "helper:v1"},
						{Name: "user-app", Image: "app:v1"},
					},
				},
			},
		},
	}
	destination := local.DeepCopy()
	destination.Spec.ManagedBy = nil
	destination.Spec.Template.Spec.Containers = []corev1.Container{
		{Name: "user-app", Image: "app:v1"},
	}

	rawSource := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata": map[string]any{
			"name":      key.Name,
			"namespace": key.Namespace,
		},
		"spec": map[string]any{
			"managedBy": managedBy,
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{
						map[string]any{
							"name":  "injected-to-remove",
							"image": "helper:v1",
						},
						map[string]any{
							"name":             "user-app",
							"image":            "app:v1",
							"futureAnnotation": "preserved",
						},
					},
				},
			},
		},
	}}
	rawSource.SetGroupVersionKind(gvk)

	created, err := NewUnstructuredWithPreservedSpec(rawSource, local, destination)
	if err != nil {
		t.Fatalf("NewUnstructuredWithPreservedSpec() error = %v", err)
	}

	containers, found, err := unstructured.NestedSlice(created.Object, "spec", "template", "spec", "containers")
	if err != nil || !found {
		t.Fatalf("containers not found: found=%t err=%v", found, err)
	}
	if len(containers) != 1 {
		t.Fatalf("containers len = %d; want 1", len(containers))
	}
	c0, ok := containers[0].(map[string]any)
	if !ok {
		t.Fatalf("containers[0] is not a map")
	}
	if got := c0["futureAnnotation"]; got != "preserved" {
		t.Errorf("containers[0].futureAnnotation = %v; want \"preserved\"", got)
	}
	if got := c0["name"]; got != "user-app" {
		t.Errorf("containers[0].name = %v; want \"user-app\"", got)
	}
	// The managedBy field must have been removed.
	if _, found, err := unstructured.NestedFieldNoCopy(created.Object, "spec", "managedBy"); err != nil || found {
		t.Fatalf("managedBy found = %t, err = %v; want absent", found, err)
	}
}

func TestNewUnstructuredWithPreservedSpec_ArrayModificationPreservesSiblingUnknownFields(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}
	key := types.NamespacedName{Name: "job", Namespace: "ns"}

	local := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "main", Image: "app:v1"},
						{Name: "sidecar", Image: "sidecar:v1"},
					},
				},
			},
		},
	}

	destination := local.DeepCopy()
	destination.Spec.Template.Spec.Containers[0].Image = "app:v2"

	rawSource := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata": map[string]any{
			"name":      key.Name,
			"namespace": key.Namespace,
		},
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{
						map[string]any{
							"name":  "main",
							"image": "app:v1",
						},
						map[string]any{
							"name":        "sidecar",
							"image":       "sidecar:v1",
							"futureField": "keep-me",
						},
					},
				},
			},
		},
	}}
	rawSource.SetGroupVersionKind(gvk)

	created, err := NewUnstructuredWithPreservedSpec(rawSource, local, destination)
	if err != nil {
		t.Fatalf("NewUnstructuredWithPreservedSpec() error = %v", err)
	}

	containers, found, err := unstructured.NestedSlice(created.Object, "spec", "template", "spec", "containers")
	if err != nil || !found {
		t.Fatalf("containers not found: found=%t err=%v", found, err)
	}
	if len(containers) != 2 {
		t.Fatalf("containers length = %d; want 2", len(containers))
	}

	c0, ok := containers[0].(map[string]any)
	if !ok {
		t.Fatalf("containers[0] is not a map")
	}
	if got := c0["image"]; got != "app:v2" {
		t.Errorf("containers[0].image = %v; want \"app:v2\"", got)
	}

	c1, ok := containers[1].(map[string]any)
	if !ok {
		t.Fatalf("containers[1] is not a map")
	}
	if got := c1["futureField"]; got != "keep-me" {
		t.Errorf("containers[1].futureField = %v; want \"keep-me\"", got)
	}
}

func TestNewUnstructuredWithPreservedSpec_DuplicateKeysFallbackToPositional(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}
	key := types.NamespacedName{Name: "job", Namespace: "ns"}

	local := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "dup", Image: "app:v1"},
						{Name: "dup", Image: "app:v2"},
					},
				},
			},
		},
	}

	destination := local.DeepCopy()
	destination.Spec.Template.Spec.Containers[0].Image = "app:v1-updated"

	rawSource := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata": map[string]any{
			"name":      key.Name,
			"namespace": key.Namespace,
		},
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{
						map[string]any{
							"name":   "dup",
							"image":  "app:v1",
							"extra1": "keep-1",
						},
						map[string]any{
							"name":   "dup",
							"image":  "app:v2",
							"extra2": "keep-2",
						},
					},
				},
			},
		},
	}}
	rawSource.SetGroupVersionKind(gvk)

	created, err := NewUnstructuredWithPreservedSpec(rawSource, local, destination)
	if err != nil {
		t.Fatalf("NewUnstructuredWithPreservedSpec() error = %v", err)
	}

	containers, found, err := unstructured.NestedSlice(created.Object, "spec", "template", "spec", "containers")
	if err != nil || !found {
		t.Fatalf("containers not found: found=%t err=%v", found, err)
	}
	if len(containers) != 2 {
		t.Fatalf("containers length = %d; want 2", len(containers))
	}

	c0, ok := containers[0].(map[string]any)
	if !ok {
		t.Fatalf("containers[0] is not a map")
	}
	if got := c0["image"]; got != "app:v1-updated" {
		t.Errorf("containers[0].image = %v; want \"app:v1-updated\"", got)
	}
	if got := c0["extra1"]; got != "keep-1" {
		t.Errorf("containers[0].extra1 = %v; want \"keep-1\"", got)
	}
	if _, found := c0["extra2"]; found {
		t.Errorf("containers[0] unexpectedly contains extra2")
	}

	c1, ok := containers[1].(map[string]any)
	if !ok {
		t.Fatalf("containers[1] is not a map")
	}
	if got := c1["image"]; got != "app:v2" {
		t.Errorf("containers[1].image = %v; want \"app:v2\"", got)
	}
	if got := c1["extra2"]; got != "keep-2" {
		t.Errorf("containers[1].extra2 = %v; want \"keep-2\"", got)
	}
	if _, found := c1["extra1"]; found {
		t.Errorf("containers[1] unexpectedly contains extra1")
	}
}


