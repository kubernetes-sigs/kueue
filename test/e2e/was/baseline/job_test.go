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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

// These tests verify integration between Kueue's Workload and the upstream
// Kubernetes scheduling.k8s.io/v1alpha3 Workload and PodGroup APIs
// (KEP-5547: Integrate Workload with Job).
//
// The tests require:
// - A kind cluster built from Kubernetes main (not a release)
// - The GenericWorkload feature gate enabled
// - The scheduling.k8s.io/v1alpha3 API enabled via runtime-config
// See hack/testing/kind-cluster-was.yaml.
var _ = ginkgo.Describe("WorkloadAwareScheduling Job", ginkgo.Label("area:was", "feature:was", "feature:was-job"), func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-was-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("A Job is admitted by Kueue with the GenericWorkload feature gate enabled", func() {
		var (
			onDemandRF       *kueue.ResourceFlavor
			localQueue       *kueue.LocalQueue
			clusterQueue     *kueue.ClusterQueue
			flavorOnDemand   string
			clusterQueueName string
		)
		ginkgo.BeforeEach(func() {
			flavorOnDemand = "on-demand-was-" + ns.Name
			clusterQueueName = "cluster-queue-was-" + ns.Name
			onDemandRF = utiltestingapi.MakeResourceFlavor(flavorOnDemand).
				NodeLabel("instance-type", "on-demand").Obj()
			util.MustCreate(ctx, k8sClient, onDemandRF)
			clusterQueue = utiltestingapi.MakeClusterQueue(clusterQueueName).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(flavorOnDemand).
						Resource(corev1.ResourceCPU, "4").
						Resource(corev1.ResourceMemory, "4Gi").
						Obj(),
				).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)
			localQueue = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue(clusterQueueName).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteAllPodsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandRF, true)
			util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		})

		ginkgo.It("Should create a Workload with PodSet count matching Job parallelism", func() {
			const parallelism int32 = 3

			// The upstream Job controller only creates PodGroups for Jobs that
			// qualify for gang scheduling: parallelism > 1, Indexed completion
			// mode, and completions == parallelism.
			// TODO: once KEP-5547 goes beta, this will actually be no longer valid.
			// There should be a new API to allow for tuning gang scheduling at the API level.
			// This test will need to be updated once that merges.
			job := testingjob.MakeJob("was-test-job", ns.Name).
				Queue("main").
				Parallelism(parallelism).
				Completions(parallelism).
				Indexed(true).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "100m").
				RequestAndLimit(corev1.ResourceMemory, "20Mi").
				TerminationGracePeriod(1).
				Obj()
			jobKey := client.ObjectKeyFromObject(job)
			util.MustCreate(ctx, k8sClient, job)

			ginkgo.By("verifying that the Kueue Workload is created with matching pod count", func() {
				createdJob := &batchv1.Job{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobKey, createdJob)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				wlLookupKey := types.NamespacedName{
					Name:      workloadjob.GetWorkloadNameForJob(job.Name, createdJob.UID),
					Namespace: ns.Name,
				}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Spec.PodSets).Should(gomega.HaveLen(1))
					g.Expect(createdWorkload.Spec.PodSets[0].Count).Should(gomega.Equal(parallelism))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying the workload is admitted and the job is unsuspended", func() {
				createdJob := &batchv1.Job{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobKey, createdJob)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				wlLookupKey := types.NamespacedName{
					Name:      workloadjob.GetWorkloadNameForJob(job.Name, createdJob.UID),
					Namespace: ns.Name,
				}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeTrue())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobKey, createdJob)).Should(gomega.Succeed())
					g.Expect(*createdJob.Spec.Suspend).Should(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying the upstream scheduling.k8s.io/v1alpha3 PodGroup exists with matching pod count", func() {
				// NOTE: The upstream Job controller may not yet create PodGroups
				// as part of KEP-5547. If no PodGroups appear, skip rather than fail.
				pgList := &schedulingv1alpha3.PodGroupList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pgList, client.InNamespace(ns.Name))).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				// Find the PodGroup that corresponds to our Job.
				var matchingPG *schedulingv1alpha3.PodGroup
				for i := range pgList.Items {
					pg := &pgList.Items[i]
					for _, ownerRef := range pg.OwnerReferences {
						if ownerRef.Kind == "Job" && ownerRef.Name == job.Name {
							matchingPG = pg
							break
						}
					}
					if matchingPG == nil && pg.Spec.PodGroupTemplateRef != nil &&
						pg.Spec.PodGroupTemplateRef.Workload != nil {
						matchingPG = pg
					}
					if matchingPG != nil {
						break
					}
				}
				gomega.Expect(matchingPG).ShouldNot(gomega.BeNil(), "expected a PodGroup matching the Job")

				if matchingPG.Spec.SchedulingPolicy.Gang != nil {
					gomega.Expect(matchingPG.Spec.SchedulingPolicy.Gang.MinCount).Should(
						gomega.Equal(parallelism),
						"PodGroup gang minCount should match Job parallelism",
					)
				}
			})

			gomega.Expect(k8sClient.Delete(ctx, job)).Should(gomega.Succeed())
		})

		ginkgo.It("Should have consistent pod counts between Kueue Workload and Job with completions", func() {
			const (
				parallelism int32 = 2
				completions int32 = 5
			)

			job := testingjob.MakeJob("was-completion-job", ns.Name).
				Queue("main").
				Parallelism(parallelism).
				Completions(completions).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "100m").
				RequestAndLimit(corev1.ResourceMemory, "20Mi").
				TerminationGracePeriod(1).
				Obj()
			jobKey := client.ObjectKeyFromObject(job)
			util.MustCreate(ctx, k8sClient, job)

			ginkgo.By("verifying the Workload PodSet count matches the Job parallelism (not completions)", func() {
				createdJob := &batchv1.Job{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobKey, createdJob)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				wlLookupKey := types.NamespacedName{
					Name:      workloadjob.GetWorkloadNameForJob(job.Name, createdJob.UID),
					Namespace: ns.Name,
				}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Spec.PodSets).Should(gomega.HaveLen(1))
					// Kueue uses min(parallelism, completions) as the pod count.
					// Since parallelism=2 < completions=5, the count should be 2.
					g.Expect(createdWorkload.Spec.PodSets[0].Count).Should(gomega.Equal(parallelism))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			gomega.Expect(k8sClient.Delete(ctx, job)).Should(gomega.Succeed())
		})
	})
})
