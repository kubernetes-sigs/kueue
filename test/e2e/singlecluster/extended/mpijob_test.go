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
	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadmpijob "sigs.k8s.io/kueue/pkg/controller/jobs/mpijob"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingmpijob "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("MPIJob", ginkgo.Label("area:singlecluster", "feature:mpijob"), func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("Creating a MPIJob", func() {
		var (
			defaultRf    *kueue.ResourceFlavor
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)

		ginkgo.BeforeEach(func() {
			flavorDefault := "default-" + ns.Name
			clusterQueueName := "cluster-queue-" + ns.Name
			defaultRf = utiltestingapi.MakeResourceFlavor(flavorDefault).Obj()
			util.MustCreate(ctx, k8sClient, defaultRf)
			clusterQueue = utiltestingapi.MakeClusterQueue(clusterQueueName).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(flavorDefault).
						Resource(corev1.ResourceCPU, "2").
						Resource(corev1.ResourceMemory, "2G").Obj()).Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)
			localQueue = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue(clusterQueueName).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllMPIJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultRf, true)
		})

		ginkgo.It("Should run a MPIJob if admitted", func() {
			mpiJob := testingmpijob.MakeMPIJob("mpijob", ns.Name).
				Queue("main").
				MPIJobReplicaSpecs(
					testingmpijob.MPIJobReplicaSpecRequirement{
						ReplicaType:  kfmpi.MPIReplicaTypeLauncher,
						ReplicaCount: 1,
						Image:        util.GetAgnHostImage(),
						Args:         util.BehaviorExitFast,
					},
					testingmpijob.MPIJobReplicaSpecRequirement{
						ReplicaType:  kfmpi.MPIReplicaTypeWorker,
						ReplicaCount: 1,
						Image:        util.GetAgnHostImage(),
						Args:         util.BehaviorExitFast,
					},
				).
				RequestAndLimit(kfmpi.MPIReplicaTypeLauncher, corev1.ResourceCPU, "500m").
				RequestAndLimit(kfmpi.MPIReplicaTypeWorker, corev1.ResourceCPU, "100m").
				Obj()

			ginkgo.By("Creating the MPIJob", func() {
				util.MustCreate(ctx, k8sClient, mpiJob)
			})

			wlLookupKey := types.NamespacedName{Name: workloadmpijob.GetWorkloadNameForMPIJob(mpiJob.Name, mpiJob.UID), Namespace: ns.Name}

			ginkgo.By("Waiting for the MPIJob to finish", func() {
				util.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlLookupKey, util.LongTimeout)
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
			flavorOnDemand := "on-demand-" + ns.Name
			flavorSpot := "spot-" + ns.Name
			clusterQueueName := "cluster-queue-" + ns.Name
			onDemandRF = utiltestingapi.MakeResourceFlavor(flavorOnDemand).
				NodeLabel("instance-type", "on-demand").Obj()
			util.MustCreate(ctx, k8sClient, onDemandRF)
			spotRF = utiltestingapi.MakeResourceFlavor(flavorSpot).
				NodeLabel("instance-type", "spot").Obj()
			util.MustCreate(ctx, k8sClient, spotRF)
			clusterQueue = utiltestingapi.MakeClusterQueue(clusterQueueName).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(flavorOnDemand).
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas(flavorSpot).
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
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllMPIJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandRF, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, spotRF, true)
		})

		ginkgo.It("Should allow to suspend a MPIJob when the ClusterQueue is stopped with the HoldAndDrain policy", func() {
			mpiJob := testingmpijob.MakeMPIJob("mpijob-suspend", ns.Name).
				Queue("main").
				MPIJobReplicaSpecs(
					testingmpijob.MPIJobReplicaSpecRequirement{
						ReplicaType:  kfmpi.MPIReplicaTypeLauncher,
						ReplicaCount: 1,
						Image:        util.GetAgnHostImage(),
						Args:         util.BehaviorExitFast,
					},
					testingmpijob.MPIJobReplicaSpecRequirement{
						ReplicaType:  kfmpi.MPIReplicaTypeWorker,
						ReplicaCount: 1,
						Image:        util.GetAgnHostImage(),
						Args:         util.BehaviorExitFast,
					},
				).
				RequestAndLimit(kfmpi.MPIReplicaTypeLauncher, corev1.ResourceCPU, "100m").
				RequestAndLimit(kfmpi.MPIReplicaTypeWorker, corev1.ResourceCPU, "100m").
				Obj()

			ginkgo.By("Creating the mpiJob", func() {
				util.MustCreate(ctx, k8sClient, mpiJob)
			})

			ginkgo.By("Waiting for the mpiJob to be unsuspended", func() {
				jobKey := client.ObjectKeyFromObject(mpiJob)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobKey, mpiJob)).To(gomega.Succeed())
					g.Expect(mpiJob.Spec.RunPolicy.Suspend).Should(gomega.BeEquivalentTo(new(false)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verify the mpiJob has nodeSelector set", func() {
				gomega.Expect(mpiJob.Spec.MPIReplicaSpecs[kfmpi.MPIReplicaTypeLauncher].Template.Spec.NodeSelector).To(gomega.Equal(
					map[string]string{
						"instance-type": "on-demand",
					},
				))
			})

			ginkgo.By("Stopping the ClusterQueue to make the MPIJob be stopped and suspended")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).To(gomega.Succeed())
				clusterQueue.Spec.StopPolicy = new(kueue.HoldAndDrain)
				g.Expect(k8sClient.Update(ctx, clusterQueue)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Waiting for the mpiJob to be suspended", func() {
				jobKey := client.ObjectKeyFromObject(mpiJob)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobKey, mpiJob)).To(gomega.Succeed())
					g.Expect(mpiJob.Spec.RunPolicy.Suspend).Should(gomega.BeEquivalentTo(new(true)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
