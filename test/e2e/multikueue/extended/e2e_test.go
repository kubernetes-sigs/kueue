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
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

const (
	// extra virtual resource that allows to directly to a specific worker cluster, without relying on real resources.
	extraResourceGPUHighCost = "example.com/gpu-h100"
	extraResourceGPULowCost  = "example.com/gpu-v100s"
)

type kubernetesClientsMap map[string]struct {
	client     client.Client
	restClient *rest.RESTClient
	cfg        *rest.Config
}

var _ = ginkgo.Describe("MultiKueue", func() {
	var (
		managerNs *corev1.Namespace
		worker1Ns *corev1.Namespace
		worker2Ns *corev1.Namespace

		workerCluster1   *kueue.MultiKueueCluster
		workerCluster2   *kueue.MultiKueueCluster
		multiKueueConfig *kueue.MultiKueueConfig
		multiKueueAc     *kueue.AdmissionCheck
		managerHighWPC   *kueue.WorkloadPriorityClass
		managerLowWPC    *kueue.WorkloadPriorityClass
		managerFlavor    *kueue.ResourceFlavor
		managerCq        *kueue.ClusterQueue
		managerLq        *kueue.LocalQueue

		worker1HighWPC *kueue.WorkloadPriorityClass
		worker1LowWPC  *kueue.WorkloadPriorityClass
		worker1Flavor  *kueue.ResourceFlavor
		worker1Cq      *kueue.ClusterQueue
		worker1Lq      *kueue.LocalQueue

		worker2HighWPC *kueue.WorkloadPriorityClass
		worker2LowWPC  *kueue.WorkloadPriorityClass
		worker2Flavor  *kueue.ResourceFlavor
		worker2Cq      *kueue.ClusterQueue
		worker2Lq      *kueue.LocalQueue

		kubernetesClients kubernetesClientsMap
	)

	ginkgo.BeforeEach(func() {
		managerNs = util.CreateNamespaceFromPrefixWithLog(ctx, k8sManagerClient, "multikueue-")
		worker1Ns = util.CreateNamespaceWithLog(ctx, k8sWorker1Client, managerNs.Name)
		worker2Ns = util.CreateNamespaceWithLog(ctx, k8sWorker2Client, managerNs.Name)

		workerCluster1 = utiltestingapi.MakeMultiKueueClusterWithGeneratedName("worker1-").KubeConfig(kueue.SecretLocationType, "multikueue1").Obj()
		util.MustCreate(ctx, k8sManagerClient, workerCluster1)

		workerCluster2 = utiltestingapi.MakeMultiKueueClusterWithGeneratedName("worker2-").KubeConfig(kueue.SecretLocationType, "multikueue2").Obj()
		util.MustCreate(ctx, k8sManagerClient, workerCluster2)

		multiKueueConfig = utiltestingapi.MakeMultiKueueConfigWithGeneratedName("multikueueconfig-").Clusters(workerCluster1.Name, workerCluster2.Name).Obj()
		util.MustCreate(ctx, k8sManagerClient, multiKueueConfig)

		multiKueueAc = utiltestingapi.MakeAdmissionCheck("").
			GeneratedName("ac1-").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", multiKueueConfig.Name).
			Obj()
		util.CreateAdmissionChecksAndWaitForActive(ctx, k8sManagerClient, multiKueueAc)

		managerHighWPC = utiltestingapi.MakeWorkloadPriorityClass("").
			GeneratedName("high-workload-").PriorityValue(300).Obj()
		util.MustCreate(ctx, k8sManagerClient, managerHighWPC)

		managerLowWPC = utiltestingapi.MakeWorkloadPriorityClass("").
			GeneratedName("low-workload-").PriorityValue(100).Obj()
		util.MustCreate(ctx, k8sManagerClient, managerLowWPC)

		managerFlavor = utiltestingapi.MakeResourceFlavor("").GeneratedName("default-").Obj()
		util.MustCreate(ctx, k8sManagerClient, managerFlavor)

		managerCq = utiltestingapi.MakeClusterQueue("").
			GeneratedName("q1-").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(managerFlavor.Name).
					Resource(corev1.ResourceCPU, "2").
					Resource(corev1.ResourceMemory, "4G").
					Resource(corev1.ResourceEphemeralStorage, "100G").
					Resource(extraResourceGPUHighCost, "2").
					Resource(extraResourceGPULowCost, "2").
					Obj(),
			).
			AdmissionChecks(kueue.AdmissionCheckReference(multiKueueAc.Name)).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sManagerClient, managerCq)

		managerLq = utiltestingapi.MakeLocalQueue(managerCq.Name, managerNs.Name).ClusterQueue(managerCq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sManagerClient, managerLq)

		worker1HighWPC = utiltestingapi.MakeWorkloadPriorityClass(managerHighWPC.Name).PriorityValue(300).Obj()
		util.MustCreate(ctx, k8sWorker1Client, worker1HighWPC)

		worker1LowWPC = utiltestingapi.MakeWorkloadPriorityClass(managerLowWPC.Name).PriorityValue(100).Obj()
		util.MustCreate(ctx, k8sWorker1Client, worker1LowWPC)

		worker1Flavor = utiltestingapi.MakeResourceFlavor(managerFlavor.Name).Obj()
		util.MustCreate(ctx, k8sWorker1Client, worker1Flavor)

		worker1Cq = utiltestingapi.MakeClusterQueue(managerCq.Name).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(worker1Flavor.Name).
					Resource(corev1.ResourceCPU, "2").
					Resource(corev1.ResourceMemory, "1G").
					Resource(corev1.ResourceEphemeralStorage, "15G").
					Resource(extraResourceGPUHighCost, "2").
					Resource(extraResourceGPULowCost, "1").
					Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sWorker1Client, worker1Cq)

		worker1Lq = utiltestingapi.MakeLocalQueue(worker1Cq.Name, worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sWorker1Client, worker1Lq)

		worker2HighWPC = utiltestingapi.MakeWorkloadPriorityClass(managerHighWPC.Name).PriorityValue(300).Obj()
		util.MustCreate(ctx, k8sWorker2Client, worker2HighWPC)

		worker2LowWPC = utiltestingapi.MakeWorkloadPriorityClass(managerLowWPC.Name).PriorityValue(100).Obj()
		util.MustCreate(ctx, k8sWorker2Client, worker2LowWPC)

		worker2Flavor = utiltestingapi.MakeResourceFlavor(managerFlavor.Name).Obj()
		util.MustCreate(ctx, k8sWorker2Client, worker2Flavor)

		worker2Cq = utiltestingapi.MakeClusterQueue(managerCq.Name).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(worker2Flavor.Name).
					Resource(corev1.ResourceCPU, "1200m").
					Resource(corev1.ResourceMemory, "4G").
					Resource(corev1.ResourceEphemeralStorage, "5G").
					Resource(extraResourceGPUHighCost, "1").
					Resource(extraResourceGPULowCost, "2").
					Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sWorker2Client, worker2Cq)

		worker2Lq = utiltestingapi.MakeLocalQueue(worker2Cq.Name, worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sWorker2Client, worker2Lq)

		kubernetesClients = kubernetesClientsMap{
			workerCluster1.Name: {
				client:     k8sWorker1Client,
				restClient: worker1RestClient,
				cfg:        worker1Cfg,
			},
			workerCluster2.Name: {
				client:     k8sWorker2Client,
				restClient: worker2RestClient,
				cfg:        worker2Cfg,
			},
		}
	})

	ginkgo.AfterEach(func() {
		// Clean up resources created by the RayService test on all clusters.
		rayServiceConfigMap := &corev1.ConfigMap{Name: "rayservice-hello", Namespace: managerNs.Name}
		gomega.Expect(client.IgnoreNotFound(k8sManagerClient.Delete(ctx, rayServiceConfigMap))).To(gomega.Succeed())
		gomega.Expect(client.IgnoreNotFound(k8sWorker1Client.Delete(ctx, rayServiceConfigMap.DeepCopy()))).To(gomega.Succeed())
		gomega.Expect(client.IgnoreNotFound(k8sWorker2Client.Delete(ctx, rayServiceConfigMap.DeepCopy()))).To(gomega.Succeed())

		// Use the CRD-tolerant helper: shards without KubeRay have no RayService CRD installed.
		gomega.Expect(util.DeleteAllRayServicesInNamespace(ctx, k8sManagerClient, managerNs)).To(gomega.Succeed())
		gomega.Expect(util.DeleteAllRayServicesInNamespace(ctx, k8sWorker1Client, worker1Ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteAllRayServicesInNamespace(ctx, k8sWorker2Client, worker2Ns)).To(gomega.Succeed())

		gomega.Expect(util.DeleteNamespace(ctx, k8sManagerClient, managerNs)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sWorker1Client, worker1Ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sWorker2Client, worker2Ns)).To(gomega.Succeed())

		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1Cq, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1Flavor, true, util.MediumTimeout)
		util.ExpectObjectToBeDeleted(ctx, k8sWorker1Client, worker1HighWPC, true)
		util.ExpectObjectToBeDeleted(ctx, k8sWorker1Client, worker1LowWPC, true)

		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, worker2Cq, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, worker2Flavor, true, util.MediumTimeout)
		util.ExpectObjectToBeDeleted(ctx, k8sWorker2Client, worker2HighWPC, true)
		util.ExpectObjectToBeDeleted(ctx, k8sWorker2Client, worker2LowWPC, true)

		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerCq, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerFlavor, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerHighWPC, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerLowWPC, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, multiKueueAc, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, multiKueueConfig, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, workerCluster1, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, workerCluster2, true, util.MediumTimeout)

		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sManagerClient, managerNs)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sWorker1Client, worker1Ns)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sWorker2Client, worker2Ns)
	})

	registerRayAutoscalingTests(func() rayAutoscalingTestContext {
		return rayAutoscalingTestContext{
			managerNs:         managerNs,
			managerCq:         managerCq,
			managerLq:         managerLq,
			managerHighWPC:    managerHighWPC,
			managerLowWPC:     managerLowWPC,
			multiKueueAc:      multiKueueAc,
			kubernetesClients: kubernetesClients,
		}
	})

	ginkgo.When("Creating a multikueue integration workload", func() {
		registerLeaderWorkerSetTests(func() leaderWorkerSetTestContext {
			return leaderWorkerSetTestContext{
				managerNs:         managerNs,
				managerLq:         managerLq,
				multiKueueAc:      multiKueueAc,
				kubernetesClients: kubernetesClients,
			}
		})

		registerJobSetTests(func() jobSetTestContext {
			return jobSetTestContext{
				managerNs:         managerNs,
				managerLq:         managerLq,
				multiKueueAc:      multiKueueAc,
				kubernetesClients: kubernetesClients,
			}
		})

		registerAppWrapperTests(func() appWrapperTestContext {
			return appWrapperTestContext{
				managerNs:         managerNs,
				managerLq:         managerLq,
				multiKueueAc:      multiKueueAc,
				kubernetesClients: kubernetesClients,
			}
		})

		registerPyTorchJobTests(func() pyTorchJobTestContext {
			return pyTorchJobTestContext{
				managerNs:    managerNs,
				managerLq:    managerLq,
				multiKueueAc: multiKueueAc,
			}
		})

		registerMPIJobTests(func() mpiJobTestContext {
			return mpiJobTestContext{
				managerNs:    managerNs,
				managerLq:    managerLq,
				multiKueueAc: multiKueueAc,
			}
		})

		registerTrainJobTests(func() trainJobTestContext {
			return trainJobTestContext{
				managerNs:    managerNs,
				managerLq:    managerLq,
				multiKueueAc: multiKueueAc,
			}
		})

		registerKubeRayTests(func() kubeRayTestContext {
			return kubeRayTestContext{
				managerNs:         managerNs,
				managerLq:         managerLq,
				multiKueueAc:      multiKueueAc,
				kubernetesClients: kubernetesClients,
			}
		})
	})
})
