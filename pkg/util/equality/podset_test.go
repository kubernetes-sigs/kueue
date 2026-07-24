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
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestComparePodTemplate(t *testing.T) {
	base := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name: "c",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
			},
		}},
		NodeSelector: map[string]string{"f1l1": "v1"},
	}

	cases := map[string]struct {
		a    corev1.PodSpec
		b    corev1.PodSpec
		want bool
	}{
		"identical": {
			a:    *base.DeepCopy(),
			b:    *base.DeepCopy(),
			want: true,
		},
		"identical ignoring non-compared PodSpec fields": {
			// Only containers/initContainers/tolerations are compared. nodeSelector
			// and API-defaulted fields are ignored (same strategy as Job ↔ Workload).
			a: func() corev1.PodSpec {
				ps := *base.DeepCopy()
				ps.NodeSelector = map[string]string{"f2l1": "v2"}
				ps.RestartPolicy = corev1.RestartPolicyAlways
				ps.DNSPolicy = corev1.DNSClusterFirst
				ps.SchedulerName = "default-scheduler"
				ps.TerminationGracePeriodSeconds = ptr.To[int64](30)
				ps.SecurityContext = &corev1.PodSecurityContext{}
				return ps
			}(),
			b:    *base.DeepCopy(),
			want: true,
		},
		"different container resources": {
			a: func() corev1.PodSpec {
				ps := *base.DeepCopy()
				ps.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("2")
				return ps
			}(),
			b:    *base.DeepCopy(),
			want: false,
		},
		"different tolerations": {
			a: func() corev1.PodSpec {
				ps := *base.DeepCopy()
				ps.Tolerations = []corev1.Toleration{{
					Key:      "k",
					Value:    "v",
					Operator: corev1.TolerationOpEqual,
					Effect:   corev1.TaintEffectNoSchedule,
				}}
				return ps
			}(),
			b:    *base.DeepCopy(),
			want: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := ComparePodTemplate(&tc.a, &tc.b); got != tc.want {
				t.Errorf("ComparePodTemplate() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestComparePodSetSlices(t *testing.T) {
	cases := map[string]struct {
		a                 []kueue.PodSet
		b                 []kueue.PodSet
		ignoreTolerations bool
		wantEquivalent    bool
	}{
		"different name": {
			a:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(5).Obj()},
			b:              []kueue.PodSet{*utiltestingapi.MakePodSet("ps2", 10).SetMinimumCount(5).Obj()},
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
			got := ComparePodSetSlices(tc.a, tc.b, options...)
			if got != tc.wantEquivalent {
				t.Errorf("Unexpected result, want %v", tc.wantEquivalent)
			}
		})
	}
}
