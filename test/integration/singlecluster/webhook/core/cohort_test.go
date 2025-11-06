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

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Cohort Webhook", ginkgo.Ordered, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, func(ctx context.Context, mgr manager.Manager) {
			managerSetup(ctx, mgr)
		})
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})
	ginkgo.When("Creating a Cohort", func() {
		ginkgo.DescribeTable("Validate Cohort on creation", func(cohort *kueue.Cohort, matcher types.GomegaMatcher) {
			err := k8sClient.Create(ctx, cohort)
			if err == nil {
				defer func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, cohort, true)
				}()
			}
			gomega.Expect(err).Should(matcher)
		},
			ginkgo.Entry("Should disallow empty name",
				utiltestingapi.MakeCohort("").Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should allow default Cohort",
				utiltestingapi.MakeCohort("cohort").Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow valid parent name",
				utiltestingapi.MakeCohort("cohort").Parent("prod").Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should reject invalid parent name",
				utiltestingapi.MakeCohort("cohort").Parent("@prod").Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("ResourceGroup should have at least one flavor",
				utiltestingapi.MakeCohort("cohort").ResourceGroup().Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("FlavorQuota should have at least one resource",
				utiltestingapi.MakeCohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("foo").Obj()).
					Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should reject invalid flavor name",
				utiltestingapi.MakeCohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("@x86").Resource(corev1.ResourceCPU, "5").Obj()).
					Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should allow valid resource name",
				utiltestingapi.MakeCohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("x86").Resource("@cpu", "5").Obj()).
					Obj(),
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("Should reject too many flavors in resource group",
				func() *kueue.Cohort {
					var flavors []kueue.FlavorQuotas
					for i := range 65 {
						flavors = append(flavors,
							*utiltestingapi.MakeFlavorQuotas(fmt.Sprintf("f%d", i)).
								Resource(corev1.ResourceCPU).
								Obj(),
						)
					}
					return utiltestingapi.MakeCohort("cohort").
						ResourceGroup(flavors...).
						Obj()
				}(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should reject too many resources in resource group",
				func() *kueue.Cohort {
					fq := utiltestingapi.MakeFlavorQuotas("flavor")
					for i := range 65 {
						fq = fq.Resource(corev1.ResourceName(fmt.Sprintf("cpu%d", i)))
					}
					return utiltestingapi.MakeCohort("cohort").
						ResourceGroup(*fq.Obj()).
						Obj()
				}(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should allow resource with valid name",
				utiltestingapi.MakeCohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU).Obj()).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should reject resource with invalid name",
				utiltestingapi.MakeCohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("@cpu").Obj()).
					Obj(),
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("Should allow extended resources with valid name",
				utiltestingapi.MakeCohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource("example.com/gpu").Obj()).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow flavor with valid name",
				utiltestingapi.MakeCohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU).Obj()).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should reject flavor with invalid name",
				utiltestingapi.MakeCohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("x_86").Resource(corev1.ResourceCPU).Obj()).
					Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("Should reject negative nominal quota",
				utiltestingapi.MakeCohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "-1").Obj()).
					Obj(),
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("Should reject negative borrowing limit",
				utiltestingapi.MakeCohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "1", "-1").Obj()).
					Obj(),
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("Should reject negative lending limit",
				utiltestingapi.MakeCohort("cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "1", "", "-1").Obj()).
					Obj(),
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("Should reject borrowingLimit when no parent",
				utiltestingapi.MakeCohort("cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "1", "1").Obj()).
					Obj(),
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("Should allow borrowingLimit 0 when parent exists",
				utiltestingapi.MakeCohort("cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "1", "0").Obj()).
					Parent("parent").
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow borrowingLimit when parent exists",
				utiltestingapi.MakeCohort("cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "1", "1").Obj()).
					Parent("parent").
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should reject lendingLimit when no parent",
				utiltestingapi.MakeCohort("cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("x86").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("1").LendingLimit("1").Append().
							Obj(),
					).
					Obj(),
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("Should allow lendingLimit when parent exists",
				utiltestingapi.MakeCohort("cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("x86").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("1").LendingLimit("1").Append().
							Obj(),
					).
					Parent("parent").
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow lendingLimit 0 when parent exists",
				utiltestingapi.MakeCohort("cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("x86").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").LendingLimit("0").Append().
							Obj(),
					).
					Parent("parent").
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow lending limit to exceed nominal quota",
				utiltestingapi.MakeCohort("cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("x86").
							ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("3").LendingLimit("5").Append().
							Obj(),
					).
					Parent("parent").
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow multiple resource groups",
				utiltestingapi.MakeCohort("cohort").
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
			ginkgo.Entry("Should reject resources in a flavor in different order",
				&kueue.Cohort{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cohort",
					},
					Spec: kueue.CohortSpec{
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
			ginkgo.Entry("Should reject missing resources in a flavor",
				&kueue.Cohort{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cohort",
					},
					Spec: kueue.CohortSpec{
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
			ginkgo.Entry("Should reject resource not defined in resource group",
				&kueue.Cohort{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cohort",
					},
					Spec: kueue.CohortSpec{
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
			ginkgo.Entry("Should reject resource in more than one resource group",
				utiltestingapi.MakeCohort("cohort").
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
			ginkgo.Entry("Should reject flavor in more than one resource group",
				utiltestingapi.MakeCohort("cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("alpha").Resource(corev1.ResourceCPU).Obj(),
						*utiltestingapi.MakeFlavorQuotas("beta").Resource(corev1.ResourceCPU).Obj(),
					).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("beta").Resource(corev1.ResourceMemory).Obj(),
					).
					Obj(),
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("Should allow FairSharing weight",
				utiltestingapi.MakeCohort("cohort").FairWeight(resource.MustParse("1")).Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow zero FairSharing weight",
				utiltestingapi.MakeCohort("cohort").FairWeight(resource.MustParse("0")).Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should forbid negative FairSharing weight",
				utiltestingapi.MakeCohort("cohort").FairWeight(resource.MustParse("-1")).Obj(),
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("Should allow fractional FairSharing weight",
				utiltestingapi.MakeCohort("cohort").FairWeight(resource.MustParse("0.5")).Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow small FairSharing weight",
				// 10^-3
				utiltestingapi.MakeCohort("cohort").FairWeight(resource.MustParse("1m")).Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow even smaller FairSharing weight",
				// 10^-6
				utiltestingapi.MakeCohort("cohort").FairWeight(resource.MustParse("1u")).Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow smallest FairSharing weight",
				// 2 * 10^-9
				utiltestingapi.MakeCohort("cohort").FairWeight(resource.MustParse("2n")).Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should forbid threshold FairSharing weight",
				// 10^-9
				utiltestingapi.MakeCohort("cohort").FairWeight(resource.MustParse("1n")).Obj(),
				utiltesting.BeForbiddenError()),
			ginkgo.Entry("Should forbid collapsed FairSharing weight",
				// 10^-10
				utiltestingapi.MakeCohort("cohort").FairWeight(resource.MustParse("0.0000000001")).Obj(),
				utiltesting.BeForbiddenError()),
		)
	})

	ginkgo.When("Updating a Cohort", func() {
		var (
			cohort *kueue.Cohort
		)

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cohort, true)
		})

		ginkgo.It("Should update parent", func() {
			cohort = utiltestingapi.MakeCohort("cohort").Obj()
			util.MustCreate(ctx, k8sClient, cohort)

			gomega.Eventually(func(g gomega.Gomega) {
				createCohort := &kueue.Cohort{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cohort), createCohort)).Should(gomega.Succeed())
				createCohort.Spec.ParentName = "cohort2"
				g.Expect(k8sClient.Update(ctx, createCohort)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
		ginkgo.It("Should reject invalid parent", func() {
			cohort = utiltestingapi.MakeCohort("cohort").Obj()
			util.MustCreate(ctx, k8sClient, cohort)

			gomega.Eventually(func(g gomega.Gomega) {
				createCohort := &kueue.Cohort{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cohort), createCohort)).Should(gomega.Succeed())
				createCohort.Spec.ParentName = "@cohort2"
				gomega.Expect(k8sClient.Update(ctx, createCohort)).ShouldNot(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
		ginkgo.It("Should reject negative borrowing limit", func() {
			cohort = utiltestingapi.MakeCohort("cohort").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "-1").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cohort)).ShouldNot(gomega.Succeed())
		})
	})
})
