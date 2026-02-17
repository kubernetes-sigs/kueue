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

package mkdra

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	draconsts "sigs.k8s.io/dra-example-driver/pkg/consts"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

type objAsPtr[T any] interface {
	client.Object
	*T
}

func expectObjectToBeDeletedOnWorkerClusters[PtrT objAsPtr[T], T any](ctx context.Context, obj PtrT) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		util.ExpectObjectToBeDeleted(ctx, k8sWorker1Client, obj, false)
		util.ExpectObjectToBeDeleted(ctx, k8sWorker2Client, obj, false)
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
}

var _ = ginkgo.Describe("MultiKueue with DRA", ginkgo.Label("feature:dra", "area:multikueue", "feature:multikueue"), ginkgo.Ordered, func() {
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
	)

	ginkgo.BeforeEach(func() {
		managerNs = util.CreateNamespaceFromPrefixWithLog(ctx, k8sManagerClient, "multikueue-dra-")
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

		managerFlavor = utiltestingapi.MakeResourceFlavor("dra-flavor").Obj()
		util.MustCreate(ctx, k8sManagerClient, managerFlavor)

		managerCq = utiltestingapi.MakeClusterQueue("dra-cq").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(managerFlavor.Name).
					Resource(corev1.ResourceCPU, "8").
					Resource("gpu", "4").
					Obj(),
			).
			AdmissionChecks(kueue.AdmissionCheckReference(multiKueueAc.Name)).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sManagerClient, managerCq)

		managerLq = utiltestingapi.MakeLocalQueue(managerCq.Name, managerNs.Name).ClusterQueue(managerCq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sManagerClient, managerLq)

		worker1Flavor = utiltestingapi.MakeResourceFlavor("dra-flavor").Obj()
		util.MustCreate(ctx, k8sWorker1Client, worker1Flavor)

		worker1Cq = utiltestingapi.MakeClusterQueue("dra-cq").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(worker1Flavor.Name).
					Resource(corev1.ResourceCPU, "8").
					Resource("gpu", "4").
					Obj(),
			).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sWorker1Client, worker1Cq)

		worker1Lq = utiltestingapi.MakeLocalQueue(worker1Cq.Name, worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sWorker1Client, worker1Lq)

		worker2Flavor = utiltestingapi.MakeResourceFlavor("dra-flavor").Obj()
		util.MustCreate(ctx, k8sWorker2Client, worker2Flavor)

		worker2Cq = utiltestingapi.MakeClusterQueue("dra-cq").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(worker2Flavor.Name).
					Resource(corev1.ResourceCPU, "8").
					Resource("gpu", "4").
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

	ginkgo.When("Creating Jobs with ResourceClaimTemplates", func() {
		ginkgo.It("Should admit and run a DRA job across MultiKueue clusters", func() {
			// Use different device counts per cluster to verify which cluster's RCT is used
			// Manager: 1, Worker1: 2, Worker2: 3
			ginkgo.By("Creating ResourceClaimTemplate on manager and both workers with different device counts")
			managerRct := utiltesting.MakeResourceClaimTemplate("gpu-template", managerNs.Name).
				DeviceRequest("gpu-request", draconsts.DriverName, 1).
				Obj()
			util.MustCreate(ctx, k8sManagerClient, managerRct)

			worker1Rct := utiltesting.MakeResourceClaimTemplate("gpu-template", worker1Ns.Name).
				DeviceRequest("gpu-request", draconsts.DriverName, 2).
				Obj()
			util.MustCreate(ctx, k8sWorker1Client, worker1Rct)

			worker2Rct := utiltesting.MakeResourceClaimTemplate("gpu-template", worker2Ns.Name).
				DeviceRequest("gpu-request", draconsts.DriverName, 3).
				Obj()
			util.MustCreate(ctx, k8sWorker2Client, worker2Rct)

			ginkgo.By("Creating Job with ResourceClaimTemplate reference on manager")
			job := testingjob.MakeJob("dra-job", managerNs.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				Request(corev1.ResourceCPU, "100m").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				ResourceClaimTemplate("gpu", "gpu-template").
				Obj()
			util.MustCreate(ctx, k8sManagerClient, job)

			ginkgo.By("Waiting for job to be managed by MultiKueue")
			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := &batchv1.Job{}
				g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
				g.Expect(ptr.Deref(createdJob.Spec.ManagedBy, "")).To(gomega.BeEquivalentTo(kueue.MultiKueueControllerName))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			wlLookupKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				Namespace: managerNs.Name,
			}

			var assignedWorkerCluster client.Client
			var assignedClusterName string
			var expectedDeviceCount int64

			ginkgo.By("Checking which worker cluster was assigned")
			gomega.Eventually(func(g gomega.Gomega) {
				managerWl := &kueue.Workload{}
				g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
				g.Expect(managerWl.Status.ClusterName).NotTo(gomega.BeNil())

				assignedClusterName = *managerWl.Status.ClusterName
				g.Expect(assignedClusterName).To(gomega.BeElementOf(workerCluster1.Name, workerCluster2.Name))
				if assignedClusterName == workerCluster1.Name {
					assignedWorkerCluster = k8sWorker1Client
					expectedDeviceCount = 2
				} else {
					assignedWorkerCluster = k8sWorker2Client
					expectedDeviceCount = 3
				}

				g.Expect(admissioncheck.FindAdmissionCheck(managerWl.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAc.Name))).To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
					Name:    kueue.AdmissionCheckReference(multiKueueAc.Name),
					State:   kueue.CheckStateReady,
					Message: fmt.Sprintf(`The workload got reservation on "%s"`, assignedClusterName),
				}, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By(fmt.Sprintf("Verifying workload admitted on %s has correct DRA resource usage (expecting %d devices)", assignedClusterName, expectedDeviceCount))
			workerWlLookupKey := types.NamespacedName{Name: wlLookupKey.Name, Namespace: managerNs.Name}
			workerWl := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(assignedWorkerCluster.Get(ctx, workerWlLookupKey, workerWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(workerWl)).To(gomega.BeTrue())
				g.Expect(workerWl.Status.Admission).NotTo(gomega.BeNil())
				g.Expect(workerWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))

				assignment := workerWl.Status.Admission.PodSetAssignments[0]
				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName("gpu")))
				// Verify the worker's RCT device count is used (not the manager's).
				actualGPU := assignment.ResourceUsage["gpu"]
				g.Expect(actualGPU.Value()).To(gomega.Equal(expectedDeviceCount))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Finishing the job's pods")
			listOpts := util.GetListOptsFromLabel(fmt.Sprintf("batch.kubernetes.io/job-name=%s", job.Name))
			if assignedClusterName == workerCluster1.Name {
				util.WaitForActivePodsAndTerminate(ctx, k8sWorker1Client, worker1RestClient, worker1Cfg, job.Namespace, 1, 0, listOpts)
			} else {
				util.WaitForActivePodsAndTerminate(ctx, k8sWorker2Client, worker2RestClient, worker2Cfg, job.Namespace, 1, 0, listOpts)
			}

			ginkgo.By("Waiting for workload to finish")
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(kueue.WorkloadFinished, kueue.WorkloadFinishedReasonSucceeded))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Checking no objects are left in worker clusters and job is completed")
			expectObjectToBeDeletedOnWorkerClusters(ctx, createdWorkload)
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

		ginkgo.It("Should handle workload when ResourceClaimTemplate is missing on worker", func() {
			ginkgo.By("Creating ResourceClaimTemplate only on manager (NOT on workers)")
			managerRct := utiltesting.MakeResourceClaimTemplate("missing-rct", managerNs.Name).
				DeviceRequest("gpu-request", draconsts.DriverName, 1).
				Obj()
			util.MustCreate(ctx, k8sManagerClient, managerRct)

			ginkgo.By("Creating Job on manager")
			job := testingjob.MakeJob("missing-rct-job", managerNs.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				Request(corev1.ResourceCPU, "100m").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				ResourceClaimTemplate("gpu", "missing-rct").
				Obj()
			util.MustCreate(ctx, k8sManagerClient, job)

			wlLookupKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				Namespace: managerNs.Name,
			}

			ginkgo.By("Verifying workload is created on manager")
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Verifying workloads on workers don't get admitted (missing RCT)")
			gomega.Consistently(func(g gomega.Gomega) {
				managerWl := &kueue.Workload{}
				g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
				g.Expect(managerWl.Status.ClusterName).To(gomega.BeNil())
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("Workers have different DRA capabilities", func() {
		ginkgo.It("Should route DRA job to worker that has ResourceClaimTemplate", func() {
			ginkgo.By("Creating ResourceClaimTemplate only on manager and worker1 (NOT on worker2)")
			managerRct := utiltesting.MakeResourceClaimTemplate("worker1-only-rct", managerNs.Name).
				DeviceRequest("gpu-request", draconsts.DriverName, 1).
				Obj()
			util.MustCreate(ctx, k8sManagerClient, managerRct)

			worker1Rct := utiltesting.MakeResourceClaimTemplate("worker1-only-rct", worker1Ns.Name).
				DeviceRequest("gpu-request", draconsts.DriverName, 1).
				Obj()
			util.MustCreate(ctx, k8sWorker1Client, worker1Rct)

			// Intentionally NOT creating RCT on worker2

			ginkgo.By("Creating Job with ResourceClaimTemplate on manager")
			job := testingjob.MakeJob("heterogeneous-dra-job", managerNs.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				Request(corev1.ResourceCPU, "100m").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				ResourceClaimTemplate("gpu", "worker1-only-rct").
				Obj()
			util.MustCreate(ctx, k8sManagerClient, job)

			wlLookupKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				Namespace: managerNs.Name,
			}

			ginkgo.By("Verifying job is assigned to worker1 (the only worker with RCT)")
			gomega.Eventually(func(g gomega.Gomega) {
				managerWl := &kueue.Workload{}
				g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
				g.Expect(managerWl.Status.ClusterName).NotTo(gomega.BeNil())
				g.Expect(*managerWl.Status.ClusterName).To(gomega.Equal(workerCluster1.Name))

				g.Expect(admissioncheck.FindAdmissionCheck(managerWl.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAc.Name))).To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
					Name:    kueue.AdmissionCheckReference(multiKueueAc.Name),
					State:   kueue.CheckStateReady,
					Message: fmt.Sprintf(`The workload got reservation on "%s"`, workerCluster1.Name),
				}, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime")))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Verifying workload is admitted on worker1 with correct DRA resource usage")
			workerWl := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sWorker1Client.Get(ctx, wlLookupKey, workerWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(workerWl)).To(gomega.BeTrue())
				g.Expect(workerWl.Status.Admission).NotTo(gomega.BeNil())

				assignment := workerWl.Status.Admission.PodSetAssignments[0]
				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName("gpu")))
				g.Expect(assignment.ResourceUsage["gpu"]).To(gomega.Equal(resource.MustParse("1")))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Finishing the job's pods on worker1")
			listOpts := util.GetListOptsFromLabel(fmt.Sprintf("batch.kubernetes.io/job-name=%s", job.Name))
			util.WaitForActivePodsAndTerminate(ctx, k8sWorker1Client, worker1RestClient, worker1Cfg, job.Namespace, 1, 0, listOpts)

			ginkgo.By("Waiting for job to complete")
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(kueue.WorkloadFinished, kueue.WorkloadFinishedReasonSucceeded))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Verifying job completed successfully")
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

	ginkgo.When("Creating Jobs with multiple ResourceClaimTemplates", func() {
		ginkgo.It("Should correctly account for DRA resources from multiple pods", func() {
			ginkgo.By("Creating ResourceClaimTemplates on all clusters")
			// All clusters use the same namespace name (manager namespace name is used on workers)
			for _, c := range []client.Client{k8sManagerClient, k8sWorker1Client, k8sWorker2Client} {
				rct := utiltesting.MakeResourceClaimTemplate("multi-pod-gpu-template", managerNs.Name).
					DeviceRequest("gpu-request", draconsts.DriverName, 1).
					Obj()
				util.MustCreate(ctx, c, rct)
			}

			ginkgo.By("Creating Job with parallelism=2 on manager")
			job := testingjob.MakeJob("multi-pod-dra-job", managerNs.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				Parallelism(2).
				Completions(2).
				Request(corev1.ResourceCPU, "100m").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				ResourceClaimTemplate("gpu", "multi-pod-gpu-template").
				Obj()
			util.MustCreate(ctx, k8sManagerClient, job)

			wlLookupKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				Namespace: managerNs.Name,
			}

			var assignedWorkerCluster client.Client
			var assignedClusterName string

			ginkgo.By("Waiting for workload to be admitted")
			gomega.Eventually(func(g gomega.Gomega) {
				managerWl := &kueue.Workload{}
				g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
				g.Expect(managerWl.Status.ClusterName).NotTo(gomega.BeNil())

				assignedClusterName = *managerWl.Status.ClusterName
				if assignedClusterName == workerCluster1.Name {
					assignedWorkerCluster = k8sWorker1Client
				} else {
					assignedWorkerCluster = k8sWorker2Client
				}
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By(fmt.Sprintf("Verifying workload on %s has 2 GPU resource usage (1 per pod)", assignedClusterName))
			workerWl := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(assignedWorkerCluster.Get(ctx, wlLookupKey, workerWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(workerWl)).To(gomega.BeTrue())
				g.Expect(workerWl.Status.Admission).NotTo(gomega.BeNil())

				assignment := workerWl.Status.Admission.PodSetAssignments[0]
				g.Expect(assignment.Count).To(gomega.Equal(ptr.To(int32(2))))
				g.Expect(assignment.ResourceUsage["gpu"]).To(gomega.Equal(resource.MustParse("2")))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Finishing the job's pods")
			listOpts := util.GetListOptsFromLabel(fmt.Sprintf("batch.kubernetes.io/job-name=%s", job.Name))
			if assignedClusterName == workerCluster1.Name {
				util.WaitForActivePodsAndTerminate(ctx, k8sWorker1Client, worker1RestClient, worker1Cfg, job.Namespace, 2, 0, listOpts)
			} else {
				util.WaitForActivePodsAndTerminate(ctx, k8sWorker2Client, worker2RestClient, worker2Cfg, job.Namespace, 2, 0, listOpts)
			}

			ginkgo.By("Waiting for job to complete")
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(kueue.WorkloadFinished, kueue.WorkloadFinishedReasonSucceeded))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
