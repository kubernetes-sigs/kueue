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

package job

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Job partial preemption", ginkgo.Ordered, func() {
	var (
		ns       *corev1.Namespace
		flavor   *kueue.ResourceFlavor
		aCQ, bCQ *kueue.ClusterQueue
		aLQ, bLQ *kueue.LocalQueue
		lowWPC   *kueue.WorkloadPriorityClass
		highWPC  *kueue.WorkloadPriorityClass
	)

	ginkgo.BeforeAll(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.PartialPreemption, true)
		fwk.StartManager(ctx, cfg, managerAndControllersSetup(false, true, nil))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "job-partial-preemption-")

		flavor = utiltestingapi.MakeResourceFlavor("alpha").Obj()
		behavioral.MustCreate(ctx, k8sClient, flavor)

		// Two ClusterQueues of 3 CPU each share the "all" cohort, and each may reclaim borrowed
		// quota from the other. The victim in b-cq can borrow up to 3 CPU from a-cq's share.
		aCQ = utiltestingapi.MakeClusterQueue("a-cq").
			Cohort("all").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "3").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, aCQ)
		aLQ = utiltestingapi.MakeLocalQueue("a-q", ns.Name).ClusterQueue(aCQ.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, aLQ)

		bCQ = utiltestingapi.MakeClusterQueue("b-cq").
			Cohort("all").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "3").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, bCQ)
		bLQ = utiltestingapi.MakeLocalQueue("b-q", ns.Name).ClusterQueue(bCQ.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, bLQ)

		lowWPC = utiltestingapi.MakeWorkloadPriorityClass("low").PriorityValue(100).Obj()
		behavioral.MustCreate(ctx, k8sClient, lowWPC)
		highWPC = utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(1000).Obj()
		behavioral.MustCreate(ctx, k8sClient, highWPC)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, aCQ, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, bCQ, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lowWPC, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, highWPC, true)
	})

	workloadForJob := func(g gomega.Gomega, job client.Object) *kueue.Workload {
		wls := &kueue.WorkloadList{}
		g.Expect(k8sClient.List(ctx, wls, client.InNamespace(ns.Name))).Should(gomega.Succeed())
		var owned []*kueue.Workload
		for i := range wls.Items {
			if metav1.IsControlledBy(&wls.Items[i], job) {
				owned = append(owned, &wls.Items[i])
			}
		}
		g.Expect(owned).Should(gomega.HaveLen(1))
		return owned[0]
	}

	expectPartialScaleDown := func(minParallelism, maxReclaimed string, wantTarget int32) {
		ginkgo.GinkgoHelper()

		ginkgo.By("Admitting a low-priority elastic victim Job in b-cq at parallelism 5 (borrowing 2 CPU from the cohort)")
		victimBuilder := testingjob.MakeJob("victim", ns.Name).
			Queue(kueue.LocalQueueName(bLQ.Name)).
			WorkloadPriorityClass(lowWPC.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(constants.PartialPreemptionAnnotation, "true").
			SetAnnotation(workloadjob.JobMinParallelismAnnotation, minParallelism).
			Parallelism(5).
			Request(corev1.ResourceCPU, "1")
		if maxReclaimed != "" {
			victimBuilder.SetAnnotation(constants.PartialPreemptionMaxReclaimedCountAnnotation, maxReclaimed)
		}
		victimJob := victimBuilder.Obj()
		behavioral.MustCreate(ctx, k8sClient, victimJob)

		gomega.Eventually(func(g gomega.Gomega) {
			victimWl := workloadForJob(g, victimJob)
			g.Expect(workload.IsAdmitted(victimWl)).Should(gomega.BeTrue())
			g.Expect(ptr.Deref(victimWl.Status.Admission.PodSetAssignments[0].Count, -1)).Should(gomega.BeEquivalentTo(int32(5)))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("Creating a higher-priority preemptor Job in a-cq requesting 3 CPU")
		preemptorJob := testingjob.MakeJob("preemptor", ns.Name).
			Queue(kueue.LocalQueueName(aLQ.Name)).
			WorkloadPriorityClass(highWPC.Name).
			Parallelism(3).
			Request(corev1.ResourceCPU, "1").
			Obj()
		behavioral.MustCreate(ctx, k8sClient, preemptorJob)

		ginkgo.By("The Job reconciler scales victim spec.parallelism down to the reclaim target")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(victimJob), victimJob)).Should(gomega.Succeed())
			g.Expect(ptr.Deref(victimJob.Spec.Parallelism, -1)).Should(gomega.BeEquivalentTo(wantTarget))
		}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("The victim slice is scaled down (not evicted) and its satisfied reclaim target is cleared")
		gomega.Eventually(func(g gomega.Gomega) {
			victimWl := workloadForJob(g, victimJob)
			g.Expect(workloadevict.IsEvicted(victimWl)).Should(gomega.BeFalse())
			g.Expect(workload.IsAdmitted(victimWl)).Should(gomega.BeTrue())
			// The job controller synced the lowered parallelism onto the slice's spec count.
			g.Expect(victimWl.Spec.PodSets[0].Count).Should(gomega.BeEquivalentTo(wantTarget))
			// Workload slicing charges min(spec.Count, admission.Count), so the admission Count is
			// left untouched at 5 — the reduced effective usage is what frees quota.
			g.Expect(ptr.Deref(victimWl.Status.Admission.PodSetAssignments[0].Count, -1)).Should(gomega.BeEquivalentTo(int32(5)))
			g.Expect(victimWl.Status.Admission.PodSetAssignments[0].ReclaimTargetCount).Should(gomega.BeNil())
			// The workload controller reset the Preempted condition once the reclaim converged.
			preempted := meta.FindStatusCondition(victimWl.Status.Conditions, kueue.WorkloadPreempted)
			if preempted != nil {
				g.Expect(preempted.Status).Should(gomega.Equal(metav1.ConditionFalse))
			}
		}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("Freed quota admits the preemptor")
		gomega.Eventually(func(g gomega.Gomega) {
			preemptorWl := workloadForJob(g, preemptorJob)
			g.Expect(workload.IsAdmitted(preemptorWl)).Should(gomega.BeTrue())
		}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
	}

	ginkgo.It("scales a borrowing elastic Job down to its minimum count to admit a higher-priority preemptor", func() {
		// Without a per-round reclaim cap, a single reclaim round scales the victim all the way to
		// its minimum count (1).
		expectPartialScaleDown("1", "", 1)
	})

	ginkgo.It("reclaims at most maxReclaimedCount replicas per round, stopping above the minimum count", func() {
		// With a per-round cap of 2 and minimum 1, a single reclaim round scales the victim from 5 to
		// 3 (5-2) — enough to free the 2 CPU the preemptor needs — rather than down to the minimum.
		expectPartialScaleDown("1", "2", 3)
	})
})
