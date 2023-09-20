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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
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
			cq            *kueue.ClusterQueue
			q             *kueue.LocalQueue
			checkAnytime  *kueue.AdmissionCheck
			checkOnDemand *kueue.AdmissionCheck
		)

		ginkgo.BeforeEach(func() {
			checkAnytime = testing.MakeAdmissionCheck("anytime").Policy(kueue.Anytime).Obj()
			gomega.Expect(k8sClient.Create(ctx, checkAnytime)).To(gomega.Succeed())

			checkOnDemand = testing.MakeAdmissionCheck("on-demand").Policy(kueue.AfterCheckPassedOrOnDemand).Obj()
			gomega.Expect(k8sClient.Create(ctx, checkOnDemand)).To(gomega.Succeed())

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
			util.ExpectAdmissionCheckToBeDeleted(ctx, k8sClient, checkOnDemand, true)
			util.ExpectAdmissionCheckToBeDeleted(ctx, k8sClient, checkAnytime, true)
		})

		ginkgo.It("Should preempt Workloads with lower priority when there is not enough quota", func() {
			ginkgo.By("Creating initial Workloads with different priorities")
			lowWl1 := testing.MakeWorkload("low-wl-1", ns.Name).
				Queue(q.Name).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			lowWl2 := testing.MakeWorkload("low-wl-2", ns.Name).
				Queue(q.Name).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			midWl := testing.MakeWorkload("mid-wl", ns.Name).
				Queue(q.Name).
				Priority(midPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			highWl1 := testing.MakeWorkload("high-wl-1", ns.Name).
				Queue(q.Name).
				Priority(highPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, lowWl1)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, lowWl2)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, midWl)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, highWl1)).To(gomega.Succeed())

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, lowWl1, lowWl2, midWl, highWl1)

			ginkgo.By("Creating a low priority Workload")
			lowWl3 := testing.MakeWorkload("low-wl-3", ns.Name).
				Queue(q.Name).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, lowWl3)).To(gomega.Succeed())

			util.ExpectWorkloadsToBePending(ctx, k8sClient, lowWl3)

			ginkgo.By("Creating a high priority Workload")
			highWl2 := testing.MakeWorkload("high-wl-2", ns.Name).
				Queue(q.Name).
				Priority(highPriority).
				Request(corev1.ResourceCPU, "2").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, highWl2)).To(gomega.Succeed())

			util.FinishEvictionForWorkloads(ctx, k8sClient, lowWl1, lowWl2)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, highWl2)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, lowWl1, lowWl2)
		})

		ginkgo.It("Should preempt newer Workloads with the same priority when there is not enough quota", func() {
			ginkgo.By("Creating initial Workloads")
			wl1 := testing.MakeWorkload("wl-1", ns.Name).
				Queue(q.Name).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			wl2 := testing.MakeWorkload("wl-2", ns.Name).
				Queue(q.Name).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			wl3 := testing.MakeWorkload("wl-3", ns.Name).
				Queue(q.Name).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "3").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, wl2)).To(gomega.Succeed())
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, wl1, wl2)

			gomega.Expect(k8sClient.Create(ctx, wl3)).To(gomega.Succeed())
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl3)

			ginkgo.By("Waiting one second, to ensure that the new workload has a later creation time")
			time.Sleep(time.Second)

			ginkgo.By("Creating a new Workload")
			wl4 := testing.MakeWorkload("wl-4", ns.Name).
				Queue(q.Name).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
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

		ginkgo.It("Should preempt immediately when anytime check is used", func() {
			ginkgo.By("Adding the check to the ClusterQueue", func() {
				cqKey := client.ObjectKeyFromObject(cq)
				gomega.Eventually(func() error {
					updatedQueue := &kueue.ClusterQueue{}
					err := k8sClient.Get(ctx, cqKey, updatedQueue)
					if err != nil {
						return err
					}
					updatedQueue.Spec.AdmissionChecks = []string{checkAnytime.Name}
					return k8sClient.Update(ctx, updatedQueue)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			lowWl := testing.MakeWorkload("low-wl", ns.Name).
				Queue(q.Name).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "4").
				Obj()

			ginkgo.By("Creating the first workload wait its admission", func() {
				gomega.Expect(k8sClient.Create(ctx, lowWl)).To(gomega.Succeed())
				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, lowWl)
				util.SetWorkloadsAdmissionCkeck(ctx, k8sClient, lowWl, checkAnytime.Name, kueue.CheckStateReady)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, cq.Name, lowWl)
			})

			highWl := testing.MakeWorkload("high-wl", ns.Name).
				Queue(q.Name).
				Priority(highPriority).
				Request(corev1.ResourceCPU, "4").
				Obj()
			ginkgo.By("Creating a high priority Workload", func() {
				gomega.Expect(k8sClient.Create(ctx, highWl)).To(gomega.Succeed())
				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, highWl)
				util.FinishEvictionForWorkloads(ctx, k8sClient, lowWl)
				util.SetWorkloadsAdmissionCkeck(ctx, k8sClient, highWl, checkAnytime.Name, kueue.CheckStateReady)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, cq.Name, highWl)
			})
		})

		ginkgo.It("Should not preempt immediately when on-demand check is used, after succeed", func() {
			ginkgo.By("Adding the check to the ClusterQueue", func() {
				cqKey := client.ObjectKeyFromObject(cq)
				gomega.Eventually(func() error {
					updatedQueue := &kueue.ClusterQueue{}
					err := k8sClient.Get(ctx, cqKey, updatedQueue)
					if err != nil {
						return err
					}
					updatedQueue.Spec.AdmissionChecks = []string{checkOnDemand.Name}
					return k8sClient.Update(ctx, updatedQueue)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			lowWl := testing.MakeWorkload("low-wl", ns.Name).
				Queue(q.Name).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "4").
				Obj()

			ginkgo.By("Creating the first workload wait its admission", func() {
				gomega.Expect(k8sClient.Create(ctx, lowWl)).To(gomega.Succeed())
				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, lowWl)
				util.SetWorkloadsAdmissionCkeck(ctx, k8sClient, lowWl, checkOnDemand.Name, kueue.CheckStateReady)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, cq.Name, lowWl)
			})

			highWl := testing.MakeWorkload("high-wl", ns.Name).
				Queue(q.Name).
				Priority(highPriority).
				Request(corev1.ResourceCPU, "4").
				Obj()
			ginkgo.By("Creating a high priority Workload", func() {
				gomega.Expect(k8sClient.Create(ctx, highWl)).To(gomega.Succeed())
				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, highWl)
				gomega.Consistently(func() bool {
					readWl := &kueue.Workload{}
					err := k8sClient.Get(ctx, client.ObjectKeyFromObject(lowWl), readWl)
					if err != nil {
						return false
					}
					return apimeta.IsStatusConditionTrue(readWl.Status.Conditions, kueue.WorkloadAdmitted)

				}, util.ConsistentDuration, util.Interval).Should(gomega.BeTrue())
				util.SetWorkloadsAdmissionCkeck(ctx, k8sClient, highWl, checkOnDemand.Name, kueue.CheckStateReady)
				util.FinishEvictionForWorkloads(ctx, k8sClient, lowWl)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, cq.Name, highWl)
			})
		})

		ginkgo.It("Should not preempt immediately when on-demand check is used, after demand", func() {
			ginkgo.By("Adding the check to the ClusterQueue", func() {
				cqKey := client.ObjectKeyFromObject(cq)
				gomega.Eventually(func() error {
					updatedQueue := &kueue.ClusterQueue{}
					err := k8sClient.Get(ctx, cqKey, updatedQueue)
					if err != nil {
						return err
					}
					updatedQueue.Spec.AdmissionChecks = []string{checkOnDemand.Name}
					return k8sClient.Update(ctx, updatedQueue)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			lowWl := testing.MakeWorkload("low-wl", ns.Name).
				Queue(q.Name).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "4").
				Obj()

			ginkgo.By("Creating the first workload wait its admission", func() {
				gomega.Expect(k8sClient.Create(ctx, lowWl)).To(gomega.Succeed())
				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, lowWl)
				util.SetWorkloadsAdmissionCkeck(ctx, k8sClient, lowWl, checkOnDemand.Name, kueue.CheckStateReady)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, cq.Name, lowWl)
			})

			highWl := testing.MakeWorkload("high-wl", ns.Name).
				Queue(q.Name).
				Priority(highPriority).
				Request(corev1.ResourceCPU, "4").
				Obj()

			ginkgo.By("Creating a high priority Workload", func() {
				gomega.Expect(k8sClient.Create(ctx, highWl)).To(gomega.Succeed())
				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, highWl)
			})

			ginkgo.By("Checking that the low priority workload stays admitted", func() {
				gomega.Consistently(func() bool {
					readWl := &kueue.Workload{}
					err := k8sClient.Get(ctx, client.ObjectKeyFromObject(lowWl), readWl)
					if err != nil {
						return false
					}
					return apimeta.IsStatusConditionTrue(readWl.Status.Conditions, kueue.WorkloadAdmitted)

				}, util.ConsistentDuration, util.Interval).Should(gomega.BeTrue())
			})

			ginkgo.By("Checking that the low priority workload is evicted after the check of the high one changes", func() {
				util.SetWorkloadsAdmissionCkeck(ctx, k8sClient, highWl, checkOnDemand.Name, kueue.CheckStatePreemptionRequired)
				util.FinishEvictionForWorkloads(ctx, k8sClient, lowWl)
				// the admission will be happen later on
			})
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

			alphaLowWl := testing.MakeWorkload("alpha-low", ns.Name).
				Queue(alphaQ.Name).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, alphaLowWl)).To(gomega.Succeed())

			betaMidWl := testing.MakeWorkload("beta-mid", ns.Name).
				Queue(betaQ.Name).
				Priority(midPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, betaMidWl)).To(gomega.Succeed())
			betaHighWl := testing.MakeWorkload("beta-high", ns.Name).
				Queue(betaQ.Name).
				Priority(highPriority).
				Request(corev1.ResourceCPU, "4").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, betaHighWl)).To(gomega.Succeed())

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, alphaCQ.Name, alphaLowWl)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, betaCQ.Name, betaMidWl, betaHighWl)

			ginkgo.By("Creating workload in alpha-cq to preempt workloads in both ClusterQueues")
			alphaMidWl := testing.MakeWorkload("alpha-mid", ns.Name).
				Queue(alphaQ.Name).
				Priority(midPriority).
				Request(corev1.ResourceCPU, "2").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, alphaMidWl)).To(gomega.Succeed())

			util.FinishEvictionForWorkloads(ctx, k8sClient, alphaLowWl, betaMidWl)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, alphaCQ.Name, alphaMidWl)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, alphaLowWl, betaMidWl)
		})

		ginkgo.It("Should not preempt Workloads in the cohort, if the ClusterQueue requires borrowing", func() {
			ginkgo.By("Creating workloads in beta-cq that borrow quota")

			alphaHighWl1 := testing.MakeWorkload("alpha-high-1", ns.Name).
				Queue(alphaQ.Name).
				Priority(highPriority).
				Request(corev1.ResourceCPU, "2").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, alphaHighWl1)).To(gomega.Succeed())
			betaLowWl := testing.MakeWorkload("beta-low", ns.Name).
				Queue(betaQ.Name).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "4").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, betaLowWl)).To(gomega.Succeed())

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, alphaCQ.Name, alphaHighWl1)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, betaCQ.Name, betaLowWl)

			ginkgo.By("Creating high priority workload in alpha-cq that doesn't fit without borrowing")
			alphaHighWl2 := testing.MakeWorkload("alpha-high-2", ns.Name).
				Queue(alphaQ.Name).
				Priority(highPriority).
				Request(corev1.ResourceCPU, "2").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, alphaHighWl2)).To(gomega.Succeed())

			// No preemptions happen.
			util.ExpectWorkloadsToBePending(ctx, k8sClient, alphaHighWl2)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, alphaCQ.Name, alphaHighWl1)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, betaCQ.Name, betaLowWl)
		})

		ginkgo.It("Should preempt all necessary workloads in concurrent scheduling", func() {
			ginkgo.By("Creating workloads in beta-cq that borrow quota")

			betaMidWl := testing.MakeWorkload("beta-mid", ns.Name).
				Queue(betaQ.Name).
				Priority(midPriority).
				Request(corev1.ResourceCPU, "3").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, betaMidWl)).To(gomega.Succeed())
			betaHighWl := testing.MakeWorkload("beta-high", ns.Name).
				Queue(betaQ.Name).
				Priority(highPriority).
				Request(corev1.ResourceCPU, "3").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, betaHighWl)).To(gomega.Succeed())

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, betaCQ.Name, betaMidWl, betaHighWl)

			ginkgo.By("Creating workload in alpha-cq and gamma-cq that need to preempt")
			alphaMidWl := testing.MakeWorkload("alpha-mid", ns.Name).
				Queue(alphaQ.Name).
				Priority(midPriority).
				Request(corev1.ResourceCPU, "2").
				Obj()

			gammaMidWl := testing.MakeWorkload("gamma-mid", ns.Name).
				Queue(gammaQ.Name).
				Priority(midPriority).
				Request(corev1.ResourceCPU, "2").
				Obj()

			gomega.Expect(k8sClient.Create(ctx, alphaMidWl)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, gammaMidWl)).To(gomega.Succeed())

			util.FinishEvictionForWorkloads(ctx, k8sClient, betaMidWl, betaHighWl)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, alphaCQ.Name, alphaMidWl)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, gammaCQ.Name, gammaMidWl)
		})
	})

})
