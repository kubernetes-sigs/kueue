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
	"sigs.k8s.io/kueue/test/util/behavioral"
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

		// Created by the borrowing spec alone; nil elsewhere.
		bigFlavor     *kueue.ResourceFlavor
		cohort        *kueue.Cohort
		lender        *kueue.ClusterQueue
		borrower      *kueue.ClusterQueue
		borrowerQueue *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup)

		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "restart-accounting-")
		onDemandFlavor = utiltestingapi.MakeResourceFlavor("on-demand").Obj()
		behavioral.MustCreate(ctx, k8sClient, onDemandFlavor)
		runtimeClass = utiltesting.MakeRuntimeClass("kata-restart", "bar-handler").
			PodOverhead(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, runtimeClass)
		clusterQueue = utiltestingapi.MakeClusterQueue("cq-restart").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(onDemandFlavor.Name).
				Resource(corev1.ResourceCPU, "10").Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, clusterQueue)
		behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)
		localQueue = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, localQueue)
		behavioral.ExpectLocalQueuesToBeActive(ctx, k8sClient, localQueue)
		bigFlavor, cohort, lender, borrower, borrowerQueue = nil, nil, nil, nil, nil
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, borrowerQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, borrower, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lender, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cohort, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, bigFlavor, true)
		gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, runtimeClass)).To(gomega.Succeed())
		fwk.StopManager(ctx)
		metrics.InitMetricVectors(nil)
	})

	expectReservation := func(cq *kueue.ClusterQueue, total string) {
		gomega.Eventually(func(g gomega.Gomega) {
			updatedCQ := kueue.ClusterQueue{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCQ)).To(gomega.Succeed())
			g.Expect(updatedCQ.Status.FlavorsReservation).To(gomega.HaveLen(1))
			g.Expect(updatedCQ.Status.FlavorsReservation[0].Resources).To(gomega.HaveLen(1))
			got := updatedCQ.Status.FlavorsReservation[0].Resources[0].Total
			g.Expect(got.Equal(resource.MustParse(total))).To(gomega.BeTrue(), "reservation = %s, want %s", got.String(), total)
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}

	// A single nominal quota is a Quantity, so a ledger past int64 needs two
	// ClusterQueues: the borrower owns seven and borrows MaxInt64 from the lender.
	ginkgo.It("keeps a borrowed total past int64 across a restart, and gives it back exactly", func() {
		const gpu = corev1.ResourceName("example.com/gpu")
		const maxInt64 = "9223372036854775807"

		bigFlavor = utiltestingapi.MakeResourceFlavor("on-demand-big").Obj()
		behavioral.MustCreate(ctx, k8sClient, bigFlavor)

		cohort = utiltestingapi.MakeCohort("restart-cohort").Obj()
		behavioral.MustCreate(ctx, k8sClient, cohort)

		lender = utiltestingapi.MakeClusterQueue("cq-restart-lender").
			Cohort("restart-cohort").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(bigFlavor.Name).
				Resource(gpu, maxInt64).Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, lender)

		borrower = utiltestingapi.MakeClusterQueue("cq-restart-borrower").
			Cohort("restart-cohort").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(bigFlavor.Name).
				Resource(gpu, "7").Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, borrower)
		behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, lender, borrower)

		borrowerQueue = utiltestingapi.MakeLocalQueue("queue-borrower", ns.Name).ClusterQueue(borrower.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, borrowerQueue)
		behavioral.ExpectLocalQueuesToBeActive(ctx, k8sClient, borrowerQueue)

		big := utiltestingapi.MakeWorkload("borrowed-max", ns.Name).
			Queue(kueue.LocalQueueName(borrowerQueue.Name)).Request(gpu, maxInt64).Obj()
		small := utiltestingapi.MakeWorkload("borrowed-seven", ns.Name).
			Queue(kueue.LocalQueueName(borrowerQueue.Name)).Request(gpu, "7").Obj()

		ginkgo.By("admitting both, so the ledger holds MaxInt64+7", func() {
			behavioral.MustCreate(ctx, k8sClient, big)
			behavioral.MustCreate(ctx, k8sClient, small)
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, big, small)
			// MaxInt64+7 is past what a Quantity carries, so the status is
			// capped; the removal below reads back what the accounting held.
			expectReservation(borrower, maxInt64)
		})

		ginkgo.By("restarting the manager", func() {
			fwk.StopManager(ctx)
			fwk.StartManager(ctx, cfg, managerAndSchedulerSetup)
		})

		ginkgo.By("finding the rebuilt total unchanged", func() {
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, big, small)
			expectReservation(borrower, maxInt64)
		})

		ginkgo.By("removing the larger reservation and finding the smaller one intact", func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, big, true)
			expectReservation(borrower, "7")
		})
	})

	ginkgo.It("keeps the effective-resource accounting after a restart", func() {
		wl := utiltestingapi.MakeWorkload("adjusted", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Limit(corev1.ResourceCPU, "3").
			RuntimeClass(runtimeClass.Name).
			Obj()

		ginkgo.By("admitting a workload whose accounting depends on adjustments", func() {
			behavioral.MustCreate(ctx, k8sClient, wl)
			gomega.Eventually(func(g gomega.Gomega) {
				read := kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&read)).To(gomega.BeTrue())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			// 3 CPU copied from limits + 2 CPU RuntimeClass overhead.
			expectReservation(clusterQueue, "5")
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
			}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
			expectReservation(clusterQueue, "5")
		})

		ginkgo.By("verifying the rebuilt books gate new admissions correctly", func() {
			// 10 - 5 = 5 free; a raw 6 CPU workload must stay pending.
			wl2 := utiltestingapi.MakeWorkload("too-big", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Request(corev1.ResourceCPU, "6").
				Obj()
			behavioral.MustCreate(ctx, k8sClient, wl2)
			gomega.Consistently(func(g gomega.Gomega) {
				read := kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), &read)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&read)).To(gomega.BeFalse())
			}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
		})
	})
})
