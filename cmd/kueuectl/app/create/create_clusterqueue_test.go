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
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
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
							*utiltesting.MakeFlavorQuotas("alpha").
								Resource("cpu", "0", "0", "0").
								Resource("memory", "0", "0", "0").
								Obj(),
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
								*utiltesting.MakeFlavorQuotas("alpha").
									Resource("cpu", "0", "0", "0").
									Resource("memory", "0", "0", "0").
									Obj(),
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

func TestParseResourceQuotas(t *testing.T) {
	testCases := map[string]struct {
		quotaArgs          []string
		borrowingArgs      []string
		lendingArgs        []string
		wantErr            error
		wantResourceGroups []v1beta1.ResourceGroup
	}{
		"should create one resource group with one flavor and nominalQuota set": {
			quotaArgs: []string{"alpha-2.0:cpu=1;memory=1;example-1.org/memory=5Gi"},
			wantResourceGroups: []v1beta1.ResourceGroup{
				{
					CoveredResources: []corev1.ResourceName{"cpu", "memory", "example-1.org/memory"},
					Flavors: []v1beta1.FlavorQuotas{
						*utiltesting.MakeFlavorQuotas("alpha-2.0").
							Resource("cpu", "1").
							Resource("memory", "1").
							Resource("example-1.org/memory", "5Gi").
							Obj(),
					},
				},
			},
		},
		"should create one resource group with one flavor and borrowingLimit set": {
			borrowingArgs: []string{"alpha:cpu=1;memory=1"},
			wantResourceGroups: []v1beta1.ResourceGroup{
				{
					CoveredResources: []corev1.ResourceName{"cpu", "memory"},
					Flavors: []v1beta1.FlavorQuotas{
						*utiltesting.MakeFlavorQuotas("alpha").
							Resource("cpu", "0", "1").
							Resource("memory", "0", "1").
							Obj(),
					},
				},
			},
		},
		"should create one resource group with one flavor and lendingLimit set": {
			lendingArgs: []string{"alpha:cpu=1;memory=1"},
			wantResourceGroups: []v1beta1.ResourceGroup{
				{
					CoveredResources: []corev1.ResourceName{"cpu", "memory"},
					Flavors: []v1beta1.FlavorQuotas{
						*utiltesting.MakeFlavorQuotas("alpha").
							Resource("cpu", "0", "", "1").
							Resource("memory", "0", "", "1").
							Obj(),
					},
				},
			},
		},
		"should create one resource group with two flavors and nominalQuota set": {
			quotaArgs: []string{"alpha:example.com/gpu=1", "beta:example.com/gpu=2"},
			wantResourceGroups: []v1beta1.ResourceGroup{
				{
					CoveredResources: []corev1.ResourceName{"example.com/gpu"},
					Flavors: []v1beta1.FlavorQuotas{
						*utiltesting.MakeFlavorQuotas("alpha").
							Resource("example.com/gpu", "1").
							Obj(),
						*utiltesting.MakeFlavorQuotas("beta").
							Resource("example.com/gpu", "2").
							Obj(),
					},
				},
			},
		},
		"should create one resource group when flavors listing resources in different order": {
			quotaArgs: []string{"alpha:cpu=1;memory=1", "beta:memory=2;cpu=2"},
			wantResourceGroups: []v1beta1.ResourceGroup{
				{
					CoveredResources: []corev1.ResourceName{"cpu", "memory"},
					Flavors: []v1beta1.FlavorQuotas{
						*utiltesting.MakeFlavorQuotas("alpha").
							Resource("cpu", "1").
							Resource("memory", "1").
							Obj(),
						*utiltesting.MakeFlavorQuotas("beta").
							Resource("memory", "2").
							Resource("cpu", "2").
							Obj(),
					},
				},
			},
		},
		"should create two resource groups with one flavor each and nominalQuota set": {
			quotaArgs: []string{"alpha:cpu=1;memory=1", "beta:example.com/gpu=2"},
			wantResourceGroups: []v1beta1.ResourceGroup{
				{
					CoveredResources: []corev1.ResourceName{"cpu", "memory"},
					Flavors: []v1beta1.FlavorQuotas{
						*utiltesting.MakeFlavorQuotas("alpha").
							Resource("cpu", "1").
							Resource("memory", "1").
							Obj(),
					},
				},
				{
					CoveredResources: []corev1.ResourceName{"example.com/gpu"},
					Flavors: []v1beta1.FlavorQuotas{
						*utiltesting.MakeFlavorQuotas("beta").
							Resource("example.com/gpu", "2").
							Obj(),
					},
				},
			},
		},
		"should create two resource groups with multiple flavors and nominalQuota set": {
			quotaArgs: []string{"alpha:cpu=1;memory=1", "beta:example.com/gpu=2", "gamma:cpu=2;memory=2"},
			wantResourceGroups: []v1beta1.ResourceGroup{
				{
					CoveredResources: []corev1.ResourceName{"cpu", "memory"},
					Flavors: []v1beta1.FlavorQuotas{
						*utiltesting.MakeFlavorQuotas("alpha").
							Resource("cpu", "1").
							Resource("memory", "1").
							Obj(),
						*utiltesting.MakeFlavorQuotas("gamma").
							Resource("cpu", "2").
							Resource("memory", "2").
							Obj(),
					},
				},
				{
					CoveredResources: []corev1.ResourceName{"example.com/gpu"},
					Flavors: []v1beta1.FlavorQuotas{
						*utiltesting.MakeFlavorQuotas("beta").
							Resource("example.com/gpu", "2").
							Obj(),
					},
				},
			},
		},
		"should create one resource group with one flavor and all quotas set": {
			quotaArgs:     []string{"alpha:cpu=1;memory=2"},
			borrowingArgs: []string{"alpha:cpu=1;memory=2"},
			lendingArgs:   []string{"alpha:cpu=1;memory=2"},
			wantResourceGroups: []v1beta1.ResourceGroup{
				{
					CoveredResources: []corev1.ResourceName{"cpu", "memory"},
					Flavors: []v1beta1.FlavorQuotas{
						*utiltesting.MakeFlavorQuotas("alpha").
							Resource("cpu", "1", "1", "1").
							Resource("memory", "2", "2", "2").
							Obj(),
					},
				},
			},
		},
		"should create one resource group with two flavors and all quotas set": {
			quotaArgs:     []string{"alpha:example.com/gpu=1", "beta:example.com/gpu=2"},
			borrowingArgs: []string{"alpha:example.com/gpu=1", "beta:example.com/gpu=2"},
			lendingArgs:   []string{"alpha:example.com/gpu=1", "beta:example.com/gpu=2"},
			wantResourceGroups: []v1beta1.ResourceGroup{
				{
					CoveredResources: []corev1.ResourceName{"example.com/gpu"},
					Flavors: []v1beta1.FlavorQuotas{
						*utiltesting.MakeFlavorQuotas("alpha").
							Resource("example.com/gpu", "1", "1", "1").
							Obj(),
						*utiltesting.MakeFlavorQuotas("beta").
							Resource("example.com/gpu", "2", "2", "2").
							Obj(),
					},
				},
			},
		},
		"should create two resource groups with one flavor each and all quotas set": {
			quotaArgs:     []string{"alpha:cpu=1;memory=1", "beta:example.com/gpu=2"},
			borrowingArgs: []string{"alpha:cpu=1;memory=1", "beta:example.com/gpu=2"},
			lendingArgs:   []string{"alpha:cpu=1;memory=1", "beta:example.com/gpu=2"},
			wantResourceGroups: []v1beta1.ResourceGroup{
				{
					CoveredResources: []corev1.ResourceName{"cpu", "memory"},
					Flavors: []v1beta1.FlavorQuotas{
						*utiltesting.MakeFlavorQuotas("alpha").
							Resource("cpu", "1", "1", "1").
							Resource("memory", "1", "1", "1").
							Obj(),
					},
				},
				{
					CoveredResources: []corev1.ResourceName{"example.com/gpu"},
					Flavors: []v1beta1.FlavorQuotas{
						*utiltesting.MakeFlavorQuotas("beta").
							Resource("example.com/gpu", "2", "2", "2").
							Obj(),
					},
				},
			},
		},
		"should create two resource groups with multiple flavors and all quotas set": {
			quotaArgs:     []string{"alpha:cpu=1;memory=1", "beta:example.com/gpu=2", "gamma:cpu=2;memory=2"},
			borrowingArgs: []string{"alpha:cpu=1;memory=1", "beta:example.com/gpu=2", "gamma:cpu=2;memory=2"},
			lendingArgs:   []string{"alpha:cpu=1;memory=1", "beta:example.com/gpu=2", "gamma:cpu=2;memory=2"},
			wantResourceGroups: []v1beta1.ResourceGroup{
				{
					CoveredResources: []corev1.ResourceName{"cpu", "memory"},
					Flavors: []v1beta1.FlavorQuotas{
						*utiltesting.MakeFlavorQuotas("alpha").
							Resource("cpu", "1", "1", "1").
							Resource("memory", "1", "1", "1").
							Obj(),
						*utiltesting.MakeFlavorQuotas("gamma").
							Resource("cpu", "2", "2", "2").
							Resource("memory", "2", "2", "2").
							Obj(),
					},
				},
				{
					CoveredResources: []corev1.ResourceName{"example.com/gpu"},
					Flavors: []v1beta1.FlavorQuotas{
						*utiltesting.MakeFlavorQuotas("beta").
							Resource("example.com/gpu", "2", "2", "2").
							Obj(),
					},
				},
			},
		},
		"should fail to create a resource group with an invalid flavor and one quota set": {
			quotaArgs: []string{"alpha:cpu=1;memory=1", "alpha:example.com/gpu=2"},
			wantErr:   errInvalidFlavor,
		},
		"should fail to create when one resource is shared by multiple resource groups": {
			quotaArgs: []string{"alpha:cpu=1;memory=1", "beta:cpu=1"},
			wantErr:   errInvalidResourceGroup,
		},
		"should fail to create a resource group with an invalid flavor and multiple quotas set": {
			quotaArgs:     []string{"alpha:cpu=1;memory=1"},
			borrowingArgs: []string{"alpha:example.com/gpu=2"},
			wantErr:       errInvalidFlavor,
		},
		"should fail when invalid resource quotas": {
			quotaArgs: []string{"alpha:cpu=;memory=1"},
			wantErr:   errInvalidResourcesSpec,
		},
		"should fail when invalid resources separator": {
			quotaArgs: []string{"alpha:cpu=0,memory=1"},
			wantErr:   errInvalidResourcesSpec,
		},
		"should fail when invalid param": {
			quotaArgs: []string{"alpha"},
			wantErr:   errInvalidResourcesSpec,
		},
		"should fail when invalid flavor specification": {
			quotaArgs: []string{"alpha=cpu=0;memory=1"},
			wantErr:   errInvalidResourcesSpec,
		},
		"should fail when invalid quantity": {
			quotaArgs: []string{"alpha:cpu=a;memory=1"},
			wantErr:   errInvalidResourceQuota,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			cqOptions := ClusterQueueOptions{
				UserSpecifiedNominalQuota:   tc.quotaArgs,
				UserSpecifiedBorrowingLimit: tc.borrowingArgs,
				UserSpecifiedLendingLimit:   tc.lendingArgs,
			}
			gotErr := cqOptions.parseResourceGroups()

			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(cqOptions.ResourceGroups, tc.wantResourceGroups); diff != "" {
				t.Errorf("Unexpected ResourceGroups (-want,+got):\n%s", diff)
			}
		})
	}
}
