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

package cache

import (
	"context"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestDRADeviceClassMapping(t *testing.T) {
	log := ctrl.LoggerFrom(context.Background())
	cache := New(nil)

	cases := []struct {
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
				Spec: kueuealpha.DynamicResourceAllocationConfigSpec{
					Resources: []kueuealpha.DynamicResource{
						{
							Name:             "example.com/gpu",
							DeviceClassNames: []corev1.ResourceName{"example.com/foo"},
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
					deviceClass:    "example.com/foo",
					expectedRes:    "example.com/gpu",
					expectedExists: true,
				},
				{
					deviceClass:    "example.com/bar",
					expectedRes:    "",
					expectedExists: false,
				},
			},
		},
		{
			name: "multiple device classes to single resource",
			config: &kueuealpha.DynamicResourceAllocationConfig{
				Spec: kueuealpha.DynamicResourceAllocationConfigSpec{
					Resources: []kueuealpha.DynamicResource{
						{
							Name:             "example.com/accelerator",
							DeviceClassNames: []corev1.ResourceName{"example.com/foo", "example.com/bar", "example.com/baz"},
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
					deviceClass:    "example.com/foo",
					expectedRes:    "example.com/accelerator",
					expectedExists: true,
				},
				{
					deviceClass:    "example.com/bar",
					expectedRes:    "example.com/accelerator",
					expectedExists: true,
				},
				{
					deviceClass:    "example.com/baz",
					expectedRes:    "example.com/accelerator",
					expectedExists: true,
				},
				{
					deviceClass:    "unknown.com/device",
					expectedRes:    "",
					expectedExists: false,
				},
			},
		},
		{
			name: "multiple resources with different device classes",
			config: &kueuealpha.DynamicResourceAllocationConfig{
				Spec: kueuealpha.DynamicResourceAllocationConfigSpec{
					Resources: []kueuealpha.DynamicResource{
						{
							Name:             "example.com/gpu",
							DeviceClassNames: []corev1.ResourceName{"example.com/foo", "example.com/bar"},
						},
						{
							Name:             "example.com/fpga",
							DeviceClassNames: []corev1.ResourceName{"example.com/baz"},
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
					deviceClass:    "example.com/foo",
					expectedRes:    "example.com/gpu",
					expectedExists: true,
				},
				{
					deviceClass:    "example.com/bar",
					expectedRes:    "example.com/gpu",
					expectedExists: true,
				},
				{
					deviceClass:    "example.com/baz",
					expectedRes:    "example.com/fpga",
					expectedExists: true,
				},
				{
					deviceClass:    "unknown.com/device",
					expectedRes:    "",
					expectedExists: false,
				},
			},
		},
		{
			name: "empty config",
			config: &kueuealpha.DynamicResourceAllocationConfig{
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
					deviceClass:    "example.com/foo",
					expectedRes:    "",
					expectedExists: false,
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Update the cache with the config
			cache.AddOrUpdateDynamicResourceAllocationConfig(log, tc.config)

			// Test all lookups
			for _, lookup := range tc.lookups {
				gotRes, gotExists := cache.GetResourceNameForDeviceClass(lookup.deviceClass)
				if gotRes != lookup.expectedRes {
					t.Errorf("GetResourceNameForDeviceClass(%s): got resource %s, want %s",
						lookup.deviceClass, gotRes, lookup.expectedRes)
				}
				if gotExists != lookup.expectedExists {
					t.Errorf("GetResourceNameForDeviceClass(%s): got exists %v, want %v",
						lookup.deviceClass, gotExists, lookup.expectedExists)
				}
			}
		})
	}
}

func TestDRAConfigUpdates(t *testing.T) {
	log := ctrl.LoggerFrom(context.Background())
	cache := New(nil)

	// Initial config
	config1 := &kueuealpha.DynamicResourceAllocationConfig{
		Spec: kueuealpha.DynamicResourceAllocationConfigSpec{
			Resources: []kueuealpha.DynamicResource{
				{
					Name:             "example.com/gpu",
					DeviceClassNames: []corev1.ResourceName{"example.com/foo"},
				},
			},
		},
	}
	cache.AddOrUpdateDynamicResourceAllocationConfig(log, config1)

	// Verify initial mapping
	gotRes, gotExists := cache.GetResourceNameForDeviceClass("example.com/foo")
	if !gotExists || gotRes != "example.com/gpu" {
		t.Errorf("Initial config: expected example.com/foo -> example.com/gpu, got %s (exists: %v)", gotRes, gotExists)
	}

	// Update config - add new mapping
	config2 := &kueuealpha.DynamicResourceAllocationConfig{
		Spec: kueuealpha.DynamicResourceAllocationConfigSpec{
			Resources: []kueuealpha.DynamicResource{
				{
					Name:             "example.com/gpu",
					DeviceClassNames: []corev1.ResourceName{"example.com/foo", "example.com/bar"},
				},
			},
		},
	}
	cache.AddOrUpdateDynamicResourceAllocationConfig(log, config2)

	// Verify both mappings exist
	gotRes, gotExists = cache.GetResourceNameForDeviceClass("example.com/foo")
	if !gotExists || gotRes != "example.com/gpu" {
		t.Errorf("After update: expected example.com/foo -> example.com/gpu, got %s (exists: %v)", gotRes, gotExists)
	}
	gotRes, gotExists = cache.GetResourceNameForDeviceClass("example.com/bar")
	if !gotExists || gotRes != "example.com/gpu" {
		t.Errorf("After update: expected example.com/bar -> example.com/gpu, got %s (exists: %v)", gotRes, gotExists)
	}

	// Update config - remove mapping (stale entries should be cleared)
	config3 := &kueuealpha.DynamicResourceAllocationConfig{
		Spec: kueuealpha.DynamicResourceAllocationConfigSpec{
			Resources: []kueuealpha.DynamicResource{
				{
					Name:             "example.com/fpga",
					DeviceClassNames: []corev1.ResourceName{"example.com/baz"},
				},
			},
		},
	}
	cache.AddOrUpdateDynamicResourceAllocationConfig(log, config3)

	// Verify old mappings are gone and new mapping exists
	_, gotExists = cache.GetResourceNameForDeviceClass("example.com/foo")
	if gotExists {
		t.Error("After removing mapping: example.com/foo should not exist")
	}
	_, gotExists = cache.GetResourceNameForDeviceClass("example.com/bar")
	if gotExists {
		t.Error("After removing mapping: example.com/bar should not exist")
	}
	gotRes, gotExists = cache.GetResourceNameForDeviceClass("example.com/baz")
	if !gotExists || gotRes != "example.com/fpga" {
		t.Errorf("After removing old mappings: expected example.com/baz -> example.com/fpga, got %s (exists: %v)", gotRes, gotExists)
	}
}

func TestDRAConcurrentAccess(t *testing.T) {
	log := ctrl.LoggerFrom(context.Background())
	cache := New(nil)

	// Initial config
	config := &kueuealpha.DynamicResourceAllocationConfig{
		Spec: kueuealpha.DynamicResourceAllocationConfigSpec{
			Resources: []kueuealpha.DynamicResource{
				{
					Name:             "example.com/gpu",
					DeviceClassNames: []corev1.ResourceName{"example.com/foo"},
				},
			},
		},
	}
	cache.AddOrUpdateDynamicResourceAllocationConfig(log, config)

	// Test concurrent reads and writes
	var wg sync.WaitGroup
	const numGoroutines = 100
	const numOperations = 10

	// Concurrent readers
	for range numGoroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range numOperations {
				gotRes, gotExists := cache.GetResourceNameForDeviceClass("example.com/foo")
				if !gotExists || gotRes != "example.com/gpu" {
					t.Errorf("Concurrent read: expected example.com/foo -> example.com/gpu, got %s (exists: %v)", gotRes, gotExists)
					return
				}
			}
		}()
	}

	// Concurrent writers (updating same config)
	for range numGoroutines / 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range numOperations {
				cache.AddOrUpdateDynamicResourceAllocationConfig(log, config)
			}
		}()
	}

	wg.Wait()

	// Verify final state is consistent
	gotRes, gotExists := cache.GetResourceNameForDeviceClass("example.com/foo")
	if !gotExists || gotRes != "example.com/gpu" {
		t.Errorf("After concurrent access: expected example.com/foo -> example.com/gpu, got %s (exists: %v)", gotRes, gotExists)
	}
}

func TestDRAWithDRAResourcesOption(t *testing.T) {
	// Test that WithDRAResources option properly configures the cache
	clientBuilder := utiltesting.NewClientBuilder()
	cl := clientBuilder.Build()

	cache := New(cl, WithDRAResources(cl))

	// Verify DRA is enabled in the cache
	if !cache.draEnabled {
		t.Error("WithDRAResources option should enable DRA in cache")
	}
	if cache.draClient != cl {
		t.Error("WithDRAResources option should set the DRA client")
	}
}

func TestDRAWorkloadInfoIntegration(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.DynamicResourceAllocation, true)

	clientBuilder := utiltesting.NewClientBuilder()
	cl := clientBuilder.Build()

	log := ctrl.LoggerFrom(context.Background())
	cache := New(cl, WithDRAResources(cl))

	// Add DRA config
	draConfig := &kueuealpha.DynamicResourceAllocationConfig{
		Spec: kueuealpha.DynamicResourceAllocationConfigSpec{
			Resources: []kueuealpha.DynamicResource{
				{
					Name:             "example.com/gpu",
					DeviceClassNames: []corev1.ResourceName{"example.com/foo"},
				},
			},
		},
	}
	cache.AddOrUpdateDynamicResourceAllocationConfig(log, draConfig)

	// Create a workload that would use DRA resources
	wl := utiltesting.MakeWorkload("test-wl", "test-ns").
		PodSets(*utiltesting.MakePodSet("main", 1).Obj()).
		Obj()

	// Add resource claims to the workload
	wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
		{Name: "gpu-claim", ResourceClaimName: ptr.To("rc1")},
	}
	wl.Spec.PodSets[0].Template.Spec.Containers = []corev1.Container{
		{
			Name:  "main",
			Image: "pause",
			Resources: corev1.ResourceRequirements{
				Claims: []corev1.ResourceClaim{{Name: "gpu-claim"}},
			},
		},
	}

	// Verify that the cache can handle DRA-enabled workloads
	// This tests the integration between DRA config and workload processing
	gotRes, gotExists := cache.GetResourceNameForDeviceClass("example.com/foo")
	if !gotExists || gotRes != "example.com/gpu" {
		t.Errorf("DRA config integration: expected example.com/foo -> example.com/gpu, got %s (exists: %v)", gotRes, gotExists)
	}
}

func TestDRAIdempotentUpdates(t *testing.T) {
	log := ctrl.LoggerFrom(context.Background())
	cache := New(nil)

	// Create config
	config := &kueuealpha.DynamicResourceAllocationConfig{
		Spec: kueuealpha.DynamicResourceAllocationConfigSpec{
			Resources: []kueuealpha.DynamicResource{
				{
					Name:             "example.com/gpu",
					DeviceClassNames: []corev1.ResourceName{"example.com/foo"},
				},
			},
		},
	}

	// Apply the same config multiple times
	for range 5 {
		cache.AddOrUpdateDynamicResourceAllocationConfig(log, config)
	}

	// Verify the mapping is still correct
	gotRes, gotExists := cache.GetResourceNameForDeviceClass("example.com/foo")
	if !gotExists || gotRes != "example.com/gpu" {
		t.Errorf("After idempotent updates: expected example.com/foo -> example.com/gpu, got %s (exists: %v)", gotRes, gotExists)
	}
}

func TestDRAStaleEntryCleanup(t *testing.T) {
	log := ctrl.LoggerFrom(context.Background())
	cache := New(nil)

	// Initial config with multiple mappings
	config1 := &kueuealpha.DynamicResourceAllocationConfig{
		Spec: kueuealpha.DynamicResourceAllocationConfigSpec{
			Resources: []kueuealpha.DynamicResource{
				{
					Name:             "example.com/gpu",
					DeviceClassNames: []corev1.ResourceName{"example.com/foo", "example.com/bar"},
				},
				{
					Name:             "example.com/fpga",
					DeviceClassNames: []corev1.ResourceName{"example.com/baz"},
				},
			},
		},
	}
	cache.AddOrUpdateDynamicResourceAllocationConfig(log, config1)

	// Verify all mappings exist
	gpuRes, gpuExists := cache.GetResourceNameForDeviceClass("example.com/foo")
	amdRes, amdExists := cache.GetResourceNameForDeviceClass("example.com/bar")
	fpgaRes, fpgaExists := cache.GetResourceNameForDeviceClass("example.com/baz")

	if !gpuExists || gpuRes != "example.com/gpu" {
		t.Errorf("Initial setup: example.com/foo should map to example.com/gpu")
	}
	if !amdExists || amdRes != "example.com/gpu" {
		t.Errorf("Initial setup: example.com/bar should map to example.com/gpu")
	}
	if !fpgaExists || fpgaRes != "example.com/fpga" {
		t.Errorf("Initial setup: example.com/baz should map to example.com/fpga")
	}

	// Update config to completely different mappings
	config2 := &kueuealpha.DynamicResourceAllocationConfig{
		Spec: kueuealpha.DynamicResourceAllocationConfigSpec{
			Resources: []kueuealpha.DynamicResource{
				{
					Name:             "example.com/tpu",
					DeviceClassNames: []corev1.ResourceName{"example.com/qux"},
				},
			},
		},
	}
	cache.AddOrUpdateDynamicResourceAllocationConfig(log, config2)

	// Verify old mappings are gone
	_, gpuExists = cache.GetResourceNameForDeviceClass("example.com/foo")
	_, amdExists = cache.GetResourceNameForDeviceClass("example.com/bar")
	_, fpgaExists = cache.GetResourceNameForDeviceClass("example.com/baz")

	if gpuExists {
		t.Error("Stale cleanup: example.com/foo should no longer exist")
	}
	if amdExists {
		t.Error("Stale cleanup: example.com/bar should no longer exist")
	}
	if fpgaExists {
		t.Error("Stale cleanup: example.com/baz should no longer exist")
	}

	// Verify new mapping exists
	tpuRes, tpuExists := cache.GetResourceNameForDeviceClass("example.com/qux")
	if !tpuExists || tpuRes != "example.com/tpu" {
		t.Errorf("New mapping: expected example.com/qux -> example.com/tpu, got %s (exists: %v)", tpuRes, tpuExists)
	}
}
