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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

// +kubebuilder:docs-gen:collapse=Imports

var _ = ginkgo.Describe("AdmissionCheck controller", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var ns *corev1.Namespace

	ginkgo.BeforeAll(func() {
		fwk = &framework.Framework{CRDPath: crdPath, WebhookPath: webhookPath}
		cfg = fwk.Init()
		ctx, k8sClient = fwk.RunManager(cfg, managerSetup)
	})
	ginkgo.AfterAll(func() {
		fwk.Teardown()
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
			admissionCheck = utiltesting.MakeAdmissionCheck("check1").Obj()
			clusterQueue = utiltesting.MakeClusterQueue("foo").
				AdmissionChecks("check1").
				Obj()

			gomega.Expect(k8sClient.Create(ctx, admissionCheck)).To(gomega.Succeed())

			ginkgo.By("Activating the admission check", func() {
				util.SetAdmissionCheckActive(ctx, k8sClient, admissionCheck, metav1.ConditionTrue)
			})

			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())

			ginkgo.By("Wait for the queue to become active", func() {
				gomega.Eventually(func() bool {
					var cq kueue.ClusterQueue
					err := k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &cq)
					if err != nil {
						return false
					}
					return apimeta.IsStatusConditionTrue(cq.Status.Conditions, kueue.ClusterQueueActive)
				}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			})
		})

		ginkgo.AfterEach(func() {
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectAdmissionCheckToBeDeleted(ctx, k8sClient, admissionCheck, true)
		})

		ginkgo.It("Should delete the admissionCheck when the corresponding clusterQueue no longer uses the admissionCheck", func() {
			ginkgo.By("Try to delete admissionCheck")
			gomega.Expect(util.DeleteAdmissionCheck(ctx, k8sClient, admissionCheck)).To(gomega.Succeed())
			var ac kueue.AdmissionCheck
			gomega.Eventually(func() []string {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(admissionCheck), &ac)).To(gomega.Succeed())
				return ac.GetFinalizers()
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo([]string{kueue.ResourceInUseFinalizerName}))
			gomega.Expect(ac.GetDeletionTimestamp()).ShouldNot(gomega.BeNil())

			ginkgo.By("Update clusterQueue's cohort")
			var cq kueue.ClusterQueue
			gomega.Eventually(func() error {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &cq)).To(gomega.Succeed())
				cq.Spec.Cohort = "foo-cohort"
				return k8sClient.Update(ctx, &cq)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKeyFromObject(admissionCheck), &ac)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Change clusterQueue's checks")
			gomega.Eventually(func() error {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &cq)
				if err != nil {
					return err
				}
				cq.Spec.AdmissionChecks = []string{"check2"}
				return k8sClient.Update(ctx, &cq)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKeyFromObject(admissionCheck), &ac)
			}, util.Timeout, util.Interval).Should(utiltesting.BeNotFoundError())
		})

		ginkgo.It("Should delete the admissionCheck when the corresponding clusterQueue is deleted", func() {
			gomega.Expect(util.DeleteAdmissionCheck(ctx, k8sClient, admissionCheck)).To(gomega.Succeed())

			var rf kueue.AdmissionCheck
			gomega.Eventually(func() []string {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(admissionCheck), &rf)).To(gomega.Succeed())
				return rf.GetFinalizers()
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo([]string{kueue.ResourceInUseFinalizerName}))
			gomega.Expect(rf.GetDeletionTimestamp()).ShouldNot(gomega.BeNil())

			gomega.Expect(util.DeleteClusterQueue(ctx, k8sClient, clusterQueue)).To(gomega.Succeed())
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKeyFromObject(admissionCheck), &rf)
			}, util.Timeout, util.Interval).Should(utiltesting.BeNotFoundError())
		})
	})
})
