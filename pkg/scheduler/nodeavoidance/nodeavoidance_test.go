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

package nodeavoidance

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestIsNodeUnhealthy(t *testing.T) {
	testCases := []struct {
		name           string
		node           *corev1.Node
		unhealthyLabel string
		want           bool
	}{
		{
			name:           "nil node",
			node:           nil,
			unhealthyLabel: "unhealthy",
			want:           false,
		},
		{
			name: "node without labels",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-1",
				},
			},
			unhealthyLabel: "unhealthy",
			want:           false,
		},
		{
			name: "node with unhealthy label",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-1",
					Labels: map[string]string{
						"unhealthy": "true",
					},
				},
			},
			unhealthyLabel: "unhealthy",
			want:           true,
		},
		{
			name: "node with other label",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-1",
					Labels: map[string]string{
						"other": "true",
					},
				},
			},
			unhealthyLabel: "unhealthy",
			want:           false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsNodeUnhealthy(tc.node, tc.unhealthyLabel)
			if got != tc.want {
				t.Errorf("IsNodeUnhealthy() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGetNodeAvoidancePolicy(t *testing.T) {
	testCases := []struct {
		name string
		wl   *kueue.Workload
		want string
	}{
		{
			name: "nil workload",
			wl:   nil,
			want: "",
		},
		{
			name: "workload without annotations",
			wl:   utiltesting.MakeWorkload("wl", "ns").Obj(),
			want: "",
		},
		{
			name: "workload with DisallowUnhealthy",
			wl: utiltesting.MakeWorkload("wl", "ns").
				Annotations(map[string]string{
					constants.NodeAvoidancePolicyAnnotation: constants.NodeAvoidancePolicyDisallowUnhealthy,
				}).Obj(),
			want: constants.NodeAvoidancePolicyDisallowUnhealthy,
		},
		{
			name: "workload with PreferHealthy",
			wl: utiltesting.MakeWorkload("wl", "ns").
				Annotations(map[string]string{
					constants.NodeAvoidancePolicyAnnotation: constants.NodeAvoidancePolicyPreferHealthy,
				}).Obj(),
			want: constants.NodeAvoidancePolicyPreferHealthy,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := GetNodeAvoidancePolicy(tc.wl)
			if got != tc.want {
				t.Errorf("GetNodeAvoidancePolicy() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestConstructNodeAffinity(t *testing.T) {
	testCases := []struct {
		name           string
		policy         string
		unhealthyLabel string
		want           *corev1.NodeAffinity
	}{
		{
			name:           "empty label",
			policy:         constants.NodeAvoidancePolicyDisallowUnhealthy,
			unhealthyLabel: "",
			want:           nil,
		},
		{
			name:           "unknown policy",
			policy:         "unknown",
			unhealthyLabel: "unhealthy",
			want:           nil,
		},
		{
			name:           "DisallowUnhealthy",
			policy:         constants.NodeAvoidancePolicyDisallowUnhealthy,
			unhealthyLabel: "unhealthy",
			want: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{
						{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									Key:      "unhealthy",
									Operator: corev1.NodeSelectorOpDoesNotExist,
								},
							},
						},
					},
				},
			},
		},
		{
			name:           "PreferHealthy",
			policy:         constants.NodeAvoidancePolicyPreferHealthy,
			unhealthyLabel: "unhealthy",
			want: &corev1.NodeAffinity{
				PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{
					{
						Weight: 100,
						Preference: corev1.NodeSelectorTerm{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									Key:      "unhealthy",
									Operator: corev1.NodeSelectorOpDoesNotExist,
								},
							},
						},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := ConstructNodeAffinity(tc.policy, tc.unhealthyLabel)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ConstructNodeAffinity() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
