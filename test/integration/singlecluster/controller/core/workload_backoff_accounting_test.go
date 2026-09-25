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
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/metrics"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Workload accounting after requeue backoff", ginkgo.Label("controller:workload", "area:core"), func() {
	var (
		ns           *corev1.Namespace
		flavor       *kueue.ResourceFlavor
		runtimeClass *nodev1.RuntimeClass
		cq           *kueue.ClusterQueue
		lq           *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup)
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "backoff-accounting-")
		flavor = utiltestingapi.MakeResourceFlavor("backoff-flavor").Obj()
		behavioral.MustCreate(ctx, k8sClient, flavor)
		runtimeClass = utiltesting.MakeRuntimeClass("backoff-runtime", "handler").
			PodOverhead(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}).Obj()
		behavioral.MustCreate(ctx, k8sClient, runtimeClass)
		cq = utiltestingapi.MakeClusterQueue("backoff-cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "8").Obj()).Obj()
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
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, runtimeClass, true)
		fwk.StopManager(ctx)
		metrics.InitMetricVectors(nil)
	})

	ginkgo.It("considers effective resource requests when readmitting a workload after backoff", func() {
		wl := utiltestingapi.MakeWorkload("adjusted", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).Limit(corev1.ResourceCPU, "3").RuntimeClass(runtimeClass.Name).Obj()
		other := utiltestingapi.MakeWorkload("other", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).Request(corev1.ResourceCPU, "4").Obj()
		wlKey := client.ObjectKeyFromObject(wl)

		ginkgo.By("admitting the workload with 3 CPU from limits and 2 CPU overhead", func() {
			behavioral.MustCreate(ctx, k8sClient, wl)
			behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, wl)
			behavioral.MustCreate(ctx, k8sClient, other)
			behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, other)
		})

		ginkgo.By("releasing its quota with a requeue backoff, allowing the other workload to run", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				g.Expect(workloadpatching.PatchAdmissionStatus(ctx, k8sClient, wl, behavioral.RealClock, func(wl *kueue.Workload) (bool, error) {
					workload.UnsetQuotaReservationWithCondition(wl, kueue.WorkloadQuotaReservedReasonPendingEvaluation, "By test", time.Now())
					// Hold backoff until the other workload has quota; expire it explicitly below.
					wl.Status.RequeueState = &kueue.RequeueState{
						Count:     new(int32(1)),
						RequeueAt: new(metav1.NewTime(time.Now().Add(time.Hour))),
					}
					return true, nil
				})).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, other)
		})

		ginkgo.By("ending backoff after the other workload has reserved quota", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				var updated kueue.Workload
				g.Expect(k8sClient.Get(ctx, wlKey, &updated)).To(gomega.Succeed())
				g.Expect(workloadpatching.PatchAdmissionStatus(ctx, k8sClient, &updated, behavioral.RealClock, func(wl *kueue.Workload) (bool, error) {
					wl.Status.RequeueState.RequeueAt = new(metav1.NewTime(time.Now().Add(-time.Second)))
					return true, nil
				})).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("keeping the workload pending after backoff while only 4 CPU remain available", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				var updated kueue.Workload
				g.Expect(k8sClient.Get(ctx, wlKey, &updated)).To(gomega.Succeed())
				g.Expect(updated.Status.RequeueState).NotTo(gomega.BeNil())
				g.Expect(updated.Status.RequeueState.RequeueAt).To(gomega.BeNil())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeFalse())
			}, behavioral.LongConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.By("readmitting the workload with its full effective request once quota is released", func() {
			behavioral.FinishWorkloads(ctx, k8sClient, other)
			behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, wl)
			gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			gomega.Expect(wl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
			gomega.Expect(wl.Status.Admission.PodSetAssignments[0].ResourceUsage).To(gomega.Equal(corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("5"),
			}))
		})
	})
})
