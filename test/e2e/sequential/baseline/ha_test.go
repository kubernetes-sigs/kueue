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

// Tests in this file require 2 Kueue replicas for leader failover checks.
var _ = ginkgo.Describe("HA tests", ginkgo.Label("feature:ha", util.Shard0), ginkgo.Serial, ginkgo.Ordered, func() {
	var (
		originalReplicas    int32
		originalReplicasSet bool
	)

	ginkgo.BeforeAll(func() {
		util.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName)
		originalReplicas = kueueControllerManagerReplicas()
		originalReplicasSet = true
		scaleKueueControllerManager(2)
	})

	ginkgo.AfterAll(func() {
		if originalReplicasSet {
			scaleKueueControllerManager(originalReplicas)
		}
	})

	ginkgo.Context("Cert bootstrap with replica recovery", func() {
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
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, clusterQueue, true, util.MediumTimeout)
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, onDemandFlavor, true, util.MediumTimeout)
		})

		ginkgo.It("Should allow to bootstrap certificates", func() {
			var oldReplicas int32
			deployment := &appsv1.Deployment{}
			localQueueCRD := &apiextensionsv1.CustomResourceDefinition{}
			key := types.NamespacedName{Namespace: kueueNS, Name: "kueue-controller-manager"}
			ginkgo.By("scale down replicas to 0", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, key, deployment)).To(gomega.Succeed())
					oldReplicas = max(oldReplicas, ptr.Deref(deployment.Spec.Replicas, int32(0)))
					deployment.Spec.Replicas = ptr.To[int32](0)
					g.Expect(k8sClient.Update(ctx, deployment)).Should(gomega.Succeed())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("await for no replicas", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, key, deployment)).To(gomega.Succeed())
					g.Expect(deployment.Status.Replicas).To(gomega.Equal(int32(0)))
					g.Expect(ptr.Deref(deployment.Status.TerminatingReplicas, 0)).To(gomega.Equal(int32(0)))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
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
					deployment.Spec.Replicas = &oldReplicas
					g.Expect(k8sClient.Update(ctx, deployment)).Should(gomega.Succeed())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("await for ready replicas", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, key, deployment)).To(gomega.Succeed())
					g.Expect(deployment.Status.ReadyReplicas).To(gomega.Equal(oldReplicas))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("await for Kueue to be available", func() {
				util.WaitForKueueAvailability(ctx, k8sClient)
			})

			ginkgo.By("verify the caBundle is set again for CRD", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, localQueueCRDKey, localQueueCRD)).To(gomega.Succeed())
					caBundle := localQueueCRD.Spec.Conversion.Webhook.ClientConfig.CABundle
					g.Expect(caBundle).NotTo(gomega.BeEmpty())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the caBundle is set again for mutating webhooks", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					mwc := &admissionregistrationv1.MutatingWebhookConfiguration{}
					g.Expect(k8sClient.Get(ctx, mvcKey, mwc)).To(gomega.Succeed())
					for _, webhook := range mwc.Webhooks {
						g.Expect(webhook.ClientConfig.CABundle).ToNot(gomega.BeEmpty())
					}
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the localQueue can be fetched and mutated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(localQueue), localQueue)).To(gomega.Succeed())
					localQueue.Spec.StopPolicy = ptr.To(kueue.Hold)
					g.Expect(k8sClient.Update(ctx, localQueue)).Should(gomega.Succeed())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the LocalQueue status is updated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(localQueue), localQueue)).To(gomega.Succeed())
					g.Expect(localQueue.Status.Conditions).To(utiltesting.HaveConditionStatusFalse(kueue.LocalQueueActive))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.Context("HA Failover", func() {
		var (
			ns *corev1.Namespace
			rf *kueue.ResourceFlavor
			cq *kueue.ClusterQueue
			lq *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "ha-failover-")

			rf = utiltestingapi.MakeResourceFlavor("rf").Obj()
			util.MustCreate(ctx, k8sClient, rf)

			cq = utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(rf.Name).
						Resource(corev1.ResourceCPU, "1").
						Obj(),
				).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
			util.WaitForKueueAvailabilityNoRestartCountCheck(ctx, k8sClient)
		})

		ginkgo.It("should admit a workload after leader failover when a previously admitted workload was deleted", func() {
			ginkgo.By("admitting workload-a so it enters both leader and follower caches")
			wl1 := utiltestingapi.MakeWorkload("workload-1", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, wl1)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)

			ginkgo.By("deleting workload-1")
			util.ExpectObjectToBeDeleted(ctx, k8sClient, wl1, true)

			ginkgo.By("forcing a leader failover so the former follower becomes the new leader")
			util.ForceLeaderFailover(ctx, k8sClient)

			ginkgo.By("creating workload-2 and expecting the new leader to admit it")
			wl2 := utiltestingapi.MakeWorkload("workload-2", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, wl2)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
		})

		ginkgo.It("should admit a high-priority workload after lower-priority workload-1 removal and leader failover with preemption of existing lower-priority workload-2", func() {
			ginkgo.By("admitting low priority wl1 and wl2 so they enter both leader and follower caches")
			wl1 := utiltestingapi.MakeWorkload("workload-1", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "500m").
				Priority(100).
				Obj()
			util.MustCreate(ctx, k8sClient, wl1)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)

			wl2 := utiltestingapi.MakeWorkload("workload-2", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "500m").
				Priority(100).
				Obj()
			util.MustCreate(ctx, k8sClient, wl2)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)

			ginkgo.By("deleting workload-1")
			util.ExpectObjectToBeDeleted(ctx, k8sClient, wl1, true)

			ginkgo.By("forcing a leader failover so the former follower becomes the new leader")
			util.ForceLeaderFailover(ctx, k8sClient)

			ginkgo.By("creating workload-3")
			wl3 := utiltestingapi.MakeWorkload("workload-3", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").
				Priority(200).
				Obj()
			util.MustCreate(ctx, k8sClient, wl3)

			ginkgo.By("expecting wl2 to be preempted")
			util.ExpectWorkloadsToBePreempted(ctx, k8sClient, wl2)
			util.FinishEvictionForWorkloads(ctx, k8sClient, wl2)

			ginkgo.By("expecting wl3 to be admitted")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl3)
		})
	})
})

func kueueControllerManagerReplicas() int32 {
	ginkgo.GinkgoHelper()
	key := types.NamespacedName{Namespace: kueueNS, Name: "kueue-controller-manager"}
	deployment := &appsv1.Deployment{}
	gomega.Expect(k8sClient.Get(ctx, key, deployment)).To(gomega.Succeed())
	return ptr.Deref(deployment.Spec.Replicas, int32(1))
}

func scaleKueueControllerManager(replicas int32) {
	ginkgo.GinkgoHelper()
	key := types.NamespacedName{Namespace: kueueNS, Name: "kueue-controller-manager"}
	if kueueControllerManagerReplicas() == replicas {
		return
	}

	ginkgo.By("scaling kueue-controller-manager", func() {
		util.UpdateDeploymentAndWaitForProgressing(ctx, k8sClient, key, kindClusterName, func(deployment *appsv1.Deployment) {
			deployment.Spec.Replicas = &replicas
		})
		util.WaitForKueueAvailabilityNoRestartCountCheck(ctx, k8sClient)
	})
}
