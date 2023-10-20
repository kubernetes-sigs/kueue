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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestAdjustResources(t *testing.T) {
	cases := map[string]struct {
		runtimeClasses []nodev1.RuntimeClass
		limitranges    []corev1.LimitRange
		wl             *kueue.Workload
		wantWl         *kueue.Workload
	}{
		"Handle Pod Overhead": {
			runtimeClasses: []nodev1.RuntimeClass{
				//utiltesting.MakeRuntimeClass("runtime-a", "handler-a").
				//	RuntimeClass,
				utiltesting.MakeRuntimeClass("runtime-a", "handler-b").
					PodOverhead(corev1.ResourceList{
						corev1.ResourceCPU:    ResourceQuantity(corev1.ResourceCPU, 1),
						corev1.ResourceMemory: ResourceQuantity(corev1.ResourceMemory, 1024),
					}).
					RuntimeClass,
			},
			wl: utiltesting.MakeWorkload("foo", "").
				PodSets(
					*utiltesting.MakePodSet("a", 1).
						RuntimeClass("runtime-a").
						Obj(),
					//*utiltesting.MakePodSet("b", 1).
					//	RuntimeClass("runtime-b").
					//	Obj(),
					//*utiltesting.MakePodSet("c", 1).
					//	RuntimeClass("runtime-c").
					//	Obj(),
				).
				Obj(),
			wantWl: utiltesting.MakeWorkload("foo", "").
				PodSets(
					//*utiltesting.MakePodSet("a", 1).
					//	RuntimeClass("runtime-a").
					//	Obj(),
					*utiltesting.MakePodSet("a", 1).
						RuntimeClass("runtime-a").PodOverHead(
						corev1.ResourceList{
							corev1.ResourceCPU:    ResourceQuantity(corev1.ResourceCPU, 1),
							corev1.ResourceMemory: ResourceQuantity(corev1.ResourceMemory, 1024),
						}).Obj(),
					//*utiltesting.MakePodSet("c", 1).
					//	RuntimeClass("runtime-c").
					//	Obj(),
				).
				Obj(),
		},
		"Handle Pod Limit Range": {
			limitranges: []corev1.LimitRange{
				utiltesting.MakeLimitRange("foo", "").
					WithType(corev1.LimitTypeContainer).
					WithValue(
						"Max", corev1.ResourceCPU, "4",
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
					//*utiltesting.MakePodSet("b", 1).
					//	Limit(corev1.ResourceCPU, "5").
					//	Request(corev1.ResourceCPU, "1").
					//	Obj(),
					*utiltesting.MakePodSet("b", 1).
						Limit(corev1.ResourceCPU, "3").
						Request(corev1.ResourceCPU, "3").
						Obj(),
				).
				Obj(),
			wantWl: utiltesting.MakeWorkload("foo", "").
				PodSets(
					*utiltesting.MakePodSet("a", 1).
						Limit(corev1.ResourceCPU, "4").
						Request(corev1.ResourceCPU, "4").
						Obj(),
					//*utiltesting.MakePodSet("b", 1).
					//	Limit(corev1.ResourceCPU, "6").
					//	Request(corev1.ResourceCPU, "1").
					//	Obj(),
					*utiltesting.MakePodSet("b", 1).
						Limit(corev1.ResourceCPU, "3").
						Request(corev1.ResourceCPU, "3").
						Obj(),
				).
				Obj(),
		},
		"Limits applied to requests": {
			wl: utiltesting.MakeWorkload("foo", "").
				PodSets(
					*utiltesting.MakePodSet("a", 1).
						Limit(corev1.ResourceCPU, "1").
						Limit(corev1.ResourceMemory, "1Gi").
						Obj(),
					*utiltesting.MakePodSet("b", 1).
						Request(corev1.ResourceCPU, "2").
						Limit(corev1.ResourceCPU, "3").
						Limit(corev1.ResourceMemory, "1Gi").
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
						Obj(),
					*utiltesting.MakePodSet("b", 1).
						Limit(corev1.ResourceCPU, "2").
						Limit(corev1.ResourceMemory, "1Gi").
						Request(corev1.ResourceCPU, "3").
						Request(corev1.ResourceMemory, "1Gi").
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
			).Build()
			ctx, _ := utiltesting.ContextWithLog(t)
			AdjustResources(ctx, cl, tc.wl)
			if diff := cmp.Diff(tc.wl, tc.wantWl); diff != "" {
				t.Errorf("Unexpected resources after adjusting (-want,+got): %s", diff)
			}
		})
	}
}
