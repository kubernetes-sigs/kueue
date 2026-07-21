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

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
)

func TestNewDRAResourceMapper(t *testing.T) {
	mapper := NewResourceMapper()

	if mapper == nil {
		t.Fatal("NewResourceMapper() returned nil")
	}

	if mapper.deviceClassToResource == nil {
		t.Fatal("deviceClassToResource map not initialized")
	}

	// Test empty mapping lookup
	_, found := mapper.Lookup("nonexistent")
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
			mapper := NewResourceMapper()

			err := mapper.PopulateFromConfiguration(tt.config)
			if err != nil {
				t.Fatalf("populateFromConfiguration failed: %v", err)
			}

			for _, lookup := range tt.lookups {
				actualResource, actualExists := mapper.Lookup(lookup.deviceClass)

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
			mapper := NewResourceMapper()

			// Initial population
			if err := mapper.PopulateFromConfiguration(tt.initialConfig); err != nil {
				t.Fatalf("Initial populateFromConfiguration failed: %v", err)
			}

			// Update population
			if err := mapper.PopulateFromConfiguration(tt.updateConfig); err != nil {
				t.Fatalf("Update populateFromConfiguration failed: %v", err)
			}

			// Test final state
			for _, lookup := range tt.finalLookups {
				actualResource, actualExists := mapper.Lookup(lookup.deviceClass)

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
	type wantCounterConfig struct {
		quotaResource corev1.ResourceName
		counterName   string
		driver        DriverReference
	}
	tests := []struct {
		name                   string
		deviceClassMappings    []configapi.DeviceClassMapping
		wantDeviceLookup       map[corev1.ResourceName]corev1.ResourceName
		wantCounterConfigsList map[corev1.ResourceName][]wantCounterConfig
	}{
		{
			name:             "nil mappings",
			wantDeviceLookup: map[corev1.ResourceName]corev1.ResourceName{},
		},
		{
			name: "deviceClassMappings without counters",
			deviceClassMappings: []configapi.DeviceClassMapping{
				{Name: "foo", DeviceClassNames: []corev1.ResourceName{"foo.example.com"}},
			},
			wantDeviceLookup: map[corev1.ResourceName]corev1.ResourceName{
				"foo.example.com": "foo",
			},
		},
		{
			name: "deviceClassMappings with counter source",
			deviceClassMappings: []configapi.DeviceClassMapping{
				{
					Name:             "gpu.memory",
					DeviceClassNames: []corev1.ResourceName{"mig.nvidia.com"},
					Sources: []configapi.DeviceClassSourceConfig{
						{Counter: &configapi.DeviceClassCounterSource{Name: "memory", Driver: "gpu.nvidia.com"}},
					},
				},
			},
			wantDeviceLookup: map[corev1.ResourceName]corev1.ResourceName{
				"mig.nvidia.com": "gpu.memory",
			},
			wantCounterConfigsList: map[corev1.ResourceName][]wantCounterConfig{
				"mig.nvidia.com": {
					{quotaResource: "gpu.memory", counterName: "memory", driver: "gpu.nvidia.com"},
				},
			},
		},
		{
			name: "multi-counter tracking for same DeviceClass",
			deviceClassMappings: []configapi.DeviceClassMapping{
				{
					Name:             "gpu.memory",
					DeviceClassNames: []corev1.ResourceName{"mig.nvidia.com"},
					Sources: []configapi.DeviceClassSourceConfig{
						{Counter: &configapi.DeviceClassCounterSource{Name: "memory", Driver: "gpu.nvidia.com"}},
					},
				},
				{
					Name:             "gpu.compute",
					DeviceClassNames: []corev1.ResourceName{"mig.nvidia.com"},
					Sources: []configapi.DeviceClassSourceConfig{
						{Counter: &configapi.DeviceClassCounterSource{Name: "multiprocessors", Driver: "gpu.nvidia.com"}},
					},
				},
			},
			wantCounterConfigsList: map[corev1.ResourceName][]wantCounterConfig{
				"mig.nvidia.com": {
					{quotaResource: "gpu.memory", counterName: "memory", driver: "gpu.nvidia.com"},
					{quotaResource: "gpu.compute", counterName: "multiprocessors", driver: "gpu.nvidia.com"},
				},
			},
		},
		{
			name: "unified whole-GPU and MIG",
			deviceClassMappings: []configapi.DeviceClassMapping{
				{
					Name:             "gpu.memory",
					DeviceClassNames: []corev1.ResourceName{"gpu.nvidia.com", "mig.nvidia.com"},
					Sources: []configapi.DeviceClassSourceConfig{
						{Counter: &configapi.DeviceClassCounterSource{Name: "memory", Driver: "gpu.nvidia.com"}},
					},
				},
			},
			wantDeviceLookup: map[corev1.ResourceName]corev1.ResourceName{
				"gpu.nvidia.com": "gpu.memory",
				"mig.nvidia.com": "gpu.memory",
			},
			wantCounterConfigsList: map[corev1.ResourceName][]wantCounterConfig{
				"gpu.nvidia.com": {
					{quotaResource: "gpu.memory", counterName: "memory", driver: "gpu.nvidia.com"},
				},
				"mig.nvidia.com": {
					{quotaResource: "gpu.memory", counterName: "memory", driver: "gpu.nvidia.com"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapper := NewResourceMapper()
			err := mapper.PopulateFromConfiguration(tt.deviceClassMappings)
			if err != nil {
				t.Fatalf("PopulateFromConfiguration failed: %v", err)
			}
			for dc, wantResource := range tt.wantDeviceLookup {
				got, found := mapper.Lookup(dc)
				if !found {
					t.Errorf("Expected to find device class %s in mapper", dc)
				} else if got != wantResource {
					t.Errorf("Device class %s: want %s, got %s", dc, wantResource, got)
				}
			}
			if tt.wantCounterConfigsList == nil {
				for dc := range tt.wantDeviceLookup {
					if ccs := mapper.getCounterConfigs(dc); len(ccs) > 0 {
						t.Errorf("Expected no counter config for %s, got %+v", dc, ccs)
					}
				}
			}
			for dc, wantConfigs := range tt.wantCounterConfigsList {
				ccs := mapper.getCounterConfigs(dc)
				if len(ccs) != len(wantConfigs) {
					t.Fatalf("Counter configs for %s: want %d, got %d", dc, len(wantConfigs), len(ccs))
				}
				for i, want := range wantConfigs {
					if ccs[i].quotaResource != want.quotaResource {
						t.Errorf("Counter config[%d] for %s: want quotaResource %s, got %s", i, dc, want.quotaResource, ccs[i].quotaResource)
					}
					if ccs[i].counterName != want.counterName {
						t.Errorf("Counter config[%d] for %s: want counterName %s, got %s", i, dc, want.counterName, ccs[i].counterName)
					}
					if ccs[i].driver != want.driver {
						t.Errorf("Counter config[%d] for %s: want driver %s, got %s", i, dc, want.driver, ccs[i].driver)
					}
				}
			}
		})
	}
}

func TestResourceMapperCounterBasedResourceNames(t *testing.T) {
	mapper := NewResourceMapper()
	err := mapper.PopulateFromConfiguration([]configapi.DeviceClassMapping{
		{
			Name:             corev1.ResourceName("gpu.memory"),
			DeviceClassNames: []corev1.ResourceName{"gpu-a.example.com"},
			Sources: []configapi.DeviceClassSourceConfig{
				{Counter: &configapi.DeviceClassCounterSource{Driver: "gpu.example.com", Name: "memory"}},
			},
		},
		{
			Name:             corev1.ResourceName("gpu.memory"),
			DeviceClassNames: []corev1.ResourceName{"gpu-b.example.com"},
			Sources: []configapi.DeviceClassSourceConfig{
				{Counter: &configapi.DeviceClassCounterSource{Driver: "gpu.example.com", Name: "memory"}},
			},
		},
		{
			Name:             corev1.ResourceName("gpu.compute"),
			DeviceClassNames: []corev1.ResourceName{"gpu-c.example.com"},
			Sources: []configapi.DeviceClassSourceConfig{
				{Counter: &configapi.DeviceClassCounterSource{Driver: "gpu.example.com", Name: "compute"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("PopulateFromConfiguration failed: %v", err)
	}

	want := []corev1.ResourceName{"gpu.compute", "gpu.memory"}
	if diff := cmp.Diff(want, mapper.CounterBasedResourceNames()); diff != "" {
		t.Errorf("CounterBasedResourceNames() mismatch (-want,+got):\n%s", diff)
	}
}
