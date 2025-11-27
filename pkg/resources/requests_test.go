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
