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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("MultiKueue with scheduler and preemption gates", ginkgo.Label("area:multikueue", "feature:multikueue"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		managerNs *corev1.Namespace
		worker1Ns *corev1.Namespace

		managerMultiKueueSecret1 *corev1.Secret
		workerCluster1           *kueue.MultiKueueCluster
		managerMultiKueueConfig  *kueue.MultiKueueConfig
		multiKueueAC             *kueue.AdmissionCheck

		managerHighWPC *kueue.WorkloadPriorityClass
		managerLowWPC  *kueue.WorkloadPriorityClass
		managerFlavor  *kueue.ResourceFlavor
		managerCq      *kueue.ClusterQueue
		managerLq      *kueue.LocalQueue

		worker1Flavor *kueue.ResourceFlavor
		worker1Cq     *kueue.ClusterQueue
		worker1Lq     *kueue.LocalQueue
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
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MultiKueueOrchestratedPreemption, true)

		managerNs = util.CreateNamespaceFromPrefixWithLog(managerTestCluster.ctx, managerTestCluster.client, "multikueue-preemption-")
		worker1Ns = util.CreateNamespaceWithLog(worker1TestCluster.ctx, worker1TestCluster.client, managerNs.Name)

		w1Kubeconfig, err := worker1TestCluster.kubeConfigBytes()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		managerMultiKueueSecret1 = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multikueue1",
				Namespace: managersConfigNamespace.Name,
			},
			Data: map[string][]byte{
				kueue.MultiKueueConfigSecretKey: w1Kubeconfig,
			},
		}
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret1)

		workerCluster1 = utiltestingapi.MakeMultiKueueCluster("worker1").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret1.Name).Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, workerCluster1)

		managerMultiKueueConfig = utiltestingapi.MakeMultiKueueConfig("multikueueconfig").Clusters(workerCluster1.Name).Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueConfig)

		multiKueueAC = utiltestingapi.MakeAdmissionCheck("ac1").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", managerMultiKueueConfig.Name).
			Obj()
		util.CreateAdmissionChecksAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, multiKueueAC)

		managerHighWPC = utiltestingapi.MakeWorkloadPriorityClass("high-workload").PriorityValue(300).Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerHighWPC)

		managerLowWPC = utiltestingapi.MakeWorkloadPriorityClass("low-workload").PriorityValue(100).Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerLowWPC)

		managerFlavor = utiltestingapi.MakeResourceFlavor("fl").Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerFlavor)

		managerCq = utiltestingapi.MakeClusterQueue("q1").
			AdmissionChecks(kueue.AdmissionCheckReference(multiKueueAC.Name)).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(managerFlavor.Name).
					Resource(corev1.ResourceCPU, "4").
					Resource(corev1.ResourceMemory, "2G").
					Obj(),
			).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, managerCq)

		managerLq = utiltestingapi.MakeLocalQueue("lq", managerNs.Name).ClusterQueue(managerCq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, managerLq)

		worker1Flavor = utiltestingapi.MakeResourceFlavor("fl").Obj()
		util.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, worker1Flavor)

		worker1Cq = utiltestingapi.MakeClusterQueue("cq1").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(worker1Flavor.Name).
					Resource(corev1.ResourceCPU, "2").
					Resource(corev1.ResourceMemory, "1G").
					Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq)

		worker1Lq = utiltestingapi.MakeLocalQueue("lq", worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(managerTestCluster.ctx, managerTestCluster.client, managerNs)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(worker1TestCluster.ctx, worker1TestCluster.client, worker1Ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerCq, true)
		util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerFlavor, true)
		util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1Flavor, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerLowWPC, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerHighWPC, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, multiKueueAC, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueConfig, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, workerCluster1, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret1, true)
	})

	ginkgo.It("should not preempt a running low-priority workload when a high-priority workload is waiting for preemption gate", func() {
		lowJob := testingjob.MakeJob("low-job", managerNs.Name).
			WorkloadPriorityClass(managerLowWPC.Name).
			Queue(kueue.LocalQueueName(managerLq.Name)).
			RequestAndLimit(corev1.ResourceCPU, "2").
			RequestAndLimit(corev1.ResourceMemory, "1G").
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, lowJob)

		lowWlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(lowJob.Name, lowJob.UID), Namespace: managerNs.Name}

		managerLowWl := &kueue.Workload{}
		workerLowWorkload := &kueue.Workload{}

		ginkgo.By("Checking that the low-priority workload is created and admitted in the manager cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, lowWlKey, managerLowWl)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(managerLowWl)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the low-priority workload is created in worker1 and that its spec matches the manager workload", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, lowWlKey, workerLowWorkload)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(workerLowWorkload)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		highJob := testingjob.MakeJob("high-job", managerNs.Name).
			WorkloadPriorityClass(managerHighWPC.Name).
			Queue(kueue.LocalQueueName(managerLq.Name)).
			RequestAndLimit(corev1.ResourceCPU, "2").
			RequestAndLimit(corev1.ResourceMemory, "1G").
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, highJob)

		highWlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(highJob.Name, highJob.UID), Namespace: managerNs.Name}

		managerHighWl := &kueue.Workload{}
		workerHighWorkload := &kueue.Workload{}

		ginkgo.By("Checking that the high-priority workload is created in the manager cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, highWlKey, managerHighWl)).To(gomega.Succeed())
				g.Expect(managerHighWl.Spec.QueueName).To(gomega.BeEquivalentTo(managerLq.Name))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				err := managerTestCluster.client.Get(managerTestCluster.ctx, highWlKey, managerHighWl)
				g.Expect(err).To(gomega.Succeed())

				job := &batchv1.Job{}
				err = managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(highJob), job)

				lq := &kueue.LocalQueue{}
				err = managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(managerLq), lq)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the high-priority workload is created in worker1 and has a closed preemption gate", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, highWlKey, workerHighWorkload)).To(gomega.Succeed())
				g.Expect(workload.HasClosedPreemptionGate(workerHighWorkload)).To(gomega.BeTrue())
				g.Expect(workerHighWorkload.Spec.PreemptionGates).To(gomega.ContainElement(kueue.PreemptionGate{Name: constants.MultiKueuePreemptionGate}))
				g.Expect(workload.IsAdmitted(workerHighWorkload)).To(gomega.BeFalse())
				preemptionBlockedCondition := apimeta.FindStatusCondition(workerHighWorkload.Status.Conditions, kueue.WorkloadPreemptionBlocked)
				g.Expect(preemptionBlockedCondition.Status).To(gomega.Equal(metav1.ConditionTrue))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the low-priority workload is NOT preempted in the worker", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, lowWlKey, workerLowWorkload)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(workerLowWorkload)).To(gomega.BeTrue())
				g.Expect(workerLowWorkload.Status.Conditions).NotTo(testing.HaveConditionStatusTrue(kueue.WorkloadPreempted))
			}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Opening the preemption gate in the high-priority workload", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, highWlKey, workerHighWorkload)).To(gomega.Succeed())
				g.Expect(workload.SetPreemptionGateState(workerHighWorkload, constants.MultiKueuePreemptionGate, kueue.GateStateOpen, metav1.Now())).To(gomega.BeTrue())
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, workerHighWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the low-priority workload is preempted in the worker", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, lowWlKey, workerLowWorkload)).To(gomega.Succeed())
				g.Expect(workload.IsEvicted(workerLowWorkload)).To(gomega.BeTrue())
				g.Expect(workerLowWorkload.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadPreempted))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the high-priority workload is admitted in the worker", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, highWlKey, workerHighWorkload)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(workerHighWorkload)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
