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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Hierarchical Cohort", ginkgo.Label("area:singlecluster", "feature:cohort"), func() {
	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "ns-")
		rf = utiltestingapi.MakeResourceFlavor("rf-" + ns.Name).Obj()
		util.MustCreate(ctx, k8sClient, rf)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

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
			util.MustCreate(ctx, k8sClient, rootCohort)

			childCohort = utiltestingapi.MakeCohort(kueue.CohortReference("child-" + ns.Name)).
				Parent(kueue.CohortReference("root-" + ns.Name)).
				Obj()
			util.MustCreate(ctx, k8sClient, childCohort)

			cq = utiltestingapi.MakeClusterQueue("cq-" + ns.Name).
				Cohort(kueue.CohortReference("child-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "0").
					Obj()).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, childCohort, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, rootCohort, true)
		})

		ginkgo.It("should admit workloads through hierarchical borrowing", func() {
			ginkgo.By("submitting jobs that require borrowing from the parent cohort")
			for i := range 2 {
				job := testingjob.MakeJob(fmt.Sprintf("job-%d", i+1), ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "500m").
					TerminationGracePeriod(1).Obj()
				util.MustCreate(ctx, k8sClient, job)
			}

			ginkgo.By("verifying workloads are admitted and resources are borrowed")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).Should(gomega.Succeed())
				g.Expect(cq.Status.AdmittedWorkloads).Should(gomega.Equal(int32(2)))
				g.Expect(cq.Status.PendingWorkloads).Should(gomega.Equal(int32(0)))
				g.Expect(cq.Status.FlavorsUsage[0].Resources[0].Total).Should(gomega.BeEquivalentTo(resource.MustParse("1")))
				g.Expect(cq.Status.FlavorsUsage[0].Resources[0].Borrowed).Should(gomega.BeEquivalentTo(resource.MustParse("1")))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("submitting an overflow job that exceeds the root cohort capacity")
			overflowJob := testingjob.MakeJob("job-overflow", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "500m").
				TerminationGracePeriod(1).Obj()
			util.MustCreate(ctx, k8sClient, overflowJob)

			ginkgo.By("verifying the overflow job stays pending")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).Should(gomega.Succeed())
				g.Expect(cq.Status.PendingWorkloads).Should(gomega.Equal(int32(1)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).Should(gomega.Succeed())
				g.Expect(cq.Status.AdmittedWorkloads).Should(gomega.Equal(int32(2)))
				g.Expect(cq.Status.PendingWorkloads).Should(gomega.Equal(int32(1)))
				g.Expect(cq.Status.FlavorsUsage[0].Resources[0].Total).Should(gomega.BeEquivalentTo(resource.MustParse("1")))
				g.Expect(cq.Status.FlavorsUsage[0].Resources[0].Borrowed).Should(gomega.BeEquivalentTo(resource.MustParse("1")))
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("a Cohort with pending workloads is reparented to a larger root", func() {
		var (
			rootA       *kueue.Cohort
			rootB       *kueue.Cohort
			childCohort *kueue.Cohort
			cq          *kueue.ClusterQueue
			lq          *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			rootA = utiltestingapi.MakeCohort(kueue.CohortReference("root-a-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "1").
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, rootA)

			rootB = utiltestingapi.MakeCohort(kueue.CohortReference("root-b-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "2").
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, rootB)

			childCohort = utiltestingapi.MakeCohort(kueue.CohortReference("child-" + ns.Name)).
				Parent(kueue.CohortReference("root-a-" + ns.Name)).
				Obj()
			util.MustCreate(ctx, k8sClient, childCohort)

			cq = utiltestingapi.MakeClusterQueue("cq-" + ns.Name).
				Cohort(kueue.CohortReference("child-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "0").
					Obj()).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, childCohort, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, rootA, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, rootB, true)
		})

		ginkgo.It("should admit overflow workload after reparenting to a larger root", func() {
			ginkgo.By("submitting jobs that fill root-a's capacity")
			for i := range 2 {
				job := testingjob.MakeJob(fmt.Sprintf("fill-%d", i+1), ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "500m").
					TerminationGracePeriod(1).Obj()
				util.MustCreate(ctx, k8sClient, job)
			}

			ginkgo.By("verifying root-a is filled and parentName points to root-a")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).Should(gomega.Succeed())
				g.Expect(cq.Status.AdmittedWorkloads).Should(gomega.Equal(int32(2)))
				g.Expect(cq.Status.FlavorsUsage[0].Resources[0].Borrowed).Should(gomega.BeEquivalentTo(resource.MustParse("1")))
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(childCohort), childCohort)).Should(gomega.Succeed())
				g.Expect(childCohort.Spec.ParentName).Should(gomega.Equal(kueue.CohortReference("root-a-" + ns.Name)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("submitting an overflow job that exceeds root-a's capacity")
			overflowJob := testingjob.MakeJob("overflow", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "500m").
				TerminationGracePeriod(1).Obj()
			util.MustCreate(ctx, k8sClient, overflowJob)

			ginkgo.By("verifying the overflow job stays pending under root-a")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).Should(gomega.Succeed())
				g.Expect(cq.Status.PendingWorkloads).Should(gomega.Equal(int32(1)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).Should(gomega.Succeed())
				g.Expect(cq.Status.AdmittedWorkloads).Should(gomega.Equal(int32(2)))
				g.Expect(cq.Status.PendingWorkloads).Should(gomega.Equal(int32(1)))
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())

			ginkgo.By("reparenting child-cohort from root-a to root-b")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(childCohort), childCohort)).Should(gomega.Succeed())
				childCohort.Spec.ParentName = kueue.CohortReference("root-b-" + ns.Name)
				g.Expect(k8sClient.Update(ctx, childCohort)).Should(gomega.Succeed())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("verifying the overflow job is admitted under root-b")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).Should(gomega.Succeed())
				g.Expect(cq.Status.AdmittedWorkloads).Should(gomega.Equal(int32(3)))
				g.Expect(cq.Status.PendingWorkloads).Should(gomega.Equal(int32(0)))
				g.Expect(cq.Status.FlavorsUsage[0].Resources[0].Borrowed).Should(gomega.BeEquivalentTo(resource.MustParse("1500m")))
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(childCohort), childCohort)).Should(gomega.Succeed())
				g.Expect(childCohort.Spec.ParentName).Should(gomega.Equal(kueue.CohortReference("root-b-" + ns.Name)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("a mid-tree Cohort is deleted while workloads are admitted", func() {
		var (
			rootCohort *kueue.Cohort
			midCohort  *kueue.Cohort
			cq         *kueue.ClusterQueue
			lq         *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			rootCohort = utiltestingapi.MakeCohort(kueue.CohortReference("root-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "1").
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, rootCohort)

			midCohort = utiltestingapi.MakeCohort(kueue.CohortReference("mid-" + ns.Name)).
				Parent(kueue.CohortReference("root-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "1").
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, midCohort)

			cq = utiltestingapi.MakeClusterQueue("cq-" + ns.Name).
				Cohort(kueue.CohortReference("mid-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "1").
					Obj()).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, midCohort, true) // may already be deleted by the test
			util.ExpectObjectToBeDeleted(ctx, k8sClient, rootCohort, true)
		})

		ginkgo.It("should keep admitted workloads after mid-tree deletion and block new borrowing", func() {
			ginkgo.By("submitting jobs that borrow through mid-cohort")
			for i := range 3 {
				job := testingjob.MakeJob(fmt.Sprintf("fill-%d", i+1), ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "500m").
					TerminationGracePeriod(1).Obj()
				util.MustCreate(ctx, k8sClient, job)
			}

			ginkgo.By("verifying workloads are admitted with borrowing through mid-cohort")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).Should(gomega.Succeed())
				g.Expect(cq.Status.AdmittedWorkloads).Should(gomega.Equal(int32(3)))
				g.Expect(cq.Status.FlavorsUsage[0].Resources[0].Borrowed).Should(gomega.BeEquivalentTo(resource.MustParse("500m")))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("deleting mid-cohort while workloads are borrowing through it")
			util.ExpectObjectToBeDeleted(ctx, k8sClient, midCohort, true)

			ginkgo.By("verifying admitted workloads are NOT evicted after mid-tree deletion")
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).Should(gomega.Succeed())
				g.Expect(cq.Status.AdmittedWorkloads).Should(gomega.Equal(int32(3)))
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())

			ginkgo.By("submitting a new job that would require borrowing")
			util.MustCreate(ctx, k8sClient, testingjob.MakeJob("after-delete", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "500m").
				TerminationGracePeriod(1).Obj())

			ginkgo.By("verifying new borrowing is blocked after mid-tree deletion")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).Should(gomega.Succeed())
				g.Expect(cq.Status.PendingWorkloads).Should(gomega.Equal(int32(1)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).Should(gomega.Succeed())
				g.Expect(cq.Status.AdmittedWorkloads).Should(gomega.Equal(int32(3)))
				g.Expect(cq.Status.PendingWorkloads).Should(gomega.Equal(int32(1)))
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("Cohorts form a mutual parent cycle", func() {
		var (
			cohortX *kueue.Cohort
			cohortY *kueue.Cohort
			cq      *kueue.ClusterQueue
			lq      *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			cohortX = utiltestingapi.MakeCohort(kueue.CohortReference("cohort-x-" + ns.Name)).
				Parent(kueue.CohortReference("cohort-y-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "1").
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, cohortX)

			cohortY = utiltestingapi.MakeCohort(kueue.CohortReference("cohort-y-" + ns.Name)).
				Parent(kueue.CohortReference("cohort-x-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "1").
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, cohortY)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cohortX), cohortX)).Should(gomega.Succeed())
				g.Expect(cohortX.Spec.ParentName).Should(gomega.Equal(kueue.CohortReference("cohort-y-" + ns.Name)))
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cohortY), cohortY)).Should(gomega.Succeed())
				g.Expect(cohortY.Spec.ParentName).Should(gomega.Equal(kueue.CohortReference("cohort-x-" + ns.Name)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			cq = utiltestingapi.MakeClusterQueue("cq-" + ns.Name).
				Cohort(kueue.CohortReference("cohort-x-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "1").
					Obj()).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cohortX, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cohortY, true)
		})

		ginkgo.It("should block all admissions when Cohorts form a cycle", func() {
			ginkgo.By("submitting a job to the cycled CQ")
			job := testingjob.MakeJob("cycle-job", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "500m").
				TerminationGracePeriod(1).Obj()
			util.MustCreate(ctx, k8sClient, job)

			ginkgo.By("verifying the workload is blocked because the CQ is inactive in the scheduler snapshot")
			gomega.Eventually(func(g gomega.Gomega) {
				var wlList kueue.WorkloadList
				g.Expect(k8sClient.List(ctx, &wlList, client.InNamespace(ns.Name))).Should(gomega.Succeed())
				g.Expect(wlList.Items).Should(gomega.HaveLen(1))
				cond := apimeta.FindStatusCondition(wlList.Items[0].Status.Conditions, kueue.WorkloadQuotaReserved)
				g.Expect(cond).ShouldNot(gomega.BeNil())
				g.Expect(cond.Status).Should(gomega.Equal(metav1.ConditionFalse))
				g.Expect(cond.Message).Should(gomega.ContainSubstring("is inactive"))
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).Should(gomega.Succeed())
				g.Expect(cq.Status.AdmittedWorkloads).Should(gomega.Equal(int32(0)))
				g.Expect(cq.Status.PendingWorkloads).Should(gomega.Equal(int32(1)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("verifying the blocked state holds: no admissions and workload still pending")
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).Should(gomega.Succeed())
				g.Expect(cq.Status.AdmittedWorkloads).Should(gomega.Equal(int32(0)))
				g.Expect(cq.Status.PendingWorkloads).Should(gomega.Equal(int32(1)))
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("a Cohort has lendingLimit that caps sibling borrowing", func() {
		var (
			rootCohort     *kueue.Cohort
			lenderCohort   *kueue.Cohort
			borrowerCohort *kueue.Cohort
			lenderCq       *kueue.ClusterQueue
			borrowerCq     *kueue.ClusterQueue
			lq             *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			rootCohort = utiltestingapi.MakeCohort(kueue.CohortReference("root-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "0").
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, rootCohort)

			lenderCohort = utiltestingapi.MakeCohort(kueue.CohortReference("lender-" + ns.Name)).
				Parent(kueue.CohortReference("root-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "1", "", "500m").
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, lenderCohort)

			borrowerCohort = utiltestingapi.MakeCohort(kueue.CohortReference("borrower-" + ns.Name)).
				Parent(kueue.CohortReference("root-" + ns.Name)).
				Obj()
			util.MustCreate(ctx, k8sClient, borrowerCohort)

			lenderCq = utiltestingapi.MakeClusterQueue("lender-cq-" + ns.Name).
				Cohort(kueue.CohortReference("lender-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "1").
					Obj()).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, lenderCq)

			borrowerCq = utiltestingapi.MakeClusterQueue("borrower-cq-" + ns.Name).
				Cohort(kueue.CohortReference("borrower-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "0").
					Obj()).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, borrowerCq)

			lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(borrowerCq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, borrowerCq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lenderCq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, borrowerCohort, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lenderCohort, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, rootCohort, true)
		})

		ginkgo.It("should reject lendingLimit on a root Cohort", func() {
			invalidCohort := utiltestingapi.MakeCohort(kueue.CohortReference("invalid-lending-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "1", "", "500m").
					Obj()).
				Obj()
			err := k8sClient.Create(ctx, invalidCohort)
			if err == nil {
				ginkgo.DeferCleanup(func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, invalidCohort, true)
				})
			}
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(err.Error()).Should(gomega.ContainSubstring("must be nil when parent is empty"))
		})

		ginkgo.It("should admit only up to the lendingLimit amount", func() {
			ginkgo.By("submitting jobs to the borrower queue")
			for i := range 2 {
				job := testingjob.MakeJob(fmt.Sprintf("job-%d", i+1), ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "500m").
					TerminationGracePeriod(1).Obj()
				util.MustCreate(ctx, k8sClient, job)
			}

			ginkgo.By("verifying only one job is admitted up to the lendingLimit")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(borrowerCq), borrowerCq)).Should(gomega.Succeed())
				g.Expect(borrowerCq.Status.PendingWorkloads).Should(gomega.Equal(int32(1)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(borrowerCq), borrowerCq)).Should(gomega.Succeed())
				g.Expect(borrowerCq.Status.AdmittedWorkloads).Should(gomega.Equal(int32(1)))
				g.Expect(borrowerCq.Status.PendingWorkloads).Should(gomega.Equal(int32(1)))
				g.Expect(borrowerCq.Status.FlavorsUsage[0].Resources[0].Borrowed).Should(gomega.BeEquivalentTo(resource.MustParse("500m")))
				g.Expect(borrowerCq.Status.FlavorsUsage[0].Resources[0].Total).Should(gomega.BeEquivalentTo(resource.MustParse("500m")))
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())

			ginkgo.By("verifying lender CQ has no usage")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lenderCq), lenderCq)).Should(gomega.Succeed())
				g.Expect(lenderCq.Status.AdmittedWorkloads).Should(gomega.Equal(int32(0)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("borrowingLimit enforces one-directional borrowing between orgs", func() {
		var (
			rootCohort    *kueue.Cohort
			researchOrg   *kueue.Cohort
			productionOrg *kueue.Cohort
			researchCq    *kueue.ClusterQueue
			productionCq  *kueue.ClusterQueue
			researchLq    *kueue.LocalQueue
			productionLq  *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			rootCohort = utiltestingapi.MakeCohort(kueue.CohortReference("root-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "0").
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, rootCohort)

			researchOrg = utiltestingapi.MakeCohort(kueue.CohortReference("research-" + ns.Name)).
				Parent(kueue.CohortReference("root-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "0", "0", "").
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, researchOrg)

			productionOrg = utiltestingapi.MakeCohort(kueue.CohortReference("production-" + ns.Name)).
				Parent(kueue.CohortReference("root-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "0").
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, productionOrg)

			researchCq = utiltestingapi.MakeClusterQueue("research-cq-" + ns.Name).
				Cohort(kueue.CohortReference("research-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "1").
					Obj()).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, researchCq)

			productionCq = utiltestingapi.MakeClusterQueue("production-cq-" + ns.Name).
				Cohort(kueue.CohortReference("production-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "1").
					Obj()).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, productionCq)

			researchLq = utiltestingapi.MakeLocalQueue("research-lq", ns.Name).ClusterQueue(researchCq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, researchLq)

			productionLq = utiltestingapi.MakeLocalQueue("production-lq", ns.Name).ClusterQueue(productionCq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, productionLq)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, researchLq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, productionLq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, researchCq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, productionCq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, researchOrg, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, productionOrg, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, rootCohort, true)
		})

		ginkgo.It("should reject borrowingLimit on a root Cohort", func() {
			invalidCohort := utiltestingapi.MakeCohort(kueue.CohortReference("invalid-borrowing-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "1", "500m", "").
					Obj()).
				Obj()
			err := k8sClient.Create(ctx, invalidCohort)
			if err == nil {
				ginkgo.DeferCleanup(func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, invalidCohort, true)
				})
			}
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(err.Error()).Should(gomega.ContainSubstring("must be nil when parent is empty"))
		})

		ginkgo.It("should allow production to borrow but block research from borrowing", func() {
			ginkgo.By("submitting production jobs that borrow from idle research capacity")
			for i := range 3 {
				job := testingjob.MakeJob(fmt.Sprintf("prod-%d", i+1), ns.Name).
					Queue(kueue.LocalQueueName(productionLq.Name)).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "500m").
					TerminationGracePeriod(1).Obj()
				util.MustCreate(ctx, k8sClient, job)
			}

			ginkgo.By("verifying production admits all 3 jobs with borrowing")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(productionCq), productionCq)).Should(gomega.Succeed())
				g.Expect(productionCq.Status.AdmittedWorkloads).Should(gomega.Equal(int32(3)))
				g.Expect(productionCq.Status.FlavorsUsage[0].Resources[0].Borrowed).Should(gomega.BeEquivalentTo(resource.MustParse("500m")))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("submitting research jobs that cannot borrow due to borrowingLimit:0")
			for i := range 3 {
				job := testingjob.MakeJob(fmt.Sprintf("research-%d", i+1), ns.Name).
					Queue(kueue.LocalQueueName(researchLq.Name)).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "500m").
					TerminationGracePeriod(1).Obj()
				util.MustCreate(ctx, k8sClient, job)
			}

			ginkgo.By("verifying research admits only 1 job and cannot borrow")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(researchCq), researchCq)).Should(gomega.Succeed())
				g.Expect(researchCq.Status.PendingWorkloads).Should(gomega.Equal(int32(2)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(researchCq), researchCq)).Should(gomega.Succeed())
				g.Expect(researchCq.Status.AdmittedWorkloads).Should(gomega.Equal(int32(1)))
				g.Expect(researchCq.Status.PendingWorkloads).Should(gomega.Equal(int32(2)))
				g.Expect(researchCq.Status.FlavorsUsage[0].Resources[0].Borrowed).Should(gomega.BeEquivalentTo(resource.MustParse("0")))
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("isolated orgs share a root-level burst queue", func() {
		var (
			rootCohort *kueue.Cohort
			orgACohort *kueue.Cohort
			orgBCohort *kueue.Cohort
			orgACq     *kueue.ClusterQueue
			orgBCq     *kueue.ClusterQueue
			burstCq    *kueue.ClusterQueue
			orgALq     *kueue.LocalQueue
			burstLq    *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			rootCohort = utiltestingapi.MakeCohort(kueue.CohortReference("root-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "0").
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, rootCohort)

			orgACohort = utiltestingapi.MakeCohort(kueue.CohortReference("org-a-" + ns.Name)).
				Parent(kueue.CohortReference("root-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "0", "0", "").
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, orgACohort)

			orgBCohort = utiltestingapi.MakeCohort(kueue.CohortReference("org-b-" + ns.Name)).
				Parent(kueue.CohortReference("root-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "0", "0", "").
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, orgBCohort)

			orgACq = utiltestingapi.MakeClusterQueue("org-a-cq-" + ns.Name).
				Cohort(kueue.CohortReference("org-a-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "1").
					Obj()).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, orgACq)

			orgBCq = utiltestingapi.MakeClusterQueue("org-b-cq-" + ns.Name).
				Cohort(kueue.CohortReference("org-b-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "1").
					Obj()).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, orgBCq)

			burstCq = utiltestingapi.MakeClusterQueue("burst-cq-" + ns.Name).
				Cohort(kueue.CohortReference("root-" + ns.Name)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "0").
					Obj()).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, burstCq)

			orgALq = utiltestingapi.MakeLocalQueue("org-a-lq", ns.Name).ClusterQueue(orgACq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, orgALq)

			burstLq = utiltestingapi.MakeLocalQueue("burst-lq", ns.Name).ClusterQueue(burstCq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, burstLq)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, orgALq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, burstLq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, orgACq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, orgBCq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, burstCq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, orgACohort, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, orgBCohort, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, rootCohort, true)
		})

		ginkgo.It("should allow burst queue to borrow idle capacity while orgs stay isolated", func() {
			ginkgo.By("filling org-a and submitting an overflow job")
			for i := range 2 {
				job := testingjob.MakeJob(fmt.Sprintf("org-a-%d", i+1), ns.Name).
					Queue(kueue.LocalQueueName(orgALq.Name)).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "500m").
					TerminationGracePeriod(1).Obj()
				util.MustCreate(ctx, k8sClient, job)
			}
			overflowJob := testingjob.MakeJob("org-a-overflow", ns.Name).
				Queue(kueue.LocalQueueName(orgALq.Name)).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "500m").
				TerminationGracePeriod(1).Obj()
			util.MustCreate(ctx, k8sClient, overflowJob)

			ginkgo.By("verifying org-a is full and overflow is pending")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(orgACq), orgACq)).Should(gomega.Succeed())
				g.Expect(orgACq.Status.PendingWorkloads).Should(gomega.Equal(int32(1)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(orgACq), orgACq)).Should(gomega.Succeed())
				g.Expect(orgACq.Status.AdmittedWorkloads).Should(gomega.Equal(int32(2)))
				g.Expect(orgACq.Status.PendingWorkloads).Should(gomega.Equal(int32(1)))
				g.Expect(orgACq.Status.FlavorsUsage[0].Resources[0].Borrowed).Should(gomega.BeEquivalentTo(resource.MustParse("0")))
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())

			ginkgo.By("submitting burst jobs that borrow idle capacity from org-b")
			for i := range 2 {
				job := testingjob.MakeJob(fmt.Sprintf("burst-%d", i+1), ns.Name).
					Queue(kueue.LocalQueueName(burstLq.Name)).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "500m").
					TerminationGracePeriod(1).Obj()
				util.MustCreate(ctx, k8sClient, job)
			}

			ginkgo.By("verifying burst queue borrows while org-a stays isolated")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(burstCq), burstCq)).Should(gomega.Succeed())
				g.Expect(burstCq.Status.AdmittedWorkloads).Should(gomega.Equal(int32(2)))
				g.Expect(burstCq.Status.FlavorsUsage[0].Resources[0].Borrowed).Should(gomega.BeEquivalentTo(resource.MustParse("1")))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(orgACq), orgACq)).Should(gomega.Succeed())
				g.Expect(orgACq.Status.PendingWorkloads).Should(gomega.Equal(int32(1)))
				g.Expect(orgACq.Status.FlavorsUsage[0].Resources[0].Borrowed).Should(gomega.BeEquivalentTo(resource.MustParse("0")))
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})
	})
})
