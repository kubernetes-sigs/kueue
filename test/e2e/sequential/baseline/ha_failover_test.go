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
	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("HA Failover", ginkgo.Label("feature:ha"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		util.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName)
	})

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
		util.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
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
