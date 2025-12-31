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

package e2e

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Kueue Certs", ginkgo.Serial, func() {
	var (
		ns             *corev1.Namespace
		onDemandFlavor *kueue.ResourceFlavor
		localQueue     *kueue.LocalQueue
		clusterQueue   *kueue.ClusterQueue
	)

	var (
		mvcKey           = client.ObjectKey{Name: "kueue-mutating-webhook-configuration"}
		localQueueCRDKey = client.ObjectKey{Name: "localqueues.kueue.x-k8s.io"}
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-certs-")
		onDemandFlavor = utiltestingapi.MakeResourceFlavor("on-demand-"+ns.Name).
			NodeLabel("instance-type", "on-demand").Obj()
		util.MustCreate(ctx, k8sClient, onDemandFlavor)
		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue-" + ns.Name).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(onDemandFlavor.Name).
					Resource(corev1.ResourceCPU, "1").
					Resource(corev1.ResourceMemory, "1Gi").
					Obj(),
			).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, clusterQueue, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, onDemandFlavor, true, util.LongTimeout)
	})

	ginkgo.It("should rotate the certificates for the CRD resources", func() {
		localQueueCRD := &apiextensionsv1.CustomResourceDefinition{}
		ginkgo.By("verify the caBundle is set after startup for CRD", func() {
			gomega.Expect(k8sClient.Get(ctx, localQueueCRDKey, localQueueCRD)).To(gomega.Succeed())

			caBundle := localQueueCRD.Spec.Conversion.Webhook.ClientConfig.CABundle
			gomega.Expect(caBundle).NotTo(gomega.BeEmpty())
		})

		ginkgo.By("verify the caBundle is set after startup for webhooks", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				mwc := &admissionregistrationv1.MutatingWebhookConfiguration{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "kueue-mutating-webhook-configuration"}, mwc)).To(gomega.Succeed())
				for _, webhook := range mwc.Webhooks {
					g.Expect(webhook.ClientConfig.CABundle).ToNot(gomega.BeEmpty())
				}
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("clear the caBundle field", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, localQueueCRDKey, localQueueCRD)).Should(gomega.Succeed())
				localQueueCRD.Spec.Conversion.Webhook.ClientConfig.CABundle = nil
				g.Expect(k8sClient.Update(ctx, localQueueCRD)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("clear the caBundle fields for webhooks", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				mwc := &admissionregistrationv1.MutatingWebhookConfiguration{}
				g.Expect(k8sClient.Get(ctx, mvcKey, mwc)).To(gomega.Succeed())
				for _, webhook := range mwc.Webhooks {
					webhook.ClientConfig.CABundle = nil
				}
				g.Expect(k8sClient.Update(ctx, mwc)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verify the caBundle is set again for CRD", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, localQueueCRDKey, localQueueCRD)).To(gomega.Succeed())

				caBundle := localQueueCRD.Spec.Conversion.Webhook.ClientConfig.CABundle
				g.Expect(caBundle).NotTo(gomega.BeEmpty())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verify the caBundle is set again for mutating webhooks", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				mwc := &admissionregistrationv1.MutatingWebhookConfiguration{}
				g.Expect(k8sClient.Get(ctx, mvcKey, mwc)).To(gomega.Succeed())
				for _, webhook := range mwc.Webhooks {
					g.Expect(webhook.ClientConfig.CABundle).ToNot(gomega.BeEmpty())
				}
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verify the localQueue can be fetched and mutated", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(localQueue), localQueue)).To(gomega.Succeed())
				localQueue.Spec.StopPolicy = ptr.To(kueue.Hold)
				g.Expect(k8sClient.Update(ctx, localQueue)).Should(gomega.Succeed())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verify the LocalQueue status is updated", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(localQueue), localQueue)).To(gomega.Succeed())
				g.Expect(localQueue.Status.Conditions).To(utiltesting.HaveConditionStatusFalse(kueue.LocalQueueActive))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should allow to bootstrap certificates", func() {
		oldReplicas := 0
		deployment := &appsv1.Deployment{}
		localQueueCRD := &apiextensionsv1.CustomResourceDefinition{}
		key := types.NamespacedName{Namespace: kueueNS, Name: "kueue-controller-manager"}
		ginkgo.By("scale down replicas to 0", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, key, deployment)).To(gomega.Succeed())
				oldReplicas = max(oldReplicas, int(ptr.Deref(deployment.Spec.Replicas, 0)))
				deployment.Spec.Replicas = ptr.To[int32](0)
				g.Expect(k8sClient.Update(ctx, deployment)).Should(gomega.Succeed())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("await for no replicas", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, key, deployment)).To(gomega.Succeed())
				g.Expect(deployment.Status.Replicas).To(gomega.Equal(int32(0)))
				g.Expect(deployment.Status.TerminatingReplicas).To(gomega.BeNil())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("clear the caBundle field", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, localQueueCRDKey, localQueueCRD)).Should(gomega.Succeed())
				localQueueCRD.Spec.Conversion.Webhook.ClientConfig.CABundle = nil
				g.Expect(k8sClient.Update(ctx, localQueueCRD)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("clear the caBundle fields for webhooks", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				mwc := &admissionregistrationv1.MutatingWebhookConfiguration{}
				g.Expect(k8sClient.Get(ctx, mvcKey, mwc)).To(gomega.Succeed())
				for i := range mwc.Webhooks {
					mwc.Webhooks[i].ClientConfig.CABundle = nil
				}
				g.Expect(k8sClient.Update(ctx, mwc)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("scale back replicas", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, key, deployment)).To(gomega.Succeed())
				deployment.Spec.Replicas = ptr.To(int32(oldReplicas))
				g.Expect(k8sClient.Update(ctx, deployment)).Should(gomega.Succeed())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("await for ready replicas", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, key, deployment)).To(gomega.Succeed())
				g.Expect(deployment.Status.ReadyReplicas).To(gomega.Equal(int32(oldReplicas)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("await for Kueue to be available", func() {
			util.WaitForKueueAvailability(ctx, k8sClient)
		})

		ginkgo.By("verify the caBundle is set again for CRD", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, localQueueCRDKey, localQueueCRD)).To(gomega.Succeed())
				caBundle := localQueueCRD.Spec.Conversion.Webhook.ClientConfig.CABundle
				g.Expect(caBundle).NotTo(gomega.BeEmpty())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verify the caBundle is set again for mutating webhooks", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				mwc := &admissionregistrationv1.MutatingWebhookConfiguration{}
				g.Expect(k8sClient.Get(ctx, mvcKey, mwc)).To(gomega.Succeed())
				for _, webhook := range mwc.Webhooks {
					g.Expect(webhook.ClientConfig.CABundle).ToNot(gomega.BeEmpty())
				}
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verify the localQueue can be fetched and mutated", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(localQueue), localQueue)).To(gomega.Succeed())
				localQueue.Spec.StopPolicy = ptr.To(kueue.Hold)
				g.Expect(k8sClient.Update(ctx, localQueue)).Should(gomega.Succeed())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verify the LocalQueue status is updated", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(localQueue), localQueue)).To(gomega.Succeed())
				g.Expect(localQueue.Status.Conditions).To(utiltesting.HaveConditionStatusFalse(kueue.LocalQueueActive))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
