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
	"math"
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
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingdra "sigs.k8s.io/kueue/pkg/util/testingjobs/dra"
)

func Test_GetResourceRequests(t *testing.T) {
	tmpl := utiltesting.MakeResourceClaimTemplate("claim-tmpl-1", "ns1").
		DeviceRequest("device-request", "test-deviceclass-1", 2).
		Obj()

	celPoolError := field.ErrorList{field.InternalError(
		field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0).Child("exactly", "selectors"),
		errors.New(""),
	)}
	celPoolResources := map[kueue.PodSetReference]corev1.ResourceList{
		"main": {"res-1": resource.MustParse("2")},
	}

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

	tests := map[string]struct {
		modifyWL             func(w *kueue.Workload)
		extraObjects         []runtime.Object
		lookup               func(corev1.ResourceName) (corev1.ResourceName, bool)
		skipDeviceClassCheck bool
		want                 map[kueue.PodSetReference]corev1.ResourceList
		wantErr              field.ErrorList
	}{
		"Single claim template with single device": {
			extraObjects: []runtime.Object{testingdra.MakeDeviceClass("test-deviceclass-1").Obj()},
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
		"Unmapped DeviceClass returns error": {
			extraObjects: []runtime.Object{testingdra.MakeDeviceClass("test-deviceclass-1").Obj()},
			lookup:       noLookup,
			wantErr: field.ErrorList{
				field.NotFound(field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("resourceClaimTemplateName"), ""),
			},
		},
		"Two containers each using different claim templates": {
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
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-2").Obj(),
			},
			lookup: func(dc corev1.ResourceName) (corev1.ResourceName, bool) {
				m := map[corev1.ResourceName]corev1.ResourceName{"test-deviceclass-1": "res-1", "test-deviceclass-2": "res-2"}
				lr, ok := m[dc]
				return lr, ok
			},
			want: map[kueue.PodSetReference]corev1.ResourceList{"main": {"res-1": resource.MustParse("2"), "res-2": resource.MustParse("1")}},
		},
		"Two containers sharing one claim template": {
			extraObjects: []runtime.Object{testingdra.MakeDeviceClass("test-deviceclass-1").Obj()},
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
		"Single template requesting two devices": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-3", "ns1").
					DeviceRequest("device-request", "test-deviceclass-1", 2).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
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
		"Init and regular container sharing one template": {
			extraObjects: []runtime.Object{testingdra.MakeDeviceClass("test-deviceclass-1").Obj()},
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
		"AllocationMode All returns error": {
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
		"CEL selectors with DeviceClassName counts correctly": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-cel", "ns1").
					DeviceRequest("req", "test-deviceclass-1", 1).
					WithCELSelectors("device.driver == \"test-driver\"").
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
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
		"CEL pool validation excludes stale devices from a complete newer generation": {
			lookup: defaultLookup,
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-cel-pool", "ns1").
					DeviceRequest("device-request", "test-deviceclass-1", 2).
					WithCELSelectors("true").
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("a-old", "test-driver").
					Pool("pool-1", 1, 1).
					Device("old-0").
					Obj(),
				utiltesting.MakeResourceSlice("z-new", "test-driver").
					Pool("pool-1", 2, 1).
					Device("new-0").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-cel-pool", ResourceClaimTemplateName: new("claim-tmpl-cel-pool")},
				}
			},
			wantErr: celPoolError,
		},
		"CEL pool validation excludes stale slices listed after the latest generation": {
			lookup: defaultLookup,
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-cel-pool", "ns1").
					DeviceRequest("device-request", "test-deviceclass-1", 2).
					WithCELSelectors("true").
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("a-new", "test-driver").
					Pool("pool-1", 2, 1).
					Device("new-0").
					Obj(),
				utiltesting.MakeResourceSlice("z-old", "test-driver").
					Pool("pool-1", 1, 1).
					Device("old-0").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-cel-pool", ResourceClaimTemplateName: new("claim-tmpl-cel-pool")},
				}
			},
			wantErr: celPoolError,
		},
		"CEL pool validation excludes an incomplete latest generation and does not fall back": {
			lookup: defaultLookup,
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-cel-pool", "ns1").
					DeviceRequest("device-request", "test-deviceclass-1", 2).
					WithCELSelectors("true").
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("a-old", "test-driver").
					Pool("pool-1", 1, 1).
					Device("old-0").
					Device("old-1").
					Obj(),
				utiltesting.MakeResourceSlice("z-new", "test-driver").
					Pool("pool-1", 2, 2).
					Device("new-0").
					Device("new-1").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-cel-pool", ResourceClaimTemplateName: new("claim-tmpl-cel-pool")},
				}
			},
			wantErr: celPoolError,
		},
		"CEL pool validation excludes an incomplete pool without older slices": {
			lookup: defaultLookup,
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-cel-pool", "ns1").
					DeviceRequest("device-request", "test-deviceclass-1", 2).
					WithCELSelectors("true").
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("slice-1", "test-driver").
					Pool("pool-1", 1, 2).
					Device("dev-0").
					Device("dev-1").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-cel-pool", ResourceClaimTemplateName: new("claim-tmpl-cel-pool")},
				}
			},
			wantErr: celPoolError,
		},
		"CEL pool validation accepts all slices of the complete latest generation": {
			lookup: defaultLookup,
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-cel-pool", "ns1").
					DeviceRequest("device-request", "test-deviceclass-1", 2).
					WithCELSelectors("true").
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("a-old", "test-driver").
					Pool("pool-1", 1, 1).
					Device("old-0").
					Obj(),
				utiltesting.MakeResourceSlice("b-new", "test-driver").
					Pool("pool-1", 2, 2).
					Device("new-0").
					Obj(),
				utiltesting.MakeResourceSlice("c-new", "test-driver").
					Pool("pool-1", 2, 2).
					Device("new-1").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-cel-pool", ResourceClaimTemplateName: new("claim-tmpl-cel-pool")},
				}
			},
			want: celPoolResources,
		},
		"CEL pool validation keeps complete pools when another pool is incomplete": {
			lookup: defaultLookup,
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-cel-pool", "ns1").
					DeviceRequest("device-request", "test-deviceclass-1", 2).
					WithCELSelectors("true").
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("slice-1", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-0").
					Device("dev-1").
					Obj(),
				utiltesting.MakeResourceSlice("slice-2", "test-driver").
					Pool("pool-2", 2, 2).
					Device("other-0").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-cel-pool", ResourceClaimTemplateName: new("claim-tmpl-cel-pool")},
				}
			},
			want: celPoolResources,
		},
		"CEL pool validation treats identically named pools from different drivers independently": {
			lookup: defaultLookup,
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-cel-pool", "ns1").
					DeviceRequest("device-request", "test-deviceclass-1", 2).
					WithCELSelectors("true").
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("slice-1", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-0").
					Obj(),
				utiltesting.MakeResourceSlice("slice-2", "other-driver").
					Pool("pool-1", 2, 1).
					Device("dev-0").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-cel-pool", ResourceClaimTemplateName: new("claim-tmpl-cel-pool")},
				}
			},
			want: celPoolResources,
		},
		"CEL pool validation preserves device order for overlapping requests across pools": {
			lookup: defaultLookup,
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-cel-overlap", "ns1").
					DeviceRequest("any", "test-deviceclass-1", 1).
					WithCELSelectors("true").
					DeviceRequest("selected", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].selected == "true"`).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("a-unselected", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-0").
					Attribute("test-driver/selected", "false").
					Obj(),
				utiltesting.MakeResourceSlice("z-selected", "test-driver").
					Pool("pool-2", 1, 1).
					Device("dev-1").
					Attribute("test-driver/selected", "true").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-cel-overlap", ResourceClaimTemplateName: new("claim-tmpl-cel-overlap")},
				}
			},
			want: celPoolResources,
		},
		"CEL pool validation includes slices without devices in the completeness check": {
			lookup: defaultLookup,
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-cel-pool", "ns1").
					DeviceRequest("device-request", "test-deviceclass-1", 2).
					WithCELSelectors("true").
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("slice-1", "test-driver").
					Pool("pool-1", 1, 2).
					Device("dev-0").
					Device("dev-1").
					Obj(),
				utiltesting.MakeResourceSlice("slice-2", "test-driver").
					Pool("pool-1", 1, 2).
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-cel-pool", ResourceClaimTemplateName: new("claim-tmpl-cel-pool")},
				}
			},
			want: celPoolResources,
		},
		"CEL pool validation excludes a pool with duplicate device names": {
			lookup: defaultLookup,
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-cel-pool", "ns1").
					DeviceRequest("device-request", "test-deviceclass-1", 2).
					WithCELSelectors("true").
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("slice-1", "test-driver").
					Pool("pool-1", 1, 2).
					Device("dev-0").
					Obj(),
				utiltesting.MakeResourceSlice("slice-2", "test-driver").
					Pool("pool-1", 1, 2).
					Device("dev-0").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-cel-pool", ResourceClaimTemplateName: new("claim-tmpl-cel-pool")},
				}
			},
			wantErr: celPoolError,
		},
		"CEL pool validation excludes a pool whose devices consume an unknown counter set": {
			lookup: defaultLookup,
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-cel-pool", "ns1").
					DeviceRequest("device-request", "test-deviceclass-1", 2).
					WithCELSelectors("true").
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("slice-1", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-0").
					CounterConsumption("gpu-0", "memory", "10Gi").
					Device("dev-1").
					CounterConsumption("gpu-0", "memory", "10Gi").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-cel-pool", ResourceClaimTemplateName: new("claim-tmpl-cel-pool")},
				}
			},
			wantErr: celPoolError,
		},
		"CEL pool validation accepts a pool whose devices consume a published counter set": {
			lookup: defaultLookup,
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-cel-pool", "ns1").
					DeviceRequest("device-request", "test-deviceclass-1", 2).
					WithCELSelectors("true").
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("counters", "test-driver").
					Pool("pool-1", 1, 2).
					CounterSet("gpu-0", "memory", "40Gi").
					Obj(),
				utiltesting.MakeResourceSlice("devices", "test-driver").
					Pool("pool-1", 1, 2).
					Device("dev-0").
					CounterConsumption("gpu-0", "memory", "10Gi").
					Device("dev-1").
					CounterConsumption("gpu-0", "memory", "10Gi").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-cel-pool", ResourceClaimTemplateName: new("claim-tmpl-cel-pool")},
				}
			},
			want: celPoolResources,
		},
		"CEL pool validation keeps valid pools when another pool is invalid": {
			lookup: defaultLookup,
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-cel-pool", "ns1").
					DeviceRequest("device-request", "test-deviceclass-1", 2).
					WithCELSelectors("true").
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("slice-1", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dup-0").
					Device("dup-0").
					Obj(),
				utiltesting.MakeResourceSlice("slice-2", "test-driver").
					Pool("pool-2", 1, 1).
					Device("dev-0").
					Device("dev-1").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-cel-pool", ResourceClaimTemplateName: new("claim-tmpl-cel-pool")},
				}
			},
			want: celPoolResources,
		},
		"CEL selectors pre-filtered by DeviceClass selectors": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-cel-filtered", "ns1").
					DeviceRequest("req", "gpu-class", 1).
					WithCELSelectors("device.driver == \"gpu-driver\"").
					Obj(),
				testingdra.MakeDeviceClass("gpu-class").
					CELSelector("device.driver == \"gpu-driver\"").
					Obj(),
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
		"CEL selectors with unsatisfiable expression returns error": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-unsat", "ns1").
					DeviceRequest("req", "test-deviceclass-1", 2).
					WithCELSelectors("device.driver == \"nonexistent-driver\"").
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
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
				field.InternalError(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0).Child("exactly", "selectors"),
					errors.New(""),
				),
			},
		},
		"CEL selectors with unsatisfiable expression still return error when skipDeviceClassCheck is true": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-unsat-skip", "ns1").
					DeviceRequest("req", "test-deviceclass-1", 2).
					WithCELSelectors("device.driver == \"nonexistent-driver\"").
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
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
					{Name: "req-unsat-skip", ResourceClaimTemplateName: new("claim-tmpl-unsat-skip")},
				}
			},
			lookup:               defaultLookup,
			skipDeviceClassCheck: true,
			wantErr: field.ErrorList{
				field.InternalError(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0).Child("exactly", "selectors"),
					errors.New(""),
				),
			},
		},
		"CEL selector on allowMultipleAllocations matches a device that allows multiple allocations": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-shared", "ns1").
					DeviceRequest("req", "test-deviceclass-1", 1).
					WithCELSelectors("device.allowMultipleAllocations == true").
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("slice-shared", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-0").AllowMultipleAllocations(true).
					Device("dev-1").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-shared", ResourceClaimTemplateName: new("claim-tmpl-shared")},
				}
			},
			lookup: defaultLookup,
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {
					"res-1": resource.MustParse("1"),
				},
			},
		},
		"CEL selector on allowMultipleAllocations does not match a device that allows multiple allocations when negated": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-exclusive", "ns1").
					DeviceRequest("req", "test-deviceclass-1", 2).
					WithCELSelectors("!device.allowMultipleAllocations").
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("slice-exclusive", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-0").AllowMultipleAllocations(true).
					Device("dev-1").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-exclusive", ResourceClaimTemplateName: new("claim-tmpl-exclusive")},
				}
			},
			lookup: defaultLookup,
			wantErr: field.ErrorList{
				field.InternalError(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0).Child("exactly", "selectors"),
					errors.New(""),
				),
			},
		},
		"CEL selectors with insufficient matching devices returns error": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-insuf", "ns1").
					DeviceRequest("req", "test-deviceclass-1", 3).
					WithCELSelectors("device.driver == \"test-driver\"").
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
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
				field.InternalError(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0).Child("exactly", "selectors"),
					errors.New(""),
				),
			},
		},
		"Multi-request CEL selectors do not double-count devices": {
			extraObjects: []runtime.Object{
				// Two requests each wanting 1 device, but only 1 device exists.
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-multi", "ns1").
					DeviceRequest("req-a", "test-deviceclass-1", 1).
					WithCELSelectors("device.driver == \"test-driver\"").
					DeviceRequest("req-b", "test-deviceclass-1", 1).
					WithCELSelectors("device.driver == \"test-driver\"").
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
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
				field.InternalError(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(1).Child("exactly", "selectors"),
					errors.New(""),
				),
			},
		},
		"Multi-request CEL selectors move an earlier request to a device that a later request does not need": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-backtrack", "ns1").
					DeviceRequest("any", "test-deviceclass-1", 1).
					WithCELSelectors(`true`).
					DeviceRequest("only-a", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id == "a"`).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("slice-backtrack", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-a").
					Attribute("test-driver/id", "a").
					Device("dev-b").
					Attribute("test-driver/id", "b").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-backtrack", ResourceClaimTemplateName: new("claim-tmpl-backtrack")},
				}
			},
			lookup: defaultLookup,
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {"res-1": resource.MustParse("2")},
			},
		},
		"Multi-request CEL selectors choose another pair of devices for a request with count 2": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-backtrack", "ns1").
					DeviceRequest("any-two", "test-deviceclass-1", 2).
					WithCELSelectors(`true`).
					DeviceRequest("only-a", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id == "a"`).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("slice-backtrack", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-a").
					Attribute("test-driver/id", "a").
					Device("dev-b").
					Attribute("test-driver/id", "b").
					Device("dev-c").
					Attribute("test-driver/id", "c").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-backtrack", ResourceClaimTemplateName: new("claim-tmpl-backtrack")},
				}
			},
			lookup: defaultLookup,
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {"res-1": resource.MustParse("3")},
			},
		},
		"Multi-request CEL selectors are not evaluated for later requests until the earlier ones fit": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-backtrack", "ns1").
					DeviceRequest("not-c", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id != "c"`).
					DeviceRequest("only-a", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id == "a"`).
					DeviceRequest("value", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].value == "yes"`).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("slice-backtrack", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-a").
					Attribute("test-driver/id", "a").
					Device("dev-b").
					Attribute("test-driver/id", "b").
					Device("dev-c").
					Attribute("test-driver/id", "c").
					Attribute("test-driver/value", "yes").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-backtrack", ResourceClaimTemplateName: new("claim-tmpl-backtrack")},
				}
			},
			lookup: defaultLookup,
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {"res-1": resource.MustParse("3")},
			},
		},
		"Multi-request CEL selectors do not return an evaluation error on a device that the search does not reach": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-backtrack", "ns1").
					DeviceRequest("fails-on-c", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id == "c" ? device.attributes["test-driver"].missing == "yes" : true`).
					DeviceRequest("any", "test-deviceclass-1", 1).
					WithCELSelectors(`true`).
					DeviceRequest("only-a", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id == "a"`).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("slice-backtrack", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-a").
					Attribute("test-driver/id", "a").
					Device("dev-b").
					Attribute("test-driver/id", "b").
					Device("dev-c").
					Attribute("test-driver/id", "c").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-backtrack", ResourceClaimTemplateName: new("claim-tmpl-backtrack")},
				}
			},
			lookup: defaultLookup,
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {"res-1": resource.MustParse("3")},
			},
		},
		"Multi-request CEL selectors return the evaluation error of the first device tried": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-backtrack", "ns1").
					DeviceRequest("fails-on-c", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id == "c" ? device.attributes["test-driver"].missing == "yes" : true`).
					DeviceRequest("any", "test-deviceclass-1", 1).
					WithCELSelectors(`true`).
					DeviceRequest("only-a", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id == "a"`).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("slice-backtrack", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-c").
					Attribute("test-driver/id", "c").
					Device("dev-b").
					Attribute("test-driver/id", "b").
					Device("dev-a").
					Attribute("test-driver/id", "a").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-backtrack", ResourceClaimTemplateName: new("claim-tmpl-backtrack")},
				}
			},
			lookup: defaultLookup,
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0).Child("exactly", "selectors"),
					nil, "",
				),
			},
		},
		"Multi-request CEL selectors keep the shortage with an evaluation error reached while trying other devices": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-backtrack", "ns1").
					DeviceRequest("any", "test-deviceclass-1", 1).
					WithCELSelectors(`true`).
					DeviceRequest("b-fails-on-c", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id == "c" ? device.attributes["test-driver"].missing == "yes" : device.attributes["test-driver"].id == "b"`).
					DeviceRequest("only-a", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id == "a"`).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("slice-backtrack", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-a").
					Attribute("test-driver/id", "a").
					Device("dev-b").
					Attribute("test-driver/id", "b").
					Device("dev-c").
					Attribute("test-driver/id", "c").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-backtrack", ResourceClaimTemplateName: new("claim-tmpl-backtrack")},
				}
			},
			lookup: defaultLookup,
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(1).Child("exactly", "selectors"),
					nil, "",
				),
				field.InternalError(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(2).Child("exactly", "selectors"),
					errors.New(""),
				),
			},
		},
		"Multi-request CEL selectors return the evaluation error of a device that an earlier request gives back, even when the later request cannot fit": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-backtrack", "ns1").
					DeviceRequest("not-c", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id != "c"`).
					DeviceRequest("two-fails-on-a", "test-deviceclass-1", 2).
					WithCELSelectors(`device.attributes["test-driver"].value == "yes"`).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("slice-backtrack", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-a").
					Attribute("test-driver/id", "a").
					Device("dev-b").
					Attribute("test-driver/id", "b").
					Attribute("test-driver/value", "yes").
					Device("dev-c").
					Attribute("test-driver/id", "c").
					Attribute("test-driver/value", "no").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-backtrack", ResourceClaimTemplateName: new("claim-tmpl-backtrack")},
				}
			},
			lookup: defaultLookup,
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(1).Child("exactly", "selectors"),
					nil, "",
				),
				field.InternalError(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(1).Child("exactly", "selectors"),
					errors.New(""),
				),
			},
		},
		"Multi-request CEL selectors are not evaluated on a device that an earlier request uses": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-backtrack", "ns1").
					DeviceRequest("any-two", "test-deviceclass-1", 2).
					WithCELSelectors(`true`).
					DeviceRequest("value", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].value == "yes"`).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("slice-backtrack", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-a").
					Attribute("test-driver/id", "a").
					Device("dev-b").
					Attribute("test-driver/id", "b").
					Attribute("test-driver/value", "yes").
					Device("dev-c").
					Attribute("test-driver/id", "c").
					Attribute("test-driver/value", "no").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-backtrack", ResourceClaimTemplateName: new("claim-tmpl-backtrack")},
				}
			},
			lookup: defaultLookup,
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {"res-1": resource.MustParse("3")},
			},
		},
		"Multi-request CEL selectors return the evaluation errors and the shortage of the first request that falls short": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-backtrack", "ns1").
					DeviceRequest("fails", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].missing == "yes"`).
					DeviceRequest("only-x", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id == "x"`).
					DeviceRequest("only-y", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id == "y"`).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("slice-backtrack", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-c").
					Attribute("test-driver/id", "c").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-backtrack", ResourceClaimTemplateName: new("claim-tmpl-backtrack")},
				}
			},
			lookup: defaultLookup,
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0).Child("exactly", "selectors"),
					nil, "",
				),
				field.InternalError(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(1).Child("exactly", "selectors"),
					errors.New(""),
				),
			},
		},
		"Multi-request CEL selectors report the shortage of a request that needs a device matched before an evaluation error": {
			extraObjects: []runtime.Object{
				// two-yes matches dev-a and then fails on dev-b, so only-a cannot take dev-a.
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-backtrack", "ns1").
					DeviceRequest("two-yes", "test-deviceclass-1", 2).
					WithCELSelectors(`device.attributes["test-driver"].value == "yes"`).
					DeviceRequest("only-a", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id == "a"`).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("slice-backtrack", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-a").
					Attribute("test-driver/id", "a").
					Attribute("test-driver/value", "yes").
					Device("dev-b").
					Attribute("test-driver/id", "b").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-backtrack", ResourceClaimTemplateName: new("claim-tmpl-backtrack")},
				}
			},
			lookup: defaultLookup,
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0).Child("exactly", "selectors"),
					nil, "",
				),
				field.InternalError(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(1).Child("exactly", "selectors"),
					errors.New(""),
				),
			},
		},
		"Multi-request CEL selectors return the evaluation error of a request after the one that falls short": {
			extraObjects: []runtime.Object{
				// fails-on-x reaches dev-x only after the search moves two-any to dev-z and dev-w.
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-backtrack", "ns1").
					DeviceRequest("two-any", "test-deviceclass-1", 2).
					WithCELSelectors(`true`).
					DeviceRequest("only-y", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id == "y"`).
					DeviceRequest("fails-on-x", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id == "x" ? device.attributes["test-driver"].missing == "yes" : false`).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("slice-backtrack", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-x").
					Attribute("test-driver/id", "x").
					Device("dev-y").
					Attribute("test-driver/id", "y").
					Device("dev-z").
					Attribute("test-driver/id", "z").
					Device("dev-w").
					Attribute("test-driver/id", "w").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-backtrack", ResourceClaimTemplateName: new("claim-tmpl-backtrack")},
				}
			},
			lookup: defaultLookup,
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(2).Child("exactly", "selectors"),
					nil, "",
				),
				field.InternalError(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(1).Child("exactly", "selectors"),
					errors.New(""),
				),
			},
		},
		"Multi-request CEL selectors return the DeviceClass error of a request that only the search reaches": {
			extraObjects: []runtime.Object{
				// The first pass stops at only-a, so the search is the first to compile the
				// selector of test-deviceclass-2.
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-backtrack", "ns1").
					DeviceRequest("any", "test-deviceclass-1", 1).
					WithCELSelectors(`true`).
					DeviceRequest("only-a", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id == "a"`).
					DeviceRequest("bad-class", "test-deviceclass-2", 1).
					WithCELSelectors(`true`).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-2").CELSelector("this is not valid CEL!!!").Obj(),
				utiltesting.MakeResourceSlice("slice-backtrack", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-a").
					Attribute("test-driver/id", "a").
					Device("dev-b").
					Attribute("test-driver/id", "b").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-backtrack", ResourceClaimTemplateName: new("claim-tmpl-backtrack")},
				}
			},
			lookup: defaultLookup,
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(2).Index(0).Child("cel", "expression"),
					nil, "",
				),
				field.InternalError(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(1).Child("exactly", "selectors"),
					errors.New(""),
				),
			},
		},
		"Multi-request CEL selectors report the shortage without spending the search on device sets that cannot fit": {
			extraObjects: []runtime.Object{
				// "spare" has the 18 devices it needs only with dev-first, which only-first
				// needs. A search that tried sets of devices that cannot fit would run out
				// of steps and let the claim through.
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-backtrack", "ns1").
					DeviceRequest("spare", "test-deviceclass-1", 18).
					WithCELSelectors(`device.attributes["test-driver"].group != "other"`).
					DeviceRequest("only-first", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].group == "first"`).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("slice-backtrack", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-first").
					Attribute("test-driver/group", "first").
					Devices("dev-main", 16, "test-driver/group", "main").
					Devices("dev-spare", 1, "test-driver/group", "spare").
					Devices("dev-other", 6, "test-driver/group", "other").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-backtrack", ResourceClaimTemplateName: new("claim-tmpl-backtrack")},
				}
			},
			lookup: defaultLookup,
			wantErr: field.ErrorList{
				field.InternalError(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(1).Child("exactly", "selectors"),
					errors.New(""),
				),
			},
		},
		"Multi-request CEL selectors find devices among 40,000 and reach the next request before the search runs out of steps": {
			extraObjects: []runtime.Object{
				// Counting all 40,000 devices for each device that the search picks would
				// run out of steps before j-or-k tries dev-k, and let the claim through.
				// Only a search past only-j compiles the selector of test-deviceclass-2.
				// The fake client takes more devices in one ResourceSlice than the API
				// allows; the check joins all ResourceSlices into one list anyway.
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-backtrack", "ns1").
					DeviceRequest("many", "test-deviceclass-1", 28).
					WithCELSelectors(`device.attributes["test-driver"].id == "many"`).
					DeviceRequest("j-or-k", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id in ["j", "k"]`).
					DeviceRequest("only-j", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id == "j"`).
					DeviceRequest("bad-class", "test-deviceclass-2", 1).
					WithCELSelectors(`true`).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-2").CELSelector("this is not valid CEL!!!").Obj(),
				utiltesting.MakeResourceSlice("slice-backtrack", "test-driver").
					Pool("pool-1", 1, 1).
					Devices("dev-many", 28, "test-driver/id", "many").
					Device("dev-j").
					Attribute("test-driver/id", "j").
					Device("dev-k").
					Attribute("test-driver/id", "k").
					Devices("dev-other", 39970, "test-driver/id", "other").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-backtrack", ResourceClaimTemplateName: new("claim-tmpl-backtrack")},
				}
			},
			lookup: defaultLookup,
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(3).Index(0).Child("cel", "expression"),
					nil, "",
				),
				field.InternalError(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(2).Child("exactly", "selectors"),
					errors.New(""),
				),
			},
		},
		"Multi-request CEL selectors let the claim through when the search runs out of steps before it finds devices": {
			extraObjects: []runtime.Object{
				// r-0 can take dev-10, but the search runs out of steps before it tries dev-10 for r-0.
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-backtrack", "ns1").
					DeviceRequest("r-0", "test-deviceclass-1", 1).
					WithCELSelectors(`true`).
					DeviceRequest("r-1", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].eligible == "yes"`).
					DeviceRequest("r-2", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].eligible == "yes"`).
					DeviceRequest("r-3", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].eligible == "yes"`).
					DeviceRequest("r-4", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].eligible == "yes"`).
					DeviceRequest("r-5", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].eligible == "yes"`).
					DeviceRequest("r-6", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].eligible == "yes"`).
					DeviceRequest("r-7", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].eligible == "yes"`).
					DeviceRequest("r-8", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].eligible == "yes"`).
					DeviceRequest("r-9", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].eligible == "yes"`).
					DeviceRequest("r-10", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].eligible == "yes"`).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				utiltesting.MakeResourceSlice("slice-backtrack", "test-driver").
					Pool("pool-1", 1, 1).
					Devices("dev", 10, "test-driver/eligible", "yes").
					Device("dev-10").
					Attribute("test-driver/eligible", "no").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-backtrack", ResourceClaimTemplateName: new("claim-tmpl-backtrack")},
				}
			},
			lookup: defaultLookup,
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {"res-1": resource.MustParse("11")},
			},
		},
		"Invalid CEL selector returns error": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-badcel", "ns1").
					DeviceRequest("req", "test-deviceclass-1", 1).
					WithCELSelectors("this is not valid CEL!!!").
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
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
		"Device constraints are pass-through and counted correctly": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-constraints", "ns1").
					DeviceRequest("gpu-1", "test-deviceclass-1", 1).
					DeviceRequest("gpu-2", "test-deviceclass-1", 1).
					WithDeviceConstraints([]string{"gpu-1", "gpu-2"}, "numa-node").
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-constraints", ResourceClaimTemplateName: new("claim-tmpl-constraints")},
				}
			},
			lookup: defaultLookup,
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {"res-1": resource.MustParse("2")},
			},
		},
		"FirstAvailable returns error": {
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
		"Exactly and FirstAvailable are nil returns error": {
			extraObjects: []runtime.Object{
				&resourcev1.ResourceClaimTemplate{
					ObjectMeta: metav1.ObjectMeta{Name: "claim-tmpl-empty", Namespace: "ns1"},
					Spec: resourcev1.ResourceClaimTemplateSpec{
						Spec: resourcev1.ResourceClaimSpec{
							Devices: resourcev1.DeviceClaim{
								Requests: []resourcev1.DeviceRequest{
									{Name: "req"},
								},
							},
						},
					},
				},
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-empty", ResourceClaimTemplateName: new("claim-tmpl-empty")},
				}
			},
			lookup: defaultLookup,
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0), "", ""),
			},
		},
		"AdminAccess returns error": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-admin", "ns1").
					DeviceRequest("req", "test-deviceclass-1", 1).
					WithAdminAccess(true).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
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
		"CEL selectors with nonexistent DeviceClass returns error": {
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
				field.NotFound(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0).Child("exactly", "deviceClassName"),
					"",
				),
			},
		},
		"A request on a nonexistent DeviceClass returns error": {
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-1", ResourceClaimTemplateName: new("claim-tmpl-1")},
				}
			},
			lookup: defaultLookup,
			wantErr: field.ErrorList{
				field.NotFound(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0).Child("exactly", "deviceClassName"),
					"",
				),
			},
		},
		"A request on a nonexistent DeviceClass is charged when skipDeviceClassCheck is true": {
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-1", ResourceClaimTemplateName: new("claim-tmpl-1")},
				}
			},
			lookup:               defaultLookup,
			skipDeviceClassCheck: true,
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {"res-1": resource.MustParse("2")},
			},
		},
		"CEL selectors with empty DeviceClassName succeeds": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-nodc", "ns1").
					DeviceRequest("req", "", 1).
					WithCELSelectors("device.driver == \"test-driver\"").
					Obj(),
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
		"Device config is pass-through and counted correctly": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-config", "ns1").
					DeviceRequest("req", "test-deviceclass-1", 1).
					WithDeviceConfig("req", "", nil).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-config", ResourceClaimTemplateName: new("claim-tmpl-config")},
				}
			},
			lookup: defaultLookup,
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {"res-1": resource.MustParse("1")},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var testMapper *ResourceMapper
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
				if name == "Unmapped DeviceClass returns error" {
					mappings = []configapi.DeviceClassMapping{}
				}
				mapper := NewResourceMapper()
				err := mapper.PopulateFromConfiguration(mappings)
				if err != nil {
					t.Fatalf("Failed to initialize DRA mapper: %v", err)
				}
				testMapper = mapper
			}

			objs := []client.Object{tmpl, claim}
			if tc.extraObjects != nil {
				for _, o := range tc.extraObjects {
					objs = append(objs, o.(client.Object))
				}
			}
			baseClient := utiltesting.NewClientBuilder().
				WithIndex(&resourcev1.ResourceSlice{}, "spec.driver", func(obj client.Object) []string {
					return []string{obj.(*resourcev1.ResourceSlice).Spec.Driver}
				}).
				WithObjects(objs...).Build()

			wlCopy := wl.DeepCopy()
			if tc.modifyWL != nil {
				tc.modifyWL(wlCopy)
			}

			ctx, _ := utiltesting.ContextWithLog(t)
			if testMapper == nil {
				testMapper = NewResourceMapper()
			}
			sliceCache := NewResourceSliceCache(baseClient)
			got, err := GetResourceRequestsForResourceClaimTemplates(ctx, baseClient, sliceCache, testMapper, wlCopy, tc.skipDeviceClassCheck)

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

func Test_countDevicesPerClass_overflow(t *testing.T) {
	cases := map[string]struct {
		requests  []resourcev1.DeviceRequest
		wantCount resources.Amount
	}{
		"normal sum across requests": {
			requests: []resourcev1.DeviceRequest{
				testingdra.MakeDeviceRequest("r0", "gpu", 2).Obj(),
				testingdra.MakeDeviceRequest("r1", "gpu", 3).Obj(),
			},
			wantCount: resources.NewAmount(5),
		},
		"an exactly sum past MaxInt64 stays exact": {
			requests: []resourcev1.DeviceRequest{
				testingdra.MakeDeviceRequest("r0", "gpu", math.MaxInt64).Obj(),
				testingdra.MakeDeviceRequest("r1", "gpu", math.MaxInt64).Obj(),
			},
			wantCount: resources.NewAmount(math.MaxInt64).MulInt64(2),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			spec := &resourcev1.ResourceClaimSpec{Devices: resourcev1.DeviceClaim{Requests: tc.requests}}
			out, errs := countDevicesPerClass(spec)
			if len(errs) != 0 {
				t.Fatalf("unexpected errors: %v", errs)
			}
			if got := out.ResourceValue("gpu"); !got.Equal(tc.wantCount) {
				t.Errorf("count = %s, want %s", got, tc.wantCount)
			}
		})
	}
}
