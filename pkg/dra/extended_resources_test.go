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
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utilresource "sigs.k8s.io/kueue/pkg/util/resource"
)

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

func TestGetResourceRequestsFromExtendedResources(t *testing.T) {
	// Create DeviceClasses with extendedResourceName
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

	// DeviceClass without extendedResourceName
	plainDeviceClass := &resourceapi.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "plain.nvidia.com",
		},
		Spec: resourceapi.DeviceClassSpec{
			// No ExtendedResourceName
		},
	}

	tests := []struct {
		name          string
		workload      *kueue.Workload
		deviceClasses []*resourceapi.DeviceClass
		mappings      []configapi.DeviceClassMapping
		want          map[kueue.PodSetReference]corev1.ResourceList
		wantReplaced  map[kueue.PodSetReference]sets.Set[corev1.ResourceName]
		wantErr       bool
		wantErrCount  int
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
			mappings: []configapi.DeviceClassMapping{
				{
					Name:             "gpu-quota",
					DeviceClassNames: []corev1.ResourceName{"gpu.nvidia.com"},
				},
			},
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {
					"gpu-quota": resource.MustParse("2"),
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
			mappings: []configapi.DeviceClassMapping{
				{
					Name:             "gpu-quota",
					DeviceClassNames: []corev1.ResourceName{"gpu.nvidia.com"},
				},
				{
					Name:             "mig-quota",
					DeviceClassNames: []corev1.ResourceName{"mig.nvidia.com"},
				},
			},
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {
					"gpu-quota": resource.MustParse("1"),
					"mig-quota": resource.MustParse("2"),
				},
			},
			wantReplaced: map[kueue.PodSetReference]sets.Set[corev1.ResourceName]{
				"main": sets.New[corev1.ResourceName]("example.com/gpu", "nvidia.com/mig-1g.10gb"),
			},
		},
		{
			name: "workload with extended resource not in deviceClassMappings",
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
			mappings:      []configapi.DeviceClassMapping{
				// No mapping for gpu.nvidia.com
			},
			wantErr:      true,
			wantErrCount: 1,
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
			deviceClasses: []*resourceapi.DeviceClass{gpuDeviceClass}, // only has example.com/gpu mapping
			mappings: []configapi.DeviceClassMapping{
				{
					Name:             "gpu-quota",
					DeviceClassNames: []corev1.ResourceName{"gpu.nvidia.com"},
				},
			},
			want: nil, // No resources matched
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
			mappings: []configapi.DeviceClassMapping{
				{
					Name:             "gpu-quota",
					DeviceClassNames: []corev1.ResourceName{"gpu.nvidia.com"},
				},
			},
			want: nil, // No extended resources to process
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
			deviceClasses: []*resourceapi.DeviceClass{plainDeviceClass}, // has no extendedResourceName
			mappings: []configapi.DeviceClassMapping{
				{
					Name:             "plain-quota",
					DeviceClassNames: []corev1.ResourceName{"plain.nvidia.com"},
				},
			},
			want: nil, // No mapping since DeviceClass has no extendedResourceName
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
			mappings: []configapi.DeviceClassMapping{
				{
					Name:             "gpu-quota",
					DeviceClassNames: []corev1.ResourceName{"gpu.nvidia.com"},
				},
			},
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {
					"gpu-quota": resource.MustParse("3"), // 1 + 2 = 3
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
			mappings: []configapi.DeviceClassMapping{
				{
					Name:             "gpu-quota",
					DeviceClassNames: []corev1.ResourceName{"gpu.nvidia.com"},
				},
			},
			// max(init) = max(5, 3) = 5
			// sum(regular) = 1 + 2 = 3
			// pod total = max(5, 3) = 5
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {
					"gpu-quota": resource.MustParse("5"),
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
											"example.com/gpu": resource.MustParse("500m"), // 0.5, non-integer
										},
									},
								}},
							},
						},
					}},
				},
			},
			deviceClasses: []*resourceapi.DeviceClass{gpuDeviceClass},
			mappings: []configapi.DeviceClassMapping{
				{
					Name:             "gpu-quota",
					DeviceClassNames: []corev1.ResourceName{"gpu.nvidia.com"},
				},
			},
			wantErr:      true,
			wantErrCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mapper with test mappings
			if len(tt.mappings) > 0 {
				_ = CreateMapperFromConfiguration(tt.mappings)
			} else {
				// Reset mapper
				globalMapper = newDRAResourceMapper()
			}

			// Setup DeviceClass cache and populate it directly
			cache := NewDeviceClassCache()

			// Add DeviceClasses to cache (simulating what controller would do)
			for _, dc := range tt.deviceClasses {
				cache.AddOrUpdate(dc)
			}

			got, gotReplaced, errs := GetResourceRequestsFromExtendedResources(tt.workload, cache)

			if tt.wantErr {
				if len(errs) == 0 {
					t.Errorf("GetResourceRequestsFromExtendedResources() expected error, got none")
				}
				if tt.wantErrCount > 0 && len(errs) != tt.wantErrCount {
					t.Errorf("GetResourceRequestsFromExtendedResources() expected %d errors, got %d: %v", tt.wantErrCount, len(errs), errs)
				}
				return
			}

			if len(errs) > 0 {
				t.Errorf("GetResourceRequestsFromExtendedResources() unexpected errors: %v", errs)
				return
			}

			opts := []cmp.Option{
				cmpopts.EquateEmpty(),
			}
			if diff := cmp.Diff(tt.want, got, opts...); diff != "" {
				t.Errorf("GetResourceRequestsFromExtendedResources() resources mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantReplaced, gotReplaced, opts...); diff != "" {
				t.Errorf("GetResourceRequestsFromExtendedResources() replacedExtendedResources mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestHasExtendedResourcesBackedByDRA(t *testing.T) {
	gpuDeviceClass := &resourceapi.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gpu.nvidia.com",
		},
		Spec: resourceapi.DeviceClassSpec{
			ExtendedResourceName: ptr.To("example.com/gpu"),
		},
	}

	tests := []struct {
		name          string
		workload      *kueue.Workload
		deviceClasses []*resourceapi.DeviceClass
		want          bool
	}{
		{
			name: "has extended resource backed by DRA",
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
			want:          true,
		},
		{
			name: "has extended resource NOT backed by DRA",
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
											"other.vendor/resource": resource.MustParse("1"),
										},
									},
								}},
							},
						},
					}},
				},
			},
			deviceClasses: []*resourceapi.DeviceClass{gpuDeviceClass},
			want:          false,
		},
		{
			name: "no extended resources",
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
										},
									},
								}},
							},
						},
					}},
				},
			},
			deviceClasses: []*resourceapi.DeviceClass{gpuDeviceClass},
			want:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := NewDeviceClassCache()

			// Add DeviceClasses to cache (simulating what controller would do)
			for _, dc := range tt.deviceClasses {
				cache.AddOrUpdate(dc)
			}

			got := HasExtendedResourcesBackedByDRA(tt.workload, cache)
			if got != tt.want {
				t.Errorf("HasExtendedResourcesBackedByDRA() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDeviceClassCache(t *testing.T) {
	gpuDeviceClass := &resourceapi.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gpu.nvidia.com",
		},
		Spec: resourceapi.DeviceClassSpec{
			ExtendedResourceName: ptr.To("example.com/gpu"),
		},
	}

	plainDeviceClass := &resourceapi.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "plain.nvidia.com",
		},
		Spec: resourceapi.DeviceClassSpec{
			// No ExtendedResourceName
		},
	}

	cache := NewDeviceClassCache()

	// Test before adding any DeviceClass
	if _, found := cache.GetDeviceClass("example.com/gpu"); found {
		t.Error("expected no mapping before adding DeviceClass")
	}

	// Add DeviceClass with extendedResourceName
	cache.AddOrUpdate(gpuDeviceClass)

	// Test GetDeviceClass
	if className, found := cache.GetDeviceClass("example.com/gpu"); !found {
		t.Error("expected mapping for example.com/gpu after adding DeviceClass")
	} else if className != "gpu.nvidia.com" {
		t.Errorf("GetDeviceClass() = %s, want gpu.nvidia.com", className)
	}

	// Test unknown extended resource
	if _, found := cache.GetDeviceClass("unknown/resource"); found {
		t.Error("expected no mapping for unknown extended resource")
	}

	// Test GetExtendedResource
	if extRes, found := cache.GetExtendedResource("gpu.nvidia.com"); !found {
		t.Error("expected reverse mapping for gpu.nvidia.com")
	} else if extRes != "example.com/gpu" {
		t.Errorf("GetExtendedResource() = %s, want example.com/gpu", extRes)
	}

	// Add DeviceClass without extendedResourceName
	cache.AddOrUpdate(plainDeviceClass)

	// Test DeviceClass without extendedResourceName
	if _, found := cache.GetExtendedResource("plain.nvidia.com"); found {
		t.Error("expected no reverse mapping for plain.nvidia.com (no extendedResourceName)")
	}

	// Test GetDeviceClass for mapping existence
	if _, found := cache.GetDeviceClass("example.com/gpu"); !found {
		t.Error("GetDeviceClass() = false for example.com/gpu, want true")
	}
	if _, found := cache.GetDeviceClass("unknown/resource"); found {
		t.Error("GetDeviceClass() = true for unknown/resource, want false")
	}

	// Test Delete
	cache.Delete(gpuDeviceClass)
	if _, found := cache.GetDeviceClass("example.com/gpu"); found {
		t.Error("expected no mapping after deleting DeviceClass")
	}

	// Test updating DeviceClass with changed extendedResourceName
	gpuDeviceClass2 := &resourceapi.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gpu.nvidia.com",
		},
		Spec: resourceapi.DeviceClassSpec{
			ExtendedResourceName: ptr.To("new.example.com/gpu"),
		},
	}
	cache.AddOrUpdate(gpuDeviceClass)
	cache.AddOrUpdate(gpuDeviceClass2)

	// Old mapping should be gone
	if _, found := cache.GetDeviceClass("example.com/gpu"); found {
		t.Error("expected old mapping to be removed after update")
	}
	// New mapping should exist
	if className, found := cache.GetDeviceClass("new.example.com/gpu"); !found {
		t.Error("expected new mapping after update")
	} else if className != "gpu.nvidia.com" {
		t.Errorf("GetDeviceClass() = %s, want gpu.nvidia.com", className)
	}
}
