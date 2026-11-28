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
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe("StrictFIFO NoFit retry notifications", ginkgo.Label("area:core"), func() {
	var ns *corev1.Namespace
	var flavor *kueue.ResourceFlavor
	var cq, donorCQ, otherCQ *kueue.ClusterQueue
	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "strictfifo-retry-")
		flavor = utiltestingapi.MakeResourceFlavor("strictfifo-retry-flavor").Obj()
		behavioral.MustCreate(ctx, k8sClient, flavor)
		cq = utiltestingapi.MakeClusterQueue("strictfifo-retry-cq").QueueingStrategy(kueue.StrictFIFO).
			Cohort("strictfifo-retry-cohort").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "2").Obj()).Obj()
		donorCQ = utiltestingapi.MakeClusterQueue("strictfifo-retry-donor").Cohort("strictfifo-retry-cohort").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "2").Obj()).Obj()
		otherCQ = utiltestingapi.MakeClusterQueue("strictfifo-retry-other").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "1").Obj()).Obj()
		behavioral.MustCreate(ctx, k8sClient, cq)
		behavioral.MustCreate(ctx, k8sClient, donorCQ)
		behavioral.MustCreate(ctx, k8sClient, otherCQ)
		behavioral.MustCreate(ctx, k8sClient,
			utiltestingapi.MakeLocalQueue("blocked", ns.Name).ClusterQueue(cq.Name).Obj(),
		)
		behavioral.MustCreate(ctx, k8sClient,
			utiltestingapi.MakeLocalQueue("donor", ns.Name).ClusterQueue(donorCQ.Name).Obj())
		behavioral.MustCreate(ctx, k8sClient,
			utiltestingapi.MakeLocalQueue("other", ns.Name).ClusterQueue(otherCQ.Name).Obj())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, donorCQ, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, otherCQ, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
	})
	ginkgo.It("Should retain a NoFit head, allow another CQ, and recover after quota increases", func() {
		head := utiltestingapi.MakeWorkload("head", ns.Name).Queue("blocked").Request(corev1.ResourceCPU, "5").Obj()
		behavioral.MustCreate(ctx, k8sClient, head)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(head), head)).To(gomega.Succeed())
			condition := apimeta.FindStatusCondition(head.Status.Conditions, kueue.WorkloadQuotaReserved)
			g.Expect(condition).ToNot(gomega.BeNil())
			g.Expect(condition.Status).To(gomega.Equal(metav1.ConditionFalse))
			g.Expect(condition.Reason).To(gomega.Equal(kueue.WorkloadQuotaReservedReasonExceedsMaxQuota))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		// API creation timestamps have second precision; establish FIFO order
		// explicitly so equal timestamps cannot leave it to the generated UIDs.
		behavioral.WaitForNextSecondAfterCreation(head)
		tail := utiltestingapi.MakeWorkload("tail", ns.Name).Queue("blocked").Request(corev1.ResourceCPU, "1").Obj()
		other := utiltestingapi.MakeWorkload("independent", ns.Name).Queue("other").Request(corev1.ResourceCPU, "1").Obj()
		behavioral.MustCreate(ctx, k8sClient, tail)
		gomega.Expect(head.CreationTimestamp.Before(&tail.CreationTimestamp)).To(gomega.BeTrue())
		behavioral.MustCreate(ctx, k8sClient, other)
		behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, client.ObjectKeyFromObject(other))
		gomega.Consistently(func(g gomega.Gomega) {
			for _, wl := range []*kueue.Workload{head, tail} {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeFalse())
			}
		}, 1500*time.Millisecond, behavioral.Interval).Should(gomega.Succeed())

		before := cq.DeepCopy()
		cq.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota = resource.MustParse("6")
		gomega.Expect(k8sClient.Patch(ctx, cq, client.MergeFrom(before))).To(gomega.Succeed())
		behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, client.ObjectKeyFromObject(head), client.ObjectKeyFromObject(tail))
	})

	ginkgo.It("Should preserve FIFO order and borrow after another CQ releases quota", func() {
		occupier := utiltestingapi.MakeWorkload("occupier", ns.Name).Queue("donor").Request(corev1.ResourceCPU, "2").Obj()
		behavioral.MustCreate(ctx, k8sClient, occupier)
		behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, client.ObjectKeyFromObject(occupier))
		head := utiltestingapi.MakeWorkload("head", ns.Name).Queue("blocked").Request(corev1.ResourceCPU, "3").Obj()
		behavioral.MustCreate(ctx, k8sClient, head)
		// The request exceeds this CQ's nominal quota, but not the cohort's
		// capacity. With the donor occupied and preemption disabled, it is NoFit.
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(head), head)).To(gomega.Succeed())
			condition := apimeta.FindStatusCondition(head.Status.Conditions, kueue.WorkloadQuotaReserved)
			g.Expect(condition).ToNot(gomega.BeNil())
			g.Expect(condition.Status).To(gomega.Equal(metav1.ConditionFalse))
			g.Expect(condition.Reason).To(gomega.Equal(kueue.WorkloadQuotaReservedReasonWaitingForQuota))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		behavioral.WaitForNextSecondAfterCreation(head)
		tail := utiltestingapi.MakeWorkload("tail", ns.Name).Queue("blocked").Request(corev1.ResourceCPU, "1").Obj()
		other := utiltestingapi.MakeWorkload("independent", ns.Name).Queue("other").Request(corev1.ResourceCPU, "1").Obj()
		behavioral.MustCreate(ctx, k8sClient, tail)
		gomega.Expect(head.CreationTimestamp.Before(&tail.CreationTimestamp)).To(gomega.BeTrue())
		behavioral.MustCreate(ctx, k8sClient, other)
		behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, client.ObjectKeyFromObject(other))
		gomega.Consistently(func(g gomega.Gomega) {
			for _, wl := range []*kueue.Workload{head, tail} {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeFalse())
			}
		}, 1500*time.Millisecond, behavioral.Interval).Should(gomega.Succeed())
		integration.FinishWorkloads(ctx, k8sClient, occupier)
		behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, client.ObjectKeyFromObject(head), client.ObjectKeyFromObject(tail))
		behavioral.ExpectClusterQueueResourceUsage(ctx, k8sClient, client.ObjectKeyFromObject(cq), corev1.ResourceCPU, 4)
	})
})
