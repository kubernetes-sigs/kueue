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
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
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
							{Name: "req-1", ResourceClaimTemplateName: ptr.To("claim-tmpl-1")},
							{Name: "req-2", ResourceClaimName: ptr.To("claim-2")},
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
					{Name: "req-a", ResourceClaimTemplateName: ptr.To("claim-tmpl-1")},
					{Name: "req-b", ResourceClaimTemplateName: ptr.To("claim-tmpl-2")},
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
					{Name: "req-a", ResourceClaimTemplateName: ptr.To("claim-tmpl-1")},
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
					{Name: "req-x", ResourceClaimTemplateName: ptr.To("claim-tmpl-3")},
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
					{Name: "rc", ResourceClaimTemplateName: ptr.To("claim-tmpl-1")},
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
					{Name: "req-all", ResourceClaimTemplateName: ptr.To("claim-tmpl-all")},
				}
			},
			lookup: defaultLookup,
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0).Child("exactly", "allocationMode"), "", ""),
			},
		},
		{
			name: "CEL selectors returns error",
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-cel", "ns1").
					DeviceRequest("req", "test-deviceclass-1", 1).
					WithCELSelectors("device.driver == \"test-driver\"").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-cel", ResourceClaimTemplateName: ptr.To("claim-tmpl-cel")},
				}
			},
			lookup: defaultLookup,
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0).Child("exactly", "selectors"), "", ""),
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
					{Name: "req-constraints", ResourceClaimTemplateName: ptr.To("claim-tmpl-constraints")},
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
					{Name: "req-first", ResourceClaimTemplateName: ptr.To("claim-tmpl-first")},
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
					{Name: "req-admin", ResourceClaimTemplateName: ptr.To("claim-tmpl-admin")},
				}
			},
			lookup: defaultLookup,
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0).Child("exactly", "adminAccess"), "", ""),
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
					{Name: "req-config", ResourceClaimTemplateName: ptr.To("claim-tmpl-config")},
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
						DeviceClassNames: []corev1.ResourceName{"test-deviceclass-1"},
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
				if !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("unexpected result; got=%v want=%v", got, tc.want)
				}
			}
		})
	}
}
