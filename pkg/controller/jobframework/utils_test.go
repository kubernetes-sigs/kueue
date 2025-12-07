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

package jobframework_test

import (
	"testing"

	"go.uber.org/mock/gomock"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	mocks "sigs.k8s.io/kueue/internal/mocks/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
)

func TestRegisterGenericJobConvertFunc(t *testing.T) {
	testGVK := schema.GroupVersionKind{
		Group:   "test.example.com",
		Version: "v1",
		Kind:    "TestJob",
	}

	// Clean up after test (note: we can't access the internal registry directly from _test package)
	defer func() {
		jobframework.RegisterGenericJobConvertFunc(testGVK, nil)
	}()

	t.Run("register converter function", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		converterCalled := false
		testConverter := func(obj runtime.Object) jobframework.GenericJob {
			converterCalled = true
			mockJob := mocks.NewMockGenericJob(ctrl)
			return mockJob
		}

		jobframework.RegisterGenericJobConvertFunc(testGVK, testConverter)

		// Create a test object and call CreateGenericJobFromRuntimeObject
		testObj := &batchv1.Job{
			TypeMeta: metav1.TypeMeta{
				APIVersion: testGVK.GroupVersion().String(),
				Kind:       testGVK.Kind,
			},
		}
		testObj.SetGroupVersionKind(testGVK)

		result := jobframework.CreateGenericJobFromRuntimeObject(testObj)
		if result == nil {
			t.Error("CreateGenericJobFromRuntimeObject returned nil")
		}
		if !converterCalled {
			t.Error("converter was not called")
		}
	})

	t.Run("register nil deletes converter", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		// First register a converter
		testConverter := func(obj runtime.Object) jobframework.GenericJob {
			return mocks.NewMockGenericJob(ctrl)
		}
		jobframework.RegisterGenericJobConvertFunc(testGVK, testConverter)

		// Test that we can create a generic job (converter is registered)
		testObj := &batchv1.Job{}
		testObj.SetGroupVersionKind(testGVK)
		result := jobframework.CreateGenericJobFromRuntimeObject(testObj)
		if result == nil {
			t.Error("CreateGenericJobFromRuntimeObject should work with registered converter")
		}

		// Now delete it by registering nil
		jobframework.RegisterGenericJobConvertFunc(testGVK, nil)

		// Test that we can no longer create a generic job (converter is deleted)
		result = jobframework.CreateGenericJobFromRuntimeObject(testObj)
		if result != nil {
			t.Error("CreateGenericJobFromRuntimeObject should return nil after converter is deleted")
		}
	})

	t.Run("multiple registrations overwrite", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		firstCalled := false
		firstConverter := func(obj runtime.Object) jobframework.GenericJob {
			firstCalled = true
			return mocks.NewMockGenericJob(ctrl)
		}

		secondCalled := false
		secondConverter := func(obj runtime.Object) jobframework.GenericJob {
			secondCalled = true
			return mocks.NewMockGenericJob(ctrl)
		}

		// Register first converter
		jobframework.RegisterGenericJobConvertFunc(testGVK, firstConverter)

		// Register second converter (should overwrite first)
		jobframework.RegisterGenericJobConvertFunc(testGVK, secondConverter)

		// Test the converter by calling CreateGenericJobFromRuntimeObject
		testObj := &batchv1.Job{}
		testObj.SetGroupVersionKind(testGVK)
		jobframework.CreateGenericJobFromRuntimeObject(testObj)

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
		jobframework.RegisterGenericJobConvertFunc(testGVK, nil)
	}()

	t.Run("successfully convert with registered converter", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		expectedJob := mocks.NewMockGenericJob(ctrl)
		testConverter := func(obj runtime.Object) jobframework.GenericJob {
			return expectedJob
		}

		jobframework.RegisterGenericJobConvertFunc(testGVK, testConverter)

		testObj := &batchv1.Job{
			TypeMeta: metav1.TypeMeta{
				APIVersion: testGVK.GroupVersion().String(),
				Kind:       testGVK.Kind,
			},
		}
		testObj.SetGroupVersionKind(testGVK)

		result := jobframework.CreateGenericJobFromRuntimeObject(testObj)
		if result == nil {
			t.Fatal("CreateGenericJobFromRuntimeObject returned nil")
		}
		// Compare the underlying objects since result is an interface
		mockResult, ok := result.(*mocks.MockGenericJob)
		if !ok {
			t.Error("result is not of type *mocks.MockGenericJob")
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

		result := jobframework.CreateGenericJobFromRuntimeObject(testObj)
		if result != nil {
			t.Error("CreateGenericJobFromRuntimeObject should return nil for unregistered GVK")
		}
	})

	t.Run("return nil for object with nil ObjectKind", func(t *testing.T) {
		// Create an object with nil GVK
		testObj := &batchv1.Job{}
		// Explicitly set an empty GVK to simulate nil ObjectKind scenario
		testObj.SetGroupVersionKind(schema.GroupVersionKind{})

		result := jobframework.CreateGenericJobFromRuntimeObject(testObj)
		if result != nil {
			t.Error("CreateGenericJobFromRuntimeObject should return nil for object with empty GVK")
		}
	})

	t.Run("converter returns correct GenericJob", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		testGVK2 := schema.GroupVersionKind{
			Group:   "another.example.com",
			Version: "v2",
			Kind:    "AnotherJob",
		}

		defer jobframework.RegisterGenericJobConvertFunc(testGVK2, nil)

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

		testConverter := func(obj runtime.Object) jobframework.GenericJob {
			job := obj.(*batchv1.Job)
			mockJob := mocks.NewMockGenericJob(ctrl)
			mockJob.EXPECT().Object().Return(job).AnyTimes()
			mockJob.EXPECT().GVK().Return(testGVK2).AnyTimes()
			return mockJob
		}

		jobframework.RegisterGenericJobConvertFunc(testGVK2, testConverter)

		result := jobframework.CreateGenericJobFromRuntimeObject(testObj)
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
