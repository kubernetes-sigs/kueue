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

package equality

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestComparePodSetSlices(t *testing.T) {
	cases := map[string]struct {
		a                     []kueue.PodSet
		b                     []kueue.PodSet
		ignoreTolerations     bool
		ignoreTopologyRequest bool
		wantEquivalent        bool
	}{
		"different name": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps2", 10).SetMinimumCount(5).Obj()},
			wantEquivalent: true,
		},
		// The class decides the overhead the Pods will actually carry, so a
		// prebuilt Workload naming a cheaper one is not the Job it stands for.
		"different runtime class": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).RuntimeClass("kata").Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).RuntimeClass("runc").Obj()},
			wantEquivalent: false,
		},
		"runtime class on one side only": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).RuntimeClass("kata").Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).Obj()},
			wantEquivalent: false,
		},
		"same runtime class": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).RuntimeClass("kata").Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).RuntimeClass("kata").Obj()},
			wantEquivalent: true,
		},
		"different min count": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(2).Obj()},
			wantEquivalent: false,
		},
		"different node selector": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).NodeSelector(map[string]string{"key": "val"}).Obj()},
			wantEquivalent: true,
		},
		"same required topology": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).RequiredTopologyRequest(corev1.LabelHostname).Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).RequiredTopologyRequest(corev1.LabelHostname).Obj()},
			wantEquivalent: true,
		},
		"different required topology": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).RequiredTopologyRequest(corev1.LabelHostname).Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).RequiredTopologyRequest(corev1.LabelTopologyZone).Obj()},
			wantEquivalent: false,
		},
		"different required topology ignored": {
			a:                     []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).RequiredTopologyRequest(corev1.LabelHostname).Obj()},
			b:                     []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).RequiredTopologyRequest(corev1.LabelTopologyZone).Obj()},
			ignoreTopologyRequest: true,
			wantEquivalent:        true,
		},
		"topology request present on one side": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).RequiredTopologyRequest(corev1.LabelHostname).Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).Obj()},
			wantEquivalent: false,
		},
		"required and preferred topology": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).RequiredTopologyRequest(corev1.LabelHostname).Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).PreferredTopologyRequest(corev1.LabelHostname).Obj()},
			wantEquivalent: false,
		},
		"derived pod index without topology constraint": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).PodIndexLabel(new("index")).Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).Obj()},
			wantEquivalent: true,
		},
		"different pod index under topology constraint": {
			a: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).
				RequiredTopologyRequest(corev1.LabelHostname).
				PodIndexLabel(new("index-a")).
				Obj()},
			b: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).
				RequiredTopologyRequest(corev1.LabelHostname).
				PodIndexLabel(new("index-b")).
				Obj()},
			wantEquivalent: false,
		},
		"legacy and unified slice topology constraints": {
			a: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).
				SliceRequiredTopologyRequest(corev1.LabelHostname).
				SliceSizeTopologyRequest(2).
				Obj()},
			b: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).
				SliceRequiredTopologyConstraints(kueue.PodsetSliceRequiredTopologyConstraint{
					Topology: corev1.LabelHostname,
					Size:     2,
				}).
				Obj()},
			wantEquivalent: true,
		},
		"different requests": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).Request("res", "1").Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).Request("res", "2").Obj()},
			wantEquivalent: false,
		},
		"different requests in init containers": {
			a: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).InitContainers(corev1.Container{
				Image: "img1",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						"res": resource.MustParse("1"),
					},
				},
			}).Obj()},
			b: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).InitContainers(corev1.Container{
				Image: "img1",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						"res": resource.MustParse("2"),
					},
				},
			}).Obj()},
			wantEquivalent: false,
		},
		"different requests in toleration": {
			a: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).Toleration(corev1.Toleration{
				Key:      "instance",
				Operator: corev1.TolerationOpEqual,
				Value:    "spot",
				Effect:   corev1.TaintEffectNoSchedule,
			}).Obj()},
			b: []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).Toleration(corev1.Toleration{
				Key:      "instance",
				Operator: corev1.TolerationOpEqual,
				Value:    "demand",
				Effect:   corev1.TaintEffectNoSchedule,
			}).Obj()},
			wantEquivalent: false,
		},
		"different count": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 20).SetMinimumCount(5).Obj()},
			wantEquivalent: false,
		},
		"different slice len": {
			a:              []kueue.PodSet{{}, {}},
			b:              []kueue.PodSet{{}, {}, {}},
			wantEquivalent: false,
		},
		"different requests in toleration, ignore tolerations": {
			a: []kueue.PodSet{
				*utiltestingapi.MakePodSet("ps", 10).
					SetMinimumCount(5).
					Toleration(corev1.Toleration{
						Key:      "instance",
						Operator: corev1.TolerationOpEqual,
						Value:    "spot",
						Effect:   corev1.TaintEffectNoSchedule,
					}).Obj(),
			},
			b: []kueue.PodSet{
				*utiltestingapi.MakePodSet("ps", 10).
					SetMinimumCount(5).
					Toleration(corev1.Toleration{
						Key:      "instance",
						Operator: corev1.TolerationOpEqual,
						Value:    "demand",
						Effect:   corev1.TaintEffectNoSchedule,
					}).Obj(),
			},
			ignoreTolerations: true,
			wantEquivalent:    true,
		},
		"different requests in node selector": {
			a: []kueue.PodSet{
				*utiltestingapi.MakePodSet("ps", 10).
					SetMinimumCount(5).
					NodeSelector(map[string]string{"key": "val"}).
					Obj(),
			},
			b: []kueue.PodSet{
				*utiltestingapi.MakePodSet("ps", 10).
					SetMinimumCount(5).
					NodeSelector(map[string]string{"key": "val2"}).
					Obj(),
			},
			wantEquivalent: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			options := make([]ComparePodSetsOption, 0, 1)
			if tc.ignoreTolerations {
				options = append(options, WithIgnoreTolerations())
			}
			if tc.ignoreTopologyRequest {
				options = append(options, WithIgnoreTopologyRequest())
			}
			got := ComparePodSetSlices(tc.a, tc.b, options...)
			if got != tc.wantEquivalent {
				t.Errorf("Unexpected result, want %v", tc.wantEquivalent)
			}
		})
	}
}
