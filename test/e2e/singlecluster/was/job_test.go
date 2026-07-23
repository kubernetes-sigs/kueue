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

package was

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

// These tests verify integration between Kueue's Workload and a Job admitted
// with the WorkloadWithJob feature gate enabled (KEP-5547: Integrate Workload
// with Job), which builds on the GenericWorkload prerequisite gate.
//
// The tests require:
// - A kind cluster built from Kubernetes main (not a release)
// - The GenericWorkload and WorkloadWithJob feature gates enabled
// - The scheduling.k8s.io/v1beta1 API enabled via runtime-config
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

		ginkgo.XIt("Should create a Workload with PodSet count matching Job parallelism", func() {
			const parallelism int32 = 3

			// This job is being skipped because in 1.37 the job controller has an
			// api to control gang scheduling.
			// The default behavior is not have gang scheduling so PodSet will not match podgroup mincount
			// unless we bring in 1.37 apis to create the right job.
			// Skipping for now but will reenable once we have 1.37 apis.
			job := testingjob.MakeJob("was-test-job", ns.Name).
				Queue("main").
				Parallelism(parallelism).
				Completions(parallelism).
				Indexed(true).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				RequestAndLimit(corev1.ResourceMemory, "20Mi").
				TerminationGracePeriod(1).
				Obj()
			jobKey := client.ObjectKeyFromObject(job)
			util.MustCreate(ctx, k8sClient, job)

			ginkgo.By("verifying that the Kueue Workload is created with matching pod count", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdWorkload := workloadForJob(g, jobKey)
					g.Expect(createdWorkload.Spec.PodSets).Should(gomega.HaveLen(1))
					g.Expect(createdWorkload.Spec.PodSets[0].Count).Should(gomega.Equal(parallelism))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying the workload is admitted and the job is unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdWorkload := workloadForJob(g, jobKey)
					g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeTrue())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())

				createdJob := &batchv1.Job{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobKey, createdJob)).Should(gomega.Succeed())
					g.Expect(*createdJob.Spec.Suspend).Should(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying the upstream PodGroup gang minCount matches the Kueue Workload pod count", func() {
				// The Job qualifies for gang scheduling (parallelism > 1, Indexed,
				// completions == parallelism), so the upstream Job controller creates
				// a PodGroup owned by the Job. We read it as unstructured data to
				// avoid vendoring k8s.io/api/scheduling/v1beta1 into Kueue.
				gomega.Eventually(func(g gomega.Gomega) {
					createdWorkload := workloadForJob(g, jobKey)
					g.Expect(createdWorkload.Spec.PodSets).Should(gomega.HaveLen(1))

					minCount, found, err := gangMinCountForJob(ns.Name, job.Name)
					g.Expect(err).ShouldNot(gomega.HaveOccurred())
					g.Expect(found).Should(gomega.BeTrue(), "expected a PodGroup owned by the Job with a gang scheduling policy")
					g.Expect(minCount).Should(
						gomega.Equal(int64(createdWorkload.Spec.PodSets[0].Count)),
						"PodGroup gang minCount should match the Kueue Workload pod count",
					)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})

// podGroupListGVK identifies the upstream scheduling.k8s.io/v1beta1 PodGroup
// list kind. We deliberately query it as unstructured data (instead of
// importing k8s.io/api/scheduling/v1beta1) so that Kueue does not need to
// vendor that API just to observe it in this e2e test.
// TODO: once Kueue can depend on a k8s.io/api release that vendors the beta
// types (Kubernetes 1.37), switch this back to a structured client using the
// typed PodGroup/PodGroupList types.
var podGroupListGVK = schema.GroupVersionKind{
	Group:   "scheduling.k8s.io",
	Version: "v1beta1",
	Kind:    "PodGroupList",
}

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
func gangMinCountForJob(namespace, jobName string) (int64, bool, error) {
	podGroupList := &unstructured.UnstructuredList{}
	podGroupList.SetGroupVersionKind(podGroupListGVK)
	if err := k8sClient.List(ctx, podGroupList, client.InNamespace(namespace)); err != nil {
		return 0, false, err
	}

	for i := range podGroupList.Items {
		pg := &podGroupList.Items[i]
		for _, ownerRef := range pg.GetOwnerReferences() {
			if ownerRef.Kind == "Job" && ownerRef.Name == jobName {
				return unstructured.NestedInt64(pg.Object, "spec", "schedulingPolicy", "gang", "minCount")
			}
		}
	}
	return 0, false, nil
}
