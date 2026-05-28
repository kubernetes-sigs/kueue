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
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	dracel "k8s.io/dynamic-resource-allocation/cel"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func makeDevice(name string, profile string, memoryValue string) resourcev1.Device {
	dev := resourcev1.Device{
		Name: name,
		Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
			"gpu.nvidia.com/profile": {StringValue: &profile},
		},
	}
	if memoryValue != "" {
		dev.ConsumesCounters = []resourcev1.DeviceCounterConsumption{
			{
				CounterSet: "gpu-counter-set",
				Counters: map[string]resourcev1.Counter{
					"memory": {Value: resource.MustParse(memoryValue)},
				},
			},
		}
	}
	return dev
}

func makeDeviceWithMultipleCounters(name string, profile string, memory string, multiprocessors string) resourcev1.Device {
	dev := makeDevice(name, profile, memory)
	dev.ConsumesCounters[0].Counters["multiprocessors"] = resourcev1.Counter{Value: resource.MustParse(multiprocessors)}
	return dev
}

func makeResourceSlice(name, driver, poolName string, gen int64, sliceCount int64, devices []resourcev1.Device) resourcev1.ResourceSlice {
	return resourcev1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: resourcev1.ResourceSliceSpec{
			Driver: driver,
			Pool: resourcev1.ResourcePool{
				Name:               poolName,
				Generation:         gen,
				ResourceSliceCount: sliceCount,
			},
			Devices: devices,
		},
	}
}

func TestComputeCounterCharges(t *testing.T) {
	defaultCC := &deviceClassCounterConfig{
		driver:      "gpu.nvidia.com",
		counterName: "memory",
	}

	tests := []struct {
		name          string
		cc            *deviceClassCounterConfig
		quotaResource corev1.ResourceName
		matched       []resourcev1.Device
		count         int64
		want          corev1.ResourceList
	}{
		{
			name:          "single device, count=1",
			cc:            defaultCC,
			quotaResource: "gpu.memory",
			matched: []resourcev1.Device{
				makeDevice("mig-1g10gb-0", "1g.10gb", "9856Mi"),
			},
			count: 1,
			want: corev1.ResourceList{
				"gpu.memory": resource.MustParse("9856Mi"),
			},
		},
		{
			name:          "single device, count=3",
			cc:            defaultCC,
			quotaResource: "gpu.memory",
			matched: []resourcev1.Device{
				makeDevice("mig-1g10gb-0", "1g.10gb", "9856Mi"),
			},
			count: 3,
			want: corev1.ResourceList{
				"gpu.memory": *resource.NewQuantity(9856*1024*1024*3, resource.BinarySI),
			},
		},
		{
			name:          "multiple devices same value, MAX is same",
			cc:            defaultCC,
			quotaResource: "gpu.memory",
			matched: []resourcev1.Device{
				makeDevice("mig-1g10gb-0", "1g.10gb", "9856Mi"),
				makeDevice("mig-1g10gb-1", "1g.10gb", "9856Mi"),
				makeDevice("mig-1g10gb-2", "1g.10gb", "9856Mi"),
			},
			count: 1,
			want: corev1.ResourceList{
				"gpu.memory": resource.MustParse("9856Mi"),
			},
		},
		{
			name:          "multiple devices different values, MAX wins",
			cc:            defaultCC,
			quotaResource: "gpu.memory",
			matched: []resourcev1.Device{
				makeDevice("mig-1g10gb-0", "1g.10gb", "9856Mi"),
				makeDevice("mig-3g40gb-0", "3g.40gb", "40Gi"),
			},
			count: 1,
			want: corev1.ResourceList{
				"gpu.memory": resource.MustParse("40Gi"),
			},
		},
		{
			name:          "counter not in config, skipped",
			cc:            defaultCC,
			quotaResource: "gpu.memory",
			matched: []resourcev1.Device{
				makeDeviceWithMultipleCounters("mig-1g10gb-0", "1g.10gb", "9856Mi", "14"),
			},
			count: 1,
			want: corev1.ResourceList{
				"gpu.memory": resource.MustParse("9856Mi"),
			},
		},
		{
			name:          "no consumesCounters, no deviceSelector",
			cc:            defaultCC,
			quotaResource: "gpu.memory",
			matched: []resourcev1.Device{
				makeDevice("whole-gpu-0", "", ""),
			},
			count: 1,
			want:  nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := computeCounterCharges(tc.cc, tc.quotaResource, tc.matched, tc.count)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("computeCounterCharges() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestGroupSlicesByPool(t *testing.T) {
	tests := []struct {
		name           string
		slices         []resourcev1.ResourceSlice
		driver         string
		wantPools      int
		wantComplete   map[string]bool
		wantGeneration map[string]int64
	}{
		{
			name: "single pool, complete",
			slices: []resourcev1.ResourceSlice{
				makeResourceSlice("slice1", "gpu.nvidia.com", "node1-gpu0", 1, 2, nil),
				makeResourceSlice("slice2", "gpu.nvidia.com", "node1-gpu0", 1, 2, nil),
			},
			driver:         "gpu.nvidia.com",
			wantPools:      1,
			wantComplete:   map[string]bool{"node1-gpu0": true},
			wantGeneration: map[string]int64{"node1-gpu0": 1},
		},
		{
			name: "single pool, incomplete",
			slices: []resourcev1.ResourceSlice{
				makeResourceSlice("slice1", "gpu.nvidia.com", "node1-gpu0", 1, 2, nil),
			},
			driver:         "gpu.nvidia.com",
			wantPools:      1,
			wantComplete:   map[string]bool{"node1-gpu0": false},
			wantGeneration: map[string]int64{"node1-gpu0": 1},
		},
		{
			name: "two pools",
			slices: []resourcev1.ResourceSlice{
				makeResourceSlice("slice1", "gpu.nvidia.com", "node1-gpu0", 1, 2, nil),
				makeResourceSlice("slice2", "gpu.nvidia.com", "node1-gpu0", 1, 2, nil),
				makeResourceSlice("slice3", "gpu.nvidia.com", "node1-gpu1", 1, 2, nil),
				makeResourceSlice("slice4", "gpu.nvidia.com", "node1-gpu1", 1, 2, nil),
			},
			driver:    "gpu.nvidia.com",
			wantPools: 2,
			wantComplete: map[string]bool{
				"node1-gpu0": true,
				"node1-gpu1": true,
			},
			wantGeneration: map[string]int64{
				"node1-gpu0": 1,
				"node1-gpu1": 1,
			},
		},
		{
			name: "filter by driver",
			slices: []resourcev1.ResourceSlice{
				makeResourceSlice("slice1", "gpu.nvidia.com", "node1-gpu0", 1, 1, nil),
				makeResourceSlice("slice2", "net.example.com", "node1-net0", 1, 1, nil),
			},
			driver:         "gpu.nvidia.com",
			wantPools:      1,
			wantComplete:   map[string]bool{"node1-gpu0": true},
			wantGeneration: map[string]int64{"node1-gpu0": 1},
		},
		{
			name: "higher generation replaces older",
			slices: []resourcev1.ResourceSlice{
				makeResourceSlice("slice1-old", "gpu.nvidia.com", "node1-gpu0", 1, 2, nil),
				makeResourceSlice("slice2-old", "gpu.nvidia.com", "node1-gpu0", 1, 2, nil),
				makeResourceSlice("slice1-new", "gpu.nvidia.com", "node1-gpu0", 2, 2, nil),
				makeResourceSlice("slice2-new", "gpu.nvidia.com", "node1-gpu0", 2, 2, nil),
			},
			driver:         "gpu.nvidia.com",
			wantPools:      1,
			wantComplete:   map[string]bool{"node1-gpu0": true},
			wantGeneration: map[string]int64{"node1-gpu0": 2},
		},
		{
			name: "mixed generation, incomplete new",
			slices: []resourcev1.ResourceSlice{
				makeResourceSlice("slice1-old", "gpu.nvidia.com", "node1-gpu0", 1, 2, nil),
				makeResourceSlice("slice2-old", "gpu.nvidia.com", "node1-gpu0", 1, 2, nil),
				makeResourceSlice("slice1-new", "gpu.nvidia.com", "node1-gpu0", 2, 2, nil),
			},
			driver:         "gpu.nvidia.com",
			wantPools:      1,
			wantComplete:   map[string]bool{"node1-gpu0": false},
			wantGeneration: map[string]int64{"node1-gpu0": 2},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pools := groupSlicesByPool(tc.slices, tc.driver)
			if len(pools) != tc.wantPools {
				t.Errorf("got %d pools, want %d", len(pools), tc.wantPools)
			}
			for name, wantComplete := range tc.wantComplete {
				pool, ok := pools[name]
				if !ok {
					t.Errorf("pool %s not found", name)
					continue
				}
				if pool.isComplete() != wantComplete {
					t.Errorf("pool %s: isComplete()=%v, want %v", name, pool.isComplete(), wantComplete)
				}
			}
			for name, wantGen := range tc.wantGeneration {
				pool, ok := pools[name]
				if !ok {
					continue
				}
				if pool.generation != wantGen {
					t.Errorf("pool %s: generation=%d, want %d", name, pool.generation, wantGen)
				}
			}
		})
	}
}

func TestMatchDevicesWithSelectors_CELErrorPropagation(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)

	pools := map[string]*poolInfo{
		"pool1": {
			name:               "pool1",
			generation:         1,
			resourceSliceCount: 1,
			slices: []resourcev1.ResourceSlice{
				{
					Spec: resourcev1.ResourceSliceSpec{
						Driver: "gpu.example.com",
						Pool:   resourcev1.ResourcePool{Name: "pool1", Generation: 1, ResourceSliceCount: 1},
						Devices: []resourcev1.Device{
							{
								Name: "gpu-0",
								Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
									"gpu.example.com/type": {},
								},
							},
						},
					},
				},
			},
		},
	}

	cache := dracel.NewCache(1, dracel.Features{})
	result := cache.GetOrCompile("device.driver == 'gpu.example.com'")
	if result.Error != nil {
		t.Fatalf("CEL compilation failed: %v", result.Error)
	}

	reqPath := field.NewPath("test")
	matched, errs := matchDevicesWithSelectors(ctx, pools, "gpu.example.com",
		[]dracel.CompilationResult{result}, nil, nil, reqPath)

	if len(errs) == 0 {
		t.Fatalf("Expected CEL evaluation error to be propagated, got %d matched devices", len(matched))
	}
	if !strings.Contains(errs[0].Detail, "unsupported attribute value") {
		t.Errorf("Expected error containing 'unsupported attribute value', got: %s", errs[0].Detail)
	}
}
