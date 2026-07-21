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

package capacity

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
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

var _ = ginkgo.Describe("DRA Consumable Capacity", func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-dra-cc-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("Creating Jobs with consumable capacity resources", func() {
		var (
			resourceFlavor *kueue.ResourceFlavor
			clusterQueue   *kueue.ClusterQueue
			localQueue     *kueue.LocalQueue
		)
		ginkgo.BeforeEach(func() {
			resourceFlavor = utiltestingapi.MakeResourceFlavor("dra-cc-flavor-" + ns.Name).Obj()
			util.MustCreate(ctx, k8sClient, resourceFlavor)

			clusterQueue = utiltestingapi.MakeClusterQueue("dra-cc-cq-" + ns.Name).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
						Resource(corev1.ResourceCPU, "4").
						Resource("gpu.memory", "320Gi").
						Obj()).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("dra-cc-lq", ns.Name).
				ClusterQueue(clusterQueue.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
		})

		ginkgo.It("Should admit workload with explicit capacity request", func() {
			ginkgo.By("Creating ResourceClaimTemplate with capacity.requests")
			rct := utiltesting.MakeResourceClaimTemplate("cc-explicit-template", ns.Name).
				DeviceRequest("gpu-request", util.DRAExampleDriverName, 1).
				WithCapacityRequests(map[string]string{"memory": "20Gi"}).
				Obj()
			util.MustCreate(ctx, k8sClient, rct)

			ginkgo.By("Creating Job with capacity RCT")
			job := testingjob.MakeJob("cc-explicit-job", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				ResourceClaimTemplate("gpu", "cc-explicit-template").
				Obj()
			util.MustCreate(ctx, k8sClient, job)

			wlLookupKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				Namespace: ns.Name,
			}

			ginkgo.By("Verifying workload is admitted with capacity charge of 20Gi")
			util.ExpectWorkloadResourceUsage(ctx, k8sClient, wlLookupKey, "gpu.memory", "20Gi")

			ginkgo.By("Verifying job completes successfully")
			util.ExpectJobToBeCompleted(ctx, k8sClient, job)
			util.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlLookupKey, util.LongTimeout)
		})

		ginkgo.It("Should default to full device capacity when no request specified", func() {
			ginkgo.By("Creating ResourceClaimTemplate without capacity.requests")
			rct := utiltesting.MakeResourceClaimTemplate("cc-default-template", ns.Name).
				DeviceRequest("gpu-request", util.DRAExampleDriverName, 1).
				Obj()
			util.MustCreate(ctx, k8sClient, rct)

			ginkgo.By("Creating Job")
			job := testingjob.MakeJob("cc-default-job", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				ResourceClaimTemplate("gpu", "cc-default-template").
				Obj()
			util.MustCreate(ctx, k8sClient, job)

			wlLookupKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				Namespace: ns.Name,
			}

			ginkgo.By("Verifying workload charges RequestPolicy.Default (80Gi)")
			util.ExpectWorkloadResourceUsage(ctx, k8sClient, wlLookupKey, "gpu.memory", "80Gi")

			util.ExpectJobToBeCompleted(ctx, k8sClient, job)
			util.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlLookupKey, util.LongTimeout)
		})

		ginkgo.It("Should round up capacity request to ValidRange step", func() {
			ginkgo.By("Creating ResourceClaimTemplate requesting 15500Mi (rounds up to 16Gi with step=1Gi)")
			rct := utiltesting.MakeResourceClaimTemplate("cc-round-template", ns.Name).
				DeviceRequest("gpu-request", util.DRAExampleDriverName, 1).
				WithCapacityRequests(map[string]string{"memory": "15500Mi"}).
				Obj()
			util.MustCreate(ctx, k8sClient, rct)

			ginkgo.By("Creating Job")
			job := testingjob.MakeJob("cc-round-job", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				ResourceClaimTemplate("gpu", "cc-round-template").
				Obj()
			util.MustCreate(ctx, k8sClient, job)

			wlLookupKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				Namespace: ns.Name,
			}

			ginkgo.By("Verifying charge is 16Gi (15500Mi rounded up to next 1Gi step)")
			util.ExpectWorkloadResourceUsage(ctx, k8sClient, wlLookupKey, "gpu.memory", "16Gi")

			util.ExpectJobToBeCompleted(ctx, k8sClient, job)
			util.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlLookupKey, util.LongTimeout)
		})

		ginkgo.It("Should multiply capacity charge by request count", func() {
			ginkgo.By("Creating ResourceClaimTemplate for 2 devices with capacity.requests")
			rct := utiltesting.MakeResourceClaimTemplate("cc-count2-template", ns.Name).
				DeviceRequest("gpu-request", util.DRAExampleDriverName, 2).
				WithCapacityRequests(map[string]string{"memory": "20Gi"}).
				Obj()
			util.MustCreate(ctx, k8sClient, rct)

			ginkgo.By("Creating Job requesting 2 devices")
			job := testingjob.MakeJob("cc-count2-job", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				ResourceClaimTemplate("gpu", "cc-count2-template").
				Obj()
			util.MustCreate(ctx, k8sClient, job)

			wlLookupKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				Namespace: ns.Name,
			}

			ginkgo.By("Verifying capacity charge is 40Gi (20Gi x 2)")
			util.ExpectWorkloadResourceUsage(ctx, k8sClient, wlLookupKey, "gpu.memory", "40Gi")

			util.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlLookupKey, util.LongTimeout)
		})

		ginkgo.It("Should not admit workload when capacity charge exceeds quota", func() {
			ginkgo.By("Creating ResourceClaimTemplate requesting 5 devices at 80Gi each (400Gi > 320Gi quota)")
			rct := utiltesting.MakeResourceClaimTemplate("cc-exceed-template", ns.Name).
				DeviceRequest("gpu-request", util.DRAExampleDriverName, 5).
				WithCapacityRequests(map[string]string{"memory": "80Gi"}).
				Obj()
			util.MustCreate(ctx, k8sClient, rct)

			ginkgo.By("Creating Job with capacity charge exceeding quota")
			job := testingjob.MakeJob("cc-exceed-job", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				ResourceClaimTemplate("gpu", "cc-exceed-template").
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
			}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should admit multiple workloads sharing capacity quota", func() {
			ginkgo.By("Creating ResourceClaimTemplate for capacity request")
			rct := utiltesting.MakeResourceClaimTemplate("cc-share-template", ns.Name).
				DeviceRequest("gpu-request", util.DRAExampleDriverName, 1).
				WithCapacityRequests(map[string]string{"memory": "20Gi"}).
				Obj()
			util.MustCreate(ctx, k8sClient, rct)

			ginkgo.By("Creating two jobs each requesting 20Gi from the same gpu.memory pool")
			job1 := testingjob.MakeJob("cc-share-job-1", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				ResourceClaimTemplate("gpu", "cc-share-template").
				Obj()
			util.MustCreate(ctx, k8sClient, job1)

			job2 := testingjob.MakeJob("cc-share-job-2", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				ResourceClaimTemplate("gpu", "cc-share-template").
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
				util.ExpectWorkloadResourceUsage(ctx, k8sClient, wlKey, "gpu.memory", "20Gi")
			}

			ginkgo.By("Verifying both jobs complete")
			util.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlLookupKey1, util.LongTimeout)
			util.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlLookupKey2, util.LongTimeout)
		})

		ginkgo.It("Should mark workload inadmissible when capacity dimension has no matching devices", func() {
			ginkgo.By("Creating ResourceClaimTemplate with unmatchable CEL selector")
			rct := utiltesting.MakeResourceClaimTemplate("cc-nomatch-template", ns.Name).
				DeviceRequest("gpu-request", util.DRAExampleDriverName, 1).
				WithCELSelectors("device.capacity[\"gpu.example.com\"].memory.compareTo(quantity(\"999Gi\")) == 0").
				WithCapacityRequests(map[string]string{"memory": "10Gi"}).
				Obj()
			util.MustCreate(ctx, k8sClient, rct)

			ginkgo.By("Creating Job with unmatchable CEL selector")
			job := testingjob.MakeJob("cc-nomatch-job", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				ResourceClaimTemplate("gpu", "cc-nomatch-template").
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
					gomega.HaveField("Reason", kueue.WorkloadInadmissible),
				)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
