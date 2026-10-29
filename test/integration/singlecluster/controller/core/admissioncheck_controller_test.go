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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("AdmissionCheck controller", ginkgo.Label("controller:admissioncheck", "area:core"), func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerSetup)
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-admissioncheck-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		fwk.StopManager(ctx)
	})

	ginkgo.When("one clusterQueue references admissionChecks", func() {
		var admissionCheck *kueue.AdmissionCheck
		var clusterQueue *kueue.ClusterQueue

		ginkgo.BeforeEach(func() {
			admissionCheck = utiltestingapi.MakeAdmissionCheck("check1").ControllerName("ac-controller").Obj()
			clusterQueue = utiltestingapi.MakeClusterQueue("foo").
				AdmissionChecks("check1").
				Obj()

			behavioral.MustCreate(ctx, k8sClient, admissionCheck)

			ginkgo.By("Activating the admission check", func() {
				behavioral.SetAdmissionCheckActive(ctx, k8sClient, admissionCheck, metav1.ConditionTrue)
			})

			behavioral.MustCreate(ctx, k8sClient, clusterQueue)

			ginkgo.By("Wait for the queue to become active", func() {
				behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)
			})
		})

		ginkgo.AfterEach(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, admissionCheck, true)
		})

		ginkgo.It("Should delete the admissionCheck when the corresponding clusterQueue no longer uses the admissionCheck", func() {
			var ac kueue.AdmissionCheck

			ginkgo.By("Wait for the finalizer to be added")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(admissionCheck), &ac)).To(gomega.Succeed())
				g.Expect(ac.GetFinalizers()).Should(gomega.ContainElement(kueue.ResourceInUseFinalizerName))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("Try to delete admissionCheck")
			gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, admissionCheck)).To(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(admissionCheck), &ac)).To(gomega.Succeed())
				g.Expect(ac.GetDeletionTimestamp()).ShouldNot(gomega.BeNil())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("Update clusterQueue's cohort")
			var cq kueue.ClusterQueue
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &cq)).To(gomega.Succeed())
				cq.Spec.CohortName = "foo-cohort"
				g.Expect(k8sClient.Update(ctx, &cq)).Should(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(admissionCheck), &ac)).Should(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("Change clusterQueue's checks")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &cq)).Should(gomega.Succeed())
				cq.Spec.AdmissionChecksStrategy = &kueue.AdmissionChecksStrategy{
					AdmissionChecks: []kueue.AdmissionCheckStrategyRule{
						{Name: "check2"},
					},
				}
				g.Expect(k8sClient.Update(ctx, &cq)).Should(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(admissionCheck), &ac)).Should(utiltesting.BeNotFoundError())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should delete the admissionCheck when the corresponding clusterQueue is deleted", func() {
			var rf kueue.AdmissionCheck

			ginkgo.By("Wait for the finalizer to be added")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(admissionCheck), &rf)).To(gomega.Succeed())
				g.Expect(rf.GetFinalizers()).Should(gomega.ContainElement(kueue.ResourceInUseFinalizerName))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("Try to delete admissionCheck")
			gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, admissionCheck)).To(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(admissionCheck), &rf)).To(gomega.Succeed())
				g.Expect(rf.GetDeletionTimestamp()).ShouldNot(gomega.BeNil())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, clusterQueue)).To(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, admissionCheck, false)
		})
	})
})
