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

package core

import (
	"fmt"
	"slices"
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

const (
	resourcesMaxItems      = 64
	flavorsMaxItems        = 64
	resourceGroupsMaxItems = 16
)

// defaultFlavorFungibility matches ClusterQueue defaulting (see apis defaulting).
var defaultFlavorFungibility = &kueue.FlavorFungibility{
	WhenCanBorrow:  kueue.MayStopSearch,
	WhenCanPreempt: kueue.TryNextFlavor,
}

var _ = ginkgo.Describe("ClusterQueue Webhook", func() {
	ginkgo.BeforeEach(func() {
		fwk.StartManagerForSpec(ctx, cfg, managerSetup)
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Creating a ClusterQueue", func() {
		// The input ClusterQueues are plain structs rather than MakeClusterQueue
		// wrappers, because MakeClusterQueue pre-fills fields that the webhook is
		// expected to default.
		ginkgo.DescribeTable("Defaulting on creation", func(cq, wantCQ kueue.ClusterQueue) {
			behavioral.MustCreate(ctx, k8sClient, &cq)
			defer func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &cq, true)
			}()
			gomega.Expect(cq).To(gomega.BeComparableTo(wantCQ,
				cmpopts.IgnoreTypes(kueue.ClusterQueueStatus{}),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "UID", "ResourceVersion", "Generation", "CreationTimestamp", "ManagedFields")))
		},
			ginkgo.Entry("All defaults",
				kueue.ClusterQueue{
					Name: "foo",
				},
				*utiltestingapi.MakeClusterQueue("foo").
					NamespaceSelector(nil).
					Finalizers(kueue.ResourceInUseFinalizerName).
					QueueingStrategy(kueue.BestEffortFIFO).
					StopPolicy(kueue.None).
					FlavorFungibility(*defaultFlavorFungibility).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyNever,
						ReclaimWithinCohort: kueue.PreemptionPolicyNever,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy: kueue.BorrowWithinCohortPolicyNever,
						},
					}).Obj(),
			),
			ginkgo.Entry("Preemption overridden",
				kueue.ClusterQueue{
					Name: "foo",
					Spec: kueue.ClusterQueueSpec{
						FlavorFungibility: defaultFlavorFungibility,
						Preemption: &kueue.ClusterQueuePreemption{
							WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
							ReclaimWithinCohort: kueue.PreemptionPolicyAny,
							BorrowWithinCohort: &kueue.BorrowWithinCohort{
								Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
								MaxPriorityThreshold: new(int32(100)),
							},
						},
					},
				},
				*utiltestingapi.MakeClusterQueue("foo").
					NamespaceSelector(nil).
					Finalizers(kueue.ResourceInUseFinalizerName).
					QueueingStrategy(kueue.BestEffortFIFO).
					StopPolicy(kueue.None).
					FlavorFungibility(*defaultFlavorFungibility).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
							MaxPriorityThreshold: new(int32(100)),
						},
					}).
					Obj(),
			),
			ginkgo.Entry("Default fair sharing",
				kueue.ClusterQueue{
					Name: "foo",
					Spec: kueue.ClusterQueueSpec{
						FairSharing: &kueue.FairSharing{},
					},
				},
				*utiltestingapi.MakeClusterQueue("foo").
					NamespaceSelector(nil).
					Finalizers(kueue.ResourceInUseFinalizerName).
					QueueingStrategy(kueue.BestEffortFIFO).
					StopPolicy(kueue.None).
					FlavorFungibility(*defaultFlavorFungibility).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyNever,
						ReclaimWithinCohort: kueue.PreemptionPolicyNever,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy: kueue.BorrowWithinCohortPolicyNever,
						},
					}).
					FairWeight(resource.MustParse("1")).
					Obj(),
			),
			ginkgo.Entry("Default ConcurrentAdmissionPolicy migration mode",
				kueue.ClusterQueue{
					Name: "foo",
					Spec: kueue.ClusterQueueSpec{
						ConcurrentAdmissionPolicy: &kueue.ConcurrentAdmissionPolicy{
							Migration: kueue.ConcurrentAdmissionMigration{
								Constraints: &kueue.ConcurrentAdmissionConstraints{
									LastAcceptableFlavorName: new(kueue.ResourceFlavorReference("default-flavor")),
								},
							},
						},
					},
				},
				*utiltestingapi.MakeClusterQueue("foo").
					NamespaceSelector(nil).
					Finalizers(kueue.ResourceInUseFinalizerName).
					QueueingStrategy(kueue.BestEffortFIFO).
					StopPolicy(kueue.None).
					FlavorFungibility(*defaultFlavorFungibility).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyNever,
						ReclaimWithinCohort: kueue.PreemptionPolicyNever,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy: kueue.BorrowWithinCohortPolicyNever,
						},
					}).
					ConcurrentAdmissionPolicy(kueue.ConcurrentAdmissionTryPreferredFlavors).
					LastAcceptableFlavorName("default-flavor").
					Obj(),
			),
			ginkgo.Entry("Default ConcurrentAdmissionPolicy with empty policy",
				kueue.ClusterQueue{
					Name: "foo",
					Spec: kueue.ClusterQueueSpec{
						ConcurrentAdmissionPolicy: &kueue.ConcurrentAdmissionPolicy{},
					},
				},
				*utiltestingapi.MakeClusterQueue("foo").
					NamespaceSelector(nil).
					Finalizers(kueue.ResourceInUseFinalizerName).
					QueueingStrategy(kueue.BestEffortFIFO).
					StopPolicy(kueue.None).
					FlavorFungibility(*defaultFlavorFungibility).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyNever,
						ReclaimWithinCohort: kueue.PreemptionPolicyNever,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy: kueue.BorrowWithinCohortPolicyNever,
						},
					}).
					ConcurrentAdmissionPolicy(kueue.ConcurrentAdmissionTryPreferredFlavors).
					Obj(),
			),
		)

		ginkgo.It("Should have qualified flavor names when updating", func() {
			ginkgo.By("Creating a new clusterQueue")
			cq := utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("x86").Resource(corev1.ResourceMemory).Obj()).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, cq)

			defer func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			}()

			gomega.Eventually(func(g gomega.Gomega) {
				var updateCQ kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updateCQ)).Should(gomega.Succeed())
				updateCQ.Spec.ResourceGroups[0].Flavors[0].Name = "@x86"
				g.Expect(k8sClient.Update(ctx, &updateCQ)).Should(utiltesting.BeInvalidError())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should allow to update queueingStrategy with different value", func() {
			ginkgo.By("Creating a new clusterQueue")
			cq := utiltestingapi.MakeClusterQueue("cluster-queue").
				QueueingStrategy(kueue.StrictFIFO).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("x86").Resource(corev1.ResourceMemory).Obj()).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, cq)

			defer func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			}()

			gomega.Eventually(func(g gomega.Gomega) {
				var updateCQ kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updateCQ)).Should(gomega.Succeed())
				updateCQ.Spec.QueueingStrategy = kueue.BestEffortFIFO
				g.Expect(k8sClient.Update(ctx, &updateCQ)).Should(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.DescribeTable("Validate ClusterQueue on creation", func(cq *kueue.ClusterQueue, matcher types.GomegaMatcher) {
			err := k8sClient.Create(ctx, cq)
			if err == nil {
				defer func() {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
				}()
			}
			gomega.Expect(err).Should(matcher)
			if matcher == gomega.Succeed() {
				// Validating that defaults are set.
				gomega.Expect(cq.Spec.QueueingStrategy).ToNot(gomega.BeEmpty())
				if cq.Spec.Preemption != nil {
					preemption := cq.Spec.Preemption
					gomega.Expect(preemption.ReclaimWithinCohort).ToNot(gomega.BeEmpty())
					gomega.Expect(preemption.WithinClusterQueue).ToNot(gomega.BeEmpty())
				}
			}
		},
			ginkgo.Entry("Should have non-negative borrowing limit",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "2", "-1").Obj()).
					Cohort("cohort").
					Obj(),
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("Should have non-negative quota value",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "-1").Obj()).
					Obj(),
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("Should have at least one resource",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("foo").Obj()).
					Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should have qualified flavor name",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("invalid_name").Resource(corev1.ResourceCPU, "5").Obj()).
					Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should have qualified resource name",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("x86").Resource("@cpu", "5").Obj()).
					Obj(),
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("Should have valid resources quantity",
				func() *kueue.ClusterQueue {
					flvQuotas := utiltestingapi.MakeFlavorQuotas("flavor")
					for i := range resourcesMaxItems + 1 {
						flvQuotas = flvQuotas.Resource(corev1.ResourceName(fmt.Sprintf("r%d", i)))
					}
					return utiltestingapi.MakeClusterQueue("cluster-queue").ResourceGroup(*flvQuotas.Obj()).Obj()
				}(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should have valid flavors quantity",
				func() *kueue.ClusterQueue {
					flavors := make([]kueue.FlavorQuotas, flavorsMaxItems+1)
					for i := range flavors {
						flavors[i] = *utiltestingapi.MakeFlavorQuotas(fmt.Sprintf("f%d", i)).
							Resource(corev1.ResourceCPU).
							Obj()
					}
					return utiltestingapi.MakeClusterQueue("cluster-queue").ResourceGroup(flavors...).Obj()
				}(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should forbid clusterQueue creation with unqualified labelSelector",
				utiltestingapi.MakeClusterQueue("cluster-queue").NamespaceSelector(&metav1.LabelSelector{
					MatchLabels: map[string]string{"nospecialchars^=@": "bar"},
				}).Obj(),
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("Should forbid to create clusterQueue with unknown clusterQueueingStrategy",
				utiltestingapi.MakeClusterQueue("cluster-queue").QueueingStrategy("unknown").Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should allow to create clusterQueue with empty clusterQueueingStrategy",
				utiltestingapi.MakeClusterQueue("cluster-queue").QueueingStrategy("").Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow to create clusterQueue with empty preemption",
				utiltestingapi.MakeClusterQueue("cluster-queue").Preemption(kueue.ClusterQueuePreemption{}).Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow to create clusterQueue with preemption policies",
				utiltestingapi.MakeClusterQueue("cluster-queue").Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				}).FlavorFungibility(*defaultFlavorFungibility).Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should forbid to create clusterQueue with unknown preemption.withinCohort",
				utiltestingapi.MakeClusterQueue("cluster-queue").Preemption(kueue.ClusterQueuePreemption{ReclaimWithinCohort: "unknown"}).Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should forbid to create clusterQueue with unknown preemption.withinClusterQueue",
				utiltestingapi.MakeClusterQueue("cluster-queue").Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: "unknown"}).Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should allow to create clusterQueue with built-in resources with qualified names",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU).Obj()).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should forbid to create clusterQueue with invalid resource name",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("@cpu").Obj()).
					Obj(),
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("Should allow to create clusterQueue with valid cohort",
				utiltestingapi.MakeClusterQueue("cluster-queue").Cohort("prod").Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should forbid to create clusterQueue with invalid cohort",
				utiltestingapi.MakeClusterQueue("cluster-queue").Cohort("@prod").Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should allow to create clusterQueue with extended resources with qualified names",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("example.com/gpu").Obj()).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow to create clusterQueue with flavor with qualified names",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU).Obj()).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should forbid to create clusterQueue with flavor with unqualified names",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("invalid_name").Obj()).
					Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should forbid to create clusterQueue with flavor quota with negative value",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "-1").Obj()).
					Obj(),
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("Should allow to create clusterQueue with flavor quota with zero values",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "0").Obj()).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow to create clusterQueue with flavor quota with borrowingLimit 0",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "1", "0").Obj()).
					Cohort("cohort").
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should forbid to create clusterQueue with flavor quota with negative borrowingLimit",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "1", "-1").Obj()).
					Cohort("cohort").
					Obj(),
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("Should forbid to create clusterQueue with flavor quota with borrowingLimit and empty cohort",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "1", "1").Obj()).
					Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should allow to create clusterQueue with empty queueing strategy",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					QueueingStrategy("").
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should forbid to create clusterQueue with namespaceSelector with invalid labels",
				utiltestingapi.MakeClusterQueue("cluster-queue").NamespaceSelector(&metav1.LabelSelector{
					MatchLabels: map[string]string{"nospecialchars^=@": "bar"},
				}).Obj(),
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("Should forbid to create clusterQueue with namespaceSelector with invalid expressions",
				utiltestingapi.MakeClusterQueue("cluster-queue").NamespaceSelector(&metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      "key",
							Operator: "In",
						},
					},
				}).Obj(),
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("Should allow to create clusterQueue with multiple resource groups",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("alpha").
							Resource(corev1.ResourceCPU, "0").
							Resource(corev1.ResourceMemory, "0").
							Obj(),
						*utiltestingapi.MakeFlavorQuotas("beta").
							Resource(corev1.ResourceCPU, "0").
							Resource(corev1.ResourceMemory, "0").
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
				gomega.Succeed()),
			ginkgo.Entry("Should allow to create clusterQueue with a resource group with empty flavors",
				&kueue.ClusterQueue{
					Name: "cluster-queue",
					Spec: kueue.ClusterQueueSpec{
						ResourceGroups: []kueue.ResourceGroup{
							{
								CoveredResources: []corev1.ResourceName{corev1.ResourceCPU},
								Flavors:          []kueue.FlavorQuotas{},
							},
						},
					},
				},
				gomega.Succeed()),
			ginkgo.Entry("Should allow to create clusterQueue with no resource groups",
				&kueue.ClusterQueue{
					Name: "cluster-queue",
					Spec: kueue.ClusterQueueSpec{
						ResourceGroups: nil,
					},
				},
				gomega.Succeed()),
			ginkgo.Entry("Should forbid to create clusterQueue with resources in a flavor in different order",
				&kueue.ClusterQueue{
					Name: "cluster-queue",
					Spec: kueue.ClusterQueueSpec{
						ResourceGroups: []kueue.ResourceGroup{
							{
								CoveredResources: []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory},
								Flavors: []kueue.FlavorQuotas{
									*utiltestingapi.MakeFlavorQuotas("alpha").
										Resource(corev1.ResourceCPU, "0").
										Resource(corev1.ResourceMemory, "0").
										Obj(),
									*utiltestingapi.MakeFlavorQuotas("beta").
										Resource(corev1.ResourceMemory, "0").
										Resource(corev1.ResourceCPU, "0").
										Obj(),
								},
							},
						},
					},
				},
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("Should forbid to create clusterQueue missing resources in a flavor",
				&kueue.ClusterQueue{
					Name: "cluster-queue",
					Spec: kueue.ClusterQueueSpec{
						ResourceGroups: []kueue.ResourceGroup{
							{
								CoveredResources: []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory},
								Flavors: []kueue.FlavorQuotas{
									*utiltestingapi.MakeFlavorQuotas("alpha").
										Resource(corev1.ResourceCPU, "0").
										Obj(),
								},
							},
						},
					},
				},
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should forbid to create clusterQueue missing resources in a flavor",
				&kueue.ClusterQueue{
					Name: "cluster-queue",
					Spec: kueue.ClusterQueueSpec{
						ResourceGroups: []kueue.ResourceGroup{
							{
								CoveredResources: []corev1.ResourceName{corev1.ResourceCPU},
								Flavors: []kueue.FlavorQuotas{
									*utiltestingapi.MakeFlavorQuotas("alpha").
										Resource(corev1.ResourceCPU, "0").
										Resource(corev1.ResourceMemory, "0").
										Obj(),
								},
							},
						},
					},
				},
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should forbid to create clusterQueue missing resources in a flavor and mismatch",
				&kueue.ClusterQueue{
					Name: "cluster-queue",
					Spec: kueue.ClusterQueueSpec{
						ResourceGroups: []kueue.ResourceGroup{
							{
								CoveredResources: []corev1.ResourceName{"blah"},
								Flavors: []kueue.FlavorQuotas{
									*utiltestingapi.MakeFlavorQuotas("alpha").
										Resource(corev1.ResourceCPU, "0").
										Resource(corev1.ResourceMemory, "0").
										Obj(),
								},
							},
						},
					},
				},
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should forbid to create clusterQueue with resource in more than one resource group",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("alpha").
							Resource(corev1.ResourceCPU, "0").
							Resource(corev1.ResourceMemory, "0").
							Obj(),
					).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("beta").
							Resource(corev1.ResourceMemory, "0").
							Obj(),
					).
					Obj(),
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("Should forbid to create clusterQueue with flavor in more than one resource group",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("alpha").Resource(corev1.ResourceCPU).Obj(),
						*utiltestingapi.MakeFlavorQuotas("beta").Resource(corev1.ResourceCPU).Obj(),
					).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("beta").Resource(corev1.ResourceMemory).Obj(),
					).
					Obj(),
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("Should forbid to create clusterQueue missing with invalid preemption due to reclaimWithinCohort=Never, while borrowWithinCohort!=nil",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyNever,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
						},
					}).
					Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should allow to create clusterQueue with valid preemption with borrowWithinCohort",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
						BorrowWithinCohort: &kueue.BorrowWithinCohort{
							Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
							MaxPriorityThreshold: new(int32(10)),
						},
					}).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow to create clusterQueue with existing cluster queue created with older Kueue version that has a nil borrowWithinCohort field",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyNever,
					}).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow zero FairSharing weight",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					FairWeight(resource.MustParse("0")).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow fractional FairSharing weight",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					FairWeight(resource.MustParse("0.1")).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow small FairSharing weight",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					// 10^-3
					FairWeight(resource.MustParse("1m")).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow even smaller FairSharing weight",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					// 10^-6
					FairWeight(resource.MustParse("1u")).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow smallest FairSharing weight",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					// 2 * 10^-9
					FairWeight(resource.MustParse("2n")).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should forbid threshold FairSharing weight",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					// 10^-9
					FairWeight(resource.MustParse("1n")).
					Obj(),
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("Should forbid collapsed FairSharing weight",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					// 10^-10
					FairWeight(resource.MustParse("0.0000000001")).
					Obj(),
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("Should forbid negative FairSharing weight",
				utiltestingapi.MakeClusterQueue("cluster-queue").
					FairWeight(resource.MustParse("-1")).
					Obj(),
				utiltesting.BeForbiddenError()),
		)
	})

	ginkgo.When("Updating a ClusterQueue status", func() {
		var (
			cq *kueue.ClusterQueue
		)

		ginkgo.BeforeEach(func() {
			cq = utiltestingapi.MakeClusterQueue("cluster-queue").Obj()
			behavioral.MustCreate(ctx, k8sClient, cq)
		})

		ginkgo.AfterEach(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		})

		maxResourceGroups := make([]kueue.ResourceGroup, resourceGroupsMaxItems)
		for i := range maxResourceGroups {
			maxResourceGroups[i] = utiltestingapi.ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(fmt.Sprintf("f%d", i)).
					Resource(corev1.ResourceCPU, "1").
					Obj(),
			)
		}
		moreThanMaxResourceGroups := append(slices.Clone(maxResourceGroups), utiltestingapi.ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas(fmt.Sprintf("f%d", resourceGroupsMaxItems)).
				Resource(corev1.ResourceCPU, "1").
				Obj(),
		))

		ginkgo.DescribeTable("Validate status.effectiveQuotas on update",
			func(eq *kueue.EffectiveQuotaStatus, matcher types.GomegaMatcher) {
				gomega.Eventually(func(g gomega.Gomega) {
					var updateCQ kueue.ClusterQueue
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updateCQ)).Should(gomega.Succeed())
					updateCQ.Status.EffectiveQuotas = eq
					err := k8sClient.Status().Update(ctx, &updateCQ)
					g.Expect(err).Should(matcher)
					if err == nil {
						var gotCQ kueue.ClusterQueue
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &gotCQ)).Should(gomega.Succeed())
						g.Expect(gotCQ.Status.EffectiveQuotas).Should(gomega.BeComparableTo(eq))
					}
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			},
			ginkgo.Entry("Should allow valid effectiveQuotas with empty resourceGroups",
				utiltestingapi.MakeEffectiveQuotaStatus().Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow effectiveQuotas with expirationTime",
				utiltestingapi.MakeEffectiveQuotaStatus().
					ExpirationTime(time.Now().Add(time.Hour).Truncate(time.Second)).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow valid effectiveQuotas with resourceGroups",
				utiltestingapi.MakeEffectiveQuotaStatus().
					ResourceGroups(
						utiltestingapi.ResourceGroup(
							*utiltestingapi.MakeFlavorQuotas("f0").
								Resource(corev1.ResourceCPU, "1").
								Obj(),
						),
					).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow effectiveQuotas with maximum number of resourceGroups",
				utiltestingapi.MakeEffectiveQuotaStatus().
					ResourceGroups(maxResourceGroups...).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should reject effectiveQuotas with invalid orchestratorRef apiGroup pattern",
				utiltestingapi.MakeEffectiveQuotaStatus().
					APIGroup("Invalid_APIGroup").
					Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should reject effectiveQuotas with invalid orchestratorRef kind pattern",
				utiltestingapi.MakeEffectiveQuotaStatus().
					Kind("123Invalid").
					Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should reject effectiveQuotas with invalid orchestratorRef name pattern",
				utiltestingapi.MakeEffectiveQuotaStatus().
					Name("@invalid").
					Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should reject effectiveQuotas with more than 16 resourceGroups",
				utiltestingapi.MakeEffectiveQuotaStatus().
					ResourceGroups(moreThanMaxResourceGroups...).
					Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should reject flavor resource count mismatch with coveredResources",
				utiltestingapi.MakeEffectiveQuotaStatus().
					ResourceGroups(
						kueue.ResourceGroup{
							CoveredResources: []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory},
							Flavors: []kueue.FlavorQuotas{
								*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU).Obj(),
							},
						},
					).
					Obj(),
				utiltesting.BeInvalidError()),
		)
	})
})
