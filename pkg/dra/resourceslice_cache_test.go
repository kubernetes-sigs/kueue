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

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func newSlice(name, driver, pool string) *resourcev1.ResourceSlice {
	return &resourcev1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: resourcev1.ResourceSliceSpec{
			Driver: driver,
			Pool: resourcev1.ResourcePool{
				Name:               pool,
				Generation:         1,
				ResourceSliceCount: 1,
			},
		},
	}
}

func TestResourceSliceCache_ListByDriver(t *testing.T) {
	gpuSlice1 := newSlice("gpu-slice-1", "gpu.nvidia.com", "node1")
	gpuSlice2 := newSlice("gpu-slice-2", "gpu.nvidia.com", "node2")
	nicSlice := newSlice("nic-slice", "nic.mellanox.com", "node1")

	cl := utiltesting.NewClientBuilder().
		WithIndex(&resourcev1.ResourceSlice{}, "spec.driver", func(obj client.Object) []string {
			return []string{obj.(*resourcev1.ResourceSlice).Spec.Driver}
		}).
		WithObjects(gpuSlice1, gpuSlice2, nicSlice).
		Build()

	ctx := t.Context()
	cache := NewResourceSliceCache(cl)

	t.Run("returns only requested driver slices", func(t *testing.T) {
		got, err := cache.ListByDriver(ctx, DriverReference("gpu.nvidia.com"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("expected 2 gpu slices, got %d", len(got))
		}
	})

	t.Run("second call returns cached result", func(t *testing.T) {
		got1, err := cache.ListByDriver(ctx, DriverReference("gpu.nvidia.com"))
		if err != nil {
			t.Fatalf("unexpected error on first call: %v", err)
		}
		got2, err := cache.ListByDriver(ctx, DriverReference("gpu.nvidia.com"))
		if err != nil {
			t.Fatalf("unexpected error on second call: %v", err)
		}
		if diff := cmp.Diff(got1, got2, cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion")); diff != "" {
			t.Errorf("cached result mismatch (-first +second):\n%s", diff)
		}
	})

	t.Run("different driver returns different slices", func(t *testing.T) {
		got, err := cache.ListByDriver(ctx, DriverReference("nic.mellanox.com"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Errorf("expected 1 nic slice, got %d", len(got))
		}
	})
}

func TestResourceSliceCache_ListAll(t *testing.T) {
	gpuSlice := newSlice("gpu-slice", "gpu.nvidia.com", "node1")
	nicSlice := newSlice("nic-slice", "nic.mellanox.com", "node1")

	cl := utiltesting.NewClientBuilder().
		WithObjects(gpuSlice, nicSlice).
		Build()

	ctx := t.Context()
	cache := NewResourceSliceCache(cl)

	t.Run("returns all slices", func(t *testing.T) {
		got, err := cache.ListAll(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("expected 2 slices, got %d", len(got))
		}
	})

	t.Run("second call returns cached result", func(t *testing.T) {
		got1, err := cache.ListAll(ctx)
		if err != nil {
			t.Fatalf("unexpected error on first call: %v", err)
		}
		got2, err := cache.ListAll(ctx)
		if err != nil {
			t.Fatalf("unexpected error on second call: %v", err)
		}
		if diff := cmp.Diff(got1, got2, cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion")); diff != "" {
			t.Errorf("cached result mismatch (-first +second):\n%s", diff)
		}
	})
}

func TestResourceSliceCache_ListAllThenListByDriver(t *testing.T) {
	gpuSlice1 := newSlice("gpu-slice-1", "gpu.nvidia.com", "node1")
	gpuSlice2 := newSlice("gpu-slice-2", "gpu.nvidia.com", "node2")
	nicSlice := newSlice("nic-slice", "nic.mellanox.com", "node1")

	listCallCount := 0
	cl := utiltesting.NewClientBuilder().
		WithIndex(&resourcev1.ResourceSlice{}, "spec.driver", func(obj client.Object) []string {
			return []string{obj.(*resourcev1.ResourceSlice).Spec.Driver}
		}).
		WithObjects(gpuSlice1, gpuSlice2, nicSlice).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				listCallCount++
				return cl.List(ctx, list, opts...)
			},
		}).
		Build()

	ctx := t.Context()
	cache := NewResourceSliceCache(cl)

	// First call: ListAll fetches everything
	allSlices, err := cache.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll failed: %v", err)
	}
	if len(allSlices) != 3 {
		t.Errorf("expected 3 slices from ListAll, got %d", len(allSlices))
	}
	if listCallCount != 1 {
		t.Errorf("expected 1 API call after ListAll, got %d", listCallCount)
	}

	// Second call: ListByDriver filters from cached all-slices, no extra API call
	gpuSlices, err := cache.ListByDriver(ctx, DriverReference("gpu.nvidia.com"))
	if err != nil {
		t.Fatalf("ListByDriver failed: %v", err)
	}
	if len(gpuSlices) != 2 {
		t.Errorf("expected 2 gpu slices from ListByDriver, got %d", len(gpuSlices))
	}
	if listCallCount != 1 {
		t.Errorf("expected still 1 API call after ListByDriver (should use cache), got %d", listCallCount)
	}
}

func TestResourceSliceCache_ListByDriverThenListByDriverDifferent(t *testing.T) {
	gpuSlice := newSlice("gpu-slice", "gpu.nvidia.com", "node1")
	nicSlice := newSlice("nic-slice", "nic.mellanox.com", "node1")

	listCallCount := 0
	cl := utiltesting.NewClientBuilder().
		WithIndex(&resourcev1.ResourceSlice{}, "spec.driver", func(obj client.Object) []string {
			return []string{obj.(*resourcev1.ResourceSlice).Spec.Driver}
		}).
		WithObjects(gpuSlice, nicSlice).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				listCallCount++
				return cl.List(ctx, list, opts...)
			},
		}).
		Build()

	ctx := t.Context()
	cache := NewResourceSliceCache(cl)

	if _, err := cache.ListByDriver(ctx, DriverReference("gpu.nvidia.com")); err != nil {
		t.Fatalf("unexpected error listing gpu driver: %v", err)
	}
	if listCallCount != 1 {
		t.Errorf("expected 1 API call, got %d", listCallCount)
	}

	if _, err := cache.ListByDriver(ctx, DriverReference("nic.mellanox.com")); err != nil {
		t.Fatalf("unexpected error listing nic driver: %v", err)
	}
	if listCallCount != 2 {
		t.Errorf("expected 2 API calls for different drivers, got %d", listCallCount)
	}

	// Same driver again — should be cached
	if _, err := cache.ListByDriver(ctx, DriverReference("gpu.nvidia.com")); err != nil {
		t.Fatalf("unexpected error listing cached gpu driver: %v", err)
	}
	if listCallCount != 2 {
		t.Errorf("expected still 2 API calls (gpu cached), got %d", listCallCount)
	}
}
