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

package multikueue

import (
	"context"
	"fmt"
	"regexp"
	"strconv"

	"github.com/google/go-cmp/cmp/cmpopts"
	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	kftrainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	workloadaw "sigs.k8s.io/kueue/pkg/controller/jobs/appwrapper"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	workloadjobset "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	workloadpytorchjob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/pytorchjob"
	workloadleaderworkerset "sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset"
	workloadmpijob "sigs.k8s.io/kueue/pkg/controller/jobs/mpijob"
	workloadpod "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	workloadraycluster "sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	workloadrayjob "sigs.k8s.io/kueue/pkg/controller/jobs/rayjob"
	workloadrayservice "sigs.k8s.io/kueue/pkg/controller/jobs/rayservice"
	workloadstatefulset "sigs.k8s.io/kueue/pkg/controller/jobs/statefulset"
	workloadtrainjob "sigs.k8s.io/kueue/pkg/controller/jobs/trainjob"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingaw "sigs.k8s.io/kueue/pkg/util/testingjobs/appwrapper"
	testingdeployment "sigs.k8s.io/kueue/pkg/util/testingjobs/deployment"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	testingleaderworkerset "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	testingmpijob "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	testingpytorchjob "sigs.k8s.io/kueue/pkg/util/testingjobs/pytorchjob"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	testingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	testingrayservice "sigs.k8s.io/kueue/pkg/util/testingjobs/rayservice"
	testingstatefulset "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
	testingtrainjob "sigs.k8s.io/kueue/pkg/util/testingjobs/trainjob"
	"sigs.k8s.io/kueue/pkg/workload"
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
			Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", multiKueueConfig.Name).
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
		rayServiceConfigMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "rayservice-hello", Namespace: managerNs.Name}}
		gomega.Expect(client.IgnoreNotFound(k8sManagerClient.Delete(ctx, rayServiceConfigMap))).To(gomega.Succeed())
		gomega.Expect(client.IgnoreNotFound(k8sWorker1Client.Delete(ctx, rayServiceConfigMap.DeepCopy()))).To(gomega.Succeed())
		gomega.Expect(client.IgnoreNotFound(k8sWorker2Client.Delete(ctx, rayServiceConfigMap.DeepCopy()))).To(gomega.Succeed())

		rayService := &rayv1.RayService{ObjectMeta: metav1.ObjectMeta{Name: "rayservice1", Namespace: managerNs.Name}}
		gomega.Expect(client.IgnoreNotFound(k8sManagerClient.Delete(ctx, rayService))).To(gomega.Succeed())
		gomega.Expect(client.IgnoreNotFound(k8sWorker1Client.Delete(ctx, rayService.DeepCopy()))).To(gomega.Succeed())
		gomega.Expect(client.IgnoreNotFound(k8sWorker2Client.Delete(ctx, rayService.DeepCopy()))).To(gomega.Succeed())

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

	ginkgo.When("Creating a multikueue integration workload", func() {
		ginkgo.It("Should create a pod on worker if admitted", func() {
			pod := testingpod.MakePod("pod", managerNs.Name).
				RequestAndLimit(corev1.ResourceCPU, "100m").
				RequestAndLimit(corev1.ResourceMemory, "100M").
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				Queue(managerLq.Name).
				Obj()

			ginkgo.By("Creating the pod", func() {
				util.MustCreate(ctx, k8sManagerClient, pod)
				gomega.Eventually(func(g gomega.Gomega) {
					createdPod := &corev1.Pod{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(pod), createdPod)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			wlLookupKey := types.NamespacedName{Name: workloadpod.GetWorkloadNameForPod(pod.Name, pod.UID), Namespace: managerNs.Name}

			ginkgo.By("Waiting to be admitted in worker", func() {
				util.ExpectAdmissionCheckStateWithMessage(
					ctx, k8sManagerClient, wlLookupKey,
					multiKueueAc.Name,
					kueue.CheckStateReady,
					"",
				)

				gomega.Eventually(func(g gomega.Gomega) {
					createdPod := &corev1.Pod{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(pod), createdPod)).To(gomega.Succeed())
					g.Expect(utilpod.HasGate(createdPod, podconstants.SchedulingGateName)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
			finishReason := "PodCompleted"
			finishPodConditions := []corev1.PodCondition{
				{
					Type:   corev1.PodReadyToStartContainers,
					Status: corev1.ConditionFalse,
					Reason: "",
				},
				{
					Type:   corev1.PodInitialized,
					Status: corev1.ConditionTrue,
					Reason: finishReason,
				},
				{
					Type:   corev1.PodReady,
					Status: corev1.ConditionFalse,
					Reason: finishReason,
				},
				{
					Type:   corev1.ContainersReady,
					Status: corev1.ConditionFalse,
					Reason: finishReason,
				},
				{
					Type:   corev1.PodScheduled,
					Status: corev1.ConditionTrue,
					Reason: "",
				},
			}
			ginkgo.By("Waiting for the pod to get status updates", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdPod := corev1.Pod{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(pod), &createdPod)).To(gomega.Succeed())
					g.Expect(createdPod.Status.Phase).To(gomega.Equal(corev1.PodSucceeded))
					g.Expect(createdPod.Status.Conditions).To(gomega.BeComparableTo(finishPodConditions, util.IgnorePodConditionTimestampsMessageAndObservedGeneration))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create pods on worker if the deployment is admitted", func() {
			deployment := testingdeployment.MakeDeployment("deployment", managerNs.Name).
				RequestAndLimit(corev1.ResourceCPU, "100m").
				RequestAndLimit(corev1.ResourceMemory, "100M").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Replicas(3).
				TerminationGracePeriod(1).
				Queue(managerLq.Name).
				Obj()

			ginkgo.By("Creating the deployment", func() {
				util.MustCreate(ctx, k8sManagerClient, deployment)
				gomega.Eventually(func(g gomega.Gomega) {
					createdDeployment := &appsv1.Deployment{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(deployment), createdDeployment)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Wait for replicas ready", func() {
				createdDeployment := &appsv1.Deployment{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(deployment), createdDeployment)).To(gomega.Succeed())
					g.Expect(createdDeployment.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Ensure Pod Workloads are created and Pods are Running on the worker cluster", func() {
				ensurePodWorkloadsRunning(deployment, *managerNs, multiKueueAc, kubernetesClients)
			})

			deploymentConditions := []appsv1.DeploymentCondition{
				{
					Type:    appsv1.DeploymentAvailable,
					Status:  corev1.ConditionTrue,
					Reason:  "MinimumReplicasAvailable",
					Message: "Deployment has minimum availability.",
				},
				{
					Type:   appsv1.DeploymentProgressing,
					Status: corev1.ConditionTrue,
					Reason: "NewReplicaSetAvailable",
				},
			}

			ginkgo.By("Waiting for the deployment to get status updates", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdDeployment := appsv1.Deployment{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(deployment), &createdDeployment)).To(gomega.Succeed())
					g.Expect(createdDeployment.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
					g.Expect(createdDeployment.Status.Replicas).To(gomega.Equal(int32(3)))
					g.Expect(createdDeployment.Status.UpdatedReplicas).To(gomega.Equal(int32(3)))
					g.Expect(createdDeployment.Status.Conditions).
						To(gomega.BeComparableTo(deploymentConditions, cmpopts.IgnoreFields(appsv1.DeploymentCondition{}, "LastTransitionTime", "LastUpdateTime", "Message")))
					// Ignoring message as it is required to gather the pod replica set
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Increasing the replica count on the deployment", func() {
				createdDeployment := &appsv1.Deployment{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(deployment), createdDeployment)).To(gomega.Succeed())
					createdDeployment.Spec.Replicas = ptr.To[int32](4)
					g.Expect(k8sManagerClient.Update(ctx, createdDeployment)).To(gomega.Succeed())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())

				ginkgo.By("Wait for replicas ready", func() {
					createdDeployment := &appsv1.Deployment{}
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(deployment), createdDeployment)).To(gomega.Succeed())
						g.Expect(createdDeployment.Status.ReadyReplicas).To(gomega.Equal(int32(4)))
					}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.By("Ensure Pod Workloads are created and Pods are Running on the worker cluster", func() {
				ensurePodWorkloadsRunning(deployment, *managerNs, multiKueueAc, kubernetesClients)
			})

			ginkgo.By("Decreasing the replica count on the deployment", func() {
				createdDeployment := &appsv1.Deployment{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(deployment), createdDeployment)).To(gomega.Succeed())
					createdDeployment.Spec.Replicas = ptr.To[int32](2)
					g.Expect(k8sManagerClient.Update(ctx, createdDeployment)).To(gomega.Succeed())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())

				ginkgo.By("Wait for replicas ready", func() {
					createdDeployment := &appsv1.Deployment{}
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(deployment), createdDeployment)).To(gomega.Succeed())
						g.Expect(createdDeployment.Status.ReadyReplicas).To(gomega.Equal(int32(2)))
					}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.By("Ensure Pod Workloads are created and Pods are Running on the worker cluster", func() {
				ensurePodWorkloadsRunning(deployment, *managerNs, multiKueueAc, kubernetesClients)
			})

			ginkgo.By("Deleting the deployment all pods should be deleted", func() {
				gomega.Expect(k8sManagerClient.Delete(ctx, deployment)).Should(gomega.Succeed())
				for _, workerClient := range kubernetesClients {
					pods := &corev1.PodList{}
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(workerClient.client.List(ctx, pods, client.InNamespace(managerNs.Namespace), client.MatchingLabels(deployment.Spec.Selector.MatchLabels))).To(gomega.Succeed())
						g.Expect(pods.Items).To(gomega.BeEmpty())
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				}
			})
		})

		ginkgo.It("Should sync a StatefulSet to worker if admitted", func() {
			statefulset := testingstatefulset.MakeStatefulSet("statefulset", managerNs.Name).
				RequestAndLimit(corev1.ResourceCPU, "100m").
				RequestAndLimit(corev1.ResourceMemory, "100M").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Replicas(3).
				TerminationGracePeriod(1).
				Queue(managerLq.Name).
				Obj()

			ginkgo.By("Creating the statefulset", func() {
				util.MustCreate(ctx, k8sManagerClient, statefulset)
			})

			wlLookupKey := types.NamespacedName{
				Name:      workloadstatefulset.GetWorkloadName(statefulset.UID, statefulset.Name),
				Namespace: managerNs.Name,
			}

			admittedWorkerName := ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
			workerClient := kubernetesClients[admittedWorkerName].client

			ginkgo.By("Waiting for StatefulSet to be synced to worker cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					workerSts := &appsv1.StatefulSet{}
					g.Expect(workerClient.Get(ctx, client.ObjectKeyFromObject(statefulset), workerSts)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for all replicas to be ready on worker cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					workerSts := &appsv1.StatefulSet{}
					g.Expect(workerClient.Get(ctx, client.ObjectKeyFromObject(statefulset), workerSts)).To(gomega.Succeed())
					g.Expect(workerSts.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verifying pods on management cluster remain gated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					pods := &corev1.PodList{}
					g.Expect(k8sManagerClient.List(ctx, pods, client.InNamespace(managerNs.Name), client.MatchingLabels{
						podconstants.GroupNameLabel: workloadstatefulset.GetWorkloadName(statefulset.UID, statefulset.Name),
					})).To(gomega.Succeed())
					g.Expect(pods.Items).ToNot(gomega.BeEmpty())
					for _, pod := range pods.Items {
						g.Expect(utilpod.HasGate(&pod, podconstants.SchedulingGateName)).To(gomega.BeTrue())
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Deleting the statefulset", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sManagerClient, statefulset, true)
				util.ExpectObjectToBeDeletedWithTimeout(ctx, workerClient, statefulset, false, util.MediumTimeout)
			})
		})

		ginkgo.It("Should sync a LeaderWorkerSet and run replicas on worker cluster", func() {
			lws := testingleaderworkerset.MakeLeaderWorkerSet("leaderworkerset", managerNs.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Replicas(2).
				Size(2).
				RequestAndLimit(corev1.ResourceCPU, "100m").
				RequestAndLimit(corev1.ResourceMemory, "100M").
				Queue(managerLq.Name).
				TerminationGracePeriod(1).
				Obj()

			ginkgo.By("Creating the leaderworkerset", func() {
				util.MustCreate(ctx, k8sManagerClient, lws)
			})

			createdLWS := &leaderworkersetv1.LeaderWorkerSet{}
			gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLWS)).To(gomega.Succeed())

			wlLookupKey0 := types.NamespacedName{
				Name:      workloadleaderworkerset.GetWorkloadName(createdLWS.UID, createdLWS.Name, "0"),
				Namespace: managerNs.Name,
			}
			wlLookupKey1 := types.NamespacedName{
				Name:      workloadleaderworkerset.GetWorkloadName(createdLWS.UID, createdLWS.Name, "1"),
				Namespace: managerNs.Name,
			}

			admittedWorkerName := ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey0, multiKueueAc.Name)
			workerClient := kubernetesClients[admittedWorkerName].client

			ginkgo.By("Verifying both workloads are admitted on the same worker", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					wl0 := &kueue.Workload{}
					g.Expect(workerClient.Get(ctx, wlLookupKey0, wl0)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(wl0)).To(gomega.BeTrue())
					wl1 := &kueue.Workload{}
					g.Expect(workerClient.Get(ctx, wlLookupKey1, wl1)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(wl1)).To(gomega.BeTrue())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for LWS to be synced to worker cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					workerLWS := &leaderworkersetv1.LeaderWorkerSet{}
					g.Expect(workerClient.Get(ctx, client.ObjectKeyFromObject(lws), workerLWS)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for all replicas to be ready on worker cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					workerLWS := &leaderworkersetv1.LeaderWorkerSet{}
					g.Expect(workerClient.Get(ctx, client.ObjectKeyFromObject(lws), workerLWS)).To(gomega.Succeed())
					g.Expect(workerLWS.Status.ReadyReplicas).To(gomega.Equal(int32(2)))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verifying pods on management cluster remain gated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					pods := &corev1.PodList{}
					g.Expect(k8sManagerClient.List(ctx, pods, client.InNamespace(managerNs.Name), client.MatchingLabels{
						leaderworkersetv1.SetNameLabelKey: lws.Name,
					})).To(gomega.Succeed())
					g.Expect(pods.Items).ToNot(gomega.BeEmpty())
					for _, pod := range pods.Items {
						g.Expect(utilpod.HasGate(&pod, podconstants.SchedulingGateName)).To(gomega.BeTrue())
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Deleting the leaderworkerset", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sManagerClient, lws, true)
				util.ExpectObjectToBeDeletedWithTimeout(ctx, workerClient, lws, false, util.MediumTimeout)
			})

			ginkgo.By("Checking that all workloads are deleted from manager and worker clusters", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey0, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey1, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
					g.Expect(workerClient.Get(ctx, wlLookupKey0, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
					g.Expect(workerClient.Get(ctx, wlLookupKey1, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should dispatch all LeaderWorkerSet workloads to the same worker", func() {
			const lwsReplicas = 3
			lws := testingleaderworkerset.MakeLeaderWorkerSet("leaderworkerset", managerNs.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Replicas(lwsReplicas).
				Size(2).
				RequestAndLimit(corev1.ResourceCPU, "100m").
				RequestAndLimit(corev1.ResourceMemory, "100M").
				Queue(managerLq.Name).
				TerminationGracePeriod(1).
				Obj()

			ginkgo.By("Creating the leaderworkerset", func() {
				util.MustCreate(ctx, k8sManagerClient, lws)
			})

			createdLWS := &leaderworkersetv1.LeaderWorkerSet{}
			gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLWS)).To(gomega.Succeed())

			wlKeys := make([]types.NamespacedName, lwsReplicas)
			for i := range lwsReplicas {
				wlKeys[i] = types.NamespacedName{
					Name:      workloadleaderworkerset.GetWorkloadName(createdLWS.UID, createdLWS.Name, strconv.Itoa(i)),
					Namespace: managerNs.Name,
				}
			}

			ginkgo.By("Waiting for workloads to be created on manager cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					for _, key := range wlKeys {
						wl := &kueue.Workload{}
						g.Expect(k8sManagerClient.Get(ctx, key, wl)).To(gomega.Succeed())
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			admittedWorkerName := ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlKeys[0], multiKueueAc.Name)
			workerClient := kubernetesClients[admittedWorkerName].client

			ginkgo.By("Verifying primary workload is admitted on worker2", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					wl := &kueue.Workload{}
					g.Expect(workerClient.Get(ctx, wlKeys[0], wl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verifying LWS is synced to worker cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					workerLWS := &leaderworkersetv1.LeaderWorkerSet{}
					g.Expect(workerClient.Get(ctx, client.ObjectKeyFromObject(lws), workerLWS)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verifying follower workloads are dispatched to the same worker cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					primaryWl := &kueue.Workload{}
					g.Expect(k8sManagerClient.Get(ctx, wlKeys[0], primaryWl)).To(gomega.Succeed())
					g.Expect(primaryWl.Status.ClusterName).ToNot(gomega.BeNil())
					for _, key := range wlKeys[1:] {
						wl := &kueue.Workload{}
						g.Expect(k8sManagerClient.Get(ctx, key, wl)).To(gomega.Succeed())
						g.Expect(wl.Status.ClusterName).ToNot(gomega.BeNil())
						g.Expect(*wl.Status.ClusterName).To(gomega.Equal(*primaryWl.Status.ClusterName))
					}
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Deleting the leaderworkerset", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sManagerClient, lws, true)
				util.ExpectObjectToBeDeletedWithTimeout(ctx, workerClient, lws, false, util.MediumTimeout)
			})

			ginkgo.By("Checking that all workloads are deleted from manager and worker clusters", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					for _, key := range wlKeys {
						g.Expect(k8sManagerClient.Get(ctx, key, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
						g.Expect(workerClient.Get(ctx, key, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create a pod group on worker if admitted", func() {
			numPods := 2
			groupName := "test-group"
			group := testingpod.MakePod(groupName, managerNs.Name).
				Queue(managerLq.Name).
				RequestAndLimit(corev1.ResourceCPU, "100m").
				RequestAndLimit(corev1.ResourceMemory, "100M").
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				MakeGroup(numPods)

			ginkgo.By("Creating the Pod group", func() {
				for _, p := range group {
					gomega.Expect(k8sManagerClient.Create(ctx, p)).To(gomega.Succeed())
					gomega.Expect(p.Spec.SchedulingGates).To(gomega.ContainElements(
						corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName},
					))
				}
			})

			pods := &corev1.PodList{}
			ginkgo.By("ensure all pods are created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.List(ctx, pods, client.InNamespace(managerNs.Name))).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			wlLookupKey := types.NamespacedName{Name: groupName, Namespace: managerNs.Name}
			admittedWorkerName := ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)

			// the execution should be given to the admitted worker
			ginkgo.By("Waiting to be admitted in the worker", func() {
				util.ExpectAdmissionCheckStateWithMessage(
					ctx, k8sManagerClient, wlLookupKey,
					multiKueueAc.Name,
					kueue.CheckStateReady,
					fmt.Sprintf("The workload got reservation on %q", admittedWorkerName),
				)

				ginkgo.By("ensure all pods are created", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sManagerClient.List(ctx, pods, client.InNamespace(managerNs.Name))).To(gomega.Succeed())
						for _, p := range pods.Items {
							g.Expect(utilpod.HasGate(&p, podconstants.SchedulingGateName)).To(gomega.BeTrue())
						}
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.By("Waiting for the group to get status updates", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.List(ctx, pods, client.InNamespace(managerNs.Name))).To(gomega.Succeed())
					for _, p := range pods.Items {
						g.Expect(p.Status.Phase).To(gomega.Equal(corev1.PodSucceeded))
					}
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should run a job on worker if admitted", func() {
			job := testingjob.MakeJob("job", managerNs.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				TerminationGracePeriod(1).
				RequestAndLimit(corev1.ResourceCPU, "100m").
				RequestAndLimit(corev1.ResourceMemory, "100M").
				// Give it the time to be observed Active in the live status update step.
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Obj()

			ginkgo.By("Creating the job", func() {
				util.MustCreate(ctx, k8sManagerClient, job)
				gomega.Eventually(func(g gomega.Gomega) {
					createdJob := &batchv1.Job{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
					g.Expect(ptr.Deref(createdJob.Spec.ManagedBy, "")).To(gomega.BeEquivalentTo(kueue.MultiKueueControllerName))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			createdLeaderWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}
			admittedWorkerName := ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
			admittedWorker := kubernetesClients[admittedWorkerName]

			// the execution should be given to the admitted worker
			ginkgo.By("Waiting to be admitted in admitted worker, and the manager's job unsuspended", func() {
				util.ExpectAdmissionCheckStateWithMessage(
					ctx, k8sManagerClient, wlLookupKey,
					multiKueueAc.Name,
					kueue.CheckStateReady,
					fmt.Sprintf("The workload got reservation on %q", admittedWorkerName),
				)

				gomega.Eventually(func(g gomega.Gomega) {
					createdJob := &batchv1.Job{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
					g.Expect(ptr.Deref(createdJob.Spec.Suspend, false)).To(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for the job to get status updates", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdJob := batchv1.Job{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
					g.Expect(createdJob.Status.StartTime).NotTo(gomega.BeNil())
					g.Expect(createdJob.Status.Active).To(gomega.Equal(int32(1)))
					g.Expect(createdJob.Status.CompletionTime).To(gomega.BeNil())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Finishing the job's pod", func() {
				listOpts := util.GetListOptsFromLabel(fmt.Sprintf("batch.kubernetes.io/job-name=%s", job.Name))
				util.WaitForActivePodsAndTerminate(ctx, admittedWorker.client, admittedWorker.restClient, admittedWorker.cfg, job.Namespace, 1, 0, listOpts)
			})

			ginkgo.By("Waiting for the job to finish", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdLeaderWorkload)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(kueue.WorkloadFinished, kueue.WorkloadFinishedReasonSucceeded))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking no objects are left in the worker clusters and the job is completed", func() {
				util.ExpectObjectToBeDeletedOnClusters(ctx, createdLeaderWorkload, k8sWorker1Client, k8sWorker2Client)
				util.ExpectObjectToBeDeletedOnClusters(ctx, job, k8sWorker1Client, k8sWorker2Client)

				createdJob := &batchv1.Job{}
				gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
				gomega.Expect(createdJob.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(
					batchv1.JobCondition{
						Type:   batchv1.JobComplete,
						Status: corev1.ConditionTrue,
					},
					cmpopts.IgnoreFields(batchv1.JobCondition{}, "LastTransitionTime", "LastProbeTime", "Reason", "Message"))))
			})
		})

		ginkgo.It("Should run a jobSet on worker if admitted", func() {
			jobSet := testingjobset.MakeJobSet("job-set", managerNs.Name).
				Queue(managerLq.Name).
				ReplicatedJobs(
					testingjobset.ReplicatedJobRequirements{
						Name:        "replicated-job-1",
						Replicas:    2,
						Parallelism: 2,
						Completions: 2,
						Image:       util.GetAgnHostImage(),
						// Give it the time to be observed Active in the live status update step.
						Args: util.BehaviorWaitForDeletion,
					},
				).
				RequestAndLimit("replicated-job-1", corev1.ResourceCPU, "100m").
				RequestAndLimit("replicated-job-1", corev1.ResourceMemory, "100M").
				Obj()

			ginkgo.By("Creating the jobSet", func() {
				util.MustCreate(ctx, k8sManagerClient, jobSet)
			})

			createdLeaderWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: managerNs.Name}

			admittedWorkerName := ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
			admittedWorker := kubernetesClients[admittedWorkerName]

			ginkgo.By("Waiting for the jobSet to get status updates", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdJobset := &jobset.JobSet{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(jobSet), createdJobset)).To(gomega.Succeed())

					g.Expect(createdJobset.Status.ReplicatedJobsStatus).To(gomega.BeComparableTo([]jobset.ReplicatedJobStatus{
						{
							Name:   "replicated-job-1",
							Ready:  2,
							Active: 2,
						},
					}, cmpopts.IgnoreFields(jobset.ReplicatedJobStatus{}, "Succeeded", "Failed")))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Finishing the jobset pods", func() {
				listOpts := util.GetListOptsFromLabel(fmt.Sprintf("jobset.sigs.k8s.io/jobset-name=%s", jobSet.Name))
				util.WaitForActivePodsAndTerminate(ctx, admittedWorker.client, admittedWorker.restClient, admittedWorker.cfg, jobSet.Namespace, 4, 0, listOpts)
			})

			ginkgo.By("Waiting for the jobSet to finish", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdLeaderWorkload)).To(gomega.Succeed())

					g.Expect(apimeta.FindStatusCondition(createdLeaderWorkload.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeComparableTo(&metav1.Condition{
						Type:    kueue.WorkloadFinished,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadFinishedReasonSucceeded,
						Message: "jobset completed successfully",
					}, util.IgnoreConditionTimestampsAndObservedGeneration))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking no objects are left in the worker clusters and the jobSet is completed", func() {
				util.ExpectObjectToBeDeletedOnClusters(ctx, createdLeaderWorkload, k8sWorker1Client, k8sWorker2Client)
				util.ExpectObjectToBeDeletedOnClusters(ctx, jobSet, k8sWorker1Client, k8sWorker2Client)

				createdJobSet := &jobset.JobSet{}
				gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(jobSet), createdJobSet)).To(gomega.Succeed())
				gomega.Expect(ptr.Deref(createdJobSet.Spec.Suspend, true)).To(gomega.BeFalse())
				gomega.Expect(createdJobSet.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(
					metav1.Condition{
						Type:    string(jobset.JobSetCompleted),
						Status:  metav1.ConditionTrue,
						Reason:  "AllJobsCompleted",
						Message: "jobset completed successfully",
					},
					util.IgnoreConditionTimestampsAndObservedGeneration)))
			})
		})

		ginkgo.It("Should run an appwrapper containing a job on worker if admitted", func() {
			jobName := "job-1"
			aw := testingaw.MakeAppWrapper("aw", managerNs.Name).
				Queue(managerLq.Name).
				Component(testingaw.Component{
					Template: testingjob.MakeJob(jobName, managerNs.Name).
						SetTypeMeta().
						Suspend(false).
						Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion). // Give it the time to be observed Active in the live status update step.
						Parallelism(2).
						RequestAndLimit(corev1.ResourceCPU, "100m").
						RequestAndLimit(corev1.ResourceMemory, "100M").
						TerminationGracePeriod(1).
						SetTypeMeta().Obj(),
				}).
				Obj()

			ginkgo.By("Creating the appwrapper", func() {
				util.MustCreate(ctx, k8sManagerClient, aw)
			})

			wlLookupKey := types.NamespacedName{Name: workloadaw.GetWorkloadNameForAppWrapper(aw.Name, aw.UID), Namespace: managerNs.Name}

			admittedWorkerName := ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
			admittedWorker := kubernetesClients[admittedWorkerName]

			ginkgo.By("Waiting for the appwrapper to get status updates", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdAppWrapper := &awv1beta2.AppWrapper{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(aw), createdAppWrapper)).To(gomega.Succeed())
					g.Expect(createdAppWrapper.Status.Phase).To(gomega.Equal(awv1beta2.AppWrapperRunning))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Finishing the wrapped job's pods", func() {
				listOpts := util.GetListOptsFromLabel(fmt.Sprintf("batch.kubernetes.io/job-name=%s", jobName))
				util.WaitForActivePodsAndTerminate(ctx, admittedWorker.client, admittedWorker.restClient, admittedWorker.cfg, aw.Namespace, 2, 0, listOpts)
			})

			ginkgo.By("Waiting for the appwrapper to finish", func() {
				util.ExpectWorkloadToFinish(ctx, k8sManagerClient, wlLookupKey)
			})

			ginkgo.By("Checking no objects are left in the worker clusters and the appwrapper is completed", func() {
				createdWorkload := &kueue.Workload{}
				gomega.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				util.ExpectObjectToBeDeletedOnClusters(ctx, createdWorkload, k8sWorker1Client, k8sWorker2Client)
				util.ExpectObjectToBeDeletedOnClusters(ctx, aw, k8sWorker1Client, k8sWorker2Client)

				createdAppWrapper := &awv1beta2.AppWrapper{}
				gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(aw), createdAppWrapper)).To(gomega.Succeed())
				gomega.Expect(createdAppWrapper.Spec.Suspend).To(gomega.BeFalse())
				gomega.Expect(createdAppWrapper.Status.Phase).To(gomega.Equal(awv1beta2.AppWrapperSucceeded))
			})
		})

		ginkgo.It("Should run a kubeflow PyTorchJob on worker if admitted", func() {
			pyTorchJob := testingpytorchjob.MakePyTorchJob("pytorchjob1", managerNs.Name).
				ManagedBy(kueue.MultiKueueControllerName).
				Queue(managerLq.Name).
				PyTorchReplicaSpecs(
					testingpytorchjob.PyTorchReplicaSpecRequirement{
						ReplicaType:   kftraining.PyTorchJobReplicaTypeMaster,
						ReplicaCount:  1,
						RestartPolicy: "Never",
						Image:         util.GetAgnHostImage(),
						Args:          util.BehaviorExitFast,
					},
				).
				RequestAndLimit(kftraining.PyTorchJobReplicaTypeMaster, corev1.ResourceCPU, "100m").
				RequestAndLimit(kftraining.PyTorchJobReplicaTypeMaster, corev1.ResourceMemory, "100M").
				RequestAndLimit(kftraining.PyTorchJobReplicaTypeWorker, corev1.ResourceCPU, "100m").
				RequestAndLimit(kftraining.PyTorchJobReplicaTypeWorker, corev1.ResourceMemory, "100M").
				Obj()

			ginkgo.By("Creating the PyTorchJob", func() {
				util.MustCreate(ctx, k8sManagerClient, pyTorchJob)
			})

			wlLookupKey := types.NamespacedName{Name: workloadpytorchjob.GetWorkloadNameForPyTorchJob(pyTorchJob.Name, pyTorchJob.UID), Namespace: managerNs.Name}

			admittedWorker := ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
			ginkgo.GinkgoLogr.Info("PyTorchJob %s is admitted in worker cluster %s", pyTorchJob.Name, admittedWorker)

			ginkgo.By("Waiting for the PyTorchJob to finish", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdPyTorchJob := &kftraining.PyTorchJob{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(pyTorchJob), createdPyTorchJob)).To(gomega.Succeed())
					g.Expect(createdPyTorchJob.Status.ReplicaStatuses[kftraining.PyTorchJobReplicaTypeMaster]).To(gomega.BeComparableTo(
						&kftraining.ReplicaStatus{
							Active:    0,
							Succeeded: 1,
							Selector: fmt.Sprintf(
								"training.kubeflow.org/job-name=%s,training.kubeflow.org/operator-name=pytorchjob-controller,training.kubeflow.org/replica-type=master",
								createdPyTorchJob.Name,
							),
						},
					))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
				util.ExpectWorkloadToFinish(ctx, k8sManagerClient, wlLookupKey)
			})

			ginkgo.By("Checking no objects are left in the worker clusters and the PyTorchJob is completed", func() {
				wl := &kueue.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Name:      wlLookupKey.Name,
						Namespace: wlLookupKey.Namespace,
					},
				}
				util.ExpectObjectToBeDeletedOnClusters(ctx, wl, k8sWorker1Client, k8sWorker2Client)
				util.ExpectObjectToBeDeletedOnClusters(ctx, pyTorchJob, k8sWorker1Client, k8sWorker2Client)
			})
		})

		ginkgo.It("Should run a MPIJob on worker if admitted", func() {
			mpijob := testingmpijob.MakeMPIJob("mpijob1", managerNs.Name).
				Queue(managerLq.Name).
				ManagedBy(kueue.MultiKueueControllerName).
				MPIJobReplicaSpecs(
					testingmpijob.MPIJobReplicaSpecRequirement{
						ReplicaType:   kfmpi.MPIReplicaTypeLauncher,
						ReplicaCount:  1,
						RestartPolicy: "OnFailure",
						Image:         util.GetAgnHostImage(),
						Args:          util.BehaviorExitFast,
					},
					testingmpijob.MPIJobReplicaSpecRequirement{
						ReplicaType:   kfmpi.MPIReplicaTypeWorker,
						ReplicaCount:  1,
						RestartPolicy: "OnFailure",
						Image:         util.GetAgnHostImage(),
						Args:          util.BehaviorExitFast,
					},
				).
				RequestAndLimit(kfmpi.MPIReplicaTypeLauncher, corev1.ResourceCPU, "100m").
				RequestAndLimit(kfmpi.MPIReplicaTypeLauncher, corev1.ResourceMemory, "100M").
				RequestAndLimit(kfmpi.MPIReplicaTypeWorker, corev1.ResourceCPU, "100m").
				RequestAndLimit(kfmpi.MPIReplicaTypeWorker, corev1.ResourceMemory, "100M").
				Obj()

			ginkgo.By("Creating the MPIJob", func() {
				util.MustCreate(ctx, k8sManagerClient, mpijob)
			})

			wlLookupKey := types.NamespacedName{Name: workloadmpijob.GetWorkloadNameForMPIJob(mpijob.Name, mpijob.UID), Namespace: managerNs.Name}

			admittedWorker := ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
			ginkgo.GinkgoLogr.Info("MPIJob %s is admitted in worker cluster %s", mpijob.Name, admittedWorker)

			ginkgo.By("Waiting for the MPIJob to finish", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdMPIJob := &kfmpi.MPIJob{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(mpijob), createdMPIJob)).To(gomega.Succeed())
					g.Expect(createdMPIJob.Status.ReplicaStatuses[kfmpi.MPIReplicaTypeLauncher]).To(gomega.BeComparableTo(
						&kfmpi.ReplicaStatus{
							Active:    0,
							Succeeded: 1,
						},
					))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
				util.ExpectWorkloadToFinish(ctx, k8sManagerClient, wlLookupKey)
			})

			ginkgo.By("Checking no objects are left in the worker clusters and the MPIJob is completed", func() {
				wl := &kueue.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Name:      wlLookupKey.Name,
						Namespace: wlLookupKey.Namespace,
					},
				}
				util.ExpectObjectToBeDeletedOnClusters(ctx, wl, k8sWorker1Client, k8sWorker2Client)
				util.ExpectObjectToBeDeletedOnClusters(ctx, mpijob, k8sWorker1Client, k8sWorker2Client)
			})
		})

		ginkgo.It("Should run a TrainJob on worker if admitted", func() {
			trainjob := testingtrainjob.MakeTrainJob("trainjob-test", managerNs.Name).
				RuntimeRefName("torch-distributed").
				Queue(managerLq.Name).
				RequestAndLimit(corev1.ResourceCPU, "100m", "100m").
				RequestAndLimit(corev1.ResourceMemory, "100M", "100M").
				// Even if we override the image coming from the TrainingRuntime, we still need to set the command and args
				TrainerImage(util.GetAgnHostImage(), []string{"/agnhost"}, util.BehaviorExitFast).
				Obj()

			ginkgo.By("Creating the trainjob", func() {
				util.MustCreate(ctx, k8sManagerClient, trainjob)
			})

			wlLookupKey := types.NamespacedName{Name: workloadtrainjob.GetWorkloadNameForTrainJob(trainjob.Name, trainjob.UID), Namespace: managerNs.Name}

			admittedWorker := ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
			ginkgo.GinkgoLogr.Info("TrainJob %s is admitted in worker cluster %s", trainjob.Name, admittedWorker)

			ginkgo.By("Checking the TrainJob is ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdTrainJob := &kftrainer.TrainJob{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(trainjob), createdTrainJob)).To(gomega.Succeed())
					g.Expect(ptr.Deref(createdTrainJob.Spec.Suspend, false)).To(gomega.BeFalse())
				}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.When("Ray integration tests", ginkgo.Ordered, func() {
			ginkgo.It("Should run a RayJob on worker if admitted", func() {
				kuberayTestImage := util.GetKuberayTestImage()
				rayjob := testingrayjob.MakeJob("rayjob1", managerNs.Name).
					Suspend(true).
					Queue(managerLq.Name).
					WithSubmissionMode(rayv1.K8sJobMode).
					Entrypoint("python -c \"import ray; ray.init(); print(ray.cluster_resources())\"").
					RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "1").
					RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "0.5").
					Image(rayv1.HeadNode, kuberayTestImage).
					Image(rayv1.WorkerNode, kuberayTestImage).
					Obj()

				ginkgo.By("Creating the RayJob", func() {
					util.MustCreate(ctx, k8sManagerClient, rayjob)
				})

				wlLookupKey := types.NamespacedName{Name: workloadrayjob.GetWorkloadNameForRayJob(rayjob.Name, rayjob.UID), Namespace: managerNs.Name}

				admittedWorker := ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
				ginkgo.GinkgoLogr.Info(fmt.Sprintf("RayJob %s/%s is admitted in worker cluster %s", rayjob.Name, rayjob.Namespace, admittedWorker))

				ginkgo.By("Waiting for the RayJob to finish", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						createdRayJob := &rayv1.RayJob{}
						g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(rayjob), createdRayJob)).To(gomega.Succeed())
						g.Expect(createdRayJob.Status.JobDeploymentStatus).To(gomega.Equal(rayv1.JobDeploymentStatusComplete))
					}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
					util.ExpectWorkloadToFinish(ctx, k8sManagerClient, wlLookupKey)
				})

				ginkgo.By("Checking no objects are left in the worker clusters and the RayJob is completed", func() {
					wl := &kueue.Workload{
						ObjectMeta: metav1.ObjectMeta{
							Name:      wlLookupKey.Name,
							Namespace: wlLookupKey.Namespace,
						},
					}
					util.ExpectObjectToBeDeletedOnClusters(ctx, wl, k8sWorker1Client, k8sWorker2Client)
					util.ExpectObjectToBeDeletedOnClusters(ctx, rayjob, k8sWorker1Client, k8sWorker2Client)
				})
			})

			ginkgo.It("Should run a RayCluster on worker if admitted", func() {
				kuberayTestImage := util.GetKuberayTestImage()
				raycluster := testingraycluster.MakeCluster("raycluster1", managerNs.Name).
					Suspend(true).
					Queue(managerLq.Name).
					RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "1").
					RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "0.5").
					Image(rayv1.HeadNode, kuberayTestImage, []string{}).
					Image(rayv1.WorkerNode, kuberayTestImage, []string{}).
					Obj()

				ginkgo.By("Creating the RayCluster", func() {
					util.MustCreate(ctx, k8sManagerClient, raycluster)
				})

				wlLookupKey := types.NamespacedName{Name: workloadraycluster.GetWorkloadNameForRayCluster(raycluster.Name, raycluster.UID), Namespace: managerNs.Name}
				// the execution should be given to the worker1
				admittedWorker := ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
				ginkgo.GinkgoLogr.Info(fmt.Sprintf("RayCluster %s/%s is admitted in worker cluster %s", raycluster.Name, raycluster.Namespace, admittedWorker))

				ginkgo.By("Checking the RayCluster is ready", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						createdRayCluster := &rayv1.RayCluster{}
						g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), createdRayCluster)).To(gomega.Succeed())
						g.Expect(createdRayCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(1)))
						g.Expect(createdRayCluster.Status.ReadyWorkerReplicas).To(gomega.Equal(int32(1)))
						g.Expect(createdRayCluster.Status.AvailableWorkerReplicas).To(gomega.Equal(int32(1)))
					}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.It("Should run a RayService on worker if admitted", func() {
				kuberayTestImage := util.GetKuberayTestImage()

				// Create ConfigMap with a simple Ray Serve application
				configMap := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "rayservice-hello",
						Namespace: managerNs.Name,
					},
					Data: map[string]string{
						"hello_serve.py": `from ray import serve

@serve.deployment
class HelloWorld:
    def __call__(self, request):
        return "Hello, World!"

app = HelloWorld.bind()`,
					},
				}

				serveConfigV2 := `applications:
  - name: hello_app
    import_path: hello_serve:app
    route_prefix: /
    deployments:
      - name: HelloWorld
        num_replicas: 1
        max_replicas_per_node: 1
        ray_actor_options:
          num_cpus: 0.2`

				volumes := []corev1.Volume{
					{
						Name: "code-sample",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "rayservice-hello",
								},
								Items: []corev1.KeyToPath{
									{
										Key:  "hello_serve.py",
										Path: "hello_serve.py",
									},
								},
							},
						},
					},
				}
				volumeMounts := []corev1.VolumeMount{
					{
						Name:      "code-sample",
						MountPath: "/home/ray/samples",
					},
				}
				env := []corev1.EnvVar{
					{
						Name:  "PYTHONPATH",
						Value: "/home/ray/samples:$PYTHONPATH",
					},
				}

				rayService := testingrayservice.MakeService("rayservice1", managerNs.Name).
					Suspend(true).
					Queue(managerLq.Name).
					RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "1").
					RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "0.5").
					Image(rayv1.HeadNode, kuberayTestImage).
					Image(rayv1.WorkerNode, kuberayTestImage).
					RayStartParam(rayv1.HeadNode, "object-store-memory", "100000000").
					WithServeConfigV2(serveConfigV2).
					Env(rayv1.HeadNode, env).
					Env(rayv1.WorkerNode, env).
					Volumes(rayv1.HeadNode, volumes).
					Volumes(rayv1.WorkerNode, volumes).
					VolumeMounts(rayv1.HeadNode, volumeMounts).
					VolumeMounts(rayv1.WorkerNode, volumeMounts).
					Obj()

				rayService.Spec.RayClusterSpec.WorkerGroupSpecs[0].GroupName = "small-group"
				rayService.Spec.RayClusterSpec.WorkerGroupSpecs[0].MinReplicas = ptr.To[int32](1)
				rayService.Spec.RayClusterSpec.WorkerGroupSpecs[0].MaxReplicas = ptr.To[int32](2)

				ginkgo.By("Creating the ConfigMap on all clusters", func() {
					worker1ConfigMap := configMap.DeepCopy()
					worker2ConfigMap := configMap.DeepCopy()
					util.MustCreate(ctx, k8sManagerClient, configMap)
					util.MustCreate(ctx, k8sWorker1Client, worker1ConfigMap)
					util.MustCreate(ctx, k8sWorker2Client, worker2ConfigMap)
				})

				ginkgo.By("Creating the RayService", func() {
					util.MustCreate(ctx, k8sManagerClient, rayService)
				})

				wlLookupKey := types.NamespacedName{Name: workloadrayservice.GetWorkloadNameForRayService(rayService.Name, rayService.UID), Namespace: managerNs.Name}

				admittedWorker := ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
				ginkgo.GinkgoLogr.Info(fmt.Sprintf("RayService %s/%s is admitted in worker cluster %s", rayService.Name, rayService.Namespace, admittedWorker))

				ginkgo.By("Checking the RayService is running", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						createdRayService := &rayv1.RayService{}
						g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(rayService), createdRayService)).To(gomega.Succeed())
						g.Expect(createdRayService.Spec.RayClusterSpec.Suspend).To(gomega.Equal(ptr.To(false)))
						g.Expect(apimeta.IsStatusConditionTrue(createdRayService.Status.Conditions, string(rayv1.RayServiceReady))).To(gomega.BeTrue())
					}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
				})
			})
		})
	})
	ginkgo.When("Preemption with a multikueue admission check", func() {
		ginkgo.It("Should preempt a running low-priority workload when a high-priority workload is admitted (same worker)", func() {
			lowJob := testingjob.MakeJob("", managerNs.Name).
				GeneratedName("low-job-").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				WorkloadPriorityClass(managerLowWPC.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(extraResourceGPUHighCost, "2").
				Obj()
			util.MustCreate(ctx, k8sManagerClient, lowJob)

			lowWlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(lowJob.Name, lowJob.UID), Namespace: managerNs.Name}

			managerLowWl := &kueue.Workload{}
			workerLowWorkload := &kueue.Workload{}

			ginkgo.By("Checking that the low-priority workload is created and admitted in the manager cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, lowWlKey, managerLowWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(managerLowWl)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the low-priority workload is created in worker1 and not in worker2, and that its spec matches the manager workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, lowWlKey, workerLowWorkload)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(workerLowWorkload)).To(gomega.BeTrue())
					g.Expect(workerLowWorkload.Spec).To(gomega.BeComparableTo(managerLowWl.Spec))
					g.Expect(k8sWorker2Client.Get(ctx, lowWlKey, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			highJob := testingjob.MakeJob("", managerNs.Name).
				GeneratedName("high-job-").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				WorkloadPriorityClass(managerHighWPC.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(extraResourceGPUHighCost, "2").
				Obj()
			util.MustCreate(ctx, k8sManagerClient, highJob)

			highWlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(highJob.Name, highJob.UID), Namespace: managerNs.Name}

			managerHighWl := &kueue.Workload{}
			workerHighWorkload := &kueue.Workload{}

			ginkgo.By("Checking that the high-priority workload is created and admitted in the manager cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, highWlKey, managerHighWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(managerHighWl)).To(gomega.BeTrue())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the high-priority workload is created in worker1 and not in worker2, and that its spec matches the manager workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, highWlKey, workerHighWorkload)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(workerHighWorkload)).To(gomega.BeTrue())
					g.Expect(workerHighWorkload.Spec).To(gomega.BeComparableTo(managerHighWl.Spec))
					g.Expect(k8sWorker2Client.Get(ctx, highWlKey, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the low-priority workload is preempted in the manager cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, lowWlKey, managerLowWl)).To(gomega.Succeed())
					g.Expect(workload.IsEvicted(managerLowWl)).To(gomega.BeTrue())
					g.Expect(managerLowWl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadPreempted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the low-priority workload is deleted from the worker clusters", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, lowWlKey, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
					g.Expect(k8sWorker2Client.Get(ctx, lowWlKey, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should re-do admission process when workload gets evicted in the worker", func() {
			job := testingjob.MakeJob("job", managerNs.Name).
				WorkloadPriorityClass(managerLowWPC.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "100m").
				RequestAndLimit(corev1.ResourceMemory, "0.1G").
				RequestAndLimit(extraResourceGPUHighCost, "2"). // This will make the workload only schedulable in worker1
				Obj()
			util.MustCreate(ctx, k8sManagerClient, job)

			wlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}
			managerWl := &kueue.Workload{}
			workerWorkload := &kueue.Workload{}

			ginkgo.By("Checking that the workload is created and admitted in the manager cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlKey, managerWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(managerWl)).To(gomega.BeTrue(), util.AssertMsg("Workload not admitted in manager", managerWl))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the workload is created on worker1", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, wlKey, workerWorkload)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(workerWorkload)).To(gomega.BeTrue(), util.AssertMsg("Workload not admitted in worker1", workerWorkload))
					g.Expect(workerWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))

					g.Expect(k8sWorker2Client.Get(ctx, wlKey, workerWorkload)).To(utiltesting.BeNotFoundError(), util.AssertMsg("Workload present in worker2", workerWorkload))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Switching worker cluster queues' resources to enforce re-admission on the worker2", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, client.ObjectKeyFromObject(worker1Cq), worker1Cq)).To(gomega.Succeed())
					g.Expect(k8sWorker1Client.Update(ctx, util.SetResourceNominalQuota(worker1Cq, extraResourceGPUHighCost, "1"))).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker2Client.Get(ctx, client.ObjectKeyFromObject(worker2Cq), worker2Cq)).To(gomega.Succeed())
					g.Expect(k8sWorker2Client.Update(ctx, util.SetResourceNominalQuota(worker2Cq, extraResourceGPUHighCost, "2"))).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
			ginkgo.By("Triggering eviction in worker1", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, wlKey, workerWorkload)).To(gomega.Succeed())
					g.Expect(workload.SetConditionAndUpdate(
						ctx,
						k8sWorker1Client,
						workerWorkload,
						kueue.WorkloadEvicted,
						metav1.ConditionTrue,
						kueue.WorkloadEvictedByPreemption,
						"By test",
						"evict",
						util.RealClock,
					)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the workload is re-admitted in worker2", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlKey, managerWl)).To(gomega.Succeed())
					g.Expect(managerWl.Status.ClusterName).To(gomega.HaveValue(gomega.Equal(workerCluster2.Name)), util.AssertMsg("Manager Workload is not admitted in worker2", managerWl))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the workload is created in worker2", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker2Client.Get(ctx, wlKey, workerWorkload)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(workerWorkload)).To(gomega.BeTrue(), util.AssertMsg("Workload not Admitted in worker2", workerWorkload))
					g.Expect(workerWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))

					g.Expect(k8sWorker1Client.Get(ctx, wlKey, workerWorkload)).To(utiltesting.BeNotFoundError(), util.AssertMsg("Workload present in worker1", workerWorkload))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should preempt a running low-priority workload when a high-priority workload is admitted (other workers)", func() {
			lowJob := testingjob.MakeJob("", managerNs.Name).
				GeneratedName("low-job-").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				WorkloadPriorityClass(managerLowWPC.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "100m").
				RequestAndLimit(corev1.ResourceMemory, "0.1G").
				RequestAndLimit(extraResourceGPUHighCost, "2").
				RequestAndLimit(extraResourceGPULowCost, "1").
				Obj()
			util.MustCreate(ctx, k8sManagerClient, lowJob)

			lowWlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(lowJob.Name, lowJob.UID), Namespace: managerNs.Name}

			managerLowWl := &kueue.Workload{}
			workerLowWorkload := &kueue.Workload{}

			ginkgo.By("Checking that the low-priority workload is created and admitted in the manager cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, lowWlKey, managerLowWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(managerLowWl)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the low-priority workload is created in worker1 and not in worker2, and that its spec matches the manager workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, lowWlKey, workerLowWorkload)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(workerLowWorkload)).To(gomega.BeTrue())
					g.Expect(workerLowWorkload.Spec).To(gomega.BeComparableTo(managerLowWl.Spec))
					g.Expect(k8sWorker2Client.Get(ctx, lowWlKey, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			highJob := testingjob.MakeJob("", managerNs.Name).
				GeneratedName("high-job-").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				WorkloadPriorityClass(managerHighWPC.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "100m").
				RequestAndLimit(corev1.ResourceMemory, "0.1G").
				RequestAndLimit(extraResourceGPUHighCost, "1").
				RequestAndLimit(extraResourceGPULowCost, "2").
				Obj()
			util.MustCreate(ctx, k8sManagerClient, highJob)

			highWlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(highJob.Name, highJob.UID), Namespace: managerNs.Name}

			managerHighWl := &kueue.Workload{}
			workerHighWorkload := &kueue.Workload{}

			ginkgo.By("Checking that the high-priority workload is created and admitted in the manager cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, highWlKey, managerHighWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(managerHighWl)).To(gomega.BeTrue())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the high-priority workload is created in worker2 and not in worker1, and that its spec matches the manager workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker2Client.Get(ctx, highWlKey, workerHighWorkload)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(workerHighWorkload)).To(gomega.BeTrue())
					g.Expect(workerHighWorkload.Spec).To(gomega.BeComparableTo(managerHighWl.Spec))
					g.Expect(k8sWorker1Client.Get(ctx, highWlKey, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the low-priority workload is preempted in the manager cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, lowWlKey, managerLowWl)).To(gomega.Succeed())
					g.Expect(workload.IsEvicted(managerLowWl)).To(gomega.BeTrue())
					g.Expect(managerLowWl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadPreempted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the low-priority workload is deleted from the worker clusters", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, lowWlKey, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
					g.Expect(k8sWorker2Client.Get(ctx, lowWlKey, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should preempt a running low-priority workload on a worker when a high-priority workload is admitted to it by manager", func() {
			ginkgo.By("Adjusting manager CQ quota to allow both jobs", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(managerCq), managerCq)).To(gomega.Succeed())
					g.Expect(k8sManagerClient.Update(ctx, util.SetResourceNominalQuota(managerCq, extraResourceGPULowCost, "4"))).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			lowJob := testingjob.MakeJob("", managerNs.Name).
				GeneratedName("low-job-").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				WorkloadPriorityClass(managerLowWPC.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "100m").
				RequestAndLimit(corev1.ResourceMemory, "0.1G").
				RequestAndLimit(extraResourceGPUHighCost, "1").
				RequestAndLimit(extraResourceGPULowCost, "2").
				Obj()
			util.MustCreate(ctx, k8sManagerClient, lowJob)

			lowWlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(lowJob.Name, lowJob.UID), Namespace: managerNs.Name}

			managerLowWl := &kueue.Workload{}
			workerLowWorkload := &kueue.Workload{}

			ginkgo.By("Checking that the low-priority workload is created and admitted in the manager cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, lowWlKey, managerLowWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(managerLowWl)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the low-priority workload is created in worker2 and not in worker1, and that its spec matches the manager workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker2Client.Get(ctx, lowWlKey, workerLowWorkload)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(workerLowWorkload)).To(gomega.BeTrue())
					g.Expect(workerLowWorkload.Spec).To(gomega.BeComparableTo(managerLowWl.Spec))
					g.Expect(k8sWorker1Client.Get(ctx, lowWlKey, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the low-priority job is active", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					admittedJob := &batchv1.Job{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(lowJob), admittedJob)).To(gomega.Succeed())
					g.Expect(admittedJob.Status.Active).To(gomega.Equal(int32(1)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			highJob := testingjob.MakeJob("", managerNs.Name).
				GeneratedName("high-job-").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				WorkloadPriorityClass(managerHighWPC.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "100m").
				RequestAndLimit(corev1.ResourceMemory, "0.1G").
				RequestAndLimit(extraResourceGPUHighCost, "1").
				RequestAndLimit(extraResourceGPULowCost, "2").
				Obj()
			util.MustCreate(ctx, k8sManagerClient, highJob)

			highWlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(highJob.Name, highJob.UID), Namespace: managerNs.Name}

			managerHighWl := &kueue.Workload{}
			workerHighWorkload := &kueue.Workload{}

			ginkgo.By("Checking that the high-priority workload is created and admitted in the manager cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, highWlKey, managerHighWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(managerHighWl)).To(gomega.BeTrue())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the high-priority workload has preempted the low-priority job on worker2 and is not present on worker1, and that its spec matches the manager workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker2Client.Get(ctx, highWlKey, workerHighWorkload)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(workerHighWorkload)).To(gomega.BeTrue())
					g.Expect(workerHighWorkload.Spec).To(gomega.BeComparableTo(managerHighWl.Spec))
					g.Expect(k8sWorker1Client.Get(ctx, highWlKey, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the low-priority workload was evicted and backed off (is no longer admitted)", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, lowWlKey, managerLowWl)).To(gomega.Succeed())
					g.Expect(managerLowWl.Status.Conditions).To(utiltesting.HaveConditionStatusFalse(kueue.WorkloadAdmitted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the low-priority job is not active", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					admittedJob := &batchv1.Job{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(lowJob), admittedJob)).To(gomega.Succeed())
					g.Expect(admittedJob.Status.Active).To(gomega.Equal(int32(0)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the high-priority job is active", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					admittedJob := &batchv1.Job{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(highJob), admittedJob)).To(gomega.Succeed())
					g.Expect(admittedJob.Status.Active).To(gomega.Equal(int32(1)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the low-priority workload is dispatched again after backoff", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, lowWlKey, managerLowWl)).To(gomega.Succeed())
					g.Expect(managerLowWl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadQuotaReserved))
					g.Expect(workload.IsAdmitted(managerLowWl)).To(gomega.BeFalse())

					g.Expect(k8sWorker1Client.Get(ctx, lowWlKey, workerLowWorkload)).To(gomega.Succeed())
					g.Expect(workload.HasActiveQuotaReservation(workerLowWorkload)).To(gomega.BeFalse())

					g.Expect(k8sWorker2Client.Get(ctx, lowWlKey, workerLowWorkload)).To(gomega.Succeed())
					g.Expect(workload.HasActiveQuotaReservation(workerLowWorkload)).To(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("Priority class mutation", func() {
		ginkgo.It("Should allow to mutate kueue.x-k8s.io/priority-class (other workers)", func() {
			job1 := testingjob.MakeJob("job1", managerNs.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				WorkloadPriorityClass(managerHighWPC.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(extraResourceGPUHighCost, "2").
				RequestAndLimit(extraResourceGPULowCost, "1").
				Obj()
			ginkgo.By("Creating the first job", func() {
				util.MustCreate(ctx, k8sManagerClient, job1)
			})

			job1Key := client.ObjectKeyFromObject(job1)
			wl1Key := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job1.Name, job1.UID), Namespace: managerNs.Name}

			managerWl1 := &kueue.Workload{}
			workerWl1 := &kueue.Workload{}

			ginkgo.By("Checking that the first workload is created and admitted in the manager cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wl1Key, managerWl1)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(managerWl1)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the first workload is created in worker1 and not in worker2, and that its spec matches the manager workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, wl1Key, workerWl1)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(workerWl1)).To(gomega.BeTrue())
					g.Expect(workerWl1.Spec).To(gomega.BeComparableTo(managerWl1.Spec))
					g.Expect(k8sWorker2Client.Get(ctx, wl1Key, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			job2 := testingjob.MakeJob("job2", managerNs.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				WorkloadPriorityClass(managerHighWPC.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(extraResourceGPUHighCost, "1").
				RequestAndLimit(extraResourceGPULowCost, "2").
				Obj()
			ginkgo.By("Creating the second job", func() {
				util.MustCreate(ctx, k8sManagerClient, job2)
			})

			wl2Key := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job2.Name, job2.UID), Namespace: managerNs.Name}

			managerWl2 := &kueue.Workload{}
			workerWl2 := &kueue.Workload{}

			ginkgo.By("Checking that the second workload is created and pending in the manager cluster", func() {
				util.ExpectWorkloadsToBePendingByKeys(ctx, k8sManagerClient, wl2Key)
			})

			ginkgo.By("Checking that the second workload is not created on the workers", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, wl2Key, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
					g.Expect(k8sWorker2Client.Get(ctx, wl2Key, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Set a low priority class on the first job", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, job1Key, job1)).To(gomega.Succeed())
					job1.Labels[constants.WorkloadPriorityClassLabel] = managerLowWPC.Name
					g.Expect(k8sManagerClient.Update(ctx, job1)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the second workload is created in worker2 and not in worker1, and that its spec matches the manager workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wl2Key, managerWl2)).To(gomega.Succeed())
					g.Expect(k8sWorker2Client.Get(ctx, wl2Key, workerWl2)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(workerWl2)).To(gomega.BeTrue())
					g.Expect(workerWl2.Spec).To(gomega.BeComparableTo(managerWl2.Spec))
					g.Expect(k8sWorker1Client.Get(ctx, wl2Key, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the first workload is preempted in the manager cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wl1Key, managerWl1)).To(gomega.Succeed())
					g.Expect(workload.IsEvicted(managerWl1)).To(gomega.BeTrue())
					g.Expect(managerWl1.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadPreempted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the first workload is deleted from the worker clusters", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, wl1Key, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
					g.Expect(k8sWorker2Client.Get(ctx, wl1Key, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should keep workload priority event if job reconcile", func() {
			job := testingjob.MakeJob("job", managerNs.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				WorkloadPriorityClass(managerHighWPC.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "2").
				RequestAndLimit(corev1.ResourceMemory, "1G").
				TerminationGracePeriod(1).
				Parallelism(1).
				Completions(1).
				BackoffLimitPerIndex(2).
				CompletionMode(batchv1.IndexedCompletion).
				Obj()
			ginkgo.By("Creating the first job", func() {
				util.MustCreate(ctx, k8sManagerClient, job)
			})

			jobKey := client.ObjectKeyFromObject(job)
			wlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

			managerWl := &kueue.Workload{}
			workerWl := &kueue.Workload{}

			ginkgo.By("Checking that the workload is created and admitted in the manager cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlKey, managerWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(managerWl)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the workload is created in worker1 and not in worker2, and that its spec matches the manager workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, wlKey, workerWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(workerWl)).To(gomega.BeTrue())
					g.Expect(workerWl.Spec).To(gomega.BeComparableTo(managerWl.Spec))
					g.Expect(k8sWorker2Client.Get(ctx, wlKey, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting a low priority class on the job", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, jobKey, job)).To(gomega.Succeed())
					job.Labels[constants.WorkloadPriorityClassLabel] = managerLowWPC.Name
					g.Expect(k8sManagerClient.Update(ctx, job)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the workload was updated on worker", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlKey, managerWl)).To(gomega.Succeed())
					g.Expect(managerWl.Spec.Priority).To(gomega.Equal(ptr.To(managerLowWPC.Value)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the workload was updated on worker", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, wlKey, workerWl)).To(gomega.Succeed())
					g.Expect(workerWl.Spec.Priority).To(gomega.Equal(ptr.To(managerLowWPC.Value)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Simulating pod failure", func() {
				util.WaitForActivePodsAndTerminate(ctx, k8sWorker1Client, worker1RestClient, worker1Cfg, worker1Ns.Name, 1, 1)
			})

			ginkgo.By("Checking that the workload still have low priority value", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, wlKey, workerWl)).To(gomega.Succeed())
					g.Expect(workerWl.Spec.Priority).To(gomega.Equal(ptr.To(managerLowWPC.Value)))
				}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the workload is still admitted on the worker1", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, wlKey, workerWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(workerWl)).To(gomega.BeTrue())
					g.Expect(workerWl.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("Cluster Role Sharing", func() {
		var (
			// Regular Kueue Cluster Queues and Local Queues
			managerRegularCq *kueue.ClusterQueue
			managerRegularLq *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			managerRegularCq = utiltestingapi.MakeClusterQueue("q2").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(managerFlavor.Name).
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "2G").
						Obj(),
				).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sManagerClient, managerRegularCq)
			managerRegularLq = utiltestingapi.MakeLocalQueue(managerRegularCq.Name, managerNs.Name).ClusterQueue(managerRegularCq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sManagerClient, managerRegularLq)
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerRegularCq, true, util.MediumTimeout)
		})

		ginkgo.It("should allow to run a MultiKueue and a regular Job on the same cluster", func() {
			jobMk := testingjob.MakeJob("job-mk", managerNs.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				TerminationGracePeriod(1).
				RequestAndLimit(corev1.ResourceCPU, "1").
				RequestAndLimit(corev1.ResourceMemory, "2G").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Obj()
			jobRegular := testingjob.MakeJob("job-regular", managerNs.Name).
				Queue(kueue.LocalQueueName(managerRegularLq.Name)).
				TerminationGracePeriod(1).
				RequestAndLimit(corev1.ResourceCPU, "1").
				RequestAndLimit(corev1.ResourceMemory, "2G").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Obj()

			ginkgo.By("Creating jobs", func() {
				util.MustCreate(ctx, k8sManagerClient, jobMk)
				expectJobToBeCreatedAndManagedBy(ctx, k8sManagerClient, jobMk, kueue.MultiKueueControllerName)
				util.MustCreate(ctx, k8sManagerClient, jobRegular)
				expectJobToBeCreatedAndManagedBy(ctx, k8sManagerClient, jobRegular, "")
			})

			ginkgo.By("Verifying both jobs are unsuspended and running", func() {
				for _, job := range []*batchv1.Job{jobMk, jobRegular} {
					util.ExpectJobUnsuspended(ctx, k8sManagerClient, client.ObjectKeyFromObject(job))
					util.ExpectJobToBeRunning(ctx, k8sManagerClient, job)
				}
			})

			ginkgo.By("Finishing the MK job's pod", func() {
				listOpts := util.GetListOptsFromLabel(fmt.Sprintf("batch.kubernetes.io/job-name=%s", jobMk.Name))
				util.WaitForActivePodsAndTerminate(ctx, k8sWorker2Client, worker2RestClient, worker2Cfg, jobMk.Namespace, 1, 0, listOpts)
			})

			ginkgo.By("Finishing the regular job's pod", func() {
				listOpts := util.GetListOptsFromLabel(fmt.Sprintf("batch.kubernetes.io/job-name=%s", jobRegular.Name))
				util.WaitForActivePodsAndTerminate(ctx, k8sManagerClient, managerRestClient, managerCfg, jobRegular.Namespace, 1, 0, listOpts)
			})

			ginkgo.By("Waiting for both jobs to complete", func() {
				util.ExpectJobToBeCompleted(ctx, k8sManagerClient, jobMk)
				util.ExpectJobToBeCompleted(ctx, k8sManagerClient, jobRegular)
			})
		})
	})
})

func ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx context.Context, client client.Client, wlLookupKey types.NamespacedName, acName string) string {
	ginkgo.GinkgoHelper()
	createdWorkload := &kueue.Workload{}
	var workerName string
	util.ExpectWorkloadsToBeAdmittedByKeys(ctx, client, wlLookupKey)
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(client.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
		admissionCheckMessage := admissioncheck.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(acName)).Message
		workerName = GetMultiKueueClusterNameFromAdmissionCheckMessage(admissionCheckMessage)
		g.Expect(workerName).NotTo(gomega.BeEmpty())
	}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
	return workerName
}

func ensurePodWorkloadsRunning(deployment *appsv1.Deployment, managerNs corev1.Namespace, multiKueueAc *kueue.AdmissionCheck, kubernetesClients map[string]struct {
	client     client.Client
	restClient *rest.RESTClient
	cfg        *rest.Config
}) {
	// Given the unpredictable nature of where the deployment pods run this function gathers the workload of a Pod first
	// it then gets the Pod's assigned cluster from the admission check message and uses the appropriate client to ensure the Pod is running
	pods := &corev1.PodList{}
	gomega.Expect(k8sManagerClient.List(ctx, pods, client.InNamespace(managerNs.Namespace),
		client.MatchingLabels(deployment.Spec.Selector.MatchLabels))).To(gomega.Succeed())
	for i, pod := range pods.Items { // We want to test that all deployment pods have workloads.
		ginkgo.By(fmt.Sprintf("Verifying pod status: %s [%d/%d]", pod.Name, i+1, len(pods.Items)), func() {
			var createdLeaderWorkload *kueue.Workload
			var admissionCheck *kueue.AdmissionCheckState
			wlLookupKey := types.NamespacedName{Name: workloadpod.GetWorkloadNameForPod(pod.Name, pod.UID), Namespace: managerNs.Name}
			ginkgo.By("Checking pod is admitted on manager", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdLeaderWorkload = &kueue.Workload{}
					gomega.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdLeaderWorkload)).To(gomega.Succeed())
					admissionCheck = admissioncheck.FindAdmissionCheck(createdLeaderWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAc.Name))
					gomega.Expect(admissionCheck.State).To(gomega.Equal(kueue.CheckStateReady))
				}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
			})

			// Through checking the assigned cluster we can discern which client to use
			workerClusterName := GetMultiKueueClusterNameFromAdmissionCheckMessage(admissionCheck.Message)
			if workerClusterName == "" {
				ginkgo.Fail(fmt.Sprintf("Could not find Worker Cluster for multikueue admission check message: \"%s\"", admissionCheck.Message))
			}
			workerCluster, ok := kubernetesClients[workerClusterName]
			if !ok {
				ginkgo.Fail(fmt.Sprintf("Worker Cluster not found: %s", workerClusterName))
			}

			ginkgo.By(fmt.Sprintf("Verifying pod is running on worker cluster %s", workerClusterName), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdPod := &corev1.Pod{}
					g.Expect(workerCluster.client.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: pod.Name}, createdPod)).To(gomega.Succeed())
					g.Expect(createdPod.Status.Phase).Should(gomega.Equal(corev1.PodRunning))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	}
}

func GetMultiKueueClusterNameFromAdmissionCheckMessage(message string) string {
	regex := regexp.MustCompile(`"([^"]*)"`)
	// Find the match
	match := regex.FindStringSubmatch(message)
	if len(match) > 1 {
		workerName := match[1]
		return workerName
	}
	return ""
}

func expectJobToBeCreatedAndManagedBy(ctx context.Context, c client.Client, job *batchv1.Job, managedBy string) {
	ginkgo.GinkgoHelper()
	createdJob := &batchv1.Job{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
		g.Expect(ptr.Deref(createdJob.Spec.ManagedBy, "")).To(gomega.Equal(managedBy))
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
}
