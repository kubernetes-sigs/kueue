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
var _ = ginkgo.Describe("Scheduling hash freshness across LimitRange changes", ginkgo.Ordered, func() {
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
				*utiltestingapi.MakeFlavorQuotas(largeFlavor.Name).Resource(corev1.ResourceCPU, "10").Obj(),
			).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)
		localQueue = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)
		util.ExpectLocalQueuesToBeActive(ctx, k8sClient, localQueue)

		limitRange = utiltesting.MakeLimitRange("limits", ns.Name).
			WithValue("DefaultRequest", corev1.ResourceCPU, "1").Obj()
		util.MustCreate(ctx, k8sClient, limitRange)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, largeFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, smallFlavor, true)
		fwk.StopManager(ctx)
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
		ginkgo.By("admitting the first raw workload on the small flavor (effective 1 CPU)", func() {
			util.MustCreate(ctx, k8sClient, wl1)
			expectAdmittedOnFlavor(wl1, smallFlavor.Name)
		})

		ginkgo.By("raising the LimitRange defaultRequest to 3 CPU", func() {
			updatedLr := corev1.LimitRange{}
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(limitRange), &updatedLr)).To(gomega.Succeed())
			updatedLr.Spec.Limits[0].DefaultRequest[corev1.ResourceCPU] = resource.MustParse("3")
			gomega.Expect(k8sClient.Update(ctx, &updatedLr)).To(gomega.Succeed())
		})

		ginkgo.By("admitting a raw-identical second workload on the large flavor (effective 3 CPU)", func() {
			// If the scheduling equivalence hash were not refreshed from the
			// new effective resources, the second workload could reuse the
			// first one's assignment and land on the small flavor.
			wl2 := utiltestingapi.MakeWorkload("two", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Obj()
			util.MustCreate(ctx, k8sClient, wl2)
			expectAdmittedOnFlavor(wl2, largeFlavor.Name)
		})

		ginkgo.By("verifying the first workload keeps its original assignment", func() {
			expectAdmittedOnFlavor(wl1, smallFlavor.Name)
		})
	})
})
