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

package counter

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

func expectWorkloadCounterCharge(ctx context.Context, wlKey types.NamespacedName, expectedMemory string) {
	createdWorkload := &kueue.Workload{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlKey, createdWorkload)).To(gomega.Succeed())
		g.Expect(workload.HasQuotaReservation(createdWorkload)).To(gomega.BeTrue())
		g.Expect(createdWorkload.Status.Admission).NotTo(gomega.BeNil())
		g.Expect(createdWorkload.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))

		assignment := createdWorkload.Status.Admission.PodSetAssignments[0]
		g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName("gpu.memory")))
		memUsage := assignment.ResourceUsage["gpu.memory"]
		g.Expect(memUsage.Cmp(resource.MustParse(expectedMemory))).To(gomega.Equal(0))
	}, util.Timeout, util.Interval).Should(gomega.Succeed(), util.AssertMsg("workload should have counter charge of "+expectedMemory, createdWorkload))
}

var _ = ginkgo.Describe("DRA Partitionable Devices", func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-dra-extended-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("Creating Jobs with partitionable device resources", func() {
		var (
			resourceFlavor *kueue.ResourceFlavor
			clusterQueue   *kueue.ClusterQueue
			localQueue     *kueue.LocalQueue
		)
		ginkgo.BeforeEach(func() {
			resourceFlavor = utiltestingapi.MakeResourceFlavor("dra-ext-flavor-" + ns.Name).Obj()
			util.MustCreate(ctx, k8sClient, resourceFlavor)

			clusterQueue = utiltestingapi.MakeClusterQueue("dra-ext-cq-" + ns.Name).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
						Resource(corev1.ResourceCPU, "4").
						Resource("gpu.memory", "160Gi").
						Obj()).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("dra-ext-lq", ns.Name).
				ClusterQueue(clusterQueue.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
		})

		ginkgo.It("Should admit partition workload with counter-based gpu.memory charge", func() {
			ginkgo.By("Creating ResourceClaimTemplate for a GPU partition")
			rct := utiltesting.MakeResourceClaimTemplate("partition-template", ns.Name).
				DeviceRequest("gpu-request", util.DRAExampleDriverName, 1).
				WithCELSelectors("device.capacity[\"gpu.example.com\"].memory.compareTo(quantity(\"20Gi\")) == 0").
				Obj()
			util.MustCreate(ctx, k8sClient, rct)

			ginkgo.By("Creating Job with partition RCT")
			job := testingjob.MakeJob("partition-job", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				ResourceClaimTemplate("gpu", "partition-template").
				Obj()
			util.MustCreate(ctx, k8sClient, job)

			wlLookupKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				Namespace: ns.Name,
			}

			ginkgo.By("Verifying workload is admitted with counter charge of 20Gi")
			expectWorkloadCounterCharge(ctx, wlLookupKey, "20Gi")

			ginkgo.By("Verifying job completes successfully")
			util.ExpectJobToBeCompleted(ctx, k8sClient, job)
			util.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlLookupKey, util.LongTimeout)
		})

		ginkgo.It("Should multiply counter charge by request count", func() {
			ginkgo.By("Creating ResourceClaimTemplate for 2 GPU partitions")
			rct := utiltesting.MakeResourceClaimTemplate("partition-count2-template", ns.Name).
				DeviceRequest("gpu-request", util.DRAExampleDriverName, 2).
				WithCELSelectors("device.capacity[\"gpu.example.com\"].memory.compareTo(quantity(\"20Gi\")) == 0").
				Obj()
			util.MustCreate(ctx, k8sClient, rct)

			ginkgo.By("Creating Job requesting 2 partitions")
			job := testingjob.MakeJob("count2-job", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				ResourceClaimTemplate("gpu", "partition-count2-template").
				Obj()
			util.MustCreate(ctx, k8sClient, job)

			wlLookupKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				Namespace: ns.Name,
			}

			ginkgo.By("Verifying counter charge is 40Gi (20Gi x 2)")
			expectWorkloadCounterCharge(ctx, wlLookupKey, "40Gi")

			util.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlLookupKey, util.LongTimeout)
		})

		ginkgo.It("Should charge largest counter value when CEL matches multiple device types", func() {
			ginkgo.By("Creating ResourceClaimTemplate matching partitions (20Gi) and full GPUs (80Gi)")
			rct := utiltesting.MakeResourceClaimTemplate("broad-template", ns.Name).
				DeviceRequest("gpu-request", util.DRAExampleDriverName, 1).
				WithCELSelectors("device.capacity[\"gpu.example.com\"].memory.compareTo(quantity(\"20Gi\")) >= 0").
				Obj()
			util.MustCreate(ctx, k8sClient, rct)

			ginkgo.By("Creating Job with broad CEL selector")
			job := testingjob.MakeJob("broad-job", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				ResourceClaimTemplate("gpu", "broad-template").
				Obj()
			util.MustCreate(ctx, k8sClient, job)

			wlLookupKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				Namespace: ns.Name,
			}

			ginkgo.By("Verifying counter charge is 80Gi (largest across matched devices)")
			expectWorkloadCounterCharge(ctx, wlLookupKey, "80Gi")

			util.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlLookupKey, util.LongTimeout)
		})

		ginkgo.It("Should admit full GPU workload with counter charge", func() {
			ginkgo.By("Creating ResourceClaimTemplate for full GPU (no CEL selector)")
			rct := utiltesting.MakeResourceClaimTemplate("fullgpu-template", ns.Name).
				DeviceRequest("gpu-request", util.DRAExampleDriverName, 1).
				Obj()
			util.MustCreate(ctx, k8sClient, rct)

			ginkgo.By("Creating Job requesting full GPU")
			job := testingjob.MakeJob("fullgpu-job", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				ResourceClaimTemplate("gpu", "fullgpu-template").
				Obj()
			util.MustCreate(ctx, k8sClient, job)

			wlLookupKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				Namespace: ns.Name,
			}

			ginkgo.By("Verifying counter charge is 80Gi (largest across all devices)")
			expectWorkloadCounterCharge(ctx, wlLookupKey, "80Gi")

			util.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlLookupKey, util.LongTimeout)
		})

		ginkgo.It("Should admit unified workload with full GPU and partition charges", func() {
			ginkgo.By("Creating ResourceClaimTemplates for full GPU and partition")
			rctFull := utiltesting.MakeResourceClaimTemplate("unified-full-template", ns.Name).
				DeviceRequest("gpu-request", util.DRAExampleDriverName, 1).
				Obj()
			util.MustCreate(ctx, k8sClient, rctFull)

			rctPartition := utiltesting.MakeResourceClaimTemplate("unified-partition-template", ns.Name).
				DeviceRequest("gpu-request", util.DRAExampleDriverName, 1).
				WithCELSelectors("device.capacity[\"gpu.example.com\"].memory.compareTo(quantity(\"20Gi\")) == 0").
				Obj()
			util.MustCreate(ctx, k8sClient, rctPartition)

			ginkgo.By("Creating Job with both full GPU and partition RCTs")
			job := testingjob.MakeJob("unified-job", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				ResourceClaimTemplate("full", "unified-full-template").
				ResourceClaimTemplate("partition", "unified-partition-template").
				Obj()
			util.MustCreate(ctx, k8sClient, job)

			wlLookupKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				Namespace: ns.Name,
			}

			ginkgo.By("Verifying combined counter charge is 100Gi (80Gi full + 20Gi partition)")
			expectWorkloadCounterCharge(ctx, wlLookupKey, "100Gi")

			ginkgo.By("Verifying job completes successfully")
			util.ExpectJobToBeCompleted(ctx, k8sClient, job)
			util.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlLookupKey, util.LongTimeout)
		})

		ginkgo.It("Should not admit workload when counter charge exceeds quota", func() {
			ginkgo.By("Creating ResourceClaimTemplate requesting 10 partitions (200Gi > 160Gi quota)")
			rct := utiltesting.MakeResourceClaimTemplate("toomany-template", ns.Name).
				DeviceRequest("gpu-request", util.DRAExampleDriverName, 10).
				WithCELSelectors("device.capacity[\"gpu.example.com\"].memory.compareTo(quantity(\"20Gi\")) == 0").
				Obj()
			util.MustCreate(ctx, k8sClient, rct)

			ginkgo.By("Creating Job with counter charge exceeding quota")
			job := testingjob.MakeJob("toomany-job", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				ResourceClaimTemplate("gpu", "toomany-template").
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

			ginkgo.By("Verifying workload is not admitted")
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(createdWorkload)).To(gomega.BeFalse())
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.It("Should admit multiple workloads sharing counter quota", func() {
			ginkgo.By("Creating ResourceClaimTemplate for partition")
			rct := utiltesting.MakeResourceClaimTemplate("share-template", ns.Name).
				DeviceRequest("gpu-request", util.DRAExampleDriverName, 1).
				WithCELSelectors("device.capacity[\"gpu.example.com\"].memory.compareTo(quantity(\"20Gi\")) == 0").
				Obj()
			util.MustCreate(ctx, k8sClient, rct)

			ginkgo.By("Creating two jobs each requesting 20Gi from the same gpu.memory pool")
			job1 := testingjob.MakeJob("share-job-1", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				ResourceClaimTemplate("gpu", "share-template").
				Obj()
			util.MustCreate(ctx, k8sClient, job1)

			job2 := testingjob.MakeJob("share-job-2", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				ResourceClaimTemplate("gpu", "share-template").
				Obj()
			util.MustCreate(ctx, k8sClient, job2)

			ginkgo.By("Verifying both workloads are admitted with 20Gi each")
			wlLookupKey1 := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job1.Name, job1.UID),
				Namespace: ns.Name,
			}
			wlLookupKey2 := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job2.Name, job2.UID),
				Namespace: ns.Name,
			}

			for _, wlKey := range []types.NamespacedName{wlLookupKey1, wlLookupKey2} {
				expectWorkloadCounterCharge(ctx, wlKey, "20Gi")
			}

			ginkgo.By("Verifying both jobs complete")
			util.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlLookupKey1, util.LongTimeout)
			util.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlLookupKey2, util.LongTimeout)
		})

		ginkgo.It("Should mark workload inadmissible when CEL matches no devices", func() {
			ginkgo.By("Creating ResourceClaimTemplate with nonexistent capacity selector")
			rct := utiltesting.MakeResourceClaimTemplate("nomatch-template", ns.Name).
				DeviceRequest("gpu-request", util.DRAExampleDriverName, 1).
				WithCELSelectors("device.capacity[\"gpu.example.com\"].memory.compareTo(quantity(\"30Gi\")) == 0").
				Obj()
			util.MustCreate(ctx, k8sClient, rct)

			ginkgo.By("Creating Job with unmatchable CEL selector")
			job := testingjob.MakeJob("nomatch-job", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				ResourceClaimTemplate("gpu", "nomatch-template").
				Obj()
			util.MustCreate(ctx, k8sClient, job)

			wlLookupKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				Namespace: ns.Name,
			}

			ginkgo.By("Verifying workload is marked inadmissible")
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(createdWorkload)).To(gomega.BeFalse())

				g.Expect(createdWorkload.Status.Conditions).To(gomega.ContainElement(gomega.And(
					gomega.HaveField("Type", kueue.WorkloadQuotaReserved),
					gomega.HaveField("Status", metav1.ConditionFalse),
					gomega.HaveField("Reason", kueue.WorkloadQuotaReservedReasonMisconfigured),
				)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
