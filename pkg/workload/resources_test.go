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
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/limitrange"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func defaultResourceQuantity(name corev1.ResourceName, value int64) resource.Quantity {
	return resources.NewResourceFormatter().ResourceQuantity(name, value)
}

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
						corev1.ResourceCPU:    defaultResourceQuantity(corev1.ResourceCPU, 1),
						corev1.ResourceMemory: defaultResourceQuantity(corev1.ResourceMemory, 1024),
					}).
					RuntimeClass,
			},
			wl: utiltestingapi.MakeWorkload("foo", "").
				PodSets(
					*utiltestingapi.MakePodSet("a", 1).
						RuntimeClass("runtime-a").
						Obj(),
					*utiltestingapi.MakePodSet("b", 1).
						Obj(),
					*utiltestingapi.MakePodSet("c", 1).
						RuntimeClass("runtime-a").
						PodOverHead(
							corev1.ResourceList{
								corev1.ResourceCPU:    defaultResourceQuantity(corev1.ResourceCPU, 2),
								corev1.ResourceMemory: defaultResourceQuantity(corev1.ResourceMemory, 2048),
							}).
						Obj(),
					*utiltestingapi.MakePodSet("d", 1).
						RuntimeClass("runtime-d").
						Obj(),
					*utiltestingapi.MakePodSet("e", 1).
						RuntimeClass("runtime-e").
						PodOverHead(
							corev1.ResourceList{
								corev1.ResourceCPU:    defaultResourceQuantity(corev1.ResourceCPU, 2),
								corev1.ResourceMemory: defaultResourceQuantity(corev1.ResourceMemory, 2048),
							}).
						Obj(),
				).
				Obj(),
			wantWl: utiltestingapi.MakeWorkload("foo", "").
				PodSets(
					*utiltestingapi.MakePodSet("a", 1).
						RuntimeClass("runtime-a").
						PodOverHead(
							corev1.ResourceList{
								corev1.ResourceCPU:    defaultResourceQuantity(corev1.ResourceCPU, 1),
								corev1.ResourceMemory: defaultResourceQuantity(corev1.ResourceMemory, 1024),
							}).
						Obj(),
					*utiltestingapi.MakePodSet("b", 1).
						Obj(),
					// Larger than the class defines, so it is what the Pods carry.
					*utiltestingapi.MakePodSet("c", 1).
						RuntimeClass("runtime-a").
						PodOverHead(
							corev1.ResourceList{
								corev1.ResourceCPU:    defaultResourceQuantity(corev1.ResourceCPU, 2),
								corev1.ResourceMemory: defaultResourceQuantity(corev1.ResourceMemory, 2048),
							}).
						Obj(),
					*utiltestingapi.MakePodSet("d", 1).
						RuntimeClass("runtime-d").
						Obj(),
					*utiltestingapi.MakePodSet("e", 1).
						RuntimeClass("runtime-e").
						PodOverHead(
							corev1.ResourceList{
								corev1.ResourceCPU:    defaultResourceQuantity(corev1.ResourceCPU, 2),
								corev1.ResourceMemory: defaultResourceQuantity(corev1.ResourceMemory, 2048),
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
			wl: utiltestingapi.MakeWorkload("foo", "").
				PodSets(
					*utiltestingapi.MakePodSet("a", 1).
						RuntimeClass("runtime-a").
						Obj(),
					*utiltestingapi.MakePodSet("b", 1).
						Obj(),
					*utiltestingapi.MakePodSet("c", 1).
						RuntimeClass("runtime-a").
						PodOverHead(
							corev1.ResourceList{
								corev1.ResourceCPU:    defaultResourceQuantity(corev1.ResourceCPU, 1),
								corev1.ResourceMemory: defaultResourceQuantity(corev1.ResourceMemory, 1024),
							}).
						Obj(),
					*utiltestingapi.MakePodSet("d", 1).
						RuntimeClass("runtime-d").
						Obj(),
					*utiltestingapi.MakePodSet("e", 1).
						RuntimeClass("runtime-e").
						PodOverHead(
							corev1.ResourceList{
								corev1.ResourceCPU:    defaultResourceQuantity(corev1.ResourceCPU, 1),
								corev1.ResourceMemory: defaultResourceQuantity(corev1.ResourceMemory, 1024),
							}).
						Obj(),
				).
				Obj(),
			wantWl: utiltestingapi.MakeWorkload("foo", "").
				PodSets(
					*utiltestingapi.MakePodSet("a", 1).
						RuntimeClass("runtime-a").
						Obj(),
					*utiltestingapi.MakePodSet("b", 1).
						Obj(),
					// The class defines none, so there is nothing to raise this to.
					*utiltestingapi.MakePodSet("c", 1).
						RuntimeClass("runtime-a").
						PodOverHead(
							corev1.ResourceList{
								corev1.ResourceCPU:    defaultResourceQuantity(corev1.ResourceCPU, 1),
								corev1.ResourceMemory: defaultResourceQuantity(corev1.ResourceMemory, 1024),
							}).
						Obj(),
					*utiltestingapi.MakePodSet("d", 1).
						RuntimeClass("runtime-d").
						Obj(),
					*utiltestingapi.MakePodSet("e", 1).
						RuntimeClass("runtime-e").
						PodOverHead(
							corev1.ResourceList{
								corev1.ResourceCPU:    defaultResourceQuantity(corev1.ResourceCPU, 1),
								corev1.ResourceMemory: defaultResourceQuantity(corev1.ResourceMemory, 1024),
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
			wl: utiltestingapi.MakeWorkload("foo", "").
				PodSets(
					*utiltestingapi.MakePodSet("a", 1).
						InitContainers(corev1.Container{}).
						Obj(),
					*utiltestingapi.MakePodSet("b", 1).
						InitContainers(corev1.Container{}).
						Limit(corev1.ResourceCPU, "6").
						Obj(),
					*utiltestingapi.MakePodSet("c", 1).
						InitContainers(corev1.Container{}).
						Request(corev1.ResourceCPU, "1").
						Obj(),
				).
				Obj(),
			wantWl: utiltestingapi.MakeWorkload("foo", "").
				PodSets(
					*utiltestingapi.MakePodSet("a", 1).
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
					*utiltestingapi.MakePodSet("b", 1).
						Limit(corev1.ResourceCPU, "6").
						// The limits are copied into the missing requests before
						// the LimitRange defaultRequest applies, mirroring the
						// requests the created Pods will carry.
						Request(corev1.ResourceCPU, "6").
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
					*utiltestingapi.MakePodSet("c", 1).
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
			wl: utiltestingapi.MakeWorkload("foo", "").
				PodSets(
					*utiltestingapi.MakePodSet("a", 1).
						Obj(),
					*utiltestingapi.MakePodSet("b", 1).
						Limit(corev1.ResourceCPU, "6").
						Request(corev1.ResourceCPU, "1").
						Obj(),
				).
				Obj(),
			wantWl: utiltestingapi.MakeWorkload("foo", "").
				PodSets(
					*utiltestingapi.MakePodSet("a", 1).
						Obj(),
					*utiltestingapi.MakePodSet("b", 1).
						Limit(corev1.ResourceCPU, "6").
						Request(corev1.ResourceCPU, "1").
						Obj(),
				).
				Obj(),
		},
		"Handle pod-level resources with pod limit range": {
			limitranges: []corev1.LimitRange{
				utiltesting.MakeLimitRange("foo", "").
					WithType(corev1.LimitTypePod).
					WithValue(
						"Default", corev1.ResourceCPU, "4",
					).
					WithValue(
						"Default", corev1.ResourceMemory, "1Gi",
					).
					WithValue(
						"DefaultRequest", corev1.ResourceCPU, "3",
					).
					WithValue(
						"DefaultRequest", corev1.ResourceMemory, "512Mi",
					).
					LimitRange,
			},
			wl: utiltestingapi.MakeWorkload("foo", "").
				PodSets(
					*utiltestingapi.MakePodSet("a", 1).
						PodLevelLimit(corev1.ResourceMemory, "2Gi").
						Obj(),
					*utiltestingapi.MakePodSet("b", 1).
						PodLevelLimit(corev1.ResourceCPU, "6").
						PodLevelRequest(corev1.ResourceCPU, "1").
						Obj(),
				).
				Obj(),
			wantWl: utiltestingapi.MakeWorkload("foo", "").
				PodSets(
					*utiltestingapi.MakePodSet("a", 1).
						PodLevelLimit(corev1.ResourceCPU, "4").
						PodLevelLimit(corev1.ResourceMemory, "2Gi").
						PodLevelRequest(corev1.ResourceCPU, "3").
						// The user-set memory limit is copied into the missing
						// request before the LimitRange defaultRequest applies.
						PodLevelRequest(corev1.ResourceMemory, "2Gi").
						Obj(),
					*utiltestingapi.MakePodSet("b", 1).
						PodLevelLimit(corev1.ResourceCPU, "6").
						PodLevelLimit(corev1.ResourceMemory, "1Gi").
						PodLevelRequest(corev1.ResourceCPU, "1").
						PodLevelRequest(corev1.ResourceMemory, "512Mi").
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
			wl: utiltestingapi.MakeWorkload("foo", "").
				PodSets(
					*utiltestingapi.MakePodSet("a", 1).
						Obj(),
					*utiltestingapi.MakePodSet("b", 1).
						Limit(corev1.ResourceCPU, "6").
						Request(corev1.ResourceCPU, "1").
						Obj(),
				).
				Obj(),
			wantWl: utiltestingapi.MakeWorkload("foo", "").
				PodSets(
					*utiltestingapi.MakePodSet("a", 1).
						Obj(),
					*utiltestingapi.MakePodSet("b", 1).
						Limit(corev1.ResourceCPU, "6").
						Request(corev1.ResourceCPU, "1").
						Obj(),
				).
				Obj(),
		},
		"Apply pod-level limits to requests": {
			wl: utiltestingapi.MakeWorkload("foo", "").
				PodSets(
					*utiltestingapi.MakePodSet("a", 1).
						PodLevelLimit(corev1.ResourceCPU, "1").
						PodLevelLimit(corev1.ResourceMemory, "1Gi").
						Obj(),
					*utiltestingapi.MakePodSet("b", 1).
						PodLevelRequest(corev1.ResourceCPU, "2").
						PodLevelLimit(corev1.ResourceCPU, "3").
						PodLevelLimit(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				Obj(),
			wantWl: utiltestingapi.MakeWorkload("foo", "").
				PodSets(
					*utiltestingapi.MakePodSet("a", 1).
						PodLevelLimit(corev1.ResourceCPU, "1").
						PodLevelLimit(corev1.ResourceMemory, "1Gi").
						PodLevelRequest(corev1.ResourceCPU, "1").
						PodLevelRequest(corev1.ResourceMemory, "1Gi").
						Obj(),
					*utiltestingapi.MakePodSet("b", 1).
						PodLevelRequest(corev1.ResourceCPU, "2").
						PodLevelLimit(corev1.ResourceCPU, "3").
						PodLevelLimit(corev1.ResourceMemory, "1Gi").
						PodLevelRequest(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				Obj(),
		},
		"Apply limits to requests": {
			wl: utiltestingapi.MakeWorkload("foo", "").
				PodSets(
					*utiltestingapi.MakePodSet("a", 1).
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
					*utiltestingapi.MakePodSet("b", 1).
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
					*utiltestingapi.MakePodSet("c", 1).
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
					*utiltestingapi.MakePodSet("d", 1).
						Limit(corev1.ResourceCPU, "1").
						InitContainers(corev1.Container{
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU: *resource.NewQuantity(1, resource.DecimalSI),
								},
							},
						}).
						Obj(),
					*utiltestingapi.MakePodSet("e", 1).
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
			wantWl: utiltestingapi.MakeWorkload("foo", "").
				PodSets(
					*utiltestingapi.MakePodSet("a", 1).
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
					*utiltestingapi.MakePodSet("b", 1).
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
					*utiltestingapi.MakePodSet("c", 1).
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
					*utiltestingapi.MakePodSet("d", 1).
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
					*utiltestingapi.MakePodSet("e", 1).
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
			).WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).
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
				Obj: utiltestingapi.MakeWorkload("alpha", metav1.NamespaceDefault).
					PodSets(
						*utiltestingapi.MakePodSet("a", 1).
							Containers(
								*utiltesting.MakeContainer().
									WithResourceReq(corev1.ResourceCPU, "100m").
									WithResourceLimit(corev1.ResourceCPU, "200m").
									WithResourceReq(corev1.ResourceMemory, "100Mi").
									WithResourceLimit(corev1.ResourceMemory, "200Mi").
									Obj()).
							Obj(),
						*utiltestingapi.MakePodSet("b", 1).
							InitContainers(
								*utiltesting.MakeContainer().
									WithResourceReq(corev1.ResourceCPU, "100m").
									WithResourceLimit(corev1.ResourceCPU, "200m").
									Obj()).
							Obj(),
					).Obj(),
			},
		},
		"valid workload with pod-level resources": {
			workloadInfo: &Info{
				Obj: utiltestingapi.MakeWorkload("alpha", metav1.NamespaceDefault).
					PodSets(
						*utiltestingapi.MakePodSet("a", 1).
							PodLevelRequest(corev1.ResourceCPU, "100m").
							PodLevelLimit(corev1.ResourceCPU, "200m").
							Obj(),
					).Obj(),
			},
		},
		"invalid workload; pod-level requests exceed limits": {
			workloadInfo: &Info{
				Obj: utiltestingapi.MakeWorkload("alpha", metav1.NamespaceDefault).
					PodSets(
						*utiltestingapi.MakePodSet("a", 1).
							PodLevelRequest(corev1.ResourceCPU, "300m").
							PodLevelLimit(corev1.ResourceCPU, "200m").
							Obj(),
					).Obj(),
			},
			wantError: field.ErrorList{
				field.Invalid(PodSetsPath.Index(0).Child("template").Child("spec").Child("resources"),
					[]corev1.ResourceName{corev1.ResourceCPU}, RequestsMustNotExceedLimitMessage),
			},
		},
		"invalid workload; multiple PodSet has invalid initContainers and containers": {
			workloadInfo: &Info{
				Obj: utiltestingapi.MakeWorkload("alpha", metav1.NamespaceDefault).PodSets(
					*utiltestingapi.MakePodSet("a", 1).
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
					*utiltestingapi.MakePodSet("b", 1).
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
					[]corev1.ResourceName{corev1.ResourceMemory}, RequestsMustNotExceedLimitMessage),
				field.Invalid(PodSetsPath.Index(0).Child("template").Child("spec").Child("containers").Index(0),
					[]corev1.ResourceName{corev1.ResourceCPU}, RequestsMustNotExceedLimitMessage),
				field.Invalid(PodSetsPath.Index(1).Child("template").Child("spec").Child("initContainers").Index(0),
					[]corev1.ResourceName{corev1.ResourceCPU}, RequestsMustNotExceedLimitMessage),
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
			workload: utiltestingapi.MakeWorkload("", metav1.NamespaceDefault).
				PodSets(
					*utiltestingapi.MakePodSet("alpha", 1).
						Request(corev1.ResourceCPU, "300m").
						Obj(),
					*utiltestingapi.MakePodSet("beta", 1).
						Request(corev1.ResourceCPU, "200m").
						Obj(),
				).
				Obj(),
		},
		"valid case without LimitRange": {
			workload: utiltestingapi.MakeWorkload("test", metav1.NamespaceDefault).
				PodSets(
					*utiltestingapi.MakePodSet("alpha", 1).
						Request(corev1.ResourceCPU, "300m").
						Obj(),
					*utiltestingapi.MakePodSet("beta", 1).
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
			workload: utiltestingapi.MakeWorkload("test", metav1.NamespaceDefault).
				PodSets(
					*utiltestingapi.MakePodSet("alpha", 1).
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
					[]corev1.ResourceName{corev1.ResourceCPU},
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

// podOwners is what the pod controller adds to a group's Workload: a plain
// reference per member, none of them the controller.
var podOwners = []metav1.OwnerReference{
	{APIVersion: "v1", Kind: "Pod", Name: "p0", UID: "pod-0"},
	{APIVersion: "v1", Kind: "Pod", Name: "p1", UID: "pod-1"},
}

func overheadWorkload(class string, overhead corev1.ResourceList, owners []metav1.OwnerReference) *kueue.Workload {
	wl := utiltestingapi.MakeWorkload("w", "ns").
		PodSets(*utiltestingapi.MakePodSet("a", 1).RuntimeClass(class).Obj()).Obj()
	wl.Spec.PodSets[0].Template.Spec.Overhead = overhead
	wl.OwnerReferences = owners
	return wl
}

func TestHandlePodOverhead(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	cpu := func(s string) corev1.ResourceList {
		return corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(s)}
	}
	rc := utiltesting.MakeRuntimeClass("rc", "h").PodOverhead(cpu("250m")).RuntimeClass
	bare := utiltesting.MakeRuntimeClass("bare", "h").RuntimeClass
	mixed := utiltesting.MakeRuntimeClass("mixed", "h").PodOverhead(corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("100m"),
		corev1.ResourceMemory: resource.MustParse("2Gi"),
	}).RuntimeClass

	cases := map[string]struct {
		wl      *kueue.Workload
		want    corev1.ResourceList
		wantErr bool
	}{
		"a PodSet carrying less than its class is raised to it": {
			wl: overheadWorkload("rc", cpu("1m"), nil), want: cpu("250m"),
		},
		"a PodSet carrying none is given the class overhead": {
			wl: overheadWorkload("rc", nil, nil), want: cpu("250m"),
		},
		// A StatefulSet builds its Workload from the parent template, which never
		// passed the admission that writes overhead, and the pod controller adds
		// the created Pods as owners afterwards. Neither costs it the class value.
		"owning Pods do not stop the class from applying": {
			wl: overheadWorkload("rc", nil, podOwners), want: cpu("250m"),
		},
		// Only handler is immutable, so a class can be lowered after a Pod was
		// admitted under it, and that Pod still carries the older, larger value.
		"a PodSet carrying more than its class keeps what it has": {
			wl: overheadWorkload("rc", cpu("500m"), podOwners), want: cpu("500m"),
		},
		"a key the class does not define survives": {
			wl: overheadWorkload("rc", corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")}, nil),
			want: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("250m"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
		},
		// Larger on a different side for each resource, so taking whichever list
		// wins on one of them cannot pass this.
		"each resource takes its own larger value": {
			wl: overheadWorkload("mixed", corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			}, nil),
			want: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("2Gi"),
			},
		},
		"a class defining no overhead leaves the PodSet alone": {
			wl: overheadWorkload("bare", cpu("250m"), nil), want: cpu("250m"),
		},
		// AdjustResources only logs these, so the caller that can act on one has to see it here.
		"a class that does not resolve is reported and changes nothing": {
			wl: overheadWorkload("gone", cpu("250m"), nil), want: cpu("250m"), wantErr: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cl := utiltesting.NewClientBuilder().
				WithLists(&nodev1.RuntimeClassList{Items: []nodev1.RuntimeClass{rc, bare, mixed}}).
				Build()
			errs := handlePodOverhead(ctx, cl, tc.wl)
			if gotErr := len(errs) > 0; gotErr != tc.wantErr {
				t.Errorf("errors = %v, want error %t", errs, tc.wantErr)
			}
			for _, err := range errs {
				if !apierrors.IsNotFound(err) || !strings.Contains(err.Error(), "podSet a") {
					t.Errorf("error does not name the podSet and the missing class: %v", err)
				}
			}
			if diff := cmp.Diff(tc.want, tc.wl.Spec.PodSets[0].Template.Spec.Overhead); diff != "" {
				t.Errorf("Unexpected overhead (-want,+got):\n%s", diff)
			}
		})
	}
}
