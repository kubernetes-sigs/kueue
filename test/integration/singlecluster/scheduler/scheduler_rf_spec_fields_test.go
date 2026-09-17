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
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Scheduler non-TAS ResourceFlavor tolerations", ginkgo.Ordered, func() {
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
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "rf-tolerations-")

		flavor = utiltestingapi.MakeResourceFlavor("tolerations-flavor").
			Taint(taint).
			Obj()
		util.MustCreate(ctx, k8sClient, flavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).
				Resource(corev1.ResourceCPU, "5").
				Obj()).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).
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

	makeWorkload := func(name, cpu string) *kueue.Workload {
		return utiltestingapi.MakeWorkload(name, ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(corev1.ResourceCPU, cpu).
			Obj()
	}

	addFlavorToleration := func() {
		gomega.Eventually(func(g gomega.Gomega) {
			var updatedFlavor kueue.ResourceFlavor
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(flavor), &updatedFlavor)).To(gomega.Succeed())
			updatedFlavor.Spec.Tolerations = []corev1.Toleration{{
				Key:      taint.Key,
				Operator: corev1.TolerationOpEqual,
				Value:    taint.Value,
				Effect:   taint.Effect,
			}}
			g.Expect(k8sClient.Update(ctx, &updatedFlavor)).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	}

	// The second workload's shape is the only difference between the scenarios:
	// a differently-shaped one proves the scheduler cache saw the flavor update,
	// while an identically-shaped one additionally exercises the
	// scheduling-equivalence-hash freeze that would otherwise block resubmissions.
	ginkgo.DescribeTable("should retry pending workloads after the flavor tolerates its own taint",
		func(secondWorkloadCPU string) {
			wl1 := makeWorkload("wl1", "1")
			ginkgo.By("creating a workload that cannot tolerate the flavor nodeTaints", func() {
				util.MustCreate(ctx, k8sClient, wl1)
				util.ExpectWorkloadsToBePending(ctx, k8sClient, wl1)
			})

			ginkgo.By("adding a matching toleration to the flavor spec", addFlavorToleration)

			ginkgo.By("verifying the pending workload is retried and admitted", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
			})

			ginkgo.By("verifying a workload created after the update is admitted", func() {
				wl2 := makeWorkload("wl2", secondWorkloadCPU)
				util.MustCreate(ctx, k8sClient, wl2)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
			})
		},
		ginkgo.Entry("with a differently-shaped new workload", "500m"),
		ginkgo.Entry("with an identically-shaped new workload", "1"),
	)
})

var _ = ginkgo.Describe("Scheduler non-TAS ResourceFlavor nodeLabels", ginkgo.Ordered, func() {
	var (
		ns           *corev1.Namespace
		flavor       *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
	)

	const zoneKey = "example.com/zone"

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "rf-nodelabels-")

		flavor = utiltestingapi.MakeResourceFlavor("nodelabels-flavor").
			NodeLabel(zoneKey, "zone-a").
			Obj()
		util.MustCreate(ctx, k8sClient, flavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).
				Resource(corev1.ResourceCPU, "5").
				Obj()).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).
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

	makeWorkload := func(name, cpu string) *kueue.Workload {
		return utiltestingapi.MakeWorkload(name, ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(corev1.ResourceCPU, cpu).
			NodeSelector(map[string]string{zoneKey: "zone-b"}).
			Obj()
	}

	setFlavorZone := func(zone string) {
		gomega.Eventually(func(g gomega.Gomega) {
			var updatedFlavor kueue.ResourceFlavor
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(flavor), &updatedFlavor)).To(gomega.Succeed())
			updatedFlavor.Spec.NodeLabels = map[string]string{zoneKey: zone}
			g.Expect(k8sClient.Update(ctx, &updatedFlavor)).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	}

	// The second workload's shape is the only difference between the scenarios:
	// a differently-shaped one proves the scheduler cache saw the flavor update,
	// while an identically-shaped one additionally exercises the
	// scheduling-equivalence-hash freeze that would otherwise block resubmissions.
	ginkgo.DescribeTable("should retry pending workloads after nodeLabels match their nodeSelector",
		func(secondWorkloadCPU string) {
			wl1 := makeWorkload("wl1", "1")
			ginkgo.By("creating a workload whose nodeSelector does not match the flavor nodeLabels", func() {
				util.MustCreate(ctx, k8sClient, wl1)
				util.ExpectWorkloadsToBePending(ctx, k8sClient, wl1)
			})

			ginkgo.By("updating the flavor nodeLabels to match the workload nodeSelector", func() {
				setFlavorZone("zone-b")
			})

			ginkgo.By("verifying the pending workload is retried and admitted", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
			})

			ginkgo.By("verifying a workload created after the update is admitted", func() {
				wl2 := makeWorkload("wl2", secondWorkloadCPU)
				util.MustCreate(ctx, k8sClient, wl2)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
			})
		},
		ginkgo.Entry("with a differently-shaped new workload", "500m"),
		ginkgo.Entry("with an identically-shaped new workload", "1"),
	)
})
