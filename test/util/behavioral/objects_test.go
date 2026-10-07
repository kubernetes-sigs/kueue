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

package behavioral

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestSetNodeCondition(t *testing.T) {
	ctx := context.Background()

	testCases := map[string]struct {
		initialConditions []corev1.NodeCondition
		newCondition      corev1.NodeCondition
		wantStatus        corev1.ConditionStatus
		wantTime          *metav1.Time
	}{
		"adds a missing condition": {
			newCondition: corev1.NodeCondition{
				Type:   corev1.NodeReady,
				Status: corev1.ConditionTrue,
			},
			wantStatus: corev1.ConditionTrue,
		},
		"updates an existing condition": {
			initialConditions: []corev1.NodeCondition{
				{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: metav1.NewTime(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)),
				},
			},
			newCondition: corev1.NodeCondition{
				Type:               corev1.NodeReady,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: metav1.NewTime(time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)),
			},
			wantStatus: corev1.ConditionTrue,
			wantTime:   ptr.To(metav1.NewTime(time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC))),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gomega.RegisterTestingT(t)

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "node"},
				Status: corev1.NodeStatus{
					Conditions: tc.initialConditions,
				},
			}
			k8sClient := utiltesting.NewClientBuilder().
				WithObjects(node).
				WithStatusSubresource(&corev1.Node{}).
				Build()

			SetNodeCondition(ctx, k8sClient, node, &tc.newCondition)

			gotNode := &corev1.Node{}
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(node), gotNode); err != nil {
				t.Fatalf("Failed to get node: %v", err)
			}

			if diff := cmp.Diff(1, len(gotNode.Status.Conditions)); diff != "" {
				t.Fatalf("Unexpected condition count (-want,+got):\n%s", diff)
			}
			gotCondition := utiltas.GetNodeCondition(gotNode, corev1.NodeReady)
			if gotCondition == nil {
				t.Fatalf("Expected node condition %s to be present", corev1.NodeReady)
			}
			if diff := cmp.Diff(tc.wantStatus, gotCondition.Status); diff != "" {
				t.Errorf("Unexpected condition status (-want,+got):\n%s", diff)
			}
			if gotCondition.LastTransitionTime.IsZero() {
				t.Errorf("Expected LastTransitionTime to be set")
			}
			if tc.wantTime != nil {
				if diff := cmp.Diff(*tc.wantTime, gotCondition.LastTransitionTime); diff != "" {
					t.Errorf("Unexpected LastTransitionTime (-want,+got):\n%s", diff)
				}
			}
		})
	}
}
