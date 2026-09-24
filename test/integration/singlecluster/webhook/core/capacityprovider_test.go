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

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("CapacityProvider Validation", func() {
	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerSetup)
	})
	ginkgo.AfterEach(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.When("Creating CapacityProvider", func() {
		ginkgo.It("Should allow valid parameters reference", func() {
			cp := utiltestingalpha.MakeCapacityProvider("cp-valid-params").
				ControllerName("test-controller").
				OrchestratedFlavors("flavor-1").
				Parameters("example.com", "Config", "my-config").
				Obj()

			util.MustCreate(ctx, k8sClient, cp)
			defer func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, cp, true)
			}()
		})

		ginkgo.DescribeTable("Validate parameters reference on creation",
			func(apiGroup, kind, name string) {
				cp := utiltestingalpha.MakeCapacityProvider("cp-invalid-params").
					ControllerName("test-controller").
					OrchestratedFlavors("flavor-1").
					Parameters(apiGroup, kind, name).
					Obj()

				err := k8sClient.Create(ctx, cp)
				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(err).To(utiltesting.BeInvalidError())
			},
			ginkgo.Entry("Disallow empty apiGroup", "", "Config", "my-config"),
			ginkgo.Entry("Disallow empty kind", "example.com", "", "my-config"),
			ginkgo.Entry("Disallow empty name", "example.com", "Config", ""),
		)

		ginkgo.DescribeTable("Validate orchestratedFlavors on creation",
			func(flavors []kueuealpha.CapacityProviderOrchestratedFlavor, isValid bool) {
				cp := utiltestingalpha.MakeCapacityProvider("cp-orchestrated-flavors").
					ControllerName("test-controller").
					Obj()
				cp.Spec.OrchestratedFlavors = flavors

				err := k8sClient.Create(ctx, cp)
				if isValid {
					gomega.Expect(err).To(gomega.Succeed())
					util.ExpectObjectToBeDeleted(ctx, k8sClient, cp, true)
				} else {
					gomega.Expect(err).To(gomega.HaveOccurred())
					gomega.Expect(err).To(utiltesting.BeInvalidError())
				}
			},
			ginkgo.Entry("Disallow omitted orchestratedFlavors", nil, false),
			ginkgo.Entry("Disallow empty orchestratedFlavors", []kueuealpha.CapacityProviderOrchestratedFlavor{}, false),
			ginkgo.Entry("Disallow duplicate flavor names", orchestratedFlavors("flavor-1", "flavor-1"), false),
			ginkgo.Entry("Disallow empty flavor name", orchestratedFlavors(""), false),
			ginkgo.Entry("Disallow invalid flavor name", orchestratedFlavors("Bad_Name"), false),
			ginkgo.Entry("Allow a single flavor", orchestratedFlavors("flavor-1"), true),
			ginkgo.Entry("Allow maximum flavors (count 64)", numberedOrchestratedFlavors(64), true),
			ginkgo.Entry("Disallow too many flavors (count 65)", numberedOrchestratedFlavors(65), false),
		)
	})

	ginkgo.When("Updating CapacityProvider spec", func() {
		ginkgo.It("Should enforce controllerName immutability", func() {
			cp := utiltestingalpha.MakeCapacityProvider("cp-immutability").
				ControllerName("initial-controller").
				OrchestratedFlavors("flavor-1").
				Obj()

			util.MustCreate(ctx, k8sClient, cp)
			defer func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, cp, true)
			}()

			var fetched kueuealpha.CapacityProvider
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cp), &fetched)).To(gomega.Succeed())

			ginkgo.By("Rejecting an update that modifies controllerName")
			fetched.Spec.ControllerName = "modified-controller"
			err := k8sClient.Update(ctx, &fetched)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(err).To(utiltesting.BeInvalidError())
			gomega.Expect(err.Error()).To(gomega.ContainSubstring("field is immutable"))

			ginkgo.By("Allowing an update that keeps controllerName unchanged")
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cp), &fetched)).To(gomega.Succeed())
			fetched.Spec.OrchestratedFlavors = append(fetched.Spec.OrchestratedFlavors, kueuealpha.CapacityProviderOrchestratedFlavor{
				Name: "flavor-2",
			})
			gomega.Expect(k8sClient.Update(ctx, &fetched)).To(gomega.Succeed())
		})
	})

	ginkgo.When("Updating CapacityProvider status capacity flavors resources", func() {
		ginkgo.DescribeTable("Validate resources count in status.capacity.flavors[*]",
			func(resourceCount int, isValid bool) {
				cp := utiltestingalpha.MakeCapacityProvider(fmt.Sprintf("cp-res-%d", resourceCount)).
					ControllerName("controller-res").
					OrchestratedFlavors("flavor-1").
					Obj()

				util.MustCreate(ctx, k8sClient, cp)
				defer func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, cp, true)
				}()

				resources := corev1.ResourceList{}
				for i := range resourceCount {
					resources[corev1.ResourceName(fmt.Sprintf("example.com/res-%d", i))] = resource.MustParse("1")
				}

				var fetched kueuealpha.CapacityProvider
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cp), &fetched)).To(gomega.Succeed())
				fetched.Status.Capacity = &kueuealpha.CapacityProviderNormalizedCapacity{
					Flavors: []kueuealpha.CapacityProviderNormalizedCapacityFlavor{
						{
							Name:      "flavor-1",
							Resources: resources,
						},
					},
				}

				err := k8sClient.Status().Update(ctx, &fetched)
				if isValid {
					gomega.Expect(err).To(gomega.Succeed())
				} else {
					gomega.Expect(err).To(gomega.HaveOccurred())
					gomega.Expect(err).To(utiltesting.BeInvalidError())
					gomega.Expect(err.Error()).To(gomega.ContainSubstring("resource capacity must have at most 64 entries"))
				}
			},
			ginkgo.Entry("Allow empty resources (count 0)", 0, true),
			ginkgo.Entry("Allow minimum valid resources (count 1)", 1, true),
			ginkgo.Entry("Allow maximum valid resources (count 64)", 64, true),
			ginkgo.Entry("Disallow too many resources (count 65)", 65, false),
		)
	})
})

func orchestratedFlavors(names ...string) []kueuealpha.CapacityProviderOrchestratedFlavor {
	flavors := make([]kueuealpha.CapacityProviderOrchestratedFlavor, len(names))
	for i, name := range names {
		flavors[i] = kueuealpha.CapacityProviderOrchestratedFlavor{Name: kueuealpha.ResourceFlavorReference(name)}
	}
	return flavors
}

func numberedOrchestratedFlavors(count int) []kueuealpha.CapacityProviderOrchestratedFlavor {
	names := make([]string, count)
	for i := range count {
		names[i] = fmt.Sprintf("flavor-%d", i)
	}
	return orchestratedFlavors(names...)
}
