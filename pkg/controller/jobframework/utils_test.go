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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/podset"
)

// mockGenericJob is a simple mock implementation of GenericJob for testing
type mockGenericJob struct {
	obj client.Object
	gvk schema.GroupVersionKind
}

func (m *mockGenericJob) Object() client.Object {
	return m.obj
}

func (m *mockGenericJob) IsSuspended() bool {
	return false
}

func (m *mockGenericJob) Suspend() {}

func (m *mockGenericJob) RunWithPodSetsInfo(podSetsInfo []podset.PodSetInfo) error {
	return nil
}

func (m *mockGenericJob) RestorePodSetsInfo(podSetsInfo []podset.PodSetInfo) bool {
	return false
}

func (m *mockGenericJob) Finished() (message string, success, finished bool) {
	return "", false, false
}

func (m *mockGenericJob) PodSets() ([]kueue.PodSet, error) {
	return nil, nil
}

func (m *mockGenericJob) IsActive() bool {
	return false
}

func (m *mockGenericJob) PodsReady() bool {
	return false
}

func (m *mockGenericJob) GVK() schema.GroupVersionKind {
	return m.gvk
}

func TestRegisterGenericJobConvertFunc(t *testing.T) {
	testGVK := schema.GroupVersionKind{
		Group:   "test.example.com",
		Version: "v1",
		Kind:    "TestJob",
	}

	// Clean up after test
	defer func() {
		jobConvertRegistry.Delete(testGVK)
	}()

	t.Run("register converter function", func(t *testing.T) {
		converterCalled := false
		testConverter := func(obj runtime.Object) GenericJob {
			converterCalled = true
			return &mockGenericJob{
				obj: obj.(client.Object),
				gvk: testGVK,
			}
		}

		RegisterGenericJobConvertFunc(testGVK, testConverter)

		// Verify the function was registered by loading it
		val, ok := jobConvertRegistry.Load(testGVK)
		if !ok {
			t.Error("converter function was not registered")
		}

		// Verify the registered function works
		if val == nil {
			t.Error("registered converter is nil")
		}

		// Create a test object and call the converter
		testObj := &batchv1.Job{
			TypeMeta: metav1.TypeMeta{
				APIVersion: testGVK.GroupVersion().String(),
				Kind:       testGVK.Kind,
			},
		}
		testObj.SetGroupVersionKind(testGVK)

		result := val.(runtimeObjectToJobConvertFunc)(testObj)
		if result == nil {
			t.Error("converter returned nil")
		}
		if !converterCalled {
			t.Error("converter was not called")
		}
	})

	t.Run("register nil deletes converter", func(t *testing.T) {
		// First register a converter
		testConverter := func(obj runtime.Object) GenericJob {
			return &mockGenericJob{}
		}
		RegisterGenericJobConvertFunc(testGVK, testConverter)

		// Verify it's registered
		_, ok := jobConvertRegistry.Load(testGVK)
		if !ok {
			t.Error("converter function was not registered")
		}

		// Now delete it by registering nil
		RegisterGenericJobConvertFunc(testGVK, nil)

		// Verify it's deleted
		_, ok = jobConvertRegistry.Load(testGVK)
		if ok {
			t.Error("converter function was not deleted when nil was registered")
		}
	})

	t.Run("multiple registrations overwrite", func(t *testing.T) {
		firstCalled := false
		firstConverter := func(obj runtime.Object) GenericJob {
			firstCalled = true
			return &mockGenericJob{}
		}

		secondCalled := false
		secondConverter := func(obj runtime.Object) GenericJob {
			secondCalled = true
			return &mockGenericJob{}
		}

		// Register first converter
		RegisterGenericJobConvertFunc(testGVK, firstConverter)

		// Register second converter (should overwrite first)
		RegisterGenericJobConvertFunc(testGVK, secondConverter)

		// Load and call the converter
		val, ok := jobConvertRegistry.Load(testGVK)
		if !ok {
			t.Fatal("converter function was not registered")
		}

		testObj := &batchv1.Job{}
		testObj.SetGroupVersionKind(testGVK)
		val.(runtimeObjectToJobConvertFunc)(testObj)

		// Verify only the second converter was called
		if firstCalled {
			t.Error("first converter should not have been called after being overwritten")
		}
		if !secondCalled {
			t.Error("second converter should have been called")
		}
	})
}

func TestCreateGenericJobFromRuntimeObject(t *testing.T) {
	testGVK := schema.GroupVersionKind{
		Group:   "test.example.com",
		Version: "v1",
		Kind:    "TestJob",
	}

	// Clean up after tests
	defer func() {
		jobConvertRegistry.Delete(testGVK)
	}()

	t.Run("successfully convert with registered converter", func(t *testing.T) {
		expectedJob := &mockGenericJob{gvk: testGVK}
		testConverter := func(obj runtime.Object) GenericJob {
			return expectedJob
		}

		RegisterGenericJobConvertFunc(testGVK, testConverter)

		testObj := &batchv1.Job{
			TypeMeta: metav1.TypeMeta{
				APIVersion: testGVK.GroupVersion().String(),
				Kind:       testGVK.Kind,
			},
		}
		testObj.SetGroupVersionKind(testGVK)

		result := CreateGenericJobFromRuntimeObject(testObj)
		if result == nil {
			t.Fatal("CreateGenericJobFromRuntimeObject returned nil")
		}
		// Compare the underlying objects since result is an interface
		mockResult, ok := result.(*mockGenericJob)
		if !ok {
			t.Error("result is not of type *mockGenericJob")
		}
		if mockResult != expectedJob {
			t.Error("returned job does not match expected job")
		}
	})

	t.Run("return nil for object with no registered converter", func(t *testing.T) {
		unregisteredGVK := schema.GroupVersionKind{
			Group:   "unregistered.example.com",
			Version: "v1",
			Kind:    "UnregisteredJob",
		}

		testObj := &batchv1.Job{
			TypeMeta: metav1.TypeMeta{
				APIVersion: unregisteredGVK.GroupVersion().String(),
				Kind:       unregisteredGVK.Kind,
			},
		}
		testObj.SetGroupVersionKind(unregisteredGVK)

		result := CreateGenericJobFromRuntimeObject(testObj)
		if result != nil {
			t.Error("CreateGenericJobFromRuntimeObject should return nil for unregistered GVK")
		}
	})

	t.Run("return nil for object with nil ObjectKind", func(t *testing.T) {
		// Create an object with nil GVK
		testObj := &batchv1.Job{}
		// Explicitly set an empty GVK to simulate nil ObjectKind scenario
		testObj.SetGroupVersionKind(schema.GroupVersionKind{})

		result := CreateGenericJobFromRuntimeObject(testObj)
		if result != nil {
			t.Error("CreateGenericJobFromRuntimeObject should return nil for object with empty GVK")
		}
	})

	t.Run("converter returns correct GenericJob", func(t *testing.T) {
		testGVK2 := schema.GroupVersionKind{
			Group:   "another.example.com",
			Version: "v2",
			Kind:    "AnotherJob",
		}

		defer jobConvertRegistry.Delete(testGVK2)

		testObj := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-job",
				Namespace: "default",
			},
			TypeMeta: metav1.TypeMeta{
				APIVersion: testGVK2.GroupVersion().String(),
				Kind:       testGVK2.Kind,
			},
		}
		testObj.SetGroupVersionKind(testGVK2)

		testConverter := func(obj runtime.Object) GenericJob {
			job := obj.(*batchv1.Job)
			return &mockGenericJob{
				obj: job,
				gvk: testGVK2,
			}
		}

		RegisterGenericJobConvertFunc(testGVK2, testConverter)

		result := CreateGenericJobFromRuntimeObject(testObj)
		if result == nil {
			t.Fatal("CreateGenericJobFromRuntimeObject returned nil")
		}

		if result.GVK() != testGVK2 {
			t.Errorf("expected GVK %v, got %v", testGVK2, result.GVK())
		}

		if result.Object() != testObj {
			t.Error("GenericJob does not contain the original object")
		}
	})
}
