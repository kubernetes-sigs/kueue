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
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingdra "sigs.k8s.io/kueue/pkg/util/testingjobs/dra"
)

func TestEligibleSlices(t *testing.T) {
	const driver = "gpu.example.com"

	tests := []struct {
		name   string
		slices []*resourcev1.ResourceSlice
		want   []string
	}{
		{
			name: "complete pool",
			slices: []*resourcev1.ResourceSlice{
				testingdra.MakeResourceSlice("slice-1", driver).Pool("pool-1", 1, 2).Device("dev-0").Obj(),
				testingdra.MakeResourceSlice("slice-2", driver).Pool("pool-1", 1, 2).Device("dev-1").Obj(),
			},
			want: []string{"slice-1", "slice-2"},
		},
		{
			name: "incomplete pool",
			slices: []*resourcev1.ResourceSlice{
				testingdra.MakeResourceSlice("slice-1", driver).Pool("pool-1", 1, 2).Device("dev-0").Obj(),
			},
		},
		{
			name: "slices without devices count towards completeness",
			slices: []*resourcev1.ResourceSlice{
				testingdra.MakeResourceSlice("slice-1", driver).Pool("pool-1", 1, 2).Device("dev-0").Obj(),
				testingdra.MakeResourceSlice("slice-2", driver).Pool("pool-1", 1, 2).Obj(),
			},
			want: []string{"slice-1", "slice-2"},
		},
		{
			name: "higher generation replaces older slices in any listing order",
			slices: []*resourcev1.ResourceSlice{
				testingdra.MakeResourceSlice("old-1", driver).Pool("pool-1", 1, 1).Device("dev-0").Obj(),
				testingdra.MakeResourceSlice("new-1", driver).Pool("pool-1", 2, 1).Device("dev-0").Obj(),
				testingdra.MakeResourceSlice("old-2", driver).Pool("pool-1", 1, 1).Device("dev-0").Obj(),
			},
			want: []string{"new-1"},
		},
		{
			name: "incomplete latest generation does not fall back to the older one",
			slices: []*resourcev1.ResourceSlice{
				testingdra.MakeResourceSlice("old-1", driver).Pool("pool-1", 1, 1).Device("dev-0").Obj(),
				testingdra.MakeResourceSlice("new-1", driver).Pool("pool-1", 2, 2).Device("dev-0").Obj(),
			},
		},
		{
			name: "identically named pools from different drivers are separate",
			slices: []*resourcev1.ResourceSlice{
				testingdra.MakeResourceSlice("slice-1", driver).Pool("pool-1", 1, 1).Device("dev-0").Obj(),
				testingdra.MakeResourceSlice("slice-2", "other.example.com").Pool("pool-1", 2, 1).Device("dev-0").Obj(),
			},
			want: []string{"slice-1", "slice-2"},
		},
		{
			name: "listing order is kept across pools",
			slices: []*resourcev1.ResourceSlice{
				testingdra.MakeResourceSlice("z-slice", driver).Pool("pool-2", 1, 1).Device("dev-0").Obj(),
				testingdra.MakeResourceSlice("a-slice", driver).Pool("pool-1", 1, 1).Device("dev-0").Obj(),
			},
			want: []string{"z-slice", "a-slice"},
		},
		{
			name: "duplicate device names in one slice make the pool invalid",
			slices: []*resourcev1.ResourceSlice{
				testingdra.MakeResourceSlice("slice-1", driver).Pool("pool-1", 1, 1).Device("dev-0").Device("dev-0").Obj(),
			},
		},
		{
			name: "duplicate device names across slices make the pool invalid",
			slices: []*resourcev1.ResourceSlice{
				testingdra.MakeResourceSlice("slice-1", driver).Pool("pool-1", 1, 2).Device("dev-0").Obj(),
				testingdra.MakeResourceSlice("slice-2", driver).Pool("pool-1", 1, 2).Device("dev-0").Obj(),
			},
		},
		{
			name: "duplicate device names in a stale generation are ignored",
			slices: []*resourcev1.ResourceSlice{
				testingdra.MakeResourceSlice("old-1", driver).Pool("pool-1", 1, 1).Device("dev-0").Device("dev-0").Obj(),
				testingdra.MakeResourceSlice("new-1", driver).Pool("pool-1", 2, 1).Device("dev-0").Obj(),
			},
			want: []string{"new-1"},
		},
		{
			name: "devices consuming a published counter set are valid",
			slices: []*resourcev1.ResourceSlice{
				testingdra.MakeResourceSlice("counters", driver).Pool("pool-1", 1, 2).
					CounterSet("gpu-0", "memory", "40Gi").CounterSet("gpu-0", "multiprocessors", "98").
					Obj(),
				testingdra.MakeResourceSlice("devices", driver).Pool("pool-1", 1, 2).
					Device("mig-0").
					CounterConsumption("gpu-0", "memory", "10Gi").
					Obj(),
			},
			want: []string{"counters", "devices"},
		},
		{
			name: "duplicate counter set names make the pool invalid",
			slices: []*resourcev1.ResourceSlice{
				testingdra.MakeResourceSlice("counters-1", driver).Pool("pool-1", 1, 2).CounterSet("gpu-0", "memory", "40Gi").Obj(),
				testingdra.MakeResourceSlice("counters-2", driver).Pool("pool-1", 1, 2).CounterSet("gpu-0", "memory", "40Gi").Obj(),
			},
		},
		{
			name: "consuming an unknown counter set makes the pool invalid",
			slices: []*resourcev1.ResourceSlice{
				testingdra.MakeResourceSlice("devices", driver).Pool("pool-1", 1, 1).
					Device("mig-0").
					CounterConsumption("gpu-0", "memory", "10Gi").
					Obj(),
			},
		},
		{
			name: "consuming an unknown counter makes the pool invalid",
			slices: []*resourcev1.ResourceSlice{
				testingdra.MakeResourceSlice("counters", driver).Pool("pool-1", 1, 2).CounterSet("gpu-0", "memory", "40Gi").Obj(),
				testingdra.MakeResourceSlice("devices", driver).Pool("pool-1", 1, 2).
					Device("mig-0").
					CounterConsumption("gpu-0", "multiprocessors", "14").
					Obj(),
			},
		},
		{
			name: "an invalid pool does not affect other pools",
			slices: []*resourcev1.ResourceSlice{
				testingdra.MakeResourceSlice("invalid", driver).Pool("pool-1", 1, 1).Device("dev-0").Device("dev-0").Obj(),
				testingdra.MakeResourceSlice("valid", driver).Pool("pool-2", 1, 1).Device("dev-0").Obj(),
			},
			want: []string{"valid"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			slices := make([]resourcev1.ResourceSlice, 0, len(tc.slices))
			for _, slice := range tc.slices {
				slices = append(slices, *slice)
			}

			var got []string
			for _, slice := range eligibleSlices(ctx, slices) {
				got = append(got, slice.Name)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("eligibleSlices() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestCounterAndCapacityChargesSkipIneligiblePools(t *testing.T) {
	const (
		driver      = "gpu.example.com"
		deviceClass = "gpu-class"
	)
	reqPath := field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0)
	allDevices := resourcev1.DeviceSelector{CEL: &resourcev1.CELDeviceSelector{Expression: "true"}}
	counterMapping := configapi.DeviceClassMapping{
		Name:             "gpu-memory",
		DeviceClassNames: []corev1.ResourceName{deviceClass},
		Sources: []configapi.DeviceClassSourceConfig{{
			Counter: &configapi.DeviceClassCounterSource{Name: "memory", Driver: driver, DeviceSelector: allDevices},
		}},
	}
	capacityMapping := configapi.DeviceClassMapping{
		Name:             "gpu-memory",
		DeviceClassNames: []corev1.ResourceName{deviceClass},
		Sources: []configapi.DeviceClassSourceConfig{{
			Capacity: &configapi.DeviceClassCapacitySource{Name: "memory", Driver: driver, DeviceSelector: allDevices},
		}},
	}
	type chargeFunc func(*testing.T, client.Client, *ResourceMapper, *kueue.Workload) (map[kueue.PodSetReference]corev1.ResourceList, field.ErrorList)
	counterCharges := func(t *testing.T, cl client.Client, mapper *ResourceMapper, wl *kueue.Workload) (map[kueue.PodSetReference]corev1.ResourceList, field.ErrorList) {
		ctx, _ := utiltesting.ContextWithLog(t)
		return GetCounterResourcesForWorkload(ctx, cl, NewResourceSliceCache(cl), mapper, wl)
	}
	capacityCharges := func(t *testing.T, cl client.Client, mapper *ResourceMapper, wl *kueue.Workload) (map[kueue.PodSetReference]corev1.ResourceList, field.ErrorList) {
		ctx, _ := utiltesting.ContextWithLog(t)
		return DetermineCapacityResourcesForWorkload(ctx, cl, NewResourceSliceCache(cl), mapper, wl)
	}

	tests := []struct {
		name    string
		charges chargeFunc
		mapping configapi.DeviceClassMapping
		slices  []*resourcev1.ResourceSlice
		want    map[kueue.PodSetReference]corev1.ResourceList
		wantErr field.ErrorList
	}{
		{
			name:    "counter charge ignores a pool whose devices consume an unpublished counter set",
			charges: counterCharges,
			mapping: counterMapping,
			slices: []*resourcev1.ResourceSlice{
				testingdra.MakeResourceSlice("valid-counters", driver).Pool("valid", 1, 2).CounterSet("gpu-0", "memory", "40Gi").Obj(),
				testingdra.MakeResourceSlice("valid-devices", driver).Pool("valid", 1, 2).
					Device("dev-0").CounterConsumption("gpu-0", "memory", "10Gi").Obj(),
				testingdra.MakeResourceSlice("invalid", driver).Pool("invalid", 1, 1).
					Device("dev-0").CounterConsumption("gpu-1", "memory", "20Gi").Obj(),
			},
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {"gpu-memory": resource.MustParse("10Gi")},
			},
		},
		{
			name:    "counter charge fails when only invalid pools match",
			charges: counterCharges,
			mapping: counterMapping,
			slices: []*resourcev1.ResourceSlice{
				testingdra.MakeResourceSlice("invalid", driver).Pool("invalid", 1, 1).
					Device("dev-0").CounterConsumption("gpu-1", "memory", "20Gi").Obj(),
			},
			wantErr: field.ErrorList{field.InternalError(reqPath, errors.New(""))},
		},
		{
			name:    "capacity charge ignores a pool with duplicate device names",
			charges: capacityCharges,
			mapping: capacityMapping,
			slices: []*resourcev1.ResourceSlice{
				testingdra.MakeResourceSlice("valid", driver).Pool("valid", 1, 1).
					Device("dev-0").DeviceCapacity("memory", "40Gi", nil).Obj(),
				testingdra.MakeResourceSlice("invalid", driver).Pool("invalid", 1, 1).
					Device("dev-0").DeviceCapacity("memory", "80Gi", nil).
					Device("dev-0").DeviceCapacity("memory", "80Gi", nil).Obj(),
			},
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {"gpu-memory": resource.MustParse("40Gi")},
			},
		},
		{
			name:    "capacity charge fails when only invalid pools match",
			charges: capacityCharges,
			mapping: capacityMapping,
			slices: []*resourcev1.ResourceSlice{
				testingdra.MakeResourceSlice("invalid", driver).Pool("invalid", 1, 1).
					Device("dev-0").DeviceCapacity("memory", "80Gi", nil).
					Device("dev-0").DeviceCapacity("memory", "80Gi", nil).Obj(),
			},
			wantErr: field.ErrorList{field.InternalError(reqPath, errors.New(""))},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mapper := NewResourceMapper()
			if err := mapper.PopulateFromConfiguration([]configapi.DeviceClassMapping{tc.mapping}); err != nil {
				t.Fatalf("Failed to initialize DRA mapper: %v", err)
			}
			objs := []client.Object{
				testingdra.MakeDeviceClass(deviceClass).Obj(),
				utiltesting.MakeResourceClaimTemplate("claim-tmpl", "ns1").DeviceRequest("gpu", deviceClass, 1).Obj(),
			}
			for _, slice := range tc.slices {
				objs = append(objs, slice)
			}
			cl := utiltesting.NewClientBuilder().
				WithIndex(&resourcev1.ResourceSlice{}, "spec.driver", func(obj client.Object) []string {
					return []string{obj.(*resourcev1.ResourceSlice).Spec.Driver}
				}).
				WithObjects(objs...).Build()
			wl := &kueue.Workload{
				Name: "wl", Namespace: "ns1",
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{{
						Name:  "main",
						Count: 1,
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								ResourceClaims: []corev1.PodResourceClaim{{Name: "gpu", ResourceClaimTemplateName: new("claim-tmpl")}},
							},
						},
					}},
				},
			}

			got, errs := tc.charges(t, cl, mapper, wl)
			if diff := cmp.Diff(tc.wantErr, errs, cmpopts.IgnoreFields(field.Error{}, "Detail", "BadValue")); diff != "" {
				t.Errorf("Unexpected errors (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected charges (-want,+got):\n%s", diff)
			}
		})
	}
}
