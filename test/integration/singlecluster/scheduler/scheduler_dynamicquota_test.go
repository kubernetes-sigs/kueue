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

package scheduler

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Scheduler DynamicQuotaOrchestration", ginkgo.Ordered, func() {
	var (
		ns           *corev1.Namespace
		flavor       *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DynamicQuotaOrchestration, true)

		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "dynquota-")

		flavor = utiltestingapi.MakeResourceFlavor("dynquota-flavor").Obj()
		util.MustCreate(ctx, k8sClient, flavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("dynquota-cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).
				Resource(corev1.ResourceCPU, "1").
				Obj()).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("dynquota-lq", ns.Name).
			ClusterQueue(clusterQueue.Name).
			Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
		gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("should admit pending workloads when EffectiveQuotas status is updated with quota", func() {
		wl := utiltestingapi.MakeWorkload("wl1", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(corev1.ResourceCPU, "2").
			Obj()

		ginkgo.By("creating a workload when spec quota is 1 CPU (requesting 2 CPU)", func() {
			util.MustCreate(ctx, k8sClient, wl)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
		})

		ginkgo.By("updating ClusterQueue status with EffectiveQuotas providing 5 CPU", func() {
			updateCQEffectiveQuotas(ctx, k8sClient, clusterQueue, flavor, "5", "dqo-test")
		})

		ginkgo.By("verifying the pending workload is scheduled and admitted", func() {
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
		})

		ginkgo.By("creating a second workload that fits within remaining EffectiveQuotas", func() {
			wl2 := utiltestingapi.MakeWorkload("wl2", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Request(corev1.ResourceCPU, "2").
				Obj()
			util.MustCreate(ctx, k8sClient, wl2)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
		})

		ginkgo.By("creating a third workload that exceeds remaining EffectiveQuotas", func() {
			wl3 := utiltestingapi.MakeWorkload("wl3", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Request(corev1.ResourceCPU, "2").
				Obj()
			util.MustCreate(ctx, k8sClient, wl3)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl3)
		})
	})

	ginkgo.It("should fallback to spec when EffectiveQuotas status is cleared", func() {
		ginkgo.By("populating EffectiveQuotas to allow initial admission", func() {
			updateCQEffectiveQuotas(ctx, k8sClient, clusterQueue, flavor, "5", "dqo-test")

			wl1 := utiltestingapi.MakeWorkload("wl1", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Request(corev1.ResourceCPU, "2").
				Obj()
			util.MustCreate(ctx, k8sClient, wl1)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
		})

		ginkgo.By("clearing EffectiveQuotas in status", func() {
			updateCQEffectiveQuotas(ctx, k8sClient, clusterQueue, flavor, "", "")
		})

		ginkgo.By("verifying subsequent workload falls back to spec and stays pending", func() {
			wl2 := utiltestingapi.MakeWorkload("wl2", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Request(corev1.ResourceCPU, "2").
				Obj()
			util.MustCreate(ctx, k8sClient, wl2)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
		})
	})

	ginkgo.It("should ignore EffectiveQuotas status when DynamicQuotaOrchestration feature gate is disabled", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DynamicQuotaOrchestration, false)

		updateCQEffectiveQuotas(ctx, k8sClient, clusterQueue, flavor, "5", "dqo-test")

		wl := utiltestingapi.MakeWorkload("wl-fg-disabled", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(corev1.ResourceCPU, "2").
			Obj()
		util.MustCreate(ctx, k8sClient, wl)
		util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
	})

	ginkgo.It("should adjust scheduling capacity dynamically when EffectiveQuotas is reduced", func() {
		ginkgo.By("setting initial EffectiveQuotas of 10 CPU", func() {
			updateCQEffectiveQuotas(ctx, k8sClient, clusterQueue, flavor, "10", "dqo-test")
		})

		wl1 := utiltestingapi.MakeWorkload("wl1-reduction", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(corev1.ResourceCPU, "4").
			Obj()
		util.MustCreate(ctx, k8sClient, wl1)
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)

		ginkgo.By("reducing EffectiveQuotas to 5 CPU while wl1 is running", func() {
			updateCQEffectiveQuotas(ctx, k8sClient, clusterQueue, flavor, "5", "dqo-test")
		})

		ginkgo.By("verifying running workload wl1 remains admitted", func() {
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
		})

		ginkgo.By("verifying a new workload fitting within remaining reduced capacity is admitted", func() {
			wlFitting := utiltestingapi.MakeWorkload("wl-fitting", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Request(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, wlFitting)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlFitting)
		})

		ginkgo.By("verifying a new workload exceeding reduced EffectiveQuotas stays pending", func() {
			wl2 := utiltestingapi.MakeWorkload("wl2-reduction", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Request(corev1.ResourceCPU, "2").
				Obj()
			util.MustCreate(ctx, k8sClient, wl2)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
		})
	})
})

func updateCQEffectiveQuotas(ctx context.Context, k8sClient client.Client, cq *kueue.ClusterQueue, flavor *kueue.ResourceFlavor, cpuQty, orchestratorName string) {
	gomega.Eventually(func(g gomega.Gomega) {
		var currentCQ kueue.ClusterQueue
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &currentCQ)).To(gomega.Succeed())
		currentCQ.Status.EffectiveQuotas = makeEffectiveQuotaStatus(flavor, cpuQty, orchestratorName)
		g.Expect(k8sClient.Status().Update(ctx, &currentCQ)).To(gomega.Succeed())
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
}

func makeEffectiveQuotaStatus(flavor *kueue.ResourceFlavor, cpuQty, orchestratorName string) *kueue.EffectiveQuotaStatus {
	if cpuQty == "" {
		return nil
	}
	return &kueue.EffectiveQuotaStatus{
		OrchestratorRef: kueue.EffectiveQuotaStatusOrchestratorRef{
			APIGroup: "kueue.x-k8s.io",
			Kind:     "DynamicQuotaOrchestrator",
			Name:     orchestratorName,
		},
		ResourceGroups: []kueue.ResourceGroup{
			utiltestingapi.ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(flavor.Name).
					Resource(corev1.ResourceCPU, cpuQty).
					Obj(),
			),
		},
	}
}
