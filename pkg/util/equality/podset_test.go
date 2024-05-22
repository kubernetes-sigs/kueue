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

package equality

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltestting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestComparePodSetSlices(t *testing.T) {
	cases := map[string]struct {
		a         []kueue.PodSet
		b         []kueue.PodSet
		wantEqual bool
	}{
		"different name": {
			a:         []kueue.PodSet{*utiltestting.MakePodSet("ps", 10).SetMinimumCount(5).Obj()},
			b:         []kueue.PodSet{*utiltestting.MakePodSet("ps2", 10).SetMinimumCount(5).Obj()},
			wantEqual: true,
		},
		"different min count": {
			a:         []kueue.PodSet{*utiltestting.MakePodSet("ps", 10).SetMinimumCount(5).Obj()},
			b:         []kueue.PodSet{*utiltestting.MakePodSet("ps", 10).SetMinimumCount(2).Obj()},
			wantEqual: false,
		},
		"different node selector": {
			a:         []kueue.PodSet{*utiltestting.MakePodSet("ps", 10).SetMinimumCount(5).Obj()},
			b:         []kueue.PodSet{*utiltestting.MakePodSet("ps", 10).SetMinimumCount(5).NodeSelector(map[string]string{"key": "val"}).Obj()},
			wantEqual: true,
		},
		"different requests": {
			a:         []kueue.PodSet{*utiltestting.MakePodSet("ps", 10).SetMinimumCount(5).Request("res", "1").Obj()},
			b:         []kueue.PodSet{*utiltestting.MakePodSet("ps", 10).SetMinimumCount(5).Request("res", "2").Obj()},
			wantEqual: false,
		},
		"different requests in init containers": {
			a: []kueue.PodSet{*utiltestting.MakePodSet("ps", 10).SetMinimumCount(5).InitContainers(corev1.Container{
				Image: "img1",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						"res": resource.MustParse("1"),
					},
				},
			}).Obj()},
			b: []kueue.PodSet{*utiltestting.MakePodSet("ps", 10).SetMinimumCount(5).InitContainers(corev1.Container{
				Image: "img1",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						"res": resource.MustParse("2"),
					},
				},
			}).Obj()},
			wantEqual: false,
		},
		"different requests in toleration": {
			a: []kueue.PodSet{*utiltestting.MakePodSet("ps", 10).SetMinimumCount(5).Toleration(corev1.Toleration{
				Key:      "instance",
				Operator: corev1.TolerationOpEqual,
				Value:    "spot",
				Effect:   corev1.TaintEffectNoSchedule,
			}).Obj()},
			b: []kueue.PodSet{*utiltestting.MakePodSet("ps", 10).SetMinimumCount(5).Toleration(corev1.Toleration{
				Key:      "instance",
				Operator: corev1.TolerationOpEqual,
				Value:    "demand",
				Effect:   corev1.TaintEffectNoSchedule,
			}).Obj()},
			wantEqual: false,
		},
		"different count": {
			a:         []kueue.PodSet{*utiltestting.MakePodSet("ps", 10).SetMinimumCount(5).Obj()},
			b:         []kueue.PodSet{*utiltestting.MakePodSet("ps", 20).SetMinimumCount(5).Obj()},
			wantEqual: false,
		},
		"different slice len": {
			a:         []kueue.PodSet{{}, {}},
			b:         []kueue.PodSet{{}, {}, {}},
			wantEqual: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := ComparePodSetSlices(tc.a, tc.b, false)
			if got != tc.wantEqual {
				t.Errorf("Unexpected result, want %v", tc.wantEqual)
			}
		})
	}
}
