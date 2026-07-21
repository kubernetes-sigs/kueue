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

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2/ktesting"
)

func TestRoundToValidValues(t *testing.T) {
	tests := []struct {
		name        string
		value       string
		validValues []string
		want        string
		wantErr     bool
	}{
		{
			name:        "exact match",
			value:       "20Gi",
			validValues: []string{"10Gi", "20Gi", "40Gi"},
			want:        "20Gi",
		},
		{
			name:        "round up to next",
			value:       "15Gi",
			validValues: []string{"10Gi", "20Gi", "40Gi"},
			want:        "20Gi",
		},
		{
			name:        "below minimum rounds to first",
			value:       "5Gi",
			validValues: []string{"10Gi", "20Gi", "40Gi"},
			want:        "10Gi",
		},
		{
			name:        "exceeds all values",
			value:       "50Gi",
			validValues: []string{"10Gi", "20Gi", "40Gi"},
			wantErr:     true,
		},
		{
			name:        "single valid value exact",
			value:       "80Gi",
			validValues: []string{"80Gi"},
			want:        "80Gi",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			vals := make([]resource.Quantity, len(tc.validValues))
			for i, v := range tc.validValues {
				vals[i] = resource.MustParse(v)
			}
			got, err := roundToValidValues(resource.MustParse(tc.value), vals)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %s", got.String())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Cmp(resource.MustParse(tc.want)) != 0 {
				t.Errorf("got %s, want %s", got.String(), tc.want)
			}
		})
	}
}

func TestRoundToValidRange(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		min     string
		max     string
		step    string
		want    string
		wantErr bool
	}{
		{
			name:  "already aligned",
			value: "20Gi",
			min:   "1Gi",
			max:   "80Gi",
			step:  "1Gi",
			want:  "20Gi",
		},
		{
			name:  "round up to step",
			value: "15500Mi",
			min:   "1Gi",
			max:   "80Gi",
			step:  "1Gi",
			want:  "16Gi",
		},
		{
			name:  "below min rounds to min",
			value: "500Mi",
			min:   "1Gi",
			max:   "80Gi",
			step:  "1Gi",
			want:  "1Gi",
		},
		{
			name:    "exceeds max after rounding",
			value:   "81Gi",
			min:     "1Gi",
			max:     "80Gi",
			step:    "1Gi",
			wantErr: true,
		},
		{
			name:  "no step no rounding",
			value: "15500Mi",
			min:   "1Gi",
			max:   "80Gi",
			want:  "15500Mi",
		},
		{
			name:  "step without min skips alignment",
			value: "15500Mi",
			max:   "80Gi",
			step:  "1Gi",
			want:  "15500Mi",
		},
		{
			name:  "large step alignment",
			value: "25Gi",
			min:   "10Gi",
			max:   "80Gi",
			step:  "20Gi",
			want:  "30Gi",
		},
		{
			name:  "exact step boundary",
			value: "30Gi",
			min:   "10Gi",
			max:   "80Gi",
			step:  "20Gi",
			want:  "30Gi",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			vr := &resourcev1.CapacityRequestPolicyRange{}
			if tc.min != "" {
				q := resource.MustParse(tc.min)
				vr.Min = &q
			}
			if tc.max != "" {
				q := resource.MustParse(tc.max)
				vr.Max = &q
			}
			if tc.step != "" {
				q := resource.MustParse(tc.step)
				vr.Step = &q
			}
			got, err := roundToValidRange(resource.MustParse(tc.value), vr)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %s", got.String())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Cmp(resource.MustParse(tc.want)) != 0 {
				t.Errorf("got %s, want %s", got.String(), tc.want)
			}
		})
	}
}

func TestComputeCapacityCharge(t *testing.T) {
	log := ktesting.NewLogger(t, ktesting.NewConfig())
	reqPath := field.NewPath("test")

	makeDevice := func(name string, capValue string, policy *resourcev1.CapacityRequestPolicy) resourcev1.Device {
		return resourcev1.Device{
			Name: name,
			Capacity: map[resourcev1.QualifiedName]resourcev1.DeviceCapacity{
				"memory": {
					Value:         resource.MustParse(capValue),
					RequestPolicy: policy,
				},
			},
		}
	}
	ptrQty := func(s string) *resource.Quantity {
		q := resource.MustParse(s)
		return &q
	}

	tests := []struct {
		name            string
		matched         []resourcev1.Device
		count           int64
		explicitRequest *resource.Quantity
		wantCharge      string
		wantErrType     field.ErrorType
	}{
		{
			name:            "explicit request with single device",
			matched:         []resourcev1.Device{makeDevice("gpu-0", "80Gi", nil)},
			count:           1,
			explicitRequest: ptrQty("20Gi"),
			wantCharge:      "20Gi",
		},
		{
			name: "no request uses device Default",
			matched: []resourcev1.Device{
				makeDevice("gpu-0", "80Gi", &resourcev1.CapacityRequestPolicy{Default: ptrQty("40Gi")}),
			},
			count:      1,
			wantCharge: "40Gi",
		},
		{
			name: "heterogeneous Defaults uses max",
			matched: []resourcev1.Device{
				makeDevice("gpu-0", "80Gi", &resourcev1.CapacityRequestPolicy{Default: ptrQty("40Gi")}),
				makeDevice("gpu-1", "100Gi", &resourcev1.CapacityRequestPolicy{Default: ptrQty("10Gi")}),
			},
			count:      1,
			wantCharge: "40Gi",
		},
		{
			name:        "no capacity dimension returns InternalError",
			matched:     []resourcev1.Device{{Name: "gpu-0"}},
			count:       1,
			wantErrType: field.ErrorTypeInternal,
		},
		{
			name: "all policies reject returns Invalid not InternalError",
			matched: []resourcev1.Device{
				makeDevice("gpu-0", "80Gi", &resourcev1.CapacityRequestPolicy{
					ValidValues: []resource.Quantity{resource.MustParse("10Gi"), resource.MustParse("20Gi")},
				}),
				makeDevice("gpu-1", "80Gi", &resourcev1.CapacityRequestPolicy{
					ValidValues: []resource.Quantity{resource.MustParse("10Gi"), resource.MustParse("30Gi")},
				}),
			},
			count:           1,
			explicitRequest: ptrQty("50Gi"),
			wantErrType:     field.ErrorTypeInvalid,
		},
		{
			name: "mixed devices some without dimension all with-dimension reject",
			matched: []resourcev1.Device{
				makeDevice("gpu-0", "80Gi", &resourcev1.CapacityRequestPolicy{
					ValidValues: []resource.Quantity{resource.MustParse("10Gi")},
				}),
				{Name: "gpu-1"},
			},
			count:           1,
			explicitRequest: ptrQty("50Gi"),
			wantErrType:     field.ErrorTypeInvalid,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &deviceClassCapacityConfig{
				driver:       "gpu.example.com",
				resourceName: "memory",
			}
			result, errs := computeCapacityCharge(log, cfg, tc.matched, tc.count, tc.explicitRequest, reqPath)
			if tc.wantErrType != "" {
				if len(errs) == 0 {
					t.Fatalf("expected error type %s, got no error (result=%v)", tc.wantErrType, result)
				}
				if errs[0].Type != tc.wantErrType {
					t.Errorf("expected error type %s, got %s: %s", tc.wantErrType, errs[0].Type, errs[0].Detail)
				}
				return
			}
			if len(errs) > 0 {
				t.Fatalf("unexpected error: %v", errs)
			}
			if result == nil {
				t.Fatal("expected non-nil result")
			}
			want := resource.MustParse(tc.wantCharge)
			if result.Cmp(want) != 0 {
				t.Errorf("got %s, want %s", result.String(), tc.wantCharge)
			}
		})
	}
}
