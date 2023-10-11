/*
Copyright 2023 The Kubernetes Authors.

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
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

const (
	lowPriority int32 = iota - 1
	midPriority
	highPriority
)

var _ = ginkgo.Describe("Preemption", func() {
	var (
		alphaFlavor *kueue.ResourceFlavor
		ns          *corev1.Namespace
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "preemption-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
		alphaFlavor = testing.MakeResourceFlavor("alpha").Obj()
		gomega.Expect(k8sClient.Create(ctx, alphaFlavor)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, alphaFlavor, true)
	})

	ginkgo.Context("In a single ClusterQueue", func() {
		var (
			cq *kueue.ClusterQueue
			q  *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			cq = testing.MakeClusterQueue("cq").
				ResourceGroup(*testing.MakeFlavorQuotas("alpha").Resource(corev1.ResourceCPU, "4").Obj()).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerOrNewerEqualPriority,
				}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())

			q = testing.MakeLocalQueue("q", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, q)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, true)
		})

		ginkgo.It("Should preempt Workloads with lower priority when there is not enough quota", func() {
			ginkgo.By("Creating pod templates with different priorities")
			lowPt1 := testing.MakePodTemplate("main-1", ns.Name).
				Labels(map[string]string{constants.WorkloadNameLabel: "low-wl-1"}).
				Request(corev1.ResourceCPU, "1").
				Priority(lowPriority).
				Obj()
			lowPt2 := testing.MakePodTemplate("main-2", ns.Name).
				Labels(map[string]string{constants.WorkloadNameLabel: "low-wl-2"}).
				Request(corev1.ResourceCPU, "1").
				Priority(lowPriority).
				Obj()
			midPt := testing.MakePodTemplate("main-mid", ns.Name).
				Labels(map[string]string{constants.WorkloadNameLabel: "mid-wl"}).
				Request(corev1.ResourceCPU, "1").
				Priority(midPriority).
				Obj()
			highPt := testing.MakePodTemplate("main-high", ns.Name).
				Labels(map[string]string{constants.WorkloadNameLabel: "high-wl-1"}).
				Request(corev1.ResourceCPU, "1").
				Priority(highPriority).
				Obj()

			gomega.Expect(k8sClient.Create(ctx, lowPt1)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, lowPt2)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, midPt)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, highPt)).To(gomega.Succeed())

			ginkgo.By("Creating initial Workloads with different priorities")
			lowWl1 := testing.MakeWorkload("low-wl-1", ns.Name).
				SetPodSetName(lowPt1.Name).
				SetPodTemplateName(lowPt1.Name).
				Priority(lowPriority).
				Queue(q.Name).
				Obj()
			lowWl2 := testing.MakeWorkload("low-wl-2", ns.Name).
				SetPodSetName(lowPt2.Name).
				SetPodTemplateName(lowPt2.Name).
				Priority(lowPriority).
				Queue(q.Name).
				Obj()
			midWl := testing.MakeWorkload("mid-wl", ns.Name).
				SetPodSetName(midPt.Name).
				SetPodTemplateName(midPt.Name).
				Priority(midPriority).
				Queue(q.Name).
				Obj()
			highWl1 := testing.MakeWorkload("high-wl-1", ns.Name).
				SetPodSetName(highPt.Name).
				SetPodTemplateName(highPt.Name).
				Priority(highPriority).
				Queue(q.Name).
				Obj()

			gomega.Expect(k8sClient.Create(ctx, lowWl1)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, lowWl2)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, midWl)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, highWl1)).To(gomega.Succeed())

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, lowWl1, lowWl2, midWl, highWl1)

			ginkgo.By("Creating a low priority pod template")
			lowPt3 := testing.MakePodTemplate("low-main-3", ns.Name).
				Labels(map[string]string{constants.WorkloadNameLabel: "low-wl-3"}).
				Request(corev1.ResourceCPU, "1").
				Priority(lowPriority).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, lowPt3)).To(gomega.Succeed())

			lowWl3 := testing.MakeWorkload("low-wl-3", ns.Name).
				SetPodSetName(lowPt3.Name).
				SetPodTemplateName(lowPt3.Name).
				Priority(lowPriority).
				Queue(q.Name).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, lowWl3)).To(gomega.Succeed())

			util.ExpectWorkloadsToBePending(ctx, k8sClient, lowWl3)

			ginkgo.By("Creating a high priority Workload")
			highPt2 := testing.MakePodTemplate("high-pt-2", ns.Name).
				Labels(map[string]string{constants.WorkloadNameLabel: "high-wl-2"}).
				Request(corev1.ResourceCPU, "2").
				Priority(highPriority).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, highPt2)).To(gomega.Succeed())

			highWl2 := testing.MakeWorkload("high-wl-2", ns.Name).
				SetPodSetName(highPt2.Name).
				SetPodTemplateName(highPt2.Name).
				Priority(highPriority).
				Queue(q.Name).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, highWl2)).To(gomega.Succeed())

			util.FinishEvictionForWorkloads(ctx, k8sClient, lowWl1, lowWl2)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, highWl2)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, lowWl1, lowWl2)
		})

		ginkgo.It("Should preempt newer Workloads with the same priority when there is not enough quota", func() {
			ginkgo.By("Creating pod templates")
			pt1 := testing.MakePodTemplate("main-1", ns.Name).
				Labels(map[string]string{constants.WorkloadNameLabel: "wl-1"}).
				Request(corev1.ResourceCPU, "1").
				Obj()
			pt2 := testing.MakePodTemplate("main-2", ns.Name).
				Labels(map[string]string{constants.WorkloadNameLabel: "wl-2"}).
				Request(corev1.ResourceCPU, "1").
				Obj()
			pt3 := testing.MakePodTemplate("main-3", ns.Name).
				Labels(map[string]string{constants.WorkloadNameLabel: "wl-3"}).
				Request(corev1.ResourceCPU, "3").
				Obj()

			ginkgo.By("Creating initial Workloads")
			wl1 := testing.MakeWorkload("wl-1", ns.Name).
				SetPodSetName(pt1.Name).
				SetPodTemplateName(pt1.Name).
				Priority(lowPriority).
				Queue(q.Name).
				Obj()
			wl2 := testing.MakeWorkload("wl-2", ns.Name).
				SetPodSetName(pt2.Name).
				SetPodTemplateName(pt2.Name).
				Priority(lowPriority).
				Queue(q.Name).
				Obj()
			wl3 := testing.MakeWorkload("wl-3", ns.Name).
				SetPodSetName(pt3.Name).
				SetPodTemplateName(pt3.Name).
				Priority(lowPriority).
				Queue(q.Name).
				Obj()

			gomega.Expect(k8sClient.Create(ctx, pt1)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, pt2)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, wl1)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, wl2)).To(gomega.Succeed())
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, wl1, wl2)

			gomega.Expect(k8sClient.Create(ctx, pt3)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, wl3)).To(gomega.Succeed())
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl3)

			ginkgo.By("Waiting one second, to ensure that the new workload has a later creation time")
			time.Sleep(time.Second)

			ginkgo.By("Creating a new PodTemplate")
			pt4 := testing.MakePodTemplate("main-4", ns.Name).
				Labels(map[string]string{constants.WorkloadNameLabel: "wl-4"}).
				Request(corev1.ResourceCPU, "1").
				Priority(lowPriority).
				Obj()

			ginkgo.By("Creating a new Workload")
			wl4 := testing.MakeWorkload("wl-4", ns.Name).
				SetPodSetName(pt4.Name).
				SetPodTemplateName(pt4.Name).
				Priority(lowPriority).
				Queue(q.Name).
				Obj()

			gomega.Expect(k8sClient.Create(ctx, pt4)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, wl4)).To(gomega.Succeed())

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, wl1, wl2, wl4)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl3)

			ginkgo.By("Finishing the first workload")
			util.FinishWorkloads(ctx, k8sClient, wl1)

			ginkgo.By("Finishing eviction for wl4")
			util.FinishEvictionForWorkloads(ctx, k8sClient, wl4)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, wl3)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl4)
		})
	})

	ginkgo.Context("In a ClusterQueue that is part of a cohort", func() {
		var (
			alphaCQ, betaCQ, gammaCQ *kueue.ClusterQueue
			alphaQ, betaQ, gammaQ    *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			alphaCQ = testing.MakeClusterQueue("alpha-cq").
				Cohort("all").
				ResourceGroup(*testing.MakeFlavorQuotas("alpha").Resource(corev1.ResourceCPU, "2").Obj()).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, alphaCQ)).To(gomega.Succeed())
			alphaQ = testing.MakeLocalQueue("alpha-q", ns.Name).ClusterQueue(alphaCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, alphaQ)).To(gomega.Succeed())

			betaCQ = testing.MakeClusterQueue("beta-cq").
				Cohort("all").
				ResourceGroup(*testing.MakeFlavorQuotas("alpha").Resource(corev1.ResourceCPU, "2").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, betaCQ)).To(gomega.Succeed())
			betaQ = testing.MakeLocalQueue("beta-q", ns.Name).ClusterQueue(betaCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, betaQ)).To(gomega.Succeed())

			gammaCQ = testing.MakeClusterQueue("gamma-cq").
				Cohort("all").
				ResourceGroup(*testing.MakeFlavorQuotas("alpha").Resource(corev1.ResourceCPU, "2").Obj()).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, gammaCQ)).To(gomega.Succeed())
			gammaQ = testing.MakeLocalQueue("gamma-q", ns.Name).ClusterQueue(gammaCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, gammaQ)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, alphaCQ, true)
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, betaCQ, true)
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, gammaCQ, true)
		})

		ginkgo.It("Should preempt Workloads in the cohort borrowing quota, when the ClusterQueue is using less than nominal quota", func() {
			ginkgo.By("Creating workloads in beta-cq that borrow quota")

			alphaLowPt := testing.MakePodTemplate("alpha-low", ns.Name).
				Labels(map[string]string{constants.WorkloadNameLabel: "alpha-low"}).
				Request(corev1.ResourceCPU, "1").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, alphaLowPt)).To(gomega.Succeed())

			alphaLowWl := testing.MakeWorkload("alpha-low", ns.Name).
				SetPodSetName(alphaLowPt.Name).
				SetPodTemplateName(alphaLowPt.Name).
				Queue(alphaQ.Name).
				Priority(lowPriority).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, alphaLowWl)).To(gomega.Succeed())

			betaMidPt := testing.MakePodTemplate("beta-mid", ns.Name).
				Labels(map[string]string{constants.WorkloadNameLabel: "beta-mid"}).
				Request(corev1.ResourceCPU, "1").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, betaMidPt)).To(gomega.Succeed())

			betaMidWl := testing.MakeWorkload("beta-mid", ns.Name).
				SetPodSetName(betaMidPt.Name).
				SetPodTemplateName(betaMidPt.Name).
				Queue(betaQ.Name).
				Priority(midPriority).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, betaMidWl)).To(gomega.Succeed())

			betaHighPt := testing.MakePodTemplate("beta-high", ns.Name).
				Labels(map[string]string{constants.WorkloadNameLabel: "beta-high"}).
				Request(corev1.ResourceCPU, "4").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, betaHighPt)).To(gomega.Succeed())

			betaHighWl := testing.MakeWorkload("beta-high", ns.Name).
				SetPodSetName(betaHighPt.Name).
				SetPodTemplateName(betaHighPt.Name).
				Queue(betaQ.Name).
				Priority(highPriority).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, betaHighWl)).To(gomega.Succeed())

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, alphaCQ.Name, alphaLowWl)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, betaCQ.Name, betaMidWl, betaHighWl)

			ginkgo.By("Creating workload in alpha-cq to preempt workloads in both ClusterQueues")
			alphaMidPt := testing.MakePodTemplate("alpha-mid", ns.Name).
				Labels(map[string]string{constants.WorkloadNameLabel: "alpha-mid"}).
				Request(corev1.ResourceCPU, "2").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, alphaMidPt)).To(gomega.Succeed())

			alphaMidWl := testing.MakeWorkload("alpha-mid", ns.Name).
				SetPodSetName(alphaMidPt.Name).
				SetPodTemplateName(alphaMidPt.Name).
				Queue(alphaQ.Name).
				Priority(midPriority).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, alphaMidWl)).To(gomega.Succeed())

			util.FinishEvictionForWorkloads(ctx, k8sClient, alphaLowWl, betaMidWl)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, alphaCQ.Name, alphaMidWl)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, alphaLowWl, betaMidWl)
		})

		ginkgo.It("Should not preempt Workloads in the cohort, if the ClusterQueue requires borrowing", func() {
			ginkgo.By("Creating workloads in beta-cq that borrow quota")
			alphaHighPt1 := testing.MakePodTemplate("alpha-high-1", ns.Name).
				Labels(map[string]string{constants.WorkloadNameLabel: "alpha-high-1"}).
				Request(corev1.ResourceCPU, "2").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, alphaHighPt1)).To(gomega.Succeed())

			alphaHighWl1 := testing.MakeWorkload("alpha-high-1", ns.Name).
				SetPodTemplateName(alphaHighPt1.Name).
				Queue(alphaQ.Name).
				Priority(highPriority).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, alphaHighWl1)).To(gomega.Succeed())

			betaLowPt1 := testing.MakePodTemplate("beta-low", ns.Name).
				Labels(map[string]string{constants.WorkloadNameLabel: "beta-low"}).
				Request(corev1.ResourceCPU, "4").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, betaLowPt1)).To(gomega.Succeed())

			betaLowWl := testing.MakeWorkload("beta-low", ns.Name).
				SetPodTemplateName(betaLowPt1.Name).
				Queue(betaQ.Name).
				Priority(lowPriority).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, betaLowWl)).To(gomega.Succeed())

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, alphaCQ.Name, alphaHighWl1)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, betaCQ.Name, betaLowWl)

			ginkgo.By("Creating high priority workload in alpha-cq that doesn't fit without borrowing")
			alphaHighPt2 := testing.MakePodTemplate("alpha-high-2", ns.Name).
				Labels(map[string]string{constants.WorkloadNameLabel: "alpha-high-2"}).
				Request(corev1.ResourceCPU, "2").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, alphaHighPt2)).To(gomega.Succeed())

			alphaHighWl2 := testing.MakeWorkload("alpha-high-2", ns.Name).
				SetPodTemplateName(alphaHighPt2.Name).
				Queue(alphaQ.Name).
				Priority(highPriority).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, alphaHighWl2)).To(gomega.Succeed())

			// No preemptions happen.
			util.ExpectWorkloadsToBePending(ctx, k8sClient, alphaHighWl2)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, alphaCQ.Name, alphaHighWl1)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, betaCQ.Name, betaLowWl)
		})

		ginkgo.It("Should preempt all necessary workloads in concurrent scheduling", func() {
			ginkgo.By("Creating workloads in beta-cq that borrow quota")
			betaMidPt := testing.MakePodTemplate("beta-mid", ns.Name).
				Labels(map[string]string{constants.WorkloadNameLabel: "beta-mid"}).
				Request(corev1.ResourceCPU, "3").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, betaMidPt)).To(gomega.Succeed())

			betaMidWl := testing.MakeWorkload("beta-mid", ns.Name).
				SetPodSetName(betaMidPt.Name).
				SetPodTemplateName(betaMidPt.Name).
				Queue(betaQ.Name).
				Priority(midPriority).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, betaMidWl)).To(gomega.Succeed())

			betaHighPt := testing.MakePodTemplate("beta-high", ns.Name).
				Labels(map[string]string{constants.WorkloadNameLabel: "beta-high"}).
				Request(corev1.ResourceCPU, "3").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, betaHighPt)).To(gomega.Succeed())

			betaHighWl := testing.MakeWorkload("beta-high", ns.Name).
				SetPodSetName(betaHighPt.Name).
				SetPodTemplateName(betaHighPt.Name).
				Queue(betaQ.Name).
				Priority(highPriority).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, betaHighWl)).To(gomega.Succeed())

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, betaCQ.Name, betaMidWl, betaHighWl)

			ginkgo.By("Creating workload in alpha-cq and gamma-cq that need to preempt")
			alphaMidPt := testing.MakePodTemplate("alpha-mid", ns.Name).
				Request(corev1.ResourceCPU, "2").
				Obj()
			alphaMidPt.SetLabels(map[string]string{constants.WorkloadNameLabel: "alpha-mid"})
			gomega.Expect(k8sClient.Create(ctx, alphaMidPt)).To(gomega.Succeed())

			alphaMidWl := testing.MakeWorkload("alpha-mid", ns.Name).
				SetPodSetName(alphaMidPt.Name).
				SetPodTemplateName(alphaMidPt.Name).
				Queue(alphaQ.Name).
				Priority(midPriority).
				Obj()

			gammaMidPt := testing.MakePodTemplate("gamma-mid", ns.Name).
				Labels(map[string]string{constants.WorkloadNameLabel: "gamma-mid"}).
				Request(corev1.ResourceCPU, "2").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, gammaMidPt)).To(gomega.Succeed())

			gammaMidWl := testing.MakeWorkload("gamma-mid", ns.Name).
				SetPodSetName(gammaMidPt.Name).
				SetPodTemplateName(gammaMidPt.Name).
				Queue(gammaQ.Name).
				Priority(midPriority).
				Obj()

			gomega.Expect(k8sClient.Create(ctx, alphaMidWl)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, gammaMidWl)).To(gomega.Succeed())

			// since the two pending workloads are not aware of each other both of them
			// will request the eviction of betaMidWl only
			util.FinishEvictionForWorkloads(ctx, k8sClient, betaMidWl)

			// one of alpha-mid and gamma-mid should be admitted
			gomega.Eventually(func() []*kueue.Workload { return util.FilterAdmittedWorkloads(ctx, k8sClient, alphaMidWl, gammaMidWl) }, util.Interval*4, util.Interval).Should(gomega.HaveLen(1))

			// betaHighWl remains admitted
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, betaCQ.Name, betaHighWl)

			// the last one should request the preemption of betaHighWl
			util.FinishEvictionForWorkloads(ctx, k8sClient, betaHighWl)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, alphaCQ.Name, alphaMidWl)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, gammaCQ.Name, gammaMidWl)
		})
	})

})
