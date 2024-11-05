/*
Copyright 2021 The Kubernetes Authors.

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

package webhooks

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	testingutil "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestValidateClusterQueue(t *testing.T) {
	specPath := field.NewPath("spec")
	resourceGroupsPath := specPath.Child("resourceGroups")

	testcases := []struct {
		name                string
		clusterQueue        *kueue.ClusterQueue
		wantErr             field.ErrorList
		disableLendingLimit bool
	}{
		{
			name: "built-in resources with qualified names",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").
				ResourceGroup(*testingutil.MakeFlavorQuotas("default").Resource("cpu").Obj()).
				Obj(),
		},
		{
			name: "invalid resource name",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").
				ResourceGroup(*testingutil.MakeFlavorQuotas("default").Resource("@cpu").Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(resourceGroupsPath.Index(0).Child("coveredResources").Index(0), "@cpu", ""),
			},
		},
		{
			name: "admissionChecks defined",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").
				AdmissionChecks("ac1").
				Obj(),
		},
		{
			name: "admissionCheckStrategy defined",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").
				AdmissionCheckStrategy(
					*testingutil.MakeAdmissionCheckStrategyRule("ac1", "flavor1").Obj(),
				).Obj(),
		},
		{
			name: "both admissionChecks and admissionCheckStrategy is defined",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").
				AdmissionChecks("ac1").
				AdmissionCheckStrategy().Obj(),
			wantErr: field.ErrorList{
				field.Invalid(specPath, "spec", "Either AdmissionChecks or AdmissionCheckStrategy can be set, but not both"),
			},
		},
		{
			name:         "in cohort",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").Cohort("prod").Obj(),
		},
		{
			name: "extended resources with qualified names",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").
				ResourceGroup(*testingutil.MakeFlavorQuotas("default").Resource("example.com/gpu").Obj()).
				Obj(),
		},
		{
			name: "flavor with qualified names",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").
				ResourceGroup(*testingutil.MakeFlavorQuotas("x86").Obj()).
				Obj(),
		},
		{
			name: "flavor quota with negative value",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testingutil.MakeFlavorQuotas("x86").Resource("cpu", "-1").Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(resourceGroupsPath.Index(0).Child("flavors").Index(0).Child("resources").Index(0).Child("nominalQuota"), "-1", ""),
			},
		},
		{
			name: "flavor quota with zero value",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testingutil.MakeFlavorQuotas("x86").Resource("cpu", "0").Obj()).
				Obj(),
		},
		{
			name: "flavor quota with borrowingLimit 0",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testingutil.MakeFlavorQuotas("x86").Resource("cpu", "1", "0").Obj()).
				Cohort("cohort").
				Obj(),
		},
		{
			name: "flavor quota with negative borrowingLimit",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testingutil.MakeFlavorQuotas("x86").Resource("cpu", "1", "-1").Obj()).
				Cohort("cohort").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(resourceGroupsPath.Index(0).Child("flavors").Index(0).Child("resources").Index(0).Child("borrowingLimit"), "-1", ""),
			},
		},
		{
			name: "flavor quota with lendingLimit 0",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testingutil.MakeFlavorQuotas("x86").Resource("cpu", "1", "", "0").Obj()).
				Cohort("cohort").
				Obj(),
		},
		{
			name: "flavor quota with negative lendingLimit",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testingutil.MakeFlavorQuotas("x86").Resource("cpu", "1", "", "-1").Obj()).
				Cohort("cohort").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(resourceGroupsPath.Index(0).Child("flavors").Index(0).Child("resources").Index(0).Child("lendingLimit"), "-1", ""),
			},
		},
		{
			name: "flavor quota with lendingLimit and empty cohort",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testingutil.MakeFlavorQuotas("x86").Resource("cpu", "1", "", "1").Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(resourceGroupsPath.Index(0).Child("flavors").Index(0).Child("resources").Index(0).Child("lendingLimit"), "1", limitIsEmptyErrorMsg),
			},
		},
		{
			name: "flavor quota with lendingLimit greater than nominalQuota",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testingutil.MakeFlavorQuotas("x86").Resource("cpu", "1", "", "2").Obj()).
				Cohort("cohort").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(resourceGroupsPath.Index(0).Child("flavors").Index(0).Child("resources").Index(0).Child("lendingLimit"), "2", lendingLimitErrorMsg),
			},
		},
		{
			name:                "flavor quota with lendingLimit and empty cohort, but feature disabled",
			disableLendingLimit: true,
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testingutil.MakeFlavorQuotas("x86").Resource("cpu", "1", "", "1").Obj()).
				Obj(),
		},
		{
			name: "empty queueing strategy is supported",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").
				QueueingStrategy("").
				Obj(),
		},
		{
			name: "namespaceSelector with invalid labels",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").NamespaceSelector(&metav1.LabelSelector{
				MatchLabels: map[string]string{"nospecialchars^=@": "bar"},
			}).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(specPath.Child("namespaceSelector", "matchLabels"), "nospecialchars^=@", ""),
			},
		},
		{
			name: "namespaceSelector with invalid expressions",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").NamespaceSelector(&metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      "key",
						Operator: "In",
					},
				},
			}).Obj(),
			wantErr: field.ErrorList{
				field.Required(specPath.Child("namespaceSelector", "matchExpressions").Index(0).Child("values"), ""),
			},
		},
		{
			name: "multiple resource groups",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testingutil.MakeFlavorQuotas("alpha").
						Resource("cpu", "0").
						Resource("memory", "0").
						Obj(),
					*testingutil.MakeFlavorQuotas("beta").
						Resource("cpu", "0").
						Resource("memory", "0").
						Obj(),
				).
				ResourceGroup(
					*testingutil.MakeFlavorQuotas("gamma").
						Resource("example.com/gpu", "0").
						Obj(),
					*testingutil.MakeFlavorQuotas("omega").
						Resource("example.com/gpu", "0").
						Obj(),
				).
				Obj(),
		},
		{
			name: "resources in a flavor in different order",
			clusterQueue: &kueue.ClusterQueue{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster-queue",
				},
				Spec: kueue.ClusterQueueSpec{
					ResourceGroups: []kueue.ResourceGroup{
						{
							CoveredResources: []corev1.ResourceName{"cpu", "memory"},
							Flavors: []kueue.FlavorQuotas{
								*testingutil.MakeFlavorQuotas("alpha").
									Resource("cpu", "0").
									Resource("memory", "0").
									Obj(),
								*testingutil.MakeFlavorQuotas("beta").
									Resource("memory", "0").
									Resource("cpu", "0").
									Obj(),
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				field.Invalid(resourceGroupsPath.Index(0).Child("flavors").Index(1).Child("resources").Index(0).Child("name"), nil, ""),
				field.Invalid(resourceGroupsPath.Index(0).Child("flavors").Index(1).Child("resources").Index(1).Child("name"), nil, ""),
			},
		},
		{
			name: "resource in more than one resource group",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testingutil.MakeFlavorQuotas("alpha").
						Resource("cpu", "0").
						Resource("memory", "0").
						Obj(),
				).
				ResourceGroup(
					*testingutil.MakeFlavorQuotas("beta").
						Resource("memory", "0").
						Obj(),
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Duplicate(resourceGroupsPath.Index(1).Child("coveredResources").Index(0), nil),
			},
		},
		{
			name: "flavor in more than one resource group",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testingutil.MakeFlavorQuotas("alpha").Resource("cpu").Obj(),
					*testingutil.MakeFlavorQuotas("beta").Resource("cpu").Obj(),
				).
				ResourceGroup(
					*testingutil.MakeFlavorQuotas("beta").Resource("memory").Obj(),
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Duplicate(resourceGroupsPath.Index(1).Child("flavors").Index(0).Child("name"), nil),
			},
		},
		{
			name: "valid preemption with borrowWithinCohort",
			clusterQueue: &kueue.ClusterQueue{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster-queue",
				},
				Spec: kueue.ClusterQueueSpec{
					Preemption: &kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
							MaxPriorityThreshold: ptr.To[int32](10),
						},
					},
				},
			},
		},
		{
			name: "existing cluster queue created with older Kueue version that has a nil borrowWithinCohort field",
			clusterQueue: &kueue.ClusterQueue{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster-queue",
				},
				Spec: kueue.ClusterQueueSpec{
					Preemption: &kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyNever,
					},
				},
			},
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.disableLendingLimit {
				features.SetFeatureGateDuringTest(t, features.LendingLimit, false)
			}
			gotErr := ValidateClusterQueue(tc.clusterQueue)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "Detail", "BadValue")); diff != "" {
				t.Errorf("ValidateResources() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidateClusterQueueUpdate(t *testing.T) {
	testcases := []struct {
		name            string
		newClusterQueue *kueue.ClusterQueue
		oldClusterQueue *kueue.ClusterQueue
		wantErr         field.ErrorList
	}{
		{
			name:            "queueingStrategy can be updated",
			newClusterQueue: testingutil.MakeClusterQueue("cluster-queue").QueueingStrategy("BestEffortFIFO").Obj(),
			oldClusterQueue: testingutil.MakeClusterQueue("cluster-queue").QueueingStrategy("StrictFIFO").Obj(),
			wantErr:         nil,
		},
		{
			name:            "same queueingStrategy",
			newClusterQueue: testingutil.MakeClusterQueue("cluster-queue").QueueingStrategy("BestEffortFIFO").Obj(),
			oldClusterQueue: testingutil.MakeClusterQueue("cluster-queue").QueueingStrategy("BestEffortFIFO").Obj(),
			wantErr:         nil,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			gotErr := ValidateClusterQueueUpdate(tc.newClusterQueue)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "Detail", "BadValue")); diff != "" {
				t.Errorf("ValidateResources() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
