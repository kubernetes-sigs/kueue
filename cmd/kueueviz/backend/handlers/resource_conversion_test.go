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

package handlers

import (
	"math"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func TestQuantityToFloat64(t *testing.T) {
	tests := map[string]struct {
		input resource.Quantity
		want  float64
	}{
		"zero": {
			input: resource.MustParse("0"),
			want:  0,
		},
		"integer cores": {
			input: resource.MustParse("4"),
			want:  4,
		},
		"millicores": {
			input: resource.MustParse("500m"),
			want:  0.5,
		},
		"fractional millicores": {
			input: resource.MustParse("4250m"),
			want:  4.25,
		},
		"Ki suffix": {
			input: resource.MustParse("512Ki"),
			want:  512 * 1024,
		},
		"Mi suffix": {
			input: resource.MustParse("256Mi"),
			want:  256 * 1024 * 1024,
		},
		"Gi suffix": {
			input: resource.MustParse("2Gi"),
			want:  2 * 1024 * 1024 * 1024,
		},
		"Ti suffix": {
			input: resource.MustParse("1Ti"),
			want:  1024 * 1024 * 1024 * 1024,
		},
		"Pi suffix": {
			input: resource.MustParse("1Pi"),
			want:  1024 * 1024 * 1024 * 1024 * 1024,
		},
		"Ei suffix": {
			input: resource.MustParse("1Ei"),
			want:  1024 * 1024 * 1024 * 1024 * 1024 * 1024,
		},
		"decimal k suffix": {
			input: resource.MustParse("1k"),
			want:  1000,
		},
		"decimal M suffix": {
			input: resource.MustParse("1M"),
			want:  1000000,
		},
		"decimal G suffix": {
			input: resource.MustParse("1G"),
			want:  1000000000,
		},
		"nano suffix": {
			input: resource.MustParse("1n"),
			want:  1e-9,
		},
		"micro suffix": {
			input: resource.MustParse("1u"),
			want:  1e-6,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := quantityToFloat64(tc.input)
			if math.Abs(got-tc.want) > 1e-6*math.Max(1, math.Abs(tc.want)) {
				t.Errorf("quantityToFloat64(%v) = %v, want %v", tc.input.String(), got, tc.want)
			}
		})
	}
}

func TestConvertResourceGroups(t *testing.T) {
	nominalQuota := resource.MustParse("4")
	borrowingLimit := resource.MustParse("2")
	lendingLimit := resource.MustParse("1")

	rgs := []kueueapi.ResourceGroup{
		{
			CoveredResources: []corev1.ResourceName{"cpu", "memory"},
			Flavors: []kueueapi.FlavorQuotas{
				{
					Name: "default-flavor",
					Resources: []kueueapi.ResourceQuota{
						{
							Name:           "cpu",
							NominalQuota:   nominalQuota,
							BorrowingLimit: &borrowingLimit,
							LendingLimit:   &lendingLimit,
						},
					},
				},
			},
		},
	}

	result := convertResourceGroups(rgs)

	if len(result) != 1 {
		t.Fatalf("expected 1 resource group, got %d", len(result))
	}

	rg := result[0]
	coveredResources := rg["coveredResources"].([]string)
	if len(coveredResources) != 2 || coveredResources[0] != "cpu" || coveredResources[1] != "memory" {
		t.Fatalf("unexpected coveredResources: %v", coveredResources)
	}

	flavors := rg["flavors"].([]map[string]any)
	if len(flavors) != 1 {
		t.Fatalf("expected 1 flavor, got %d", len(flavors))
	}

	if flavors[0]["name"] != "default-flavor" {
		t.Fatalf("expected flavor name 'default-flavor', got %v", flavors[0]["name"])
	}

	resources := flavors[0]["resources"].([]map[string]any)
	if len(resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(resources))
	}

	res := resources[0]
	if res["name"] != "cpu" {
		t.Errorf("expected resource name 'cpu', got %v", res["name"])
	}
	if res["nominalQuota"] != float64(4) {
		t.Errorf("expected nominalQuota 4, got %v", res["nominalQuota"])
	}
	if res["borrowingLimit"] != float64(2) {
		t.Errorf("expected borrowingLimit 2, got %v", res["borrowingLimit"])
	}
	if res["lendingLimit"] != float64(1) {
		t.Errorf("expected lendingLimit 1, got %v", res["lendingLimit"])
	}
}

func TestConvertResourceGroups_NilBorrowingLimit(t *testing.T) {
	nominalQuota := resource.MustParse("4")

	rgs := []kueueapi.ResourceGroup{
		{
			CoveredResources: []corev1.ResourceName{"cpu"},
			Flavors: []kueueapi.FlavorQuotas{
				{
					Name: "default-flavor",
					Resources: []kueueapi.ResourceQuota{
						{
							Name:         "cpu",
							NominalQuota: nominalQuota,
							// BorrowingLimit is nil — unlimited borrowing
						},
					},
				},
			},
		},
	}

	result := convertResourceGroups(rgs)
	resources := result[0]["flavors"].([]map[string]any)[0]["resources"].([]map[string]any)
	res := resources[0]

	if _, exists := res["borrowingLimit"]; exists {
		t.Errorf("borrowingLimit should be absent (nil), but got %v", res["borrowingLimit"])
	}
	if _, exists := res["lendingLimit"]; exists {
		t.Errorf("lendingLimit should be absent (nil), but got %v", res["lendingLimit"])
	}
}

func TestConvertFlavorsUsage(t *testing.T) {
	fus := []kueueapi.FlavorUsage{
		{
			Name: "default-flavor",
			Resources: []kueueapi.ResourceUsage{
				{
					Name:     "cpu",
					Total:    resource.MustParse("2500m"),
					Borrowed: resource.MustParse("500m"),
				},
				{
					Name:     "memory",
					Total:    resource.MustParse("4Gi"),
					Borrowed: resource.MustParse("1Gi"),
				},
			},
		},
	}

	result := convertFlavorsUsage(fus)

	if len(result) != 1 {
		t.Fatalf("expected 1 flavor usage, got %d", len(result))
	}
	if result[0]["name"] != "default-flavor" {
		t.Fatalf("expected flavor name 'default-flavor', got %v", result[0]["name"])
	}

	resources := result[0]["resources"].([]map[string]any)
	if len(resources) != 2 {
		t.Fatalf("expected 2 resources, got %d", len(resources))
	}

	// cpu: 2500m = 2.5 cores, borrowed 500m = 0.5 cores
	cpu := resources[0]
	if cpu["name"] != "cpu" {
		t.Errorf("expected resource name 'cpu', got %v", cpu["name"])
	}
	if math.Abs(cpu["total"].(float64)-2.5) > 1e-9 {
		t.Errorf("expected cpu total 2.5, got %v", cpu["total"])
	}
	if math.Abs(cpu["borrowed"].(float64)-0.5) > 1e-9 {
		t.Errorf("expected cpu borrowed 0.5, got %v", cpu["borrowed"])
	}

	// memory: 4Gi, borrowed 1Gi
	mem := resources[1]
	if mem["name"] != "memory" {
		t.Errorf("expected resource name 'memory', got %v", mem["name"])
	}
	expectedMemTotal := float64(4 * 1024 * 1024 * 1024)
	if math.Abs(mem["total"].(float64)-expectedMemTotal) > 1 {
		t.Errorf("expected memory total %v, got %v", expectedMemTotal, mem["total"])
	}
	expectedMemBorrowed := float64(1 * 1024 * 1024 * 1024)
	if math.Abs(mem["borrowed"].(float64)-expectedMemBorrowed) > 1 {
		t.Errorf("expected memory borrowed %v, got %v", expectedMemBorrowed, mem["borrowed"])
	}
}

func TestConvertLocalQueueFlavorsUsage(t *testing.T) {
	fus := []kueueapi.LocalQueueFlavorUsage{
		{
			Name: "gpu-flavor",
			Resources: []kueueapi.LocalQueueResourceUsage{
				{
					Name:  "nvidia.com/gpu",
					Total: resource.MustParse("3"),
				},
			},
		},
	}

	result := convertLocalQueueFlavorsUsage(fus)

	if len(result) != 1 {
		t.Fatalf("expected 1 flavor usage, got %d", len(result))
	}
	if result[0]["name"] != "gpu-flavor" {
		t.Fatalf("expected flavor name 'gpu-flavor', got %v", result[0]["name"])
	}

	resources := result[0]["resources"].([]map[string]any)
	if len(resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(resources))
	}
	if resources[0]["name"] != "nvidia.com/gpu" {
		t.Errorf("expected resource name 'nvidia.com/gpu', got %v", resources[0]["name"])
	}
	if resources[0]["total"] != float64(3) {
		t.Errorf("expected total 3, got %v", resources[0]["total"])
	}
	// LocalQueueResourceUsage has no Borrowed field
	if _, exists := resources[0]["borrowed"]; exists {
		t.Errorf("borrowed should not be present in LocalQueueFlavorUsage conversion")
	}
}

func TestConvertResourceGroups_Empty(t *testing.T) {
	result := convertResourceGroups(nil)
	if len(result) != 0 {
		t.Errorf("expected empty result for nil input, got %d items", len(result))
	}

	result = convertResourceGroups([]kueueapi.ResourceGroup{})
	if len(result) != 0 {
		t.Errorf("expected empty result for empty input, got %d items", len(result))
	}
}

func TestConvertFlavorsUsage_Empty(t *testing.T) {
	result := convertFlavorsUsage(nil)
	if len(result) != 0 {
		t.Errorf("expected empty result for nil input, got %d items", len(result))
	}
}

func TestConvertLocalQueueFlavorsUsage_Empty(t *testing.T) {
	result := convertLocalQueueFlavorsUsage(nil)
	if len(result) != 0 {
		t.Errorf("expected empty result for nil input, got %d items", len(result))
	}
}
