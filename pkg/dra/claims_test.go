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

package dra_test

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1beta2 "k8s.io/api/resource/v1beta2"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueuev1beta1 "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/dra"
)

func Test_GetResourceRequests(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = kueuev1beta1.AddToScheme(scheme)
	_ = resourcev1beta2.AddToScheme(scheme)

	tmpl := &resourcev1beta2.ResourceClaimTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "claim-tmpl-1", Namespace: "ns1"},
		Spec: resourcev1beta2.ResourceClaimTemplateSpec{
			Spec: resourcev1beta2.ResourceClaimSpec{
				Devices: resourcev1beta2.DeviceClaim{
					Requests: []resourcev1beta2.DeviceRequest{{
						Exactly: &resourcev1beta2.ExactDeviceRequest{
							AllocationMode:  resourcev1beta2.DeviceAllocationModeExactCount,
							Count:           2,
							DeviceClassName: "test-deviceclass-1",
						},
					}},
				},
			},
		},
	}

	claim := &resourcev1beta2.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "claim-2", Namespace: "ns1"},
		Spec: resourcev1beta2.ResourceClaimSpec{
			Devices: resourcev1beta2.DeviceClaim{
				Requests: []resourcev1beta2.DeviceRequest{{
					Exactly: &resourcev1beta2.ExactDeviceRequest{
						AllocationMode:  resourcev1beta2.DeviceAllocationModeExactCount,
						Count:           1,
						DeviceClassName: "test-deviceclass-2",
					},
				}},
			},
		},
	}

	wl := &kueuev1beta1.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "wl", Namespace: "ns1"},
		Spec: kueuev1beta1.WorkloadSpec{
			PodSets: []kueuev1beta1.PodSet{{
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

	tests := []struct {
		name         string
		modifyWL     func(w *kueuev1beta1.Workload)
		extraObjects []runtime.Object
		lookup       func(corev1.ResourceName) (corev1.ResourceName, bool)
		want         map[kueuev1beta1.PodSetReference]corev1.ResourceList
		wantErr      bool
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
			want: map[kueuev1beta1.PodSetReference]corev1.ResourceList{
				"main": {
					"res-1": resource.MustParse("2"),
				},
			},
		},
		{
			name:    "Unmapped DeviceClass returns error",
			lookup:  func(corev1.ResourceName) (corev1.ResourceName, bool) { return "", false },
			wantErr: true,
		},
		{
			name: "Two containers each using different claim templates",
			modifyWL: func(w *kueuev1beta1.Workload) {
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
				&resourcev1beta2.ResourceClaimTemplate{
					ObjectMeta: metav1.ObjectMeta{Name: "claim-tmpl-2", Namespace: "ns1"},
					Spec:       resourcev1beta2.ResourceClaimTemplateSpec{Spec: resourcev1beta2.ResourceClaimSpec{Devices: resourcev1beta2.DeviceClaim{Requests: []resourcev1beta2.DeviceRequest{{Exactly: &resourcev1beta2.ExactDeviceRequest{AllocationMode: resourcev1beta2.DeviceAllocationModeExactCount, Count: 1, DeviceClassName: "test-deviceclass-2"}}}}}},
				},
			},
			lookup: func(dc corev1.ResourceName) (corev1.ResourceName, bool) {
				m := map[corev1.ResourceName]corev1.ResourceName{"test-deviceclass-1": "res-1", "test-deviceclass-2": "res-2"}
				lr, ok := m[dc]
				return lr, ok
			},
			want: map[kueuev1beta1.PodSetReference]corev1.ResourceList{"main": {"res-1": resource.MustParse("2"), "res-2": resource.MustParse("1")}},
		},
		{
			name: "Two containers sharing one claim template",
			modifyWL: func(w *kueuev1beta1.Workload) {
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
			lookup: func(dc corev1.ResourceName) (corev1.ResourceName, bool) {
				if dc == "test-deviceclass-1" {
					return "res-1", true
				}
				return "", false
			},
			want: map[kueuev1beta1.PodSetReference]corev1.ResourceList{"main": {"res-1": resource.MustParse("2")}},
		},
		{
			name: "Single template requesting two devices",
			extraObjects: []runtime.Object{
				&resourcev1beta2.ResourceClaimTemplate{
					ObjectMeta: metav1.ObjectMeta{Name: "claim-tmpl-3", Namespace: "ns1"},
					Spec:       resourcev1beta2.ResourceClaimTemplateSpec{Spec: resourcev1beta2.ResourceClaimSpec{Devices: resourcev1beta2.DeviceClaim{Requests: []resourcev1beta2.DeviceRequest{{Exactly: &resourcev1beta2.ExactDeviceRequest{AllocationMode: resourcev1beta2.DeviceAllocationModeExactCount, Count: 2, DeviceClassName: "test-deviceclass-1"}}}}}},
				},
			},
			modifyWL: func(w *kueuev1beta1.Workload) {
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
			lookup: func(dc corev1.ResourceName) (corev1.ResourceName, bool) {
				if dc == "test-deviceclass-1" {
					return "res-1", true
				}
				return "", false
			},
			want: map[kueuev1beta1.PodSetReference]corev1.ResourceList{"main": {"res-1": resource.MustParse("2")}},
		},
		{
			name: "Init and regular container sharing one template",
			modifyWL: func(w *kueuev1beta1.Workload) {
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
			lookup: func(dc corev1.ResourceName) (corev1.ResourceName, bool) {
				if dc == "test-deviceclass-1" {
					return "res-1", true
				}
				return "", false
			},
			want: map[kueuev1beta1.PodSetReference]corev1.ResourceList{"main": {"res-1": resource.MustParse("2")}},
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
				err := dra.CreateMapperFromConfiguration(mappings)
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
			baseClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()

			wlCopy := wl.DeepCopy()
			if tc.modifyWL != nil {
				tc.modifyWL(wlCopy)
			}

			got, err := dra.GetResourceRequestsForResourceClaimTemplates(context.Background(), baseClient, wlCopy)
			if (err != nil) != tc.wantErr {
				t.Fatalf("unexpected error status: gotErr=%v wantErr=%v, err=%v", err != nil, tc.wantErr, err)
			}
			if tc.wantErr {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("unexpected result; got=%v want=%v", got, tc.want)
			}
		})
	}
}
