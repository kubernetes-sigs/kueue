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
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

const (
	resourcesMaxItems = 16
	flavorsMaxItems   = 16
)

var _ = ginkgo.Describe("ClusterQueue Webhook", ginkgo.Ordered, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, func(ctx context.Context, mgr manager.Manager) {
			managerSetup(ctx, mgr, config.MultiKueueDispatcherModeAllAtOnce)
		})
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})
	var ns *corev1.Namespace
	defaultFlavorFungibility := &kueue.FlavorFungibility{
		WhenCanBorrow:  kueue.Borrow,
		WhenCanPreempt: kueue.TryNextFlavor,
	}

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Creating a ClusterQueue", func() {
		ginkgo.DescribeTable("Defaulting on creation", func(cq, wantCQ kueue.ClusterQueue) {
			util.MustCreate(ctx, k8sClient, &cq)
			defer func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, &cq, true)
			}()
			gomega.Expect(cq).To(gomega.BeComparableTo(wantCQ,
				cmpopts.IgnoreTypes(kueue.ClusterQueueStatus{}),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "UID", "ResourceVersion", "Generation", "CreationTimestamp", "ManagedFields")))
		},
			ginkgo.Entry("All defaults",
				kueue.ClusterQueue{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
					},
				},
				kueue.ClusterQueue{
					ObjectMeta: metav1.ObjectMeta{
						Name:       "foo",
						Finalizers: []string{kueue.ResourceInUseFinalizerName},
					},
					Spec: kueue.ClusterQueueSpec{
						QueueingStrategy:  kueue.BestEffortFIFO,
						StopPolicy:        ptr.To(kueue.None),
						FlavorFungibility: defaultFlavorFungibility,
						Preemption: &kueue.ClusterQueuePreemption{
							WithinClusterQueue:  kueue.PreemptionPolicyNever,
							ReclaimWithinCohort: kueue.PreemptionPolicyNever,
							BorrowWithinCohort: &kueue.BorrowWithinCohort{
								Policy: kueue.BorrowWithinCohortPolicyNever,
							},
						},
					},
				},
			),
			ginkgo.Entry("Preemption overridden",
				kueue.ClusterQueue{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
					},
					Spec: kueue.ClusterQueueSpec{
						FlavorFungibility: defaultFlavorFungibility,
						Preemption: &kueue.ClusterQueuePreemption{
							WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
							ReclaimWithinCohort: kueue.PreemptionPolicyAny,
							BorrowWithinCohort: &kueue.BorrowWithinCohort{
								Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
								MaxPriorityThreshold: ptr.To[int32](100),
							},
						},
					},
				},
				kueue.ClusterQueue{
					ObjectMeta: metav1.ObjectMeta{
						Name:       "foo",
						Finalizers: []string{kueue.ResourceInUseFinalizerName},
					},
					Spec: kueue.ClusterQueueSpec{
						QueueingStrategy:  kueue.BestEffortFIFO,
						StopPolicy:        ptr.To(kueue.None),
						FlavorFungibility: defaultFlavorFungibility,
						Preemption: &kueue.ClusterQueuePreemption{
							WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
							ReclaimWithinCohort: kueue.PreemptionPolicyAny,
							BorrowWithinCohort: &kueue.BorrowWithinCohort{
								Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
								MaxPriorityThreshold: ptr.To[int32](100),
							},
						},
					},
				},
			),
			ginkgo.Entry("Default fair sharing",
				kueue.ClusterQueue{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
					},
					Spec: kueue.ClusterQueueSpec{
						FairSharing: &kueue.FairSharing{},
					},
				},
				kueue.ClusterQueue{
					ObjectMeta: metav1.ObjectMeta{
						Name:       "foo",
						Finalizers: []string{kueue.ResourceInUseFinalizerName},
					},
					Spec: kueue.ClusterQueueSpec{
						QueueingStrategy:  kueue.BestEffortFIFO,
						StopPolicy:        ptr.To(kueue.None),
						FlavorFungibility: defaultFlavorFungibility,
						Preemption: &kueue.ClusterQueuePreemption{
							ReclaimWithinCohort: kueue.PreemptionPolicyNever,
							WithinClusterQueue:  kueue.PreemptionPolicyNever,
							BorrowWithinCohort: &kueue.BorrowWithinCohort{
								Policy: kueue.BorrowWithinCohortPolicyNever,
							},
						},
						FairSharing: &kueue.FairSharing{
							Weight: ptr.To(resource.MustParse("1")),
						},
					},
				},
			),
		)

		ginkgo.It("Should have qualified flavor names when updating", func() {
			ginkgo.By("Creating a new clusterQueue")
			cq := testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(*testing.MakeFlavorQuotas("x86").Resource(corev1.ResourceMemory).Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, cq)

			defer func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			}()

			gomega.Eventually(func(g gomega.Gomega) {
				var updateCQ kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updateCQ)).Should(gomega.Succeed())
				updateCQ.Spec.ResourceGroups[0].Flavors[0].Name = "@x86"
				g.Expect(k8sClient.Update(ctx, &updateCQ)).Should(testing.BeInvalidError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should allow to update queueingStrategy with different value", func() {
			ginkgo.By("Creating a new clusterQueue")
			cq := testing.MakeClusterQueue("cluster-queue").
				QueueingStrategy(kueue.StrictFIFO).
				ResourceGroup(*testing.MakeFlavorQuotas("x86").Resource(corev1.ResourceMemory).Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, cq)

			defer func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			}()

			gomega.Eventually(func(g gomega.Gomega) {
				var updateCQ kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updateCQ)).Should(gomega.Succeed())
				updateCQ.Spec.QueueingStrategy = kueue.BestEffortFIFO
				g.Expect(k8sClient.Update(ctx, &updateCQ)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.DescribeTable("Validate ClusterQueue on creation", func(cq *kueue.ClusterQueue, matcher types.GomegaMatcher) {
			err := k8sClient.Create(ctx, cq)
			if err == nil {
				defer func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
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
				testing.MakeClusterQueue("cluster-queue").
					ResourceGroup(*testing.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "2", "-1").Obj()).
					Cohort("cohort").
					Obj(),
				testing.BeForbiddenError()),
			ginkgo.Entry("Should have non-negative quota value",
				testing.MakeClusterQueue("cluster-queue").
					ResourceGroup(*testing.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "-1").Obj()).
					Obj(),
				testing.BeForbiddenError()),
			ginkgo.Entry("Should have at least one flavor",
				testing.MakeClusterQueue("cluster-queue").ResourceGroup().Obj(),
				testing.BeInvalidError()),
			ginkgo.Entry("Should have at least one resource",
				testing.MakeClusterQueue("cluster-queue").
					ResourceGroup(*testing.MakeFlavorQuotas("foo").Obj()).
					Obj(),
				testing.BeInvalidError()),
			ginkgo.Entry("Should have qualified flavor name",
				testing.MakeClusterQueue("cluster-queue").
					ResourceGroup(*testing.MakeFlavorQuotas("invalid_name").Resource(corev1.ResourceCPU, "5").Obj()).
					Obj(),
				testing.BeInvalidError()),
			ginkgo.Entry("Should have qualified resource name",
				testing.MakeClusterQueue("cluster-queue").
					ResourceGroup(*testing.MakeFlavorQuotas("x86").Resource("@cpu", "5").Obj()).
					Obj(),
				testing.BeForbiddenError()),
			ginkgo.Entry("Should have valid resources quantity",
				func() *kueue.ClusterQueue {
					flvQuotas := testing.MakeFlavorQuotas("flavor")
					for i := range resourcesMaxItems + 1 {
						flvQuotas = flvQuotas.Resource(corev1.ResourceName(fmt.Sprintf("r%d", i)))
					}
					return testing.MakeClusterQueue("cluster-queue").ResourceGroup(*flvQuotas.Obj()).Obj()
				}(),
				testing.BeInvalidError()),
			ginkgo.Entry("Should have valid flavors quantity",
				func() *kueue.ClusterQueue {
					flavors := make([]kueue.FlavorQuotas, flavorsMaxItems+1)
					for i := range flavors {
						flavors[i] = *testing.MakeFlavorQuotas(fmt.Sprintf("f%d", i)).
							Resource(corev1.ResourceCPU).
							Obj()
					}
					return testing.MakeClusterQueue("cluster-queue").ResourceGroup(flavors...).Obj()
				}(),
				testing.BeInvalidError()),
			ginkgo.Entry("Should forbid clusterQueue creation with unqualified labelSelector",
				testing.MakeClusterQueue("cluster-queue").NamespaceSelector(&metav1.LabelSelector{
					MatchLabels: map[string]string{"nospecialchars^=@": "bar"},
				}).Obj(),
				testing.BeForbiddenError()),
			ginkgo.Entry("Should forbid to create clusterQueue with unknown clusterQueueingStrategy",
				testing.MakeClusterQueue("cluster-queue").QueueingStrategy(kueue.QueueingStrategy("unknown")).Obj(),
				testing.BeInvalidError()),
			ginkgo.Entry("Should allow to create clusterQueue with empty clusterQueueingStrategy",
				testing.MakeClusterQueue("cluster-queue").QueueingStrategy(kueue.QueueingStrategy("")).Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow to create clusterQueue with empty preemption",
				testing.MakeClusterQueue("cluster-queue").Preemption(kueue.ClusterQueuePreemption{}).Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow to create clusterQueue with preemption policies",
				testing.MakeClusterQueue("cluster-queue").Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				}).FlavorFungibility(*defaultFlavorFungibility).Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should forbid to create clusterQueue with unknown preemption.withinCohort",
				testing.MakeClusterQueue("cluster-queue").Preemption(kueue.ClusterQueuePreemption{ReclaimWithinCohort: "unknown"}).Obj(),
				testing.BeInvalidError()),
			ginkgo.Entry("Should forbid to create clusterQueue with unknown preemption.withinClusterQueue",
				testing.MakeClusterQueue("cluster-queue").Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: "unknown"}).Obj(),
				testing.BeInvalidError()),
			ginkgo.Entry("Should allow to create clusterQueue with built-in resources with qualified names",
				testing.MakeClusterQueue("cluster-queue").
					ResourceGroup(*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU).Obj()).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should forbid to create clusterQueue with invalid resource name",
				testing.MakeClusterQueue("cluster-queue").
					ResourceGroup(*testing.MakeFlavorQuotas("default").Resource("@cpu").Obj()).
					Obj(),
				testing.BeForbiddenError()),
			ginkgo.Entry("Should allow to create clusterQueue with valid cohort",
				testing.MakeClusterQueue("cluster-queue").Cohort("prod").Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should forbid to create clusterQueue with invalid cohort",
				testing.MakeClusterQueue("cluster-queue").Cohort("@prod").Obj(),
				testing.BeInvalidError()),
			ginkgo.Entry("Should allow to create clusterQueue with extended resources with qualified names",
				testing.MakeClusterQueue("cluster-queue").
					ResourceGroup(*testing.MakeFlavorQuotas("default").Resource("example.com/gpu").Obj()).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow to create clusterQueue with flavor with qualified names",
				testing.MakeClusterQueue("cluster-queue").
					ResourceGroup(*testing.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU).Obj()).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should forbid to create clusterQueue with flavor with unqualified names",
				testing.MakeClusterQueue("cluster-queue").
					ResourceGroup(*testing.MakeFlavorQuotas("invalid_name").Obj()).
					Obj(),
				testing.BeInvalidError()),
			ginkgo.Entry("Should forbid to create clusterQueue with flavor quota with negative value",
				testing.MakeClusterQueue("cluster-queue").
					ResourceGroup(
						*testing.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "-1").Obj()).
					Obj(),
				testing.BeForbiddenError()),
			ginkgo.Entry("Should allow to create clusterQueue with flavor quota with zero values",
				testing.MakeClusterQueue("cluster-queue").
					ResourceGroup(
						*testing.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "0").Obj()).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow to create clusterQueue with flavor quota with borrowingLimit 0",
				testing.MakeClusterQueue("cluster-queue").
					ResourceGroup(
						*testing.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "1", "0").Obj()).
					Cohort("cohort").
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should forbid to create clusterQueue with flavor quota with negative borrowingLimit",
				testing.MakeClusterQueue("cluster-queue").
					ResourceGroup(
						*testing.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "1", "-1").Obj()).
					Cohort("cohort").
					Obj(),
				testing.BeForbiddenError()),
			ginkgo.Entry("Should forbid to create clusterQueue with flavor quota with borrowingLimit and empty cohort",
				testing.MakeClusterQueue("cluster-queue").
					ResourceGroup(
						*testing.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "1", "1").Obj()).
					Obj(),
				testing.BeInvalidError()),
			ginkgo.Entry("Should allow to create clusterQueue with empty queueing strategy",
				testing.MakeClusterQueue("cluster-queue").
					QueueingStrategy("").
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should forbid to create clusterQueue with namespaceSelector with invalid labels",
				testing.MakeClusterQueue("cluster-queue").NamespaceSelector(&metav1.LabelSelector{
					MatchLabels: map[string]string{"nospecialchars^=@": "bar"},
				}).Obj(),
				testing.BeForbiddenError()),
			ginkgo.Entry("Should forbid to create clusterQueue with namespaceSelector with invalid expressions",
				testing.MakeClusterQueue("cluster-queue").NamespaceSelector(&metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      "key",
							Operator: "In",
						},
					},
				}).Obj(),
				testing.BeForbiddenError()),
			ginkgo.Entry("Should allow to create clusterQueue with multiple resource groups",
				testing.MakeClusterQueue("cluster-queue").
					ResourceGroup(
						*testing.MakeFlavorQuotas("alpha").
							Resource(corev1.ResourceCPU, "0").
							Resource(corev1.ResourceMemory, "0").
							Obj(),
						*testing.MakeFlavorQuotas("beta").
							Resource(corev1.ResourceCPU, "0").
							Resource(corev1.ResourceMemory, "0").
							Obj(),
					).
					ResourceGroup(
						*testing.MakeFlavorQuotas("gamma").
							Resource("example.com/gpu", "0").
							Obj(),
						*testing.MakeFlavorQuotas("omega").
							Resource("example.com/gpu", "0").
							Obj(),
					).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should forbid to create clusterQueue with resources in a flavor in different order",
				&kueue.ClusterQueue{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cluster-queue",
					},
					Spec: kueue.ClusterQueueSpec{
						ResourceGroups: []kueue.ResourceGroup{
							{
								CoveredResources: []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory},
								Flavors: []kueue.FlavorQuotas{
									*testing.MakeFlavorQuotas("alpha").
										Resource(corev1.ResourceCPU, "0").
										Resource(corev1.ResourceMemory, "0").
										Obj(),
									*testing.MakeFlavorQuotas("beta").
										Resource(corev1.ResourceMemory, "0").
										Resource(corev1.ResourceCPU, "0").
										Obj(),
								},
							},
						},
					},
				},
				testing.BeForbiddenError()),
			ginkgo.Entry("Should forbid to create clusterQueue missing resources in a flavor",
				&kueue.ClusterQueue{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cluster-queue",
					},
					Spec: kueue.ClusterQueueSpec{
						ResourceGroups: []kueue.ResourceGroup{
							{
								CoveredResources: []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory},
								Flavors: []kueue.FlavorQuotas{
									*testing.MakeFlavorQuotas("alpha").
										Resource(corev1.ResourceCPU, "0").
										Obj(),
								},
							},
						},
					},
				},
				testing.BeInvalidError()),
			ginkgo.Entry("Should forbid to create clusterQueue missing resources in a flavor",
				&kueue.ClusterQueue{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cluster-queue",
					},
					Spec: kueue.ClusterQueueSpec{
						ResourceGroups: []kueue.ResourceGroup{
							{
								CoveredResources: []corev1.ResourceName{corev1.ResourceCPU},
								Flavors: []kueue.FlavorQuotas{
									*testing.MakeFlavorQuotas("alpha").
										Resource(corev1.ResourceCPU, "0").
										Resource(corev1.ResourceMemory, "0").
										Obj(),
								},
							},
						},
					},
				},
				testing.BeInvalidError()),
			ginkgo.Entry("Should forbid to create clusterQueue missing resources in a flavor and mismatch",
				&kueue.ClusterQueue{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cluster-queue",
					},
					Spec: kueue.ClusterQueueSpec{
						ResourceGroups: []kueue.ResourceGroup{
							{
								CoveredResources: []corev1.ResourceName{"blah"},
								Flavors: []kueue.FlavorQuotas{
									*testing.MakeFlavorQuotas("alpha").
										Resource(corev1.ResourceCPU, "0").
										Resource(corev1.ResourceMemory, "0").
										Obj(),
								},
							},
						},
					},
				},
				testing.BeInvalidError()),
			ginkgo.Entry("Should forbid to create clusterQueue with resource in more than one resource group",
				testing.MakeClusterQueue("cluster-queue").
					ResourceGroup(
						*testing.MakeFlavorQuotas("alpha").
							Resource(corev1.ResourceCPU, "0").
							Resource(corev1.ResourceMemory, "0").
							Obj(),
					).
					ResourceGroup(
						*testing.MakeFlavorQuotas("beta").
							Resource(corev1.ResourceMemory, "0").
							Obj(),
					).
					Obj(),
				testing.BeForbiddenError()),
			ginkgo.Entry("Should forbid to create clusterQueue with flavor in more than one resource group",
				testing.MakeClusterQueue("cluster-queue").
					ResourceGroup(
						*testing.MakeFlavorQuotas("alpha").Resource(corev1.ResourceCPU).Obj(),
						*testing.MakeFlavorQuotas("beta").Resource(corev1.ResourceCPU).Obj(),
					).
					ResourceGroup(
						*testing.MakeFlavorQuotas("beta").Resource(corev1.ResourceMemory).Obj(),
					).
					Obj(),
				testing.BeForbiddenError()),
			ginkgo.Entry("Should forbid to create clusterQueue missing with invalid preemption due to reclaimWithinCohort=Never, while borrowWithinCohort!=nil",
				&kueue.ClusterQueue{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cluster-queue",
					},
					Spec: kueue.ClusterQueueSpec{
						Preemption: &kueue.ClusterQueuePreemption{
							ReclaimWithinCohort: kueue.PreemptionPolicyNever,
							BorrowWithinCohort: &kueue.BorrowWithinCohort{
								Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
							},
						},
					},
				},
				testing.BeInvalidError()),
			ginkgo.Entry("Should allow to create clusterQueue with valid preemption with borrowWithinCohort",
				&kueue.ClusterQueue{
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
				gomega.Succeed()),
			ginkgo.Entry("Should allow to create clusterQueue with existing cluster queue created with older Kueue version that has a nil borrowWithinCohort field",
				&kueue.ClusterQueue{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cluster-queue",
					},
					Spec: kueue.ClusterQueueSpec{
						Preemption: &kueue.ClusterQueuePreemption{
							ReclaimWithinCohort: kueue.PreemptionPolicyNever,
						},
					},
				},
				gomega.Succeed()),
			ginkgo.Entry("Should allow zero fair share weight",
				&kueue.ClusterQueue{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cluster-queue",
					},
					Spec: kueue.ClusterQueueSpec{
						FairSharing: &kueue.FairSharing{
							Weight: ptr.To(resource.MustParse("0")),
						},
					},
				},
				gomega.Succeed()),
			ginkgo.Entry("Should allow fractional weight",
				&kueue.ClusterQueue{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cluster-queue",
					},
					Spec: kueue.ClusterQueueSpec{
						FairSharing: &kueue.FairSharing{
							Weight: ptr.To(resource.MustParse("0.1")),
						},
					},
				},
				gomega.Succeed()),
			ginkgo.Entry("Should forbid negative weight",
				&kueue.ClusterQueue{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cluster-queue",
					},
					Spec: kueue.ClusterQueueSpec{
						FairSharing: &kueue.FairSharing{
							Weight: ptr.To(resource.MustParse("-1")),
						},
					},
				},
				testing.BeForbiddenError()),
		)
	})
})
