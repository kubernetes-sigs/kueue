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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

// The scheduler waits for the cache event handlers of the controllers that
// run before the first cycle. With TopologyAwareScheduling off no Topology
// controller runs, as in this suite, so a leftover Topology must not hold
// that wait.
var _ = ginkgo.Describe("Scheduler startup with a leftover Topology and TopologyAwareScheduling off", ginkgo.Ordered, func() {
	var (
		ns           *corev1.Namespace
		topology     *kueue.Topology
		flavor       *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TopologyAwareScheduling, false)
		topology = utiltestingapi.MakeTopology("startup-leftover").Levels(corev1.LabelHostname).Obj()
		behavioral.MustCreate(ctx, k8sClient, topology)
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup)

		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "scheduler-startup-")
		flavor = utiltestingapi.MakeResourceFlavor("startup-default").Obj()
		behavioral.MustCreate(ctx, k8sClient, flavor)
		clusterQueue = utiltestingapi.MakeClusterQueue("cq-startup").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).
				Resource(corev1.ResourceCPU, "4").Obj()).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)
		localQueue = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterAll(func() {
		gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		fwk.StopManager(ctx)
		metrics.InitMetricVectors(nil)
	})

	ginkgo.It("admits a plain workload before and after a manager restart", func() {
		ginkgo.By("admitting a workload on the first start")
		first := utiltestingapi.MakeWorkload("first", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(corev1.ResourceCPU, "1").
			Obj()
		behavioral.MustCreate(ctx, k8sClient, first)
		behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, clusterQueue.Name, first)

		ginkgo.By("restarting the manager")
		fwk.StopManager(ctx)
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup)

		ginkgo.By("admitting a workload after the restart")
		second := utiltestingapi.MakeWorkload("second", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(corev1.ResourceCPU, "1").
			Obj()
		behavioral.MustCreate(ctx, k8sClient, second)
		behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, clusterQueue.Name, first, second)
	})
})
