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
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestCountIn(t *testing.T) {
	cases := map[string]struct {
		requests   Requests
		capacity   Requests
		wantResult int32
	}{
		"requests equal capacity": {
			requests: Requests{
				corev1.ResourceCPU:    1,
				corev1.ResourceMemory: 1,
			},
			capacity: Requests{
				corev1.ResourceCPU:    1,
				corev1.ResourceMemory: 1,
			},
			wantResult: 1,
		},
		"requests with extra resource": {
			requests: Requests{
				corev1.ResourceCPU:    1,
				corev1.ResourceMemory: 1,
			},
			capacity: Requests{
				corev1.ResourceCPU: 1,
			},
			wantResult: 0,
		},
		"first resource is bottleneck": {
			requests: Requests{
				corev1.ResourceCPU:    5,
				corev1.ResourceMemory: 1,
			},
			capacity: Requests{
				corev1.ResourceCPU:    12,
				corev1.ResourceMemory: 8,
			},
			wantResult: 2,
		},
		"second resource is bottleneck": {
			requests: Requests{
				corev1.ResourceCPU:    1,
				corev1.ResourceMemory: 5,
			},
			capacity: Requests{
				corev1.ResourceCPU:    8,
				corev1.ResourceMemory: 12,
			},
			wantResult: 2,
		},
		"capacity non divisible cleanly by requests": {
			requests: Requests{
				corev1.ResourceCPU: 2,
			},
			capacity: Requests{
				corev1.ResourceCPU: 5,
			},
			wantResult: 2,
		},
		"requests amount of zero": {
			requests: Requests{
				corev1.ResourceCPU: 0,
			},
			capacity: Requests{
				corev1.ResourceCPU: 5,
			},
			wantResult: int32(math.MaxInt32),
		},
		"has one resource with request amount of zero": {
			requests: Requests{
				corev1.ResourceCPU:    0,
				corev1.ResourceMemory: 1,
			},
			capacity: Requests{
				corev1.ResourceCPU:    5,
				corev1.ResourceMemory: 5,
			},
			wantResult: 5,
		},
		"requests amount of zero for extra resource": {
			requests: Requests{
				corev1.ResourceCPU:    1,
				corev1.ResourceMemory: 0,
			},
			capacity: Requests{
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
		requests             Requests
		capacity             Requests
		wantCount            int32
		wantLimitingResource corev1.ResourceName
	}{
		"CPU is limiting": {
			requests: Requests{
				corev1.ResourceCPU:    1000,
				corev1.ResourceMemory: 8 * 1024 * 1024 * 1024,
			},
			capacity: Requests{
				corev1.ResourceCPU:    500,
				corev1.ResourceMemory: 32 * 1024 * 1024 * 1024,
			},
			wantCount:            0,
			wantLimitingResource: corev1.ResourceCPU,
		},
		"memory is limiting": {
			requests: Requests{
				corev1.ResourceCPU:    1000,
				corev1.ResourceMemory: 16 * 1024 * 1024 * 1024,
			},
			capacity: Requests{
				corev1.ResourceCPU:    8000,
				corev1.ResourceMemory: 8 * 1024 * 1024 * 1024,
			},
			wantCount:            0,
			wantLimitingResource: corev1.ResourceMemory,
		},
		"tie-breaker by resource name": {
			requests: Requests{
				corev1.ResourceCPU:    1000,
				corev1.ResourceMemory: 8 * 1024 * 1024 * 1024,
			},
			capacity: Requests{
				corev1.ResourceCPU:    500,
				corev1.ResourceMemory: 4 * 1024 * 1024 * 1024,
			},
			wantCount:            0,
			wantLimitingResource: corev1.ResourceCPU, // "cpu" < "memory"
		},
		"resource not in capacity": {
			requests: Requests{
				corev1.ResourceCPU: 1000,
				"nvidia.com/gpu":   2,
			},
			capacity: Requests{
				corev1.ResourceCPU: 8000,
				// GPU not in capacity
			},
			wantCount:            0,
			wantLimitingResource: "nvidia.com/gpu",
		},
		"capacity exhausted": {
			requests: Requests{
				corev1.ResourceCPU: 1000,
			},
			capacity: Requests{
				corev1.ResourceCPU: 0,
			},
			wantCount:            0,
			wantLimitingResource: corev1.ResourceCPU,
		},
		"request zero is skipped": {
			requests: Requests{
				corev1.ResourceCPU:    0,
				corev1.ResourceMemory: 8 * 1024 * 1024 * 1024,
			},
			capacity: Requests{
				corev1.ResourceCPU:    8000,
				corev1.ResourceMemory: 16 * 1024 * 1024 * 1024,
			},
			wantCount:            2,
			wantLimitingResource: corev1.ResourceMemory, // CPU skipped because request is 0
		},
		"GPU exhausted on GPU node": {
			requests: Requests{
				corev1.ResourceCPU:    2000,
				corev1.ResourceMemory: 8 * 1024 * 1024 * 1024,
				"nvidia.com/gpu":      2,
			},
			capacity: Requests{
				corev1.ResourceCPU:    8000,
				corev1.ResourceMemory: 32 * 1024 * 1024 * 1024,
				"nvidia.com/gpu":      0,
			},
			wantCount:            0,
			wantLimitingResource: "nvidia.com/gpu",
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
		a, b Requests
		want []corev1.ResourceName
	}{
		"empty_a": {
			b:    Requests{corev1.ResourceCPU: 1},
			want: nil,
		},
		"empty_b": {
			a:    Requests{corev1.ResourceCPU: 1},
			want: nil,
		},
		"less_one": {
			a:    Requests{corev1.ResourceCPU: 500},
			b:    Requests{corev1.ResourceCPU: 1000},
			want: nil,
		},
		"greater_one": {
			a:    Requests{corev1.ResourceCPU: 1000},
			b:    Requests{corev1.ResourceCPU: 500},
			want: []corev1.ResourceName{corev1.ResourceCPU},
		},
		"multiple": {
			a: Requests{
				"r1": 2,
				"r2": 1,
			},
			b: Requests{
				"r1": 1,
				"r2": 2,
			},
			want: []corev1.ResourceName{"r1"},
		},
		"multiple_unrelated": {
			a: Requests{
				"r1": 2,
				"r2": 2,
			},
			b: Requests{
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
	reqs := Requests{
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
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
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
