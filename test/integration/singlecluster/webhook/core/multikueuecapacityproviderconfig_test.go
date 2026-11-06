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
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("MultiKueueCapacityProviderConfig Validation", func() {
	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerSetup)
	})
	ginkgo.AfterEach(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.When("Creating MultiKueueCapacityProviderConfig", func() {
		ginkgo.DescribeTable("Validate name and managerSubtreeRootRef on creation",
			func(name string, kind kueuealpha.SubtreeRootRefKind, refName string, wantErr string) {
				config := utiltestingalpha.MakeMultiKueueCapacityProviderConfig(name).
					ManagerSubtreeRootRef(kind, refName).
					Obj()

				err := k8sClient.Create(ctx, config)
				if wantErr == "" {
					gomega.Expect(err).To(gomega.Succeed())
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, config, true)
				} else {
					gomega.Expect(err).To(utiltesting.BeInvalidError())
					gomega.Expect(err.Error()).To(gomega.ContainSubstring(wantErr))
				}
			},
			ginkgo.Entry("Allow a ClusterQueue managerSubtreeRootRef",
				"mkcpc-valid", kueuealpha.ClusterQueueSubtreeRootRefKind, "manager-cq", ""),
			ginkgo.Entry("Allow a name with 63 characters",
				strings.Repeat("a", 63), kueuealpha.ClusterQueueSubtreeRootRefKind, "manager-cq", ""),
			ginkgo.Entry("Allow a managerSubtreeRootRef name with dots",
				"mkcpc-ref-dots", kueuealpha.ClusterQueueSubtreeRootRefKind, "manager.cq", ""),
			ginkgo.Entry("Disallow a name with 64 characters",
				strings.Repeat("a", 64), kueuealpha.ClusterQueueSubtreeRootRefKind, "manager-cq",
				"name must be no more than 63 characters"),
			ginkgo.Entry("Disallow a name with dots",
				"mkcpc.with.dots", kueuealpha.ClusterQueueSubtreeRootRefKind, "manager-cq",
				"name must not contain dots"),
			ginkgo.Entry("Disallow the Cohort kind",
				"mkcpc-cohort", kueuealpha.CohortSubtreeRootRefKind, "manager-cohort",
				"only the ClusterQueue kind is allowed for managerSubtreeRootRef"),
			ginkgo.Entry("Disallow an empty managerSubtreeRootRef name",
				"mkcpc-empty-ref-name", kueuealpha.ClusterQueueSubtreeRootRefKind, "",
				"spec.managerSubtreeRootRef.name"),
			ginkgo.Entry("Disallow an invalid managerSubtreeRootRef name",
				"mkcpc-invalid-ref-name", kueuealpha.ClusterQueueSubtreeRootRefKind, "Bad_Name",
				"spec.managerSubtreeRootRef.name"),
		)

		ginkgo.It("Should disallow an omitted spec", func() {
			config := utiltestingalpha.MakeMultiKueueCapacityProviderConfig("mkcpc-no-spec").Obj()

			err := k8sClient.Create(ctx, config)
			gomega.Expect(err).To(utiltesting.BeInvalidError())
			gomega.Expect(err.Error()).To(gomega.ContainSubstring("spec: Required value"))
		})

		ginkgo.It("Should disallow an empty spec", func() {
			config := &unstructured.Unstructured{}
			config.SetAPIVersion(kueuealpha.SchemeGroupVersion.String())
			config.SetKind("MultiKueueCapacityProviderConfig")
			config.SetName("mkcpc-empty-spec")
			config.Object["spec"] = map[string]any{}

			err := k8sClient.Create(ctx, config)
			gomega.Expect(err).To(utiltesting.BeInvalidError())
			gomega.Expect(err.Error()).To(gomega.ContainSubstring("spec.managerSubtreeRootRef: Required value"))
		})
	})

	ginkgo.When("Updating MultiKueueCapacityProviderConfig", func() {
		ginkgo.It("Should allow changing managerSubtreeRootRef to another ClusterQueue only", func() {
			config := utiltestingalpha.MakeMultiKueueCapacityProviderConfig("mkcpc-update").
				ManagerSubtreeRootRef(kueuealpha.ClusterQueueSubtreeRootRefKind, "manager-cq-1").
				Obj()

			behavioral.MustCreate(ctx, k8sClient, config)
			defer func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, config, true)
			}()

			ginkgo.By("Allowing an update that points managerSubtreeRootRef to another ClusterQueue")
			var fetched kueuealpha.MultiKueueCapacityProviderConfig
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(config), &fetched)).To(gomega.Succeed())
			fetched.Spec.ManagerSubtreeRootRef.Name = "manager-cq-2"
			gomega.Expect(k8sClient.Update(ctx, &fetched)).To(gomega.Succeed())

			ginkgo.By("Rejecting an update that changes the managerSubtreeRootRef kind to Cohort")
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(config), &fetched)).To(gomega.Succeed())
			fetched.Spec.ManagerSubtreeRootRef.Kind = kueuealpha.CohortSubtreeRootRefKind
			err := k8sClient.Update(ctx, &fetched)
			gomega.Expect(err).To(utiltesting.BeInvalidError())
			gomega.Expect(err.Error()).To(gomega.ContainSubstring("only the ClusterQueue kind is allowed for managerSubtreeRootRef"))
		})
	})
})
