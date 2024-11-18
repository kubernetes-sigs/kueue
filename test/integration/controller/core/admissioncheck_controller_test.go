/*
Copyright 2023 The Kubernetes Authors.

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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("AdmissionCheck controller", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var ns *corev1.Namespace

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup)
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-admissioncheck-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("one clusterQueue references admissionChecks", func() {
		var admissionCheck *kueue.AdmissionCheck
		var clusterQueue *kueue.ClusterQueue

		ginkgo.BeforeEach(func() {
			admissionCheck = utiltesting.MakeAdmissionCheck("check1").ControllerName("ac-controller").Obj()
			clusterQueue = utiltesting.MakeClusterQueue("foo").
				AdmissionChecks("check1").
				Obj()

			gomega.Expect(k8sClient.Create(ctx, admissionCheck)).To(gomega.Succeed())

			ginkgo.By("Activating the admission check", func() {
				util.SetAdmissionCheckActive(ctx, k8sClient, admissionCheck, metav1.ConditionTrue)
			})

			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())

			ginkgo.By("Wait for the queue to become active", func() {
				util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)
			})
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, admissionCheck, true)
		})

		ginkgo.It("Should delete the admissionCheck when the corresponding clusterQueue no longer uses the admissionCheck", func() {
			ginkgo.By("Try to delete admissionCheck")
			gomega.Expect(util.DeleteObject(ctx, k8sClient, admissionCheck)).To(gomega.Succeed())
			var ac kueue.AdmissionCheck
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(admissionCheck), &ac)).To(gomega.Succeed())
				g.Expect(ac.GetFinalizers()).Should(gomega.BeComparableTo([]string{kueue.ResourceInUseFinalizerName}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Expect(ac.GetDeletionTimestamp()).ShouldNot(gomega.BeNil())

			ginkgo.By("Update clusterQueue's cohort")
			var cq kueue.ClusterQueue
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &cq)).To(gomega.Succeed())
				cq.Spec.Cohort = "foo-cohort"
				g.Expect(k8sClient.Update(ctx, &cq)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(admissionCheck), &ac)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Change clusterQueue's checks")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &cq)).Should(gomega.Succeed())
				cq.Spec.AdmissionChecks = []string{"check2"}
				g.Expect(k8sClient.Update(ctx, &cq)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(admissionCheck), &ac)).Should(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should delete the admissionCheck when the corresponding clusterQueue is deleted", func() {
			gomega.Expect(util.DeleteObject(ctx, k8sClient, admissionCheck)).To(gomega.Succeed())

			var rf kueue.AdmissionCheck
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(admissionCheck), &rf)).To(gomega.Succeed())
				g.Expect(rf.GetFinalizers()).Should(gomega.BeComparableTo([]string{kueue.ResourceInUseFinalizerName}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Expect(rf.GetDeletionTimestamp()).ShouldNot(gomega.BeNil())

			gomega.Expect(util.DeleteObject(ctx, k8sClient, clusterQueue)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, admissionCheck, false)
		})
	})
})
