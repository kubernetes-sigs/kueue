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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Scheduler with the ReclaimOnly FairSharing preemption strategy", ginkgo.Label("feature:fairsharing"), func() {
	var (
		defaultFlavor *kueue.ResourceFlavor
		ns            *corev1.Namespace

		cohorts []*kueue.Cohort
		cqs     []*kueue.ClusterQueue
		lqs     []*kueue.LocalQueue
		wls     []*kueue.Workload
	)

	var createCohort = func(cohort *kueue.Cohort) *kueue.Cohort {
		behavioral.MustCreate(ctx, k8sClient, cohort)
		cohorts = append(cohorts, cohort)
		return cohort
	}

	var createQueue = func(name, cohort, quota string) *kueue.ClusterQueue {
		cq := utiltestingapi.MakeClusterQueue(name).
			Cohort(kueue.CohortReference(cohort)).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, quota).Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, cq)
		behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)
		cqs = append(cqs, cq)

		lq := utiltestingapi.MakeLocalQueue(cq.Name, ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, lq)
		behavioral.ExpectLocalQueuesToBeActive(ctx, k8sClient, lq)
		lqs = append(lqs, lq)
		return cq
	}

	var createWorkload = func(queue string, cpuRequests string) *kueue.Workload {
		wl := utiltestingapi.MakeWorkloadWithGeneratedName("workload-", ns.Name).
			Queue(kueue.LocalQueueName(queue)).
			Request(corev1.ResourceCPU, cpuRequests).Obj()
		wls = append(wls, wl)
		behavioral.MustCreate(ctx, k8sClient, wl)
		return wl
	}

	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetupWithFairSharing(&config.FairSharing{
			PreemptionStrategies: []config.PreemptionStrategy{config.ReclaimOnly},
		}, nil))
		defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		behavioral.MustCreate(ctx, k8sClient, defaultFlavor)

		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")
	})

	ginkgo.AfterEach(func() {
		for _, wl := range wls {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, wl, true)
		}
		for _, lq := range lqs {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
		}
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		for _, cq := range cqs {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		}
		for _, cohort := range cohorts {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cohort, true)
		}
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
		fwk.StopManager(ctx)
		wls, lqs, cqs, cohorts = nil, nil, nil, nil
	})

	ginkgo.When("using hierarchical cohorts", func() {
		// root
		// ├── cohort-c   (no quota)
		// │   ├── a      10 CPU
		// │   └── s       0 CPU
		// └── t           0 CPU
		ginkgo.BeforeEach(func() {
			createCohort(utiltestingapi.MakeCohort("root").Obj())
			createCohort(utiltestingapi.MakeCohort("cohort-c").Parent("root").Obj())
			createQueue("a", "cohort-c", "10")
			createQueue("s", "cohort-c", "0")
			createQueue("t", "root", "0")
		})

		ginkgo.It("should reclaim the nominal quota from the farthest ClusterQueue first, then from the closer one", func() {
			ginkgo.By("Admitting workloads that borrow the quota of a")
			wlS := createWorkload("s", "4")
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlS)
			wlT := createWorkload("t", "6")
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlT)

			ginkgo.By("Creating a workload in a that fits once t alone is reclaimed")
			wlA1 := createWorkload("a", "6")

			ginkgo.By("Only the workload in the farthest ClusterQueue t should be preempted")
			behavioral.ExpectWorkloadsToBePreempted(ctx, k8sClient, wlT)
			behavioral.FinishEvictionForWorkloads(ctx, k8sClient, wlT)
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlA1)
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlS)
			behavioral.ExpectPreemptedWorkloadsTotalMetric("a", kueue.InCohortReclamationReason, 1)

			ginkgo.By("Creating a workload in a that needs the rest of its nominal quota")
			wlA2 := createWorkload("a", "4")

			ginkgo.By("The workload in the closer ClusterQueue s should be preempted next")
			behavioral.ExpectWorkloadsToBePreempted(ctx, k8sClient, wlS)
			behavioral.FinishEvictionForWorkloads(ctx, k8sClient, wlS)
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlA1, wlA2)
			behavioral.ExpectPreemptedWorkloadsTotalMetric("a", kueue.InCohortReclamationReason, 2)
		})
	})

	ginkgo.When("the preemptor would borrow", func() {
		ginkgo.BeforeEach(func() {
			createCohort(utiltestingapi.MakeCohort("all").Obj())
			createQueue("a", "all", "5")
			createQueue("b", "all", "5")
		})

		ginkgo.It("should not preempt to balance shares", func() {
			ginkgo.By("Admitting workloads in a that borrow from b")
			wlA1 := createWorkload("a", "4")
			wlA2 := createWorkload("a", "4")
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlA1, wlA2)

			ginkgo.By("Creating a workload in b that would make b borrow")
			wlB := createWorkload("b", "6")
			behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wlB)

			gomega.Consistently(func(g gomega.Gomega) {
				for _, wl := range []*kueue.Workload{wlA1, wlA2} {
					got := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), got)).To(gomega.Succeed())
					g.Expect(workload.HasQuotaReservation(got)).To(gomega.BeTrue())
					g.Expect(apimeta.IsStatusConditionTrue(got.Status.Conditions, kueue.WorkloadEvicted)).To(gomega.BeFalse())
				}
			}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.It("should reclaim when the preemptor stays within its nominal quota", func() {
			ginkgo.By("Admitting workloads in a that borrow from b")
			wlA1 := createWorkload("a", "4")
			wlA2 := createWorkload("a", "4")
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlA1, wlA2)

			ginkgo.By("Creating a workload in b within its nominal quota")
			wlB := createWorkload("b", "5")

			ginkgo.By("One workload of a should be preempted")
			var evicted []*kueue.Workload
			gomega.Eventually(func(g gomega.Gomega) {
				evicted = nil
				for _, wl := range []*kueue.Workload{wlA1, wlA2} {
					got := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), got)).To(gomega.Succeed())
					if apimeta.IsStatusConditionTrue(got.Status.Conditions, kueue.WorkloadEvicted) {
						evicted = append(evicted, wl)
					}
				}
				g.Expect(evicted).To(gomega.HaveLen(1))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			behavioral.FinishEvictionForWorkloads(ctx, k8sClient, evicted...)
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlB)
		})
	})
})
