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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/jobset/pkg/constants"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	workloadjobset "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("JobSet", func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})
	ginkgo.When("Creating a JobSet", func() {
		var (
			defaultRf    *kueue.ResourceFlavor
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)
		ginkgo.BeforeEach(func() {
			defaultRf = testing.MakeResourceFlavor("default").Obj()
			util.MustCreate(ctx, k8sClient, defaultRf)
			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas(defaultRf.Name).
						Resource(corev1.ResourceCPU, "2").
						Resource(corev1.ResourceMemory, "2G").Obj()).Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue)
			localQueue = testing.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllJobSetsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			// Force remove workloads to be sure that cluster queue can be removed.
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultRf, true)
			util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		})

		ginkgo.It("Should run a jobSet if admitted", func() {
			jobSet := testingjobset.MakeJobSet("job-set", ns.Name).
				Queue("main").
				ReplicatedJobs(
					testingjobset.ReplicatedJobRequirements{
						Name:        "replicated-job-1",
						Image:       util.E2eTestAgnHostImage,
						Args:        util.BehaviorExitFast,
						Replicas:    2,
						Parallelism: 2,
						Completions: 2,
					},
				).
				RequestAndLimit("replicated-job-1", "cpu", "500m").
				RequestAndLimit("replicated-job-1", "memory", "200M").
				Obj()

			ginkgo.By("Creating the jobSet", func() {
				util.MustCreate(ctx, k8sClient, jobSet)
			})

			createdLeaderWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: ns.Name}

			ginkgo.By("Waiting for the jobSet to finish", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdLeaderWorkload)).To(gomega.Succeed())
					g.Expect(apimeta.FindStatusCondition(createdLeaderWorkload.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeComparableTo(&metav1.Condition{
						Type:    kueue.WorkloadFinished,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadFinishedReasonSucceeded,
						Message: constants.AllJobsCompletedMessage,
					}, util.IgnoreConditionTimestampsAndObservedGeneration))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("Using resource flavors with node selectors", func() {
		var (
			onDemandRF   *kueue.ResourceFlavor
			spotRF       *kueue.ResourceFlavor
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)
		ginkgo.BeforeEach(func() {
			onDemandRF = testing.MakeResourceFlavor("on-demand").
				NodeLabel("instance-type", "on-demand").Obj()
			util.MustCreate(ctx, k8sClient, onDemandRF)
			spotRF = testing.MakeResourceFlavor("spot").
				NodeLabel("instance-type", "spot").Obj()
			util.MustCreate(ctx, k8sClient, spotRF)
			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
					*testing.MakeFlavorQuotas("spot").
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue)
			localQueue = testing.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllJobSetsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			// Force remove workloads to be sure that cluster queue can be removed.
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandRF, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, spotRF, true)
			util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		})

		ginkgo.It("Should allow to suspend a JobSet when injected nodeSelector", func() {
			jobSet := testingjobset.MakeJobSet("job-set-suspend", ns.Name).
				Queue("main").
				ReplicatedJobs(
					testingjobset.ReplicatedJobRequirements{
						Name:        "replicated-job-1",
						Image:       util.E2eTestAgnHostImage,
						Args:        util.BehaviorExitFast,
						Replicas:    1,
						Parallelism: 1,
						Completions: 1,
					},
				).
				RequestAndLimit("replicated-job-1", "cpu", "500m").
				RequestAndLimit("replicated-job-1", "memory", "200M").
				Obj()

			ginkgo.By("Creating the jobSet", func() {
				util.MustCreate(ctx, k8sClient, jobSet)
			})

			ginkgo.By("Waiting for the jobSet to be unsuspended", func() {
				jobKey := client.ObjectKeyFromObject(jobSet)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobKey, jobSet)).To(gomega.Succeed())
					g.Expect(jobSet.Spec.Suspend).Should(gomega.BeEquivalentTo(ptr.To(false)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verify the jobSet has nodeSelector set", func() {
				gomega.Expect(jobSet.Spec.ReplicatedJobs[0].Template.Spec.Template.Spec.NodeSelector).To(gomega.Equal(
					map[string]string{
						"instance-type": "on-demand",
					},
				))
			})

			ginkgo.By("Stopping the ClusterQueue to make the JobSet be stopped and suspended")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).To(gomega.Succeed())
				clusterQueue.Spec.StopPolicy = ptr.To(kueue.HoldAndDrain)
				g.Expect(k8sClient.Update(ctx, clusterQueue)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Waiting for the jobSet to be suspended", func() {
				jobKey := client.ObjectKeyFromObject(jobSet)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobKey, jobSet)).To(gomega.Succeed())
					g.Expect(jobSet.Spec.Suspend).Should(gomega.BeEquivalentTo(ptr.To(true)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
