/*
Copyright 2023 The Kubernetes Authors.
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

package workload

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestAdjustResources(t *testing.T) {
	cases := map[string]struct {
		runtimeClasses []nodev1.RuntimeClass
		limitranges    []corev1.LimitRange
		wl             *kueue.Workload
		wantWl         *kueue.Workload
	}{
		"Handle runtimeClass with podOverHead": {
			runtimeClasses: []nodev1.RuntimeClass{
				utiltesting.MakeRuntimeClass("runtime-a", "handler-a").
					PodOverhead(corev1.ResourceList{
						corev1.ResourceCPU:    resources.ResourceQuantity(corev1.ResourceCPU, 1),
						corev1.ResourceMemory: resources.ResourceQuantity(corev1.ResourceMemory, 1024),
					}).
					RuntimeClass,
			},
			wl: utiltesting.MakeWorkload("foo", "").
				PodSets(
					*utiltesting.MakePodSet("a", 1).
						RuntimeClass("runtime-a").
						Obj(),
					*utiltesting.MakePodSet("b", 1).
						Obj(),
					*utiltesting.MakePodSet("c", 1).
						RuntimeClass("runtime-a").
						PodOverHead(
							corev1.ResourceList{
								corev1.ResourceCPU:    resources.ResourceQuantity(corev1.ResourceCPU, 2),
								corev1.ResourceMemory: resources.ResourceQuantity(corev1.ResourceMemory, 2048),
							}).
						Obj(),
					*utiltesting.MakePodSet("d", 1).
						RuntimeClass("runtime-d").
						Obj(),
					*utiltesting.MakePodSet("e", 1).
						RuntimeClass("runtime-e").
						PodOverHead(
							corev1.ResourceList{
								corev1.ResourceCPU:    resources.ResourceQuantity(corev1.ResourceCPU, 2),
								corev1.ResourceMemory: resources.ResourceQuantity(corev1.ResourceMemory, 2048),
							}).
						Obj(),
				).
				Obj(),
			wantWl: utiltesting.MakeWorkload("foo", "").
				PodSets(
					*utiltesting.MakePodSet("a", 1).
						RuntimeClass("runtime-a").
						PodOverHead(
							corev1.ResourceList{
								corev1.ResourceCPU:    resources.ResourceQuantity(corev1.ResourceCPU, 1),
								corev1.ResourceMemory: resources.ResourceQuantity(corev1.ResourceMemory, 1024),
							}).
						Obj(),
					*utiltesting.MakePodSet("b", 1).
						Obj(),
					*utiltesting.MakePodSet("c", 1).
						RuntimeClass("runtime-a").
						PodOverHead(
							corev1.ResourceList{
								corev1.ResourceCPU:    resources.ResourceQuantity(corev1.ResourceCPU, 2),
								corev1.ResourceMemory: resources.ResourceQuantity(corev1.ResourceMemory, 2048),
							}).
						Obj(),
					*utiltesting.MakePodSet("d", 1).
						RuntimeClass("runtime-d").
						Obj(),
					*utiltesting.MakePodSet("e", 1).
						RuntimeClass("runtime-e").
						PodOverHead(
							corev1.ResourceList{
								corev1.ResourceCPU:    resources.ResourceQuantity(corev1.ResourceCPU, 2),
								corev1.ResourceMemory: resources.ResourceQuantity(corev1.ResourceMemory, 2048),
							}).
						Obj(),
				).
				Obj(),
		},
		"Handle runtimeClass without podOverHead": {
			runtimeClasses: []nodev1.RuntimeClass{
				utiltesting.MakeRuntimeClass("runtime-a", "handler-a").
					RuntimeClass,
			},
			wl: utiltesting.MakeWorkload("foo", "").
				PodSets(
					*utiltesting.MakePodSet("a", 1).
						RuntimeClass("runtime-a").
						Obj(),
					*utiltesting.MakePodSet("b", 1).
						Obj(),
					*utiltesting.MakePodSet("c", 1).
						RuntimeClass("runtime-a").
						PodOverHead(
							corev1.ResourceList{
								corev1.ResourceCPU:    resources.ResourceQuantity(corev1.ResourceCPU, 1),
								corev1.ResourceMemory: resources.ResourceQuantity(corev1.ResourceMemory, 1024),
							}).
						Obj(),
					*utiltesting.MakePodSet("d", 1).
						RuntimeClass("runtime-d").
						Obj(),
					*utiltesting.MakePodSet("e", 1).
						RuntimeClass("runtime-e").
						PodOverHead(
							corev1.ResourceList{
								corev1.ResourceCPU:    resources.ResourceQuantity(corev1.ResourceCPU, 1),
								corev1.ResourceMemory: resources.ResourceQuantity(corev1.ResourceMemory, 1024),
							}).
						Obj(),
				).
				Obj(),
			wantWl: utiltesting.MakeWorkload("foo", "").
				PodSets(
					*utiltesting.MakePodSet("a", 1).
						RuntimeClass("runtime-a").
						Obj(),
					*utiltesting.MakePodSet("b", 1).
						Obj(),
					*utiltesting.MakePodSet("c", 1).
						RuntimeClass("runtime-a").
						PodOverHead(
							corev1.ResourceList{
								corev1.ResourceCPU:    resources.ResourceQuantity(corev1.ResourceCPU, 1),
								corev1.ResourceMemory: resources.ResourceQuantity(corev1.ResourceMemory, 1024),
							}).
						Obj(),
					*utiltesting.MakePodSet("d", 1).
						RuntimeClass("runtime-d").
						Obj(),
					*utiltesting.MakePodSet("e", 1).
						RuntimeClass("runtime-e").
						PodOverHead(
							corev1.ResourceList{
								corev1.ResourceCPU:    resources.ResourceQuantity(corev1.ResourceCPU, 1),
								corev1.ResourceMemory: resources.ResourceQuantity(corev1.ResourceMemory, 1024),
							}).
						Obj(),
				).
				Obj(),
		},
		"Handle container limit range": {
			limitranges: []corev1.LimitRange{
				utiltesting.MakeLimitRange("foo", "").
					WithType(corev1.LimitTypeContainer).
					WithValue(
						"Default", corev1.ResourceCPU, "4",
					).
					WithValue(
						"DefaultRequest", corev1.ResourceCPU, "3",
					).
					WithValue(
						"Max", corev1.ResourceCPU, "5",
					).
					WithValue(
						"Min", corev1.ResourceCPU, "2",
					).
					LimitRange,
			},
			wl: utiltesting.MakeWorkload("foo", "").
				PodSets(
					*utiltesting.MakePodSet("a", 1).
						InitContainers(corev1.Container{}).
						Obj(),
					*utiltesting.MakePodSet("b", 1).
						InitContainers(corev1.Container{}).
						Limit(corev1.ResourceCPU, "6").
						Obj(),
					*utiltesting.MakePodSet("c", 1).
						InitContainers(corev1.Container{}).
						Request(corev1.ResourceCPU, "1").
						Obj(),
				).
				Obj(),
			wantWl: utiltesting.MakeWorkload("foo", "").
				PodSets(
					*utiltesting.MakePodSet("a", 1).
						Limit(corev1.ResourceCPU, "4").
						Request(corev1.ResourceCPU, "3").
						InitContainers(corev1.Container{
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU: *resource.NewQuantity(4, resource.DecimalSI),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: *resource.NewQuantity(3, resource.DecimalSI),
								},
							},
						}).
						Obj(),
					*utiltesting.MakePodSet("b", 1).
						Limit(corev1.ResourceCPU, "6").
						Request(corev1.ResourceCPU, "3").
						InitContainers(corev1.Container{
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU: *resource.NewQuantity(4, resource.DecimalSI),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: *resource.NewQuantity(3, resource.DecimalSI),
								},
							},
						}).
						Obj(),
					*utiltesting.MakePodSet("c", 1).
						Limit(corev1.ResourceCPU, "4").
						Request(corev1.ResourceCPU, "1").
						InitContainers(corev1.Container{
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU: *resource.NewQuantity(4, resource.DecimalSI),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: *resource.NewQuantity(3, resource.DecimalSI),
								},
							},
						}).
						Obj(),
				).
				Obj(),
		},
		"Handle pod limit range": {
			limitranges: []corev1.LimitRange{
				utiltesting.MakeLimitRange("foo", "").
					WithType(corev1.LimitTypePod).
					WithValue(
						"Default", corev1.ResourceCPU, "4",
					).
					WithValue(
						"DefaultRequest", corev1.ResourceCPU, "3",
					).
					WithValue(
						"Max", corev1.ResourceCPU, "5",
					).
					WithValue(
						"Min", corev1.ResourceCPU, "2",
					).
					LimitRange,
			},
			wl: utiltesting.MakeWorkload("foo", "").
				PodSets(
					*utiltesting.MakePodSet("a", 1).
						Obj(),
					*utiltesting.MakePodSet("b", 1).
						Limit(corev1.ResourceCPU, "6").
						Request(corev1.ResourceCPU, "1").
						Obj(),
				).
				Obj(),
			wantWl: utiltesting.MakeWorkload("foo", "").
				PodSets(
					*utiltesting.MakePodSet("a", 1).
						Obj(),
					*utiltesting.MakePodSet("b", 1).
						Limit(corev1.ResourceCPU, "6").
						Request(corev1.ResourceCPU, "1").
						Obj(),
				).
				Obj(),
		},
		"Handle empty container limit range": {
			limitranges: []corev1.LimitRange{
				utiltesting.MakeLimitRange("foo", "").
					WithType(corev1.LimitTypeContainer).
					LimitRange,
			},
			wl: utiltesting.MakeWorkload("foo", "").
				PodSets(
					*utiltesting.MakePodSet("a", 1).
						Obj(),
					*utiltesting.MakePodSet("b", 1).
						Limit(corev1.ResourceCPU, "6").
						Request(corev1.ResourceCPU, "1").
						Obj(),
				).
				Obj(),
			wantWl: utiltesting.MakeWorkload("foo", "").
				PodSets(
					*utiltesting.MakePodSet("a", 1).
						Obj(),
					*utiltesting.MakePodSet("b", 1).
						Limit(corev1.ResourceCPU, "6").
						Request(corev1.ResourceCPU, "1").
						Obj(),
				).
				Obj(),
		},
		"Apply limits to requests": {
			wl: utiltesting.MakeWorkload("foo", "").
				PodSets(
					*utiltesting.MakePodSet("a", 1).
						Limit(corev1.ResourceCPU, "1").
						Limit(corev1.ResourceMemory, "1Gi").
						InitContainers(corev1.Container{
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    *resource.NewQuantity(1, resource.DecimalSI),
									corev1.ResourceMemory: *resource.NewQuantity(1, resource.BinarySI),
								},
							},
						}).
						Obj(),
					*utiltesting.MakePodSet("b", 1).
						Request(corev1.ResourceCPU, "2").
						Limit(corev1.ResourceCPU, "3").
						Limit(corev1.ResourceMemory, "1Gi").
						InitContainers(corev1.Container{
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    *resource.NewQuantity(3, resource.DecimalSI),
									corev1.ResourceMemory: *resource.NewQuantity(1, resource.BinarySI),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: *resource.NewQuantity(2, resource.DecimalSI),
								},
							},
						}).
						Obj(),
					*utiltesting.MakePodSet("c", 1).
						Request(corev1.ResourceMemory, "1Gi").
						Limit(corev1.ResourceCPU, "1").
						Limit(corev1.ResourceMemory, "3Gi").
						InitContainers(corev1.Container{
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    *resource.NewQuantity(1, resource.DecimalSI),
									corev1.ResourceMemory: *resource.NewQuantity(3, resource.BinarySI),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceMemory: *resource.NewQuantity(1, resource.BinarySI),
								},
							},
						}).
						Obj(),
					*utiltesting.MakePodSet("d", 1).
						Limit(corev1.ResourceCPU, "1").
						InitContainers(corev1.Container{
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU: *resource.NewQuantity(1, resource.DecimalSI),
								},
							},
						}).
						Obj(),
					*utiltesting.MakePodSet("e", 1).
						Request(corev1.ResourceMemory, "1Gi").
						InitContainers(corev1.Container{
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceMemory: *resource.NewQuantity(1, resource.BinarySI),
								},
							},
						}).
						Obj(),
				).
				Obj(),
			wantWl: utiltesting.MakeWorkload("foo", "").
				PodSets(
					*utiltesting.MakePodSet("a", 1).
						Limit(corev1.ResourceCPU, "1").
						Limit(corev1.ResourceMemory, "1Gi").
						Request(corev1.ResourceCPU, "1").
						Request(corev1.ResourceMemory, "1Gi").
						InitContainers(corev1.Container{
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    *resource.NewQuantity(1, resource.DecimalSI),
									corev1.ResourceMemory: *resource.NewQuantity(1, resource.BinarySI),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    *resource.NewQuantity(1, resource.DecimalSI),
									corev1.ResourceMemory: *resource.NewQuantity(1, resource.BinarySI),
								},
							},
						}).
						Obj(),
					*utiltesting.MakePodSet("b", 1).
						Limit(corev1.ResourceCPU, "3").
						Limit(corev1.ResourceMemory, "1Gi").
						Request(corev1.ResourceCPU, "2").
						Request(corev1.ResourceMemory, "1Gi").
						InitContainers(corev1.Container{
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    *resource.NewQuantity(3, resource.DecimalSI),
									corev1.ResourceMemory: *resource.NewQuantity(1, resource.BinarySI),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    *resource.NewQuantity(2, resource.DecimalSI),
									corev1.ResourceMemory: *resource.NewQuantity(1, resource.BinarySI),
								},
							},
						}).
						Obj(),
					*utiltesting.MakePodSet("c", 1).
						Limit(corev1.ResourceCPU, "1").
						Limit(corev1.ResourceMemory, "3Gi").
						Request(corev1.ResourceCPU, "1").
						Request(corev1.ResourceMemory, "1Gi").
						InitContainers(corev1.Container{
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    *resource.NewQuantity(1, resource.DecimalSI),
									corev1.ResourceMemory: *resource.NewQuantity(3, resource.BinarySI),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    *resource.NewQuantity(1, resource.DecimalSI),
									corev1.ResourceMemory: *resource.NewQuantity(1, resource.BinarySI),
								},
							},
						}).
						Obj(),
					*utiltesting.MakePodSet("d", 1).
						Limit(corev1.ResourceCPU, "1").
						Request(corev1.ResourceCPU, "1").
						InitContainers(corev1.Container{
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU: *resource.NewQuantity(1, resource.DecimalSI),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: *resource.NewQuantity(1, resource.DecimalSI),
								},
							},
						}).
						Obj(),
					*utiltesting.MakePodSet("e", 1).
						Request(corev1.ResourceMemory, "1Gi").
						InitContainers(corev1.Container{
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceMemory: *resource.NewQuantity(1, resource.BinarySI),
								},
							},
						}).
						Obj(),
				).
				Obj(),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cl := utiltesting.NewClientBuilder().WithLists(
				&nodev1.RuntimeClassList{Items: tc.runtimeClasses},
				&corev1.LimitRangeList{Items: tc.limitranges},
			).WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerType, indexer.IndexLimitRangeHasContainerType).
				Build()
			ctx, _ := utiltesting.ContextWithLog(t)
			AdjustResources(ctx, cl, tc.wl)
			if diff := cmp.Diff(tc.wl, tc.wantWl); diff != "" {
				t.Errorf("Unexpected resources after adjusting (-want,+got): %s", diff)
			}
		})
	}
}
