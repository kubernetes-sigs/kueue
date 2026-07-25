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
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	workloadpod "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	workloadstatefulset "sigs.k8s.io/kueue/pkg/controller/jobs/statefulset"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingdeployment "sigs.k8s.io/kueue/pkg/util/testingjobs/deployment"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	testingstatefulset "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
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
					// Once the worker Pod was scheduled, its PodScheduled=True condition
					// is synced through to the manager Pod, overwriting the local
					// SchedulingGated condition, so a finished Pod reports that it
					// actually ran on the worker. (While the worker Pod is still
					// unscheduled the local SchedulingGated condition is preserved to
					// keep the Pod invisible to the manager cluster's cluster-autoscaler
					// - the unit tests cover that window, and the interim running state
					// is asserted by the spec below.)
					Type:   corev1.PodScheduled,
					Status: corev1.ConditionTrue,
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

		ginkgo.It("Should overwrite the manager Pod's SchedulingGated condition once it runs on the worker", func() {
			// A Pod that keeps running (BehaviorWaitForDeletion) lets us observe the
			// interim manager-Pod status deterministically, instead of racing a
			// fast-completing Pod: while the manager Pod stays scheduling-gated in its
			// spec, its PodScheduled condition must reflect the worker's
			// PodScheduled=True rather than the local SchedulingGated, so the status
			// shows the Pod is actually running.
			pod := testingpod.MakePod("running-pod", managerNs.Name).
				RequestAndLimit(corev1.ResourceCPU, "100m").
				RequestAndLimit(corev1.ResourceMemory, "100M").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				TerminationGracePeriod(1).
				Queue(managerLq.Name).
				Obj()

			ginkgo.By("Creating the pod", func() {
				util.MustCreate(ctx, k8sManagerClient, pod)
			})

			wlLookupKey := types.NamespacedName{Name: workloadpod.GetWorkloadNameForPod(pod.Name, pod.UID), Namespace: managerNs.Name}

			ginkgo.By("Waiting to be admitted in worker", func() {
				util.ExpectAdmissionCheckStateWithMessage(
					ctx, k8sManagerClient, wlLookupKey,
					multiKueueAc.Name,
					kueue.CheckStateReady,
					"",
				)
			})

			ginkgo.By("Waiting for the running manager Pod to report PodScheduled=True while staying gated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdPod := &corev1.Pod{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(pod), createdPod)).To(gomega.Succeed())
					g.Expect(createdPod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
					// The manager Pod is still gated in its spec ...
					g.Expect(utilpod.HasGate(createdPod, podconstants.SchedulingGateName)).To(gomega.BeTrue())
					// ... but its PodScheduled condition is synced from the worker.
					g.Expect(createdPod.Status.Conditions).To(gomega.ContainElement(gomega.SatisfyAll(
						gomega.HaveField("Type", corev1.PodScheduled),
						gomega.HaveField("Status", corev1.ConditionTrue),
					)))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Deleting the pod", func() {
				gomega.Expect(k8sManagerClient.Delete(ctx, pod)).To(gomega.Succeed())
				util.ExpectAllPodsInNamespaceDeleted(ctx, k8sManagerClient, managerNs)
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

			admittedWorkerName := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
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
					g.Expect(k8sManagerClient.List(ctx, pods, client.InNamespace(managerNs.Name),
						client.MatchingLabels(statefulset.Spec.Selector.MatchLabels))).To(gomega.Succeed())
					g.Expect(pods.Items).ToNot(gomega.BeEmpty())
					for _, pod := range pods.Items {
						g.Expect(utilpod.HasGate(&pod, podconstants.SchedulingGateName)).To(gomega.BeTrue())
					}
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Deleting the statefulset", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sManagerClient, statefulset, true)
				util.ExpectObjectToBeDeletedWithTimeout(ctx, workerClient, statefulset, false, util.MediumTimeout)
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
			admittedWorkerName := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)

			// the execution should be given to the admitted worker
			ginkgo.By("Waiting to be admitted in the worker", func() {
				util.ExpectAdmissionCheckStateWithMessage(
					ctx, k8sManagerClient, wlLookupKey,
					multiKueueAc.Name,
					kueue.CheckStateReady,
					fmt.Sprintf("The workload was admitted on %q", admittedWorkerName),
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
			admittedWorkerName := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
			admittedWorker := kubernetesClients[admittedWorkerName]

			// the execution should be given to the admitted worker
			ginkgo.By("Waiting to be admitted in admitted worker, and the manager's job unsuspended", func() {
				util.ExpectAdmissionCheckStateWithMessage(
					ctx, k8sManagerClient, wlLookupKey,
					multiKueueAc.Name,
					kueue.CheckStateReady,
					fmt.Sprintf("The workload was admitted on %q", admittedWorkerName),
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
	})

	ginkgo.When("Preemption with a multikueue admission check", func() {
		ginkgo.It("Should preempt a running low-priority workload when a high-priority workload is admitted (same worker)", func() {
			lowJob := testingjob.MakeJob("", managerNs.Name).
				GeneratedName("low-job-").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				WorkloadPriorityClass(managerLowWPC.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(extraResourceGPUHighCost, "2").
				TerminationGracePeriod(1).
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
				TerminationGracePeriod(1).
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
					g.Expect(workloadevict.IsEvicted(managerLowWl)).To(gomega.BeTrue())
					g.Expect(managerLowWl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadPreempted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the low-priority workload is deleted from the worker clusters", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, lowWlKey, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
					g.Expect(k8sWorker2Client.Get(ctx, lowWlKey, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
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
					g.Expect(workload.IsAdmitted(managerWl)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed(), util.AssertMsgForMk(ctx, "Workload not admitted in manager", wlKey, k8sManagerClient, k8sWorker1Client, k8sWorker2Client))
			})

			ginkgo.By("Checking that the workload is created on worker1", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, wlKey, workerWorkload)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(workerWorkload)).To(gomega.BeTrue())
					g.Expect(workerWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				}, util.Timeout, util.Interval).Should(gomega.Succeed(), util.AssertMsgForMk(ctx, "Workload not admitted in worker1", wlKey, k8sManagerClient, k8sWorker1Client, k8sWorker2Client))
			})

			ginkgo.By("Checking that the workload is not created on worker2", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker2Client.Get(ctx, wlKey, workerWorkload)).To(utiltesting.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed(), util.AssertMsgForMk(ctx, "Workload present in worker2", wlKey, k8sManagerClient, k8sWorker1Client, k8sWorker2Client))
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
					g.Expect(managerWl.Status.ClusterName).To(gomega.HaveValue(gomega.Equal(workerCluster2.Name)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed(), util.AssertMsgForMk(ctx, "Workload not Admitted in worker2", wlKey, k8sManagerClient, k8sWorker1Client, k8sWorker2Client))
			})

			ginkgo.By("Checking that the workload is created in worker2", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker2Client.Get(ctx, wlKey, workerWorkload)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(workerWorkload)).To(gomega.BeTrue())
					g.Expect(workerWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				}, util.Timeout, util.Interval).Should(gomega.Succeed(), util.AssertMsgForMk(ctx, "Workload not Admitted in worker2", wlKey, k8sManagerClient, k8sWorker1Client, k8sWorker2Client))
			})

			ginkgo.By("Checking that the workload is not created in worker1", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, wlKey, workerWorkload)).To(utiltesting.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed(), util.AssertMsgForMk(ctx, "Workload present in worker1", wlKey, k8sManagerClient, k8sWorker1Client, k8sWorker2Client))
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
				TerminationGracePeriod(1).
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
				TerminationGracePeriod(1).
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
					g.Expect(workloadevict.IsEvicted(managerLowWl)).To(gomega.BeTrue())
					g.Expect(managerLowWl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadPreempted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the low-priority workload is deleted from the worker clusters", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, lowWlKey, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
					g.Expect(k8sWorker2Client.Get(ctx, lowWlKey, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
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
				TerminationGracePeriod(1).
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
				TerminationGracePeriod(1).
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
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
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
				TerminationGracePeriod(1).
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
				TerminationGracePeriod(1).
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
					g.Expect(workloadevict.IsEvicted(managerWl1)).To(gomega.BeTrue())
					g.Expect(managerWl1.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadPreempted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the first workload is deleted from the worker clusters", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, wl1Key, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
					g.Expect(k8sWorker2Client.Get(ctx, wl1Key, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
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
					g.Expect(managerWl.Spec.Priority).To(gomega.Equal(new(managerLowWPC.Value)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the workload was updated on worker", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, wlKey, workerWl)).To(gomega.Succeed())
					g.Expect(workerWl.Spec.Priority).To(gomega.Equal(new(managerLowWPC.Value)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Simulating pod failure", func() {
				util.WaitForActivePodsAndTerminate(ctx, k8sWorker1Client, worker1RestClient, worker1Cfg, worker1Ns.Name, 1, 1)
			})

			ginkgo.By("Checking that the workload still have low priority value", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, wlKey, workerWl)).To(gomega.Succeed())
					g.Expect(workerWl.Spec.Priority).To(gomega.Equal(new(managerLowWPC.Value)))
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
				util.ExpectJobToBeCompletedWithTimeout(ctx, k8sManagerClient, jobMk, util.LongTimeout)
				util.ExpectJobToBeCompletedWithTimeout(ctx, k8sManagerClient, jobRegular, util.LongTimeout)
			})
		})
	})

	ginkgo.Context("MultiKueue Manager Quota Automation", func() {
		ginkgo.BeforeEach(func() {
			ginkgo.By("Setting manager ClusterQueue quotas to zeroes", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					cq := &kueue.ClusterQueue{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(managerCq), cq)).To(gomega.Succeed())
					cq.Spec.ResourceGroups[0].Flavors[0].Resources = []kueue.ResourceQuota{
						{Name: corev1.ResourceCPU, NominalQuota: resource.MustParse("0")},
						{Name: corev1.ResourceMemory, NominalQuota: resource.MustParse("0")},
						{Name: corev1.ResourceEphemeralStorage, NominalQuota: resource.MustParse("0")},
						{Name: extraResourceGPUHighCost, NominalQuota: resource.MustParse("0")},
						{Name: extraResourceGPULowCost, NominalQuota: resource.MustParse("0")},
					}
					g.Expect(k8sManagerClient.Update(ctx, cq)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Enabling QuotaAutomation in MultiKueueConfig", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					cfg := &kueue.MultiKueueConfig{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(multiKueueConfig), cfg)).To(gomega.Succeed())
					cfg.Spec.QuotaManagement = ptr.To(kueue.QuotaManagementAutomated)
					g.Expect(k8sManagerClient.Update(ctx, cfg)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should automatically aggregate quotas from active worker ClusterQueues", func() {
			ginkgo.By("Verifying the MultiKueueManagerQuotaAutomation status condition transitions to true", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					cq := &kueue.ClusterQueue{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(managerCq), cq)).To(gomega.Succeed())

					cond := apimeta.FindStatusCondition(cq.Status.Conditions, kueue.MultiKueueManagerQuotaAutomation)
					g.Expect(cond).NotTo(gomega.BeNil())
					g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionTrue))
					g.Expect(cond.Reason).To(gomega.Equal("QuotaAutomated"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verifying that the nominal resource quotas are automatically aggregated on the manager", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					cq := &kueue.ClusterQueue{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(managerCq), cq)).To(gomega.Succeed())

					g.Expect(cq.Spec.ResourceGroups).To(gomega.HaveLen(1))
					g.Expect(cq.Spec.ResourceGroups[0].Flavors).To(gomega.HaveLen(1))
					g.Expect(cq.Spec.ResourceGroups[0].Flavors[0].Resources).To(gomega.ConsistOf(
						kueue.ResourceQuota{
							Name:         corev1.ResourceCPU,
							NominalQuota: resource.MustParse("3200m"),
						},
						kueue.ResourceQuota{
							Name:         corev1.ResourceMemory,
							NominalQuota: resource.MustParse("5G"),
						},
						kueue.ResourceQuota{
							Name:         corev1.ResourceEphemeralStorage,
							NominalQuota: resource.MustParse("20G"),
						},
						kueue.ResourceQuota{
							Name:         extraResourceGPUHighCost,
							NominalQuota: resource.MustParse("3"),
						},
						kueue.ResourceQuota{
							Name:         extraResourceGPULowCost,
							NominalQuota: resource.MustParse("3"),
						},
					))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})

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
			workerClusterName := util.GetMultiKueueClusterNameFromAdmissionCheckMessage(admissionCheck.Message)
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

func expectJobToBeCreatedAndManagedBy(ctx context.Context, c client.Client, job *batchv1.Job, managedBy string) {
	ginkgo.GinkgoHelper()
	createdJob := &batchv1.Job{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
		g.Expect(ptr.Deref(createdJob.Spec.ManagedBy, "")).To(gomega.Equal(managedBy))
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
}
