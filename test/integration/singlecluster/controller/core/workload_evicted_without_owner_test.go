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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

// A Workload without an owner, such as a MultiKueue worker Workload before
// admission, has no job reconciler to complete its eviction.
var _ = ginkgo.Describe("Workload without an owner evicted before admission", ginkgo.Label("controller:workload", "area:core"), func() {
	var (
		ns     *corev1.Namespace
		flavor *kueue.ResourceFlavor
		check  *kueue.AdmissionCheck
		cq     *kueue.ClusterQueue
		lq     *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		fwk.StartManagerForSpec(ctx, cfg, managerAndSchedulerSetup)
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "evicted-without-owner-")
		flavor = utiltestingapi.MakeResourceFlavor("evicted-without-owner-flavor").Obj()
		behavioral.MustCreate(ctx, k8sClient, flavor)
		// No controller serves this check, so the test sets its state.
		check = utiltestingapi.MakeAdmissionCheck("evicted-without-owner-check").ControllerName("example.com/no-controller").Obj()
		behavioral.MustCreate(ctx, k8sClient, check)
		behavioral.SetAdmissionCheckActive(ctx, k8sClient, check, metav1.ConditionTrue)
		cq = utiltestingapi.MakeClusterQueue("evicted-without-owner-cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "4").Obj()).
			AdmissionChecks(kueue.AdmissionCheckReference(check.Name)).
			Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, cq)
		behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)
		lq = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, lq)
		behavioral.ExpectLocalQueuesToBeActive(ctx, k8sClient, lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, check, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
	})

	// reserveQuota creates a Workload of 3 of the 4 CPU without an owner and
	// waits until it reserves quota while its admission check is Pending.
	reserveQuota := func(name string, priority int32) *kueue.Workload {
		ginkgo.GinkgoHelper()
		wl := utiltestingapi.MakeWorkload(name, ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Priority(priority).
			Request(corev1.ResourceCPU, "3").
			Obj()
		behavioral.MustCreate(ctx, k8sClient, wl)
		behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, wl)
		gomega.Expect(wl.OwnerReferences).To(gomega.BeEmpty())
		return wl
	}

	// expectQuotaReleased waits until the evicted Workload no longer reserves
	// quota and the other Workload reserves it.
	expectQuotaReleased := func(evicted *kueue.Workload, evictionReason string, other *kueue.Workload) {
		ginkgo.GinkgoHelper()
		behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, other)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(evicted), evicted)).To(gomega.Succeed())
			g.Expect(workload.HasQuotaReservation(evicted)).To(gomega.BeFalse())
			g.Expect(evicted.Status.Admission).To(gomega.BeNil())
			g.Expect(evicted.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(kueue.WorkloadEvicted, evictionReason))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}

	ginkgo.It("releases its quota after an admission check Retry", func() {
		wl := reserveQuota("evicted", 0)
		waiting := utiltestingapi.MakeWorkload("waiting", ns.Name).Queue(kueue.LocalQueueName(lq.Name)).Request(corev1.ResourceCPU, "3").Obj()
		behavioral.MustCreate(ctx, k8sClient, waiting)
		behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, waiting)

		behavioral.SetWorkloadsAdmissionCheck(ctx, k8sClient, wl, kueue.AdmissionCheckReference(check.Name), kueue.CheckStateRetry, true)

		// Without a retry delay the released quota goes either to the waiting
		// Workload or back to this one, as a fresh reservation.
		gomega.Eventually(func(g gomega.Gomega) {
			var other kueue.Workload
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(waiting), &other)).To(gomega.Succeed())
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
			reservedAgain := workload.HasQuotaReservation(wl) && !apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadEvicted)
			g.Expect(workload.HasQuotaReservation(&other) || reservedAgain).To(gomega.BeTrue())
			g.Expect(wl.Status.AdmissionChecks).To(gomega.HaveLen(1))
			g.Expect(wl.Status.AdmissionChecks[0].State).To(gomega.Equal(kueue.CheckStatePending))
			g.Expect(wl.Status.AdmissionChecks[0].RetryCount).To(gomega.Equal(new(int32(1))))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("releases its quota after an admission check Retry with requeueAfterSeconds", func() {
		wl := reserveQuota("evicted", 0)
		waiting := utiltestingapi.MakeWorkload("waiting", ns.Name).Queue(kueue.LocalQueueName(lq.Name)).Request(corev1.ResourceCPU, "3").Obj()
		behavioral.MustCreate(ctx, k8sClient, waiting)
		behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, waiting)

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
			g.Expect(wl.Status.AdmissionChecks).To(gomega.HaveLen(1))
			wl.Status.AdmissionChecks[0].State = kueue.CheckStateRetry
			wl.Status.AdmissionChecks[0].RequeueAfterSeconds = new(int32(600))
			g.Expect(k8sClient.Status().Update(ctx, wl)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		expectQuotaReleased(wl, kueue.WorkloadEvictedByAdmissionCheck, waiting)
		gomega.Expect(wl.Status.RequeueState).NotTo(gomega.BeNil())
		gomega.Expect(wl.Status.RequeueState.RequeueAt).NotTo(gomega.BeNil())
	})

	ginkgo.It("releases its quota after a preemption", func() {
		wl := reserveQuota("evicted", 0)
		preemptor := utiltestingapi.MakeWorkload("preemptor", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Priority(100).
			Request(corev1.ResourceCPU, "3").
			Obj()
		behavioral.MustCreate(ctx, k8sClient, preemptor)

		expectQuotaReleased(wl, kueue.WorkloadEvictedByPreemption, preemptor)
		gomega.Expect(apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadRequeued)).To(gomega.BeTrue())
	})
})
