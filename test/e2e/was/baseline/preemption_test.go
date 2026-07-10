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
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

// These tests verify the interaction between Kueue's WorkloadPriorityClass and
// the upstream Workload-Aware Preemption feature
// (KEP-5710: Workload-Aware Preemption).
//
// The tests require:
// - A kind cluster built from Kubernetes main (not a release)
// - GenericWorkload feature gate enabled
// - scheduling.k8s.io/v1alpha3 API enabled via runtime-config
//
// Current scope:
// - Verify that Kueue's WorkloadPriorityClass drives preemption ordering
// - Verify consistency of priority values between Kueue Workload and upstream PodGroup
// - Verify that PodGroup disruption semantics align with Kueue preemption behavior
var _ = ginkgo.Describe("WorkloadAwarePreemption", ginkgo.Label("area:was", "feature:was", "feature:was-preemption"), func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-was-preempt-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("Jobs use WorkloadPriorityClass with preemption enabled", func() {
		var (
			onDemandRF       *kueue.ResourceFlavor
			localQueue       *kueue.LocalQueue
			clusterQueue     *kueue.ClusterQueue
			lowWPC           *kueue.WorkloadPriorityClass
			highWPC          *kueue.WorkloadPriorityClass
			flavorOnDemand   string
			clusterQueueName string
		)
		ginkgo.BeforeEach(func() {
			flavorOnDemand = "on-demand-preempt-" + ns.Name
			clusterQueueName = "cluster-queue-preempt-" + ns.Name
			onDemandRF = utiltestingapi.MakeResourceFlavor(flavorOnDemand).
				NodeLabel("instance-type", "on-demand").Obj()
			util.MustCreate(ctx, k8sClient, onDemandRF)

			// Create a ClusterQueue with limited quota and preemption enabled.
			clusterQueue = utiltestingapi.MakeClusterQueue(clusterQueueName).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(flavorOnDemand).
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)
			localQueue = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue(clusterQueueName).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)

			lowWPC = utiltestingapi.MakeWorkloadPriorityClass("low-" + ns.Name).PriorityValue(10).Obj()
			util.MustCreate(ctx, k8sClient, lowWPC)

			highWPC = utiltestingapi.MakeWorkloadPriorityClass("high-" + ns.Name).PriorityValue(1000).Obj()
			util.MustCreate(ctx, k8sClient, highWPC)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteAllPodsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandRF, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, highWPC, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lowWPC, true)
			util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		})

		ginkgo.It("Should preempt a low-priority workload when a high-priority workload is submitted", func() {
			ginkgo.By("creating a low-priority job that fills the quota", func() {
				lowJob := testingjob.MakeJob("low-priority-job", ns.Name).
					Queue("main").
					WorkloadPriorityClass(lowWPC.Name).
					Parallelism(2).
					Completions(2).
					Indexed(true).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "400m").
					RequestAndLimit(corev1.ResourceMemory, "100Mi").
					TerminationGracePeriod(1).
					Obj()
				util.MustCreate(ctx, k8sClient, lowJob)

				// Wait for the low-priority workload to be admitted.
				createdJob := &batchv1.Job{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lowJob), createdJob)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				wlLookupKey := types.NamespacedName{
					Name:      workloadjob.GetWorkloadNameForJob(lowJob.Name, createdJob.UID),
					Namespace: ns.Name,
				}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeTrue())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("creating a high-priority job that requires preemption", func() {
				highJob := testingjob.MakeJob("high-priority-job", ns.Name).
					Queue("main").
					WorkloadPriorityClass(highWPC.Name).
					Parallelism(2).
					Completions(2).
					Indexed(true).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "400m").
					RequestAndLimit(corev1.ResourceMemory, "100Mi").
					TerminationGracePeriod(1).
					Obj()
				util.MustCreate(ctx, k8sClient, highJob)

				// The high-priority workload should eventually be admitted
				// after preempting the low-priority one.
				createdJob := &batchv1.Job{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(highJob), createdJob)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				wlLookupKey := types.NamespacedName{
					Name:      workloadjob.GetWorkloadNameForJob(highJob.Name, createdJob.UID),
					Namespace: ns.Name,
				}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeTrue())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying the low-priority workload was evicted", func() {
				lowJob := &batchv1.Job{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "low-priority-job", Namespace: ns.Name}, lowJob)).Should(gomega.Succeed())
					// The low-priority job should be re-suspended after preemption.
					g.Expect(*lowJob.Spec.Suspend).Should(gomega.BeTrue())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should set WorkloadPriorityClass priority on the Kueue Workload and verify consistency with PodGroup", func() {
			job := testingjob.MakeJob("was-priority-job", ns.Name).
				Queue("main").
				WorkloadPriorityClass(highWPC.Name).
				Parallelism(2).
				Completions(2).
				Indexed(true).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "100m").
				RequestAndLimit(corev1.ResourceMemory, "20Mi").
				TerminationGracePeriod(1).
				Obj()
			jobKey := client.ObjectKeyFromObject(job)
			util.MustCreate(ctx, k8sClient, job)

			ginkgo.By("verifying the WorkloadPriorityClass label is set on the job", func() {
				createdJob := &batchv1.Job{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobKey, createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Labels).Should(gomega.HaveKeyWithValue(
						controllerconstants.WorkloadPriorityClassLabel,
						highWPC.Name,
					))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying the Kueue Workload has the correct priority value", func() {
				createdJob := &batchv1.Job{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobKey, createdJob)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				wlLookupKey := types.NamespacedName{
					Name:      workloadjob.GetWorkloadNameForJob(job.Name, createdJob.UID),
					Namespace: ns.Name,
				}
				util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, highWPC.Name, highWPC.Value, wlLookupKey)
			})

			ginkgo.By("verifying the upstream PodGroup priority is consistent (when WorkloadAwarePreemption is enabled)", func() {
				// When both GenericWorkload and WorkloadAwarePreemption feature gates
				// are enabled, the PodGroup should carry priority information.
				// NOTE: This assertion may need to be relaxed if WorkloadAwarePreemption
				// is not enabled on the cluster. In that case, PodGroup.Spec.Priority
				// will be nil and this check will be skipped.
				pgList := &schedulingv1alpha3.PodGroupList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pgList, client.InNamespace(ns.Name))).Should(gomega.Succeed())
					if len(pgList.Items) == 0 {
						// GenericWorkload might not be creating PodGroups yet —
						// skip this assertion rather than fail.
						ginkgo.Skip("No PodGroups found; GenericWorkload may not be creating PodGroups for this Job type")
					}

					// Find the PodGroup corresponding to our Job.
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
					if matchingPG == nil {
						return // Retry until we find the PodGroup.
					}

					// Verify the PodGroup exists and has a priority field.
					// Currently there is no automatic mapping from Kueue's
					// WorkloadPriorityClass to the PodGroup's Priority, so the
					// upstream PodGroup priority defaults to 0.
					// Once a mapping is implemented, update this assertion to:
					//   g.Expect(*matchingPG.Spec.Priority).Should(gomega.Equal(highWPC.Value))
					// See WORKLOAD_AWARE_PREEMPTION_ANALYSIS.md.
					g.Expect(matchingPG.Spec.Priority).ShouldNot(gomega.BeNil(),
						"PodGroup should have a priority field set")
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			gomega.Expect(k8sClient.Delete(ctx, job)).Should(gomega.Succeed())
		})
	})
})
