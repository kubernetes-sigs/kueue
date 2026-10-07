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

package wasapi

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

// These tests verify integration between Kueue's Workload and a Job admitted
// with the WorkloadWithJob feature gate enabled (KEP-5547: Integrate Workload
// with Job), which builds on the GenericWorkload prerequisite gate.
//
// The tests require:
//   - A kind cluster running Kubernetes 1.37 or newer, either a release
//     (make test-e2e-was) or k/k main (make test-e2e-k8s-main-was)
//   - The GenericWorkload and WorkloadWithJob feature gates enabled
//   - The scheduling.k8s.io/v1beta1 API enabled via runtime-config
//
// See patch_kind_config_for_was in hack/testing/e2e-common.sh.
var _ = ginkgo.Describe("WorkloadAwareScheduling Job", ginkgo.Label("area:was", "feature:was", "feature:was-job"), func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-was-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
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
			behavioral.MustCreate(ctx, k8sClient, onDemandRF)
			clusterQueue = utiltestingapi.MakeClusterQueue(clusterQueueName).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(flavorOnDemand).
						Resource(corev1.ResourceCPU, "4").
						Resource(corev1.ResourceMemory, "4Gi").
						Obj(),
				).
				Obj()
			behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)
			localQueue = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue(clusterQueueName).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(behavioral.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(behavioral.DeleteAllPodsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandRF, true)
			behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		})

		ginkgo.It("Should create a Workload with PodSet count matching Job parallelism", func() {
			const parallelism int32 = 3

			job := testingjob.MakeJob("was-test-job", ns.Name).
				Queue("main").
				Parallelism(parallelism).
				Completions(parallelism).
				Indexed(true).
				Scheduling(&batchv1.JobSchedulingConfiguration{
					SchedulingPolicy: &schedulingv1alpha3.WorkloadPodGroupSchedulingPolicy{
						Gang: &schedulingv1alpha3.WorkloadPodGroupGangSchedulingPolicy{},
					},
				}).
				Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				RequestAndLimit(corev1.ResourceMemory, "20Mi").
				TerminationGracePeriod(1).
				Obj()
			jobKey := client.ObjectKeyFromObject(job)
			behavioral.MustCreate(ctx, k8sClient, job)

			ginkgo.By("verifying that the Kueue Workload is created with matching pod count", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdWorkload := workloadForJob(g, jobKey)
					g.Expect(createdWorkload.Spec.PodSets).Should(gomega.HaveLen(1))
					g.Expect(createdWorkload.Spec.PodSets[0].Count).Should(gomega.Equal(parallelism))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying the workload is admitted and the job is unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdWorkload := workloadForJob(g, jobKey)
					g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeTrue())
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())

				createdJob := &batchv1.Job{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobKey, createdJob)).Should(gomega.Succeed())
					g.Expect(*createdJob.Spec.Suspend).Should(gomega.BeFalse())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying the upstream PodGroup gang minCount matches the Kueue Workload pod count", func() {
				// The Job requests gang scheduling via spec.scheduling.schedulingPolicy.gang
				// without a minCount, so the upstream Job controller creates a PodGroup
				// owned by the Job with minCount defaulted to the Job's parallelism.
				gomega.Eventually(func(g gomega.Gomega) {
					createdWorkload := workloadForJob(g, jobKey)
					g.Expect(createdWorkload.Spec.PodSets).Should(gomega.HaveLen(1))

					minCount, found, err := gangMinCountForJob(ns.Name, job.Name)
					g.Expect(err).ShouldNot(gomega.HaveOccurred())
					g.Expect(found).Should(gomega.BeTrue(), "expected a PodGroup owned by the Job with a gang scheduling policy")
					g.Expect(minCount).Should(
						gomega.Equal(createdWorkload.Spec.PodSets[0].Count),
						"PodGroup gang minCount should match the Kueue Workload pod count",
					)
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})
	})
})

// workloadForJob fetches the Job identified by jobKey and returns the Kueue
// Workload owned by it, asserting both fetches succeed. It is intended for
// use inside a gomega.Eventually poll, since the Job's UID (needed to derive
// the Workload name) and the Workload itself may not be available yet.
func workloadForJob(g gomega.Gomega, jobKey types.NamespacedName) *kueue.Workload {
	createdJob := &batchv1.Job{}
	g.Expect(k8sClient.Get(ctx, jobKey, createdJob)).Should(gomega.Succeed())

	wlLookupKey := types.NamespacedName{
		Name:      workloadjob.GetWorkloadNameForJob(jobKey.Name, createdJob.UID),
		Namespace: jobKey.Namespace,
	}
	createdWorkload := &kueue.Workload{}
	g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
	return createdWorkload
}

// gangMinCountForJob returns the gang scheduling minCount of the upstream
// PodGroup owned by the given Job name, if one exists.
func gangMinCountForJob(namespace, jobName string) (int32, bool, error) {
	podGroupList := &schedulingv1beta1.PodGroupList{}
	if err := k8sClient.List(ctx, podGroupList, client.InNamespace(namespace)); err != nil {
		return 0, false, err
	}

	for i := range podGroupList.Items {
		pg := &podGroupList.Items[i]
		for _, ownerRef := range pg.OwnerReferences {
			if ownerRef.Kind == "Job" && ownerRef.Name == jobName {
				if gang := pg.Spec.SchedulingPolicy.Gang; gang != nil {
					return gang.MinCount, true, nil
				}
				return 0, false, nil
			}
		}
	}
	return 0, false, nil
}
