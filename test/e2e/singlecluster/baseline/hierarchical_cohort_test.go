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

package baseline

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Hierarchical Cohort", ginkgo.Label("area:singlecluster", "feature:cohort"), func() {
	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
	)

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "ns-")
		rf = utiltestingapi.MakeResourceFlavor("rf-" + ns.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, rf)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	//      root (1 CPU)
	//        |
	//      child (no quota)
	//        |
	//       cq (0 CPU)
	ginkgo.When("a zero-quota ClusterQueue borrows from a root through a structural child Cohort", func() {
		var (
			rootCohort  *kueue.Cohort
			childCohort *kueue.Cohort
			cq          *kueue.ClusterQueue
			lq          *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			rootCohort = utiltestingapi.MakeCohort(kueue.CohortReference("root-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "1").
					Obj()).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, rootCohort)

			childCohort = utiltestingapi.MakeCohort(kueue.CohortReference("child-" + ns.Name)).
				Parent(kueue.CohortReference("root-" + ns.Name)).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, childCohort)

			cq = utiltestingapi.MakeClusterQueue("cq-" + ns.Name).
				Cohort(kueue.CohortReference("child-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "0").
					Obj()).
				Obj()
			behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(behavioral.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, childCohort, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, rootCohort, true)
		})

		ginkgo.It("should admit workloads through hierarchical borrowing", func() {
			ginkgo.By("submitting jobs that require borrowing from the parent cohort")
			for i := range 2 {
				job := testingjob.MakeJob(fmt.Sprintf("job-%d", i+1), ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					Image(behavioral.GetAgnHostImage(), behavioral.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "500m").
					TerminationGracePeriod(1).Obj()
				behavioral.MustCreate(ctx, k8sClient, job)
			}

			ginkgo.By("verifying workloads are admitted and resources are borrowed")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).Should(gomega.Succeed())
				g.Expect(cq.Status.AdmittedWorkloads).Should(gomega.Equal(int32(2)))
				g.Expect(cq.Status.PendingWorkloads).Should(gomega.Equal(int32(0)))
				g.Expect(cq.Status.FlavorsUsage[0].Resources[0].Total).Should(gomega.BeEquivalentTo(resource.MustParse("1")))
				g.Expect(cq.Status.FlavorsUsage[0].Resources[0].Borrowed).Should(gomega.BeEquivalentTo(resource.MustParse("1")))
			}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("submitting an overflow job that exceeds the root cohort capacity")
			overflowJob := testingjob.MakeJob("job-overflow", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Image(behavioral.GetAgnHostImage(), behavioral.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "500m").
				TerminationGracePeriod(1).Obj()
			behavioral.MustCreate(ctx, k8sClient, overflowJob)

			ginkgo.By("verifying the overflow job stays pending")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).Should(gomega.Succeed())
				g.Expect(cq.Status.PendingWorkloads).Should(gomega.Equal(int32(1)))
			}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).Should(gomega.Succeed())
				g.Expect(cq.Status.AdmittedWorkloads).Should(gomega.Equal(int32(2)))
				g.Expect(cq.Status.PendingWorkloads).Should(gomega.Equal(int32(1)))
				g.Expect(cq.Status.FlavorsUsage[0].Resources[0].Total).Should(gomega.BeEquivalentTo(resource.MustParse("1")))
				g.Expect(cq.Status.FlavorsUsage[0].Resources[0].Borrowed).Should(gomega.BeEquivalentTo(resource.MustParse("1")))
			}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
		})
	})
})
