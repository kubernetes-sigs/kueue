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

package e2e

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	jobtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Fair Sharing", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns  *corev1.Namespace
		rf  *v1beta1.ResourceFlavor
		cq1 *v1beta1.ClusterQueue
		cq2 *v1beta1.ClusterQueue
		cq3 *v1beta1.ClusterQueue
		lq1 *v1beta1.LocalQueue
		lq2 *v1beta1.LocalQueue
		lq3 *v1beta1.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "ns-"}}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		rf = utiltesting.MakeResourceFlavor("rf").Obj()
		gomega.Expect(k8sClient.Create(ctx, rf)).To(gomega.Succeed())

		cq1 = utiltesting.MakeClusterQueue("cq1").
			Cohort("cohort").
			ResourceGroup(*utiltesting.MakeFlavorQuotas(rf.Name).
				Resource(corev1.ResourceCPU, "9").
				Resource(corev1.ResourceMemory, "36G").
				Obj()).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, cq1)).To(gomega.Succeed())
		cq2 = utiltesting.MakeClusterQueue("cq2").
			Cohort("cohort").
			ResourceGroup(*utiltesting.MakeFlavorQuotas(rf.Name).
				Resource(corev1.ResourceCPU, "9").
				Resource(corev1.ResourceMemory, "36G").
				Obj()).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, cq2)).To(gomega.Succeed())
		cq3 = utiltesting.MakeClusterQueue("cq3").
			Cohort("cohort").
			ResourceGroup(*utiltesting.MakeFlavorQuotas(rf.Name).
				Resource(corev1.ResourceCPU, "9").
				Resource(corev1.ResourceMemory, "36G").
				Obj()).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, cq3)).To(gomega.Succeed())

		lq1 = utiltesting.MakeLocalQueue("lq1", ns.Name).ClusterQueue(cq1.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, lq1)).To(gomega.Succeed())
		lq2 = utiltesting.MakeLocalQueue("lq2", ns.Name).ClusterQueue(cq2.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, lq2)).To(gomega.Succeed())
		lq3 = utiltesting.MakeLocalQueue("lq3", ns.Name).ClusterQueue(cq3.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, lq3)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq1, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq2, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq3, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
	})

	ginkgo.When("the cluster queue starts borrowing", func() {
		ginkgo.It("should update the ClusterQueue.status.fairSharing.weightedShare", func() {
			ginkgo.By("create jobs")
			for i := 0; i < 4; i++ {
				job := jobtesting.MakeJob(fmt.Sprintf("j%d", i+1), ns.Name).
					Queue(lq1.Name).
					Image(util.E2eTestAgnHostImage, util.BehaviorExitFast).
					Parallelism(3).
					Completions(3).
					Request("cpu", "1").
					Request("memory", "200Mi").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, job)).To(gomega.Succeed())
			}

			ginkgo.By("checking cluster queues")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq1), cq1)).Should(gomega.Succeed())
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq2), cq2)).Should(gomega.Succeed())
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq3), cq3)).Should(gomega.Succeed())

				g.Expect(cq1.Status.AdmittedWorkloads).Should(gomega.Equal(int32(4)))
				g.Expect(cq1.Status.FairSharing).ShouldNot(gomega.BeNil())
				g.Expect(cq1.Status.FairSharing.WeightedShare).Should(gomega.Equal(int64(111)))

				g.Expect(cq2.Status.AdmittedWorkloads).Should(gomega.Equal(int32(0)))
				g.Expect(cq2.Status.FairSharing).ShouldNot(gomega.BeNil())
				g.Expect(cq2.Status.FairSharing.WeightedShare).Should(gomega.Equal(int64(0)))

				g.Expect(cq3.Status.AdmittedWorkloads).Should(gomega.Equal(int32(0)))
				g.Expect(cq3.Status.FairSharing).ShouldNot(gomega.BeNil())
				g.Expect(cq3.Status.FairSharing.WeightedShare).Should(gomega.Equal(int64(0)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
