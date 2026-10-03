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
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("MultiKueue with DRA", ginkgo.Label("area:multikueue", "feature:multikueue", "feature:dra"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		managerNs *corev1.Namespace
		worker1Ns *corev1.Namespace
		worker2Ns *corev1.Namespace

		managerMultiKueueSecret1 *corev1.Secret
		managerMultiKueueSecret2 *corev1.Secret
		workerCluster1           *kueue.MultiKueueCluster
		workerCluster2           *kueue.MultiKueueCluster
		managerMultiKueueConfig  *kueue.MultiKueueConfig
		multiKueueAC             *kueue.AdmissionCheck
		managerCq                *kueue.ClusterQueue
		managerLq                *kueue.LocalQueue
		managerFlavor            *kueue.ResourceFlavor

		worker1Cq *kueue.ClusterQueue
		worker1Lq *kueue.LocalQueue

		worker2Cq *kueue.ClusterQueue
		worker2Lq *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
			managerAndMultiKueueSetup(ctx, mgr, 2*time.Second, defaultEnabledIntegrations, config.MultiKueueDispatcherModeAllAtOnce)
		})
	})

	ginkgo.AfterAll(func() {
		managerTestCluster.fwk.StopManager(managerTestCluster.ctx)
	})

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.KueueDRAIntegration, true)

		managerNs = behavioral.CreateNamespaceFromPrefixWithLog(managerTestCluster.ctx, managerTestCluster.client, "multikueue-dra-")
		worker1Ns = behavioral.CreateNamespaceWithLog(worker1TestCluster.ctx, worker1TestCluster.client, managerNs.Name)
		worker2Ns = behavioral.CreateNamespaceWithLog(worker2TestCluster.ctx, worker2TestCluster.client, managerNs.Name)

		w1Kubeconfig, err := worker1TestCluster.kubeConfigBytes()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		w2Kubeconfig, err := worker2TestCluster.kubeConfigBytes()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		managerMultiKueueSecret1 = utiltesting.MakeSecret("multikueue1", managersConfigNamespace.Name).Data(kueue.MultiKueueConfigSecretKey, w1Kubeconfig).Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret1)

		managerMultiKueueSecret2 = utiltesting.MakeSecret("multikueue2", managersConfigNamespace.Name).Data(kueue.MultiKueueConfigSecretKey, w2Kubeconfig).Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret2)

		workerCluster1 = utiltestingapi.MakeMultiKueueCluster("worker1").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret1.Name).Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, workerCluster1)

		workerCluster2 = utiltestingapi.MakeMultiKueueCluster("worker2").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret2.Name).Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, workerCluster2)

		managerMultiKueueConfig = utiltestingapi.MakeMultiKueueConfig("multikueueconfig").Clusters(workerCluster1.Name, workerCluster2.Name).Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueConfig)

		multiKueueAC = utiltestingapi.MakeAdmissionCheck("ac1").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", managerMultiKueueConfig.Name).
			Obj()
		behavioral.CreateAdmissionChecksAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, multiKueueAC)

		managerFlavor = utiltestingapi.MakeResourceFlavor(string(multikueueTestFlavor)).Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerFlavor)

		managerCq = utiltestingapi.MakeClusterQueue("dra-cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(string(multikueueTestFlavor)).Resource(corev1.ResourceCPU, "5").Obj()).
			AdmissionChecks(kueue.AdmissionCheckReference(multiKueueAC.Name)).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, managerCq)

		managerLq = utiltestingapi.MakeLocalQueue(managerCq.Name, managerNs.Name).ClusterQueue(managerCq.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, managerLq)

		worker1Cq = utiltestingapi.MakeClusterQueue("dra-cq").Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq)

		worker1Lq = utiltestingapi.MakeLocalQueue(worker1Cq.Name, worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Lq)

		worker2Cq = utiltestingapi.MakeClusterQueue("dra-cq").Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2Cq)

		worker2Lq = utiltestingapi.MakeLocalQueue(worker2Cq.Name, worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2Lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(managerTestCluster.ctx, managerTestCluster.client, managerNs)).To(gomega.Succeed())
		gomega.Expect(behavioral.DeleteNamespace(worker1TestCluster.ctx, worker1TestCluster.client, worker1Ns)).To(gomega.Succeed())
		gomega.Expect(behavioral.DeleteNamespace(worker2TestCluster.ctx, worker2TestCluster.client, worker2Ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerCq, true)
		behavioral.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq, true)
		behavioral.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2Cq, true)
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerFlavor, true)
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, multiKueueAC, true)
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueConfig, true)
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, workerCluster1, true)
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, workerCluster2, true)
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret1, true)
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret2, true)
	})

	ginkgo.When("Jobs have DRA resources", func() {
		ginkgo.It("Should sync job with DRA resources to worker clusters", func() {
			ginkgo.By("creating a ResourceClaimTemplate on manager and workers", func() {
				managerRct := utiltesting.MakeResourceClaimTemplate("gpu-template", managerNs.Name).
					DeviceRequest("gpu-request", "gpu.example.com", 1).
					Obj()
				behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerRct)

				worker1Rct := utiltesting.MakeResourceClaimTemplate("gpu-template", worker1Ns.Name).
					DeviceRequest("gpu-request", "gpu.example.com", 1).
					Obj()
				behavioral.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, worker1Rct)

				worker2Rct := utiltesting.MakeResourceClaimTemplate("gpu-template", worker2Ns.Name).
					DeviceRequest("gpu-request", "gpu.example.com", 1).
					Obj()
				behavioral.MustCreate(worker2TestCluster.ctx, worker2TestCluster.client, worker2Rct)
			})

			job := testingjob.MakeJob("dra-job", managerNs.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				Obj()
			job.Spec.Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{
					Name:                      "gpu",
					ResourceClaimTemplateName: new("gpu-template"),
				},
			}

			ginkgo.By("creating a job with DRA resources on manager", func() {
				behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)
			})

			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

			ginkgo.By("setting workload reservation in the management cluster", func() {
				admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).Obj()
				behavioral.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission)
			})

			ginkgo.By("checking the workload creation in the worker clusters", func() {
				managerWl := &kueue.Workload{}
				gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
					g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("setting workload reservation in worker1", func() {
				admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).Obj()
				behavioral.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlLookupKey, admission)
			})

			ginkgo.By("verifying AC state is updated in manager and worker2 wl is removed", func() {
				behavioral.ExpectAdmissionCheckStateWithMessage(
					managerTestCluster.ctx, managerTestCluster.client, wlLookupKey,
					multiKueueAC.Name,
					kueue.CheckStateReady,
					`The workload was admitted on "worker1"`,
				)

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("finishing the worker job", func() {
				reachedPodsReason := "Reached expected number of succeeded pods"
				finishJobReason := "Job finished successfully"
				now := metav1.Now()

				gomega.Eventually(func(g gomega.Gomega) {
					createdJob := batchv1.Job{}
					g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
					createdJob.Status.Conditions = append(createdJob.Status.Conditions,
						batchv1.JobCondition{
							Type:               batchv1.JobSuccessCriteriaMet,
							Status:             corev1.ConditionTrue,
							LastProbeTime:      now,
							LastTransitionTime: now,
							Message:            reachedPodsReason,
						},
						batchv1.JobCondition{
							Type:               batchv1.JobComplete,
							Status:             corev1.ConditionTrue,
							LastProbeTime:      now,
							LastTransitionTime: now,
							Message:            finishJobReason,
						})
					createdJob.Status.Succeeded = 1
					createdJob.Status.StartTime = new(now)
					createdJob.Status.CompletionTime = new(now)
					g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdJob)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(kueue.WorkloadFinished, kueue.WorkloadFinishedReasonSucceeded))
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should not admit job when ResourceClaimTemplate is missing on all workers", func() {
			ginkgo.By("creating ResourceClaimTemplate only on manager (NOT on workers)", func() {
				managerRct := utiltesting.MakeResourceClaimTemplate("missing-rct", managerNs.Name).
					DeviceRequest("gpu-request", "gpu.example.com", 1).
					Obj()
				behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerRct)
			})

			job := testingjob.MakeJob("missing-rct-job", managerNs.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				Obj()
			job.Spec.Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{
					Name:                      "gpu",
					ResourceClaimTemplateName: new("missing-rct"),
				},
			}

			ginkgo.By("creating a job with DRA resources on manager", func() {
				behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)
			})

			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}
			createdWorkload := &kueue.Workload{}

			ginkgo.By("setting workload reservation in the management cluster", func() {
				admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).Obj()
				behavioral.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission)
			})

			ginkgo.By("checking the workload creation in the worker clusters", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying workload is not assigned to any worker (missing RCT on workers)", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Status.ClusterName).To(gomega.BeNil())
				}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
			})
		})
	})
})
