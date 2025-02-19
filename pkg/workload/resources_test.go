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

package workload

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/limitrange"
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

func TestValidateResources(t *testing.T) {
	cases := map[string]struct {
		workloadInfo *Info
		wantError    field.ErrorList
	}{
		"valid workload": {
			workloadInfo: &Info{
				Obj: utiltesting.MakeWorkload("alpha", metav1.NamespaceDefault).
					PodSets(
						*utiltesting.MakePodSet("a", 1).
							Containers(
								*utiltesting.MakeContainer().
									WithResourceReq(corev1.ResourceCPU, "100m").
									WithResourceLimit(corev1.ResourceCPU, "200m").
									WithResourceReq(corev1.ResourceMemory, "100Mi").
									WithResourceLimit(corev1.ResourceMemory, "200Mi").
									Obj()).
							Obj(),
						*utiltesting.MakePodSet("b", 1).
							InitContainers(
								*utiltesting.MakeContainer().
									WithResourceReq(corev1.ResourceCPU, "100m").
									WithResourceLimit(corev1.ResourceCPU, "200m").
									Obj()).
							Obj(),
					).Obj(),
			},
		},
		"invalid workload; multiple PodSet has invalid initContainers and containers": {
			workloadInfo: &Info{
				Obj: utiltesting.MakeWorkload("alpha", metav1.NamespaceDefault).PodSets(
					*utiltesting.MakePodSet("a", 1).
						InitContainers(
							*utiltesting.MakeContainer().
								WithResourceReq(corev1.ResourceMemory, "200Mi").
								WithResourceLimit(corev1.ResourceMemory, "100Mi").
								WithResourceReq(corev1.ResourceCPU, "100m").
								WithResourceLimit(corev1.ResourceCPU, "200m").
								Obj()).
						Containers(
							*utiltesting.MakeContainer().
								WithResourceReq(corev1.ResourceCPU, "300m").
								WithResourceLimit(corev1.ResourceCPU, "200m").
								Obj()).
						Obj(),
					*utiltesting.MakePodSet("b", 1).
						InitContainers(
							*utiltesting.MakeContainer().
								WithResourceReq(corev1.ResourceCPU, "300m").
								WithResourceLimit(corev1.ResourceCPU, "200m").
								Obj()).
						Containers(
							*utiltesting.MakeContainer().
								WithResourceReq(corev1.ResourceCPU, "100m").
								WithResourceLimit(corev1.ResourceCPU, "200m").
								Obj()).
						Obj(),
				).Obj(),
			},
			wantError: field.ErrorList{
				field.Invalid(PodSetsPath.Index(0).Child("template").Child("spec").Child("initContainers").Index(0),
					[]string{corev1.ResourceMemory.String()}, RequestsMustNotExceedLimitMessage),
				field.Invalid(PodSetsPath.Index(0).Child("template").Child("spec").Child("containers").Index(0),
					[]string{corev1.ResourceCPU.String()}, RequestsMustNotExceedLimitMessage),
				field.Invalid(PodSetsPath.Index(1).Child("template").Child("spec").Child("initContainers").Index(0),
					[]string{corev1.ResourceCPU.String()}, RequestsMustNotExceedLimitMessage),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := ValidateResources(tc.workloadInfo)
			if diff := cmp.Diff(tc.wantError, got); len(diff) != 0 {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateLimitRange(t *testing.T) {
	cases := map[string]struct {
		limitRange *corev1.LimitRange
		workload   *kueue.Workload
		wantError  field.ErrorList
	}{
		"valid case with LimitRange": {
			limitRange: utiltesting.MakeLimitRange("test", metav1.NamespaceDefault).
				WithType(corev1.LimitTypePod).
				WithValue("Max", corev1.ResourceCPU, "1000m").
				Obj(),
			workload: utiltesting.MakeWorkload("", metav1.NamespaceDefault).
				PodSets(
					*utiltesting.MakePodSet("alpha", 1).
						Request(corev1.ResourceCPU, "300m").
						Obj(),
					*utiltesting.MakePodSet("beta", 1).
						Request(corev1.ResourceCPU, "200m").
						Obj(),
				).
				Obj(),
		},
		"valid case without LimitRange": {
			workload: utiltesting.MakeWorkload("test", metav1.NamespaceDefault).
				PodSets(
					*utiltesting.MakePodSet("alpha", 1).
						Request(corev1.ResourceCPU, "300m").
						Obj(),
					*utiltesting.MakePodSet("beta", 1).
						Request(corev1.ResourceCPU, "200m").
						Obj(),
				).
				Obj(),
		},
		"pod doesn't satisfy LimitRange constraints": {
			limitRange: utiltesting.MakeLimitRange("test", metav1.NamespaceDefault).
				WithType(corev1.LimitTypePod).
				WithValue("Max", corev1.ResourceCPU, "500m").
				Obj(),
			workload: utiltesting.MakeWorkload("test", metav1.NamespaceDefault).
				PodSets(
					*utiltesting.MakePodSet("alpha", 1).
						Request(corev1.ResourceCPU, "300m").
						InitContainers(
							*utiltesting.MakeContainer().
								AsSidecar().
								WithResourceReq(corev1.ResourceCPU, "300m").
								Obj(),
						).
						Obj(),
				).
				Obj(),
			wantError: field.ErrorList{
				field.Invalid(
					PodSetsPath.Index(0).Child("template").Child("spec"),
					[]string{corev1.ResourceCPU.String()},
					limitrange.RequestsMustNotBeAboveLimitRangeMaxMessage,
				),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			cliBuilder := utiltesting.NewClientBuilder()
			if tc.limitRange != nil {
				cliBuilder.WithObjects(tc.limitRange)
			}
			got := ValidateLimitRange(ctx, cliBuilder.Build(), &Info{Obj: tc.workload})
			if diff := cmp.Diff(tc.wantError, got); len(diff) != 0 {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
		})
	}
}
