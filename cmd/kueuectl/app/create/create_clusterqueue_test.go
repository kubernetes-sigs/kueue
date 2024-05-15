/*
Copyright 2024 The Kubernetes Authors.

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

package create

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

func TestCreateClusterQueue(t *testing.T) {
	testCases := map[string]struct {
		options  *ClusterQueueOptions
		expected *v1beta1.ClusterQueue
	}{
		"success_create": {
			options: &ClusterQueueOptions{
				Name:             "cq1",
				Cohort:           "cohort",
				QueueingStrategy: "StrictFIFO",
				NamespaceSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{"foo": "bar"},
				},
				ReclaimWithinCohort:          "Any",
				PreemptionWithinClusterQueue: "LowerPriority",
				ResourceGroups: []v1beta1.ResourceGroup{
					{
						CoveredResources: []corev1.ResourceName{},
						Flavors: []v1beta1.FlavorQuotas{
							{
								Name: "alpha",
								Resources: []v1beta1.ResourceQuota{
									{
										Name:           "cpu",
										NominalQuota:   resource.MustParse("0"),
										BorrowingLimit: ptr.To(resource.MustParse("0")),
										LendingLimit:   ptr.To(resource.MustParse("0")),
									},
									{
										Name:           "memory",
										NominalQuota:   resource.MustParse("0"),
										BorrowingLimit: ptr.To(resource.MustParse("0")),
										LendingLimit:   ptr.To(resource.MustParse("0")),
									},
								},
							},
						},
					},
				},
			},
			expected: &v1beta1.ClusterQueue{
				TypeMeta:   metav1.TypeMeta{APIVersion: "kueue.x-k8s.io/v1beta1", Kind: "ClusterQueue"},
				ObjectMeta: metav1.ObjectMeta{Name: "cq1"},
				Spec: v1beta1.ClusterQueueSpec{
					Cohort:           "cohort",
					QueueingStrategy: v1beta1.StrictFIFO,
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"foo": "bar"},
					},
					Preemption: &v1beta1.ClusterQueuePreemption{
						ReclaimWithinCohort: v1beta1.PreemptionPolicyAny,
						WithinClusterQueue:  v1beta1.PreemptionPolicyLowerPriority,
					},
					ResourceGroups: []v1beta1.ResourceGroup{
						{
							CoveredResources: []corev1.ResourceName{},
							Flavors: []v1beta1.FlavorQuotas{
								{
									Name: "alpha",
									Resources: []v1beta1.ResourceQuota{
										{
											Name:           "cpu",
											NominalQuota:   resource.MustParse("0"),
											BorrowingLimit: ptr.To(resource.MustParse("0")),
											LendingLimit:   ptr.To(resource.MustParse("0")),
										},
										{
											Name:           "memory",
											NominalQuota:   resource.MustParse("0"),
											BorrowingLimit: ptr.To(resource.MustParse("0")),
											LendingLimit:   ptr.To(resource.MustParse("0")),
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			cq := tc.options.createClusterQueue()
			if diff := cmp.Diff(tc.expected, cq); diff != "" {
				t.Errorf("Unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}
