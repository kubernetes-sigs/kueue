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

package dra

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
)

func TestNewDRAResourceMapper(t *testing.T) {
	mapper := newDRAResourceMapper()

	if mapper == nil {
		t.Fatal("newDRAResourceMapper() returned nil")
	}

	if mapper.deviceClassToResource == nil {
		t.Fatal("deviceClassToResource map not initialized")
	}

	// Test empty mapping lookup
	_, found := mapper.lookup("nonexistent")
	if found {
		t.Error("Expected lookup to fail on empty mapping")
	}
}

func TestDRAResourceMapper_Lookup(t *testing.T) {
	tests := []struct {
		name    string
		config  *kueuealpha.DynamicResourceAllocationConfig
		lookups []struct {
			deviceClass    corev1.ResourceName
			expectedRes    corev1.ResourceName
			expectedExists bool
		}
	}{
		{
			name: "single device class mapping",
			config: &kueuealpha.DynamicResourceAllocationConfig{
				ObjectMeta: metav1.ObjectMeta{Name: DefaultDRAConfigName},
				Spec: kueuealpha.DynamicResourceAllocationConfigSpec{
					Resources: []kueuealpha.DynamicResource{
						{
							Name:             kueuealpha.DriverResourceName("example.com/gpu"),
							DeviceClassNames: []kueuealpha.DriverResourceName{"example.com/gpu-class"},
						},
					},
				},
			},
			lookups: []struct {
				deviceClass    corev1.ResourceName
				expectedRes    corev1.ResourceName
				expectedExists bool
			}{
				{
					deviceClass:    "example.com/gpu-class",
					expectedRes:    "example.com/gpu",
					expectedExists: true,
				},
				{
					deviceClass:    "example.com/nonexistent",
					expectedRes:    "",
					expectedExists: false,
				},
			},
		},
		{
			name: "multiple device classes to single resource",
			config: &kueuealpha.DynamicResourceAllocationConfig{
				ObjectMeta: metav1.ObjectMeta{Name: DefaultDRAConfigName},
				Spec: kueuealpha.DynamicResourceAllocationConfigSpec{
					Resources: []kueuealpha.DynamicResource{
						{
							Name: kueuealpha.DriverResourceName("example.com/accelerator"),
							DeviceClassNames: []kueuealpha.DriverResourceName{
								"example.com/gpu-a100",
								"example.com/gpu-v100",
								"example.com/tpu-v4",
							},
						},
					},
				},
			},
			lookups: []struct {
				deviceClass    corev1.ResourceName
				expectedRes    corev1.ResourceName
				expectedExists bool
			}{
				{
					deviceClass:    "example.com/gpu-a100",
					expectedRes:    "example.com/accelerator",
					expectedExists: true,
				},
				{
					deviceClass:    "example.com/gpu-v100",
					expectedRes:    "example.com/accelerator",
					expectedExists: true,
				},
				{
					deviceClass:    "example.com/tpu-v4",
					expectedRes:    "example.com/accelerator",
					expectedExists: true,
				},
				{
					deviceClass:    "example.com/unknown",
					expectedRes:    "",
					expectedExists: false,
				},
			},
		},
		{
			name: "multiple resources with different device classes",
			config: &kueuealpha.DynamicResourceAllocationConfig{
				ObjectMeta: metav1.ObjectMeta{Name: DefaultDRAConfigName},
				Spec: kueuealpha.DynamicResourceAllocationConfigSpec{
					Resources: []kueuealpha.DynamicResource{
						{
							Name:             kueuealpha.DriverResourceName("example.com/gpu"),
							DeviceClassNames: []kueuealpha.DriverResourceName{"example.com/gpu-class"},
						},
						{
							Name:             kueuealpha.DriverResourceName("example.com/fpga"),
							DeviceClassNames: []kueuealpha.DriverResourceName{"example.com/fpga-class"},
						},
					},
				},
			},
			lookups: []struct {
				deviceClass    corev1.ResourceName
				expectedRes    corev1.ResourceName
				expectedExists bool
			}{
				{
					deviceClass:    "example.com/gpu-class",
					expectedRes:    "example.com/gpu",
					expectedExists: true,
				},
				{
					deviceClass:    "example.com/fpga-class",
					expectedRes:    "example.com/fpga",
					expectedExists: true,
				},
			},
		},
		{
			name: "empty config",
			config: &kueuealpha.DynamicResourceAllocationConfig{
				ObjectMeta: metav1.ObjectMeta{Name: DefaultDRAConfigName},
				Spec: kueuealpha.DynamicResourceAllocationConfigSpec{
					Resources: []kueuealpha.DynamicResource{},
				},
			},
			lookups: []struct {
				deviceClass    corev1.ResourceName
				expectedRes    corev1.ResourceName
				expectedExists bool
			}{
				{
					deviceClass:    "example.com/any",
					expectedRes:    "",
					expectedExists: false,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapper := newDRAResourceMapper()
			err := mapper.updateFromConfig(context.Background(), tt.config)
			if err != nil {
				t.Fatalf("updateFromConfig failed: %v", err)
			}

			for _, lookup := range tt.lookups {
				actualRes, actualExists := mapper.lookup(lookup.deviceClass)

				if actualExists != lookup.expectedExists {
					t.Errorf("lookup(%s): expected exists=%v, got exists=%v",
						lookup.deviceClass, lookup.expectedExists, actualExists)
				}

				if actualRes != lookup.expectedRes {
					t.Errorf("lookup(%s): expected resource=%s, got resource=%s",
						lookup.deviceClass, lookup.expectedRes, actualRes)
				}
			}
		})
	}
}

func TestDRAResourceMapper_LoadFromConfig(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = kueuealpha.AddToScheme(scheme)

	tests := []struct {
		name           string
		existingConfig *kueuealpha.DynamicResourceAllocationConfig
		expectError    bool
		expectMapping  map[corev1.ResourceName]corev1.ResourceName
	}{
		{
			name: "load existing config",
			existingConfig: &kueuealpha.DynamicResourceAllocationConfig{
				ObjectMeta: metav1.ObjectMeta{Name: DefaultDRAConfigName},
				Spec: kueuealpha.DynamicResourceAllocationConfigSpec{
					Resources: []kueuealpha.DynamicResource{
						{
							Name:             kueuealpha.DriverResourceName("example.com/gpu"),
							DeviceClassNames: []kueuealpha.DriverResourceName{"example.com/gpu-class"},
						},
					},
				},
			},
			expectError: false,
			expectMapping: map[corev1.ResourceName]corev1.ResourceName{
				"example.com/gpu-class": "example.com/gpu",
			},
		},
		{
			name:           "config not found",
			existingConfig: nil,
			expectError:    true,
			expectMapping:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objs []client.Object
			if tt.existingConfig != nil {
				objs = append(objs, tt.existingConfig)
			}

			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
			mapper := newDRAResourceMapper()

			err := mapper.loadFromConfig(context.Background(), cl)

			if tt.expectError && err == nil {
				t.Fatal("Expected error but got none")
			}

			if !tt.expectError && err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if tt.expectMapping != nil {
				for deviceClass, expectedResource := range tt.expectMapping {
					actualResource, found := mapper.lookup(deviceClass)
					if !found {
						t.Errorf("Expected device class %s to be mapped", deviceClass)
					}
					if actualResource != expectedResource {
						t.Errorf("Expected device class %s to map to %s, got %s",
							deviceClass, expectedResource, actualResource)
					}
				}
			}
		})
	}
}

func TestDRAResourceMapper_UpdateFromConfig(t *testing.T) {
	tests := []struct {
		name          string
		initialConfig *kueuealpha.DynamicResourceAllocationConfig
		updateConfig  *kueuealpha.DynamicResourceAllocationConfig
		finalLookups  []struct {
			deviceClass    corev1.ResourceName
			expectedRes    corev1.ResourceName
			expectedExists bool
		}
	}{
		{
			name: "update replaces old mapping",
			initialConfig: &kueuealpha.DynamicResourceAllocationConfig{
				ObjectMeta: metav1.ObjectMeta{Name: DefaultDRAConfigName},
				Spec: kueuealpha.DynamicResourceAllocationConfigSpec{
					Resources: []kueuealpha.DynamicResource{
						{
							Name:             kueuealpha.DriverResourceName("example.com/old-gpu"),
							DeviceClassNames: []kueuealpha.DriverResourceName{"example.com/old-class"},
						},
					},
				},
			},
			updateConfig: &kueuealpha.DynamicResourceAllocationConfig{
				ObjectMeta: metav1.ObjectMeta{Name: DefaultDRAConfigName},
				Spec: kueuealpha.DynamicResourceAllocationConfigSpec{
					Resources: []kueuealpha.DynamicResource{
						{
							Name:             kueuealpha.DriverResourceName("example.com/new-gpu"),
							DeviceClassNames: []kueuealpha.DriverResourceName{"example.com/new-class"},
						},
					},
				},
			},
			finalLookups: []struct {
				deviceClass    corev1.ResourceName
				expectedRes    corev1.ResourceName
				expectedExists bool
			}{
				{
					deviceClass:    "example.com/old-class",
					expectedRes:    "",
					expectedExists: false,
				},
				{
					deviceClass:    "example.com/new-class",
					expectedRes:    "example.com/new-gpu",
					expectedExists: true,
				},
			},
		},
		{
			name:          "update with nil config",
			initialConfig: nil,
			updateConfig:  nil,
			finalLookups: []struct {
				deviceClass    corev1.ResourceName
				expectedRes    corev1.ResourceName
				expectedExists bool
			}{
				{
					deviceClass:    "example.com/any",
					expectedRes:    "",
					expectedExists: false,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapper := newDRAResourceMapper()

			// Apply initial config if provided
			if tt.initialConfig != nil {
				err := mapper.updateFromConfig(context.Background(), tt.initialConfig)
				if err != nil {
					t.Fatalf("Initial updateFromConfig failed: %v", err)
				}
			}

			// Apply update config
			err := mapper.updateFromConfig(context.Background(), tt.updateConfig)
			if err != nil {
				t.Fatalf("Update updateFromConfig failed: %v", err)
			}

			// Verify final state
			for _, lookup := range tt.finalLookups {
				actualRes, actualExists := mapper.lookup(lookup.deviceClass)

				if actualExists != lookup.expectedExists {
					t.Errorf("lookup(%s): expected exists=%v, got exists=%v",
						lookup.deviceClass, lookup.expectedExists, actualExists)
				}

				if actualRes != lookup.expectedRes {
					t.Errorf("lookup(%s): expected resource=%s, got resource=%s",
						lookup.deviceClass, lookup.expectedRes, actualRes)
				}
			}
		})
	}
}
