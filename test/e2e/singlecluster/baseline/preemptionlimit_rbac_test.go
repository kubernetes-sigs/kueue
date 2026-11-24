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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueueclientset "sigs.k8s.io/kueue/client-go/clientset/versioned"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

const (
	preemptionLimitAdminUser  = "preemptionlimit-test-admin"
	preemptionLimitViewerUser = "preemptionlimit-test-viewer"
	preemptionLimitBatchUser  = "preemptionlimit-test-user"
	preemptionLimitNoRoleUser = "preemptionlimit-test-nobody"
)

var _ = ginkgo.Describe("PreemptionLimit RBAC", ginkgo.Label("area:singlecluster", "feature:rbac"), func() {
	ginkgo.When("A subject is bound to kueue-batch-admin-role", func() {
		var adminClient kueueclientset.Interface

		ginkgo.BeforeEach(func() {
			adminClient = e2e.BindUserToClusterRole(ctx, k8sClient,
				preemptionLimitAdminUser, "kueue-batch-admin-role", e2e.ListLocalQueues)
		})

		ginkgo.It("Should allow the full PreemptionLimit lifecycle but forbid status writes", func() {
			preemptionLimits := adminClient.KueueV1alpha1().PreemptionLimits()
			preemptionLimit := utiltestingalpha.MakePreemptionLimit("preemptionlimit-rbac-admin", kueuealpha.GlobalLimitAggregation).
				Limit(1).
				WindowSeconds(60).
				Obj()
			// Also removed by the last step; this covers a failure before then.
			ginkgo.DeferCleanup(func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, preemptionLimit, true)
			})

			ginkgo.By("Creating a PreemptionLimit", func() {
				created, err := preemptionLimits.Create(ctx, preemptionLimit, metav1.CreateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				preemptionLimit = created
			})

			ginkgo.By("Getting the PreemptionLimit", func() {
				got, err := preemptionLimits.Get(ctx, preemptionLimit.Name, metav1.GetOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(got.Spec.Limit).Should(gomega.Equal(int32(1)))
			})

			ginkgo.By("Getting the PreemptionLimit status", func() {
				gomega.Expect(getPreemptionLimitStatus(adminClient, preemptionLimit.Name)).To(gomega.Succeed())
			})

			ginkgo.By("Listing the PreemptionLimits", func() {
				list, err := preemptionLimits.List(ctx, metav1.ListOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(list.Items).Should(gomega.ContainElement(
					gomega.HaveField("ObjectMeta.Name", preemptionLimit.Name)))
			})

			ginkgo.By("Updating the PreemptionLimit", func() {
				// Kueue may write the status meanwhile, which bumps resourceVersion, so retry on
				// conflicts. Any other error, such as Forbidden, stops the retries immediately.
				gomega.Eventually(func(g gomega.Gomega) {
					latest, err := preemptionLimits.Get(ctx, preemptionLimit.Name, metav1.GetOptions{})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					latest.Spec.Limit = 2
					updated, err := preemptionLimits.Update(ctx, latest, metav1.UpdateOptions{})
					if err != nil && !apierrors.IsConflict(err) {
						gomega.StopTrying("Failed to update the PreemptionLimit").Wrap(err).Now()
					}
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(updated.Spec.Limit).Should(gomega.Equal(int32(2)))
					preemptionLimit = updated
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Returning a Forbidden error for a status update request", func() {
				// Status is owned by the Kueue controller, so even the editor role only reads it.
				_, err := preemptionLimits.UpdateStatus(ctx, preemptionLimit, metav1.UpdateOptions{})
				gomega.Expect(err).Should(utiltesting.BeForbiddenError())
			})

			ginkgo.By("Deleting the PreemptionLimit", func() {
				err := preemptionLimits.Delete(ctx, preemptionLimit.Name, metav1.DeleteOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			})
		})
	})

	ginkgo.When("A subject is bound to kueue-preemptionlimit-viewer-role", func() {
		var (
			viewerClient    kueueclientset.Interface
			preemptionLimit *kueuealpha.PreemptionLimit
		)

		ginkgo.BeforeEach(func() {
			// The viewer cannot create its own fixture, so the suite's admin client does it.
			preemptionLimit = utiltestingalpha.MakePreemptionLimit("preemptionlimit-rbac-viewer", kueuealpha.GlobalLimitAggregation).
				Limit(1).
				WindowSeconds(60).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, preemptionLimit)
			ginkgo.DeferCleanup(func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, preemptionLimit, true)
			})

			viewerClient = e2e.BindUserToClusterRole(ctx, k8sClient,
				preemptionLimitViewerUser, "kueue-preemptionlimit-viewer-role", listPreemptionLimits)
		})

		ginkgo.It("Should allow reads but forbid writes", func() {
			preemptionLimits := viewerClient.KueueV1alpha1().PreemptionLimits()

			ginkgo.By("Getting the PreemptionLimit", func() {
				got, err := preemptionLimits.Get(ctx, preemptionLimit.Name, metav1.GetOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(got.Spec.Limit).Should(gomega.Equal(int32(1)))
			})

			ginkgo.By("Getting the PreemptionLimit status", func() {
				gomega.Expect(getPreemptionLimitStatus(viewerClient, preemptionLimit.Name)).To(gomega.Succeed())
			})

			ginkgo.By("Listing the PreemptionLimits", func() {
				list, err := preemptionLimits.List(ctx, metav1.ListOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(list.Items).Should(gomega.ContainElement(
					gomega.HaveField("ObjectMeta.Name", preemptionLimit.Name)))
			})

			expectPreemptionLimitAccessForbiddenForWrites(viewerClient, preemptionLimit)
		})
	})

	ginkgo.When("A subject is bound to kueue-batch-user-role", func() {
		var userClient kueueclientset.Interface

		ginkgo.BeforeEach(func() {
			userClient = e2e.BindUserToClusterRole(ctx, k8sClient,
				preemptionLimitBatchUser, "kueue-batch-user-role", e2e.ListLocalQueues)
		})

		ginkgo.It("Should be Forbidden from accessing PreemptionLimits", func() {
			expectPreemptionLimitAccessForbidden(userClient, "preemptionlimit-rbac-user")
		})
	})

	ginkgo.When("A subject is bound to neither kueue-batch-admin-role nor kueue-batch-user-role", func() {
		ginkgo.It("Should be Forbidden from accessing PreemptionLimits", func() {
			expectPreemptionLimitAccessForbidden(
				e2e.CreateKueueClientset(preemptionLimitNoRoleUser), "preemptionlimit-rbac-nobody")
		})
	})
})

// listPreemptionLimits is the readiness probe for preemptionlimit-viewer, which grants nothing
// outside PreemptionLimits. That makes the list assertion in that spec tautological, but its
// denials still hold.
func listPreemptionLimits(ctx context.Context, c kueueclientset.Interface) error {
	_, err := c.KueueV1alpha1().PreemptionLimits().List(ctx, metav1.ListOptions{})
	return err
}

// getPreemptionLimitStatus reads name through the status subresource, which RBAC authorizes
// separately from the main resource. The typed clientset has no getter for subresources.
func getPreemptionLimitStatus(c kueueclientset.Interface, name string) error {
	return c.KueueV1alpha1().RESTClient().Get().
		Resource("preemptionlimits").
		Name(name).
		SubResource("status").
		Do(ctx).
		Error()
}

// expectPreemptionLimitAccessForbidden asserts that c is denied every verb on PreemptionLimits.
// RBAC is checked before the object is looked up, so name does not need to exist.
func expectPreemptionLimitAccessForbidden(c kueueclientset.Interface, name string) {
	ginkgo.GinkgoHelper()

	preemptionLimits := c.KueueV1alpha1().PreemptionLimits()

	ginkgo.By("Returning a Forbidden error for a get request", func() {
		_, err := preemptionLimits.Get(ctx, name, metav1.GetOptions{})
		gomega.Expect(err).Should(utiltesting.BeForbiddenError())
	})

	ginkgo.By("Returning a Forbidden error for a status get request", func() {
		gomega.Expect(getPreemptionLimitStatus(c, name)).Should(utiltesting.BeForbiddenError())
	})

	ginkgo.By("Returning a Forbidden error for a list request", func() {
		_, err := preemptionLimits.List(ctx, metav1.ListOptions{})
		gomega.Expect(err).Should(utiltesting.BeForbiddenError())
	})

	expectPreemptionLimitAccessForbiddenForWrites(c,
		utiltestingalpha.MakePreemptionLimit(name, kueuealpha.GlobalLimitAggregation).Obj())
}

// expectPreemptionLimitAccessForbiddenForWrites asserts that c is denied every write verb on
// preemptionLimit, including writes to its status.
func expectPreemptionLimitAccessForbiddenForWrites(c kueueclientset.Interface, preemptionLimit *kueuealpha.PreemptionLimit) {
	ginkgo.GinkgoHelper()

	preemptionLimits := c.KueueV1alpha1().PreemptionLimits()

	// Only needed if the create below unexpectedly succeeds.
	ginkgo.DeferCleanup(func() {
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, preemptionLimit, true)
	})

	ginkgo.By("Returning a Forbidden error for a create request", func() {
		_, err := preemptionLimits.Create(ctx, preemptionLimit, metav1.CreateOptions{})
		gomega.Expect(err).Should(utiltesting.BeForbiddenError())
	})

	ginkgo.By("Returning a Forbidden error for an update request", func() {
		_, err := preemptionLimits.Update(ctx, preemptionLimit, metav1.UpdateOptions{})
		gomega.Expect(err).Should(utiltesting.BeForbiddenError())
	})

	ginkgo.By("Returning a Forbidden error for a status update request", func() {
		_, err := preemptionLimits.UpdateStatus(ctx, preemptionLimit, metav1.UpdateOptions{})
		gomega.Expect(err).Should(utiltesting.BeForbiddenError())
	})

	ginkgo.By("Returning a Forbidden error for a delete request", func() {
		err := preemptionLimits.Delete(ctx, preemptionLimit.Name, metav1.DeleteOptions{})
		gomega.Expect(err).Should(utiltesting.BeForbiddenError())
	})
}
