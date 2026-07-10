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

package extended

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjobset "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

// These tests verify how the upstream scheduling.k8s.io/v1alpha3 WAS resources
// interact with JobSets admitted by Kueue.
//
// Key finding: the upstream kube-controller-manager creates WAS Workload
// resources for JobSet's child Jobs when those child Jobs qualify for gang
// scheduling (parallelism > 1, completionMode: Indexed, completions == parallelism)
// under KEP-5547 (WorkloadWithJob feature gate). Child Jobs that do NOT qualify
// (e.g. parallelism=1) do not get WAS resources.
//
// The tests require:
// - A kind cluster built from Kubernetes main (not a release)
// - The GenericWorkload feature gate enabled
// - The scheduling.k8s.io/v1alpha3 API enabled via runtime-config
// See hack/testing/kind-cluster-was.yaml.
var _ = ginkgo.Describe("WorkloadAwareScheduling JobSet", ginkgo.Label("area:was", "feature:was", "feature:was-jobset"), func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-was-js-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("A JobSet is admitted by Kueue with the GenericWorkload feature gate enabled", func() {
		var (
			onDemandRF       *kueue.ResourceFlavor
			localQueue       *kueue.LocalQueue
			clusterQueue     *kueue.ClusterQueue
			flavorOnDemand   string
			clusterQueueName string
		)
		ginkgo.BeforeEach(func() {
			flavorOnDemand = "on-demand-was-js-" + ns.Name
			clusterQueueName = "cluster-queue-was-js-" + ns.Name
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
			gomega.Expect(util.DeleteAllJobSetsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteAllPodsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandRF, true)
			util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		})

		ginkgo.It("Should create upstream WAS Workloads for qualifying child Jobs of an admitted JobSet", func() {
			const parallelism int32 = 3

			jobSet := testingjobset.MakeJobSet("was-jobset-test", ns.Name).
				Queue("main").
				ReplicatedJobs(
					testingjobset.ReplicatedJobRequirements{
						Name:        "workers",
						Image:       util.GetAgnHostImage(),
						Args:        util.BehaviorWaitForDeletion,
						Replicas:    1,
						Parallelism: parallelism,
						Completions: parallelism,
					},
				).
				RequestAndLimit("workers", corev1.ResourceCPU, "100m").
				RequestAndLimit("workers", corev1.ResourceMemory, "20Mi").
				Obj()
			jobSetKey := client.ObjectKeyFromObject(jobSet)
			util.MustCreate(ctx, k8sClient, jobSet)

			ginkgo.By("verifying that the Kueue Workload is created and admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobSetKey, jobSet)).Should(gomega.Succeed())
					g.Expect(jobSet.UID).ShouldNot(gomega.BeEmpty())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				wlLookupKey := types.NamespacedName{
					Name:      workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID),
					Namespace: ns.Name,
				}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeTrue())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying the JobSet is unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobSetKey, jobSet)).Should(gomega.Succeed())
					g.Expect(*jobSet.Spec.Suspend).Should(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying an upstream scheduling.k8s.io/v1alpha3 Workload is created for the child Job", func() {
				// The child Job (parallelism=3, completions=3, Indexed) qualifies
				// for gang scheduling, so the upstream kube-controller-manager
				// creates a WAS Workload owned by the child Job.
				wlList := &schedulingv1alpha3.WorkloadList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, wlList, client.InNamespace(ns.Name))).Should(gomega.Succeed())
					g.Expect(wlList.Items).Should(gomega.HaveLen(1),
						"expected exactly 1 upstream WAS Workload for the single qualifying child Job")

					// Verify the WAS Workload is owned by the child Job, not the JobSet.
					wl := &wlList.Items[0]
					g.Expect(wl.OwnerReferences).Should(gomega.HaveLen(1))
					g.Expect(wl.OwnerReferences[0].Kind).Should(gomega.Equal("Job"))
					g.Expect(wl.OwnerReferences[0].Name).Should(gomega.HavePrefix("was-jobset-test-workers-"))

					// Verify the PodGroupTemplate has the correct gang minCount.
					g.Expect(wl.Spec.PodGroupTemplates).Should(gomega.HaveLen(1))
					g.Expect(wl.Spec.PodGroupTemplates[0].SchedulingPolicy.Gang).ShouldNot(gomega.BeNil())
					g.Expect(wl.Spec.PodGroupTemplates[0].SchedulingPolicy.Gang.MinCount).Should(
						gomega.Equal(parallelism),
						"WAS Workload gang minCount should match child Job parallelism",
					)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			gomega.Expect(k8sClient.Delete(ctx, jobSet)).Should(gomega.Succeed())
		})

		ginkgo.It("Should create WAS Workloads only for qualifying child Jobs in a multi-replica JobSet", func() {
			const workerParallelism int32 = 2

			jobSet := testingjobset.MakeJobSet("was-jobset-multi", ns.Name).
				Queue("main").
				ReplicatedJobs(
					testingjobset.ReplicatedJobRequirements{
						Name:        "driver",
						Image:       util.GetAgnHostImage(),
						Args:        util.BehaviorWaitForDeletion,
						Replicas:    1,
						Parallelism: 1,
						Completions: 1,
					},
					testingjobset.ReplicatedJobRequirements{
						Name:        "workers",
						Image:       util.GetAgnHostImage(),
						Args:        util.BehaviorWaitForDeletion,
						Replicas:    2,
						Parallelism: workerParallelism,
						Completions: workerParallelism,
					},
				).
				RequestAndLimit("driver", corev1.ResourceCPU, "100m").
				RequestAndLimit("driver", corev1.ResourceMemory, "20Mi").
				RequestAndLimit("workers", corev1.ResourceCPU, "100m").
				RequestAndLimit("workers", corev1.ResourceMemory, "20Mi").
				Obj()
			jobSetKey := client.ObjectKeyFromObject(jobSet)
			util.MustCreate(ctx, k8sClient, jobSet)

			ginkgo.By("verifying that the Kueue Workload is created and admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobSetKey, jobSet)).Should(gomega.Succeed())
					g.Expect(jobSet.UID).ShouldNot(gomega.BeEmpty())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				wlLookupKey := types.NamespacedName{
					Name:      workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID),
					Namespace: ns.Name,
				}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeTrue())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying upstream WAS Workloads are created only for the worker child Jobs", func() {
				// The driver Job (parallelism=1) does NOT qualify for gang scheduling,
				// so no WAS Workload is created for it.
				// The 2 worker replica Jobs (parallelism=2, completions=2, Indexed)
				// each qualify, so 2 WAS Workloads should be created.
				wlList := &schedulingv1alpha3.WorkloadList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, wlList, client.InNamespace(ns.Name))).Should(gomega.Succeed())
					g.Expect(wlList.Items).Should(gomega.HaveLen(2),
						"expected 2 upstream WAS Workloads (one per worker replica Job, none for the driver)")

					for _, wl := range wlList.Items {
						// Each WAS Workload should be owned by a worker child Job.
						g.Expect(wl.OwnerReferences).Should(gomega.HaveLen(1))
						g.Expect(wl.OwnerReferences[0].Kind).Should(gomega.Equal("Job"))
						g.Expect(wl.OwnerReferences[0].Name).Should(gomega.HavePrefix("was-jobset-multi-workers-"))

						// Each should have a PodGroupTemplate with gang minCount matching worker parallelism.
						g.Expect(wl.Spec.PodGroupTemplates).Should(gomega.HaveLen(1))
						g.Expect(wl.Spec.PodGroupTemplates[0].SchedulingPolicy.Gang).ShouldNot(gomega.BeNil())
						g.Expect(wl.Spec.PodGroupTemplates[0].SchedulingPolicy.Gang.MinCount).Should(
							gomega.Equal(workerParallelism),
							"WAS Workload gang minCount should match worker Job parallelism",
						)
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying no upstream WAS Workload exists for the driver child Job", func() {
				// Confirm that no WAS Workload references the driver Job.
				wlList := &schedulingv1alpha3.WorkloadList{}
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, wlList, client.InNamespace(ns.Name))).Should(gomega.Succeed())
					for _, wl := range wlList.Items {
						for _, ownerRef := range wl.OwnerReferences {
							g.Expect(ownerRef.Name).ShouldNot(gomega.HavePrefix("was-jobset-multi-driver-"),
								"no WAS Workload should be created for the driver Job (parallelism=1)")
						}
					}
				}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
			})

			gomega.Expect(k8sClient.Delete(ctx, jobSet)).Should(gomega.Succeed())
		})
	})
})
