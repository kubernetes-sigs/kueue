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
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = ginkgo.Describe("TopologyAwareScheduling Basic Fit and Quota", ginkgo.Label(e2e.Shard1, "area:tas", "feature:tas-basic-fit"), func() {
	var (
		ns           *corev1.Namespace
		topology     *kueue.Topology
		tasFlavor    *kueue.ResourceFlavor
		localQueue   *kueue.LocalQueue
		clusterQueue *kueue.ClusterQueue
	)

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-tas-fit-")

		topology = utiltestingapi.MakeDefaultThreeLevelTopology("datacenter")
		behavioral.MustCreate(ctx, k8sClient, topology)

		tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel(tasNodeGroupLabel, instanceType).
			TopologyName(topology.Name).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, tasFlavor)

		// ClusterQueue with 8 GPUs quota
		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("tas-flavor").
					Resource(extraResource, "8").
					Obj(),
			).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
		gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("Should not admit a Job if quota is insufficient", func() {
		// Request 10 GPUs when total cluster quota is 8
		sampleJob := testingjob.MakeJob("excessive-quota-job", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Parallelism(10).
			Completions(10).
			RequestAndLimit(extraResource, "1").
			Image(e2e.GetAgnHostImage(), e2e.BehaviorExitFast).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, sampleJob)

		wlLookupKey := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(sampleJob.Name, sampleJob.UID),
			Namespace: ns.Name,
		}

		createdWorkload := &kueue.Workload{}
		ginkgo.By(fmt.Sprintf("await creation of workload %q", wlLookupKey), func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By(fmt.Sprintf("verify workload %q is not admitted due to insufficient quota", wlLookupKey), func() {
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				g.Expect(createdWorkload.Status.Admission).Should(gomega.BeNil())
				g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeFalse())
			}, behavioral.ConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should not admit a Job if pod does not fit on any single node", func() {
		// Each node only has 1 GPU allocatable. A pod requesting 2 GPUs cannot fit on any node,
		// even though total ClusterQueue quota (8) is not exceeded
		sampleJob := testingjob.MakeJob("fragmented-fit-job", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Parallelism(1).
			Completions(1).
			RequestAndLimit(extraResource, "2").
			Image(e2e.GetAgnHostImage(), e2e.BehaviorExitFast).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, sampleJob)

		wlLookupKey := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(sampleJob.Name, sampleJob.UID),
			Namespace: ns.Name,
		}

		createdWorkload := &kueue.Workload{}
		ginkgo.By(fmt.Sprintf("await creation of workload %q", wlLookupKey), func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By(fmt.Sprintf("verify workload %q is not admitted due to node fit failure", wlLookupKey), func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				g.Expect(createdWorkload.Status.Admission).Should(gomega.BeNil())
				g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeFalse())
				g.Expect(createdWorkload.Status.Conditions).To(gomega.ContainElement(gomega.And(
					gomega.HaveField("Type", kueue.WorkloadQuotaReserved),
					gomega.HaveField("Status", metav1.ConditionFalse),
					gomega.HaveField("Reason", kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed),
					gomega.HaveField("Message", gomega.ContainSubstring("doesn't allow to fit")),
				)))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should admit and complete a Job when quota and node capacity fit", func() {
		// 2 pods with 1 GPU each: fits total quota (8) and fits on two distinct 1-GPU nodes
		sampleJob := testingjob.MakeJob("basic-fit-job", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Parallelism(2).
			Completions(2).
			RequestAndLimit(extraResource, "1").
			Image(e2e.GetAgnHostImage(), e2e.BehaviorExitFast).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, sampleJob)

		wlLookupKey := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(sampleJob.Name, sampleJob.UID),
			Namespace: ns.Name,
		}

		createdWorkload := &kueue.Workload{}
		ginkgo.By(fmt.Sprintf("await admission of workload %q", wlLookupKey), func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeTrue())
			}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By(fmt.Sprintf("verify workload %q finishes successfully", wlLookupKey), func() {
			behavioral.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlLookupKey, behavioral.LongTimeout)
		})
	})
})
