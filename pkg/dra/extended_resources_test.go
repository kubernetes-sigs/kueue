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

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utilresource "sigs.k8s.io/kueue/pkg/util/resource"
)

func newFakeClient(deviceClasses ...*resourceapi.DeviceClass) client.Client {
	scheme := runtime.NewScheme()
	_ = resourceapi.AddToScheme(scheme)

	builder := fake.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&resourceapi.DeviceClass{}, "spec.extendedResourceName",
			func(obj client.Object) []string {
				dc := obj.(*resourceapi.DeviceClass)
				if dc.Spec.ExtendedResourceName == nil || *dc.Spec.ExtendedResourceName == "" {
					return nil
				}
				return []string{*dc.Spec.ExtendedResourceName}
			})

	for _, dc := range deviceClasses {
		builder = builder.WithObjects(dc)
	}
	return builder.Build()
}

func TestIsExtendedResourceName(t *testing.T) {
	tests := []struct {
		name     string
		resource corev1.ResourceName
		want     bool
	}{
		{
			name:     "cpu is not extended resource",
			resource: corev1.ResourceCPU,
			want:     false,
		},
		{
			name:     "memory is not extended resource",
			resource: corev1.ResourceMemory,
			want:     false,
		},
		{
			name:     "ephemeral-storage is not extended resource",
			resource: corev1.ResourceEphemeralStorage,
			want:     false,
		},
		{
			name:     "nvidia.com/gpu is extended resource",
			resource: "nvidia.com/gpu",
			want:     true,
		},
		{
			name:     "example.com/gpu is extended resource",
			resource: "example.com/gpu",
			want:     true,
		},
		{
			name:     "unqualified name is not extended resource",
			resource: "custom-resource",
			want:     false,
		},
		{
			name:     "hugepages is not extended resource",
			resource: "hugepages-2Mi",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := utilresource.IsExtendedResourceName(tt.resource)
			if got != tt.want {
				t.Errorf("utilresource.IsExtendedResourceName(%s) = %v, want %v", tt.resource, got, tt.want)
			}
		})
	}
}

func TestResolveExtendedResourceQuota(t *testing.T) {
	gpuDeviceClass := &resourceapi.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gpu.nvidia.com",
		},
		Spec: resourceapi.DeviceClassSpec{
			ExtendedResourceName: ptr.To("example.com/gpu"),
		},
	}

	migDeviceClass := &resourceapi.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mig.nvidia.com",
		},
		Spec: resourceapi.DeviceClassSpec{
			ExtendedResourceName: ptr.To("nvidia.com/mig-1g.10gb"),
		},
	}

	plainDeviceClass := &resourceapi.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "plain.nvidia.com",
		},
		Spec: resourceapi.DeviceClassSpec{},
	}

	tests := []struct {
		name           string
		workload       *kueue.Workload
		deviceClasses  []*resourceapi.DeviceClass
		mapperMappings []configapi.DeviceClassMapping
		want           map[kueue.PodSetReference]corev1.ResourceList
		wantReplaced   map[kueue.PodSetReference]sets.Set[corev1.ResourceName]
		wantErr        bool
		wantErrCount   int
	}{
		{
			name: "workload with extended resource backed by DRA",
			workload: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{Name: "wl", Namespace: "ns1"},
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{{
						Name:  "main",
						Count: 1,
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name:  "c",
									Image: "pause",
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourceCPU: resource.MustParse("1"),
											"example.com/gpu":  resource.MustParse("2"),
										},
									},
								}},
							},
						},
					}},
				},
			},
			deviceClasses: []*resourceapi.DeviceClass{gpuDeviceClass},
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {
					"example.com/gpu": resource.MustParse("2"),
				},
			},
			wantReplaced: map[kueue.PodSetReference]sets.Set[corev1.ResourceName]{
				"main": sets.New[corev1.ResourceName]("example.com/gpu"),
			},
		},
		{
			name: "workload with multiple extended resources",
			workload: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{Name: "wl", Namespace: "ns1"},
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{{
						Name:  "main",
						Count: 1,
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name:  "c",
									Image: "pause",
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											"example.com/gpu":        resource.MustParse("1"),
											"nvidia.com/mig-1g.10gb": resource.MustParse("2"),
										},
									},
								}},
							},
						},
					}},
				},
			},
			deviceClasses: []*resourceapi.DeviceClass{gpuDeviceClass, migDeviceClass},
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {
					"example.com/gpu":        resource.MustParse("1"),
					"nvidia.com/mig-1g.10gb": resource.MustParse("2"),
				},
			},
			wantReplaced: map[kueue.PodSetReference]sets.Set[corev1.ResourceName]{
				"main": sets.New[corev1.ResourceName]("example.com/gpu", "nvidia.com/mig-1g.10gb"),
			},
		},
		{
			name: "workload with extended resource not backed by DRA (no matching DeviceClass)",
			workload: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{Name: "wl", Namespace: "ns1"},
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{{
						Name:  "main",
						Count: 1,
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name:  "c",
									Image: "pause",
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											"other.vendor.io/resource": resource.MustParse("1"),
										},
									},
								}},
							},
						},
					}},
				},
			},
			deviceClasses: []*resourceapi.DeviceClass{gpuDeviceClass},
			want:          nil,
		},
		{
			name: "workload with no extended resources",
			workload: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{Name: "wl", Namespace: "ns1"},
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{{
						Name:  "main",
						Count: 1,
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name:  "c",
									Image: "pause",
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourceCPU:    resource.MustParse("1"),
											corev1.ResourceMemory: resource.MustParse("1Gi"),
										},
									},
								}},
							},
						},
					}},
				},
			},
			deviceClasses: []*resourceapi.DeviceClass{gpuDeviceClass},
			want:          nil,
		},
		{
			name: "workload with DeviceClass that has no extendedResourceName",
			workload: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{Name: "wl", Namespace: "ns1"},
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{{
						Name:  "main",
						Count: 1,
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name:  "c",
									Image: "pause",
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											"some.other/resource": resource.MustParse("1"),
										},
									},
								}},
							},
						},
					}},
				},
			},
			deviceClasses: []*resourceapi.DeviceClass{plainDeviceClass},
			want:          nil,
		},
		{
			name: "workload with multiple containers",
			workload: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{Name: "wl", Namespace: "ns1"},
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{{
						Name:  "main",
						Count: 1,
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Name:  "c1",
										Image: "pause",
										Resources: corev1.ResourceRequirements{
											Requests: corev1.ResourceList{
												"example.com/gpu": resource.MustParse("1"),
											},
										},
									},
									{
										Name:  "c2",
										Image: "pause",
										Resources: corev1.ResourceRequirements{
											Requests: corev1.ResourceList{
												"example.com/gpu": resource.MustParse("2"),
											},
										},
									},
								},
							},
						},
					}},
				},
			},
			deviceClasses: []*resourceapi.DeviceClass{gpuDeviceClass},
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {
					"example.com/gpu": resource.MustParse("3"),
				},
			},
			wantReplaced: map[kueue.PodSetReference]sets.Set[corev1.ResourceName]{
				"main": sets.New[corev1.ResourceName]("example.com/gpu"),
			},
		},
		{
			name: "init containers use max, regular containers use sum",
			workload: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{Name: "wl", Namespace: "ns1"},
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{{
						Name:  "main",
						Count: 1,
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								InitContainers: []corev1.Container{
									{
										Name:  "init1",
										Image: "pause",
										Resources: corev1.ResourceRequirements{
											Requests: corev1.ResourceList{
												"example.com/gpu": resource.MustParse("5"),
											},
										},
									},
									{
										Name:  "init2",
										Image: "pause",
										Resources: corev1.ResourceRequirements{
											Requests: corev1.ResourceList{
												"example.com/gpu": resource.MustParse("3"),
											},
										},
									},
								},
								Containers: []corev1.Container{
									{
										Name:  "c1",
										Image: "pause",
										Resources: corev1.ResourceRequirements{
											Requests: corev1.ResourceList{
												"example.com/gpu": resource.MustParse("1"),
											},
										},
									},
									{
										Name:  "c2",
										Image: "pause",
										Resources: corev1.ResourceRequirements{
											Requests: corev1.ResourceList{
												"example.com/gpu": resource.MustParse("2"),
											},
										},
									},
								},
							},
						},
					}},
				},
			},
			deviceClasses: []*resourceapi.DeviceClass{gpuDeviceClass},
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {
					"example.com/gpu": resource.MustParse("5"),
				},
			},
			wantReplaced: map[kueue.PodSetReference]sets.Set[corev1.ResourceName]{
				"main": sets.New[corev1.ResourceName]("example.com/gpu"),
			},
		},
		{
			name: "workload with non-integer extended resource quantity",
			workload: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{Name: "wl", Namespace: "ns1"},
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{{
						Name:  "main",
						Count: 1,
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name:  "c",
									Image: "pause",
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											"example.com/gpu": resource.MustParse("500m"),
										},
									},
								}},
							},
						},
					}},
				},
			},
			deviceClasses: []*resourceapi.DeviceClass{gpuDeviceClass},
			wantErr:       true,
			wantErrCount:  1,
		},
		{
			name: "extended resource uses deviceClassMappings logical name when DeviceClass is mapped",
			workload: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{Name: "wl", Namespace: "ns1"},
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{{
						Name:  "main",
						Count: 1,
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name:  "c",
									Image: "pause",
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											"example.com/gpu": resource.MustParse("1"),
										},
									},
								}},
							},
						},
					}},
				},
			},
			deviceClasses: []*resourceapi.DeviceClass{gpuDeviceClass},
			mapperMappings: []configapi.DeviceClassMapping{
				{
					Name:             "gpu-claims",
					DeviceClassNames: []corev1.ResourceName{"gpu.nvidia.com"},
				},
			},
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {
					"gpu-claims": resource.MustParse("1"),
				},
			},
			wantReplaced: map[kueue.PodSetReference]sets.Set[corev1.ResourceName]{
				"main": sets.New[corev1.ResourceName]("example.com/gpu"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.mapperMappings != nil {
				_ = CreateMapperFromConfiguration(tt.mapperMappings)
				t.Cleanup(func() { _ = CreateMapperFromConfiguration(nil) })
			}

			cl := newFakeClient(tt.deviceClasses...)

			got, gotReplaced, errs := ResolveExtendedResourceQuota(t.Context(), cl, tt.workload)

			if tt.wantErr {
				if len(errs) == 0 {
					t.Errorf("ResolveExtendedResourceQuota() expected error, got none")
				}
				if tt.wantErrCount > 0 && len(errs) != tt.wantErrCount {
					t.Errorf("ResolveExtendedResourceQuota() expected %d errors, got %d: %v", tt.wantErrCount, len(errs), errs)
				}
				return
			}

			if len(errs) > 0 {
				t.Errorf("ResolveExtendedResourceQuota() unexpected errors: %v", errs)
				return
			}

			opts := []cmp.Option{
				cmpopts.EquateEmpty(),
			}
			if diff := cmp.Diff(tt.want, got, opts...); diff != "" {
				t.Errorf("ResolveExtendedResourceQuota() resources mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantReplaced, gotReplaced, opts...); diff != "" {
				t.Errorf("ResolveExtendedResourceQuota() replacedExtendedResources mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
