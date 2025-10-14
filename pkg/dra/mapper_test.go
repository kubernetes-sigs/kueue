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
	"testing"

	corev1 "k8s.io/api/core/v1"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
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
		config  []configapi.DeviceClassMapping
		lookups []struct {
			deviceClass    corev1.ResourceName
			expectedRes    corev1.ResourceName
			expectedExists bool
		}
	}{
		{
			name: "single device class mapping",
			config: []configapi.DeviceClassMapping{
				{
					Name:             corev1.ResourceName("foo"),
					DeviceClassNames: []corev1.ResourceName{"foo.example.com"},
				},
			},
			lookups: []struct {
				deviceClass    corev1.ResourceName
				expectedRes    corev1.ResourceName
				expectedExists bool
			}{
				{
					deviceClass:    "foo.example.com",
					expectedRes:    "foo",
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
			config: []configapi.DeviceClassMapping{
				{
					Name: corev1.ResourceName("accelerator"),
					DeviceClassNames: []corev1.ResourceName{
						"foo.example.com",
						"bar.example.com",
					},
				},
			},
			lookups: []struct {
				deviceClass    corev1.ResourceName
				expectedRes    corev1.ResourceName
				expectedExists bool
			}{
				{
					deviceClass:    "foo.example.com",
					expectedRes:    "accelerator",
					expectedExists: true,
				},
				{
					deviceClass:    "bar.example.com",
					expectedRes:    "accelerator",
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
			name: "multiple resources",
			config: []configapi.DeviceClassMapping{
				{
					Name:             corev1.ResourceName("foo"),
					DeviceClassNames: []corev1.ResourceName{"foo.example.com"},
				},
				{
					Name:             corev1.ResourceName("bar"),
					DeviceClassNames: []corev1.ResourceName{"bar.example.com"},
				},
			},
			lookups: []struct {
				deviceClass    corev1.ResourceName
				expectedRes    corev1.ResourceName
				expectedExists bool
			}{
				{
					deviceClass:    "foo.example.com",
					expectedRes:    "foo",
					expectedExists: true,
				},
				{
					deviceClass:    "bar.example.com",
					expectedRes:    "bar",
					expectedExists: true,
				},
			},
		},
		{
			name:   "empty configuration",
			config: []configapi.DeviceClassMapping{},
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

			err := mapper.populateFromConfiguration(tt.config)
			if err != nil {
				t.Fatalf("populateFromConfiguration failed: %v", err)
			}

			for _, lookup := range tt.lookups {
				actualResource, actualExists := mapper.lookup(lookup.deviceClass)

				if actualExists != lookup.expectedExists {
					t.Errorf("lookup(%s) exists = %v, want %v", lookup.deviceClass, actualExists, lookup.expectedExists)
				}

				if actualResource != lookup.expectedRes {
					t.Errorf("lookup(%s) resource = %v, want %v", lookup.deviceClass, actualResource, lookup.expectedRes)
				}
			}
		})
	}
}

func TestDRAResourceMapper_PopulateFromConfiguration(t *testing.T) {
	tests := []struct {
		name          string
		initialConfig []configapi.DeviceClassMapping
		updateConfig  []configapi.DeviceClassMapping
		finalLookups  []struct {
			deviceClass    corev1.ResourceName
			expectedRes    corev1.ResourceName
			expectedExists bool
		}
	}{
		{
			name: "populate replaces old mapping",
			initialConfig: []configapi.DeviceClassMapping{
				{
					Name:             corev1.ResourceName("old-foo"),
					DeviceClassNames: []corev1.ResourceName{"old-foo.example.com"},
				},
			},
			updateConfig: []configapi.DeviceClassMapping{
				{
					Name:             corev1.ResourceName("new-foo"),
					DeviceClassNames: []corev1.ResourceName{"new-foo.example.com"},
				},
			},
			finalLookups: []struct {
				deviceClass    corev1.ResourceName
				expectedRes    corev1.ResourceName
				expectedExists bool
			}{
				{
					deviceClass:    "old-foo.example.com",
					expectedRes:    "",
					expectedExists: false,
				},
				{
					deviceClass:    "new-foo.example.com",
					expectedRes:    "new-foo",
					expectedExists: true,
				},
			},
		},
		{
			name:          "populate with nil config",
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

			// Initial population
			if err := mapper.populateFromConfiguration(tt.initialConfig); err != nil {
				t.Fatalf("Initial populateFromConfiguration failed: %v", err)
			}

			// Update population
			if err := mapper.populateFromConfiguration(tt.updateConfig); err != nil {
				t.Fatalf("Update populateFromConfiguration failed: %v", err)
			}

			// Test final state
			for _, lookup := range tt.finalLookups {
				actualResource, actualExists := mapper.lookup(lookup.deviceClass)

				if actualExists != lookup.expectedExists {
					t.Errorf("lookup(%s) exists = %v, want %v", lookup.deviceClass, actualExists, lookup.expectedExists)
				}

				if actualResource != lookup.expectedRes {
					t.Errorf("lookup(%s) resource = %v, want %v", lookup.deviceClass, actualResource, lookup.expectedRes)
				}
			}
		})
	}
}

func TestCreateMapperFromConfiguration(t *testing.T) {
	config := []configapi.DeviceClassMapping{
		{
			Name:             corev1.ResourceName("foo"),
			DeviceClassNames: []corev1.ResourceName{"foo.example.com"},
		},
	}

	err := CreateMapperFromConfiguration(config)
	if err != nil {
		t.Fatalf("CreateMapperFromConfiguration failed: %v", err)
	}

	// Test that the global mapper was populated
	resource, found := Mapper().lookup("foo.example.com")
	if !found {
		t.Error("Expected to find device class in global mapper")
	}
	if resource != "foo" {
		t.Errorf("Expected resource 'foo', got '%s'", resource)
	}
}
