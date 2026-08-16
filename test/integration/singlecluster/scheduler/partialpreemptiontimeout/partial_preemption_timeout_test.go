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

package partialpreemptiontimeout

import (
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Partial preemption timeout", func() {
	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.PartialPreemption, true)

		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "partial-preemption-timeout-")

		rf = utiltestingapi.MakeResourceFlavor("default").Obj()
		behavioral.MustCreate(ctx, k8sClient, rf)

		cq = utiltestingapi.MakeClusterQueue("partial-preemption-timeout-cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
				Resource(corev1.ResourceCPU, "10").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue("partial-preemption-timeout-lq", ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
	})

	ginkgo.It("falls back to whole preemption when a partial target does not converge before timeout", func() {
		victim := utiltestingapi.MakeWorkload("victim", ns.Name).
			Annotation(constants.ElasticJobAnnotation, "true").
			Annotation(constants.PartialPreemptionAnnotation, "true").
			Priority(0).
			Queue(kueue.LocalQueueName(lq.Name)).
			PodSets(*utiltestingapi.MakePodSet("main", 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
			Obj()
		preemptor := utiltestingapi.MakeWorkload("preemptor", ns.Name).
			Priority(10).
			Queue(kueue.LocalQueueName(lq.Name)).
			PodSets(*utiltestingapi.MakePodSet("main", 4).Request(corev1.ResourceCPU, "1").Obj()).
			Obj()

		ginkgo.By("Admitting the victim that occupies the whole ClusterQueue")
		behavioral.MustCreate(ctx, k8sClient, victim)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, victim)
		expectCQUsage(cq, "10")

		ginkgo.By("Creating the preemptor and waiting for the initial partial preemption request")
		behavioral.MustCreate(ctx, k8sClient, preemptor)
		expectReclaimTargetCount(victim, ptr.To[int32](6))
		expectPreemptedCondition(victim, metav1.ConditionTrue)

		ginkgo.By("Keeping the victim above reclaimTargetCount until the timeout elapses")
		time.Sleep(2 * partialPreemptionTimeout)

		ginkgo.By("The next scheduling cycle falls back to whole preemption")
		expectWorkloadEvicted(victim)
		behavioral.FinishEvictionForWorkloads(ctx, k8sClient, victim)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, preemptor)
	})
})

func expectCQUsage(cq *kueue.ClusterQueue, want string) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		got := &kueue.ClusterQueue{}
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), got)).Should(gomega.Succeed())
		g.Expect(got.Status.FlavorsUsage).Should(gomega.HaveLen(1))
		g.Expect(got.Status.FlavorsUsage[0].Resources).Should(gomega.HaveLen(1))
		g.Expect(got.Status.FlavorsUsage[0].Resources[0].Total).Should(gomega.BeEquivalentTo(resource.MustParse(want)))
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
}

func expectReclaimTargetCount(wl *kueue.Workload, want *int32) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		got := &kueue.Workload{}
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), got)).Should(gomega.Succeed())
		g.Expect(workload.IsAdmitted(got)).Should(gomega.BeTrue())
		g.Expect(got.Status.Admission).ShouldNot(gomega.BeNil())
		g.Expect(got.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
		g.Expect(got.Status.Admission.PodSetAssignments[0].ReclaimTargetCount).Should(gomega.Equal(want))
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
}

func expectPreemptedCondition(wl *kueue.Workload, status metav1.ConditionStatus) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		got := &kueue.Workload{}
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), got)).Should(gomega.Succeed())
		cond := meta.FindStatusCondition(got.Status.Conditions, kueue.WorkloadPreempted)
		g.Expect(cond).ShouldNot(gomega.BeNil())
		g.Expect(cond.Status).Should(gomega.Equal(status))
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
}

func expectWorkloadEvicted(wl *kueue.Workload) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		got := &kueue.Workload{}
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), got)).Should(gomega.Succeed())
		g.Expect(workloadevict.IsEvicted(got)).Should(gomega.BeTrue())
		cond := meta.FindStatusCondition(got.Status.Conditions, kueue.WorkloadPreempted)
		g.Expect(cond).ShouldNot(gomega.BeNil())
		g.Expect(cond.Status).Should(gomega.Equal(metav1.ConditionTrue))
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
}
