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
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/metrics"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

// Pins that the scheduling equivalence hash follows the effective resources:
// two workloads with an identical raw spec must not share a scheduling
// outcome when a LimitRange change altered the effective resources between
// their admissions. Guards the AdjustResources migration tracked in
// kueue#14964 and the hash-reuse work in kueue#14958.
var _ = ginkgo.Describe("Scheduling hash freshness across LimitRange changes", func() {
	var (
		ns           *corev1.Namespace
		smallFlavor  *kueue.ResourceFlavor
		largeFlavor  *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
		limitRange   *corev1.LimitRange
	)

	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup)

		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "hash-freshness-")
		smallFlavor = utiltestingapi.MakeResourceFlavor("small").Obj()
		util.MustCreate(ctx, k8sClient, smallFlavor)
		largeFlavor = utiltestingapi.MakeResourceFlavor("large").Obj()
		util.MustCreate(ctx, k8sClient, largeFlavor)
		// The small flavor fits only effective requests of up to 2 CPU in
		// total; the large one has room for everything.
		clusterQueue = utiltestingapi.MakeClusterQueue("cq-hash-freshness").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(smallFlavor.Name).Resource(corev1.ResourceCPU, "2").Obj(),
				// Exactly one workload at the initial default: with large full,
				// the stale (3 CPU) view of a second workload fits nowhere, so a
				// racing admission before the LimitRange refresh is impossible —
				// the spec converges regardless of event timing (kueue#15145).
				*utiltestingapi.MakeFlavorQuotas(largeFlavor.Name).Resource(corev1.ResourceCPU, "3").Obj(),
			).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)
		localQueue = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)
		util.ExpectLocalQueuesToBeActive(ctx, k8sClient, localQueue)

		limitRange = utiltesting.MakeLimitRange("limits", ns.Name).
			WithValue("DefaultRequest", corev1.ResourceCPU, "3").Obj()
		util.MustCreate(ctx, k8sClient, limitRange)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, largeFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, smallFlavor, true)
		fwk.StopManager(ctx)
		metrics.InitMetricVectors(nil)
	})

	expectAdmittedOnFlavor := func(wl *kueue.Workload, flavorName string) {
		gomega.Eventually(func(g gomega.Gomega) {
			read := kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read)).To(gomega.Succeed())
			g.Expect(workload.HasQuotaReservation(&read)).To(gomega.BeTrue())
			g.Expect(read.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
			g.Expect(read.Status.Admission.PodSetAssignments[0].Flavors[corev1.ResourceCPU]).To(
				gomega.Equal(kueue.ResourceFlavorReference(flavorName)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	}

	ginkgo.It("places a raw-identical workload by its new effective resources after a defaultRequest change", func() {
		wl1 := utiltestingapi.MakeWorkload("one", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Obj()
		ginkgo.By("admitting the first raw workload on the large flavor (effective 3 CPU)", func() {
			util.MustCreate(ctx, k8sClient, wl1)
			expectAdmittedOnFlavor(wl1, largeFlavor.Name)
		})

		ginkgo.By("lowering the LimitRange defaultRequest to 1 CPU", func() {
			updatedLr := corev1.LimitRange{}
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(limitRange), &updatedLr)).To(gomega.Succeed())
			updatedLr.Spec.Limits[0].DefaultRequest[corev1.ResourceCPU] = resource.MustParse("1")
			gomega.Expect(k8sClient.Update(ctx, &updatedLr)).To(gomega.Succeed())
		})

		ginkgo.By("admitting a raw-identical second workload on the small flavor (effective 1 CPU)", func() {
			// The stale (3 CPU) view fits neither flavor — small is too small
			// and large is full — so the only way to admission is through the
			// refreshed effective resources. If the scheduling equivalence
			// hash were not refreshed alongside them, the second workload
			// would reuse the first one'"'"'s equivalence class against the full
			// large flavor and stay pending.
			wl2 := utiltestingapi.MakeWorkload("two", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Obj()
			util.MustCreate(ctx, k8sClient, wl2)
			expectAdmittedOnFlavor(wl2, smallFlavor.Name)
		})

		ginkgo.By("verifying the first workload keeps its original assignment", func() {
			expectAdmittedOnFlavor(wl1, largeFlavor.Name)
		})
	})
})

// Direct observation of the hash layer, without involving the scheduler
// outcome: two raw-identical pending workloads under different namespace
// LimitRange defaults must carry two distinct scheduling hashes.
var _ = ginkgo.Describe("Pending scheduling hashes under differing LimitRange defaults", func() {
	var (
		nsSmall      *corev1.Namespace
		nsLarge      *corev1.Namespace
		flavor       *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		lqSmall      *kueue.LocalQueue
		lqLarge      *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup)

		nsSmall = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "hash-defaults-small-")
		nsLarge = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "hash-defaults-large-")
		flavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, flavor)
		// Zero quota keeps both workloads pending as inadmissible.
		clusterQueue = utiltestingapi.MakeClusterQueue("cq-hash-defaults").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).
				Resource(corev1.ResourceCPU, "0").Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)
		lqSmall = utiltestingapi.MakeLocalQueue("queue", nsSmall.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, lqSmall)
		lqLarge = utiltestingapi.MakeLocalQueue("queue", nsLarge.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, lqLarge)
		util.ExpectLocalQueuesToBeActive(ctx, k8sClient, lqSmall, lqLarge)

		util.MustCreate(ctx, k8sClient, utiltesting.MakeLimitRange("limits", nsSmall.Name).
			WithValue("DefaultRequest", corev1.ResourceCPU, "1").Obj())
		util.MustCreate(ctx, k8sClient, utiltesting.MakeLimitRange("limits", nsLarge.Name).
			WithValue("DefaultRequest", corev1.ResourceCPU, "3").Obj())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, nsSmall)).To(gomega.Succeed())
		gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, nsLarge)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, nsSmall)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, nsLarge)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		fwk.StopManager(ctx)
		metrics.InitMetricVectors(nil)
	})

	ginkgo.It("reports two distinct hashes for raw-identical workloads under different defaults", func() {
		wlSmall := utiltestingapi.MakeWorkload("one", nsSmall.Name).
			Queue(kueue.LocalQueueName(lqSmall.Name)).
			Obj()
		wlLarge := utiltestingapi.MakeWorkload("two", nsLarge.Name).
			Queue(kueue.LocalQueueName(lqLarge.Name)).
			Obj()
		util.MustCreate(ctx, k8sClient, wlSmall)
		util.MustCreate(ctx, k8sClient, wlLarge)

		// The hashes must follow the effective requests (1 vs 3 CPU), not the
		// identical raw spec.
		util.ExpectPendingSchedulingHashesMetric(clusterQueue, 0, 2)
	})
})
