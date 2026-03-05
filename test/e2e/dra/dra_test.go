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

package dra

import (
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
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("DRA", func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-dra-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("Creating Jobs with DRA resources", func() {
		var (
			resourceFlavor *kueue.ResourceFlavor
			clusterQueue   *kueue.ClusterQueue
			localQueue     *kueue.LocalQueue
		)
		ginkgo.BeforeEach(func() {
			resourceFlavor = utiltestingapi.MakeResourceFlavor("dra-flavor-" + ns.Name).Obj()
			util.MustCreate(ctx, k8sClient, resourceFlavor)

			// ClusterQueue with DRA gpu resource quota (mapped from gpu.example.com DeviceClass)
			clusterQueue = utiltestingapi.MakeClusterQueue("dra-cq-" + ns.Name).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
						Resource(corev1.ResourceCPU, "4").
						Resource("gpu", "4").
						Obj()).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("dra-lq", ns.Name).
				ClusterQueue(clusterQueue.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
		})

		ginkgo.It("Should admit and run a job with DRA resource claim template", func() {
			ginkgo.By("Creating ResourceClaimTemplate referencing gpu.example.com DeviceClass")
			rct := utiltesting.MakeResourceClaimTemplate("gpu-template", ns.Name).
				DeviceRequest("gpu-request", draconsts.DriverName, 1).
				Obj()
			util.MustCreate(ctx, k8sClient, rct)

			ginkgo.By("Creating Job with ResourceClaimTemplate reference")
			job := testingjob.MakeJob("dra-job", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				ResourceClaimTemplate("gpu", "gpu-template").
				Obj()
			util.MustCreate(ctx, k8sClient, job)

			wlLookupKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				Namespace: ns.Name,
			}

			ginkgo.By("Verifying workload is created and has quota reservation with correct DRA resource usage")
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(createdWorkload)).To(gomega.BeTrue())
				g.Expect(createdWorkload.Status.Admission).NotTo(gomega.BeNil())
				g.Expect(createdWorkload.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))

				// Verify DRA resource usage is correctly recorded
				assignment := createdWorkload.Status.Admission.PodSetAssignments[0]
				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName("gpu")))
				g.Expect(assignment.ResourceUsage["gpu"]).To(gomega.Equal(resource.MustParse("1")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Verifying job is unsuspended")
			util.ExpectJobUnsuspended(ctx, k8sClient, client.ObjectKeyFromObject(job))

			ginkgo.By("Verifying job completes successfully")
			util.ExpectJobToBeCompleted(ctx, k8sClient, job)

			ginkgo.By("Verifying workload finished successfully")
			util.ExpectWorkloadToFinish(ctx, k8sClient, wlLookupKey)
		})

		ginkgo.It("Should keep job suspended when DRA quota is exceeded", func() {
			ginkgo.By("Creating ResourceClaimTemplate requesting more than available quota")
			rct := utiltesting.MakeResourceClaimTemplate("large-gpu-template", ns.Name).
				DeviceRequest("gpu-request", draconsts.DriverName, 10). // Exceeds quota of 4
				Obj()
			util.MustCreate(ctx, k8sClient, rct)

			ginkgo.By("Creating Job with large DRA request")
			job := testingjob.MakeJob("large-dra-job", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				ResourceClaimTemplate("gpu", "large-gpu-template").
				Obj()
			util.MustCreate(ctx, k8sClient, job)

			wlLookupKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				Namespace: ns.Name,
			}

			ginkgo.By("Waiting for workload to be created")
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Verifying job remains suspended")
			createdJob := &batchv1.Job{}
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
				g.Expect(createdJob.Spec.Suspend).To(gomega.Equal(ptr.To(true)))
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())

			ginkgo.By("Verifying workload does not get admitted")
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(createdWorkload)).To(gomega.BeFalse())
				g.Expect(workload.HasQuotaReservation(createdWorkload)).To(gomega.BeFalse())
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.It("Should admit multiple jobs that together fit within DRA quota", func() {
			ginkgo.By("Creating ResourceClaimTemplates for two jobs (2 GPUs each, total 4 = quota)")
			rct1 := utiltesting.MakeResourceClaimTemplate("gpu-template-1", ns.Name).
				DeviceRequest("gpu-request", draconsts.DriverName, 2).
				Obj()
			util.MustCreate(ctx, k8sClient, rct1)

			rct2 := utiltesting.MakeResourceClaimTemplate("gpu-template-2", ns.Name).
				DeviceRequest("gpu-request", draconsts.DriverName, 2).
				Obj()
			util.MustCreate(ctx, k8sClient, rct2)

			ginkgo.By("Creating first job requesting 2 GPUs")
			job1 := testingjob.MakeJob("dra-job-1", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				ResourceClaimTemplate("gpu", "gpu-template-1").
				Obj()
			util.MustCreate(ctx, k8sClient, job1)

			ginkgo.By("Creating second job requesting 2 GPUs")
			job2 := testingjob.MakeJob("dra-job-2", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				ResourceClaimTemplate("gpu", "gpu-template-2").
				Obj()
			util.MustCreate(ctx, k8sClient, job2)

			ginkgo.By("Verifying both workloads are admitted with correct DRA resource usage")
			for _, job := range []*batchv1.Job{job1, job2} {
				wlLookupKey := types.NamespacedName{
					Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
					Namespace: ns.Name,
				}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(workload.HasQuotaReservation(createdWorkload)).To(gomega.BeTrue())
					g.Expect(createdWorkload.Status.Admission).NotTo(gomega.BeNil())

					// Verify each workload has 2 GPUs reserved
					assignment := createdWorkload.Status.Admission.PodSetAssignments[0]
					g.Expect(assignment.ResourceUsage["gpu"]).To(gomega.Equal(resource.MustParse("2")))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			}

			ginkgo.By("Verifying both jobs complete successfully")
			util.ExpectJobToBeCompleted(ctx, k8sClient, job1)
			util.ExpectJobToBeCompleted(ctx, k8sClient, job2)

			ginkgo.By("Verifying both workloads finished successfully")
			for _, job := range []*batchv1.Job{job1, job2} {
				wlLookupKey := types.NamespacedName{
					Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
					Namespace: ns.Name,
				}
				util.ExpectWorkloadToFinish(ctx, k8sClient, wlLookupKey)
			}
		})

		ginkgo.It("Should queue third job when DRA quota is full and admit it after quota is freed", func() {
			ginkgo.By("Creating ResourceClaimTemplates for three jobs")
			rct1 := utiltesting.MakeResourceClaimTemplate("gpu-template-a", ns.Name).
				DeviceRequest("gpu-request", draconsts.DriverName, 2).
				Obj()
			util.MustCreate(ctx, k8sClient, rct1)

			rct2 := utiltesting.MakeResourceClaimTemplate("gpu-template-b", ns.Name).
				DeviceRequest("gpu-request", draconsts.DriverName, 2).
				Obj()
			util.MustCreate(ctx, k8sClient, rct2)

			rct3 := utiltesting.MakeResourceClaimTemplate("gpu-template-c", ns.Name).
				DeviceRequest("gpu-request", draconsts.DriverName, 2).
				Obj()
			util.MustCreate(ctx, k8sClient, rct3)

			ginkgo.By("Creating first job that uses 2 GPUs and completes quickly")
			job1 := testingjob.MakeJob("dra-job-a", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				ResourceClaimTemplate("gpu", "gpu-template-a").
				Obj()
			util.MustCreate(ctx, k8sClient, job1)

			ginkgo.By("Creating second job that uses 2 GPUs and completes quickly")
			job2 := testingjob.MakeJob("dra-job-b", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				ResourceClaimTemplate("gpu", "gpu-template-b").
				Obj()
			util.MustCreate(ctx, k8sClient, job2)

			ginkgo.By("Creating third job that needs 2 GPUs (quota now full with 4)")
			job3 := testingjob.MakeJob("dra-job-c", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				ResourceClaimTemplate("gpu", "gpu-template-c").
				Obj()
			util.MustCreate(ctx, k8sClient, job3)

			ginkgo.By("Verifying first two jobs complete and third job eventually runs")
			util.ExpectJobToBeCompleted(ctx, k8sClient, job3)

			ginkgo.By("Verifying third workload finished successfully")
			wlLookupKey3 := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job3.Name, job3.UID),
				Namespace: ns.Name,
			}
			util.ExpectWorkloadToFinish(ctx, k8sClient, wlLookupKey3)
		})

		ginkgo.It("Should correctly calculate DRA resources for multi-pod jobs", func() {
			ginkgo.By("Creating ResourceClaimTemplate requesting 1 GPU per pod")
			rct := utiltesting.MakeResourceClaimTemplate("multi-pod-gpu-template", ns.Name).
				DeviceRequest("gpu-request", draconsts.DriverName, 1).
				Obj()
			util.MustCreate(ctx, k8sClient, rct)

			ginkgo.By("Creating Job with parallelism=2")
			job := testingjob.MakeJob("multi-pod-dra-job", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Parallelism(2).
				Completions(2).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				ResourceClaimTemplate("gpu", "multi-pod-gpu-template").
				Obj()
			util.MustCreate(ctx, k8sClient, job)

			wlLookupKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				Namespace: ns.Name,
			}

			ginkgo.By("Verifying workload is admitted with correct total DRA resource usage")
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(createdWorkload)).To(gomega.BeTrue())
				g.Expect(createdWorkload.Status.Admission).NotTo(gomega.BeNil())
				g.Expect(createdWorkload.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))

				assignment := createdWorkload.Status.Admission.PodSetAssignments[0]
				// Verify pod count is 2
				g.Expect(assignment.Count).To(gomega.Equal(ptr.To(int32(2))))
				// Verify total GPU usage is 2 (1 GPU per pod * 2 pods)
				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName("gpu")))
				g.Expect(assignment.ResourceUsage["gpu"]).To(gomega.Equal(resource.MustParse("2")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Verifying job completes successfully")
			util.ExpectJobToBeCompleted(ctx, k8sClient, job)
		})
	})
})
