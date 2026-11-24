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

package baseline

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueueclientset "sigs.k8s.io/kueue/client-go/clientset/versioned"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

const (
	preemptionConfigAdminUser  = "preemptionconfig-test-admin"
	preemptionConfigViewerUser = "preemptionconfig-test-viewer"
	preemptionConfigBatchUser  = "preemptionconfig-test-user"
	preemptionConfigNoRoleUser = "preemptionconfig-test-nobody"
)

var _ = ginkgo.Describe("PreemptionConfig RBAC", ginkgo.Label("area:singlecluster", "feature:rbac"), func() {
	ginkgo.When("A subject is bound to kueue-batch-admin-role", func() {
		var adminClient kueueclientset.Interface

		ginkgo.BeforeEach(func() {
			adminClient = e2e.BindUserToClusterRole(ctx, k8sClient,
				preemptionConfigAdminUser, "kueue-batch-admin-role", e2e.ListLocalQueues)
		})

		ginkgo.It("Should allow the full PreemptionConfig lifecycle", func() {
			preemptionConfigs := adminClient.KueueV1alpha1().PreemptionConfigs()
			preemptionConfig := utiltestingalpha.MakePreemptionConfig("preemptionconfig-rbac-admin").
				Rule("rule", kueuealpha.InsufficientQuota,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).Obj()).
				Obj()
			// Also removed by the last step; this covers a failure before then.
			ginkgo.DeferCleanup(func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, preemptionConfig, true)
			})

			ginkgo.By("Creating a PreemptionConfig", func() {
				created, err := preemptionConfigs.Create(ctx, preemptionConfig, metav1.CreateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				preemptionConfig = created
			})

			ginkgo.By("Getting the PreemptionConfig", func() {
				got, err := preemptionConfigs.Get(ctx, preemptionConfig.Name, metav1.GetOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(got.Spec.Rules).Should(gomega.HaveLen(1))
			})

			ginkgo.By("Listing the PreemptionConfigs", func() {
				list, err := preemptionConfigs.List(ctx, metav1.ListOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(list.Items).Should(gomega.ContainElement(
					gomega.HaveField("ObjectMeta.Name", preemptionConfig.Name)))
			})

			ginkgo.By("Updating the PreemptionConfig", func() {
				// No Eventually: nothing else writes this object, so a retry would only hide a
				// missing update verb behind a timeout.
				preemptionConfig.Spec.Rules[0].CandidateSelectors[0].Scope = kueuealpha.WithinParentCohort
				updated, err := preemptionConfigs.Update(ctx, preemptionConfig, metav1.UpdateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(updated.Spec.Rules[0].CandidateSelectors[0].Scope).Should(gomega.Equal(kueuealpha.WithinParentCohort))
				preemptionConfig = updated
			})

			ginkgo.By("Deleting the PreemptionConfig", func() {
				err := preemptionConfigs.Delete(ctx, preemptionConfig.Name, metav1.DeleteOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			})
		})
	})

	ginkgo.When("A subject is bound to kueue-preemptionconfig-viewer-role", func() {
		var (
			viewerClient     kueueclientset.Interface
			preemptionConfig *kueuealpha.PreemptionConfig
		)

		ginkgo.BeforeEach(func() {
			// The viewer cannot create its own fixture, so the suite's admin client does it.
			preemptionConfig = utiltestingalpha.MakePreemptionConfig("preemptionconfig-rbac-viewer").
				Rule("rule", kueuealpha.InsufficientQuota,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).Obj()).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, preemptionConfig)
			ginkgo.DeferCleanup(func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, preemptionConfig, true)
			})

			viewerClient = e2e.BindUserToClusterRole(ctx, k8sClient,
				preemptionConfigViewerUser, "kueue-preemptionconfig-viewer-role", listPreemptionConfigs)
		})

		ginkgo.It("Should allow reads but forbid writes", func() {
			preemptionConfigs := viewerClient.KueueV1alpha1().PreemptionConfigs()

			ginkgo.By("Getting the PreemptionConfig", func() {
				got, err := preemptionConfigs.Get(ctx, preemptionConfig.Name, metav1.GetOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(got.Spec.Rules).Should(gomega.HaveLen(1))
			})

			ginkgo.By("Listing the PreemptionConfigs", func() {
				list, err := preemptionConfigs.List(ctx, metav1.ListOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(list.Items).Should(gomega.ContainElement(
					gomega.HaveField("ObjectMeta.Name", preemptionConfig.Name)))
			})

			expectPreemptionConfigAccessForbiddenForWrites(viewerClient, preemptionConfig)
		})
	})

	ginkgo.When("A subject is bound to kueue-batch-user-role", func() {
		var userClient kueueclientset.Interface

		ginkgo.BeforeEach(func() {
			userClient = e2e.BindUserToClusterRole(ctx, k8sClient,
				preemptionConfigBatchUser, "kueue-batch-user-role", e2e.ListLocalQueues)
		})

		ginkgo.It("Should be Forbidden from accessing PreemptionConfigs", func() {
			expectPreemptionConfigAccessForbidden(userClient, "preemptionconfig-rbac-user")
		})
	})

	ginkgo.When("A subject is bound to neither kueue-batch-admin-role nor kueue-batch-user-role", func() {
		ginkgo.It("Should be Forbidden from accessing PreemptionConfigs", func() {
			expectPreemptionConfigAccessForbidden(
				e2e.CreateKueueClientset(preemptionConfigNoRoleUser), "preemptionconfig-rbac-nobody")
		})
	})
})

// listPreemptionConfigs is the readiness probe for preemptionconfig-viewer, which grants nothing
// else. That makes the list assertion in that spec tautological, but its denials still hold.
func listPreemptionConfigs(ctx context.Context, c kueueclientset.Interface) error {
	_, err := c.KueueV1alpha1().PreemptionConfigs().List(ctx, metav1.ListOptions{})
	return err
}

// expectPreemptionConfigAccessForbidden asserts that c is denied every verb on PreemptionConfigs.
// RBAC is checked before the object is looked up, so name does not need to exist.
func expectPreemptionConfigAccessForbidden(c kueueclientset.Interface, name string) {
	ginkgo.GinkgoHelper()

	preemptionConfigs := c.KueueV1alpha1().PreemptionConfigs()

	ginkgo.By("Returning a Forbidden error for a get request", func() {
		_, err := preemptionConfigs.Get(ctx, name, metav1.GetOptions{})
		gomega.Expect(err).Should(utiltesting.BeForbiddenError())
	})

	ginkgo.By("Returning a Forbidden error for a list request", func() {
		_, err := preemptionConfigs.List(ctx, metav1.ListOptions{})
		gomega.Expect(err).Should(utiltesting.BeForbiddenError())
	})

	expectPreemptionConfigAccessForbiddenForWrites(c, utiltestingalpha.MakePreemptionConfig(name).Obj())
}

// expectPreemptionConfigAccessForbiddenForWrites asserts that c is denied every write verb on
// preemptionConfig.
func expectPreemptionConfigAccessForbiddenForWrites(c kueueclientset.Interface, preemptionConfig *kueuealpha.PreemptionConfig) {
	ginkgo.GinkgoHelper()

	preemptionConfigs := c.KueueV1alpha1().PreemptionConfigs()

	// Only needed if the create below unexpectedly succeeds.
	ginkgo.DeferCleanup(func() {
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, preemptionConfig, true)
	})

	ginkgo.By("Returning a Forbidden error for a create request", func() {
		_, err := preemptionConfigs.Create(ctx, preemptionConfig, metav1.CreateOptions{})
		gomega.Expect(err).Should(utiltesting.BeForbiddenError())
	})

	ginkgo.By("Returning a Forbidden error for an update request", func() {
		_, err := preemptionConfigs.Update(ctx, preemptionConfig, metav1.UpdateOptions{})
		gomega.Expect(err).Should(utiltesting.BeForbiddenError())
	})

	ginkgo.By("Returning a Forbidden error for a delete request", func() {
		err := preemptionConfigs.Delete(ctx, preemptionConfig.Name, metav1.DeleteOptions{})
		gomega.Expect(err).Should(utiltesting.BeForbiddenError())
	})
}
