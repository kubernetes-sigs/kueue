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
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Evicted Workload without owner", ginkgo.Label("controller:workload", "area:core"), func() {
	var (
		ns     *corev1.Namespace
		flavor *kueue.ResourceFlavor
		ac     *kueue.AdmissionCheck
		cq     *kueue.ClusterQueue
		lq     *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		fwk.StartManagerForSpec(ctx, cfg, managerAndSchedulerSetup)
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "evicted-no-owner-")
		flavor = utiltestingapi.MakeResourceFlavor("flavor").Obj()
		behavioral.MustCreate(ctx, k8sClient, flavor)
		ac = utiltestingapi.MakeAdmissionCheck("ac").ControllerName("example.com/ac").Obj()
		behavioral.MustCreate(ctx, k8sClient, ac)
		behavioral.SetAdmissionCheckActive(ctx, k8sClient, ac, metav1.ConditionTrue)
		cq = utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "2").Obj()).
			AdmissionChecks(kueue.AdmissionCheckReference(ac.Name)).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)
		lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
	})

	ginkgo.It("releases quota and retries after admission check Retry backoff", func() {
		wl := utiltestingapi.MakeWorkload("wl", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Request(corev1.ResourceCPU, "2").
			Obj()
		wlKey := client.ObjectKeyFromObject(wl)
		behavioral.MustCreate(ctx, k8sClient, wl)

		ginkgo.By("waiting for initial quota reservation without admission", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeTrue())
				g.Expect(workload.IsAdmitted(wl)).To(gomega.BeFalse())
				g.Expect(admissioncheck.FindAdmissionCheck(wl.Status.AdmissionChecks, kueue.AdmissionCheckReference(ac.Name))).NotTo(gomega.BeNil())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("marking admission check as Retry with a short backoff", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				workloadpatching.SetAdmissionCheckState(&wl.Status.AdmissionChecks, kueue.AdmissionCheckState{
					Name:                kueue.AdmissionCheckReference(ac.Name),
					State:               kueue.CheckStateRetry,
					Message:             "Retry check",
					RequeueAfterSeconds: new(int32(5)),
				}, behavioral.RealClock)
				g.Expect(k8sClient.Status().Update(ctx, wl)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying quota is released automatically during backoff", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeFalse())
				g.Expect(workloadevict.IsEvicted(wl)).To(gomega.BeTrue())
				g.Expect(apimeta.IsStatusConditionFalse(wl.Status.Conditions, kueue.WorkloadRequeued)).To(gomega.BeTrue())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying workload requeues and reserves quota again after backoff", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeTrue())
				g.Expect(workloadevict.IsEvicted(wl)).To(gomega.BeFalse())
				check := admissioncheck.FindAdmissionCheck(wl.Status.AdmissionChecks, kueue.AdmissionCheckReference(ac.Name))
				g.Expect(check).NotTo(gomega.BeNil())
				g.Expect(check.State).To(gomega.Equal(kueue.CheckStatePending))
				g.Expect(check.RetryCount).To(gomega.HaveValue(gomega.Equal(int32(1))))
			}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("admitting once check becomes Ready", func() {
			behavioral.SetWorkloadsAdmissionCheck(ctx, k8sClient, wl, kueue.AdmissionCheckReference(ac.Name), kueue.CheckStateReady, false)
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
		})
	})

	ginkgo.It("releases quota immediately when preempted before admission", func() {
		lowWl := utiltestingapi.MakeWorkload("low-wl", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Priority(1).
			Request(corev1.ResourceCPU, "2").
			Obj()
		lowKey := client.ObjectKeyFromObject(lowWl)
		behavioral.MustCreate(ctx, k8sClient, lowWl)

		ginkgo.By("waiting for low-priority workload to reserve quota without admission", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lowKey, lowWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(lowWl)).To(gomega.BeTrue())
				g.Expect(workload.IsAdmitted(lowWl)).To(gomega.BeFalse())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		highWl := utiltestingapi.MakeWorkload("high-wl", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Priority(10).
			Request(corev1.ResourceCPU, "2").
			Obj()
		highKey := client.ObjectKeyFromObject(highWl)
		behavioral.MustCreate(ctx, k8sClient, highWl)

		ginkgo.By("verifying low-priority workload releases quota automatically and high-priority workload reserves it", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lowKey, lowWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(lowWl)).To(gomega.BeFalse())
				g.Expect(apimeta.IsStatusConditionTrue(lowWl.Status.Conditions, kueue.WorkloadRequeued)).To(gomega.BeTrue())

				g.Expect(k8sClient.Get(ctx, highKey, highWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(highWl)).To(gomega.BeTrue())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("keeps quota reservation when an already admitted ownerless workload is evicted", func() {
		wl := utiltestingapi.MakeWorkload("admitted-wl", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Request(corev1.ResourceCPU, "2").
			Obj()
		wlKey := client.ObjectKeyFromObject(wl)
		behavioral.MustCreate(ctx, k8sClient, wl)

		ginkgo.By("admitting the workload", func() {
			behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, wl)
			behavioral.SetWorkloadsAdmissionCheck(ctx, k8sClient, wl, kueue.AdmissionCheckReference(ac.Name), kueue.CheckStateReady, false)
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
		})

		ginkgo.By("evicting the admitted workload and verifying quota is not auto-released", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				g.Expect(workloadpatching.PatchAdmissionStatus(ctx, k8sClient, wl, behavioral.RealClock, func(wl *kueue.Workload) (bool, error) {
					return workloadevict.SetEvictedCondition(wl, behavioral.RealClock.Now(), kueue.WorkloadEvictedByPreemption, "preempted"), nil
				})).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeTrue())
				g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
			}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
		})
	})
})
