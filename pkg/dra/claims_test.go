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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
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
		Name: "wl", Namespace: "ns1",
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
		modifyWL     func(w *kueue.Workload)
		extraObjects []runtime.Object
		lookup       func(corev1.ResourceName) (corev1.ResourceName, bool)
		// mappings replaces the default res-1/res-2 mappings when set.
		mappings        []configapi.DeviceClassMapping
		featureGates    map[featuregate.Feature]bool
		skipDeviceCheck bool
		// excluded lists the DeviceClasses from resources.excludeDeviceClassNames.
		excluded []corev1.ResourceName
		want     map[kueue.PodSetReference]corev1.ResourceList
		wantErr  field.ErrorList
	}{
		"An Exactly request on a logical resource named cpu is charged in whole units": {
			extraObjects: []runtime.Object{testingdra.MakeDeviceClass("test-deviceclass-1").Obj()},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-1", ResourceClaimTemplateName: new("claim-tmpl-1")},
				}
			},
			lookup:   defaultLookup,
			mappings: []configapi.DeviceClassMapping{{Name: corev1.ResourceCPU, DeviceClassNames: []corev1.ResourceName{"test-deviceclass-1"}}},
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {corev1.ResourceCPU: resource.MustParse("2")},
			},
		},
		"A prioritized list on a logical resource named cpu is charged in whole units like an Exactly request": {
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-1", ResourceClaimTemplateName: new("claim-tmpl-fa")},
				}
			},
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-fa", "ns1").
					DeviceRequests(testingdra.MakeFirstAvailableRequest("r", testingdra.MakeDeviceSubRequest("fast", "test-deviceclass-1", 2).Obj()).Obj()).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
			},
			lookup:       defaultLookup,
			mappings:     []configapi.DeviceClassMapping{{Name: corev1.ResourceCPU, DeviceClassNames: []corev1.ResourceName{"test-deviceclass-1"}}},
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPrioritizedList: true},
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {corev1.ResourceCPU: resource.MustParse("2")},
			},
		},
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
				testingdra.MakeResourceSlice("slice-1", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-0").
					Device("dev-1").
					Obj(),
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
				testingdra.MakeResourceSlice("a-old", "test-driver").
					Pool("pool-1", 1, 1).
					Device("old-0").
					Obj(),
				testingdra.MakeResourceSlice("z-new", "test-driver").
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
				testingdra.MakeResourceSlice("a-new", "test-driver").
					Pool("pool-1", 2, 1).
					Device("new-0").
					Obj(),
				testingdra.MakeResourceSlice("z-old", "test-driver").
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
				testingdra.MakeResourceSlice("a-old", "test-driver").
					Pool("pool-1", 1, 1).
					Device("old-0").
					Device("old-1").
					Obj(),
				testingdra.MakeResourceSlice("z-new", "test-driver").
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
				testingdra.MakeResourceSlice("slice-1", "test-driver").
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
				testingdra.MakeResourceSlice("a-old", "test-driver").
					Pool("pool-1", 1, 1).
					Device("old-0").
					Obj(),
				testingdra.MakeResourceSlice("b-new", "test-driver").
					Pool("pool-1", 2, 2).
					Device("new-0").
					Obj(),
				testingdra.MakeResourceSlice("c-new", "test-driver").
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
				testingdra.MakeResourceSlice("slice-1", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-0").
					Device("dev-1").
					Obj(),
				testingdra.MakeResourceSlice("slice-2", "test-driver").
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
				testingdra.MakeResourceSlice("slice-1", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-0").
					Obj(),
				testingdra.MakeResourceSlice("slice-2", "other-driver").
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
				testingdra.MakeResourceSlice("a-unselected", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-0").
					Attribute("test-driver/selected", "false").
					Obj(),
				testingdra.MakeResourceSlice("z-selected", "test-driver").
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
				testingdra.MakeResourceSlice("slice-1", "test-driver").
					Pool("pool-1", 1, 2).
					Device("dev-0").
					Device("dev-1").
					Obj(),
				testingdra.MakeResourceSlice("slice-2", "test-driver").
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
		"CEL pool validation excludes incomplete pools with a counter source": {
			lookup: defaultLookup,
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-cel-pool", "ns1").
					DeviceRequest("device-request", "test-deviceclass-1", 2).
					WithCELSelectors("true").
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeResourceSlice("slice-1", "test-driver").
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
			mappings: []configapi.DeviceClassMapping{{
				Name:             "res-1",
				DeviceClassNames: []corev1.ResourceName{"test-deviceclass-1"},
				Sources: []configapi.DeviceClassSourceConfig{{
					Counter: &configapi.DeviceClassCounterSource{
						Name:           "memory",
						Driver:         "test-driver",
						DeviceSelector: resourcev1.DeviceSelector{CEL: &resourcev1.CELDeviceSelector{Expression: "true"}},
					},
				}},
			}},
			wantErr: celPoolError,
		},
		"CEL pool validation excludes incomplete pools with a capacity source": {
			lookup: defaultLookup,
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-cel-pool", "ns1").
					DeviceRequest("device-request", "test-deviceclass-1", 2).
					WithCELSelectors("true").
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeResourceSlice("slice-1", "test-driver").
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
			mappings: []configapi.DeviceClassMapping{{
				Name:             "res-1",
				DeviceClassNames: []corev1.ResourceName{"test-deviceclass-1"},
				Sources: []configapi.DeviceClassSourceConfig{{
					Capacity: &configapi.DeviceClassCapacitySource{
						Name:           "test-driver/memory",
						Driver:         "test-driver",
						DeviceSelector: resourcev1.DeviceSelector{CEL: &resourcev1.CELDeviceSelector{Expression: "true"}},
					},
				}},
			}},
			wantErr: celPoolError,
		},
		"CEL pool validation excludes a pool with duplicate device names": {
			lookup: defaultLookup,
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-cel-pool", "ns1").
					DeviceRequest("device-request", "test-deviceclass-1", 2).
					WithCELSelectors("true").
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeResourceSlice("slice-1", "test-driver").
					Pool("pool-1", 1, 2).
					Device("dev-0").
					Obj(),
				testingdra.MakeResourceSlice("slice-2", "test-driver").
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
				testingdra.MakeResourceSlice("slice-1", "test-driver").
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
				testingdra.MakeResourceSlice("counters", "test-driver").
					Pool("pool-1", 1, 2).
					CounterSet("gpu-0", "memory", "40Gi").
					Obj(),
				testingdra.MakeResourceSlice("devices", "test-driver").
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
				testingdra.MakeResourceSlice("slice-1", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dup-0").
					Device("dup-0").
					Obj(),
				testingdra.MakeResourceSlice("slice-2", "test-driver").
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
		"CEL pool validation excludes invalid pools with a counter source": {
			lookup: defaultLookup,
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-cel-pool", "ns1").
					DeviceRequest("device-request", "test-deviceclass-1", 2).
					WithCELSelectors("true").
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeResourceSlice("slice-1", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-0").
					Device("dev-0").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-cel-pool", ResourceClaimTemplateName: new("claim-tmpl-cel-pool")},
				}
			},
			mappings: []configapi.DeviceClassMapping{{
				Name:             "res-1",
				DeviceClassNames: []corev1.ResourceName{"test-deviceclass-1"},
				Sources: []configapi.DeviceClassSourceConfig{{
					Counter: &configapi.DeviceClassCounterSource{
						Name:           "memory",
						Driver:         "test-driver",
						DeviceSelector: resourcev1.DeviceSelector{CEL: &resourcev1.CELDeviceSelector{Expression: "true"}},
					},
				}},
			}},
			wantErr: celPoolError,
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
				testingdra.MakeResourceSlice("gpu-slice", "gpu-driver").
					Pool("gpu-pool", 1, 1).
					Device("gpu-0").
					Obj(),
				testingdra.MakeResourceSlice("nic-slice", "nic-driver").
					Pool("nic-pool", 1, 1).
					Device("nic-0").
					Device("nic-1").
					Device("nic-2").
					Obj(),
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
				testingdra.MakeResourceSlice("slice-2", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-0").
					Obj(),
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
		"CEL selector on allowMultipleAllocations matches a device that allows multiple allocations": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-shared", "ns1").
					DeviceRequest("req", "test-deviceclass-1", 1).
					WithCELSelectors("device.allowMultipleAllocations == true").
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeResourceSlice("slice-shared", "test-driver").
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
				testingdra.MakeResourceSlice("slice-exclusive", "test-driver").
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
				testingdra.MakeResourceSlice("slice-3", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-0").
					Device("dev-1").
					Obj(),
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
		"CEL selectors on a firstAvailable request fall back to the next alternative with enough devices": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-fa-fallback", "ns1").
					DeviceRequests(testingdra.MakeFirstAvailableRequest("r",
						testingdra.MakeDeviceSubRequest("big", "test-deviceclass-1", 2).CELSelector(`device.attributes["test-driver"].big == "true"`).Obj(),
						testingdra.MakeDeviceSubRequest("any", "test-deviceclass-1", 2).CELSelector("true").Obj(),
					).Obj()).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeResourceSlice("slice-fa-fallback", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-0").
					Attribute("test-driver/big", "true").
					Device("dev-1").
					Attribute("test-driver/big", "false").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-fa-fallback", ResourceClaimTemplateName: new("claim-tmpl-fa-fallback")},
				}
			},
			lookup:       defaultLookup,
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPrioritizedList: true},
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {"res-1": resource.MustParse("2")},
			},
		},
		"CEL selectors on a firstAvailable request with no alternative matching enough devices returns error": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-fa-short", "ns1").
					DeviceRequests(testingdra.MakeFirstAvailableRequest("r",
						testingdra.MakeDeviceSubRequest("big", "test-deviceclass-1", 3).CELSelector(`device.attributes["test-driver"].big == "true"`).Obj(),
						testingdra.MakeDeviceSubRequest("any", "test-deviceclass-1", 3).CELSelector("true").Obj(),
					).Obj()).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeResourceSlice("slice-fa-short", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-0").
					Attribute("test-driver/big", "true").
					Device("dev-1").
					Attribute("test-driver/big", "false").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-fa-short", ResourceClaimTemplateName: new("claim-tmpl-fa-short")},
				}
			},
			lookup:       defaultLookup,
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPrioritizedList: true},
			wantErr: field.ErrorList{
				field.InternalError(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0).Child("firstAvailable"),
					errors.New(""),
				),
			},
		},
		"CEL selectors on a firstAvailable request are not checked when an alternative has no CEL selector": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-fa-nocel", "ns1").
					DeviceRequests(testingdra.MakeFirstAvailableRequest("r",
						testingdra.MakeDeviceSubRequest("big", "test-deviceclass-1", 3).CELSelector(`device.attributes["test-driver"].big == "true"`).Obj(),
						testingdra.MakeDeviceSubRequest("any", "test-deviceclass-1", 3).Obj(),
					).Obj()).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeResourceSlice("slice-fa-nocel", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-0").
					Attribute("test-driver/big", "true").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-fa-nocel", ResourceClaimTemplateName: new("claim-tmpl-fa-nocel")},
				}
			},
			lookup:       defaultLookup,
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPrioritizedList: true},
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {"res-1": resource.MustParse("3")},
			},
		},
		"CEL selectors on a firstAvailable alternative that falls short leave its devices to later requests": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-fa-release", "ns1").
					DeviceRequests(
						testingdra.MakeFirstAvailableRequest("r0",
							testingdra.MakeDeviceSubRequest("big", "test-deviceclass-1", 2).CELSelector(`device.attributes["test-driver"].big == "true"`).Obj(),
							testingdra.MakeDeviceSubRequest("any", "test-deviceclass-1", 2).CELSelector("true").Obj(),
						).Obj(),
						testingdra.MakeDeviceRequest("r1", "test-deviceclass-1", 1).CELSelector("true").Obj(),
					).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeResourceSlice("slice-fa-release", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-0").
					Attribute("test-driver/big", "true").
					Device("dev-1").
					Attribute("test-driver/big", "false").
					Device("dev-2").
					Attribute("test-driver/big", "false").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-fa-release", ResourceClaimTemplateName: new("claim-tmpl-fa-release")},
				}
			},
			lookup:       defaultLookup,
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPrioritizedList: true},
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {"res-1": resource.MustParse("3")},
			},
		},
		"CEL selectors on a firstAvailable request reserve the devices of the alternative that matches": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-fa-reserve", "ns1").
					DeviceRequests(
						testingdra.MakeFirstAvailableRequest("r0",
							testingdra.MakeDeviceSubRequest("any", "test-deviceclass-1", 2).CELSelector("true").Obj(),
						).Obj(),
						testingdra.MakeDeviceRequest("r1", "test-deviceclass-1", 1).CELSelector("true").Obj(),
					).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeResourceSlice("slice-fa-reserve", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-0").
					Device("dev-1").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-fa-reserve", ResourceClaimTemplateName: new("claim-tmpl-fa-reserve")},
				}
			},
			lookup:       defaultLookup,
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPrioritizedList: true},
			wantErr: field.ErrorList{
				field.InternalError(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(1).Child("exactly", "selectors"),
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
				testingdra.MakeResourceSlice("slice-multi", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-0").
					Obj(),
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
				testingdra.MakeResourceSlice("slice-backtrack", "test-driver").
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
		"Multi-request CEL selectors fall back to the next firstAvailable alternative when a later request needs the device of the first": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-backtrack", "ns1").
					DeviceRequests(
						testingdra.MakeFirstAvailableRequest("first-a-then-b",
							testingdra.MakeDeviceSubRequest("a", "test-deviceclass-1", 1).CELSelector(`device.attributes["test-driver"].id == "a"`).Obj(),
							testingdra.MakeDeviceSubRequest("b", "test-deviceclass-1", 1).CELSelector(`device.attributes["test-driver"].id == "b"`).Obj(),
						).Obj(),
						testingdra.MakeDeviceRequest("only-a", "test-deviceclass-1", 1).CELSelector(`device.attributes["test-driver"].id == "a"`).Obj(),
					).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeResourceSlice("slice-backtrack", "test-driver").
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
			lookup:       defaultLookup,
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPrioritizedList: true},
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {"res-1": resource.MustParse("2")},
			},
		},
		"Multi-request CEL selectors keep the first firstAvailable alternative when another device lets it satisfy the claim": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-backtrack", "ns1").
					DeviceRequests(
						testingdra.MakeFirstAvailableRequest("any-then-probe",
							testingdra.MakeDeviceSubRequest("any", "test-deviceclass-1", 1).CELSelector(`true`).Obj(),
							testingdra.MakeDeviceSubRequest("probe", "test-deviceclass-1", 1).CELSelector(`device.attributes["test-driver"].missing == "yes"`).Obj(),
						).Obj(),
						testingdra.MakeDeviceRequest("only-a", "test-deviceclass-1", 1).CELSelector(`device.attributes["test-driver"].id == "a"`).Obj(),
					).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeResourceSlice("slice-backtrack", "test-driver").
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
			lookup:       defaultLookup,
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPrioritizedList: true},
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
				testingdra.MakeResourceSlice("slice-backtrack", "test-driver").
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
				testingdra.MakeResourceSlice("slice-backtrack", "test-driver").
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
				testingdra.MakeResourceSlice("slice-backtrack", "test-driver").
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
				testingdra.MakeResourceSlice("slice-backtrack", "test-driver").
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
				testingdra.MakeResourceSlice("slice-backtrack", "test-driver").
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
				testingdra.MakeResourceSlice("slice-backtrack", "test-driver").
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
				testingdra.MakeResourceSlice("slice-backtrack", "test-driver").
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
				testingdra.MakeResourceSlice("slice-backtrack", "test-driver").
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
				testingdra.MakeResourceSlice("slice-backtrack", "test-driver").
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
				testingdra.MakeResourceSlice("slice-backtrack", "test-driver").
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
				testingdra.MakeResourceSlice("slice-backtrack", "test-driver").
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
		"Multi-request CEL selectors report the shortage without spending the search on a firstAvailable alternative that matches too few devices": {
			extraObjects: []runtime.Object{
				// "main" matches 17 of the 18 devices it needs, and "spare" has 18 only with
				// dev-first, which only-first needs. A search that still counted the devices
				// known not to match "main" would run out of steps and let the claim through.
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-backtrack", "ns1").
					DeviceRequests(
						testingdra.MakeFirstAvailableRequest("main-then-spare",
							testingdra.MakeDeviceSubRequest("main", "test-deviceclass-1", 18).CELSelector(`device.attributes["test-driver"].group in ["first", "main"]`).Obj(),
							testingdra.MakeDeviceSubRequest("spare", "test-deviceclass-1", 18).CELSelector(`device.attributes["test-driver"].group != "other"`).Obj(),
						).Obj(),
						testingdra.MakeDeviceRequest("only-first", "test-deviceclass-1", 1).CELSelector(`device.attributes["test-driver"].group == "first"`).Obj(),
					).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeResourceSlice("slice-backtrack", "test-driver").
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
			lookup:       defaultLookup,
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPrioritizedList: true},
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
				testingdra.MakeResourceSlice("slice-backtrack", "test-driver").
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
				testingdra.MakeResourceSlice("slice-backtrack", "test-driver").
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
		"CEL selectors with unsatisfiable expression succeeds when skipDeviceCheck is true": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-unsat-skip", "ns1").
					DeviceRequest("req", "test-deviceclass-1", 2).
					WithCELSelectors("device.driver == \"nonexistent-driver\"").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-unsat-skip", ResourceClaimTemplateName: new("claim-tmpl-unsat-skip")},
				}
			},
			lookup:          defaultLookup,
			skipDeviceCheck: true,
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {"res-1": resource.MustParse("2")},
			},
		},
		"Invalid CEL selector still returns error when skipDeviceCheck is true": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-badcel-skip", "ns1").
					DeviceRequest("req", "test-deviceclass-1", 1).
					WithCELSelectors("this is not valid CEL!!!").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-badcel-skip", ResourceClaimTemplateName: new("claim-tmpl-badcel-skip")},
				}
			},
			lookup:          defaultLookup,
			skipDeviceCheck: true,
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0).Child("exactly", "selectors"),
					"",
					"",
				),
			},
		},
		"CEL selectors on a firstAvailable request with no alternative matching enough devices succeeds when skipDeviceCheck is true": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-fa-short-skip", "ns1").
					DeviceRequests(testingdra.MakeFirstAvailableRequest("r",
						testingdra.MakeDeviceSubRequest("big", "test-deviceclass-1", 3).CELSelector(`device.attributes["test-driver"].big == "true"`).Obj(),
						testingdra.MakeDeviceSubRequest("any", "test-deviceclass-1", 3).CELSelector("true").Obj(),
					).Obj()).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeResourceSlice("slice-fa-short-skip", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-0").
					Attribute("test-driver/big", "true").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-fa-short-skip", ResourceClaimTemplateName: new("claim-tmpl-fa-short-skip")},
				}
			},
			lookup:          defaultLookup,
			featureGates:    map[featuregate.Feature]bool{features.KueueDRAIntegrationPrioritizedList: true},
			skipDeviceCheck: true,
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {"res-1": resource.MustParse("3")},
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
			lookup:       defaultLookup,
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPrioritizedList: false},
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0), "", ""),
			},
		},
		"Exactly and FirstAvailable are nil returns error": {
			extraObjects: []runtime.Object{
				&resourcev1.ResourceClaimTemplate{
					Name: "claim-tmpl-empty", Namespace: "ns1",
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
		"AdminAccess request with AllocationMode All returns the CEL evaluation error before its first matching device": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-admin-all", "ns1").
					DeviceRequest("admin-all", "test-deviceclass-1", 0).
					AllocationModeAll().
					WithAdminAccess(true).
					WithCELSelectors(`device.attributes["test-driver"].value == "yes"`).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeResourceSlice("slice-admin-all", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-a").
					Attribute("test-driver/id", "a").
					Device("dev-b").
					Attribute("test-driver/value", "yes").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-admin-all", ResourceClaimTemplateName: new("claim-tmpl-admin-all")},
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
		"AdminAccess request with AllocationMode All needs at least one matching device": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-admin-all", "ns1").
					DeviceRequest("admin-all", "test-deviceclass-1", 0).
					AllocationModeAll().
					WithAdminAccess(true).
					WithCELSelectors(`device.attributes["test-driver"].id == "a"`).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeResourceSlice("slice-admin-all", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-b").
					Attribute("test-driver/id", "b").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-admin-all", ResourceClaimTemplateName: new("claim-tmpl-admin-all")},
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
		"AdminAccess request with AllocationMode All keeps its first matching device from a later request": {
			extraObjects: []runtime.Object{
				// admin-all holds dev-a, so only-a cannot take it.
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-admin-all", "ns1").
					DeviceRequest("admin-all", "test-deviceclass-1", 0).
					AllocationModeAll().
					WithAdminAccess(true).
					WithCELSelectors(`true`).
					DeviceRequest("only-a", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id == "a"`).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeResourceSlice("slice-admin-all", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-a").
					Attribute("test-driver/id", "a").
					Device("dev-b").
					Attribute("test-driver/id", "b").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-admin-all", ResourceClaimTemplateName: new("claim-tmpl-admin-all")},
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
		"AdminAccess request with AllocationMode All keeps the shortage when a later request needs one of its devices": {
			extraObjects: []runtime.Object{
				// kube-scheduler gives admin-all both dev-c and dev-d, so only-d cannot fit.
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-admin-all", "ns1").
					DeviceRequest("a-or-b", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id in ["a", "b"]`).
					DeviceRequest("only-a", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id == "a"`).
					DeviceRequest("admin-all", "test-deviceclass-1", 0).
					AllocationModeAll().
					WithAdminAccess(true).
					WithCELSelectors(`device.attributes["test-driver"].id in ["c", "d"]`).
					DeviceRequest("only-d", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id == "d"`).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeResourceSlice("slice-admin-all", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-a").
					Attribute("test-driver/id", "a").
					Device("dev-b").
					Attribute("test-driver/id", "b").
					Device("dev-c").
					Attribute("test-driver/id", "c").
					Device("dev-d").
					Attribute("test-driver/id", "d").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-admin-all", ResourceClaimTemplateName: new("claim-tmpl-admin-all")},
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
		"AdminAccess request with AllocationMode All keeps the shortage when an earlier request needs one of its devices": {
			extraObjects: []runtime.Object{
				// kube-scheduler cannot give admin-all dev-b once a-or-b holds it, and
				// only-a needs dev-a.
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-admin-all", "ns1").
					DeviceRequest("a-or-b", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id in ["a", "b"]`).
					DeviceRequest("only-a", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id == "a"`).
					DeviceRequest("admin-all", "test-deviceclass-1", 0).
					AllocationModeAll().
					WithAdminAccess(true).
					WithCELSelectors(`device.attributes["test-driver"].id in ["b", "c"]`).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeResourceSlice("slice-admin-all", "test-driver").
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
					{Name: "req-admin-all", ResourceClaimTemplateName: new("claim-tmpl-admin-all")},
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
		"AdminAccess request with AllocationMode All keeps the shortage when it matches no device": {
			extraObjects: []runtime.Object{
				// kube-scheduler needs at least one device for admin-all.
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-admin-all", "ns1").
					DeviceRequest("a-or-b", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id in ["a", "b"]`).
					DeviceRequest("only-a", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id == "a"`).
					DeviceRequest("admin-all", "test-deviceclass-1", 0).
					AllocationModeAll().
					WithAdminAccess(true).
					WithCELSelectors(`device.attributes["test-driver"].id == "z"`).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeResourceSlice("slice-admin-all", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-a").
					Attribute("test-driver/id", "a").
					Device("dev-b").
					Attribute("test-driver/id", "b").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-admin-all", ResourceClaimTemplateName: new("claim-tmpl-admin-all")},
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
		"AdminAccess request with AllocationMode All lets the search fit the other requests around its devices": {
			extraObjects: []runtime.Object{
				// The search gives back dev-c when only-a cannot fit, and kube-scheduler gives
				// the requests dev-b, dev-c and dev-a.
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-admin-all", "ns1").
					DeviceRequest("a-or-b", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id in ["a", "b"]`).
					DeviceRequest("admin-all", "test-deviceclass-1", 0).
					AllocationModeAll().
					WithAdminAccess(true).
					WithCELSelectors(`device.attributes["test-driver"].id == "c"`).
					DeviceRequest("only-a", "test-deviceclass-1", 1).
					WithCELSelectors(`device.attributes["test-driver"].id == "a"`).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeResourceSlice("slice-admin-all", "test-driver").
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
					{Name: "req-admin-all", ResourceClaimTemplateName: new("claim-tmpl-admin-all")},
				}
			},
			lookup: defaultLookup,
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {"res-1": resource.MustParse("2")},
			},
		},
		"AdminAccess request is skipped with zero quota": {
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
		},
		"Mixed AdminAccess and normal requests counts only normal": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-mixed", "ns1").
					DeviceRequest("normal-req", "test-deviceclass-1", 2).
					DeviceRequest("admin-req", "test-deviceclass-1", 1).
					WithAdminAccess(true).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-mixed", ResourceClaimTemplateName: new("claim-tmpl-mixed")},
				}
			},
			lookup: defaultLookup,
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {"res-1": resource.MustParse("2")},
			},
		},
		"CEL selectors with nonexistent DeviceClass returns error": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-noclass", "ns1").
					DeviceRequest("req", "nonexistent-class", 1).
					WithCELSelectors("device.driver == \"test-driver\"").
					Obj(),
				testingdra.MakeResourceSlice("slice-noclass", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-0").
					Obj(),
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
		"A request on a nonexistent DeviceClass is charged when skipDeviceCheck is true": {
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-1", ResourceClaimTemplateName: new("claim-tmpl-1")},
				}
			},
			lookup:          defaultLookup,
			skipDeviceCheck: true,
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {"res-1": resource.MustParse("2")},
			},
		},
		"A prioritized list with a nonexistent fallback DeviceClass returns error": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-fa-noclass", "ns1").
					DeviceRequests(testingdra.MakeFirstAvailableRequest("r",
						testingdra.MakeDeviceSubRequest("fast", "test-deviceclass-1", 1).Obj(),
						testingdra.MakeDeviceSubRequest("slow", "gpu-class", 1).Obj(),
					).Obj()).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-fa", ResourceClaimTemplateName: new("claim-tmpl-fa-noclass")},
				}
			},
			lookup:       defaultLookup,
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPrioritizedList: true},
			wantErr: field.ErrorList{
				field.NotFound(
					field.NewPath("spec", "podSets").
						Index(0).
						Child("template", "spec", "resourceClaims").
						Index(0).
						Child("devices", "requests").
						Index(0).
						Child("firstAvailable").
						Index(1).
						Child("deviceClassName"),
					"",
				),
			},
		},
		"CEL selectors with empty DeviceClassName succeeds": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-nodc", "ns1").
					DeviceRequest("req", "", 1).
					WithCELSelectors("device.driver == \"test-driver\"").
					Obj(),
				testingdra.MakeResourceSlice("slice-nodc", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-0").
					Obj(),
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
		"AllocationMode All on an excluded DeviceClass is not charged": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-nic", "ns1").
					DeviceRequest("nic", "nic.example.com", 0).
					AllocationModeAll().
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeDeviceClass("nic.example.com").Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-1", ResourceClaimTemplateName: new("claim-tmpl-1")},
					{Name: "req-nic", ResourceClaimTemplateName: new("claim-tmpl-nic")},
				}
			},
			lookup:   defaultLookup,
			excluded: []corev1.ResourceName{"nic.example.com"},
			want:     map[kueue.PodSetReference]corev1.ResourceList{"main": {"res-1": resource.MustParse("2")}},
		},
		"ExactCount on an unmapped excluded DeviceClass is not charged": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-channel", "ns1").
					DeviceRequest("channel", "channel.example.com", 1).
					Obj(),
				testingdra.MakeDeviceClass("channel.example.com").Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-channel", ResourceClaimTemplateName: new("claim-tmpl-channel")},
				}
			},
			lookup:   defaultLookup,
			excluded: []corev1.ResourceName{"channel.example.com"},
		},
		"Excluded DeviceClass that does not exist returns error": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-channel", "ns1").
					DeviceRequest("channel", "channel.example.com", 1).
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-channel", ResourceClaimTemplateName: new("claim-tmpl-channel")},
				}
			},
			lookup:   defaultLookup,
			excluded: []corev1.ResourceName{"channel.example.com"},
			wantErr: field.ErrorList{
				field.NotFound(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0).Child("exactly", "deviceClassName"),
					"",
				),
			},
		},
		"Excluded DeviceClass is rejected as unmapped with the KueueDRAExcludeDeviceClasses gate off": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-channel", "ns1").
					DeviceRequest("channel", "channel.example.com", 1).
					Obj(),
				testingdra.MakeDeviceClass("channel.example.com").Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-channel", ResourceClaimTemplateName: new("claim-tmpl-channel")},
				}
			},
			lookup:       defaultLookup,
			excluded:     []corev1.ResourceName{"channel.example.com"},
			featureGates: map[featuregate.Feature]bool{features.KueueDRAExcludeDeviceClasses: false},
			wantErr: field.ErrorList{
				field.NotFound(field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("resourceClaimTemplateName"), ""),
			},
		},
		"Unmapped DeviceClass missing from the exclude list returns error": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-channel", "ns1").
					DeviceRequest("channel", "channel.example.com", 1).
					Obj(),
				testingdra.MakeDeviceClass("channel.example.com").Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-channel", ResourceClaimTemplateName: new("claim-tmpl-channel")},
				}
			},
			lookup:   defaultLookup,
			excluded: []corev1.ResourceName{"nic.example.com"},
			wantErr: field.ErrorList{
				field.NotFound(field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("resourceClaimTemplateName"), ""),
			},
		},
		"CEL selector on an excluded DeviceClass skips the ResourceSlice check": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-nic-cel", "ns1").
					DeviceRequest("nic", "nic.example.com", 2).
					WithCELSelectors(`device.driver == "nic-driver"`).
					Obj(),
				testingdra.MakeDeviceClass("nic.example.com").Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-1", ResourceClaimTemplateName: new("claim-tmpl-1")},
					{Name: "req-nic", ResourceClaimTemplateName: new("claim-tmpl-nic-cel")},
				}
			},
			lookup:   defaultLookup,
			excluded: []corev1.ResourceName{"nic.example.com"},
			want:     map[kueue.PodSetReference]corev1.ResourceList{"main": {"res-1": resource.MustParse("2")}},
		},
		"Invalid CEL selector on an excluded DeviceClass returns error": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-nic-badcel", "ns1").
					DeviceRequest("nic", "nic.example.com", 1).
					WithCELSelectors("this is not valid CEL!!!").
					Obj(),
				testingdra.MakeDeviceClass("nic.example.com").Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-nic", ResourceClaimTemplateName: new("claim-tmpl-nic-badcel")},
				}
			},
			lookup:   defaultLookup,
			excluded: []corev1.ResourceName{"nic.example.com"},
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(0).Child("exactly", "selectors"),
					"",
					"",
				),
			},
		},
		"firstAvailable whose alternatives are all excluded is not charged with the prioritized-list gate off": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-nic-fa", "ns1").
					DeviceRequests(testingdra.MakeFirstAvailableRequest("nic",
						testingdra.MakeDeviceSubRequest("all", "nic-a.example.com", 0).AllocationModeAll().Obj(),
						testingdra.MakeDeviceSubRequest("one", "nic-b.example.com", 1).Obj(),
					).Obj()).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeDeviceClass("nic-a.example.com").Obj(),
				testingdra.MakeDeviceClass("nic-b.example.com").Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-1", ResourceClaimTemplateName: new("claim-tmpl-1")},
					{Name: "req-nic", ResourceClaimTemplateName: new("claim-tmpl-nic-fa")},
				}
			},
			lookup:       defaultLookup,
			excluded:     []corev1.ResourceName{"nic-a.example.com", "nic-b.example.com"},
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPrioritizedList: false},
			want:         map[kueue.PodSetReference]corev1.ResourceList{"main": {"res-1": resource.MustParse("2")}},
		},
		"firstAvailable whose alternatives are all excluded still compiles their CEL selectors": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-nic-fa-badcel", "ns1").
					DeviceRequests(testingdra.MakeFirstAvailableRequest("nic",
						testingdra.MakeDeviceSubRequest("one", "nic-a.example.com", 1).CELSelector("this is not valid CEL!!!").Obj(),
					).Obj()).
					Obj(),
				testingdra.MakeDeviceClass("nic-a.example.com").Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-nic", ResourceClaimTemplateName: new("claim-tmpl-nic-fa-badcel")},
				}
			},
			lookup:   defaultLookup,
			excluded: []corev1.ResourceName{"nic-a.example.com"},
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "podSets").
						Index(0).
						Child("template", "spec", "resourceClaims").
						Index(0).
						Child("devices", "requests").
						Index(0).
						Child("firstAvailable").
						Index(0).
						Child("selectors"),
					"",
					"",
				),
			},
		},
		"firstAvailable mixing an excluded and a mapped DeviceClass returns error": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-mixed-fa", "ns1").
					DeviceRequests(testingdra.MakeFirstAvailableRequest("gpu",
						testingdra.MakeDeviceSubRequest("nic", "nic-a.example.com", 1).Obj(),
						testingdra.MakeDeviceSubRequest("gpu", "test-deviceclass-1", 1).Obj(),
					).Obj()).
					Obj(),
				testingdra.MakeDeviceClass("nic-a.example.com").Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-mixed", ResourceClaimTemplateName: new("claim-tmpl-mixed-fa")},
				}
			},
			lookup:       defaultLookup,
			excluded:     []corev1.ResourceName{"nic-a.example.com"},
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPrioritizedList: true},
			wantErr: field.ErrorList{
				field.NotFound(
					field.NewPath("spec", "podSets").
						Index(0).
						Child("template", "spec", "resourceClaims").
						Index(0).
						Child("devices", "requests").
						Index(0).
						Child("firstAvailable").
						Index(0).
						Child("deviceClassName"),
					"",
				),
			},
		},
		"Excluded request does not hide a mapped request in the same ResourceClaimTemplate": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-nic-gpu", "ns1").
					DeviceRequest("nic", "nic.example.com", 0).
					AllocationModeAll().
					DeviceRequest("gpu", "test-deviceclass-1", 1).
					WithCELSelectors(`device.driver == "test-driver"`).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeResourceSlice("slice-1", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-0").
					Obj(),
				testingdra.MakeDeviceClass("nic.example.com").Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-nic-gpu", ResourceClaimTemplateName: new("claim-tmpl-nic-gpu")},
				}
			},
			lookup:   defaultLookup,
			excluded: []corev1.ResourceName{"nic.example.com"},
			want:     map[kueue.PodSetReference]corev1.ResourceList{"main": {"res-1": resource.MustParse("1")}},
		},
		"Excluded request does not skip the ResourceSlice check of a mapped request in the same ResourceClaimTemplate": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-nic-gpu", "ns1").
					DeviceRequest("nic", "nic.example.com", 0).
					AllocationModeAll().
					DeviceRequest("gpu", "test-deviceclass-1", 1).
					WithCELSelectors(`device.driver == "test-driver"`).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeDeviceClass("nic.example.com").Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-nic-gpu", ResourceClaimTemplateName: new("claim-tmpl-nic-gpu")},
				}
			},
			lookup:   defaultLookup,
			excluded: []corev1.ResourceName{"nic.example.com"},
			wantErr: field.ErrorList{
				field.InternalError(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "resourceClaims").Index(0).Child("devices", "requests").Index(1).Child("exactly", "selectors"),
					errors.New(""),
				),
			},
		},
		"firstAvailable whose alternatives are all excluded is not charged with the prioritized-list gate on": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-nic-fa", "ns1").
					DeviceRequests(testingdra.MakeFirstAvailableRequest("nic",
						testingdra.MakeDeviceSubRequest("all", "nic-a.example.com", 0).AllocationModeAll().Obj(),
						testingdra.MakeDeviceSubRequest("one", "nic-b.example.com", 1).Obj(),
					).Obj()).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeDeviceClass("nic-a.example.com").Obj(),
				testingdra.MakeDeviceClass("nic-b.example.com").Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-1", ResourceClaimTemplateName: new("claim-tmpl-1")},
					{Name: "req-nic", ResourceClaimTemplateName: new("claim-tmpl-nic-fa")},
				}
			},
			lookup:       defaultLookup,
			excluded:     []corev1.ResourceName{"nic-a.example.com", "nic-b.example.com"},
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPrioritizedList: true},
			want:         map[kueue.PodSetReference]corev1.ResourceList{"main": {"res-1": resource.MustParse("2")}},
		},
		"firstAvailable whose alternatives are all excluded skips the CEL device check": {
			extraObjects: []runtime.Object{
				utiltesting.MakeResourceClaimTemplate("claim-tmpl-nic-fa-cel", "ns1").
					DeviceRequests(testingdra.MakeFirstAvailableRequest("nic",
						testingdra.MakeDeviceSubRequest("a", "nic-a.example.com", 1).CELSelector(`device.attributes["test-driver"].nic == "true"`).Obj(),
						testingdra.MakeDeviceSubRequest("b", "nic-b.example.com", 1).CELSelector(`device.attributes["test-driver"].nic == "true"`).Obj(),
					).Obj()).
					Obj(),
				testingdra.MakeDeviceClass("test-deviceclass-1").Obj(),
				testingdra.MakeDeviceClass("nic-a.example.com").Obj(),
				testingdra.MakeDeviceClass("nic-b.example.com").Obj(),
				testingdra.MakeResourceSlice("slice-no-nic", "test-driver").
					Pool("pool-1", 1, 1).
					Device("dev-0").
					Attribute("test-driver/nic", "false").
					Obj(),
			},
			modifyWL: func(w *kueue.Workload) {
				w.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
					{Name: "req-1", ResourceClaimTemplateName: new("claim-tmpl-1")},
					{Name: "req-nic", ResourceClaimTemplateName: new("claim-tmpl-nic-fa-cel")},
				}
			},
			lookup:       defaultLookup,
			excluded:     []corev1.ResourceName{"nic-a.example.com", "nic-b.example.com"},
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPrioritizedList: true},
			want:         map[kueue.PodSetReference]corev1.ResourceList{"main": {"res-1": resource.MustParse("2")}},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.KueueDRAExcludeDeviceClasses, true)
			for fg, on := range tc.featureGates {
				features.SetFeatureGateDuringTest(t, fg, on)
			}
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
				if tc.mappings != nil {
					mappings = tc.mappings
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
			testMapper.SetExcludedDeviceClasses(tc.excluded)
			sliceCache := NewResourceSliceCache(baseClient)
			got, err := GetResourceRequestsForResourceClaimTemplates(ctx, baseClient, sliceCache, testMapper, wlCopy, tc.skipDeviceCheck)
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

func TestChargesForClaimSpec(t *testing.T) {
	mapper := NewResourceMapper()
	if err := mapper.PopulateFromConfiguration([]configapi.DeviceClassMapping{{
		Name:             "example.com/gpu",
		DeviceClassNames: []corev1.ResourceName{"fast.example.com", "slow.example.com"},
	}}); err != nil {
		t.Fatalf("PopulateFromConfiguration() = %v", err)
	}

	cases := map[string]struct {
		spec         resourcev1.ResourceClaimSpec
		featureGates map[featuregate.Feature]bool
		// perLogicalResource is always allocated, so an empty map is the expectation
		// when no prioritized list is charged; ToMap reports no class charges as nil.
		wantLogical map[corev1.ResourceName]resources.Amount
		wantClasses map[corev1.ResourceName]resources.Amount
		wantErr     bool
		// Set these when which error comes back is the point of the case, since
		// several guards on this path reject the same spec for different reasons.
		wantErrField string
		wantErrType  field.ErrorType
	}{
		"exactly requests on one class add up": {
			spec: utiltesting.NewResourceClaimSpecBuilder().
				DeviceRequests(
					testingdra.MakeDeviceRequest("r0", "gpu", 2).Obj(),
					testingdra.MakeDeviceRequest("r1", "gpu", 3).Obj(),
				).
				Build(),
			wantLogical: map[corev1.ResourceName]resources.Amount{},
			wantClasses: map[corev1.ResourceName]resources.Amount{"gpu": resources.NewAmount(5)},
		},
		"an exactly sum past MaxInt64 stays exact": {
			spec: utiltesting.NewResourceClaimSpecBuilder().
				DeviceRequests(
					testingdra.MakeDeviceRequest("r0", "gpu", math.MaxInt64).Obj(),
					testingdra.MakeDeviceRequest("r1", "gpu", math.MaxInt64).Obj(),
				).
				Build(),
			wantLogical: map[corev1.ResourceName]resources.Amount{},
			wantClasses: map[corev1.ResourceName]resources.Amount{"gpu": resources.NewAmount(math.MaxInt64).MulInt64(2)},
		},
		"with the gate off a prioritized list is still refused": {
			spec: utiltesting.NewResourceClaimSpecBuilder().
				DeviceRequests(
					testingdra.MakeFirstAvailableRequest("r", testingdra.MakeDeviceSubRequest("fast", "fast.example.com", 1).Obj()).Obj(),
				).
				Build(),
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPrioritizedList: false},
			wantErr:      true,
		},
		"independent requests add their own counts": {
			spec: utiltesting.NewResourceClaimSpecBuilder().
				DeviceRequests(
					testingdra.MakeFirstAvailableRequest("r0",
						testingdra.MakeDeviceSubRequest("fast", "fast.example.com", 3).Obj(),
						testingdra.MakeDeviceSubRequest("slow", "slow.example.com", 3).Obj(),
					).Obj(),
					testingdra.MakeFirstAvailableRequest("r1",
						testingdra.MakeDeviceSubRequest("fast", "fast.example.com", 5).Obj(),
						testingdra.MakeDeviceSubRequest("slow", "slow.example.com", 5).Obj(),
					).Obj(),
				).
				Build(),
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPrioritizedList: true},
			wantLogical:  map[corev1.ResourceName]resources.Amount{"example.com/gpu": resources.NewAmount(8)},
		},
		"a request whose alternatives differ in count refuses the whole claim": {
			spec: utiltesting.NewResourceClaimSpecBuilder().
				DeviceRequests(
					testingdra.MakeFirstAvailableRequest("r0",
						testingdra.MakeDeviceSubRequest("fast", "fast.example.com", 2).Obj(),
						testingdra.MakeDeviceSubRequest("slow", "slow.example.com", 2).Obj(),
					).Obj(),
					testingdra.MakeFirstAvailableRequest("r1",
						testingdra.MakeDeviceSubRequest("fast", "fast.example.com", 1).Obj(),
						testingdra.MakeDeviceSubRequest("slow", "slow.example.com", 3).Obj(),
					).Obj(),
				).
				Build(),
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPrioritizedList: true},
			wantErr:      true,
			wantErrField: "devices.requests[1].firstAvailable[1].count",
			wantErrType:  field.ErrorTypeInvalid,
		},
		"an Exactly request beside a prioritized list is counted once each": {
			spec: utiltesting.NewResourceClaimSpecBuilder().
				DeviceRequests(
					testingdra.MakeDeviceRequest("r0", "fast.example.com", 2).Obj(),
					testingdra.MakeFirstAvailableRequest("r1",
						testingdra.MakeDeviceSubRequest("fast", "fast.example.com", 4).Obj(),
						testingdra.MakeDeviceSubRequest("slow", "slow.example.com", 4).Obj(),
					).Obj(),
				).
				Build(),
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPrioritizedList: true},
			wantLogical:  map[corev1.ResourceName]resources.Amount{"example.com/gpu": resources.NewAmount(4)},
			wantClasses:  map[corev1.ResourceName]resources.Amount{"fast.example.com": resources.NewAmount(2)},
		},
		"a sum past the int64 range is kept exactly rather than saturated": {
			spec: utiltesting.NewResourceClaimSpecBuilder().
				DeviceRequests(
					testingdra.MakeFirstAvailableRequest("r0", testingdra.MakeDeviceSubRequest("fast", "fast.example.com", math.MaxInt64).Obj()).Obj(),
					testingdra.MakeFirstAvailableRequest("r1", testingdra.MakeDeviceSubRequest("fast", "fast.example.com", 1).Obj()).Obj(),
				).
				Build(),
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPrioritizedList: true},
			wantLogical:  map[corev1.ResourceName]resources.Amount{"example.com/gpu": resources.NewAmount(math.MaxInt64).AddInt64(1)},
		},
		// The claim is charged the sum of the per-request counts, so 1+4+7+6+9,
		// whatever alternative each request ends up with.
		"five requests with several alternatives each charge the sum of their counts": {
			spec: utiltesting.NewResourceClaimSpecBuilder().
				DeviceRequests(
					testingdra.MakeFirstAvailableRequest("r0", testingdra.MakeDeviceSubRequest("a", "fast.example.com", 1).Obj()).Obj(),
					testingdra.MakeFirstAvailableRequest("r1",
						testingdra.MakeDeviceSubRequest("a", "fast.example.com", 4).Obj(),
						testingdra.MakeDeviceSubRequest("b", "slow.example.com", 4).Obj(),
					).Obj(),
					testingdra.MakeFirstAvailableRequest("r2",
						testingdra.MakeDeviceSubRequest("a", "fast.example.com", 7).Obj(),
						testingdra.MakeDeviceSubRequest("b", "slow.example.com", 7).Obj(),
						testingdra.MakeDeviceSubRequest("c", "fast.example.com", 7).Obj(),
					).Obj(),
					testingdra.MakeFirstAvailableRequest("r3",
						testingdra.MakeDeviceSubRequest("a", "fast.example.com", 6).Obj(),
						testingdra.MakeDeviceSubRequest("b", "slow.example.com", 6).Obj(),
					).Obj(),
					testingdra.MakeFirstAvailableRequest("r4",
						testingdra.MakeDeviceSubRequest("a", "fast.example.com", 9).Obj(),
						testingdra.MakeDeviceSubRequest("b", "slow.example.com", 9).Obj(),
						testingdra.MakeDeviceSubRequest("c", "fast.example.com", 9).Obj(),
						testingdra.MakeDeviceSubRequest("d", "slow.example.com", 9).Obj(),
					).Obj(),
				).
				Build(),
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPrioritizedList: true},
			wantLogical:  map[corev1.ResourceName]resources.Amount{"example.com/gpu": resources.NewAmount(27)},
		},
		"an empty firstAvailable is reported against firstAvailable, not as a missing exactly": {
			spec: utiltesting.NewResourceClaimSpecBuilder().
				DeviceRequests(
					resourcev1.DeviceRequest{
						Name:           "r",
						FirstAvailable: []resourcev1.DeviceSubRequest{},
					},
				).
				Build(),
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationPrioritizedList: true},
			wantErr:      true,
			wantErrField: "devices.requests[0].firstAvailable",
			wantErrType:  field.ErrorTypeRequired,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			for fg, on := range tc.featureGates {
				features.SetFeatureGateDuringTest(t, fg, on)
			}
			got, errs := chargesForClaimSpec(&tc.spec, mapper)
			if tc.wantErr {
				if len(errs) == 0 {
					t.Fatalf("want an error, got %v", got.perLogicalResource)
				}
				if tc.wantErrField != "" {
					if len(errs) != 1 {
						t.Fatalf("want one error, got %v", errs)
					}
					if errs[0].Field != tc.wantErrField || errs[0].Type != tc.wantErrType {
						t.Errorf("got %v on %s, want %v on %s", errs[0].Type, errs[0].Field, tc.wantErrType, tc.wantErrField)
					}
				}
				return
			}
			if len(errs) != 0 {
				t.Fatalf("unexpected errors: %v", errs)
			}
			if diff := cmp.Diff(tc.wantLogical, got.perLogicalResource, cmp.Comparer(resources.Amount.Equal)); diff != "" {
				t.Errorf("logical charges (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantClasses, resources.ToMap(got.perDeviceClass)); diff != "" {
				t.Errorf("class charges (-want +got):\n%s", diff)
			}
		})
	}
}
