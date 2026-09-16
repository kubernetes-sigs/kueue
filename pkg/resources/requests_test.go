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
	"strconv"
	"sync"
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
		"count above int32 is clamped to MaxInt32": {
			requests: MapRequests{
				corev1.ResourceMemory: 1,
			},
			capacity: MapRequests{
				corev1.ResourceMemory: math.MaxInt32 + 1,
			},
			wantCount:            math.MaxInt32,
			wantLimitingResource: corev1.ResourceMemory,
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
		"multiple_greater_sorted": {
			a: MapRequests{
				"r2": 2,
				"r1": 2,
			},
			b: MapRequests{
				"r2": 1,
				"r1": 1,
			},
			want: []corev1.ResourceName{"r1", "r2"},
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

func resetBinaryFormattedResources() {
	binaryFormattedResources = sync.Map{}
}

func TestResourceValueClampsOutsideInt64(t *testing.T) {
	cases := map[string]struct {
		resource corev1.ResourceName
		quantity string
		want     int64
	}{
		"an ordinary extended resource is unchanged": {
			resource: "example.com/gpu",
			quantity: "8",
			want:     8,
		},
		"the largest representable value is kept": {
			resource: "example.com/gpu",
			quantity: strconv.FormatInt(math.MaxInt64, 10),
			want:     math.MaxInt64,
		},
		"one past it is clamped rather than wrapped": {
			resource: "example.com/gpu",
			quantity: "9223372036854775808",
			want:     math.MaxInt64,
		},
		"far past it is clamped as well": {
			resource: "example.com/gpu",
			quantity: "100000000000000000000",
			want:     math.MaxInt64,
		},
		"an ordinary negative value is unchanged": {
			resource: "example.com/gpu",
			quantity: "-3",
			want:     -3,
		},
		"far below the range is clamped rather than wrapped": {
			resource: "example.com/gpu",
			quantity: "-100000000000000000000",
			want:     math.MinInt64,
		},
		"memory past the range is clamped too": {
			resource: corev1.ResourceMemory,
			quantity: "100Ei",
			want:     math.MaxInt64,
		},
		"cpu is still read in milli-units": {
			resource: corev1.ResourceCPU,
			quantity: "1500m",
			want:     1500,
		},
		"cpu past the milli range is clamped": {
			resource: corev1.ResourceCPU,
			quantity: "10000000000000000",
			want:     math.MaxInt64,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := ResourceValue(tc.resource, resource.MustParse(tc.quantity)); got != tc.want {
				t.Errorf("ResourceValue(%s, %s) = %d, want %d", tc.resource, tc.quantity, got, tc.want)
			}
		})
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
			t.Cleanup(resetBinaryFormattedResources)
			if tc.resource == corev1.ResourceName("gpu.memory") {
				RegisterBinaryFormattedResource(tc.resource)
			}
			quantity := ResourceQuantity(tc.resource, tc.value)
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

			gotResult := MapRequests(ToMap(lazy.Get()))
			wantResult := tc.wantResult
			if diff := cmp.Diff(wantResult, gotResult); diff != "" {
				t.Errorf("unexpected Get() result, diff (-want +got):\n%s", diff)
			}

			if base != nil {
				if diff := cmp.Diff(MapRequests(ToMap(originalBase)), MapRequests(ToMap(base))); diff != "" {
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
			got := MapRequests(ToMap(r))
			want := tc.want
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
			if got := tc.req.ResourceValue(tc.resource); got != tc.want {
				t.Errorf("unexpected GetValue(), want=%d, got=%d", tc.want, got)
			}
		})
	}
}

func TestMapRequestsSet(t *testing.T) {
	cases := map[string]struct {
		initial  MapRequests
		setKey   corev1.ResourceName
		setValue int64
		want     MapRequests
	}{
		"update existing resource": {
			initial:  MapRequests{corev1.ResourceCPU: 1000},
			setKey:   corev1.ResourceCPU,
			setValue: 2000,
			want:     MapRequests{corev1.ResourceCPU: 2000},
		},
		"insert new resource": {
			initial:  MapRequests{corev1.ResourceCPU: 1000},
			setKey:   corev1.ResourceMemory,
			setValue: 1024,
			want:     MapRequests{corev1.ResourceCPU: 1000, corev1.ResourceMemory: 1024},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := maps.Clone(tc.initial)
			m.Set(tc.setKey, tc.setValue)
			if diff := cmp.Diff(tc.want, m); diff != "" {
				t.Errorf("Set mismatch (-want +got):\n%s", diff)
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
		if m.ResourceValue(corev1.ResourceMemory) != 0 {
			t.Errorf("original map was mutated after modifying clone")
		}
	})
}

func TestToMap(t *testing.T) {
	cases := map[string]struct {
		req  Requests
		want MapRequests
	}{
		"nil requests": {
			req:  nil,
			want: nil,
		},
		"empty MapRequests": {
			req:  MapRequests{},
			want: nil,
		},
		"non-empty MapRequests": {
			req:  MapRequests{corev1.ResourceCPU: 1000},
			want: MapRequests{corev1.ResourceCPU: 1000},
		},
		"SliceRequests with non-zero values": {
			req:  NewSliceRequests(MapRequests{corev1.ResourceCPU: 1000, corev1.ResourceMemory: 2048}),
			want: MapRequests{corev1.ResourceCPU: 1000, corev1.ResourceMemory: 2048},
		},
		"MapRequests with zero values": {
			req:  MapRequests{corev1.ResourceCPU: 0},
			want: MapRequests{corev1.ResourceCPU: 0},
		},
		"SliceRequests with zero values": {
			req:  NewSliceRequests(MapRequests{corev1.ResourceCPU: 0}),
			want: MapRequests{corev1.ResourceCPU: 0},
		},
		"SliceRequests with mixed zero and non-zero values": {
			req:  NewSliceRequests(MapRequests{corev1.ResourceCPU: 1000, corev1.ResourceMemory: 0}),
			want: MapRequests{corev1.ResourceCPU: 1000, corev1.ResourceMemory: 0},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := MapRequests(ToMap(tc.req))
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ToMap mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRequestsEqual(t *testing.T) {
	m1 := MapRequests{corev1.ResourceCPU: 1000, corev1.ResourceMemory: 2048}
	m2 := MapRequests{corev1.ResourceCPU: 1000, corev1.ResourceMemory: 2048}
	m3 := MapRequests{corev1.ResourceCPU: 1000}

	s1 := NewSliceRequests(m1)
	s2 := NewSliceRequests(m2)
	s3 := NewSliceRequests(m3)

	tests := map[string]struct {
		a, b Requests
		want bool
	}{
		"both nil":                {a: nil, b: nil, want: true},
		"nil and empty Map":       {a: nil, b: MapRequests{}, want: false},
		"nil and empty Slice":     {a: nil, b: NewSliceRequests(MapRequests{}), want: false},
		"equal MapRequests":       {a: m1, b: m2, want: true},
		"equal SliceRequests":     {a: s1, b: s2, want: true},
		"equal Map and Slice":     {a: m1, b: s2, want: true},
		"equal Slice and Map":     {a: s1, b: m2, want: true},
		"different MapRequests":   {a: m1, b: m3, want: false},
		"different SliceRequests": {a: s1, b: s3, want: false},
		"different Map and Slice": {a: m1, b: s3, want: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := Equal(tc.a, tc.b); got != tc.want {
				t.Errorf("Equal() = %v, want %v", got, tc.want)
			}
		})
	}
}

// CPU ceilings in milli: MaxInt64 cores is the largest a Quantity carries.
const (
	cpuCeiling      = "9223372036854775807000"
	cpuPastCeiling  = "9223372036854775807001"
	cpuBelowCeiling = "9223372036854775806999"
)

func TestAmountQuantity(t *testing.T) {
	cases := map[string]struct {
		name      corev1.ResourceName
		amount    Amount
		want      string
		wantExact bool
	}{
		"whole cores in milli":                  {name: corev1.ResourceCPU, amount: NewAmount(2000), want: "2", wantExact: true},
		"a fraction of a core":                  {name: corev1.ResourceCPU, amount: NewAmount(1500), want: "1500m", wantExact: true},
		"the largest int64 milli":               {name: corev1.ResourceCPU, amount: NewAmount(math.MaxInt64), want: "9223372036854775807m", wantExact: true},
		"one milli past the largest":            {name: corev1.ResourceCPU, amount: bigAmount(t, "9223372036854775808"), want: "9223372036854775808m", wantExact: true},
		"10P of cpu past int64":                 {name: corev1.ResourceCPU, amount: cpuAmount("10P"), want: "10P", wantExact: true},
		"a milli past 10P":                      {name: corev1.ResourceCPU, amount: bigAmount(t, "10000000000000000001"), want: "10000000000000000001m", wantExact: true},
		"1E of cpu past int64":                  {name: corev1.ResourceCPU, amount: cpuAmount("1E"), want: "1E", wantExact: true},
		"sixteen of the largest int64":          {name: corev1.ResourceCPU, amount: bigAmount(t, "147573952589676412912"), want: "147573952589676412912m", wantExact: true},
		"a milli below the cpu ceiling":         {name: corev1.ResourceCPU, amount: bigAmount(t, cpuBelowCeiling), want: "9223372036854775806999m", wantExact: true},
		"the cpu ceiling":                       {name: corev1.ResourceCPU, amount: bigAmount(t, cpuCeiling), want: "9223372036854775807", wantExact: true},
		"a milli past the cpu ceiling":          {name: corev1.ResourceCPU, amount: bigAmount(t, cpuPastCeiling), want: "9223372036854775807", wantExact: false},
		"far past the cpu ceiling":              {name: corev1.ResourceCPU, amount: bigAmount(t, "9223372036854775807000000"), want: "9223372036854775807", wantExact: false},
		"the negative cpu ceiling":              {name: corev1.ResourceCPU, amount: bigAmount(t, "-"+cpuCeiling), want: "-9223372036854775807", wantExact: true},
		"a milli past the negative cpu ceiling": {name: corev1.ResourceCPU, amount: bigAmount(t, "-"+cpuPastCeiling), want: "-9223372036854775807", wantExact: false},
		"sixteen of the largest negative":       {name: corev1.ResourceCPU, amount: bigAmount(t, "-147573952589676412912"), want: "-147573952589676412912m", wantExact: true},

		"whole devices":               {name: "example.com/gpu", amount: NewAmount(8), want: "8", wantExact: true},
		"the largest int64 device":    {name: "example.com/gpu", amount: NewAmount(math.MaxInt64), want: "9223372036854775807", wantExact: true},
		"one past the largest device": {name: "example.com/gpu", amount: bigAmount(t, "9223372036854775808"), want: "9223372036854775807", wantExact: false},
		"the largest negative device": {name: "example.com/gpu", amount: NewAmount(-math.MaxInt64), want: "-9223372036854775807", wantExact: true},
		// MinInt64 fits an int64 and is one past the magnitude a Quantity carries.
		"the smallest int64 device": {name: "example.com/gpu", amount: NewAmount(math.MinInt64), want: "-9223372036854775807", wantExact: false},
		// In milli this is nine million cores, which a Quantity holds.
		"the smallest int64 milli of cpu": {name: corev1.ResourceCPU, amount: NewAmount(math.MinInt64), want: "-9223372036854775808m", wantExact: true},
		"far past in the negative":        {name: "example.com/gpu", amount: bigAmount(t, "-18446744073709551614"), want: "-9223372036854775807", wantExact: false},
		"memory past int64":               {name: corev1.ResourceMemory, amount: bigAmount(t, "9223372036854775808"), want: "9223372036854775807", wantExact: false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			q := AmountQuantity(tc.name, tc.amount)
			if got := q.String(); got != tc.want {
				t.Errorf("String() = %s, want %s", got, tc.want)
			}
			if got := AmountQuantityString(tc.name, tc.amount); got != tc.want {
				t.Errorf("AmountQuantityString() = %s, want %s", got, tc.want)
			}
			// The Quantity must be the number, not only a string that reads back as itself.
			want := tc.amount
			if !tc.wantExact {
				want = quantityCeiling(t, tc.name, tc.amount.Sign())
			}
			if back := AmountFromQuantity(tc.name, q); !back.Equal(want) {
				t.Errorf("came back as %s, want %s", back, want)
			}
			// What is written has to read back as the same number.
			b, err := json.Marshal(q)
			if err != nil {
				t.Fatalf("Marshal() = %v", err)
			}
			var read resource.Quantity
			if err := json.Unmarshal(b, &read); err != nil {
				t.Fatalf("Unmarshal(%s) = %v", b, err)
			}
			if read.Cmp(q) != 0 {
				t.Errorf("round trip of %s came back as %s", q.String(), read.String())
			}
		})
	}
}

// quantityCeiling is the amount a capped value lands on, in the unit it is accounted in.
func quantityCeiling(t *testing.T, name corev1.ResourceName, sign int) Amount {
	t.Helper()
	digits := "9223372036854775807"
	if name == corev1.ResourceCPU {
		digits = cpuCeiling
	}
	if sign < 0 {
		digits = "-" + digits
	}
	return bigAmount(t, digits)
}

// Capping a milli past a whole core would make an increase read as a decrease.
func TestAmountQuantityIsMonotonic(t *testing.T) {
	steps := []Amount{
		NewAmount(math.MaxInt64),
		bigAmount(t, "9223372036854775808"),
		cpuAmount("10P"),
		bigAmount(t, "10000000000000000001"),
		cpuAmount("1E"),
		bigAmount(t, cpuBelowCeiling),
		bigAmount(t, cpuCeiling),
		bigAmount(t, cpuPastCeiling),
	}
	for i := 1; i < len(steps); i++ {
		if steps[i].Cmp(steps[i-1]) <= 0 {
			t.Fatalf("the fixture is not increasing at %d: %s then %s", i, steps[i-1], steps[i])
		}
		prev := AmountQuantity(corev1.ResourceCPU, steps[i-1])
		next := AmountQuantity(corev1.ResourceCPU, steps[i])
		if next.Cmp(prev) < 0 {
			t.Errorf("%s reports %s, less than %s reports for %s",
				steps[i], next.String(), prev.String(), steps[i-1])
		}
	}
}

func TestAmountQuantityKeepsTheRegisteredFormat(t *testing.T) {
	t.Cleanup(resetBinaryFormattedResources)
	RegisterBinaryFormattedResource("example.com/memory")

	q := AmountQuantity("example.com/memory", NewAmount(2*1024*1024*1024))
	if got := q.String(); got != "2Gi" {
		t.Errorf("String() = %s, want 2Gi", got)
	}
}

func cpuAmount(s string) Amount {
	return AmountFromQuantity(corev1.ResourceCPU, resource.MustParse(s))
}
