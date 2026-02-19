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

package mke2e

import (
	"context"
	"fmt"
	"os/exec"
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
	rbacv1 "k8s.io/api/rbac/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	versionutil "k8s.io/apimachinery/pkg/util/version"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/tools/clientcmd/api"
	apiv1 "k8s.io/client-go/tools/clientcmd/api/v1"
	"k8s.io/utils/ptr"
	inventoryv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueueconfig "sigs.k8s.io/kueue/apis/config/v1beta2"
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
	workloadstatefulset "sigs.k8s.io/kueue/pkg/controller/jobs/statefulset"
	workloadtrainjob "sigs.k8s.io/kueue/pkg/controller/jobs/trainjob"
	"sigs.k8s.io/kueue/pkg/features"
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
	testingstatefulset "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
	testingtrainjob "sigs.k8s.io/kueue/pkg/util/testingjobs/trainjob"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

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
	)

	ginkgo.BeforeEach(func() {
		managerNs = util.CreateNamespaceFromPrefixWithLog(ctx, k8sManagerClient, "multikueue-")
		worker1Ns = util.CreateNamespaceWithLog(ctx, k8sWorker1Client, managerNs.Name)
		worker2Ns = util.CreateNamespaceWithLog(ctx, k8sWorker2Client, managerNs.Name)

		workerCluster1 = utiltestingapi.MakeMultiKueueCluster("worker1").KubeConfig(kueue.SecretLocationType, "multikueue1").Obj()
		util.MustCreate(ctx, k8sManagerClient, workerCluster1)

		workerCluster2 = utiltestingapi.MakeMultiKueueCluster("worker2").KubeConfig(kueue.SecretLocationType, "multikueue2").Obj()
		util.MustCreate(ctx, k8sManagerClient, workerCluster2)

		multiKueueConfig = utiltestingapi.MakeMultiKueueConfig("multikueueconfig").Clusters("worker1", "worker2").Obj()
		util.MustCreate(ctx, k8sManagerClient, multiKueueConfig)

		multiKueueAc = utiltestingapi.MakeAdmissionCheck("ac1").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", multiKueueConfig.Name).
			Obj()
		util.CreateAdmissionChecksAndWaitForActive(ctx, k8sManagerClient, multiKueueAc)

		managerHighWPC = utiltestingapi.MakeWorkloadPriorityClass("high-workload").PriorityValue(300).Obj()
		util.MustCreate(ctx, k8sManagerClient, managerHighWPC)

		managerLowWPC = utiltestingapi.MakeWorkloadPriorityClass("low-workload").PriorityValue(100).Obj()
		util.MustCreate(ctx, k8sManagerClient, managerLowWPC)

		managerFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sManagerClient, managerFlavor)

		managerCq = utiltestingapi.MakeClusterQueue("q1").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(managerFlavor.Name).
					Resource(corev1.ResourceCPU, "2").
					Resource(corev1.ResourceMemory, "4G").
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

		worker1HighWPC = utiltestingapi.MakeWorkloadPriorityClass("high-workload").PriorityValue(300).Obj()
		util.MustCreate(ctx, k8sWorker1Client, worker1HighWPC)

		worker1LowWPC = utiltestingapi.MakeWorkloadPriorityClass("low-workload").PriorityValue(100).Obj()
		util.MustCreate(ctx, k8sWorker1Client, worker1LowWPC)

		worker1Flavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sWorker1Client, worker1Flavor)

		worker1Cq = utiltestingapi.MakeClusterQueue("q1").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(worker1Flavor.Name).
					Resource(corev1.ResourceCPU, "2").
					Resource(corev1.ResourceMemory, "1G").
					Obj(),
			).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sWorker1Client, worker1Cq)

		worker1Lq = utiltestingapi.MakeLocalQueue(worker1Cq.Name, worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sWorker1Client, worker1Lq)

		worker2HighWPC = utiltestingapi.MakeWorkloadPriorityClass("high-workload").PriorityValue(300).Obj()
		util.MustCreate(ctx, k8sWorker2Client, worker2HighWPC)

		worker2LowWPC = utiltestingapi.MakeWorkloadPriorityClass("low-workload").PriorityValue(100).Obj()
		util.MustCreate(ctx, k8sWorker2Client, worker2LowWPC)

		worker2Flavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sWorker2Client, worker2Flavor)

		worker2Cq = utiltestingapi.MakeClusterQueue("q1").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(worker2Flavor.Name).
					Resource(corev1.ResourceCPU, "1").
					Resource(corev1.ResourceMemory, "4G").
					Obj(),
			).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sWorker2Client, worker2Cq)

		worker2Lq = utiltestingapi.MakeLocalQueue(worker2Cq.Name, worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sWorker2Client, worker2Lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sManagerClient, managerNs)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sWorker1Client, worker1Ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sWorker2Client, worker2Ns)).To(gomega.Succeed())

		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1Cq, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1Flavor, true, util.LongTimeout)
		util.ExpectObjectToBeDeleted(ctx, k8sWorker1Client, worker1HighWPC, true)
		util.ExpectObjectToBeDeleted(ctx, k8sWorker1Client, worker1LowWPC, true)

		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, worker2Cq, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, worker2Flavor, true, util.LongTimeout)
		util.ExpectObjectToBeDeleted(ctx, k8sWorker2Client, worker2HighWPC, true)
		util.ExpectObjectToBeDeleted(ctx, k8sWorker2Client, worker2LowWPC, true)

		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerCq, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerFlavor, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerHighWPC, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerLowWPC, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, multiKueueAc, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, multiKueueConfig, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, workerCluster1, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, workerCluster2, true, util.LongTimeout)

		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sManagerClient, managerNs)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sWorker1Client, worker1Ns)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sWorker2Client, worker2Ns)
	})

	ginkgo.When("Creating a multikueue admission check", func() {
		ginkgo.It("Should create a pod on worker if admitted", func() {
			pod := testingpod.MakePod("pod", managerNs.Name).
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				RequestAndLimit(corev1.ResourceCPU, "1").
				RequestAndLimit(corev1.ResourceMemory, "2G").
				Queue(managerLq.Name).
				Obj()
			// Since it requires 2G of memory, this pod can only be admitted in worker 2.

			ginkgo.By("Creating the pod", func() {
				util.MustCreate(ctx, k8sManagerClient, pod)
				gomega.Eventually(func(g gomega.Gomega) {
					createdPod := &corev1.Pod{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(pod), createdPod)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			createdLeaderWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadpod.GetWorkloadNameForPod(pod.Name, pod.UID), Namespace: managerNs.Name}

			// the execution should be given to worker2
			ginkgo.By("Waiting to be admitted in worker2", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdLeaderWorkload)).To(gomega.Succeed())
					g.Expect(admissioncheck.FindAdmissionCheck(createdLeaderWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAc.Name))).To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
						Name:    kueue.AdmissionCheckReference(multiKueueAc.Name),
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker2"`,
					}, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime")))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

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
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create pods on worker if the deployment is admitted", func() {
			var kubernetesClients = map[string]client.Client{
				"worker1": k8sWorker1Client,
				"worker2": k8sWorker2Client,
			}

			deployment := testingdeployment.MakeDeployment("deployment", managerNs.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Replicas(3).
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
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
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
					g.Expect(createdDeployment.Status.Conditions).To(gomega.BeComparableTo(deploymentConditions, cmpopts.IgnoreFields(appsv1.DeploymentCondition{}, "LastTransitionTime", "LastUpdateTime", "Message"))) // Ignoring message as it is required to gather the pod replica set
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Increasing the replica count on the deployment", func() {
				createdDeployment := &appsv1.Deployment{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(deployment), createdDeployment)).To(gomega.Succeed())
					createdDeployment.Spec.Replicas = ptr.To[int32](4)
					g.Expect(k8sManagerClient.Update(ctx, createdDeployment)).To(gomega.Succeed())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

				ginkgo.By("Wait for replicas ready", func() {
					createdDeployment := &appsv1.Deployment{}
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(deployment), createdDeployment)).To(gomega.Succeed())
						g.Expect(createdDeployment.Status.ReadyReplicas).To(gomega.Equal(int32(4)))
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
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
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

				ginkgo.By("Wait for replicas ready", func() {
					createdDeployment := &appsv1.Deployment{}
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(deployment), createdDeployment)).To(gomega.Succeed())
						g.Expect(createdDeployment.Status.ReadyReplicas).To(gomega.Equal(int32(2)))
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
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
						g.Expect(workerClient.List(ctx, pods, client.InNamespace(managerNs.Namespace), client.MatchingLabels(deployment.Spec.Selector.MatchLabels))).To(gomega.Succeed())
						g.Expect(pods.Items).To(gomega.BeEmpty())
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				}
			})
		})

		ginkgo.It("Should sync a StatefulSet to worker if admitted", func() {
			statefulset := testingstatefulset.MakeStatefulSet("statefulset", managerNs.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Replicas(3).
				Request(corev1.ResourceCPU, "500m").
				Queue(managerLq.Name).
				Obj()

			ginkgo.By("Creating the statefulset", func() {
				util.MustCreate(ctx, k8sManagerClient, statefulset)
			})

			wlLookupKey := types.NamespacedName{
				Name:      workloadstatefulset.GetWorkloadName(statefulset.Name),
				Namespace: managerNs.Name,
			}

			waitForJobAdmitted(wlLookupKey, multiKueueAc.Name, "worker1")

			ginkgo.By("Verifying status is synced back to manager", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					managerSts := &appsv1.StatefulSet{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(statefulset), managerSts)).To(gomega.Succeed())
					g.Expect(managerSts.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verifying pods on management cluster remain gated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					pods := &corev1.PodList{}
					g.Expect(k8sManagerClient.List(ctx, pods, client.InNamespace(managerNs.Name), client.MatchingLabels{
						podconstants.GroupNameLabel: workloadstatefulset.GetWorkloadName(statefulset.Name),
					})).To(gomega.Succeed())
					g.Expect(pods.Items).ToNot(gomega.BeEmpty())
					for _, pod := range pods.Items {
						g.Expect(utilpod.HasGate(&pod, podconstants.SchedulingGateName)).To(gomega.BeTrue())
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Deleting the statefulset", func() {
				gomega.Expect(k8sManagerClient.Delete(ctx, statefulset)).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					workerSts := &appsv1.StatefulSet{}
					err := k8sWorker1Client.Get(ctx, client.ObjectKeyFromObject(statefulset), workerSts)
					g.Expect(err).To(gomega.HaveOccurred())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should sync a LeaderWorkerSet and run replicas on worker cluster", func() {
			lws := testingleaderworkerset.MakeLeaderWorkerSet("leaderworkerset", managerNs.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Replicas(2).
				Size(2).
				Request(corev1.ResourceCPU, "100m").
				Request(corev1.ResourceMemory, "1G").
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

			ginkgo.By("Verifying both workloads are admitted on worker2", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					wl0 := &kueue.Workload{}
					g.Expect(k8sWorker2Client.Get(ctx, wlLookupKey0, wl0)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(wl0)).To(gomega.BeTrue())
					wl1 := &kueue.Workload{}
					g.Expect(k8sWorker2Client.Get(ctx, wlLookupKey1, wl1)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(wl1)).To(gomega.BeTrue())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for LWS to be synced to worker cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					workerLWS := &leaderworkersetv1.LeaderWorkerSet{}
					g.Expect(k8sWorker2Client.Get(ctx, client.ObjectKeyFromObject(lws), workerLWS)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for all replicas to be ready on worker cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					workerLWS := &leaderworkersetv1.LeaderWorkerSet{}
					g.Expect(k8sWorker2Client.Get(ctx, client.ObjectKeyFromObject(lws), workerLWS)).To(gomega.Succeed())
					g.Expect(workerLWS.Status.ReadyReplicas).To(gomega.Equal(int32(2)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
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
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, lws, false, util.LongTimeout)
			})

			ginkgo.By("Checking that all workloads are deleted from manager and worker clusters", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey0, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey1, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
					g.Expect(k8sWorker2Client.Get(ctx, wlLookupKey0, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
					g.Expect(k8sWorker2Client.Get(ctx, wlLookupKey1, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should dispatch all LeaderWorkerSet workloads to the same worker", func() {
			const lwsReplicas = 3
			lws := testingleaderworkerset.MakeLeaderWorkerSet("leaderworkerset", managerNs.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Replicas(lwsReplicas).
				Size(2).
				Request(corev1.ResourceCPU, "100m").
				Request(corev1.ResourceMemory, "600M").
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

			ginkgo.By("Verifying primary workload is admitted on worker2", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					wl := &kueue.Workload{}
					g.Expect(k8sWorker2Client.Get(ctx, wlKeys[0], wl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verifying LWS is synced to worker cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					workerLWS := &leaderworkersetv1.LeaderWorkerSet{}
					g.Expect(k8sWorker2Client.Get(ctx, client.ObjectKeyFromObject(lws), workerLWS)).To(gomega.Succeed())
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
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Deleting the leaderworkerset", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sManagerClient, lws, true)
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, lws, false, util.LongTimeout)
			})

			ginkgo.By("Checking that all workloads are deleted from manager and worker clusters", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					for _, key := range wlKeys {
						g.Expect(k8sManagerClient.Get(ctx, key, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
						g.Expect(k8sWorker2Client.Get(ctx, key, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create a pod group on worker if admitted", func() {
			numPods := 2
			groupName := "test-group"
			group := testingpod.MakePod(groupName, managerNs.Name).
				Queue(managerLq.Name).
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				RequestAndLimit(corev1.ResourceMemory, "1G").
				MakeGroup(numPods)

			ginkgo.By("Creating the Pod group", func() {
				for _, p := range group {
					gomega.Expect(k8sManagerClient.Create(ctx, p)).To(gomega.Succeed())
					gomega.Expect(p.Spec.SchedulingGates).To(gomega.ContainElements(
						corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName},
					))
				}
			})

			// Since it requires 2G of memory, this pod group can only be admitted in worker 2.
			pods := &corev1.PodList{}
			ginkgo.By("ensure all pods are created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.List(ctx, pods, client.InNamespace(managerNs.Name))).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdLeaderWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: groupName, Namespace: managerNs.Name}

			// the execution should be given to worker2
			ginkgo.By("Waiting to be admitted in worker2", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdLeaderWorkload)).To(gomega.Succeed())
					g.Expect(admissioncheck.FindAdmissionCheck(createdLeaderWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAc.Name))).To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
						Name:    kueue.AdmissionCheckReference(multiKueueAc.Name),
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker2"`,
					}, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime")))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

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
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should run a job on worker if admitted", func() {
			// Since it requires 2G of memory, this job can only be admitted in worker 2.
			job := testingjob.MakeJob("job", managerNs.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "1").
				RequestAndLimit(corev1.ResourceMemory, "2G").
				TerminationGracePeriod(1).
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

			// the execution should be given to the worker
			ginkgo.By("Waiting to be admitted in worker2, and the manager's job unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdLeaderWorkload)).To(gomega.Succeed())
					g.Expect(admissioncheck.FindAdmissionCheck(createdLeaderWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAc.Name))).To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
						Name:    kueue.AdmissionCheckReference(multiKueueAc.Name),
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker2"`,
					}, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime")))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

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
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Finishing the job's pod", func() {
				listOpts := util.GetListOptsFromLabel(fmt.Sprintf("batch.kubernetes.io/job-name=%s", job.Name))
				util.WaitForActivePodsAndTerminate(ctx, k8sWorker2Client, worker2RestClient, worker2Cfg, job.Namespace, 1, 0, listOpts)
			})

			ginkgo.By("Waiting for the job to finish", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdLeaderWorkload)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(kueue.WorkloadFinished, kueue.WorkloadFinishedReasonSucceeded))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking no objects are left in the worker clusters and the job is completed", func() {
				expectObjectToBeDeletedOnWorkerClusters(ctx, createdLeaderWorkload)
				expectObjectToBeDeletedOnWorkerClusters(ctx, job)

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

		ginkgo.It("Should preempt a running low-priority workload when a high-priority workload is admitted (same worker)", func() {
			lowJob := testingjob.MakeJob("low-job", managerNs.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				WorkloadPriorityClass(managerLowWPC.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "2").
				RequestAndLimit(corev1.ResourceMemory, "1G").
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

			highJob := testingjob.MakeJob("high-job", managerNs.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				WorkloadPriorityClass(managerHighWPC.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "2").
				RequestAndLimit(corev1.ResourceMemory, "1G").
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
				RequestAndLimit(corev1.ResourceCPU, "1.5").
				RequestAndLimit(corev1.ResourceMemory, "0.5G").
				Obj()
			util.MustCreate(ctx, k8sManagerClient, job)

			wlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}
			managerWl := &kueue.Workload{}
			workerWorkload := &kueue.Workload{}

			ginkgo.By("Checking that the workload is created and admitted in the manager cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlKey, managerWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(managerWl)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the workload is created on worker1", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, wlKey, workerWorkload)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(workerWorkload)).To(gomega.BeTrue())
					g.Expect(workerWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))

					g.Expect(k8sWorker2Client.Get(ctx, wlKey, workerWorkload)).To(utiltesting.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Switching worker cluster queues' resources to enforce re-admission on the worker2", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, client.ObjectKeyFromObject(worker1Cq), worker1Cq)).To(gomega.Succeed())
					worker1Cq.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota = resource.MustParse("1")
					g.Expect(k8sWorker1Client.Update(ctx, worker1Cq)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker2Client.Get(ctx, client.ObjectKeyFromObject(worker2Cq), worker2Cq)).To(gomega.Succeed())
					worker2Cq.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota = resource.MustParse("2")
					g.Expect(k8sWorker2Client.Update(ctx, worker2Cq)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Triggering eviction in worker1", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, wlKey, workerWorkload)).To(gomega.Succeed())
					g.Expect(workload.SetConditionAndUpdate(ctx, k8sWorker1Client, workerWorkload, kueue.WorkloadEvicted, metav1.ConditionTrue, kueue.WorkloadEvictedByPreemption, "By test", "evict", util.RealClock)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the workload is re-admitted in worker2", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlKey, managerWl)).To(gomega.Succeed())
					g.Expect(managerWl.Status.ClusterName).To(gomega.HaveValue(gomega.Equal("worker2")))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the workload is created in worker2", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker2Client.Get(ctx, wlKey, workerWorkload)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(workerWorkload)).To(gomega.BeTrue())
					g.Expect(workerWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))

					g.Expect(k8sWorker1Client.Get(ctx, wlKey, workerWorkload)).To(utiltesting.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should preempt a running low-priority workload when a high-priority workload is admitted (other workers)", func() {
			lowJob := testingjob.MakeJob("low-job", managerNs.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				WorkloadPriorityClass(managerLowWPC.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "2").
				RequestAndLimit(corev1.ResourceMemory, "1G").
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

			highJob := testingjob.MakeJob("high-job", managerNs.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				WorkloadPriorityClass(managerHighWPC.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "1").
				RequestAndLimit(corev1.ResourceMemory, "2G").
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

		ginkgo.It("Should allow to mutate kueue.x-k8s.io/priority-class (other workers)", func() {
			job1 := testingjob.MakeJob("job1", managerNs.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				WorkloadPriorityClass(managerHighWPC.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "2").
				RequestAndLimit(corev1.ResourceMemory, "1G").
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
				RequestAndLimit(corev1.ResourceCPU, "1").
				RequestAndLimit(corev1.ResourceMemory, "2G").
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

		ginkgo.It("Should run a jobSet on worker if admitted", func() {
			// Since it requires 2 CPU in total, this jobset can only be admitted in worker 1.
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
				RequestAndLimit("replicated-job-1", corev1.ResourceCPU, "500m").
				RequestAndLimit("replicated-job-1", corev1.ResourceMemory, "200M").
				Obj()

			ginkgo.By("Creating the jobSet", func() {
				util.MustCreate(ctx, k8sManagerClient, jobSet)
			})

			createdLeaderWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: managerNs.Name}
			// the execution should be given to the worker1
			waitForJobAdmitted(wlLookupKey, multiKueueAc.Name, "worker1")

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
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Finishing the jobset pods", func() {
				listOpts := util.GetListOptsFromLabel(fmt.Sprintf("jobset.sigs.k8s.io/jobset-name=%s", jobSet.Name))
				util.WaitForActivePodsAndTerminate(ctx, k8sWorker1Client, worker1RestClient, worker1Cfg, jobSet.Namespace, 4, 0, listOpts)
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
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking no objects are left in the worker clusters and the jobSet is completed", func() {
				expectObjectToBeDeletedOnWorkerClusters(ctx, createdLeaderWorkload)
				expectObjectToBeDeletedOnWorkerClusters(ctx, jobSet)

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
			// Since it requires 2 CPU in total, this appwrapper can only be admitted in worker 1.
			jobName := "job-1"
			aw := testingaw.MakeAppWrapper("aw", managerNs.Name).
				Queue(managerLq.Name).
				Component(testingaw.Component{
					Template: testingjob.MakeJob(jobName, managerNs.Name).
						SetTypeMeta().
						Suspend(false).
						Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion). // Give it the time to be observed Active in the live status update step.
						Parallelism(2).
						RequestAndLimit(corev1.ResourceCPU, "1").
						TerminationGracePeriod(1).
						SetTypeMeta().Obj(),
				}).
				Obj()

			ginkgo.By("Creating the appwrapper", func() {
				util.MustCreate(ctx, k8sManagerClient, aw)
			})

			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadaw.GetWorkloadNameForAppWrapper(aw.Name, aw.UID), Namespace: managerNs.Name}

			// the execution should be given to worker 1
			waitForJobAdmitted(wlLookupKey, multiKueueAc.Name, "worker1")

			ginkgo.By("Waiting for the appwrapper to get status updates", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdAppWrapper := &awv1beta2.AppWrapper{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(aw), createdAppWrapper)).To(gomega.Succeed())
					g.Expect(createdAppWrapper.Status.Phase).To(gomega.Equal(awv1beta2.AppWrapperRunning))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Finishing the wrapped job's pods", func() {
				listOpts := util.GetListOptsFromLabel(fmt.Sprintf("batch.kubernetes.io/job-name=%s", jobName))
				util.WaitForActivePodsAndTerminate(ctx, k8sWorker1Client, worker1RestClient, worker1Cfg, aw.Namespace, 2, 0, listOpts)
			})

			ginkgo.By("Waiting for the appwrapper to finish", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())

					g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeComparableTo(&metav1.Condition{
						Type:    kueue.WorkloadFinished,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadFinishedReasonSucceeded,
						Message: "AppWrapper finished successfully",
					}, util.IgnoreConditionTimestampsAndObservedGeneration))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking no objects are left in the worker clusters and the appwrapper is completed", func() {
				expectObjectToBeDeletedOnWorkerClusters(ctx, createdWorkload)
				expectObjectToBeDeletedOnWorkerClusters(ctx, aw)

				createdAppWrapper := &awv1beta2.AppWrapper{}
				gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(aw), createdAppWrapper)).To(gomega.Succeed())
				gomega.Expect(createdAppWrapper.Spec.Suspend).To(gomega.BeFalse())
				gomega.Expect(createdAppWrapper.Status.Phase).To(gomega.Equal(awv1beta2.AppWrapperSucceeded))
			})
		})

		ginkgo.It("Should run a kubeflow PyTorchJob on worker if admitted", func() {
			// Since it requires 1600M of memory, this job can only be admitted in worker 2.
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
				RequestAndLimit(kftraining.PyTorchJobReplicaTypeMaster, corev1.ResourceCPU, "1").
				RequestAndLimit(kftraining.PyTorchJobReplicaTypeMaster, corev1.ResourceMemory, "1600M").
				Obj()

			ginkgo.By("Creating the PyTorchJob", func() {
				util.MustCreate(ctx, k8sManagerClient, pyTorchJob)
			})

			wlLookupKey := types.NamespacedName{Name: workloadpytorchjob.GetWorkloadNameForPyTorchJob(pyTorchJob.Name, pyTorchJob.UID), Namespace: managerNs.Name}

			// the execution should be given to the worker 2
			waitForJobAdmitted(wlLookupKey, multiKueueAc.Name, "worker2")

			ginkgo.By("Waiting for the PyTorchJob to finish", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdPyTorchJob := &kftraining.PyTorchJob{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(pyTorchJob), createdPyTorchJob)).To(gomega.Succeed())
					g.Expect(createdPyTorchJob.Status.ReplicaStatuses[kftraining.PyTorchJobReplicaTypeMaster]).To(gomega.BeComparableTo(
						&kftraining.ReplicaStatus{
							Active:    0,
							Succeeded: 1,
							Selector:  fmt.Sprintf("training.kubeflow.org/job-name=%s,training.kubeflow.org/operator-name=pytorchjob-controller,training.kubeflow.org/replica-type=master", createdPyTorchJob.Name),
						},
					))

					finishReasonMessage := fmt.Sprintf("PyTorchJob %s is successfully completed.", pyTorchJob.Name)
					checkFinishStatusCondition(g, wlLookupKey, finishReasonMessage)
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking no objects are left in the worker clusters and the PyTorchJob is completed", func() {
				wl := &kueue.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Name:      wlLookupKey.Name,
						Namespace: wlLookupKey.Namespace,
					},
				}
				expectObjectToBeDeletedOnWorkerClusters(ctx, wl)
				expectObjectToBeDeletedOnWorkerClusters(ctx, pyTorchJob)
			})
		})

		ginkgo.It("Should run a MPIJob on worker if admitted", func() {
			// Since it requires 1.5 CPU, this job can only be admitted in worker 1.
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
				RequestAndLimit(kfmpi.MPIReplicaTypeLauncher, corev1.ResourceCPU, "1").
				RequestAndLimit(kfmpi.MPIReplicaTypeLauncher, corev1.ResourceMemory, "200M").
				RequestAndLimit(kfmpi.MPIReplicaTypeWorker, corev1.ResourceCPU, "0.5").
				RequestAndLimit(kfmpi.MPIReplicaTypeWorker, corev1.ResourceMemory, "100M").
				Obj()

			ginkgo.By("Creating the MPIJob", func() {
				util.MustCreate(ctx, k8sManagerClient, mpijob)
			})

			wlLookupKey := types.NamespacedName{Name: workloadmpijob.GetWorkloadNameForMPIJob(mpijob.Name, mpijob.UID), Namespace: managerNs.Name}

			// the execution should be given to the worker1
			waitForJobAdmitted(wlLookupKey, multiKueueAc.Name, "worker1")

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

					finishReasonMessage := fmt.Sprintf("MPIJob %s successfully completed.", client.ObjectKeyFromObject(mpijob).String())
					checkFinishStatusCondition(g, wlLookupKey, finishReasonMessage)
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking no objects are left in the worker clusters and the MPIJob is completed", func() {
				wl := &kueue.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Name:      wlLookupKey.Name,
						Namespace: wlLookupKey.Namespace,
					},
				}
				expectObjectToBeDeletedOnWorkerClusters(ctx, wl)
				expectObjectToBeDeletedOnWorkerClusters(ctx, mpijob)
			})
		})

		ginkgo.It("Should run a RayJob on worker if admitted", func() {
			kuberayTestImage := util.GetKuberayTestImage()
			// Since it requires 1.5 CPU, this job can only be admitted in worker 1.
			rayjob := testingrayjob.MakeJob("rayjob1", managerNs.Name).
				Suspend(true).
				Queue(managerLq.Name).
				WithSubmissionMode(rayv1.K8sJobMode).
				RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "1").
				RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "0.5").
				Entrypoint("python -c \"import ray; ray.init(); print(ray.cluster_resources())\"").
				Image(rayv1.HeadNode, kuberayTestImage).
				Image(rayv1.WorkerNode, kuberayTestImage).
				Obj()

			ginkgo.By("Creating the RayJob", func() {
				util.MustCreate(ctx, k8sManagerClient, rayjob)
			})

			wlLookupKey := types.NamespacedName{Name: workloadrayjob.GetWorkloadNameForRayJob(rayjob.Name, rayjob.UID), Namespace: managerNs.Name}
			// the execution should be given to the worker1
			waitForJobAdmitted(wlLookupKey, multiKueueAc.Name, "worker1")

			ginkgo.By("Waiting for the RayJob to finish", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdRayJob := &rayv1.RayJob{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(rayjob), createdRayJob)).To(gomega.Succeed())
					g.Expect(createdRayJob.Status.JobDeploymentStatus).To(gomega.Equal(rayv1.JobDeploymentStatusComplete))
					finishReasonMessage := "Job finished successfully."
					checkFinishStatusCondition(g, wlLookupKey, finishReasonMessage)
				}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking no objects are left in the worker clusters and the RayJob is completed", func() {
				wl := &kueue.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Name:      wlLookupKey.Name,
						Namespace: wlLookupKey.Namespace,
					},
				}
				expectObjectToBeDeletedOnWorkerClusters(ctx, wl)
				expectObjectToBeDeletedOnWorkerClusters(ctx, rayjob)
			})
		})

		ginkgo.It("Should run a RayCluster on worker if admitted", func() {
			kuberayTestImage := util.GetKuberayTestImage()
			// Since it requires 1.5 CPU, this job can only be admitted in worker 1.
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
			waitForJobAdmitted(wlLookupKey, multiKueueAc.Name, "worker1")

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

		ginkgo.It("Should run a TrainJob on worker if admitted", func() {
			trainjob := testingtrainjob.MakeTrainJob("trainjob-test", managerNs.Name).
				RuntimeRefName("torch-distributed").
				Queue(managerLq.Name).
				// Even if we override the image coming from the TrainingRuntime, we still need to set the command and args
				TrainerImage(util.GetAgnHostImage(), []string{"/agnhost"}, util.BehaviorExitFast).
				TrainerRequest(corev1.ResourceCPU, "2").
				TrainerRequest(corev1.ResourceMemory, "1G").
				Obj()

			ginkgo.By("Creating the trainjob", func() {
				util.MustCreate(ctx, k8sManagerClient, trainjob)
			})

			wlLookupKey := types.NamespacedName{Name: workloadtrainjob.GetWorkloadNameForTrainJob(trainjob.Name, trainjob.UID), Namespace: managerNs.Name}
			// the execution should be given to the worker1
			waitForJobAdmitted(wlLookupKey, multiKueueAc.Name, "worker1")

			ginkgo.By("Checking the TrainJob is ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdTrainJob := &kftrainer.TrainJob{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(trainjob), createdTrainJob)).To(gomega.Succeed())
					g.Expect(ptr.Deref(createdTrainJob.Spec.Suspend, false)).To(gomega.BeFalse())
				}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("Incremental mode", ginkgo.Ordered, func() {
		var defaultManagerKueueCfg *kueueconfig.Configuration

		ginkgo.BeforeAll(func() {
			ginkgo.By("setting MultiKueue Dispatcher to Incremental", func() {
				defaultManagerKueueCfg = util.GetKueueConfiguration(ctx, k8sManagerClient)
				newCfg := defaultManagerKueueCfg.DeepCopy()
				util.UpdateKueueConfigurationAndRestart(ctx, k8sManagerClient, newCfg, managerClusterName, func(cfg *kueueconfig.Configuration) {
					if cfg.MultiKueue == nil {
						cfg.MultiKueue = &kueueconfig.MultiKueue{}
					}
					cfg.MultiKueue.DispatcherName = ptr.To(kueueconfig.MultiKueueDispatcherModeIncremental)
				})
			})
		})
		ginkgo.AfterAll(func() {
			ginkgo.By("setting MultiKueue Dispatcher back to AllAtOnce", func() {
				util.UpdateKueueConfigurationAndRestart(ctx, k8sManagerClient, defaultManagerKueueCfg, managerClusterName)
			})
		})
		ginkgo.It("Should run a job on worker if admitted", func() {
			// Since it requires 2G of memory, this job can only be admitted in worker 2.
			job := testingjob.MakeJob("job", managerNs.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "1").
				RequestAndLimit(corev1.ResourceMemory, "2G").
				TerminationGracePeriod(1).
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
			// the execution should be given to the worker
			waitForJobAdmitted(wlLookupKey, multiKueueAc.Name, "worker2")

			ginkgo.By("Waiting for the manager's job unsuspended", func() {
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
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Finishing the job's pod", func() {
				listOpts := util.GetListOptsFromLabel(fmt.Sprintf("batch.kubernetes.io/job-name=%s", job.Name))
				util.WaitForActivePodsAndTerminate(ctx, k8sWorker2Client, worker2RestClient, worker2Cfg, job.Namespace, 1, 0, listOpts)
			})

			ginkgo.By("Waiting for the job to finish", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdLeaderWorkload)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(kueue.WorkloadFinished, kueue.WorkloadFinishedReasonSucceeded))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking no objects are left in the worker clusters and the job is completed", func() {
				expectObjectToBeDeletedOnWorkerClusters(ctx, createdLeaderWorkload)
				expectObjectToBeDeletedOnWorkerClusters(ctx, job)

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

	ginkgo.When("The connection to a worker cluster is unreliable", func() {
		ginkgo.It("Should update the cluster status to reflect the connection state", func() {
			worker1Cq2 := utiltestingapi.MakeClusterQueue("q2").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(worker1Flavor.Name).
						Resource(corev1.ResourceCPU, "2").
						Resource(corev1.ResourceMemory, "1G").
						Obj(),
				).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sWorker1Client, worker1Cq2)

			worker1Container := fmt.Sprintf("%s-control-plane", worker1ClusterName)
			worker1ClusterKey := client.ObjectKeyFromObject(workerCluster1)
			ginkgo.By("Disconnecting worker1 node's APIServer", func() {
				sedCommand := `sed -i '/^[[:space:]]*- kube-apiserver/a\    - --bind-address=127.0.0.1' /etc/kubernetes/manifests/kube-apiserver.yaml`
				cmd := exec.Command("docker", "exec", worker1Container, "sh", "-c", sedCommand)
				output, err := cmd.CombinedOutput()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)

				podList := &corev1.PodList{}
				podListOptions := client.InNamespace(kueueNS)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.List(ctx, podList, podListOptions)).Should(gomega.Succeed())
				}, util.LongTimeout, util.Interval).ShouldNot(gomega.Succeed())
			})

			ginkgo.By("Waiting for the cluster to become inactive", func() {
				readClient := &kueue.MultiKueueCluster{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, worker1ClusterKey, readClient)).To(gomega.Succeed())
					g.Expect(readClient.Status.Conditions).To(utiltesting.HaveConditionStatusFalseAndReason(kueue.MultiKueueClusterActive, "ClientConnectionFailed"))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Reconnecting worker1 node's APIServer", func() {
				sedCommand := `sed -i '/^[[:space:]]*- --bind-address=127.0.0.1/d' /etc/kubernetes/manifests/kube-apiserver.yaml`
				cmd := exec.Command("docker", "exec", worker1Container, "sh", "-c", sedCommand)
				output, err := cmd.CombinedOutput()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			})

			ginkgo.By("Waiting for the cluster to become active", func() {
				readClient := &kueue.MultiKueueCluster{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, worker1ClusterKey, readClient)).To(gomega.Succeed())
					g.Expect(readClient.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(
						metav1.Condition{
							Type:    kueue.MultiKueueClusterActive,
							Status:  metav1.ConditionTrue,
							Reason:  "Active",
							Message: "Connected",
						},
						util.IgnoreConditionTimestampsAndObservedGeneration)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for kube-system to become available again", func() {
				util.WaitForKubeSystemControllersAvailability(ctx, k8sWorker1Client, worker1Container)
			})

			ginkgo.By("Restart Kueue and wait for availability again", func() {
				util.RestartKueueController(ctx, k8sWorker1Client, worker1ClusterName)
			})

			ginkgo.By("Checking that the Kueue is operational after reconnection", func() {
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1Cq2, true, util.StartUpTimeout)
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
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerRegularCq, true, util.LongTimeout)
		})

		ginkgo.It("should allow to run a MultiKueue and a regular Job on the same cluster", func() {
			jobMk := testingjob.MakeJob("job-mk", managerNs.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "1").
				RequestAndLimit(corev1.ResourceMemory, "2G").
				TerminationGracePeriod(1).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Obj()
			jobRegular := testingjob.MakeJob("job-regular", managerNs.Name).
				Queue(kueue.LocalQueueName(managerRegularLq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "1").
				RequestAndLimit(corev1.ResourceMemory, "2G").
				TerminationGracePeriod(1).
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

	ginkgo.When("Connection via ClusterProfile no plugins", ginkgo.Ordered, func() {
		var (
			workerCluster3         *kueue.MultiKueueCluster
			defaultManagerKueueCfg *kueueconfig.Configuration
			cp                     *inventoryv1alpha1.ClusterProfile
		)

		ginkgo.BeforeAll(func() {
			ginkgo.By("setting MultiKueueClusterProfile feature gate", func() {
				defaultManagerKueueCfg = util.GetKueueConfiguration(ctx, k8sManagerClient)
				newCfg := defaultManagerKueueCfg.DeepCopy()
				util.UpdateKueueConfigurationAndRestart(ctx, k8sManagerClient, newCfg, managerClusterName, func(cfg *kueueconfig.Configuration) {
					cfg.FeatureGates[string(features.MultiKueueClusterProfile)] = true
				})
			})
		})
		ginkgo.AfterAll(func() {
			ginkgo.By("reverting the configuration", func() {
				util.UpdateKueueConfigurationAndRestart(ctx, k8sManagerClient, defaultManagerKueueCfg, managerClusterName)
			})
		})

		ginkgo.BeforeEach(func() {
			workerCluster3 = utiltestingapi.MakeMultiKueueCluster("worker3").ClusterProfile("clusterprofile3-missing").Obj()
			util.MustCreate(ctx, k8sManagerClient, workerCluster3)
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, cp, true, util.LongTimeout)
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, workerCluster3, true, util.LongTimeout)
		})

		ginkgo.It("uses ClusterProfile as way to connect worker cluster", func() {
			ginkgo.By("updating MultiKueueConfig to include worker that use ClusterProfile", func() {
				createdMkConfig := &kueue.MultiKueueConfig{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(multiKueueConfig), createdMkConfig)).To(gomega.Succeed())
					if len(createdMkConfig.Spec.Clusters) == 2 {
						createdMkConfig.Spec.Clusters = append(createdMkConfig.Spec.Clusters, "worker3")
						g.Expect(k8sManagerClient.Update(ctx, createdMkConfig)).To(gomega.Succeed())
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
			worker3MkClusterKey := client.ObjectKeyFromObject(workerCluster3)
			ginkgo.By("checking MultiKueueCluster status", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdCluster := &kueue.MultiKueueCluster{}
					g.Expect(k8sManagerClient.Get(ctx, worker3MkClusterKey, createdCluster)).To(gomega.Succeed())
					g.Expect(createdCluster.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(
						metav1.Condition{
							Type:    kueue.MultiKueueClusterActive,
							Status:  metav1.ConditionFalse,
							Reason:  "BadClusterProfile",
							Message: "load client config failed: ClusterProfile.multicluster.x-k8s.io \"clusterprofile3-missing\" not found",
						},
						util.IgnoreConditionTimestampsAndObservedGeneration)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("creating missing ClusterProfile", func() {
				cp = utiltestingapi.MakeClusterProfile("clusterprofile3", kueueNS).
					ClusterManager("clustermanager3").
					Obj()
				util.MustCreate(ctx, k8sManagerClient, cp)
			})
			ginkgo.By("checking ClusterProfile exists", func() {
				clusterProfileKey := client.ObjectKeyFromObject(cp)
				gomega.Eventually(func(g gomega.Gomega) {
					createdClusterProfile := &inventoryv1alpha1.ClusterProfile{}
					g.Expect(k8sManagerClient.Get(ctx, clusterProfileKey, createdClusterProfile)).To(gomega.Succeed())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
			ginkgo.By("triggering MultiKueueCluster reconciliation", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdCluster := &kueue.MultiKueueCluster{}
					g.Expect(k8sManagerClient.Get(ctx, worker3MkClusterKey, createdCluster)).To(gomega.Succeed())
					createdCluster.Spec.ClusterSource.ClusterProfileRef = &kueue.ClusterProfileReference{Name: "clusterprofile3"}
					g.Expect(k8sManagerClient.Update(ctx, createdCluster)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
			worker3MkClusterKey = client.ObjectKeyFromObject(workerCluster3)
			ginkgo.By("checking MultiKueueCluster status again", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdCluster := &kueue.MultiKueueCluster{}
					g.Expect(k8sManagerClient.Get(ctx, worker3MkClusterKey, createdCluster)).To(gomega.Succeed())
					g.Expect(createdCluster.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(
						metav1.Condition{
							Type:    kueue.MultiKueueClusterActive,
							Status:  metav1.ConditionFalse,
							Reason:  "BadClusterProfile",
							Message: "load client config failed: no credentials provider configured",
						},
						util.IgnoreConditionTimestampsAndObservedGeneration)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("Connection via ClusterProfile with plugins", ginkgo.Ordered, func() {
		const (
			secretReaderPath    = "/plugins/secretreader-plugin"
			volumeName          = "plugins"
			volumeMountPath     = "/plugins"
			pluginContainerName = "secretreader-plugin-init"
		)
		var (
			defaultManagerKueueCfg  *kueueconfig.Configuration
			secretReaderRoleBinding *rbacv1.RoleBinding
			secretReaderRole        *rbacv1.Role

			defaultManagerDeployment = &appsv1.Deployment{}
			clusterProfileSecrets    = make([]*corev1.Secret, 0)
			clusterProfiles          = make([]*inventoryv1alpha1.ClusterProfile, 0)
			deploymentKey            = types.NamespacedName{Namespace: kueueNS, Name: "kueue-controller-manager"}
		)

		ginkgo.BeforeAll(func() {
			ginkgo.By("Creating Role and RoleBinding for secretreader-plugin", func() {
				secretReaderRole = utiltesting.MakeRole("secretreader", kueueNS).
					Rule([]string{""}, []string{"secrets"}, []string{"get", "list", "watch"}).
					Obj()
				util.MustCreate(ctx, k8sManagerClient, secretReaderRole)

				secretReaderRoleBinding = utiltesting.MakeRoleBinding("secretreader-binding", kueueNS).
					Subject(rbacv1.ServiceAccountKind, "kueue-controller-manager", kueueNS).
					RoleRef(rbacv1.GroupName, "ClusterRole", secretReaderRole.Name).
					Obj()
				util.MustCreate(ctx, k8sManagerClient, secretReaderRoleBinding)
			})

			ginkgo.By("Update 'kueue-controller-manager' deployment to have the secretreader-plugin binary", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, deploymentKey, defaultManagerDeployment)).To(gomega.Succeed())
					updatedDeployment := defaultManagerDeployment.DeepCopy()

					for i, container := range updatedDeployment.Spec.Template.Spec.Containers {
						if container.Name == "manager" {
							updatedDeployment.Spec.Template.Spec.Containers[i].VolumeMounts = append(
								updatedDeployment.Spec.Template.Spec.Containers[i].VolumeMounts,
								corev1.VolumeMount{
									Name:      volumeName,
									MountPath: volumeMountPath,
								},
							)
							break
						}
					}

					kubeVer := util.GetKubernetesVersion(managerCfg)
					if version.CompareKubeAwareVersionStrings(kubeVer, versionutil.MustParseGeneric("v1.35.0").String()) >= 0 {
						updatedDeployment.Spec.Template.Spec.Volumes = append(
							updatedDeployment.Spec.Template.Spec.Volumes,
							corev1.Volume{
								Name: volumeName,
								VolumeSource: corev1.VolumeSource{
									EmptyDir: &corev1.EmptyDirVolumeSource{},
									Image: &corev1.ImageVolumeSource{
										Reference:  util.GetClusterProfilePluginImage(),
										PullPolicy: corev1.PullIfNotPresent,
									},
								},
							},
						)
					} else {
						updatedDeployment.Spec.Template.Spec.InitContainers = []corev1.Container{
							*utiltesting.MakeContainer().
								Name(pluginContainerName).
								Image(util.GetClusterProfilePluginImage()).
								ImagePullPolicy(corev1.PullIfNotPresent).
								Command("cp", "/secretreader-plugin", secretReaderPath).
								VolumeMount(volumeName, volumeMountPath).
								Obj(),
						}
						updatedDeployment.Spec.Template.Spec.Volumes = append(
							updatedDeployment.Spec.Template.Spec.Volumes,
							corev1.Volume{
								Name: volumeName,
								VolumeSource: corev1.VolumeSource{
									EmptyDir: &corev1.EmptyDirVolumeSource{},
								},
							},
						)
					}
					g.Expect(k8sManagerClient.Update(ctx, updatedDeployment)).Should(gomega.Succeed())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				// We will wait for Kueue after setting the configuration
			})

			ginkgo.By("Updating MultiKueue configuration with CredentialsProviders", func() {
				defaultManagerKueueCfg = util.GetKueueConfiguration(ctx, k8sManagerClient)
				util.UpdateKueueConfigurationAndRestart(ctx, k8sManagerClient, defaultManagerKueueCfg, managerClusterName, func(cfg *kueueconfig.Configuration) {
					cfg.FeatureGates[string(features.MultiKueueClusterProfile)] = true
					if cfg.MultiKueue == nil {
						cfg.MultiKueue = &kueueconfig.MultiKueue{}
					}
					cfg.MultiKueue.ClusterProfile = &kueueconfig.ClusterProfile{
						CredentialsProviders: []kueueconfig.ClusterProfileCredentialsProvider{
							{
								Name: "secretreader",
								ExecConfig: api.ExecConfig{
									APIVersion:         "client.authentication.k8s.io/v1",
									Command:            secretReaderPath,
									ProvideClusterInfo: true,
									InteractiveMode:    api.NeverExecInteractiveMode,
								},
							},
						},
					}
				})
			})
		})
		ginkgo.AfterAll(func() {
			ginkgo.By("setting back the configuration", func() {
				// Just update Kueue configuration. We will restart Kueue later.
				util.UpdateKueueConfiguration(ctx, k8sManagerClient, defaultManagerKueueCfg)
			})

			ginkgo.By("setting back the deployment", func() {
				util.UpdateDeploymentAndWaitForProgressing(ctx, k8sManagerClient, deploymentKey, managerClusterName, func(deployment *appsv1.Deployment) {
					deployment.Spec = *defaultManagerDeployment.Spec.DeepCopy()
				})
			})

			ginkgo.By("wait for Kueue availability", func() {
				// We are using NoRestartCountCheck because we expect one fails in MultiKueue tests.
				// This happens on "The connection to a worker cluster is unreliable" test case.
				util.WaitForKueueAvailabilityNoRestartCountCheck(ctx, k8sManagerClient)
			})

			for _, s := range clusterProfileSecrets {
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, s, true, util.Timeout)
			}

			for _, c := range clusterProfiles {
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, c, true, util.Timeout)
			}

			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, secretReaderRoleBinding, true, util.LongTimeout)
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, secretReaderRole, true, util.LongTimeout)
		})

		ginkgo.It("Should be able to use ClusterProfile as way to connect worker cluster", func() {
			ginkgo.By("creating secrets with tokens to read from", func() {
				worker1AuthInfo := util.GetAuthInfoFromKubeConfig(worker1KConfig)
				worker2AuthInfo := util.GetAuthInfoFromKubeConfig(worker2KConfig)

				secretsData := map[string]string{
					"multikueue1-cp": worker1AuthInfo.Token,
					"multikueue2-cp": worker2AuthInfo.Token,
				}
				for name, token := range secretsData {
					secret := utiltesting.MakeSecret(name, kueueNS).Data("token", []byte(token)).Obj()
					util.MustCreate(ctx, k8sManagerClient, secret)
					clusterProfileSecrets = append(clusterProfileSecrets, secret)
				}
			})

			mkc := []*kueue.MultiKueueCluster{workerCluster1, workerCluster2}
			ginkgo.By("Update existing worker clusters to use ClusterProfiles", func() {
				for _, wc := range mkc {
					gomega.Eventually(func(g gomega.Gomega) {
						createdCluster := &kueue.MultiKueueCluster{}
						g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(wc), createdCluster)).To(gomega.Succeed())
						createdCluster.Spec.ClusterSource.KubeConfig = nil
						createdCluster.Spec.ClusterSource.ClusterProfileRef = &kueue.ClusterProfileReference{Name: wc.Name}
						g.Expect(k8sManagerClient.Update(ctx, createdCluster)).To(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				}
			})
			ginkgo.By("Create ClusterProfiles for existing worker clusters", func() {
				for _, wc := range mkc {
					c := utiltestingapi.MakeClusterProfile(wc.Name, kueueNS).ClusterManager("secretreader").Obj()
					util.MustCreate(ctx, k8sManagerClient, c)
					clusterProfiles = append(clusterProfiles, c)
				}
			})
			ginkgo.By("Update ClusterProfiles with AccessProviders", func() {
				secrets := []string{"multikueue1-cp", "multikueue2-cp"}
				workerClusterNames := []string{worker1ClusterName, worker2ClusterName}
				workerCAData := [][]byte{worker1Cfg.CAData, worker2Cfg.CAData}
				for i, wc := range mkc {
					gomega.Eventually(func(g gomega.Gomega) {
						createdCp := &inventoryv1alpha1.ClusterProfile{}
						g.Expect(k8sManagerClient.Get(ctx, client.ObjectKey{Namespace: kueueNS, Name: wc.Name}, createdCp)).To(gomega.Succeed())
						createdCp.Status.AccessProviders = []inventoryv1alpha1.AccessProvider{
							{
								Name: "secretreader",
								Cluster: apiv1.Cluster{
									Server:                   util.GetClusterServerAddress(workerClusterNames[i]),
									CertificateAuthorityData: workerCAData[i],
									Extensions: []apiv1.NamedExtension{
										{
											Name: "client.authentication.k8s.io/exec",
											Extension: runtime.RawExtension{
												Raw: fmt.Appendf(nil, `{"clusterName":"%s"}`, secrets[i]),
											},
										},
									},
								},
							},
						}
						g.Expect(k8sManagerClient.Status().Update(ctx, createdCp)).To(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				}
			})

			ginkgo.By("Check MultiKueueCluster status", func() {
				for _, wc := range mkc {
					gomega.Eventually(func(g gomega.Gomega) {
						createdCluster := &kueue.MultiKueueCluster{}
						g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(wc), createdCluster)).To(gomega.Succeed())
						g.Expect(createdCluster.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(
							metav1.Condition{
								Type:    kueue.MultiKueueClusterActive,
								Status:  metav1.ConditionTrue,
								Reason:  "Active",
								Message: "Connected",
							},
							util.IgnoreConditionTimestampsAndObservedGeneration)))
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				}
			})

			ginkgo.By("Check AdmissionChecks are active after switching to ClusterProfiles", func() {
				util.ExpectAdmissionChecksToBeActive(ctx, k8sManagerClient, multiKueueAc)
			})
		})
	})
})

func waitForJobAdmitted(wlLookupKey types.NamespacedName, acName, workerName string) {
	ginkgo.By(fmt.Sprintf("Waiting to be admitted in %s and manager", workerName))
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		createdWorkload := &kueue.Workload{}
		g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
		g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeComparableTo(&metav1.Condition{
			Type:    kueue.WorkloadAdmitted,
			Status:  metav1.ConditionTrue,
			Reason:  "Admitted",
			Message: "The workload is admitted",
		}, util.IgnoreConditionTimestampsAndObservedGeneration))
		g.Expect(admissioncheck.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(acName))).To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
			Name:    kueue.AdmissionCheckReference(acName),
			State:   kueue.CheckStateReady,
			Message: fmt.Sprintf(`The workload got reservation on "%s"`, workerName),
		}, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime")))
	}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
}

func checkFinishStatusCondition(g gomega.Gomega, wlLookupKey types.NamespacedName, finishReasonMessage string) {
	createdWorkload := &kueue.Workload{}
	g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
	g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeComparableTo(&metav1.Condition{
		Type:    kueue.WorkloadFinished,
		Status:  metav1.ConditionTrue,
		Reason:  kueue.WorkloadFinishedReasonSucceeded,
		Message: finishReasonMessage,
	}, util.IgnoreConditionTimestampsAndObservedGeneration))
}

func ensurePodWorkloadsRunning(deployment *appsv1.Deployment, managerNs corev1.Namespace, multiKueueAc *kueue.AdmissionCheck, kubernetesClients map[string]client.Client) {
	// Given the unpredictable nature of where the deployment pods run this function gathers the workload of a Pod first
	// it then gets the Pod's assigned cluster from the admission check message and uses the appropriate client to ensure the Pod is running
	pods := &corev1.PodList{}
	gomega.Expect(k8sManagerClient.List(ctx, pods, client.InNamespace(managerNs.Namespace),
		client.MatchingLabels(deployment.Spec.Selector.MatchLabels))).To(gomega.Succeed())

	for _, pod := range pods.Items { // We want to test that all deployment pods have workloads.
		createdLeaderWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadpod.GetWorkloadNameForPod(pod.Name, pod.UID), Namespace: managerNs.Name}
		gomega.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdLeaderWorkload)).To(gomega.Succeed())

		// By checking the assigned cluster we can discern which client to use
		admissionCheckMessage := admissioncheck.FindAdmissionCheck(createdLeaderWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAc.Name)).Message
		workerCluster := kubernetesClients[GetMultiKueueClusterNameFromAdmissionCheckMessage(admissionCheckMessage)]

		// Worker pods should be in "Running" phase
		gomega.Eventually(func(g gomega.Gomega) {
			createdPod := &corev1.Pod{}
			g.Expect(workerCluster.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: pod.Name}, createdPod)).To(gomega.Succeed())
			g.Expect(createdPod.Status.Phase).Should(gomega.Equal(corev1.PodRunning))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
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

type objAsPtr[T any] interface {
	client.Object
	*T
}

func expectObjectToBeDeletedOnWorkerClusters[PtrT objAsPtr[T], T any](ctx context.Context, obj PtrT) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		util.ExpectObjectToBeDeleted(ctx, k8sWorker1Client, obj, false)
		util.ExpectObjectToBeDeleted(ctx, k8sWorker2Client, obj, false)
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
}

func expectJobToBeCreatedAndManagedBy(ctx context.Context, c client.Client, job *batchv1.Job, managedBy string) {
	ginkgo.GinkgoHelper()
	createdJob := &batchv1.Job{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
		g.Expect(ptr.Deref(createdJob.Spec.ManagedBy, "")).To(gomega.Equal(managedBy))
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
}
