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

package fairsharing

import (
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Scheduler", func() {

	var (
		defaultFlavor *kueue.ResourceFlavor
		ns            *corev1.Namespace
	)

	ginkgo.BeforeEach(func() {
		defaultFlavor = testing.MakeResourceFlavor("default").Obj()
		gomega.Expect(k8sClient.Create(ctx, defaultFlavor)).To(gomega.Succeed())

		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteResourceFlavor(ctx, k8sClient, defaultFlavor)).To(gomega.Succeed())
	})

	ginkgo.When("Preemption is disabled", func() {

		var (
			cqA      *kueue.ClusterQueue
			lqA      *kueue.LocalQueue
			cqB      *kueue.ClusterQueue
			lqB      *kueue.LocalQueue
			cqShared *kueue.ClusterQueue
		)
		ginkgo.BeforeEach(func() {
			cqA = testing.MakeClusterQueue("a").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "3").Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, cqA)).To(gomega.Succeed())
			lqA = testing.MakeLocalQueue("a", ns.Name).ClusterQueue("a").Obj()
			gomega.Expect(k8sClient.Create(ctx, lqA)).To(gomega.Succeed())

			cqB = testing.MakeClusterQueue("b").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, cqB)).To(gomega.Succeed())
			lqB = testing.MakeLocalQueue("b", ns.Name).ClusterQueue("b").Obj()
			gomega.Expect(k8sClient.Create(ctx, lqB)).To(gomega.Succeed())

			cqShared = testing.MakeClusterQueue("shared").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "4").Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, cqShared)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			gomega.Expect(util.DeleteClusterQueue(ctx, k8sClient, cqA)).To(gomega.Succeed())
			gomega.Expect(util.DeleteClusterQueue(ctx, k8sClient, cqB)).To(gomega.Succeed())
			gomega.Expect(util.DeleteClusterQueue(ctx, k8sClient, cqShared)).To(gomega.Succeed())
		})

		ginkgo.It("Admits workloads respecting fair share", func() {
			ginkgo.By("Saturating cq-a")

			aWorkloads := make([]*kueue.Workload, 10)
			for i := range aWorkloads {
				aWorkloads[i] = testing.MakeWorkload(fmt.Sprintf("a-%d", i), ns.Name).
					Queue("a").
					Request(corev1.ResourceCPU, "1").Obj()
				gomega.Expect(k8sClient.Create(ctx, aWorkloads[i])).To(gomega.Succeed())
			}
			util.ExpectReservingActiveWorkloadsMetric(cqA, 8)
			util.ExpectPendingWorkloadsMetric(cqA, 0, 2)

			ginkgo.By("Creating newer workloads in cq-b")
			// Ensure workloads in cqB have a newer timestamp.
			time.Sleep(time.Second)
			bWorkloads := make([]*kueue.Workload, 5)
			for i := range bWorkloads {
				bWorkloads[i] = testing.MakeWorkload(fmt.Sprintf("b-%d", i), ns.Name).
					Queue("b").
					Request(corev1.ResourceCPU, "1").Obj()
				gomega.Expect(k8sClient.Create(ctx, bWorkloads[i])).To(gomega.Succeed())
			}
			util.ExpectPendingWorkloadsMetric(cqB, 0, 5)

			ginkgo.By("Terminating 4 running workloads in cqA: shared quota is fair-shared")
			finishRunningWorkloadsInCQ(cqA, 4)

			// Admits 1 from cqA and 3 from cqB.
			util.ExpectReservingActiveWorkloadsMetric(cqA, 5)
			util.ExpectPendingWorkloadsMetric(cqA, 0, 1)
			util.ExpectReservingActiveWorkloadsMetric(cqB, 3)
			util.ExpectPendingWorkloadsMetric(cqB, 0, 2)

			ginkgo.By("Terminating 2 more running workloads in cqA: cqB starts to take over shared quota")
			finishRunningWorkloadsInCQ(cqA, 2)

			// Admits last 1 from cqA and 1 from cqB.
			util.ExpectReservingActiveWorkloadsMetric(cqA, 4)
			util.ExpectPendingWorkloadsMetric(cqA, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(cqB, 4)
			util.ExpectPendingWorkloadsMetric(cqB, 0, 1)
		})
	})

})

func finishRunningWorkloadsInCQ(cq *kueue.ClusterQueue, n int) {
	var wList kueue.WorkloadList
	gomega.ExpectWithOffset(1, k8sClient.List(ctx, &wList)).To(gomega.Succeed())
	finished := 0
	for i := 0; i < len(wList.Items) && finished < n; i++ {
		wl := wList.Items[i]
		if wl.Status.Admission != nil && string(wl.Status.Admission.ClusterQueue) == cq.Name && !meta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadFinished) {
			util.FinishWorkloads(ctx, k8sClient, &wl)
			finished++
		}
	}
	gomega.ExpectWithOffset(1, finished).To(gomega.Equal(n), "Not enough workloads finished")
}
