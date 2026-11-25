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
	"math"
	"strings"
	"testing"

	"github.com/go-logr/logr/funcr"
	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation/field"
	dracel "k8s.io/dynamic-resource-allocation/cel"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingdra "sigs.k8s.io/kueue/pkg/util/testingjobs/dra"
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

func TestComputeCounterCharges(t *testing.T) {
	defaultCC := &deviceClassCounterConfig{
		driver:      "gpu.nvidia.com",
		counterName: "memory",
	}

	tests := map[string]struct {
		cc            *deviceClassCounterConfig
		quotaResource corev1.ResourceName
		matched       []resourcev1.Device
		count         int64
		want          corev1.ResourceList
	}{
		"single device, count=1": {
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
		"single device, count=3": {
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
		"multiple devices same value, MAX is same": {
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
		"multiple devices different values, MAX wins": {
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
		"counter not in config, skipped": {
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
		"multiple ConsumesCounters entries, MAX across counter sets": {
			cc:            defaultCC,
			quotaResource: "gpu.memory",
			matched: []resourcev1.Device{
				{
					Name: "multi-set-device",
					ConsumesCounters: []resourcev1.DeviceCounterConsumption{
						{
							CounterSet: "set-a",
							Counters: map[string]resourcev1.Counter{
								"memory": {Value: resource.MustParse("10Gi")},
							},
						},
						{
							CounterSet: "set-b",
							Counters: map[string]resourcev1.Counter{
								"memory": {Value: resource.MustParse("30Gi")},
							},
						},
					},
				},
			},
			count: 1,
			want: corev1.ResourceList{
				"gpu.memory": resource.MustParse("30Gi"),
			},
		},
		"no consumesCounters, no deviceSelector": {
			cc:            defaultCC,
			quotaResource: "gpu.memory",
			matched: []resourcev1.Device{
				makeDevice("whole-gpu-0", "", ""),
			},
			count: 1,
			want:  nil,
		},
		"count multiplication saturates instead of overflowing int64": {
			cc:            defaultCC,
			quotaResource: "gpu.memory",
			matched: []resourcev1.Device{
				makeDevice("big-counter-0", "big", "8000000000000000000"),
			},
			count: 2,
			want: corev1.ResourceList{
				"gpu.memory": *resource.NewQuantity(math.MaxInt64, resource.DecimalSI),
			},
		},
		// 1e19 > MaxInt64; Value() truncates it to 0, so without SafeValue
		// this device would silently charge nothing.
		"counter that Value() would truncate is clamped before multiply": {
			cc:            defaultCC,
			quotaResource: "gpu.memory",
			matched: []resourcev1.Device{
				makeDevice("truncating-0", "big", "10000000000000000000"),
			},
			count: 2,
			want: corev1.ResourceList{
				"gpu.memory": *resource.NewQuantity(math.MaxInt64, resource.DecimalSI),
			},
		},
		"counter above int64 is clamped at count=1": {
			cc:            defaultCC,
			quotaResource: "gpu.memory",
			matched: []resourcev1.Device{
				makeDevice("overmax-0", "big", "9223372036854775808"),
			},
			count: 1,
			want: corev1.ResourceList{
				"gpu.memory": *resource.NewQuantity(math.MaxInt64, resource.DecimalSI),
			},
		},
		"negative counter is clamped to zero": {
			cc:            defaultCC,
			quotaResource: "gpu.memory",
			matched: []resourcev1.Device{
				makeDevice("negative-0", "big", "-5"),
			},
			count: 2,
			want: corev1.ResourceList{
				"gpu.memory": *resource.NewQuantity(0, resource.DecimalSI),
			},
		},
		// count == 1 charges the counter value through Value(), which rounds a
		// fractional quantity up away from zero. This is conservative and
		// consistent with the count > 1 path; pin it so it cannot silently change.
		"fractional counter rounds up at count=1": {
			cc:            defaultCC,
			quotaResource: "gpu.memory",
			matched: []resourcev1.Device{
				makeDevice("fractional-0", "small", "500m"),
			},
			count: 1,
			want: corev1.ResourceList{
				"gpu.memory": *resource.NewQuantity(1, resource.DecimalSI),
			},
		},
		"negative counter is clamped to zero at count=1": {
			cc:            defaultCC,
			quotaResource: "gpu.memory",
			matched: []resourcev1.Device{
				makeDevice("negative-count1-0", "big", "-5"),
			},
			count: 1,
			want: corev1.ResourceList{
				"gpu.memory": *resource.NewQuantity(0, resource.DecimalSI),
			},
		},
		// A fractional negative like -500m (Value() -1, Sign() -1) is clamped to
		// 0 like any other negative; pin that it charges 0.
		"sub-integer negative counter is clamped to zero": {
			cc:            defaultCC,
			quotaResource: "gpu.memory",
			matched: []resourcev1.Device{
				makeDevice("negative-fractional-0", "small", "-500m"),
			},
			count: 1,
			want: corev1.ResourceList{
				"gpu.memory": *resource.NewQuantity(0, resource.DecimalSI),
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			got := computeCounterCharges(log, tc.cc, tc.quotaResource, tc.matched, tc.count)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("computeCounterCharges() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestComputeCounterChargesLogsNegativeCounter pins that a negative driver
// counter is not only clamped to zero but also surfaced through the logger, so
// a misbehaving driver leaves an operator-visible trace instead of a silent
// clamp.
func TestComputeCounterChargesLogsNegativeCounter(t *testing.T) {
	cc := &deviceClassCounterConfig{
		driver:      "gpu.nvidia.com",
		counterName: "memory",
	}

	// Verbosity 1 together with the "level"=1 check below pins that the clamp is
	// logged at V(1) as a controller-local anomaly with a safe fallback, visible
	// when anomaly/lifecycle logs are enabled.
	var logged []string
	logger := funcr.New(
		func(_, args string) { logged = append(logged, args) },
		funcr.Options{Verbosity: 1},
	)

	got := computeCounterCharges(logger, cc, "gpu.memory", []resourcev1.Device{
		makeDevice("negative-0", "big", "-5"),
	}, 1)

	want := corev1.ResourceList{
		"gpu.memory": *resource.NewQuantity(0, resource.DecimalSI),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("computeCounterCharges() mismatch (-want +got):\n%s", diff)
	}

	if len(logged) == 0 {
		t.Fatalf("expected a V(1) log line for the negative counter, got none")
	}
	joined := strings.Join(logged, "\n")
	for _, substr := range []string{`"level"=1`, "Unexpected negative device value from driver, clamping to 0", "gpu.nvidia.com", "memory", "-5"} {
		if !strings.Contains(joined, substr) {
			t.Errorf("expected log output to contain %q, got:\n%s", substr, joined)
		}
	}
}

func TestSelectorErrorPaths(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	cl := utiltesting.NewClientBuilder().WithObjects(testingdra.MakeDeviceClass("valid-device-class").Obj()).Build()
	claimPath := field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0)
	reqPath := claimPath.Child("devices", "requests").Index(1)

	cache := dracel.NewCache(1, dracel.Features{})
	validSelector := cache.GetOrCompile("device.driver == 'gpu.example.com'")
	if validSelector.Error != nil {
		t.Fatalf("CEL compilation failed: %v", validSelector.Error)
	}

	slices := []*resourcev1.ResourceSlice{
		utiltesting.MakeResourceSlice("slice-1", "gpu.example.com").
			Pool("pool1", 1, 1).
			Device("gpu-0").
			Obj(),
	}
	// An attribute without a value makes the CEL evaluation fail.
	slices[0].Spec.Devices[0].Attributes["gpu.example.com/type"] = resourcev1.DeviceAttribute{}

	tests := map[string]struct {
		run        func() field.ErrorList
		wantField  string
		wantDetail string
	}{
		"DeviceClass lookup error": {
			run: func() field.ErrorList {
				_, _, errs := prepareDeviceSelectors(ctx, cl, "missing-device-class", nil, make(map[string]*resourcev1.DeviceClass), claimPath, 1)
				return errs
			},
			wantField: reqPath.String(),
		},
		"request selector compilation error": {
			run: func() field.ErrorList {
				_, _, errs := prepareDeviceSelectors(
					ctx,
					cl,
					"valid-device-class",
					[]resourcev1.DeviceSelector{{CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver =="}}},
					make(map[string]*resourcev1.DeviceClass),
					claimPath,
					1,
				)
				return errs
			},
			wantField: reqPath.Child("exactly", "selectors").Index(0).Child("cel", "expression").String(),
		},
		"request selector evaluation error": {
			run: func() field.ErrorList {
				_, errs := matchDevicesWithSelectors(ctx, slices, []dracel.CompilationResult{validSelector}, nil, nil, reqPath)
				return errs
			},
			wantField:  reqPath.String(),
			wantDetail: "unsupported attribute value",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			errs := tc.run()
			if len(errs) != 1 {
				t.Fatalf("expected one error, got %d: %v", len(errs), errs)
			}
			if errs[0].Field != tc.wantField {
				t.Errorf("field path = %q, want %q", errs[0].Field, tc.wantField)
			}
			if tc.wantDetail != "" && !strings.Contains(errs[0].Detail, tc.wantDetail) {
				t.Errorf("error detail = %q, want it to contain %q", errs[0].Detail, tc.wantDetail)
			}
		})
	}
}
