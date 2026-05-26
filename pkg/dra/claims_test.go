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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func Test_GetResourceRequests(t *testing.T) {
	tmpl := utiltesting.MakeResourceClaimTemplate("claim-tmpl-1", "ns1").
		DeviceRequest("device-request", "test-deviceclass-1", 2).
		Obj()

	claim := utiltesting.MakeResourceClaim("claim-2", "ns1").
		DeviceRequest("device-request", "test-deviceclass-2", 1).
		Obj()

	wl := &kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "wl", Namespace: "ns1"},
		Spec: kueue.WorkloadSpec{
			PodSets: []kueue.PodSet{{
				Name:  "main",
				Count: 1,
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "c", Image: "pause"}},
						ResourceClaims: []corev1.PodResourceClaim{
							{Name: "req-1", ResourceClaimTemplateName: new("claim-tmpl-1")},
							{Name: "req-2", ResourceClaimName: new("claim-2")},
						},
					},
				},
			}},
		},
	}

	// Common lookup functions for reuse across test cases
	defaultLookup := func(dc corev1.ResourceName) (corev1.ResourceName, bool) {
		if dc == "test-deviceclass-1" {
			return "res-1", true
		}
		return "", false
	}

	noLookup := func(corev1.ResourceName) (corev1.ResourceName, bool) {
		return "", false
	}

	tests := []struct {
		name         string
		modifyWL     func(w *kueue.Workload)
		extraObjects []runtime.Object
		lookup       func(corev1.ResourceName) (corev1.ResourceName, bool)
		want         map[kueue.PodSetReference]corev1.ResourceList
		wantErr      field.ErrorList
	}{
		{
			name: "Single claim template with single device",
			lookup: func(dc corev1.ResourceName) (corev1.ResourceName, bool) {
				m := map[corev1.ResourceName]corev1.ResourceName{
					"test-deviceclass-1": "res-1",
					"test-deviceclass-2": "res-2",
				}
				lr, ok := m[dc]
				return lr, ok
			},
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {
					"res-1": resource.MustParse("2"),
				},
			},
		},
		{
			name:   "Unmapped DeviceClass returns error",
			lookup: noLookup,
			wantErr: field.ErrorList{
				field.NotFound(field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("resourceClaimTemplateName"), ""),
			},
		},
		{
			name: "Two containers each using different claim templates",
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.Containers = []corev1.Container{
					{
						Name:  "c1",
						Image: "pause",
						Resources: corev1.ResourceRequirements{
							Claims: []corev1.ResourceClaim{{Name: "req-a"}},
						},
					},
					{
						Name:  "c2",
						Image: "pause",
						Resources: corev1.ResourceRequirements{
							Claims: []corev1.ResourceClaim{{Name: "req-b"}},
						},
					},
				}
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-a", ResourceClaimTemplateName: new("claim-tmpl-1")},
					{Name: "req-b", ResourceClaimTemplateName: new("claim-tmpl-2")},
				}
			},
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-2", "ns1").
					DeviceRequest("device-request", "test-deviceclass-2", 1).
					Obj(),
			},
			lookup: func(dc corev1.ResourceName) (corev1.ResourceName, bool) {
				m := map[corev1.ResourceName]corev1.ResourceName{"test-deviceclass-1": "res-1", "test-deviceclass-2": "res-2"}
				lr, ok := m[dc]
				return lr, ok
			},
			want: map[kueue.PodSetReference]corev1.ResourceList{"main": {"res-1": resource.MustParse("2"), "res-2": resource.MustParse("1")}},
		},
		{
			name: "Two containers sharing one claim template",
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.Containers = []corev1.Container{
					{
						Name:  "c1",
						Image: "pause",
						Resources: corev1.ResourceRequirements{
							Claims: []corev1.ResourceClaim{{Name: "req-a"}},
						},
					},
					{
						Name:  "c2",
						Image: "pause",
						Resources: corev1.ResourceRequirements{
							Claims: []corev1.ResourceClaim{{Name: "req-a"}},
						},
					},
				}
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-a", ResourceClaimTemplateName: new("claim-tmpl-1")},
				}
			},
			lookup: defaultLookup,
			want:   map[kueue.PodSetReference]corev1.ResourceList{"main": {"res-1": resource.MustParse("2")}},
		},
		{
			name: "Single template requesting two devices",
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-3", "ns1").
					DeviceRequest("device-request", "test-deviceclass-1", 2).
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.Containers = []corev1.Container{
					{
						Name:  "c",
						Image: "pause",
						Resources: corev1.ResourceRequirements{
							Claims: []corev1.ResourceClaim{{Name: "req-x"}},
						},
					},
				}
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-x", ResourceClaimTemplateName: new("claim-tmpl-3")},
				}
			},
			lookup: defaultLookup,
			want:   map[kueue.PodSetReference]corev1.ResourceList{"main": {"res-1": resource.MustParse("2")}},
		},
		{
			name: "Init and regular container sharing one template",
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.InitContainers = []corev1.Container{
					{
						Name:  "init",
						Image: "pause",
						Resources: corev1.ResourceRequirements{
							Claims: []corev1.ResourceClaim{{Name: "rc"}},
						},
					},
				}
				w.Spec.PodSets[0].Template.Spec.Containers = []corev1.Container{
					{
						Name:  "main",
						Image: "pause",
						Resources: corev1.ResourceRequirements{
							Claims: []corev1.ResourceClaim{{Name: "rc"}},
						},
					},
				}
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "rc", ResourceClaimTemplateName: new("claim-tmpl-1")},
				}
			},
			lookup: defaultLookup,
			want:   map[kueue.PodSetReference]corev1.ResourceList{"main": {"res-1": resource.MustParse("2")}},
		},
		{
			name: "AllocationMode All returns error",
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-all", "ns1").
					DeviceRequest("device-request", "test-deviceclass-1", 0).
					AllocationModeAll().
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-all", ResourceClaimTemplateName: new("claim-tmpl-all")},
				}
			},
			lookup: defaultLookup,
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0).Child("exactly", "allocationMode"),
					"",
					"",
				),
			},
		},
		{
			name: "CEL selectors with DeviceClassName counts correctly",
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-cel", "ns1").
					DeviceRequest("req", "test-deviceclass-1", 1).
					WithCELSelectors("device.driver == \"test-driver\"").
					Obj(),
				&resourcev1.DeviceClass{
					ObjectMeta: metav1.ObjectMeta{Name: "test-deviceclass-1"},
				},
				&resourcev1.ResourceSlice{
					ObjectMeta: metav1.ObjectMeta{Name: "slice-1"},
					Spec: resourcev1.ResourceSliceSpec{
						Driver: "test-driver",
						Pool:   resourcev1.ResourcePool{Name: "pool-1", Generation: 1, ResourceSliceCount: 1},
						Devices: []resourcev1.Device{
							{Name: "dev-0"},
							{Name: "dev-1"},
						},
					},
				},
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-cel", ResourceClaimTemplateName: new("claim-tmpl-cel")},
				}
			},
			lookup: defaultLookup,
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {
					"res-1": resource.MustParse("1"),
				},
			},
		},
		{
			name: "CEL selectors pre-filtered by DeviceClass selectors",
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-cel-filtered", "ns1").
					DeviceRequest("req", "gpu-class", 1).
					WithCELSelectors("device.driver == \"gpu-driver\"").
					Obj(),
				&resourcev1.DeviceClass{
					ObjectMeta: metav1.ObjectMeta{Name: "gpu-class"},
					Spec: resourcev1.DeviceClassSpec{
						Selectors: []resourcev1.DeviceSelector{
							{CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == \"gpu-driver\""}},
						},
					},
				},
				&resourcev1.ResourceSlice{
					ObjectMeta: metav1.ObjectMeta{Name: "gpu-slice"},
					Spec: resourcev1.ResourceSliceSpec{
						Driver: "gpu-driver",
						Pool:   resourcev1.ResourcePool{Name: "gpu-pool", Generation: 1, ResourceSliceCount: 1},
						Devices: []resourcev1.Device{
							{Name: "gpu-0"},
						},
					},
				},
				&resourcev1.ResourceSlice{
					ObjectMeta: metav1.ObjectMeta{Name: "nic-slice"},
					Spec: resourcev1.ResourceSliceSpec{
						Driver: "nic-driver",
						Pool:   resourcev1.ResourcePool{Name: "nic-pool", Generation: 1, ResourceSliceCount: 1},
						Devices: []resourcev1.Device{
							{Name: "nic-0"},
							{Name: "nic-1"},
							{Name: "nic-2"},
						},
					},
				},
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-filtered", ResourceClaimTemplateName: new("claim-tmpl-cel-filtered")},
				}
			},
			lookup: func(dc corev1.ResourceName) (corev1.ResourceName, bool) {
				if dc == "gpu-class" {
					return "res-1", true
				}
				return "", false
			},
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {
					"res-1": resource.MustParse("1"),
				},
			},
		},
		{
			name: "CEL selectors with unsatisfiable expression returns error",
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-unsat", "ns1").
					DeviceRequest("req", "test-deviceclass-1", 2).
					WithCELSelectors("device.driver == \"nonexistent-driver\"").
					Obj(),
				&resourcev1.DeviceClass{
					ObjectMeta: metav1.ObjectMeta{Name: "test-deviceclass-1"},
				},
				&resourcev1.ResourceSlice{
					ObjectMeta: metav1.ObjectMeta{Name: "slice-2"},
					Spec: resourcev1.ResourceSliceSpec{
						Driver: "test-driver",
						Pool:   resourcev1.ResourcePool{Name: "pool-1", Generation: 1, ResourceSliceCount: 1},
						Devices: []resourcev1.Device{
							{Name: "dev-0"},
						},
					},
				},
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-unsat", ResourceClaimTemplateName: new("claim-tmpl-unsat")},
				}
			},
			lookup: defaultLookup,
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0).Child("exactly", "selectors"),
					"",
					"",
				),
			},
		},
		{
			name: "CEL selectors with insufficient matching devices returns error",
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-insuf", "ns1").
					DeviceRequest("req", "test-deviceclass-1", 3).
					WithCELSelectors("device.driver == \"test-driver\"").
					Obj(),
				&resourcev1.DeviceClass{
					ObjectMeta: metav1.ObjectMeta{Name: "test-deviceclass-1"},
				},
				&resourcev1.ResourceSlice{
					ObjectMeta: metav1.ObjectMeta{Name: "slice-3"},
					Spec: resourcev1.ResourceSliceSpec{
						Driver: "test-driver",
						Pool:   resourcev1.ResourcePool{Name: "pool-1", Generation: 1, ResourceSliceCount: 1},
						Devices: []resourcev1.Device{
							{Name: "dev-0"},
							{Name: "dev-1"},
						},
					},
				},
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-insuf", ResourceClaimTemplateName: new("claim-tmpl-insuf")},
				}
			},
			lookup: defaultLookup,
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0).Child("exactly", "selectors"),
					"",
					"",
				),
			},
		},
		{
			name: "Multi-request CEL selectors do not double-count devices",
			extraObjects: []runtime.Object{
				// Two requests each wanting 1 device, but only 1 device exists.
				&resourcev1.ResourceClaimTemplate{
					ObjectMeta: metav1.ObjectMeta{Name: "claim-tmpl-multi", Namespace: "ns1"},
					Spec: resourcev1.ResourceClaimTemplateSpec{
						Spec: resourcev1.ResourceClaimSpec{
							Devices: resourcev1.DeviceClaim{
								Requests: []resourcev1.DeviceRequest{
									{
										Name: "req-a",
										Exactly: &resourcev1.ExactDeviceRequest{
											DeviceClassName: "test-deviceclass-1",
											AllocationMode:  resourcev1.DeviceAllocationModeExactCount,
											Count:           1,
											Selectors: []resourcev1.DeviceSelector{
												{CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == \"test-driver\""}},
											},
										},
									},
									{
										Name: "req-b",
										Exactly: &resourcev1.ExactDeviceRequest{
											DeviceClassName: "test-deviceclass-1",
											AllocationMode:  resourcev1.DeviceAllocationModeExactCount,
											Count:           1,
											Selectors: []resourcev1.DeviceSelector{
												{CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == \"test-driver\""}},
											},
										},
									},
								},
							},
						},
					},
				},
				&resourcev1.DeviceClass{
					ObjectMeta: metav1.ObjectMeta{Name: "test-deviceclass-1"},
				},
				&resourcev1.ResourceSlice{
					ObjectMeta: metav1.ObjectMeta{Name: "slice-multi"},
					Spec: resourcev1.ResourceSliceSpec{
						Driver: "test-driver",
						Pool:   resourcev1.ResourcePool{Name: "pool-1", Generation: 1, ResourceSliceCount: 1},
						Devices: []resourcev1.Device{
							{Name: "dev-0"},
						},
					},
				},
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-multi", ResourceClaimTemplateName: new("claim-tmpl-multi")},
				}
			},
			lookup: defaultLookup,
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(1).Child("exactly", "selectors"),
					"",
					"",
				),
			},
		},
		{
			name: "Invalid CEL selector returns error",
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-badcel", "ns1").
					DeviceRequest("req", "test-deviceclass-1", 1).
					WithCELSelectors("this is not valid CEL!!!").
					Obj(),
				&resourcev1.DeviceClass{
					ObjectMeta: metav1.ObjectMeta{Name: "test-deviceclass-1"},
				},
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-badcel", ResourceClaimTemplateName: new("claim-tmpl-badcel")},
				}
			},
			lookup: defaultLookup,
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0).Child("exactly", "selectors"),
					"",
					"",
				),
			},
		},
		{
			name: "Device constraints returns error",
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-constraints", "ns1").
					DeviceRequest("gpu-1", "test-deviceclass-1", 1).
					DeviceRequest("gpu-2", "test-deviceclass-1", 1).
					WithDeviceConstraints([]string{"gpu-1", "gpu-2"}, "numa-node").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-constraints", ResourceClaimTemplateName: new("claim-tmpl-constraints")},
				}
			},
			lookup: defaultLookup,
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "constraints"), "", ""),
			},
		},
		{
			name: "FirstAvailable returns error",
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-first", "ns1").
					FirstAvailableRequest("req", "test-deviceclass-1").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-first", ResourceClaimTemplateName: new("claim-tmpl-first")},
				}
			},
			lookup: defaultLookup,
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0), "", ""),
			},
		},
		{
			name: "AdminAccess returns error",
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-admin", "ns1").
					DeviceRequest("req", "test-deviceclass-1", 1).
					WithAdminAccess(true).
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-admin", ResourceClaimTemplateName: new("claim-tmpl-admin")},
				}
			},
			lookup: defaultLookup,
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0).Child("exactly", "adminAccess"),
					"",
					"",
				),
			},
		},
		{
			name: "CEL selectors with nonexistent DeviceClass returns error",
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-noclass", "ns1").
					DeviceRequest("req", "nonexistent-class", 1).
					WithCELSelectors("device.driver == \"test-driver\"").
					Obj(),
				&resourcev1.ResourceSlice{
					ObjectMeta: metav1.ObjectMeta{Name: "slice-noclass"},
					Spec: resourcev1.ResourceSliceSpec{
						Driver: "test-driver",
						Pool:   resourcev1.ResourcePool{Name: "pool-1", Generation: 1, ResourceSliceCount: 1},
						Devices: []resourcev1.Device{
							{Name: "dev-0"},
						},
					},
				},
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-noclass", ResourceClaimTemplateName: new("claim-tmpl-noclass")},
				}
			},
			lookup: func(dc corev1.ResourceName) (corev1.ResourceName, bool) {
				return "res-1", true
			},
			wantErr: field.ErrorList{
				field.InternalError(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0),
					errors.New(""),
				),
			},
		},
		{
			name: "CEL selectors with empty DeviceClassName succeeds",
			extraObjects: []runtime.Object{
				&resourcev1.ResourceClaimTemplate{
					ObjectMeta: metav1.ObjectMeta{Name: "claim-tmpl-nodc", Namespace: "ns1"},
					Spec: resourcev1.ResourceClaimTemplateSpec{
						Spec: resourcev1.ResourceClaimSpec{
							Devices: resourcev1.DeviceClaim{
								Requests: []resourcev1.DeviceRequest{
									{
										Name: "req",
										Exactly: &resourcev1.ExactDeviceRequest{
											AllocationMode: resourcev1.DeviceAllocationModeExactCount,
											Count:          1,
											Selectors: []resourcev1.DeviceSelector{
												{CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == \"test-driver\""}},
											},
										},
									},
								},
							},
						},
					},
				},
				&resourcev1.ResourceSlice{
					ObjectMeta: metav1.ObjectMeta{Name: "slice-nodc"},
					Spec: resourcev1.ResourceSliceSpec{
						Driver: "test-driver",
						Pool:   resourcev1.ResourcePool{Name: "pool-1", Generation: 1, ResourceSliceCount: 1},
						Devices: []resourcev1.Device{
							{Name: "dev-0"},
						},
					},
				},
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-nodc", ResourceClaimTemplateName: new("claim-tmpl-nodc")},
				}
			},
			lookup: func(dc corev1.ResourceName) (corev1.ResourceName, bool) {
				return "res-1", true
			},
		},
		{
			name: "Device config returns error",
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-config", "ns1").
					DeviceRequest("req", "test-deviceclass-1", 1).
					WithDeviceConfig("req", "", nil).
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-config", ResourceClaimTemplateName: new("claim-tmpl-config")},
				}
			},
			lookup: defaultLookup,
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "config"), "", ""),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.lookup != nil {
				mappings := []configapi.DeviceClassMapping{
					{
						Name:             corev1.ResourceName("res-1"),
						DeviceClassNames: []corev1.ResourceName{"test-deviceclass-1", "gpu-class"},
					},
					{
						Name:             corev1.ResourceName("res-2"),
						DeviceClassNames: []corev1.ResourceName{"test-deviceclass-2"},
					},
				}
				if tc.name == "Unmapped DeviceClass returns error" {
					mappings = []configapi.DeviceClassMapping{}
				}
				err := CreateMapperFromConfiguration(mappings)
				if err != nil {
					t.Fatalf("Failed to initialize DRA mapper: %v", err)
				}
			}

			objs := []client.Object{tmpl, claim}
			if tc.extraObjects != nil {
				for _, o := range tc.extraObjects {
					objs = append(objs, o.(client.Object))
				}
			}
			baseClient := utiltesting.NewClientBuilder().WithObjects(objs...).Build()

			wlCopy := wl.DeepCopy()
			if tc.modifyWL != nil {
				tc.modifyWL(wlCopy)
			}

			ctx, _ := utiltesting.ContextWithLog(t)
			got, err := GetResourceRequestsForResourceClaimTemplates(ctx, baseClient, wlCopy)

			if diff := cmp.Diff(tc.wantErr, err, cmpopts.IgnoreFields(field.Error{}, "Detail", "BadValue")); diff != "" {
				t.Errorf("GetResourceRequestsForResourceClaimTemplates() error mismatch (-want +got):\n%s", diff)
			}

			if err == nil {
				if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("GetResourceRequestsForResourceClaimTemplates() result mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}
