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

package webhooks

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestValidateClusterQueue(t *testing.T) {
	specPath := field.NewPath("spec")
	resourceGroupsPath := specPath.Child("resourceGroups")

	testcases := []struct {
		name         string
		clusterQueue *kueue.ClusterQueue
		wantErr      field.ErrorList
		wantDetail   string
		wantBadValue string
	}{
		{
			name: "built-in resources with qualified names",
			clusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("cpu").Obj()).
				Obj(),
		},
		{
			name: "invalid resource name",
			clusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("@cpu").Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(resourceGroupsPath.Index(0).Child("coveredResources").Index(0), "@cpu", ""),
			},
		},
		{
			name: "admissionChecks defined",
			clusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").
				AdmissionChecks("ac1").
				Obj(),
		},
		{
			name: "admissionCheckStrategy defined",
			clusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").
				AdmissionCheckStrategy(
					*utiltestingapi.MakeAdmissionCheckStrategyRule("ac1", "flavor1").Obj(),
				).Obj(),
		},
		{
			name:         "in cohort",
			clusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").Cohort("prod").Obj(),
		},
		{
			name: "extended resources with qualified names",
			clusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("example.com/gpu").Obj()).
				Obj(),
		},
		{
			name: "flavor with qualified names",
			clusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("x86").Obj()).
				Obj(),
		},
		{
			name: "flavor quota with negative value",
			clusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("x86").Resource("cpu", "-1").Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(resourceGroupsPath.Index(0).Child("flavors").Index(0).Child("resources").Index(0).Child("nominalQuota"), "-1", ""),
			},
		},
		{
			name: "flavor quota with zero value",
			clusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("x86").Resource("cpu", "0").Obj()).
				Obj(),
		},
		{
			name: "flavor quota with borrowingLimit 0",
			clusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("x86").Resource("cpu", "1", "0").Obj()).
				Cohort("cohort").
				Obj(),
		},
		{
			name: "flavor quota with negative borrowingLimit",
			clusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("x86").Resource("cpu", "1", "-1").Obj()).
				Cohort("cohort").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(resourceGroupsPath.Index(0).Child("flavors").Index(0).Child("resources").Index(0).Child("borrowingLimit"), "-1", ""),
			},
		},
		{
			name: "flavor quota with lendingLimit 0",
			clusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("x86").Resource("cpu", "1", "", "0").Obj()).
				Cohort("cohort").
				Obj(),
		},
		{
			name: "flavor quota with negative lendingLimit",
			clusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("x86").Resource("cpu", "1", "", "-1").Obj()).
				Cohort("cohort").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(resourceGroupsPath.Index(0).Child("flavors").Index(0).Child("resources").Index(0).Child("lendingLimit"), "-1", ""),
			},
		},
		{
			name: "flavor quota with lendingLimit and empty cohort",
			clusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("x86").Resource("cpu", "1", "", "1").Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(resourceGroupsPath.Index(0).Child("flavors").Index(0).Child("resources").Index(0).Child("lendingLimit"), "1", "must be nil when cohort is empty"),
			},
		},
		{
			name: "flavor quota with lendingLimit greater than nominalQuota",
			clusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("x86").Resource("cpu", "1", "", "2").Obj()).
				Cohort("cohort").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(resourceGroupsPath.Index(0).Child("flavors").Index(0).Child("resources").Index(0).Child("lendingLimit"), "2", lendingLimitErrorMsg),
			},
		},
		{
			name: "empty queueing strategy is supported",
			clusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").
				QueueingStrategy("").
				Obj(),
		},
		{
			name: "namespaceSelector with invalid labels",
			clusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").NamespaceSelector(&metav1.LabelSelector{
				MatchLabels: map[string]string{"nospecialchars^=@": "bar"},
			}).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(specPath.Child("namespaceSelector", "matchLabels"), "nospecialchars^=@", "").
					WithOrigin("format=k8s-label-key"),
			},
		},
		{
			name: "namespaceSelector with invalid expressions",
			clusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").NamespaceSelector(&metav1.LabelSelector{
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
			clusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("alpha").
						Resource("cpu", "0").
						Resource("memory", "0").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("beta").
						Resource("cpu", "0").
						Resource("memory", "0").
						Obj(),
				).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("gamma").
						Resource("example.com/gpu", "0").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("omega").
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
								*utiltestingapi.MakeFlavorQuotas("alpha").
									Resource("cpu", "0").
									Resource("memory", "0").
									Obj(),
								*utiltestingapi.MakeFlavorQuotas("beta").
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
			clusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("alpha").
						Resource("cpu", "0").
						Resource("memory", "0").
						Obj(),
				).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("beta").
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
			clusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("alpha").Resource("cpu").Obj(),
					*utiltestingapi.MakeFlavorQuotas("beta").Resource("cpu").Obj(),
				).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("beta").Resource("memory").Obj(),
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
		{
			name: "flavorFungibility preference set but whenCanPreempt != TryNextFlavor",
			clusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.TryNextFlavor,
					WhenCanPreempt: kueue.MayStopSearch,
					Preference:     ptr.To(kueue.BorrowingOverPreemption),
				}).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(specPath.Child("flavorFungibility", "preference"), "", ""),
			},
			wantDetail:   `preference "BorrowingOverPreemption" requires both whenCanBorrow and whenCanPreempt to be TryNextFlavor`,
			wantBadValue: string(kueue.BorrowingOverPreemption),
		},
		{
			name: "flavorFungibility preference set but whenCanBorrow != TryNextFlavor",
			clusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.MayStopSearch,
					WhenCanPreempt: kueue.MayStopSearch,
					Preference:     ptr.To(kueue.BorrowingOverPreemption),
				}).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(specPath.Child("flavorFungibility", "preference"), "", ""),
			},
			wantDetail:   `preference "BorrowingOverPreemption" requires both whenCanBorrow and whenCanPreempt to be TryNextFlavor`,
			wantBadValue: string(kueue.BorrowingOverPreemption),
		},
		{
			name: "flavorFungibility preference BorrowingOverPreemption with both TryNextFlavor is valid",
			clusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.TryNextFlavor,
					WhenCanPreempt: kueue.TryNextFlavor,
					Preference:     ptr.To(kueue.BorrowingOverPreemption),
				}).Obj(),
		},
		{
			name: "flavorFungibility preference PreemptionOverBorrowing with both TryNextFlavor is valid",
			clusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.TryNextFlavor,
					WhenCanPreempt: kueue.TryNextFlavor,
					Preference:     ptr.To(kueue.PreemptionOverBorrowing),
				}).Obj(),
		},
		{
			name: "flavorFungibility preference PreemptionOverBorrowing but whenCanBorrow != TryNextFlavor",
			clusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.MayStopSearch,
					WhenCanPreempt: kueue.TryNextFlavor,
					Preference:     ptr.To(kueue.PreemptionOverBorrowing),
				}).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(specPath.Child("flavorFungibility", "preference"), "", ""),
			},
			wantDetail:   `preference "PreemptionOverBorrowing" requires both whenCanBorrow and whenCanPreempt to be TryNextFlavor`,
			wantBadValue: string(kueue.PreemptionOverBorrowing),
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			gotErr := ValidateClusterQueue(tc.clusterQueue)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "Detail", "BadValue")); diff != "" {
				t.Errorf("ValidateResources() mismatch (-want +got):\n%s", diff)
			}
			if tc.wantDetail != "" || tc.wantBadValue != "" {
				if len(gotErr) == 0 {
					t.Fatalf("expected an error but got none")
				}
				if tc.wantDetail != "" && gotErr[0].Detail != tc.wantDetail {
					t.Fatalf("unexpected error detail, want %q got %q", tc.wantDetail, gotErr[0].Detail)
				}
				if tc.wantBadValue != "" {
					gotBad := fmt.Sprint(gotErr[0].BadValue)
					if gotBad != tc.wantBadValue {
						t.Fatalf("unexpected bad value, want %q got %q", tc.wantBadValue, gotBad)
					}
				}
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
			newClusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").QueueingStrategy("BestEffortFIFO").Obj(),
			oldClusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").QueueingStrategy("StrictFIFO").Obj(),
			wantErr:         nil,
		},
		{
			name:            "same queueingStrategy",
			newClusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").QueueingStrategy("BestEffortFIFO").Obj(),
			oldClusterQueue: utiltestingapi.MakeClusterQueue("cluster-queue").QueueingStrategy("BestEffortFIFO").Obj(),
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

func makeFlavors(n int) []kueue.FlavorQuotas {
	flavs := make([]kueue.FlavorQuotas, 0, n)
	for i := range n {
		flavs = append(flavs, kueue.FlavorQuotas{
			Name: kueue.ResourceFlavorReference(fmt.Sprintf("f%03d", i)),
			Resources: []kueue.ResourceQuota{{
				Name:         corev1.ResourceCPU,
				NominalQuota: resource.MustParse("1"),
			}},
		})
	}
	return flavs
}

func TestValidateTotalFlavors(t *testing.T) {
	testcases := []struct {
		name       string
		numFlavors int
		wantErr    bool
	}{
		{"within limit (10 flavors)", 10, false},
		{"At limit (256 flavors)", 256, false},
		{"over limit (257 flavors)", 257, true},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			cq := utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(makeFlavors(tc.numFlavors)...).Obj()

			gotErr := ValidateClusterQueue(cq)

			if diff := cmp.Diff(tc.wantErr, len(gotErr) > 0); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
		})
	}
}

func makeCoveredResources(n int) []kueue.ResourceGroup {
	resources := make([]corev1.ResourceName, n)
	quotas := make([]kueue.ResourceQuota, n)
	for i := range n {
		name := corev1.ResourceName(fmt.Sprintf("res%03d", i))
		resources[i] = name
		quotas[i] = kueue.ResourceQuota{
			Name:         name,
			NominalQuota: resource.MustParse("1"),
		}
	}

	return []kueue.ResourceGroup{{
		CoveredResources: resources,
		Flavors: []kueue.FlavorQuotas{{
			Name:      "default",
			Resources: quotas,
		}},
	}}
}

func TestValidateTotalCoveredResources(t *testing.T) {
	testcases := []struct {
		name          string
		numCoveredRes int
		wantErr       bool
	}{
		{"within limit (10 covered resources)", 10, false},
		{"At limit (256 covered resources)", 256, false},
		{"over limit (257 covered resources)", 257, true},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			cq := utiltestingapi.MakeClusterQueue("cluster-queue").
				Obj()
			cq.Spec.ResourceGroups = makeCoveredResources(tc.numCoveredRes)

			gotErr := ValidateClusterQueue(cq)

			if diff := cmp.Diff(tc.wantErr, len(gotErr) > 0); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
		})
	}
}

func makeFlavorResourceCombinations(numFlavors, numResources int) []kueue.ResourceGroup {
	flavors := make([]kueue.FlavorQuotas, 0, numFlavors)
	resources := make([]corev1.ResourceName, numResources)
	for j := range numResources {
		resources[j] = corev1.ResourceName(fmt.Sprintf("res%03d", j))
	}

	for i := range numFlavors {
		quotas := make([]kueue.ResourceQuota, numResources)
		for j := range numResources {
			quotas[j] = kueue.ResourceQuota{
				Name:         resources[j],
				NominalQuota: resource.MustParse("1"),
			}
		}
		flavors = append(flavors, kueue.FlavorQuotas{
			Name:      kueue.ResourceFlavorReference(fmt.Sprintf("f%03d", i)),
			Resources: quotas,
		})
	}

	return []kueue.ResourceGroup{{
		CoveredResources: resources,
		Flavors:          flavors,
	}}
}

func TestValidateFlavorResourceCombinations(t *testing.T) {
	testcases := []struct {
		name         string
		numFlavors   int
		numResources int
		wantErr      bool
	}{
		{"within limit (1 flavor × 10 resources = 10)", 1, 10, false},
		{"at limit (8 flavors × 64 resources = 512)", 8, 64, false},
		{"over limit (9 flavors × 64 resources = 576)", 9, 64, true},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			cq := utiltestingapi.MakeClusterQueue("cluster-queue").Obj()
			cq.Spec.ResourceGroups = makeFlavorResourceCombinations(tc.numFlavors, tc.numResources)

			gotErr := ValidateClusterQueue(cq)

			if diff := cmp.Diff(tc.wantErr, len(gotErr) > 0); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
		})
	}
}
