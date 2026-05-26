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

package inadmissible

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Scheduler", func() {
	var (
		ns             *corev1.Namespace
		onDemandFlavor *kueue.ResourceFlavor
	)

	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup)

		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "equivalence-")
		onDemandFlavor = utiltestingapi.MakeResourceFlavor("on-demand").Obj()
		util.MustCreate(ctx, k8sClient, onDemandFlavor)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		fwk.StopManager(ctx)
	})

	ginkgo.When("Using scheduling equivalence classes", func() {
		var cq *kueue.ClusterQueue

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteObject(ctx, k8sClient, cq)).To(gomega.Succeed())
		})

		ginkgo.It("Should admit a fitting workload behind identical no-fit workloads in BestEffortFIFO", func() {
			cq = utiltestingapi.MakeClusterQueue("equiv-cq").
				QueueingStrategy(kueue.BestEffortFIFO).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "2").Obj(),
				).Obj()
			util.MustCreate(ctx, k8sClient, cq)

			queue := utiltestingapi.MakeLocalQueue("equiv-queue", ns.Name).ClusterQueue("equiv-cq").Obj()
			util.MustCreate(ctx, k8sClient, queue)

			ginkgo.By("creating all workloads at once: 10 identical no-fit + 1 fitting")
			for i := range 10 {
				util.MustCreate(ctx, k8sClient, utiltestingapi.MakeWorkload(fmt.Sprintf("nofit-%d", i), ns.Name).
					Queue(kueue.LocalQueueName(queue.Name)).
					Request(corev1.ResourceCPU, "10").Obj())
			}
			fitWl := utiltestingapi.MakeWorkload("fits", ns.Name).
				Queue(kueue.LocalQueueName(queue.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, fitWl)

			ginkgo.By("verifying all no-fit workloads become inadmissible via bulk-move")
			util.ExpectPendingWorkloadsMetric(cq, 0, 10)

			ginkgo.By("verifying the fitting workload gets admitted")
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, fitWl)
		})

		ginkgo.It("Should not hash workloads that don't fit due to namespace mismatch", func() {
			cq = utiltestingapi.MakeClusterQueue("ns-mismatch-cq").
				QueueingStrategy(kueue.BestEffortFIFO).
				NamespaceSelector(&metav1.LabelSelector{
					MatchLabels: map[string]string{"match": "true"},
				}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
				).Obj()
			util.MustCreate(ctx, k8sClient, cq)

			queue := utiltestingapi.MakeLocalQueue("ns-queue", ns.Name).ClusterQueue("ns-mismatch-cq").Obj()
			util.MustCreate(ctx, k8sClient, queue)

			matchingNs := util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "matching-")
			defer func() {
				gomega.Expect(util.DeleteNamespace(ctx, k8sClient, matchingNs)).To(gomega.Succeed())
			}()
			matchingNs.Labels = map[string]string{"match": "true"}
			gomega.Expect(k8sClient.Update(ctx, matchingNs)).To(gomega.Succeed())
			matchQueue := utiltestingapi.MakeLocalQueue("match-queue", matchingNs.Name).ClusterQueue("ns-mismatch-cq").Obj()
			util.MustCreate(ctx, k8sClient, matchQueue)

			ginkgo.By("creating workloads from non-matching namespace (should be inadmissible but not hashed)")
			for i := range 5 {
				util.MustCreate(ctx, k8sClient, utiltestingapi.MakeWorkload(fmt.Sprintf("nomatch-%d", i), ns.Name).
					Queue(kueue.LocalQueueName(queue.Name)).
					Request(corev1.ResourceCPU, "1").Obj())
			}

			ginkgo.By("creating a fitting workload from matching namespace")
			fitWl := utiltestingapi.MakeWorkload("matching-fit", matchingNs.Name).
				Queue(kueue.LocalQueueName(matchQueue.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, fitWl)

			ginkgo.By("verifying the matching workload gets admitted despite non-matching ones")
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, fitWl)
		})

		// Validates correctness of PreemptionNoCandidates path, not the optimization.
		// Hash mechanism tested in TestRequeueHashTriggerByReason.
		ginkgo.It("Should bulk-move equivalent workloads that need preemption but have no candidates", func() {
			cq = utiltestingapi.MakeClusterQueue("preempt-nocandidate-cq").
				QueueingStrategy(kueue.BestEffortFIFO).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "2").Obj(),
				).Obj()
			util.MustCreate(ctx, k8sClient, cq)

			queue := utiltestingapi.MakeLocalQueue("preempt-queue", ns.Name).ClusterQueue("preempt-nocandidate-cq").Obj()
			util.MustCreate(ctx, k8sClient, queue)

			ginkgo.By("admitting a workload to fill the quota")
			blocker := utiltestingapi.MakeWorkload("blocker", ns.Name).
				Queue(kueue.LocalQueueName(queue.Name)).
				Request(corev1.ResourceCPU, "2").Obj()
			util.MustCreate(ctx, k8sClient, blocker)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, blocker)

			ginkgo.By("creating identical workloads that need preemption but have no lower-priority targets")
			for i := range 10 {
				util.MustCreate(ctx, k8sClient, utiltestingapi.MakeWorkload(fmt.Sprintf("blocked-%d", i), ns.Name).
					Queue(kueue.LocalQueueName(queue.Name)).
					Request(corev1.ResourceCPU, "1").Obj())
			}

			ginkgo.By("verifying equivalent blocked workloads are bulk-moved to inadmissible")
			util.ExpectPendingWorkloadsMetric(cq, 0, 10)
		})
	})
})
