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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueueclientset "sigs.k8s.io/kueue/client-go/clientset/versioned"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
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
			adminClient = bindUserToClusterRole(
				preemptionConfigAdminUser, "kueue-batch-admin-role", listLocalQueues)
		})

		ginkgo.It("Should allow the full PreemptionConfig lifecycle", func() {
			preemptionConfigs := adminClient.KueueV1alpha1().PreemptionConfigs()
			preemptionConfig := makePreemptionConfig("preemptionconfig-rbac-admin")
			// Also removed by the last step; this covers a failure before then.
			ginkgo.DeferCleanup(func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, preemptionConfig, true)
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
			preemptionConfig = makePreemptionConfig("preemptionconfig-rbac-viewer")
			util.MustCreate(ctx, k8sClient, preemptionConfig)
			ginkgo.DeferCleanup(func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, preemptionConfig, true)
			})

			viewerClient = bindUserToClusterRole(
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
			userClient = bindUserToClusterRole(
				preemptionConfigBatchUser, "kueue-batch-user-role", listLocalQueues)
		})

		ginkgo.It("Should be Forbidden from accessing PreemptionConfigs", func() {
			expectPreemptionConfigAccessForbidden(userClient, "preemptionconfig-rbac-user")
		})
	})

	ginkgo.When("A subject is bound to neither kueue-batch-admin-role nor kueue-batch-user-role", func() {
		ginkgo.It("Should be Forbidden from accessing PreemptionConfigs", func() {
			expectPreemptionConfigAccessForbidden(
				util.CreateKueueClientset(preemptionConfigNoRoleUser), "preemptionconfig-rbac-nobody")
		})
	})
})

// makePreemptionConfig returns a PreemptionConfig with one minimal rule. RBAC is evaluated before
// validation, so the rule's content does not matter here;
func makePreemptionConfig(name string) *kueuealpha.PreemptionConfig {
	return &kueuealpha.PreemptionConfig{
		Name: name,
		Spec: kueuealpha.PreemptionConfigSpec{
			Rules: []kueuealpha.PreemptionConfigPreemptionRule{{
				Name: "rule",
				ActivationPolicy: kueuealpha.PreemptionConfigActivationPolicy{
					Trigger: kueuealpha.InsufficientQuota,
				},
				CandidateSelectors: []kueuealpha.PreemptionConfigPreemptionCandidateSelector{{
					Scope: kueuealpha.WithinClusterQueue,
				}},
			}},
		},
	}
}

// bindUserToClusterRole binds user to clusterRole and returns a clientset acting as that user,
// once probe confirms the binding is in effect. The binding is removed when the spec ends.
//
// The binding is cluster-wide because PreemptionConfig is cluster-scoped: a RoleBinding would deny
// access on scope alone, making the Forbidden assertions pass for the wrong reason.
func bindUserToClusterRole(user, clusterRole string, probe func(kueueclientset.Interface) error) kueueclientset.Interface {
	ginkgo.GinkgoHelper()

	binding := utiltesting.MakeClusterRoleBinding(user+"-binding").
		RoleRef(rbacv1.GroupName, "ClusterRole", clusterRole).
		UserSubject(user).
		Obj()
	util.MustCreate(ctx, k8sClient, binding)
	// Registered before the gate below: a gate failure must not leak this cluster-scoped object,
	// or the next run collides with it on create.
	ginkgo.DeferCleanup(func() {
		util.ExpectObjectToBeDeleted(ctx, k8sClient, binding, true)
	})

	clientset := util.CreateKueueClientset(user)
	ginkgo.By("Wait for an already granted request to succeed to make sure the role binding is in effect", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(probe(clientset)).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
	return clientset
}

// listLocalQueues is the readiness probe for roles that grant more than PreemptionConfigs. Probing
// the permission under test would report a real regression as an opaque BeforeEach timeout.
func listLocalQueues(c kueueclientset.Interface) error {
	_, err := c.KueueV1beta2().LocalQueues(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	return err
}

// listPreemptionConfigs is the readiness probe for preemptionconfig-viewer, which grants nothing
// else. That makes the list assertion in that spec tautological, but its denials still hold.
func listPreemptionConfigs(c kueueclientset.Interface) error {
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

	expectPreemptionConfigAccessForbiddenForWrites(c, &kueuealpha.PreemptionConfig{Name: name})
}

// expectPreemptionConfigAccessForbiddenForWrites asserts that c is denied every write verb on
// preemptionConfig.
func expectPreemptionConfigAccessForbiddenForWrites(c kueueclientset.Interface, preemptionConfig *kueuealpha.PreemptionConfig) {
	ginkgo.GinkgoHelper()

	preemptionConfigs := c.KueueV1alpha1().PreemptionConfigs()

	// Only needed if the create below unexpectedly succeeds.
	ginkgo.DeferCleanup(func() {
		util.ExpectObjectToBeDeleted(ctx, k8sClient, preemptionConfig, true)
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
