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

	"github.com/google/go-cmp/cmp/cmpopts"
	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueueconfig "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	workloadaw "sigs.k8s.io/kueue/pkg/controller/jobs/appwrapper"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	workloadjobset "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	workloadpytorchjob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/pytorchjob"
	workloadmpijob "sigs.k8s.io/kueue/pkg/controller/jobs/mpijob"
	workloadpod "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	workloadraycluster "sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	workloadrayjob "sigs.k8s.io/kueue/pkg/controller/jobs/rayjob"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingaw "sigs.k8s.io/kueue/pkg/util/testingjobs/appwrapper"
	testingdeployment "sigs.k8s.io/kueue/pkg/util/testingjobs/deployment"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	testingmpijob "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	testingpytorchjob "sigs.k8s.io/kueue/pkg/util/testingjobs/pytorchjob"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	testingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
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
		managerFlavor    *kueue.ResourceFlavor
		managerCq        *kueue.ClusterQueue
		managerLq        *kueue.LocalQueue

		worker1Flavor *kueue.ResourceFlavor
		worker1Cq     *kueue.ClusterQueue
		worker1Lq     *kueue.LocalQueue

		worker2Flavor *kueue.ResourceFlavor
		worker2Cq     *kueue.ClusterQueue
		worker2Lq     *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		managerNs = util.CreateNamespaceFromPrefixWithLog(ctx, k8sManagerClient, "multikueue-")
		worker1Ns = util.CreateNamespaceWithLog(ctx, k8sWorker1Client, managerNs.Name)
		worker2Ns = util.CreateNamespaceWithLog(ctx, k8sWorker2Client, managerNs.Name)

		workerCluster1 = utiltesting.MakeMultiKueueCluster("worker1").KubeConfig(kueue.SecretLocationType, "multikueue1").Obj()
		util.MustCreate(ctx, k8sManagerClient, workerCluster1)

		workerCluster2 = utiltesting.MakeMultiKueueCluster("worker2").KubeConfig(kueue.SecretLocationType, "multikueue2").Obj()
		util.MustCreate(ctx, k8sManagerClient, workerCluster2)

		multiKueueConfig = utiltesting.MakeMultiKueueConfig("multikueueconfig").Clusters("worker1", "worker2").Obj()
		util.MustCreate(ctx, k8sManagerClient, multiKueueConfig)

		multiKueueAc = utiltesting.MakeAdmissionCheck("ac1").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", multiKueueConfig.Name).
			Obj()
		util.MustCreate(ctx, k8sManagerClient, multiKueueAc)

		ginkgo.By("wait for check active", func() {
			updatedAc := kueue.AdmissionCheck{}
			acKey := client.ObjectKeyFromObject(multiKueueAc)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sManagerClient.Get(ctx, acKey, &updatedAc)).To(gomega.Succeed())
				g.Expect(updatedAc.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.AdmissionCheckActive))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
		managerFlavor = utiltesting.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sManagerClient, managerFlavor)

		managerCq = utiltesting.MakeClusterQueue("q1").
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas(managerFlavor.Name).
					Resource(corev1.ResourceCPU, "2").
					Resource(corev1.ResourceMemory, "2G").
					Obj(),
			).
			AdmissionChecks(kueue.AdmissionCheckReference(multiKueueAc.Name)).
			Obj()
		util.MustCreate(ctx, k8sManagerClient, managerCq)

		managerLq = utiltesting.MakeLocalQueue(managerCq.Name, managerNs.Name).ClusterQueue(managerCq.Name).Obj()
		util.MustCreate(ctx, k8sManagerClient, managerLq)

		worker1Flavor = utiltesting.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sWorker1Client, worker1Flavor)

		worker1Cq = utiltesting.MakeClusterQueue("q1").
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas(worker1Flavor.Name).
					Resource(corev1.ResourceCPU, "2").
					Resource(corev1.ResourceMemory, "1G").
					Obj(),
			).
			Obj()
		util.MustCreate(ctx, k8sWorker1Client, worker1Cq)

		worker1Lq = utiltesting.MakeLocalQueue(worker1Cq.Name, worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
		util.MustCreate(ctx, k8sWorker1Client, worker1Lq)

		worker2Flavor = utiltesting.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sWorker2Client, worker2Flavor)

		worker2Cq = utiltesting.MakeClusterQueue("q1").
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas(worker2Flavor.Name).
					Resource(corev1.ResourceCPU, "1").
					Resource(corev1.ResourceMemory, "2G").
					Obj(),
			).
			Obj()
		util.MustCreate(ctx, k8sWorker2Client, worker2Cq)

		worker2Lq = utiltesting.MakeLocalQueue(worker2Cq.Name, worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
		util.MustCreate(ctx, k8sWorker2Client, worker2Lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sManagerClient, managerNs)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sWorker1Client, worker1Ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sWorker2Client, worker2Ns)).To(gomega.Succeed())

		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1Cq, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1Flavor, true, util.LongTimeout)

		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, worker2Cq, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, worker2Flavor, true, util.LongTimeout)

		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerCq, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerFlavor, true, util.LongTimeout)
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
					g.Expect(workload.FindAdmissionCheck(createdLeaderWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAc.Name))).To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
						Name:    kueue.AdmissionCheckReference(multiKueueAc.Name),
						State:   kueue.CheckStatePending,
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
					Reason: finishReason},
				{
					Type:   corev1.PodReady,
					Status: corev1.ConditionFalse,
					Reason: finishReason,
				},
				{
					Type:   corev1.ContainersReady,
					Status: corev1.ConditionFalse,
					Reason: finishReason},
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
					g.Expect(createdPod.Status.Conditions).To(gomega.BeComparableTo(finishPodConditions, cmpopts.IgnoreFields(corev1.PodCondition{}, "LastTransitionTime")))
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
					g.Expect(workload.FindAdmissionCheck(createdLeaderWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAc.Name))).To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
						Name:    kueue.AdmissionCheckReference(multiKueueAc.Name),
						State:   kueue.CheckStatePending,
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
					g.Expect(workload.FindAdmissionCheck(createdLeaderWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAc.Name))).To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
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
					},
					testingpytorchjob.PyTorchReplicaSpecRequirement{
						ReplicaType:   kftraining.PyTorchJobReplicaTypeWorker,
						ReplicaCount:  1,
						RestartPolicy: "OnFailure",
					},
				).
				RequestAndLimit(kftraining.PyTorchJobReplicaTypeMaster, corev1.ResourceCPU, "0.2").
				RequestAndLimit(kftraining.PyTorchJobReplicaTypeMaster, corev1.ResourceMemory, "800M").
				RequestAndLimit(kftraining.PyTorchJobReplicaTypeWorker, corev1.ResourceCPU, "0.5").
				RequestAndLimit(kftraining.PyTorchJobReplicaTypeWorker, corev1.ResourceMemory, "800M").
				Image(kftraining.PyTorchJobReplicaTypeMaster, util.GetAgnHostImage(), util.BehaviorExitFast).
				Image(kftraining.PyTorchJobReplicaTypeWorker, util.GetAgnHostImage(), util.BehaviorExitFast).
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
					},
					testingmpijob.MPIJobReplicaSpecRequirement{
						ReplicaType:   kfmpi.MPIReplicaTypeWorker,
						ReplicaCount:  1,
						RestartPolicy: "OnFailure",
					},
				).
				RequestAndLimit(kfmpi.MPIReplicaTypeLauncher, corev1.ResourceCPU, "1").
				RequestAndLimit(kfmpi.MPIReplicaTypeLauncher, corev1.ResourceMemory, "200M").
				RequestAndLimit(kfmpi.MPIReplicaTypeWorker, corev1.ResourceCPU, "0.5").
				RequestAndLimit(kfmpi.MPIReplicaTypeWorker, corev1.ResourceMemory, "100M").
				Image(kfmpi.MPIReplicaTypeLauncher, util.GetAgnHostImage(), util.BehaviorExitFast).
				Image(kfmpi.MPIReplicaTypeWorker, util.GetAgnHostImage(), util.BehaviorExitFast).
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
	})

	ginkgo.When("Incremental mode", ginkgo.Ordered, func() {
		var defaultManagerKueueCfg *kueueconfig.Configuration

		ginkgo.BeforeAll(func() {
			ginkgo.By("setting MultiKueue Dispatcher to Incremental", func() {
				defaultManagerKueueCfg = util.GetKueueConfiguration(ctx, k8sManagerClient)
				newCfg := defaultManagerKueueCfg.DeepCopy()
				util.UpdateKueueConfiguration(ctx, k8sManagerClient, newCfg, managerClusterName, func(cfg *kueueconfig.Configuration) {
					if cfg.MultiKueue == nil {
						cfg.MultiKueue = &kueueconfig.MultiKueue{}
					}
					cfg.MultiKueue.DispatcherName = ptr.To(kueueconfig.MultiKueueDispatcherModeIncremental)
				})
			})
		})
		ginkgo.AfterAll(func() {
			ginkgo.By("setting MultiKueue Dispatcher back to AllAtOnce", func() {
				util.ApplyKueueConfiguration(ctx, k8sManagerClient, defaultManagerKueueCfg)
				util.RestartKueueController(ctx, k8sManagerClient, managerClusterName)
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
			worker1Cq2 := utiltesting.MakeClusterQueue("q2").
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas(worker1Flavor.Name).
						Resource(corev1.ResourceCPU, "2").
						Resource(corev1.ResourceMemory, "1G").
						Obj(),
				).
				Obj()
			util.MustCreate(ctx, k8sWorker1Client, worker1Cq2)

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

			ginkgo.By("Waiting for Kueue and kube-system pods to become active again", func() {
				util.WaitForKubeSystemControllersAvailability(ctx, k8sWorker1Client, worker1Container)
				util.WaitForKueueAvailabilityNoRestartCountCheck(ctx, k8sWorker1Client)
			})

			ginkgo.By("Checking that the Kueue is operational after reconnection", func() {
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1Cq2, true, util.StartUpTimeout)
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
		g.Expect(workload.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(acName))).To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
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
		admissionCheckMessage := workload.FindAdmissionCheck(createdLeaderWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAc.Name)).Message
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
