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

package core

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Cohort Webhook", func() {
	ginkgo.When("Creating a Cohort", func() {
		ginkgo.DescribeTable("Validate Cohort on creation", func(cohort *kueuealpha.Cohort, matcher types.GomegaMatcher) {
			err := k8sClient.Create(ctx, cohort)
			if err == nil {
				defer func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, cohort, true)
				}()
			}
			gomega.Expect(err).Should(matcher)
		},
			ginkgo.Entry("Should disallow empty name",
				testing.MakeCohort("").Obj(),
				testing.BeInvalidError()),
			ginkgo.Entry("Should allow default Cohort",
				testing.MakeCohort("cohort").Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow valid parent name",
				testing.MakeCohort("cohort").Parent("prod").Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should reject invalid parent name",
				testing.MakeCohort("cohort").Parent("@prod").Obj(),
				testing.BeInvalidError()),
			ginkgo.Entry("ResourceGroup should have at least one flavor",
				testing.MakeCohort("cohort").ResourceGroup().Obj(),
				testing.BeInvalidError()),
			ginkgo.Entry("FlavorQuota should have at least one resource",
				testing.MakeCohort("cohort").
					ResourceGroup(*testing.MakeFlavorQuotas("foo").Obj()).
					Obj(),
				testing.BeInvalidError()),
			ginkgo.Entry("Should reject invalid flavor name",
				testing.MakeCohort("cohort").
					ResourceGroup(*testing.MakeFlavorQuotas("@x86").Resource("cpu", "5").Obj()).
					Obj(),
				testing.BeInvalidError()),
			ginkgo.Entry("Should allow valid resource name",
				testing.MakeCohort("cohort").
					ResourceGroup(*testing.MakeFlavorQuotas("x86").Resource("@cpu", "5").Obj()).
					Obj(),
				testing.BeForbiddenError()),
			ginkgo.Entry("Should reject too many flavors in resource group",
				testing.MakeCohort("cohort").ResourceGroup(
					testing.MakeFlavorQuotas("f0").Resource("cpu").FlavorQuotas,
					testing.MakeFlavorQuotas("f1").Resource("cpu").FlavorQuotas,
					testing.MakeFlavorQuotas("f2").Resource("cpu").FlavorQuotas,
					testing.MakeFlavorQuotas("f3").Resource("cpu").FlavorQuotas,
					testing.MakeFlavorQuotas("f4").Resource("cpu").FlavorQuotas,
					testing.MakeFlavorQuotas("f5").Resource("cpu").FlavorQuotas,
					testing.MakeFlavorQuotas("f6").Resource("cpu").FlavorQuotas,
					testing.MakeFlavorQuotas("f7").Resource("cpu").FlavorQuotas,
					testing.MakeFlavorQuotas("f8").Resource("cpu").FlavorQuotas,
					testing.MakeFlavorQuotas("f9").Resource("cpu").FlavorQuotas,
					testing.MakeFlavorQuotas("f10").Resource("cpu").FlavorQuotas,
					testing.MakeFlavorQuotas("f11").Resource("cpu").FlavorQuotas,
					testing.MakeFlavorQuotas("f12").Resource("cpu").FlavorQuotas,
					testing.MakeFlavorQuotas("f13").Resource("cpu").FlavorQuotas,
					testing.MakeFlavorQuotas("f14").Resource("cpu").FlavorQuotas,
					testing.MakeFlavorQuotas("f15").Resource("cpu").FlavorQuotas,
					testing.MakeFlavorQuotas("f16").Resource("cpu").FlavorQuotas).Obj(),
				testing.BeInvalidError()),
			ginkgo.Entry("Should reject too many resources in resource group",
				testing.MakeCohort("cohort").ResourceGroup(
					testing.MakeFlavorQuotas("flavor").
						Resource("cpu0").
						Resource("cpu1").
						Resource("cpu2").
						Resource("cpu3").
						Resource("cpu4").
						Resource("cpu5").
						Resource("cpu6").
						Resource("cpu7").
						Resource("cpu8").
						Resource("cpu9").
						Resource("cpu10").
						Resource("cpu11").
						Resource("cpu12").
						Resource("cpu13").
						Resource("cpu14").
						Resource("cpu15").
						Resource("cpu16").FlavorQuotas).Obj(),
				testing.BeInvalidError()),
			ginkgo.Entry("Should allow resource with valid name",
				testing.MakeCohort("cohort").
					ResourceGroup(*testing.MakeFlavorQuotas("default").Resource("cpu").Obj()).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should reject resource with invalid name",
				testing.MakeCohort("cohort").
					ResourceGroup(*testing.MakeFlavorQuotas("default").Resource("@cpu").Obj()).
					Obj(),
				testing.BeForbiddenError()),
			ginkgo.Entry("Should allow extended resources with valid name",
				testing.MakeCohort("cohort").
					ResourceGroup(*testing.MakeFlavorQuotas("default").Resource("example.com/gpu").Obj()).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow flavor with valid name",
				testing.MakeCohort("cohort").
					ResourceGroup(*testing.MakeFlavorQuotas("x86").Resource("cpu").Obj()).
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should reject flavor with invalid name",
				testing.MakeCohort("cohort").
					ResourceGroup(*testing.MakeFlavorQuotas("x_86").Resource("cpu").Obj()).
					Obj(),
				testing.BeInvalidError()),
			ginkgo.Entry("Should reject negative nominal quota",
				testing.MakeCohort("cohort").
					ResourceGroup(*testing.MakeFlavorQuotas("x86").Resource("cpu", "-1").Obj()).
					Obj(),
				testing.BeForbiddenError()),
			ginkgo.Entry("Should reject negative borrowing limit",
				testing.MakeCohort("cohort").
					ResourceGroup(*testing.MakeFlavorQuotas("x86").Resource("cpu", "1", "-1").Obj()).
					Obj(),
				testing.BeForbiddenError()),
			ginkgo.Entry("Should reject negative lending limit",
				testing.MakeCohort("cohort").
					ResourceGroup(*testing.MakeFlavorQuotas("x86").Resource("cpu", "1", "", "-1").Obj()).
					Obj(),
				testing.BeForbiddenError()),
			ginkgo.Entry("Should reject borrowingLimit when no parent",
				testing.MakeCohort("cohort").
					ResourceGroup(
						*testing.MakeFlavorQuotas("x86").Resource("cpu", "1", "1").Obj()).
					Obj(),
				testing.BeForbiddenError()),
			ginkgo.Entry("Should allow borrowingLimit 0 when parent exists",
				testing.MakeCohort("cohort").
					ResourceGroup(
						*testing.MakeFlavorQuotas("x86").Resource("cpu", "1", "0").Obj()).
					Parent("parent").
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow borrowingLimit when parent exists",
				testing.MakeCohort("cohort").
					ResourceGroup(
						*testing.MakeFlavorQuotas("x86").Resource("cpu", "1", "1").Obj()).
					Parent("parent").
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should reject lendingLimit when no parent",
				testing.MakeCohort("cohort").
					ResourceGroup(
						testing.MakeFlavorQuotas("x86").
							ResourceQuotaWrapper("cpu").NominalQuota("1").LendingLimit("1").Append().
							FlavorQuotas,
					).
					Obj(),
				testing.BeForbiddenError()),
			ginkgo.Entry("Should allow lendingLimit when parent exists",
				testing.MakeCohort("cohort").
					ResourceGroup(
						testing.MakeFlavorQuotas("x86").
							ResourceQuotaWrapper("cpu").NominalQuota("1").LendingLimit("1").Append().
							FlavorQuotas,
					).
					Parent("parent").
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow lendingLimit 0 when parent exists",
				testing.MakeCohort("cohort").
					ResourceGroup(
						testing.MakeFlavorQuotas("x86").
							ResourceQuotaWrapper("cpu").NominalQuota("0").LendingLimit("0").Append().
							FlavorQuotas,
					).
					Parent("parent").
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow lending limit to exceed nominal quota",
				testing.MakeCohort("cohort").
					ResourceGroup(
						testing.MakeFlavorQuotas("x86").
							ResourceQuotaWrapper("cpu").NominalQuota("3").LendingLimit("5").Append().
							FlavorQuotas,
					).
					Parent("parent").
					Obj(),
				gomega.Succeed()),
			ginkgo.Entry("Should allow multiple resource groups",
				testing.MakeCohort("cohort").
					ResourceGroup(
						*testing.MakeFlavorQuotas("alpha").
							Resource("cpu", "0").
							Resource("memory", "0").
							Obj(),
						*testing.MakeFlavorQuotas("beta").
							Resource("cpu", "0").
							Resource("memory", "0").
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
			ginkgo.Entry("Should reject resources in a flavor in different order",
				&kueuealpha.Cohort{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cohort",
					},
					Spec: kueuealpha.CohortSpec{
						ResourceGroups: []kueue.ResourceGroup{
							{
								CoveredResources: []corev1.ResourceName{"cpu", "memory"},
								Flavors: []kueue.FlavorQuotas{
									*testing.MakeFlavorQuotas("alpha").
										Resource("cpu", "0").
										Resource("memory", "0").
										Obj(),
									*testing.MakeFlavorQuotas("beta").
										Resource("memory", "0").
										Resource("cpu", "0").
										Obj(),
								},
							},
						},
					},
				},
				testing.BeForbiddenError()),
			ginkgo.Entry("Should reject missing resources in a flavor",
				&kueuealpha.Cohort{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cohort",
					},
					Spec: kueuealpha.CohortSpec{
						ResourceGroups: []kueue.ResourceGroup{
							{
								CoveredResources: []corev1.ResourceName{"cpu", "memory"},
								Flavors: []kueue.FlavorQuotas{
									*testing.MakeFlavorQuotas("alpha").
										Resource("cpu", "0").
										Obj(),
								},
							},
						},
					},
				},
				testing.BeInvalidError()),
			ginkgo.Entry("Should reject resource not defined in resource group",
				&kueuealpha.Cohort{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cohort",
					},
					Spec: kueuealpha.CohortSpec{
						ResourceGroups: []kueue.ResourceGroup{
							{
								CoveredResources: []corev1.ResourceName{"cpu"},
								Flavors: []kueue.FlavorQuotas{
									*testing.MakeFlavorQuotas("alpha").
										Resource("cpu", "0").
										Resource("memory", "0").
										Obj(),
								},
							},
						},
					},
				},
				testing.BeInvalidError()),
			ginkgo.Entry("Should reject resource in more than one resource group",
				testing.MakeCohort("cohort").
					ResourceGroup(
						*testing.MakeFlavorQuotas("alpha").
							Resource("cpu", "0").
							Resource("memory", "0").
							Obj(),
					).
					ResourceGroup(
						*testing.MakeFlavorQuotas("beta").
							Resource("memory", "0").
							Obj(),
					).
					Obj(),
				testing.BeForbiddenError()),
			ginkgo.Entry("Should reject flavor in more than one resource group",
				testing.MakeCohort("cohort").
					ResourceGroup(
						*testing.MakeFlavorQuotas("alpha").Resource("cpu").Obj(),
						*testing.MakeFlavorQuotas("beta").Resource("cpu").Obj(),
					).
					ResourceGroup(
						*testing.MakeFlavorQuotas("beta").Resource("memory").Obj(),
					).
					Obj(),
				testing.BeForbiddenError()),
		)
	})

	ginkgo.When("Updating a Cohort", func() {
		ginkgo.It("Should update parent", func() {
			cohort := testing.MakeCohort("cohort").Obj()
			gomega.Expect(k8sClient.Create(ctx, cohort)).Should(gomega.Succeed())

			updated := cohort.DeepCopy()
			updated.Spec.Parent = "cohort2"

			gomega.Expect(k8sClient.Update(ctx, updated)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cohort, true)
		})
		ginkgo.It("Should reject invalid parent", func() {
			cohort := testing.MakeCohort("cohort").Obj()
			gomega.Expect(k8sClient.Create(ctx, cohort)).Should(gomega.Succeed())

			updated := cohort.DeepCopy()
			updated.Spec.Parent = "@cohort2"

			gomega.Expect(k8sClient.Update(ctx, updated)).ShouldNot(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cohort, true)
		})
		ginkgo.It("Should reject negative borrowing limit", func() {
			cohort := testing.MakeCohort("cohort").
				ResourceGroup(testing.MakeFlavorQuotas("x86").Resource("cpu", "-1").FlavorQuotas).Cohort

			gomega.Expect(k8sClient.Create(ctx, &cohort)).ShouldNot(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, &cohort, true)
		})
	})
})
