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

package fairsharing

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

// The shape of issue #9345: refill-rich is already borrowing 6 of refill-poor's
// share, leaving 2 CPU free. Fair sharing gives the first to poor; without
// refill the cycle considers only one head per ClusterQueue, so the second goes
// to rich and poor's successor waits. The cohort is full afterwards, so the
// loser stays pending and the difference is visible end to end.
//
// This pins the outcome, not the mechanism: refilled entries bypassing the
// tournament would look the same, since here the fair and the queue-jumping
// winner coincide. The per-cycle mechanics are unit-tested in pkg/scheduler.
var _ = ginkgo.Describe("Scheduler with fair sharing refill", ginkgo.Label("feature:fairsharing"), func() {
	var (
		ns            *corev1.Namespace
		defaultFlavor *kueue.ResourceFlavor
		poorCQ        *kueue.ClusterQueue
		richCQ        *kueue.ClusterQueue
		poorLQ        *kueue.LocalQueue
		richLQ        *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.FairSharingRefill, true)
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(nil))

		defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, defaultFlavor)
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "refill-")

		poorCQ = utiltestingapi.MakeClusterQueue("refill-poor").
			Cohort("refill").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "8", "0").Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, poorCQ)
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, poorCQ)

		richCQ = utiltestingapi.MakeClusterQueue("refill-rich").
			Cohort("refill").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "2", "8").Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, richCQ)
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, richCQ)

		poorLQ = utiltestingapi.MakeLocalQueue("poor-lq", ns.Name).ClusterQueue("refill-poor").Obj()
		util.MustCreate(ctx, k8sClient, poorLQ)
		richLQ = utiltestingapi.MakeLocalQueue("rich-lq", ns.Name).ClusterQueue("refill-rich").Obj()
		util.MustCreate(ctx, k8sClient, richLQ)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, poorCQ, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, richCQ, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
		fwk.StopManager(ctx)
	})

	ginkgo.It("gives the freed capacity to the poorest ClusterQueue instead of an over-share sibling", func() {
		// Fill the cohort first so no contender can be admitted on arrival;
		// otherwise they land across different cycles, each head wins its own
		// cycle uncontested, and poor drains its backlog either way. They wait
		// in inadmissible rather than the heaps, since poor-a's NoFit requeue
		// sweeps its equivalence-hash peers along with it. Releasing capacity
		// flushes them back for the cycle that has to see both heads.
		var richFill, richSpare *kueue.Workload
		ginkgo.By("letting refill-rich borrow the whole cohort", func() {
			richFill = utiltestingapi.MakeWorkload("rich-fill", ns.Name).
				Queue("rich-lq").Request(corev1.ResourceCPU, "8").Obj()
			util.MustCreate(ctx, k8sClient, richFill)
			richSpare = utiltestingapi.MakeWorkload("rich-spare", ns.Name).
				Queue("rich-lq").Request(corev1.ResourceCPU, "2").Obj()
			util.MustCreate(ctx, k8sClient, richSpare)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, richFill, richSpare)
		})

		var poorA, poorB, richPending *kueue.Workload
		ginkgo.By("queueing two workloads behind the poor queue's head and one behind rich's", func() {
			poorA = utiltestingapi.MakeWorkload("poor-a", ns.Name).
				Queue("poor-lq").Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, poorA)
			poorB = utiltestingapi.MakeWorkload("poor-b", ns.Name).
				Queue("poor-lq").Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, poorB)
			richPending = utiltestingapi.MakeWorkload("rich-pending", ns.Name).
				Queue("rich-lq").Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, richPending)
			// Only the heads carry a pending condition; poor-b was never
			// nominated, which is the point.
			util.ExpectWorkloadsToBePending(ctx, k8sClient, poorA, richPending)
			// Parked, not queued: no active heap has anything to pop yet.
			util.ExpectPendingWorkloadsMetric(poorCQ, 0, 2)
			util.ExpectPendingWorkloadsMetric(richCQ, 0, 1)
		})

		// Finishing models how capacity is normally given back; a delete would
		// work too. What matters is that it happens in one step.
		ginkgo.By("freeing exactly two CPU in a single step", func() {
			util.FinishWorkloads(ctx, k8sClient, richSpare)
		})

		// Poor takes the first CPU either way; the second is refill's.
		ginkgo.By("admitting both of the poor queue's workloads", func() {
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, poorA, poorB)
		})

		ginkgo.By("leaving the over-share sibling's workload pending", func() {
			util.ExpectWorkloadsToBePending(ctx, k8sClient, richPending)
			// Asserted inline: the util helpers poll on their own, so nesting
			// one here would retry the violation away.
			gomega.Consistently(func(g gomega.Gomega) {
				var wl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(richPending), &wl)).To(gomega.Succeed())
				g.Expect(wl.Status.Admission).To(gomega.BeNil())
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})
	})
})
