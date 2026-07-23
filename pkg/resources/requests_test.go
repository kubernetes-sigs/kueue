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

package resources

import (
	"encoding/json"
	"maps"
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestCountIn(t *testing.T) {
	cases := map[string]struct {
		requests   MapRequests
		capacity   MapRequests
		wantResult int32
	}{
		"requests equal capacity": {
			requests: MapRequests{
				corev1.ResourceCPU:    1,
				corev1.ResourceMemory: 1,
			},
			capacity: MapRequests{
				corev1.ResourceCPU:    1,
				corev1.ResourceMemory: 1,
			},
			wantResult: 1,
		},
		"requests with extra resource": {
			requests: MapRequests{
				corev1.ResourceCPU:    1,
				corev1.ResourceMemory: 1,
			},
			capacity: MapRequests{
				corev1.ResourceCPU: 1,
			},
			wantResult: 0,
		},
		"first resource is bottleneck": {
			requests: MapRequests{
				corev1.ResourceCPU:    5,
				corev1.ResourceMemory: 1,
			},
			capacity: MapRequests{
				corev1.ResourceCPU:    12,
				corev1.ResourceMemory: 8,
			},
			wantResult: 2,
		},
		"second resource is bottleneck": {
			requests: MapRequests{
				corev1.ResourceCPU:    1,
				corev1.ResourceMemory: 5,
			},
			capacity: MapRequests{
				corev1.ResourceCPU:    8,
				corev1.ResourceMemory: 12,
			},
			wantResult: 2,
		},
		"capacity non divisible cleanly by requests": {
			requests: MapRequests{
				corev1.ResourceCPU: 2,
			},
			capacity: MapRequests{
				corev1.ResourceCPU: 5,
			},
			wantResult: 2,
		},
		"requests amount of zero": {
			requests: MapRequests{
				corev1.ResourceCPU: 0,
			},
			capacity: MapRequests{
				corev1.ResourceCPU: 5,
			},
			wantResult: int32(math.MaxInt32),
		},
		"has one resource with request amount of zero": {
			requests: MapRequests{
				corev1.ResourceCPU:    0,
				corev1.ResourceMemory: 1,
			},
			capacity: MapRequests{
				corev1.ResourceCPU:    5,
				corev1.ResourceMemory: 5,
			},
			wantResult: 5,
		},
		"requests amount of zero for extra resource": {
			requests: MapRequests{
				corev1.ResourceCPU:    1,
				corev1.ResourceMemory: 0,
			},
			capacity: MapRequests{
				corev1.ResourceCPU: 5,
			},
			wantResult: 5,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotResult := tc.requests.CountIn(tc.capacity)
			if tc.wantResult != gotResult {
				t.Errorf("unexpected result, want=%d, got=%d", tc.wantResult, gotResult)
			}
		})
	}
}

func TestCountInWithLimitingResource(t *testing.T) {
	cases := map[string]struct {
		requests             MapRequests
		capacity             MapRequests
		wantCount            int32
		wantLimitingResource corev1.ResourceName
	}{
		"CPU is limiting": {
			requests: MapRequests{
				corev1.ResourceCPU:    1000,
				corev1.ResourceMemory: 8 * 1024 * 1024 * 1024,
			},
			capacity: MapRequests{
				corev1.ResourceCPU:    500,
				corev1.ResourceMemory: 32 * 1024 * 1024 * 1024,
			},
			wantCount:            0,
			wantLimitingResource: corev1.ResourceCPU,
		},
		"memory is limiting": {
			requests: MapRequests{
				corev1.ResourceCPU:    1000,
				corev1.ResourceMemory: 16 * 1024 * 1024 * 1024,
			},
			capacity: MapRequests{
				corev1.ResourceCPU:    8000,
				corev1.ResourceMemory: 8 * 1024 * 1024 * 1024,
			},
			wantCount:            0,
			wantLimitingResource: corev1.ResourceMemory,
		},
		"tie-breaker by resource name": {
			requests: MapRequests{
				corev1.ResourceCPU:    1000,
				corev1.ResourceMemory: 8 * 1024 * 1024 * 1024,
			},
			capacity: MapRequests{
				corev1.ResourceCPU:    500,
				corev1.ResourceMemory: 4 * 1024 * 1024 * 1024,
			},
			wantCount:            0,
			wantLimitingResource: corev1.ResourceCPU, // "cpu" < "memory"
		},
		"resource not in capacity": {
			requests: MapRequests{
				corev1.ResourceCPU: 1000,
				"nvidia.com/gpu":   2,
			},
			capacity: MapRequests{
				corev1.ResourceCPU: 8000,
				// GPU not in capacity
			},
			wantCount:            0,
			wantLimitingResource: "nvidia.com/gpu",
		},
		"capacity exhausted": {
			requests: MapRequests{
				corev1.ResourceCPU: 1000,
			},
			capacity: MapRequests{
				corev1.ResourceCPU: 0,
			},
			wantCount:            0,
			wantLimitingResource: corev1.ResourceCPU,
		},
		"request zero is skipped": {
			requests: MapRequests{
				corev1.ResourceCPU:    0,
				corev1.ResourceMemory: 8 * 1024 * 1024 * 1024,
			},
			capacity: MapRequests{
				corev1.ResourceCPU:    8000,
				corev1.ResourceMemory: 16 * 1024 * 1024 * 1024,
			},
			wantCount:            2,
			wantLimitingResource: corev1.ResourceMemory, // CPU skipped because request is 0
		},
		"GPU exhausted on GPU node": {
			requests: MapRequests{
				corev1.ResourceCPU:    2000,
				corev1.ResourceMemory: 8 * 1024 * 1024 * 1024,
				"nvidia.com/gpu":      2,
			},
			capacity: MapRequests{
				corev1.ResourceCPU:    8000,
				corev1.ResourceMemory: 32 * 1024 * 1024 * 1024,
				"nvidia.com/gpu":      0,
			},
			wantCount:            0,
			wantLimitingResource: "nvidia.com/gpu",
		},
		"negative capacity (over-subscribed) is clamped to 0, not negative": {
			// Capacity can go negative when callers Sub() speculative usage
			// (e.g. assumedUsage during preemption) that exceeds free capacity.
			// CountInWithLimitingResource must report this as "fits 0 times",
			// not a negative count, so downstream consumers don't propagate
			// invalid values into apiserver-validated structures.
			requests: MapRequests{
				corev1.ResourceCPU: 1000,
			},
			capacity: MapRequests{
				corev1.ResourceCPU: -3000,
			},
			wantCount:            0,
			wantLimitingResource: corev1.ResourceCPU,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotCount, gotResource := tc.requests.CountInWithLimitingResource(tc.capacity)
			if tc.wantCount != gotCount {
				t.Errorf("unexpected count, want=%d, got=%d", tc.wantCount, gotCount)
			}
			if tc.wantLimitingResource != gotResource {
				t.Errorf("unexpected limiting resource, want=%s, got=%s", tc.wantLimitingResource, gotResource)
			}
		})
	}
}

func TestGreaterKeys(t *testing.T) {
	cases := map[string]struct {
		a, b MapRequests
		want []corev1.ResourceName
	}{
		"empty_a": {
			b:    MapRequests{corev1.ResourceCPU: 1},
			want: nil,
		},
		"empty_b": {
			a:    MapRequests{corev1.ResourceCPU: 1},
			want: nil,
		},
		"less_one": {
			a:    MapRequests{corev1.ResourceCPU: 500},
			b:    MapRequests{corev1.ResourceCPU: 1000},
			want: nil,
		},
		"greater_one": {
			a:    MapRequests{corev1.ResourceCPU: 1000},
			b:    MapRequests{corev1.ResourceCPU: 500},
			want: []corev1.ResourceName{corev1.ResourceCPU},
		},
		"multiple": {
			a: MapRequests{
				"r1": 2,
				"r2": 1,
			},
			b: MapRequests{
				"r1": 1,
				"r2": 2,
			},
			want: []corev1.ResourceName{"r1"},
		},
		"multiple_unrelated": {
			a: MapRequests{
				"r1": 2,
				"r2": 2,
			},
			b: MapRequests{
				"r3": 1,
				"r4": 1,
			},
			want: nil,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := tc.a.GreaterKeys(tc.b)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Unexpected result (-want, +got)\n%s", diff)
			}
		})
	}
}

func TestGreaterKeysRL(t *testing.T) {
	reqs := MapRequests{
		corev1.ResourceCPU:    1000,
		corev1.ResourceMemory: 1024,
	}
	rl := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("500m"),
		corev1.ResourceMemory: resource.MustParse("2Ki"),
	}
	got := reqs.GreaterKeysRL(rl)
	want := []corev1.ResourceName{corev1.ResourceCPU}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Unexpected result (-want, +got)\n%s", diff)
	}
}

func TestResourceQuantityRoundTrips(t *testing.T) {
	cases := map[string]struct {
		resource corev1.ResourceName
		value    int64
		expected string
	}{
		"1": {
			resource: corev1.ResourceMemory,
			value:    1,
			expected: "1",
		},
		"1k": {
			resource: corev1.ResourceMemory,
			value:    1000,
			expected: "1k",
		},
		"100k": {
			resource: corev1.ResourceMemory,
			value:    100000,
			expected: "100k",
		},
		"1M": {
			resource: corev1.ResourceMemory,
			value:    1000000,
			expected: "1M",
		},
		"1500k (1.5M)": {
			resource: corev1.ResourceMemory,
			value:    1500000,
			expected: "1500k",
		},
		"1Ki": {
			resource: corev1.ResourceMemory,
			value:    1024,
			expected: "1Ki",
		},
		"125Ki (128k)": {
			resource: corev1.ResourceMemory,
			value:    128000,
			expected: "125Ki",
		},
		"1Mi": {
			resource: corev1.ResourceMemory,
			value:    1024 * 1024,
			expected: "1Mi",
		},
		"1536Ki (1.5Mi)": {
			resource: corev1.ResourceMemory,
			value:    1024 * 1024 * 1.5,
			expected: "1536Ki",
		},
		"1Gi": {
			resource: corev1.ResourceMemory,
			value:    1024 * 1024 * 1024,
			expected: "1Gi",
		},
		"976562500Ki (10G)": {
			resource: corev1.ResourceMemory,
			value:    10000000000,
			expected: "9765625Ki",
		},
		"counter-based DRA resource": {
			resource: corev1.ResourceName("gpu.memory"),
			value:    9984 * 1024 * 1024,
			expected: "9984Mi",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			formatter := NewResourceFormatter()
			if tc.resource == corev1.ResourceName("gpu.memory") {
				formatter.RegisterBinaryFormattedResource(tc.resource)
			}
			quantity := formatter.ResourceQuantity(tc.resource, tc.value)
			initial := quantity.String()

			if initial != tc.expected {
				t.Errorf("unexpected result, want=%s, got=%s", tc.expected, initial)
			}

			serialized, _ := json.Marshal(quantity)
			var deserialized resource.Quantity
			_ = json.Unmarshal(serialized, &deserialized)
			roundtrip := deserialized.String()

			if roundtrip != tc.expected {
				t.Errorf("unexpected result after roundtrip, want=%s, got=%s", tc.expected, roundtrip)
			}
		})
	}
}

func TestLazyRequests(t *testing.T) {
	cases := map[string]struct {
		base              MapRequests
		op                func(*LazyRequests)
		wantResult        MapRequests
		wantCachedCreated bool
		wantEmpty         bool
	}{
		"no operation preserves base": {
			base:              MapRequests{corev1.ResourceCPU: 10, corev1.ResourceMemory: 100},
			op:                nil,
			wantResult:        MapRequests{corev1.ResourceCPU: 10, corev1.ResourceMemory: 100},
			wantCachedCreated: false,
			wantEmpty:         false,
		},
		"subtraction creates clone and updates result": {
			base: MapRequests{corev1.ResourceCPU: 10, corev1.ResourceMemory: 100},
			op: func(l *LazyRequests) {
				l.Sub(MapRequests{corev1.ResourceCPU: 3})
			},
			wantResult:        MapRequests{corev1.ResourceCPU: 7, corev1.ResourceMemory: 100},
			wantCachedCreated: true,
			wantEmpty:         false,
		},
		"addition creates clone and updates result": {
			base: MapRequests{corev1.ResourceCPU: 10, corev1.ResourceMemory: 100},
			op: func(l *LazyRequests) {
				l.Add(MapRequests{corev1.ResourceCPU: 5})
			},
			wantResult:        MapRequests{corev1.ResourceCPU: 15, corev1.ResourceMemory: 100},
			wantCachedCreated: true,
			wantEmpty:         false,
		},
		"subtraction with empty map short circuits": {
			base: MapRequests{corev1.ResourceCPU: 10, corev1.ResourceMemory: 100},
			op: func(l *LazyRequests) {
				l.Sub(MapRequests{})
			},
			wantResult:        MapRequests{corev1.ResourceCPU: 10, corev1.ResourceMemory: 100},
			wantCachedCreated: false,
			wantEmpty:         false,
		},
		"addition with empty map short circuits": {
			base: MapRequests{corev1.ResourceCPU: 10, corev1.ResourceMemory: 100},
			op: func(l *LazyRequests) {
				l.Add(MapRequests{})
			},
			wantResult:        MapRequests{corev1.ResourceCPU: 10, corev1.ResourceMemory: 100},
			wantCachedCreated: false,
			wantEmpty:         false,
		},
		"nil base input with non-empty addition": {
			base: nil,
			op: func(l *LazyRequests) {
				l.Add(MapRequests{corev1.ResourceCPU: 5})
			},
			wantResult:        MapRequests{corev1.ResourceCPU: 5},
			wantCachedCreated: true,
			wantEmpty:         false,
		},
		"nil base input with empty addition short circuits": {
			base: nil,
			op: func(l *LazyRequests) {
				l.Add(MapRequests{})
			},
			wantResult:        nil,
			wantCachedCreated: false,
			wantEmpty:         true,
		},
		"nil base input with non-empty subtraction": {
			base: nil,
			op: func(l *LazyRequests) {
				l.Sub(MapRequests{corev1.ResourceCPU: 5})
			},
			wantResult:        MapRequests{corev1.ResourceCPU: -5},
			wantCachedCreated: true,
			wantEmpty:         false,
		},
		"zero-value LazyRequests is empty": {
			base:              nil,
			op:                nil,
			wantResult:        nil,
			wantCachedCreated: false,
			wantEmpty:         true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var base Requests
			if tc.base != nil {
				base = NewRequestsFromMap(tc.base)
			}
			var originalBase Requests
			if base != nil {
				originalBase = base.Clone()
			}

			lazy := NewLazyRequests(base)

			if tc.op != nil {
				tc.op(&lazy)
			}

			if gotEmpty := lazy.IsEmpty(); gotEmpty != tc.wantEmpty {
				t.Errorf("unexpected IsEmpty() result, want=%t, got=%t", tc.wantEmpty, gotEmpty)
			}

			if (lazy.cached != nil) != tc.wantCachedCreated {
				t.Errorf("expected cachedCreated=%t, got cached=%v", tc.wantCachedCreated, lazy.cached)
			}

			gotResult := ToMapRequests(lazy.Get())
			wantResult := tc.wantResult
			if diff := cmp.Diff(wantResult, gotResult); diff != "" {
				t.Errorf("unexpected Get() result, diff (-want +got):\n%s", diff)
			}

			if base != nil {
				if diff := cmp.Diff(ToMapRequests(originalBase), ToMapRequests(base)); diff != "" {
					t.Errorf("base map was mutated! diff (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func TestFloorToZero(t *testing.T) {
	cases := map[string]struct {
		requests MapRequests
		want     MapRequests
	}{
		"empty": {
			requests: MapRequests{},
			want:     MapRequests{},
		},
		"negative floored to zero": {
			requests: MapRequests{
				corev1.ResourceCPU:    -100,
				corev1.ResourceMemory: 1024,
			},
			want: MapRequests{
				corev1.ResourceCPU:    0,
				corev1.ResourceMemory: 1024,
			},
		},
		"zero and positive unchanged": {
			requests: MapRequests{
				corev1.ResourceCPU:    0,
				corev1.ResourceMemory: 1024,
			},
			want: MapRequests{
				corev1.ResourceCPU:    0,
				corev1.ResourceMemory: 1024,
			},
		},
	}
	for name, tc := range cases {
		t.Run("MapRequests/"+name, func(t *testing.T) {
			requests := maps.Clone(tc.requests)
			var r Requests = requests
			r.FloorToZero()
			if diff := cmp.Diff(tc.want, requests); diff != "" {
				t.Errorf("unexpected result (-want +got):\n%s", diff)
			}
		})
		t.Run("SliceRequests/"+name, func(t *testing.T) {
			r := NewSliceRequests(tc.requests)
			if r == nil {
				// NewSliceRequests returns nil for empty/all-zero maps.
				r = &SliceRequests{}
			}
			r.FloorToZero()
			// SliceRequests omits zero values on conversion; compare non-zero
			// entries and assert no negatives remain.
			got := ToMapRequests(r)
			want := MapRequests{}
			for k, v := range tc.want {
				if v != 0 {
					want[k] = v
				}
			}
			if len(want) == 0 {
				want = nil
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("unexpected result (-want +got):\n%s", diff)
			}
			r.ForEach(func(_ corev1.ResourceName, val int64) {
				if val < 0 {
					t.Errorf("negative value %d remains after FloorToZero", val)
				}
			})
		})
	}
}

func TestMapRequestsLen(t *testing.T) {
	cases := map[string]struct {
		req  MapRequests
		want int
	}{
		"nil map": {
			req:  nil,
			want: 0,
		},
		"empty map": {
			req:  MapRequests{},
			want: 0,
		},
		"single resource": {
			req:  MapRequests{corev1.ResourceCPU: 1000},
			want: 1,
		},
		"multiple resources": {
			req:  MapRequests{corev1.ResourceCPU: 1000, corev1.ResourceMemory: 1024, corev1.ResourcePods: 1},
			want: 3,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.req.Len(); got != tc.want {
				t.Errorf("unexpected Len(), want=%d, got=%d", tc.want, got)
			}
		})
	}
}

func TestMapRequestsIsEmpty(t *testing.T) {
	cases := map[string]struct {
		req  MapRequests
		want bool
	}{
		"nil map": {
			req:  nil,
			want: true,
		},
		"empty map": {
			req:  MapRequests{},
			want: true,
		},
		"non-empty map": {
			req:  MapRequests{corev1.ResourceCPU: 1000},
			want: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.req.IsEmpty(); got != tc.want {
				t.Errorf("unexpected IsEmpty(), want=%t, got=%t", tc.want, got)
			}
		})
	}
}

func TestMapRequestsGetValue(t *testing.T) {
	cases := map[string]struct {
		req      MapRequests
		resource corev1.ResourceName
		want     int64
	}{
		"nil map": {
			req:      nil,
			resource: corev1.ResourceCPU,
			want:     0,
		},
		"empty map": {
			req:      MapRequests{},
			resource: corev1.ResourceCPU,
			want:     0,
		},
		"missing resource": {
			req:      MapRequests{corev1.ResourceMemory: 1024},
			resource: corev1.ResourceCPU,
			want:     0,
		},
		"existing resource": {
			req:      MapRequests{corev1.ResourceCPU: 1000},
			resource: corev1.ResourceCPU,
			want:     1000,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.req.GetValue(tc.resource); got != tc.want {
				t.Errorf("unexpected GetValue(), want=%d, got=%d", tc.want, got)
			}
		})
	}
}

func TestMapRequestsClone(t *testing.T) {
	t.Run("nil map clone", func(t *testing.T) {
		var m MapRequests
		cloned := m.Clone()
		if cloned != nil && !cloned.IsEmpty() {
			t.Errorf("expected empty clone for nil map, got %v", cloned)
		}
	})

	t.Run("empty map clone", func(t *testing.T) {
		m := MapRequests{}
		cloned := m.Clone()
		if cloned != nil && !cloned.IsEmpty() {
			t.Errorf("expected empty clone, got %v", cloned)
		}
	})

	t.Run("non-empty map clone", func(t *testing.T) {
		m := MapRequests{corev1.ResourceCPU: 1000}
		cloned := m.Clone()
		if !cmp.Equal(m, cloned) {
			t.Errorf("cloned map mismatch (-want +got):\n%s", cmp.Diff(m, cloned))
		}
		cloned.Add(MapRequests{corev1.ResourceMemory: 1024})
		if m.GetValue(corev1.ResourceMemory) != 0 {
			t.Errorf("original map was mutated after modifying clone")
		}
	})
}
