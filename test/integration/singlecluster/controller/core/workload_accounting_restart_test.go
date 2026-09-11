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
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/metrics"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

// Pins that the effective-resource accounting (limits copied into missing
// requests, RuntimeClass overhead) survives a manager restart, when every
// workload's in-memory state is rebuilt from the raw objects. Guards the
// AdjustResources migration tracked in kueue#14964.
var _ = ginkgo.Describe("Workload accounting across a manager restart", func() {
	var (
		ns             *corev1.Namespace
		onDemandFlavor *kueue.ResourceFlavor
		runtimeClass   *nodev1.RuntimeClass
		clusterQueue   *kueue.ClusterQueue
		localQueue     *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup)

		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "restart-accounting-")
		onDemandFlavor = utiltestingapi.MakeResourceFlavor("on-demand").Obj()
		util.MustCreate(ctx, k8sClient, onDemandFlavor)
		runtimeClass = utiltesting.MakeRuntimeClass("kata-restart", "bar-handler").
			PodOverhead(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}).
			Obj()
		util.MustCreate(ctx, k8sClient, runtimeClass)
		clusterQueue = utiltestingapi.MakeClusterQueue("cq-restart").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(onDemandFlavor.Name).
				Resource(corev1.ResourceCPU, "10").Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)
		localQueue = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)
		util.ExpectLocalQueuesToBeActive(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		gomega.Expect(util.DeleteObject(ctx, k8sClient, runtimeClass)).To(gomega.Succeed())
		fwk.StopManager(ctx)
		metrics.InitMetricVectors(nil)
	})

	expectReservedCPU := func(total string) {
		gomega.Eventually(func(g gomega.Gomega) {
			updatedCQ := kueue.ClusterQueue{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCQ)).To(gomega.Succeed())
			g.Expect(updatedCQ.Status.FlavorsReservation).To(gomega.HaveLen(1))
			g.Expect(updatedCQ.Status.FlavorsReservation[0].Resources).To(gomega.HaveLen(1))
			g.Expect(updatedCQ.Status.FlavorsReservation[0].Resources[0].Total.Equal(resource.MustParse(total))).To(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	}

	ginkgo.It("keeps the effective-resource accounting after a restart", func() {
		wl := utiltestingapi.MakeWorkload("adjusted", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Limit(corev1.ResourceCPU, "3").
			RuntimeClass(runtimeClass.Name).
			Obj()

		ginkgo.By("admitting a workload whose accounting depends on adjustments", func() {
			util.MustCreate(ctx, k8sClient, wl)
			gomega.Eventually(func(g gomega.Gomega) {
				read := kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&read)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			// 3 CPU copied from limits + 2 CPU RuntimeClass overhead.
			expectReservedCPU("5")
		})

		ginkgo.By("restarting the manager", func() {
			fwk.StopManager(ctx)
			fwk.StartManager(ctx, cfg, managerAndSchedulerSetup)
		})

		ginkgo.By("verifying the workload stays admitted with unchanged accounting", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				read := kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&read)).To(gomega.BeTrue())
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
			expectReservedCPU("5")
		})

		ginkgo.By("verifying the rebuilt books gate new admissions correctly", func() {
			// 10 - 5 = 5 free; a raw 6 CPU workload must stay pending.
			wl2 := utiltestingapi.MakeWorkload("too-big", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Request(corev1.ResourceCPU, "6").
				Obj()
			util.MustCreate(ctx, k8sClient, wl2)
			gomega.Consistently(func(g gomega.Gomega) {
				read := kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), &read)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&read)).To(gomega.BeFalse())
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})
	})
})
