/*
Copyright 2024 The Kubernetes Authors.

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
	"sync/atomic"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	workload "sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Admission Race Condition", func() {
	var (
		ns          *corev1.Namespace
		alphaFlavor *kueue.ResourceFlavor
		cq          *kueue.ClusterQueue
		q           *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "race-")
		alphaFlavor = utiltestingapi.MakeResourceFlavor("alpha").Obj()
		util.MustCreate(ctx, k8sClient, alphaFlavor)

		cq = utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("alpha").Resource(corev1.ResourceCPU, "1").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		util.MustCreate(ctx, k8sClient, cq)

		q = utiltestingapi.MakeLocalQueue("q", ns.Name).ClusterQueue(cq.Name).Obj()
		util.MustCreate(ctx, k8sClient, q)
	})

	// ginkgo.AfterEach(func() {
	// 	gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	// 	util.ExpectObjectToBeDeleted(ctx, k8sClient, alphaFlavor, true)
	// })

	ginkgo.It("Blocks workloads from being admitted due to a phantom preemption", func() {
		blockAdmission := make(chan struct{})
		admissionProceed := make(chan struct{})

		var intercepted atomic.Bool

		fakeSubResourcePatchSpec = func(obj client.Object) (fakeClientUsage, error) {
			wl, isWl := obj.(*kueue.Workload)
			if !isWl {
				return fallThrough, nil
			}

			if wl.Name == "wl1" && workload.HasQuotaReservation(wl) {
				if intercepted.CompareAndSwap(false, true) {
					blockAdmission <- struct{}{}
					<-admissionProceed
				}
			}
			return fallThrough, nil
		}

		defer func() {
			fakeSubResourcePatchSpec = nil
		}()

		ginkgo.By("Creating a low priority Workload wl1")
		wl1 := utiltestingapi.MakeWorkload("wl1", ns.Name).
			Queue(kueue.LocalQueueName(q.Name)).
			Priority(-10).
			Request(corev1.ResourceCPU, "1").
			Obj()
		util.MustCreate(ctx, k8sClient, wl1)

		ginkgo.By("Waiting for wl1's admission patch to be intercepted")
		<-blockAdmission

		ginkgo.By("Creating a high priority Workload wl2")
		wl2 := utiltestingapi.MakeWorkload("wl2", ns.Name).
			Queue(kueue.LocalQueueName(q.Name)).
			Priority(10).
			Request(corev1.ResourceCPU, "1").
			Obj()
		util.MustCreate(ctx, k8sClient, wl2)

		ginkgo.By("Waiting for wl2 to preempt wl1, but not get a quota reservation")
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
			util.ExpectWorkloadsToBePreempted(ctx, k8sClient, wl1)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, wl1)

			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())
			g.Expect(workload.HasQuotaReservation(wl2)).To(gomega.BeFalse())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Letting wl1's admission patch proceed")
		close(admissionProceed)

		ginkgo.By("wl1 is admitted")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("wl1 is continues being admitted and wl2 keeps waiting for an eviction that will never happen")
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)

			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())
			g.Expect(workload.HasQuotaReservation(wl2)).To(gomega.BeFalse())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
})
