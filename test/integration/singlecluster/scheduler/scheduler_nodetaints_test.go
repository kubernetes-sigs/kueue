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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Scheduler non-TAS ResourceFlavor nodeTaints", ginkgo.Ordered, func() {
	var (
		ns           *corev1.Namespace
		flavor       *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
	)

	taint := corev1.Taint{
		Key:    "example.com/dedicated",
		Value:  "reserved",
		Effect: corev1.TaintEffectNoSchedule,
	}

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "nodetaints-")

		flavor = utiltestingapi.MakeResourceFlavor("tainted-flavor").
			Taint(taint).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, flavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).
				Resource(corev1.ResourceCPU, "5").
				Obj()).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).
			ClusterQueue(clusterQueue.Name).
			Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
		gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	makeWorkload := func(name, cpu string) *kueue.Workload {
		return utiltestingapi.MakeWorkload(name, ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(corev1.ResourceCPU, cpu).
			Obj()
	}

	removeNodeTaints := func() {
		gomega.Eventually(func(g gomega.Gomega) {
			var updatedFlavor kueue.ResourceFlavor
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(flavor), &updatedFlavor)).To(gomega.Succeed())
			updatedFlavor.Spec.NodeTaints = nil
			g.Expect(k8sClient.Update(ctx, &updatedFlavor)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}

	// The second workload's shape is the only difference between the scenarios:
	// a differently-shaped one proves the scheduler cache saw the flavor update,
	// while an identically-shaped one additionally exercises the
	// scheduling-equivalence-hash freeze that would otherwise block resubmissions.
	ginkgo.DescribeTable("should retry pending workloads after nodeTaints are removed",
		func(secondWorkloadCPU string) {
			wl1 := makeWorkload("wl1", "1")
			ginkgo.By("creating a workload that cannot tolerate the flavor nodeTaints", func() {
				behavioral.MustCreate(ctx, k8sClient, wl1)
				behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl1)
			})

			ginkgo.By("removing the flavor nodeTaints", removeNodeTaints)

			ginkgo.By("verifying a workload created after the update is admitted", func() {
				wl2 := makeWorkload("wl2", secondWorkloadCPU)
				behavioral.MustCreate(ctx, k8sClient, wl2)
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
			})

			ginkgo.By("verifying the pending workload is retried and admitted", func() {
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
			})
		},
		ginkgo.Entry("with a differently-shaped new workload", "500m"),
		ginkgo.Entry("with an identically-shaped new workload", "1"),
	)
})
