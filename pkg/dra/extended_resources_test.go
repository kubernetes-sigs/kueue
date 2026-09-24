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
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
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

func TestSelectedDeviceClass(t *testing.T) {
	at := func(sec int64) metav1.Time { return metav1.Unix(sec, 0) }
	class := func(name string, created metav1.Time) resourceapi.DeviceClass {
		return resourceapi.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: created}}
	}
	cases := map[string]struct {
		items []resourceapi.DeviceClass
		want  string
	}{
		"one class is the one": {
			items: []resourceapi.DeviceClass{class("a", at(100))},
			want:  "a",
		},
		"the class created later wins": {
			items: []resourceapi.DeviceClass{class("a", at(100)), class("b", at(200))},
			want:  "b",
		},
		"and wins from either end of the list": {
			items: []resourceapi.DeviceClass{class("b", at(200)), class("a", at(100))},
			want:  "b",
		},
		"created together, the lexicographically first name wins": {
			items: []resourceapi.DeviceClass{class("b", at(100)), class("a", at(100))},
			want:  "a",
		},
		"and that tie does not depend on the list order either": {
			items: []resourceapi.DeviceClass{class("a", at(100)), class("b", at(100))},
			want:  "a",
		},
		"a later class beats an earlier one with a smaller name": {
			items: []resourceapi.DeviceClass{class("a", at(100)), class("z", at(200))},
			want:  "z",
		},
		"the tie-break applies among the latest only": {
			items: []resourceapi.DeviceClass{class("a", at(300)), class("z", at(100)), class("b", at(300))},
			want:  "a",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := selectedDeviceClass(tc.items); got.Name != tc.want {
				t.Errorf("selectedDeviceClass() = %q, want %q", got.Name, tc.want)
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
			ExtendedResourceName: new("example.com/gpu"),
		},
	}

	migDeviceClass := &resourceapi.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mig.nvidia.com",
		},
		Spec: resourceapi.DeviceClassSpec{
			ExtendedResourceName: new("nvidia.com/mig-1g.10gb"),
		},
	}

	plainDeviceClass := &resourceapi.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "plain.nvidia.com",
		},
		Spec: resourceapi.DeviceClassSpec{},
	}

	// Two classes on one extendedResourceName. The names sort against the
	// timestamps, so only the creation order can explain the class picked.
	alphaDeviceClass := &resourceapi.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "alpha.example.com",
			CreationTimestamp: metav1.Unix(100, 0),
		},
		Spec: resourceapi.DeviceClassSpec{
			ExtendedResourceName: new("example.com/gpu"),
		},
	}

	omegaDeviceClass := &resourceapi.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "omega.example.com",
			CreationTimestamp: metav1.Unix(200, 0),
		},
		Spec: resourceapi.DeviceClassSpec{
			ExtendedResourceName: new("example.com/gpu"),
		},
	}

	// Two distinct extendedResourceNames, both mapped by the same deviceClassMappings
	// entry to the logical key "gpu-claims".
	classADeviceClass := &resourceapi.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "class-a",
		},
		Spec: resourceapi.DeviceClassSpec{
			ExtendedResourceName: new("vendor.example/a"),
		},
	}

	classBDeviceClass := &resourceapi.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "class-b",
		},
		Spec: resourceapi.DeviceClassSpec{
			ExtendedResourceName: new("vendor.example/b"),
		},
	}

	tests := []struct {
		name           string
		workload       *kueue.Workload
		deviceClasses  []*resourceapi.DeviceClass
		mapperMappings []configapi.DeviceClassMapping
		enablePD       bool
		want           map[kueue.PodSetReference]corev1.ResourceList
		wantReplaced   map[kueue.PodSetReference]sets.Set[corev1.ResourceName]
		wantErr        field.ErrorList
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
			name: "workload with negative extended resource request is not charged",
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
											"example.com/gpu": resource.MustParse("-3"),
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
			name: "workload with fractional quantity for extended resource not backed by DRA (no matching DeviceClass)",
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
											"other.vendor.io/resource": resource.MustParse("1500m"),
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
			name: "ordinary init containers use max, regular containers use sum",
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
			// The scheduler keeps a sidecar's devices for as long as the regular
			// containers hold theirs, so the two are held at once.
			name: "a restartable init container adds to the total rather than being maxed against it",
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
										Name:          "sidecar",
										Image:         "pause",
										RestartPolicy: new(corev1.ContainerRestartPolicyAlways),
										Resources: corev1.ResourceRequirements{
											Requests: corev1.ResourceList{
												"example.com/gpu": resource.MustParse("1"),
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
								},
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
			// A non-positive request is dropped before any aggregation runs, so the
			// sidecar contributes nothing rather than being subtracted from the
			// regular container it now shares the long-running total with.
			name: "a negative restartable init container does not reduce the regular container's charge",
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
										Name:          "sidecar",
										Image:         "pause",
										RestartPolicy: new(corev1.ContainerRestartPolicyAlways),
										Resources: corev1.ResourceRequirements{
											Requests: corev1.ResourceList{
												"example.com/gpu": resource.MustParse("-3"),
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
												"example.com/gpu": resource.MustParse("8"),
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
					"example.com/gpu": resource.MustParse("8"),
				},
			},
			wantReplaced: map[kueue.PodSetReference]sets.Set[corev1.ResourceName]{
				"main": sets.New[corev1.ResourceName]("example.com/gpu"),
			},
		},
		{
			// The init container runs with the sidecar declared before it already up,
			// so 5 and 2 together beat the 1 and 2 that follow.
			name: "an ordinary init container is measured with the sidecar already running",
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
										Name:          "sidecar",
										Image:         "pause",
										RestartPolicy: new(corev1.ContainerRestartPolicyAlways),
										Resources: corev1.ResourceRequirements{
											Requests: corev1.ResourceList{
												"example.com/gpu": resource.MustParse("2"),
											},
										},
									},
									{
										Name:  "init1",
										Image: "pause",
										Resources: corev1.ResourceRequirements{
											Requests: corev1.ResourceList{
												"example.com/gpu": resource.MustParse("5"),
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
								},
							},
						},
					}},
				},
			},
			deviceClasses: []*resourceapi.DeviceClass{gpuDeviceClass},
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {
					"example.com/gpu": resource.MustParse("7"),
				},
			},
			wantReplaced: map[kueue.PodSetReference]sets.Set[corev1.ResourceName]{
				"main": sets.New[corev1.ResourceName]("example.com/gpu"),
			},
		},
		{
			// The same two init containers the other way round. Nothing is running
			// beside the ordinary one this time, so its own 5 stands against the 2
			// and 1 that outlive it.
			name: "an ordinary init container declared before the sidecar does not run with it",
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
										Name:          "sidecar",
										Image:         "pause",
										RestartPolicy: new(corev1.ContainerRestartPolicyAlways),
										Resources: corev1.ResourceRequirements{
											Requests: corev1.ResourceList{
												"example.com/gpu": resource.MustParse("2"),
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
			// vendor.example/a and vendor.example/b both map to the "gpu-claims" quota
			// key, but each is charged against the OTHER container kind (init vs.
			// regular), so the max/sum aggregation only ever sees one contribution
			// per key if the mapping happens before aggregation across containers.
			// The correct charge is the sum of each name's own Pod aggregation:
			// max(A's init contribution, A's regular contribution) is 5 for A alone,
			// and likewise 5 for B alone, so the quota key must be charged 10, not
			// max(5, 5) = 5.
			name: "two extended resource names sharing a quota key are not collapsed by cross-container aggregation",
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
										Name:  "init",
										Image: "pause",
										Resources: corev1.ResourceRequirements{
											Requests: corev1.ResourceList{
												"vendor.example/a": resource.MustParse("5"),
											},
										},
									},
								},
								Containers: []corev1.Container{
									{
										Name:  "c",
										Image: "pause",
										Resources: corev1.ResourceRequirements{
											Requests: corev1.ResourceList{
												"vendor.example/b": resource.MustParse("5"),
											},
										},
									},
								},
							},
						},
					}},
				},
			},
			deviceClasses: []*resourceapi.DeviceClass{classADeviceClass, classBDeviceClass},
			mapperMappings: []configapi.DeviceClassMapping{
				{
					Name:             "gpu-claims",
					DeviceClassNames: []corev1.ResourceName{"class-a", "class-b"},
				},
			},
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {
					"gpu-claims": resource.MustParse("10"),
				},
			},
			wantReplaced: map[kueue.PodSetReference]sets.Set[corev1.ResourceName]{
				"main": sets.New[corev1.ResourceName]("vendor.example/a", "vendor.example/b"),
			},
		},
		{
			// vendor.example/a and vendor.example/b share the "gpu-claims" quota key.
			// The negative request for b must be dropped before aggregation, not
			// merged in and left to offset a's positive charge.
			name: "positive and negative extended resource names sharing a quota key: negative does not offset positive",
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
											"vendor.example/a": resource.MustParse("5"),
											"vendor.example/b": resource.MustParse("-3"),
										},
									},
								}},
							},
						},
					}},
				},
			},
			deviceClasses: []*resourceapi.DeviceClass{classADeviceClass, classBDeviceClass},
			mapperMappings: []configapi.DeviceClassMapping{
				{
					Name:             "gpu-claims",
					DeviceClassNames: []corev1.ResourceName{"class-a", "class-b"},
				},
			},
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {
					"gpu-claims": resource.MustParse("5"),
				},
			},
			wantReplaced: map[kueue.PodSetReference]sets.Set[corev1.ResourceName]{
				"main": sets.New[corev1.ResourceName]("vendor.example/a"),
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
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "podSets").Index(0).
						Child("template", "spec", "containers").Index(0).
						Child("resources", "requests", "example.com/gpu"),
					"",
					"",
				),
			},
		},
		{
			// Each container's quantity fits int64 on its own, but their sum
			// overflows it. Charging the aggregate without re-checking would
			// silently charge nothing instead of rejecting the request.
			name: "workload with per-container integer quantities that overflow int64 when summed",
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
												"example.com/gpu": resource.MustParse("9e18"),
											},
										},
									},
									{
										Name:  "c2",
										Image: "pause",
										Resources: corev1.ResourceRequirements{
											Requests: corev1.ResourceList{
												"example.com/gpu": resource.MustParse("9e18"),
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
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "podSets").Index(0).
						Child("template", "spec", "containers").Index(0).
						Child("resources", "requests", "example.com/gpu"),
					"",
					"",
				),
			},
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
		{
			name:     "extended resource with counters is rejected",
			enablePD: true,
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
					Name:             "gpu.memory",
					DeviceClassNames: []corev1.ResourceName{"gpu.nvidia.com"},
					Sources: []configapi.DeviceClassSourceConfig{
						{Counter: &configapi.DeviceClassCounterSource{
							Name:   "memory",
							Driver: "gpu.nvidia.com",
							DeviceSelector: resourceapi.DeviceSelector{
								CEL: &resourceapi.CELDeviceSelector{
									Expression: "device.driver == 'gpu.nvidia.com'",
								},
							},
						}},
					},
				},
			},
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "podSets").Index(0).Child("template", "spec", "containers").Index(0),
					"", "",
				),
			},
		},
		{
			name: "the mapping of the later DeviceClass decides the quota key",
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
			deviceClasses: []*resourceapi.DeviceClass{alphaDeviceClass, omegaDeviceClass},
			mapperMappings: []configapi.DeviceClassMapping{
				{
					Name:             "alpha-claims",
					DeviceClassNames: []corev1.ResourceName{"alpha.example.com"},
				},
				{
					Name:             "omega-claims",
					DeviceClassNames: []corev1.ResourceName{"omega.example.com"},
				},
			},
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {
					"omega-claims": resource.MustParse("1"),
				},
			},
			wantReplaced: map[kueue.PodSetReference]sets.Set[corev1.ResourceName]{
				"main": sets.New[corev1.ResourceName]("example.com/gpu"),
			},
		},
		{
			name: "an unmapped later DeviceClass leaves the extended resource name as the quota key",
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
			deviceClasses: []*resourceapi.DeviceClass{alphaDeviceClass, omegaDeviceClass},
			mapperMappings: []configapi.DeviceClassMapping{
				{
					Name:             "alpha-claims",
					DeviceClassNames: []corev1.ResourceName{"alpha.example.com"},
				},
			},
			want: map[kueue.PodSetReference]corev1.ResourceList{
				"main": {
					"example.com/gpu": resource.MustParse("1"),
				},
			},
			wantReplaced: map[kueue.PodSetReference]sets.Set[corev1.ResourceName]{
				"main": sets.New[corev1.ResourceName]("example.com/gpu"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.enablePD {
				features.SetFeatureGateDuringTest(t, features.KueueDRAIntegrationPartitionableDevices, true)
			}
			mapper := NewResourceMapper()
			if tt.mapperMappings != nil {
				_ = mapper.PopulateFromConfiguration(tt.mapperMappings)
			}

			cl := newFakeClient(tt.deviceClasses...)

			got, gotReplaced, errs := ResolveExtendedResourceQuota(t.Context(), cl, mapper, tt.workload)

			if diff := cmp.Diff(tt.wantErr, errs, cmpopts.IgnoreFields(field.Error{}, "Detail", "BadValue")); diff != "" {
				t.Errorf("ResolveExtendedResourceQuota() error mismatch (-want +got):\n%s", diff)
			}

			if errs == nil {
				opts := []cmp.Option{
					cmpopts.EquateEmpty(),
				}
				if diff := cmp.Diff(tt.want, got, opts...); diff != "" {
					t.Errorf("ResolveExtendedResourceQuota() resources mismatch (-want +got):\n%s", diff)
				}
				if diff := cmp.Diff(tt.wantReplaced, gotReplaced, opts...); diff != "" {
					t.Errorf("ResolveExtendedResourceQuota() replacedExtendedResources mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func TestNeedsDRAReconcile(t *testing.T) {
	tests := []struct {
		name            string
		workload        *kueue.Workload
		cachedResources map[corev1.ResourceName]string
		draGate         bool
		erGate          bool
		want            bool
	}{
		{
			name: "workload with RCT always needs DRA reconcile",
			workload: &kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{{
						Name:  "main",
						Count: 1,
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{Name: "c"}},
								ResourceClaims: []corev1.PodResourceClaim{{
									Name:                      "gpu",
									ResourceClaimTemplateName: new("gpu-template"),
								}},
							},
						},
					}},
				},
			},
			draGate: true,
			erGate:  true,
			want:    true,
		},
		{
			name: "extended resource not in cache returns false",
			workload: &kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{{
						Name:  "main",
						Count: 1,
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name: "c",
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
			draGate: true,
			erGate:  true,
			want:    false,
		},
		{
			name: "extended resource in cache returns true",
			workload: &kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{{
						Name:  "main",
						Count: 1,
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name: "c",
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
			cachedResources: map[corev1.ResourceName]string{
				"example.com/gpu": "gpu.example.com",
			},
			draGate: true,
			erGate:  true,
			want:    true,
		},
		{
			name: "DRA gate disabled returns false",
			workload: &kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{{
						Name:  "main",
						Count: 1,
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name: "c",
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
			cachedResources: map[corev1.ResourceName]string{
				"example.com/gpu": "gpu.example.com",
			},
			draGate: false,
			erGate:  false,
			want:    false,
		},
		{
			name: "ER gate disabled returns false for extended resource",
			workload: &kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{{
						Name:  "main",
						Count: 1,
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name: "c",
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
			cachedResources: map[corev1.ResourceName]string{
				"example.com/gpu": "gpu.example.com",
			},
			draGate: true,
			erGate:  false,
			want:    false,
		},
		{
			name: "cpu and memory only returns false",
			workload: &kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{{
						Name:  "main",
						Count: 1,
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name: "c",
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
			draGate: true,
			erGate:  true,
			want:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.KueueDRAIntegration, tc.draGate)
			features.SetFeatureGateDuringTest(t, features.KueueDRAIntegrationExtendedResource, tc.erGate)
			cache := NewExtendedResourceCache()
			for resName, dcName := range tc.cachedResources {
				cache.Add(resName, dcName)
			}

			got := NeedsDRAReconcile(tc.workload, cache)
			if got != tc.want {
				t.Errorf("NeedsDRAReconcile() = %v, want %v", got, tc.want)
			}
		})
	}
}
