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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

// Probe for: a Workload rejected for exceeding a LimitRange max is never
// requeued when that LimitRange is relaxed or deleted, because the
// LimitRange event handler only recomputes defaults and the queue's
// PushOrUpdate guard sees an unchanged spec.
var _ = ginkgo.Describe("LimitRange constraint relaxation wake-up probe", func() {
	var (
		ns             *corev1.Namespace
		onDemandFlavor *kueue.ResourceFlavor
		clusterQueue   *kueue.ClusterQueue
		localQueue     *kueue.LocalQueue
		limitRange     *corev1.LimitRange
		wl             *kueue.Workload
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "lr-max-probe-")
		onDemandFlavor = utiltestingapi.MakeResourceFlavor("on-demand").Obj()
		util.MustCreate(ctx, k8sClient, onDemandFlavor)
		clusterQueue = utiltestingapi.MakeClusterQueue("cq-lr-max").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(onDemandFlavor.Name).
				Resource(corev1.ResourceCPU, "10").Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)
		localQueue = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)

		limitRange = utiltesting.MakeLimitRange("limits", ns.Name).
			WithValue("Max", corev1.ResourceCPU, "2").Obj()
		util.MustCreate(ctx, k8sClient, limitRange)

		wl = utiltestingapi.MakeWorkload("over-max", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			RequestAndLimit(corev1.ResourceCPU, "3").
			Obj()
		util.MustCreate(ctx, k8sClient, wl)

		ginkgo.By("Waiting for the workload to be rejected by the LimitRange, not by quota", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				read := kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read)).To(gomega.Succeed())
				cond := apimeta.FindStatusCondition(read.Status.Conditions, kueue.WorkloadQuotaReserved)
				g.Expect(cond).NotTo(gomega.BeNil())
				g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionFalse))
				g.Expect(cond.Message).To(gomega.ContainSubstring("LimitRange"))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
	})

	ginkgo.It("admits the workload after the LimitRange max is raised above its request", func() {
		ginkgo.By("Raising the LimitRange max", func() {
			updatedLr := corev1.LimitRange{}
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(limitRange), &updatedLr)).To(gomega.Succeed())
			updatedLr.Spec.Limits[0].Max[corev1.ResourceCPU] = resource.MustParse("8")
			gomega.Expect(k8sClient.Update(ctx, &updatedLr)).To(gomega.Succeed())
		})

		ginkgo.By("Expecting the workload to be admitted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				read := kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&read)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("admits the workload after the LimitRange is deleted", func() {
		ginkgo.By("Deleting the LimitRange", func() {
			gomega.Expect(k8sClient.Delete(ctx, limitRange)).To(gomega.Succeed())
		})

		ginkgo.By("Expecting the workload to be admitted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				read := kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&read)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
