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
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

func expectMultiKueueClustersToBeActive(clusters ...*kueue.MultiKueueCluster) {
	ginkgo.GinkgoHelper()
	readCluster := &kueue.MultiKueueCluster{}
	gomega.Eventually(func(g gomega.Gomega) {
		for _, cluster := range clusters {
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(cluster), readCluster)).To(gomega.Succeed())
			g.Expect(readCluster.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.MultiKueueClusterActive))
		}
	}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed(), behavioral.AssertMsg("MultiKueueClusters did not become active", readCluster))
}

func expectCapacityProviderCapacity(cpKey client.ObjectKey, want *kueuealpha.CapacityProviderNormalizedCapacity) {
	ginkgo.GinkgoHelper()
	cp := &kueuealpha.CapacityProvider{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sManagerClient.Get(ctx, cpKey, cp)).To(gomega.Succeed())
		g.Expect(cp.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(
			kueuealpha.CapacityProviderCapacitySynchronized,
			kueuealpha.CapacityProviderReasonSynchronized,
		))
		cond := apimeta.FindStatusCondition(cp.Status.Conditions, kueuealpha.CapacityProviderCapacitySynchronized)
		g.Expect(cond.ObservedGeneration).To(gomega.Equal(cp.Generation))
		g.Expect(cp.Status.Capacity).To(gomega.BeComparableTo(want))
	}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed(), behavioral.AssertMsg("CapacityProvider capacity did not match", cp))
}

func expectDynamicQuotaOrchestratorDistributed(dqoKey client.ObjectKey) {
	ginkgo.GinkgoHelper()
	dqo := &kueuealpha.DynamicQuotaOrchestrator{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sManagerClient.Get(ctx, dqoKey, dqo)).To(gomega.Succeed())
		g.Expect(dqo.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(
			kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
			kueuealpha.DynamicQuotaOrchestratorReasonComputed,
		))
		g.Expect(dqo.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(
			kueuealpha.DynamicQuotaOrchestratorDistributed,
			kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed,
		))
	}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed(), behavioral.AssertMsg("DynamicQuotaOrchestrator did not distribute the capacity", dqo))
}

// expectClusterQueueEffectiveQuotas waits until the ClusterQueue status reports want, and checks
// that its spec still matches wantSpec: Kueue writes the quota to status only, and keeps the admin's spec.
func expectClusterQueueEffectiveQuotas(wantSpec *kueue.ClusterQueue, want *kueue.EffectiveQuotaStatus) {
	ginkgo.GinkgoHelper()
	cq := &kueue.ClusterQueue{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(wantSpec), cq)).To(gomega.Succeed())
		g.Expect(cq.Spec.ResourceGroups).To(gomega.BeComparableTo(wantSpec.Spec.ResourceGroups))
		g.Expect(cq.Status.EffectiveQuotas).To(gomega.BeComparableTo(want))
	}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed(), behavioral.AssertMsg("ClusterQueue effective quotas did not match", cq))
}

var _ = ginkgo.Describe("MultiKueue CapacityProvider", ginkgo.Label("area:multikueue", "feature:multikueue"), func() {
	var (
		managerNs *corev1.Namespace
		worker1Ns *corev1.Namespace
		worker2Ns *corev1.Namespace

		workerCluster1   *kueue.MultiKueueCluster
		workerCluster2   *kueue.MultiKueueCluster
		multiKueueConfig *kueue.MultiKueueConfig
		multiKueueAc     *kueue.AdmissionCheck

		managerFlavor *kueue.ResourceFlavor
		managerCq     *kueue.ClusterQueue
		managerLq     *kueue.LocalQueue

		worker1Flavor *kueue.ResourceFlavor
		worker1Cq     *kueue.ClusterQueue
		worker1Lq     *kueue.LocalQueue

		worker2Flavor *kueue.ResourceFlavor
		worker2Cq     *kueue.ClusterQueue
		worker2Lq     *kueue.LocalQueue

		providerConfig   *kueuealpha.MultiKueueCapacityProviderConfig
		capacityProvider *kueuealpha.CapacityProvider
		orchestrator     *kueuealpha.DynamicQuotaOrchestrator
	)

	ginkgo.BeforeEach(func() {
		managerNs = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sManagerClient, "multikueue-cp-")
		worker1Ns = behavioral.CreateNamespaceWithLog(ctx, k8sWorker1Client, managerNs.Name)
		worker2Ns = behavioral.CreateNamespaceWithLog(ctx, k8sWorker2Client, managerNs.Name)

		workerCluster1 = utiltestingapi.MakeMultiKueueClusterWithGeneratedName("worker1-").KubeConfig(kueue.SecretLocationType, "multikueue1").Obj()
		behavioral.MustCreate(ctx, k8sManagerClient, workerCluster1)

		workerCluster2 = utiltestingapi.MakeMultiKueueClusterWithGeneratedName("worker2-").KubeConfig(kueue.SecretLocationType, "multikueue2").Obj()
		behavioral.MustCreate(ctx, k8sManagerClient, workerCluster2)

		multiKueueConfig = utiltestingapi.MakeMultiKueueConfigWithGeneratedName("multikueueconfig-").Clusters(workerCluster1.Name, workerCluster2.Name).Obj()
		behavioral.MustCreate(ctx, k8sManagerClient, multiKueueConfig)

		multiKueueAc = utiltestingapi.MakeAdmissionCheck("").
			GeneratedName("ac1-").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", multiKueueConfig.Name).
			Obj()
		behavioral.CreateAdmissionChecksAndWaitForActive(ctx, k8sManagerClient, multiKueueAc)
		// The AdmissionCheck is active once one cluster is active, but the CapacityProvider
		// sums the quotas of both workers.
		expectMultiKueueClustersToBeActive(workerCluster1, workerCluster2)

		managerFlavor = utiltestingapi.MakeResourceFlavor("").GeneratedName("default-").Obj()
		behavioral.MustCreate(ctx, k8sManagerClient, managerFlavor)

		managerCq = utiltestingapi.MakeClusterQueue("").
			GeneratedName("q1-").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(managerFlavor.Name).
					Resource(corev1.ResourceCPU, "2").
					Resource(extraResourceGPULowCost, "2").
					Obj(),
			).
			AdmissionChecks(kueue.AdmissionCheckReference(multiKueueAc.Name)).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sManagerClient, managerCq)

		managerLq = utiltestingapi.MakeLocalQueue(managerCq.Name, managerNs.Name).ClusterQueue(managerCq.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sManagerClient, managerLq)

		worker1Flavor = utiltestingapi.MakeResourceFlavor(managerFlavor.Name).Obj()
		behavioral.MustCreate(ctx, k8sWorker1Client, worker1Flavor)

		worker1Cq = utiltestingapi.MakeClusterQueue(managerCq.Name).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(worker1Flavor.Name).
					Resource(corev1.ResourceCPU, "2").
					Resource(extraResourceGPULowCost, "1").
					Obj(),
			).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sWorker1Client, worker1Cq)

		worker1Lq = utiltestingapi.MakeLocalQueue(worker1Cq.Name, worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sWorker1Client, worker1Lq)

		worker2Flavor = utiltestingapi.MakeResourceFlavor(managerFlavor.Name).Obj()
		behavioral.MustCreate(ctx, k8sWorker2Client, worker2Flavor)

		worker2Cq = utiltestingapi.MakeClusterQueue(managerCq.Name).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(worker2Flavor.Name).
					Resource(corev1.ResourceCPU, "1").
					Resource(extraResourceGPULowCost, "2").
					Obj(),
			).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sWorker2Client, worker2Cq)

		worker2Lq = utiltestingapi.MakeLocalQueue(worker2Cq.Name, worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sWorker2Client, worker2Lq)

		providerConfig = utiltestingalpha.MakeMultiKueueCapacityProviderConfigWithGenerateName("mk-cp-config-").
			ManagerSubtreeRootRef(kueuealpha.ClusterQueueSubtreeRootRefKind, managerCq.Name).
			Obj()
		behavioral.MustCreate(ctx, k8sManagerClient, providerConfig)

		capacityProvider = utiltestingalpha.MakeCapacityProviderWithGenerateName("mk-cp-").
			ControllerName(kueue.MultiKueueControllerName).
			OrchestratedFlavors(managerFlavor.Name).
			Parameters(kueuealpha.SchemeGroupVersion.Group, "MultiKueueCapacityProviderConfig", providerConfig.Name).
			Obj()
		behavioral.MustCreate(ctx, k8sManagerClient, capacityProvider)

		orchestrator = utiltestingalpha.MakeDynamicQuotaOrchestratorWithGenerateName("mk-dqo-").
			DiscoveryProvider(capacityProvider.Name, nil).
			SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, managerCq.Name).
			Obj()
		behavioral.MustCreate(ctx, k8sManagerClient, orchestrator)
	})

	ginkgo.AfterEach(func() {
		behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, orchestrator, true, behavioral.MediumTimeout)
		behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, capacityProvider, true, behavioral.MediumTimeout)
		behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, providerConfig, true, behavioral.MediumTimeout)

		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sManagerClient, managerNs)).To(gomega.Succeed())
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sWorker1Client, worker1Ns)).To(gomega.Succeed())
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sWorker2Client, worker2Ns)).To(gomega.Succeed())

		behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1Cq, true, behavioral.MediumTimeout)
		behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1Flavor, true, behavioral.MediumTimeout)

		behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, worker2Cq, true, behavioral.MediumTimeout)
		behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, worker2Flavor, true, behavioral.MediumTimeout)

		behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerCq, true, behavioral.MediumTimeout)
		behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerFlavor, true, behavioral.MediumTimeout)
		behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, multiKueueAc, true, behavioral.MediumTimeout)
		behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, multiKueueConfig, true, behavioral.MediumTimeout)
		behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, workerCluster1, true, behavioral.MediumTimeout)
		behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, workerCluster2, true, behavioral.MediumTimeout)

		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sManagerClient, managerNs)
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sWorker1Client, worker1Ns)
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sWorker2Client, worker2Ns)
	})

	ginkgo.It("Should update the manager effective quota when a worker quota changes, and admit a workload that fits the new quota", func() {
		ginkgo.By("Checking that the CapacityProvider reports the sum of the worker ClusterQueue quotas", func() {
			expectCapacityProviderCapacity(client.ObjectKeyFromObject(capacityProvider),
				utiltestingalpha.MakeNormalizedCapacity().
					Flavors(utiltestingalpha.MakeNormalizedCapacityFlavor(managerFlavor.Name).
						Resource(corev1.ResourceCPU, "3").
						Resource(extraResourceGPULowCost, "3").
						Obj()).
					Obj())
		})

		ginkgo.By("Checking that the DynamicQuotaOrchestrator distributes the capacity to the manager ClusterQueue", func() {
			expectDynamicQuotaOrchestratorDistributed(client.ObjectKeyFromObject(orchestrator))
			expectClusterQueueEffectiveQuotas(managerCq,
				utiltestingapi.MakeEffectiveQuotaStatus().
					Name(orchestrator.Name).
					ResourceGroups(utiltestingapi.ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(managerFlavor.Name).
							Resource(corev1.ResourceCPU, "3").
							Resource(extraResourceGPULowCost, "3").
							Obj(),
					)).
					Obj())
		})

		// The job needs more of extraResourceGPULowCost than the manager effective quota (3),
		// the manager spec quota (2) and either worker ClusterQueue (1 and 2).
		job := testingjob.MakeJob("job", managerNs.Name).
			Queue(kueue.LocalQueueName(managerLq.Name)).
			TerminationGracePeriod(1).
			RequestAndLimit(extraResourceGPULowCost, "4").
			Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
			Obj()
		ginkgo.By("Creating a job that does not fit the manager effective quota", func() {
			behavioral.MustCreate(ctx, k8sManagerClient, job)
			expectJobToBeCreatedAndManagedBy(ctx, k8sManagerClient, job, kueue.MultiKueueControllerName)
		})

		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}
		ginkgo.By("Checking that the workload is pending on the manager", func() {
			behavioral.ExpectWorkloadsToBePendingByKeysWithTimeout(ctx, k8sManagerClient, behavioral.MediumTimeout, wlLookupKey)
		})

		ginkgo.By("Increasing the worker1 ClusterQueue quota", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				cq := &kueue.ClusterQueue{}
				g.Expect(k8sWorker1Client.Get(ctx, client.ObjectKeyFromObject(worker1Cq), cq)).To(gomega.Succeed())
				g.Expect(k8sWorker1Client.Update(ctx, behavioral.SetResourceNominalQuota(cq, extraResourceGPULowCost, "4"))).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the CapacityProvider and the manager effective quota follow the worker quota", func() {
			expectCapacityProviderCapacity(client.ObjectKeyFromObject(capacityProvider),
				utiltestingalpha.MakeNormalizedCapacity().
					Flavors(utiltestingalpha.MakeNormalizedCapacityFlavor(managerFlavor.Name).
						Resource(corev1.ResourceCPU, "3").
						Resource(extraResourceGPULowCost, "6").
						Obj()).
					Obj())
			expectClusterQueueEffectiveQuotas(managerCq,
				utiltestingapi.MakeEffectiveQuotaStatus().
					Name(orchestrator.Name).
					ResourceGroups(utiltestingapi.ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(managerFlavor.Name).
							Resource(corev1.ResourceCPU, "3").
							Resource(extraResourceGPULowCost, "6").
							Obj(),
					)).
					Obj())
		})

		ginkgo.By("Checking that the workload is admitted and dispatched to worker1", func() {
			admittedWorkerName := e2e.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
			gomega.Expect(admittedWorkerName).To(gomega.Equal(workerCluster1.Name))
			behavioral.ExpectJobUnsuspended(ctx, k8sManagerClient, client.ObjectKeyFromObject(job))
		})
	})
})
