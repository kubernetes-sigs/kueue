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
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var defaultEnabledIntegrations = sets.New(
	"batch/job", "kubeflow.org/mpijob", "ray.io/rayjob", "ray.io/raycluster",
	"jobset.x-k8s.io/jobset", "kubeflow.org/paddlejob",
	"kubeflow.org/pytorchjob", "kubeflow.org/tfjob", "kubeflow.org/xgboostjob", "kubeflow.org/jaxjob",
	"pod", "workload.codeflare.dev/appwrapper", "trainer.kubeflow.org/trainjob",
)

var _ = ginkgo.Describe("MultiKueue with scheduler", ginkgo.Label("area:multikueue", "feature:multikueue"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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

		managerHighWPC *kueue.WorkloadPriorityClass
		managerLowWPC  *kueue.WorkloadPriorityClass
		managerFlavor  *kueue.ResourceFlavor
		managerCq      *kueue.ClusterQueue
		managerLq      *kueue.LocalQueue

		worker1Flavor *kueue.ResourceFlavor
		worker1Cq     *kueue.ClusterQueue
		worker1Lq     *kueue.LocalQueue

		worker2Flavor *kueue.ResourceFlavor
		worker2Cq     *kueue.ClusterQueue
		worker2Lq     *kueue.LocalQueue
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
		managerNs = util.CreateNamespaceFromPrefixWithLog(managerTestCluster.ctx, managerTestCluster.client, "multikueue-")
		worker1Ns = util.CreateNamespaceWithLog(worker1TestCluster.ctx, worker1TestCluster.client, managerNs.Name)
		worker2Ns = util.CreateNamespaceWithLog(worker2TestCluster.ctx, worker2TestCluster.client, managerNs.Name)

		w1Kubeconfig, err := worker1TestCluster.kubeConfigBytes()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		w2Kubeconfig, err := worker2TestCluster.kubeConfigBytes()
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

		managerMultiKueueSecret2 = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multikueue2",
				Namespace: managersConfigNamespace.Name,
			},
			Data: map[string][]byte{
				kueue.MultiKueueConfigSecretKey: w2Kubeconfig,
			},
		}
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret2)

		workerCluster1 = utiltestingapi.MakeMultiKueueCluster("worker1").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret1.Name).Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, workerCluster1)

		workerCluster2 = utiltestingapi.MakeMultiKueueCluster("worker2").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret2.Name).Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, workerCluster2)

		managerMultiKueueConfig = utiltestingapi.MakeMultiKueueConfig("multikueueconfig").Clusters(workerCluster1.Name, workerCluster2.Name).Obj()
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
					Resource(corev1.ResourceCPU, "2").
					Resource(corev1.ResourceMemory, "2G").
					Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
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
			Obj()
		util.CreateClusterQueuesAndWaitForActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq)

		worker1Lq = utiltestingapi.MakeLocalQueue("lq", worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Lq)

		worker2Flavor = utiltestingapi.MakeResourceFlavor("fl").Obj()
		util.MustCreate(worker2TestCluster.ctx, worker2TestCluster.client, worker2Flavor)

		worker2Cq = utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(worker2Flavor.Name).
					Resource(corev1.ResourceCPU, "1").
					Resource(corev1.ResourceMemory, "2G").
					Obj(),
			).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2Cq)

		worker2Lq = utiltestingapi.MakeLocalQueue("lq", worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2Lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(managerTestCluster.ctx, managerTestCluster.client, managerNs)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(worker1TestCluster.ctx, worker1TestCluster.client, worker1Ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(worker2TestCluster.ctx, worker2TestCluster.client, worker2Ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerCq, true)
		util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq, true)
		util.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2Cq, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerFlavor, true)
		util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1Flavor, true)
		util.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2Flavor, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerLowWPC, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerHighWPC, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, multiKueueAC, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueConfig, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, workerCluster1, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, workerCluster2, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret1, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret2, true)
	})

	ginkgo.It("should preempt a running low-priority workload when a high-priority workload is admitted (same worker)", func() {
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

		ginkgo.By("Checking that the low-priority workload is created in worker1 and not in worker2, and that its spec matches the manager workload", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, lowWlKey, workerLowWorkload)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(workerLowWorkload)).To(gomega.BeTrue())
				g.Expect(workerLowWorkload.Spec).To(gomega.BeComparableTo(managerLowWl.Spec))
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, lowWlKey, &kueue.Workload{})).To(testing.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Updating the worker job status to active=1", func() {
			createdJob := batchv1.Job{}
			startTime := metav1.NewTime(time.Now().Truncate(time.Second))
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(lowJob), &createdJob)).To(gomega.Succeed())
				createdJob.Status.StartTime = &startTime
				createdJob.Status.Active = 1
				createdJob.Status.Ready = ptr.To[int32](1)
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(lowJob), &createdJob)).To(gomega.Succeed())
				g.Expect(createdJob.Status.StartTime).To(gomega.Equal(&startTime))
				g.Expect(createdJob.Status.Active).To(gomega.Equal(int32(1)))
				g.Expect(ptr.Deref(createdJob.Status.Ready, 0)).To(gomega.Equal(int32(1)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		highJob := testingjob.MakeJob("high-job", managerNs.Name).
			WorkloadPriorityClass(managerHighWPC.Name).
			Queue(kueue.LocalQueueName(managerLq.Name)).
			RequestAndLimit(corev1.ResourceCPU, "2").
			RequestAndLimit(corev1.ResourceMemory, "1G").
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, highJob)

		ginkgo.By("Checking that the manager job is suspended", func() {
			createdJob := batchv1.Job{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(lowJob), &createdJob)).To(gomega.Succeed())
				g.Expect(createdJob.Spec.Suspend).To(gomega.Equal(ptr.To(true)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the worker job is suspended", func() {
			createdJob := batchv1.Job{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(lowJob), &createdJob)).To(gomega.Succeed())
				g.Expect(createdJob.Spec.Suspend).To(gomega.Equal(ptr.To(true)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Updating the worker job status to active=0", func() {
			createdJob := batchv1.Job{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(lowJob), &createdJob)).To(gomega.Succeed())
				createdJob.Status.Active = 0
				createdJob.Status.Ready = ptr.To[int32](0)
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdJob)).To(gomega.Succeed())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(lowJob), &createdJob)).To(gomega.Succeed())
				g.Expect(createdJob.Status.Active).To(gomega.Equal(int32(0)))
				g.Expect(ptr.Deref(createdJob.Status.Ready, 0)).To(gomega.Equal(int32(0)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the low-priority workload has condition QuotaReserved=false in the manager clusters", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, lowWlKey, managerLowWl)).To(gomega.Succeed())
				g.Expect(managerLowWl.Status.Conditions).To(testing.HaveConditionStatusFalse(kueue.WorkloadQuotaReserved))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		highWlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(highJob.Name, highJob.UID), Namespace: managerNs.Name}

		managerHighWl := &kueue.Workload{}
		workerHighWorkload := &kueue.Workload{}

		ginkgo.By("Checking that the high-priority workload is created and admitted in the manager clusters", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, highWlKey, managerHighWl)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(managerHighWl)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the high-priority workload is created in worker1 and not in worker2, and that its spec matches the manager workload", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, highWlKey, workerHighWorkload)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(workerHighWorkload)).To(gomega.BeTrue())
				g.Expect(workerHighWorkload.Spec).To(gomega.BeComparableTo(managerHighWl.Spec))
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, highWlKey, &kueue.Workload{})).To(testing.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the low-priority workload is preempted in the manager cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, lowWlKey, managerLowWl)).To(gomega.Succeed())
				g.Expect(workload.IsEvicted(managerLowWl)).To(gomega.BeTrue())
				g.Expect(managerLowWl.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadPreempted))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the low-priority workload is deleted from the worker clusters", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, lowWlKey, &kueue.Workload{})).To(testing.BeNotFoundError())
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, lowWlKey, &kueue.Workload{})).To(testing.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("should preempt a running low-priority workload when a high-priority workload is admitted (other workers)", func() {
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

		ginkgo.By("Checking that the low-priority workload is created in worker1 and not in worker2, and that its spec matches the manager workload", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, lowWlKey, workerLowWorkload)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(workerLowWorkload)).To(gomega.BeTrue())
				g.Expect(workerLowWorkload.Spec).To(gomega.BeComparableTo(managerLowWl.Spec))
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, lowWlKey, &kueue.Workload{})).To(testing.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Updating the worker job status to active=1", func() {
			createdJob := batchv1.Job{}
			startTime := metav1.NewTime(time.Now().Truncate(time.Second))
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(lowJob), &createdJob)).To(gomega.Succeed())
				createdJob.Status.StartTime = &startTime
				createdJob.Status.Active = 1
				createdJob.Status.Ready = ptr.To[int32](1)
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(lowJob), &createdJob)).To(gomega.Succeed())
				g.Expect(createdJob.Status.StartTime).To(gomega.Equal(&startTime))
				g.Expect(createdJob.Status.Active).To(gomega.Equal(int32(1)))
				g.Expect(ptr.Deref(createdJob.Status.Ready, 0)).To(gomega.Equal(int32(1)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		highJob := testingjob.MakeJob("high-job", managerNs.Name).
			WorkloadPriorityClass(managerHighWPC.Name).
			Queue(kueue.LocalQueueName(managerLq.Name)).
			RequestAndLimit(corev1.ResourceCPU, "1").
			RequestAndLimit(corev1.ResourceMemory, "2G").
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, highJob)

		ginkgo.By("Checking that the manager job is suspended", func() {
			createdJob := batchv1.Job{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(lowJob), &createdJob)).To(gomega.Succeed())
				g.Expect(createdJob.Spec.Suspend).To(gomega.Equal(ptr.To(true)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the worker job is suspended", func() {
			createdJob := batchv1.Job{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(lowJob), &createdJob)).To(gomega.Succeed())
				g.Expect(createdJob.Spec.Suspend).To(gomega.Equal(ptr.To(true)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Updating the worker job status to active=0", func() {
			createdJob := batchv1.Job{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(lowJob), &createdJob)).To(gomega.Succeed())
				createdJob.Status.Active = 0
				createdJob.Status.Ready = ptr.To[int32](0)
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdJob)).To(gomega.Succeed())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(lowJob), &createdJob)).To(gomega.Succeed())
				g.Expect(createdJob.Status.Active).To(gomega.Equal(int32(0)))
				g.Expect(ptr.Deref(createdJob.Status.Ready, 0)).To(gomega.Equal(int32(0)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the low-priority workload has condition QuotaReserved=false in the manager clusters", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, lowWlKey, managerLowWl)).To(gomega.Succeed())
				g.Expect(managerLowWl.Status.Conditions).To(testing.HaveConditionStatusFalse(kueue.WorkloadQuotaReserved))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		highWlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(highJob.Name, highJob.UID), Namespace: managerNs.Name}

		managerHighWl := &kueue.Workload{}
		workerHighWorkload := &kueue.Workload{}

		ginkgo.By("Checking that the high-priority workload is created and admitted in the manager clusters", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, highWlKey, managerHighWl)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(managerHighWl)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the high-priority workload is created in worker2 and not in worker1, and that its spec matches the manager workload", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, highWlKey, workerHighWorkload)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(workerHighWorkload)).To(gomega.BeTrue())
				g.Expect(workerHighWorkload.Spec).To(gomega.BeComparableTo(managerHighWl.Spec))
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, highWlKey, &kueue.Workload{})).To(testing.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the low-priority workload is preempted in the manager cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, lowWlKey, managerLowWl)).To(gomega.Succeed())
				g.Expect(workload.IsEvicted(managerLowWl)).To(gomega.BeTrue())
				g.Expect(managerLowWl.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadPreempted))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the low-priority workload is deleted from the worker clusters", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, lowWlKey, &kueue.Workload{})).To(testing.BeNotFoundError())
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, lowWlKey, &kueue.Workload{})).To(testing.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("should update the workload’s priority class on the worker if it changes on the manager", func() {
		job := testingjob.MakeJob("job", managerNs.Name).
			WorkloadPriorityClass(managerHighWPC.Name).
			Queue(kueue.LocalQueueName(managerLq.Name)).
			RequestAndLimit(corev1.ResourceCPU, "2").
			RequestAndLimit(corev1.ResourceMemory, "1G").
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)

		wlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

		managerWl := &kueue.Workload{}
		workerWl := &kueue.Workload{}

		ginkgo.By("Checking that the workload is created and admitted in the manager cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlKey, managerWl)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(managerWl)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the workload is created in worker1 and not in worker2, and that its spec matches the manager workload", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlKey, workerWl)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(workerWl)).To(gomega.BeTrue())
				g.Expect(workerWl.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlKey, &kueue.Workload{})).To(testing.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Setting a low priority class on the job", func() {
			createdJob := &batchv1.Job{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
				createdJob.Labels[constants.WorkloadPriorityClassLabel] = managerLowWPC.Name
				g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the workload priority class updated in the worker", func() {
			util.ExpectWorkloadsWithWorkloadPriority(worker1TestCluster.ctx, worker1TestCluster.client, managerLowWPC.Name, managerLowWPC.Value, wlKey)
		})
	})
})
