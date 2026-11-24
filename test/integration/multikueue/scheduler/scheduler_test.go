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

package scheduler

import (
	"context"
	"fmt"
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
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	"sigs.k8s.io/kueue/test/util/behavioral"
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
		managerTestCluster.fwk.StartManagerForSpec(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
			managerAndMultiKueueSetup(ctx, mgr, 2*time.Second, defaultEnabledIntegrations, config.MultiKueueDispatcherModeAllAtOnce)
		})
	})

	ginkgo.BeforeEach(func() {
		managerNs = behavioral.CreateNamespaceFromPrefixWithLog(managerTestCluster.ctx, managerTestCluster.client, "multikueue-")
		worker1Ns = behavioral.CreateNamespaceWithLog(worker1TestCluster.ctx, worker1TestCluster.client, managerNs.Name)
		worker2Ns = behavioral.CreateNamespaceWithLog(worker2TestCluster.ctx, worker2TestCluster.client, managerNs.Name)

		w1Kubeconfig, err := worker1TestCluster.kubeConfigBytes()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		w2Kubeconfig, err := worker2TestCluster.kubeConfigBytes()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		managerMultiKueueSecret1 = &corev1.Secret{
			Name:      "multikueue1",
			Namespace: managersConfigNamespace.Name,
			Data: map[string][]byte{
				kueue.MultiKueueConfigSecretKey: w1Kubeconfig,
			},
		}
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret1)

		managerMultiKueueSecret2 = &corev1.Secret{
			Name:      "multikueue2",
			Namespace: managersConfigNamespace.Name,
			Data: map[string][]byte{
				kueue.MultiKueueConfigSecretKey: w2Kubeconfig,
			},
		}
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

		managerHighWPC = utiltestingapi.MakeWorkloadPriorityClass("high-workload").PriorityValue(300).Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerHighWPC)

		managerLowWPC = utiltestingapi.MakeWorkloadPriorityClass("low-workload").PriorityValue(100).Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerLowWPC)

		managerFlavor = utiltestingapi.MakeResourceFlavor("fl").Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerFlavor)

		managerCq = utiltestingapi.MakeClusterQueue("q1").
			AdmissionChecks(kueue.AdmissionCheckReference(multiKueueAC.Name)).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(managerFlavor.Name).
					Resource(corev1.ResourceCPU, "2").
					Resource(corev1.ResourceMemory, "2G").
					Resource(corev1.ResourceEphemeralStorage, "100G").
					Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, managerCq)

		managerLq = utiltestingapi.MakeLocalQueue("lq", managerNs.Name).ClusterQueue(managerCq.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, managerLq)

		worker1Flavor = utiltestingapi.MakeResourceFlavor("fl").Obj()
		behavioral.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, worker1Flavor)

		worker1Cq = utiltestingapi.MakeClusterQueue("cq1").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(worker1Flavor.Name).
					Resource(corev1.ResourceCPU, "2").
					Resource(corev1.ResourceMemory, "1G").
					Resource(corev1.ResourceEphemeralStorage, "15G").
					Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq)

		worker1Lq = utiltestingapi.MakeLocalQueue("lq", worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Lq)

		worker2Flavor = utiltestingapi.MakeResourceFlavor("fl").Obj()
		behavioral.MustCreate(worker2TestCluster.ctx, worker2TestCluster.client, worker2Flavor)

		worker2Cq = utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(worker2Flavor.Name).
					Resource(corev1.ResourceCPU, "1").
					Resource(corev1.ResourceMemory, "2G").
					Resource(corev1.ResourceEphemeralStorage, "5G").
					Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2Cq)

		worker2Lq = utiltestingapi.MakeLocalQueue("lq", worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
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
		behavioral.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1Flavor, true)
		behavioral.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2Flavor, true)
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerLowWPC, true)
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerHighWPC, true)
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, multiKueueAC, true)
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueConfig, true)
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, workerCluster1, true)
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, workerCluster2, true)
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret1, true)
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret2, true)
	})

	ginkgo.It("should preempt a running low-priority workload when a high-priority workload is admitted (same worker)", func() {
		lowJob := testingjob.MakeJob("low-job", managerNs.Name).
			WorkloadPriorityClass(managerLowWPC.Name).
			Queue(kueue.LocalQueueName(managerLq.Name)).
			RequestAndLimit(corev1.ResourceCPU, "2").
			RequestAndLimit(corev1.ResourceMemory, "1G").
			Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, lowJob)

		lowWlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(lowJob.Name, lowJob.UID), Namespace: managerNs.Name}

		managerLowWl := &kueue.Workload{}
		workerLowWorkload := &kueue.Workload{}

		ginkgo.By("Checking that the low-priority workload is created and admitted in the manager cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, lowWlKey, managerLowWl)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(managerLowWl)).To(gomega.BeTrue())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the low-priority workload is created in worker1 and not in worker2, and that its spec matches the manager workload", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, lowWlKey, workerLowWorkload)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(workerLowWorkload)).To(gomega.BeTrue())
				behavioral.ExpectRemoteWorkloadSpec(g, workerLowWorkload, managerLowWl)
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, lowWlKey, &kueue.Workload{})).To(testing.BeNotFoundError())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Updating the worker job status to active=1", func() {
			createdJob := batchv1.Job{}
			startTime := metav1.NewTime(time.Now().Truncate(time.Second))
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(lowJob), &createdJob)).To(gomega.Succeed())
				createdJob.Status.StartTime = &startTime
				createdJob.Status.Active = 1
				createdJob.Status.Ready = new(int32(1))
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdJob)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(lowJob), &createdJob)).To(gomega.Succeed())
				g.Expect(createdJob.Status.StartTime).To(gomega.Equal(&startTime))
				g.Expect(createdJob.Status.Active).To(gomega.Equal(int32(1)))
				g.Expect(ptr.Deref(createdJob.Status.Ready, 0)).To(gomega.Equal(int32(1)))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		highJob := testingjob.MakeJob("high-job", managerNs.Name).
			WorkloadPriorityClass(managerHighWPC.Name).
			Queue(kueue.LocalQueueName(managerLq.Name)).
			RequestAndLimit(corev1.ResourceCPU, "2").
			RequestAndLimit(corev1.ResourceMemory, "1G").
			Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, highJob)

		ginkgo.By("Checking that the manager job is suspended", func() {
			createdJob := batchv1.Job{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(lowJob), &createdJob)).To(gomega.Succeed())
				g.Expect(createdJob.Spec.Suspend).To(gomega.Equal(new(true)))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the worker job is suspended", func() {
			createdJob := batchv1.Job{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(lowJob), &createdJob)).To(gomega.Succeed())
				g.Expect(createdJob.Spec.Suspend).To(gomega.Equal(new(true)))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Updating the worker job status to active=0", func() {
			createdJob := batchv1.Job{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(lowJob), &createdJob)).To(gomega.Succeed())
				createdJob.Status.Active = 0
				createdJob.Status.Ready = new(int32(0))
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdJob)).To(gomega.Succeed())
			}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(lowJob), &createdJob)).To(gomega.Succeed())
				g.Expect(createdJob.Status.Active).To(gomega.Equal(int32(0)))
				g.Expect(ptr.Deref(createdJob.Status.Ready, 0)).To(gomega.Equal(int32(0)))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the low-priority workload has condition QuotaReserved=false in the manager clusters", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, lowWlKey, managerLowWl)).To(gomega.Succeed())
				g.Expect(managerLowWl.Status.Conditions).To(testing.HaveConditionStatusFalse(kueue.WorkloadQuotaReserved))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		highWlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(highJob.Name, highJob.UID), Namespace: managerNs.Name}

		managerHighWl := &kueue.Workload{}
		workerHighWorkload := &kueue.Workload{}

		ginkgo.By("Checking that the high-priority workload is created and admitted in the manager clusters", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, highWlKey, managerHighWl)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(managerHighWl)).To(gomega.BeTrue())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the high-priority workload is created in worker1 and not in worker2, and that its spec matches the manager workload", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, highWlKey, workerHighWorkload)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(workerHighWorkload)).To(gomega.BeTrue())
				behavioral.ExpectRemoteWorkloadSpec(g, workerHighWorkload, managerHighWl)
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, highWlKey, &kueue.Workload{})).To(testing.BeNotFoundError())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the low-priority workload is preempted in the manager cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, lowWlKey, managerLowWl)).To(gomega.Succeed())
				g.Expect(workloadevict.IsEvicted(managerLowWl)).To(gomega.BeTrue())
				g.Expect(managerLowWl.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadPreempted))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the low-priority workload is deleted from the worker clusters", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, lowWlKey, &kueue.Workload{})).To(testing.BeNotFoundError())
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, lowWlKey, &kueue.Workload{})).To(testing.BeNotFoundError())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("should preempt a running low-priority workload when a high-priority workload is admitted (other workers)", func() {
		lowJob := testingjob.MakeJob("low-job", managerNs.Name).
			WorkloadPriorityClass(managerLowWPC.Name).
			Queue(kueue.LocalQueueName(managerLq.Name)).
			RequestAndLimit(corev1.ResourceCPU, "2").
			RequestAndLimit(corev1.ResourceMemory, "1G").
			Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, lowJob)

		lowWlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(lowJob.Name, lowJob.UID), Namespace: managerNs.Name}

		managerLowWl := &kueue.Workload{}
		workerLowWorkload := &kueue.Workload{}

		ginkgo.By("Checking that the low-priority workload is created and admitted in the manager cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, lowWlKey, managerLowWl)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(managerLowWl)).To(gomega.BeTrue())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the low-priority workload is created in worker1 and not in worker2, and that its spec matches the manager workload", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, lowWlKey, workerLowWorkload)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(workerLowWorkload)).To(gomega.BeTrue())
				behavioral.ExpectRemoteWorkloadSpec(g, workerLowWorkload, managerLowWl)
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, lowWlKey, &kueue.Workload{})).To(testing.BeNotFoundError())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Updating the worker job status to active=1", func() {
			createdJob := batchv1.Job{}
			startTime := metav1.NewTime(time.Now().Truncate(time.Second))
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(lowJob), &createdJob)).To(gomega.Succeed())
				createdJob.Status.StartTime = &startTime
				createdJob.Status.Active = 1
				createdJob.Status.Ready = new(int32(1))
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdJob)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(lowJob), &createdJob)).To(gomega.Succeed())
				g.Expect(createdJob.Status.StartTime).To(gomega.Equal(&startTime))
				g.Expect(createdJob.Status.Active).To(gomega.Equal(int32(1)))
				g.Expect(ptr.Deref(createdJob.Status.Ready, 0)).To(gomega.Equal(int32(1)))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		highJob := testingjob.MakeJob("high-job", managerNs.Name).
			WorkloadPriorityClass(managerHighWPC.Name).
			Queue(kueue.LocalQueueName(managerLq.Name)).
			RequestAndLimit(corev1.ResourceCPU, "1").
			RequestAndLimit(corev1.ResourceMemory, "2G").
			Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, highJob)

		ginkgo.By("Checking that the manager job is suspended", func() {
			createdJob := batchv1.Job{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(lowJob), &createdJob)).To(gomega.Succeed())
				g.Expect(createdJob.Spec.Suspend).To(gomega.Equal(new(true)))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the worker job is suspended", func() {
			createdJob := batchv1.Job{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(lowJob), &createdJob)).To(gomega.Succeed())
				g.Expect(createdJob.Spec.Suspend).To(gomega.Equal(new(true)))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Updating the worker job status to active=0", func() {
			createdJob := batchv1.Job{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(lowJob), &createdJob)).To(gomega.Succeed())
				createdJob.Status.Active = 0
				createdJob.Status.Ready = new(int32(0))
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdJob)).To(gomega.Succeed())
			}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(lowJob), &createdJob)).To(gomega.Succeed())
				g.Expect(createdJob.Status.Active).To(gomega.Equal(int32(0)))
				g.Expect(ptr.Deref(createdJob.Status.Ready, 0)).To(gomega.Equal(int32(0)))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the low-priority workload has condition QuotaReserved=false in the manager clusters", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, lowWlKey, managerLowWl)).To(gomega.Succeed())
				g.Expect(managerLowWl.Status.Conditions).To(testing.HaveConditionStatusFalse(kueue.WorkloadQuotaReserved))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		highWlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(highJob.Name, highJob.UID), Namespace: managerNs.Name}

		managerHighWl := &kueue.Workload{}
		workerHighWorkload := &kueue.Workload{}

		ginkgo.By("Checking that the high-priority workload is created and admitted in the manager clusters", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, highWlKey, managerHighWl)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(managerHighWl)).To(gomega.BeTrue())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the high-priority workload is created in worker2 and not in worker1, and that its spec matches the manager workload", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, highWlKey, workerHighWorkload)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(workerHighWorkload)).To(gomega.BeTrue())
				behavioral.ExpectRemoteWorkloadSpec(g, workerHighWorkload, managerHighWl)
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, highWlKey, &kueue.Workload{})).To(testing.BeNotFoundError())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the low-priority workload is preempted in the manager cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, lowWlKey, managerLowWl)).To(gomega.Succeed())
				g.Expect(workloadevict.IsEvicted(managerLowWl)).To(gomega.BeTrue())
				g.Expect(managerLowWl.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadPreempted))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the low-priority workload is deleted from the worker clusters", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, lowWlKey, &kueue.Workload{})).To(testing.BeNotFoundError())
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, lowWlKey, &kueue.Workload{})).To(testing.BeNotFoundError())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("should update the workload’s priority class on the worker if it changes on the manager", func() {
		job := testingjob.MakeJob("job", managerNs.Name).
			WorkloadPriorityClass(managerHighWPC.Name).
			Queue(kueue.LocalQueueName(managerLq.Name)).
			RequestAndLimit(corev1.ResourceCPU, "2").
			RequestAndLimit(corev1.ResourceMemory, "1G").
			Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)

		wlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

		managerWl := &kueue.Workload{}
		workerWl := &kueue.Workload{}

		ginkgo.By("Checking that the workload is created and admitted in the manager cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlKey, managerWl)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(managerWl)).To(gomega.BeTrue())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the workload is created in worker1 and not in worker2, and that its spec matches the manager workload", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlKey, workerWl)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(workerWl)).To(gomega.BeTrue())
				behavioral.ExpectRemoteWorkloadSpec(g, workerWl, managerWl)
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlKey, &kueue.Workload{})).To(testing.BeNotFoundError())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Setting a low priority class on the job", func() {
			createdJob := &batchv1.Job{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
				createdJob.Labels[constants.WorkloadPriorityClassLabel] = managerLowWPC.Name
				g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, createdJob)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the workload priority class updated in the worker", func() {
			behavioral.ExpectWorkloadsWithWorkloadPriority(worker1TestCluster.ctx, worker1TestCluster.client, managerLowWPC.Name, managerLowWPC.Value, wlKey)
		})
	})

	ginkgo.It("should update the workload’s priority boost on the worker in place if it changes on the manager", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.PriorityBoost, true)

		job := testingjob.MakeJob("job", managerNs.Name).
			Queue(kueue.LocalQueueName(managerLq.Name)).
			RequestAndLimit(corev1.ResourceCPU, "2").
			RequestAndLimit(corev1.ResourceMemory, "1G").
			Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)

		wlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

		managerWl := &kueue.Workload{}
		workerWl := &kueue.Workload{}

		ginkgo.By("Checking that the workload is created and admitted in the manager cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlKey, managerWl)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(managerWl)).To(gomega.BeTrue())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the workload is created and admitted in worker1", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlKey, workerWl)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(workerWl)).To(gomega.BeTrue())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
		workerWlUID := workerWl.UID

		ginkgo.By("Setting a priority boost on the manager workload", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlKey, managerWl)).To(gomega.Succeed())
				managerWl.Spec.PriorityBoost = new(int32(-100))
				g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, managerWl)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the priority boost is updated on the same worker workload", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlKey, workerWl)).To(gomega.Succeed())
				g.Expect(workerWl.Spec.PriorityBoost).To(gomega.HaveValue(gomega.Equal(int32(-100))))
				g.Expect(workerWl.UID).To(gomega.Equal(workerWlUID))
				g.Expect(workload.IsAdmitted(workerWl)).To(gomega.BeTrue())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlKey, workerWl)).To(gomega.Succeed())
				g.Expect(workerWl.Spec.PriorityBoost).To(gomega.HaveValue(gomega.Equal(int32(-100))))
				g.Expect(workerWl.UID).To(gomega.Equal(workerWlUID))
				g.Expect(workload.IsAdmitted(workerWl)).To(gomega.BeTrue())
			}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.By("Setting a different priority boost on the worker workload", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlKey, workerWl)).To(gomega.Succeed())
				workerWl.Spec.PriorityBoost = new(int32(50))
				g.Expect(worker1TestCluster.client.Update(worker1TestCluster.ctx, workerWl)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the priority boost of the manager workload is restored on the same worker workload", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlKey, workerWl)).To(gomega.Succeed())
				g.Expect(workerWl.Spec.PriorityBoost).To(gomega.HaveValue(gomega.Equal(int32(-100))))
				g.Expect(workerWl.UID).To(gomega.Equal(workerWlUID))
				g.Expect(workload.IsAdmitted(workerWl)).To(gomega.BeTrue())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlKey, workerWl)).To(gomega.Succeed())
				g.Expect(workerWl.Spec.PriorityBoost).To(gomega.HaveValue(gomega.Equal(int32(-100))))
				g.Expect(workerWl.UID).To(gomega.Equal(workerWlUID))
				g.Expect(workload.IsAdmitted(workerWl)).To(gomega.BeTrue())
			}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("MultiKueueOrchestratedPreemption is enabled", func() {
		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MultiKueueOrchestratedPreemption, true)
		})

		ginkgo.It("Should treat manager-level and worker-level preemption gates independently", func() {
			job := testingjob.MakeJob("job-with-gates", managerNs.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "0.1").
				RequestAndLimit(corev1.ResourceMemory, "0.1G").
				TerminationGracePeriod(1).
				Obj()

			ginkgo.By("Creating the job", func() {
				behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)
			})

			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

			managerWl := &kueue.Workload{}
			ginkgo.By("Waiting for the workload to be created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			dummyGateName := "dummy-gate"
			ginkgo.By("Adding a dummy preemption gate to the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
					managerWl.Spec.PreemptionGates = []kueue.PreemptionGate{{Name: dummyGateName}}
					g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, managerWl)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			workerWl := &kueue.Workload{}
			ginkgo.By("Checking that the workload gets admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
					selectedWorker := behavioral.GetClientForSelectedWorkerCluster(
						g,
						managerWl,
						behavioral.DefaultClusterInfosForTests(
							worker1TestCluster.ctx,
							worker1TestCluster.client,
							worker2TestCluster.ctx,
							worker2TestCluster.client,
						)...,
					)

					// When the gates are not treated independently, a mismatch between the gates will cause
					// the workload to never get admitted and be continuously recreated.
					// See: https://github.com/kubernetes-sigs/kueue/issues/12543
					g.Expect(selectedWorker.Client.Get(selectedWorker.Ctx, wlLookupKey, workerWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(workerWl)).To(gomega.BeTrue())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the remote workload does not contain the manager workload's dummy gate", func() {
				gomega.Expect(workerWl.Spec.PreemptionGates).To(gomega.Not(gomega.ContainElement(kueue.PreemptionGate{Name: dummyGateName})))
			})
		})

		ginkgo.It("should not trigger concurrent preemptions", func() {
			// Fits only in worker1
			lowJob1 := testingjob.MakeJob("low-job1", managerNs.Name).
				WorkloadPriorityClass(managerLowWPC.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "0.1").
				RequestAndLimit(corev1.ResourceMemory, "0.1G").
				RequestAndLimit(corev1.ResourceEphemeralStorage, "15G").
				Obj()
			behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, lowJob1)

			lowWlKey1 := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(lowJob1.Name, lowJob1.UID), Namespace: managerNs.Name}
			managerLowWl1 := &kueue.Workload{}
			workerLowWl1 := &kueue.Workload{}

			ginkgo.By("Checking that the first low-priority workload is created and admitted in the manager cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, lowWlKey1, managerLowWl1)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(managerLowWl1)).To(gomega.BeTrue())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the first low-priority workload is created in worker1 and not in worker2", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, lowWlKey1, workerLowWl1)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(workerLowWl1)).To(gomega.BeTrue())

					g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, lowWlKey1, &kueue.Workload{})).To(testing.BeNotFoundError())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			// Fits only in worker2
			lowJob2 := testingjob.MakeJob("low-job2", managerNs.Name).
				WorkloadPriorityClass(managerLowWPC.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "0.1").
				RequestAndLimit(corev1.ResourceMemory, "1.5G").
				RequestAndLimit(corev1.ResourceEphemeralStorage, "5G").
				Obj()
			behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, lowJob2)

			lowWlKey2 := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(lowJob2.Name, lowJob2.UID), Namespace: managerNs.Name}
			managerLowWl2 := &kueue.Workload{}
			workerLowWl2 := &kueue.Workload{}

			ginkgo.By("Checking that the second low-priority workload is created and admitted in the manager cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, lowWlKey2, managerLowWl2)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(managerLowWl2)).To(gomega.BeTrue())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the second low-priority workload is created in worker2 and not in worker1", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, lowWlKey2, workerLowWl2)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(workerLowWl1)).To(gomega.BeTrue())

					g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, lowWlKey2, &kueue.Workload{})).To(testing.BeNotFoundError())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
			// Can fit in both workers after preemptions
			highJob := testingjob.MakeJob("high-job", managerNs.Name).
				WorkloadPriorityClass(managerHighWPC.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "0.1").
				RequestAndLimit(corev1.ResourceMemory, "0.1G").
				RequestAndLimit(corev1.ResourceEphemeralStorage, "5G").
				Obj()
			behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, highJob)

			highWlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(highJob.Name, highJob.UID), Namespace: managerNs.Name}

			managerHighWl := &kueue.Workload{}

			ginkgo.By("Checking that the high-priority workload is created in the manager cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, highWlKey, managerHighWl)).To(gomega.Succeed())
					g.Expect(managerHighWl.Spec.QueueName).To(gomega.BeEquivalentTo(managerLq.Name))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			var evictedWlKey types.NamespacedName
			var unaffectedWlKey types.NamespacedName
			var unaffectedWorkerCluster cluster

			ginkgo.By("Checking that the high-priority workload is admitted in one of the workers", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					worker1HighWorkload := &kueue.Workload{}
					worker2HighWorkload := &kueue.Workload{}

					worker1Error := worker1TestCluster.client.Get(worker1TestCluster.ctx, highWlKey, worker1HighWorkload)
					worker2Error := worker2TestCluster.client.Get(worker2TestCluster.ctx, highWlKey, worker2HighWorkload)

					g.Expect(worker1Error == nil).NotTo(gomega.Equal(worker2Error == nil))

					if worker1Error == nil {
						g.Expect(worker1HighWorkload.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
						evictedWlKey = lowWlKey1
						unaffectedWlKey = lowWlKey2
						unaffectedWorkerCluster = worker2TestCluster
					} else {
						g.Expect(worker2HighWorkload.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
						evictedWlKey = lowWlKey2
						unaffectedWlKey = lowWlKey1
						unaffectedWorkerCluster = worker1TestCluster
					}
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the non-evicted workload remains Admitted", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					unaffectedWl := &kueue.Workload{}

					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, unaffectedWlKey, unaffectedWl)).To(gomega.Succeed())
					g.Expect(unaffectedWl.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
					g.Expect(workloadevict.IsEvicted(unaffectedWl)).To(gomega.BeFalse())

					g.Expect(unaffectedWorkerCluster.client.Get(unaffectedWorkerCluster.ctx, unaffectedWlKey, unaffectedWl)).To(gomega.Succeed())
					g.Expect(unaffectedWl.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
					g.Expect(workloadevict.IsEvicted(unaffectedWl)).To(gomega.BeFalse())
				}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the evicted workload was requeued successfully", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					evictedWl := &kueue.Workload{}

					// Evicted workload was requeued on the manager
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, evictedWlKey, evictedWl)).To(gomega.Succeed())
					g.Expect(evictedWl.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadQuotaReserved))
					g.Expect(evictedWl.Status.Conditions).To(testing.HaveConditionStatusFalse(kueue.WorkloadAdmitted))

					// Evicted workload is requeued and pending on worker 1
					g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, evictedWlKey, evictedWl)).To(gomega.Succeed())
					g.Expect(evictedWl.Status.Conditions).To(testing.HaveConditionStatusFalse(kueue.WorkloadQuotaReserved))

					// Evicted workload is requeued and pending on worker 2
					g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, evictedWlKey, evictedWl)).To(gomega.Succeed())
					g.Expect(evictedWl.Status.Conditions).To(testing.HaveConditionStatusFalse(kueue.WorkloadQuotaReserved))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.When("worker ClusterQueues reclaim borrowed quota within a cohort", func() {
			const (
				cohortName    = kueue.CohortReference("team-cohort")
				teamALqName   = "team-a-lq"
				teamBLqName   = "team-b-lq"
				teamBJobCount = 3
			)

			var (
				managerTeamAFlavor *kueue.ResourceFlavor
				managerTeamACq     *kueue.ClusterQueue
				managerTeamALq     *kueue.LocalQueue
			)

			createWorkerCohort := func(c cluster, ns *corev1.Namespace) {
				ginkgo.GinkgoHelper()
				flavor := utiltestingapi.MakeResourceFlavor("team-fl").Obj()
				behavioral.MustCreate(c.ctx, c.client, flavor)
				teamACq := utiltestingapi.MakeClusterQueue("team-a").
					Cohort(cohortName).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "4").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					}).
					Obj()
				teamBCq := utiltestingapi.MakeClusterQueue("team-b").
					Cohort(cohortName).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "0").Obj()).
					Obj()
				behavioral.CreateClusterQueuesAndWaitForActive(c.ctx, c.client, teamACq, teamBCq)
				behavioral.CreateLocalQueuesAndWaitForActive(c.ctx, c.client,
					utiltestingapi.MakeLocalQueue(teamALqName, ns.Name).ClusterQueue(teamACq.Name).Obj(),
					utiltestingapi.MakeLocalQueue(teamBLqName, ns.Name).ClusterQueue(teamBCq.Name).Obj(),
				)
				// DeferCleanup runs after the outer AfterEach deleted the namespaces,
				// so the ClusterQueues no longer hold workloads.
				ginkgo.DeferCleanup(func() {
					behavioral.ExpectObjectToBeDeleted(c.ctx, c.client, teamACq, true)
					behavioral.ExpectObjectToBeDeleted(c.ctx, c.client, teamBCq, true)
					behavioral.ExpectObjectToBeDeleted(c.ctx, c.client, flavor, true)
				})
			}

			// createBorrowingTeamBJobs fills the team-b ClusterQueue of a worker with
			// jobs that borrow cpu from the team-a nominal quota.
			createBorrowingTeamBJobs := func(c cluster, ns *corev1.Namespace, prefix string) []types.NamespacedName {
				ginkgo.GinkgoHelper()
				keys := make([]types.NamespacedName, 0, teamBJobCount)
				for i := range teamBJobCount {
					job := testingjob.MakeJob(fmt.Sprintf("%s-%d", prefix, i), ns.Name).
						Queue(teamBLqName).
						RequestAndLimit(corev1.ResourceCPU, "1").
						Obj()
					behavioral.MustCreate(c.ctx, c.client, job)
					keys = append(keys, types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: ns.Name})
				}
				gomega.Eventually(func(g gomega.Gomega) {
					for _, key := range keys {
						wl := &kueue.Workload{}
						g.Expect(c.client.Get(c.ctx, key, wl)).To(gomega.Succeed())
						g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
					}
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				return keys
			}

			ginkgo.BeforeEach(func() {
				managerTeamAFlavor = utiltestingapi.MakeResourceFlavor("team-fl").Obj()
				behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerTeamAFlavor)
				// The manager quota is the sum of the worker team-a nominal quotas.
				managerTeamACq = utiltestingapi.MakeClusterQueue("team-a").
					AdmissionChecks(kueue.AdmissionCheckReference(multiKueueAC.Name)).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(managerTeamAFlavor.Name).Resource(corev1.ResourceCPU, "8").Obj()).
					Obj()
				behavioral.CreateClusterQueuesAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, managerTeamACq)
				managerTeamALq = utiltestingapi.MakeLocalQueue(teamALqName, managerNs.Name).ClusterQueue(managerTeamACq.Name).Obj()
				behavioral.CreateLocalQueuesAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, managerTeamALq)

				createWorkerCohort(worker1TestCluster, worker1Ns)
				createWorkerCohort(worker2TestCluster, worker2Ns)
			})

			// The manager ClusterQueue references the shared AdmissionCheck, so it has to
			// be deleted before the outer AfterEach deletes that AdmissionCheck.
			ginkgo.AfterEach(func() {
				gomega.Expect(behavioral.DeleteAllJobsOfAnyTypeInNamespace(managerTestCluster.ctx, managerTestCluster.client, managerNs)).To(gomega.Succeed())
				gomega.Expect(behavioral.DeleteWorkloadsInNamespace(managerTestCluster.ctx, managerTestCluster.client, managerNs)).To(gomega.Succeed())
				behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerTeamACq, true)
				behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerTeamAFlavor, true)
			})

			ginkgo.It("should reclaim borrowed quota on exactly one worker when the manager quota fits but no single worker has room", func() {
				var worker1TeamBKeys, worker2TeamBKeys []types.NamespacedName
				ginkgo.By("Filling each worker with team-b jobs that borrow 3 of the 4 cpus of team-a", func() {
					worker1TeamBKeys = createBorrowingTeamBJobs(worker1TestCluster, worker1Ns, "team-b-w1")
					worker2TeamBKeys = createBorrowingTeamBJobs(worker2TestCluster, worker2Ns, "team-b-w2")
				})

				// 4 cpus fit in the manager quota of 8, but only 1 cpu is free on each worker.
				job := testingjob.MakeJob("team-a-job", managerNs.Name).
					Queue(teamALqName).
					RequestAndLimit(corev1.ResourceCPU, "4").
					Obj()
				ginkgo.By("Creating the team-a job on the manager", func() {
					behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)
				})
				wlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

				var reclaimedKeys, untouchedKeys []types.NamespacedName
				var reclaimedCluster, untouchedCluster cluster
				ginkgo.By("Checking that the workload is admitted on exactly one worker", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						managerWl := &kueue.Workload{}
						g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlKey, managerWl)).To(gomega.Succeed())
						g.Expect(workload.IsAdmitted(managerWl)).To(gomega.BeTrue())
						selected := behavioral.GetClientForSelectedWorkerCluster(g, managerWl,
							behavioral.DefaultClusterInfosForTests(
								worker1TestCluster.ctx, worker1TestCluster.client,
								worker2TestCluster.ctx, worker2TestCluster.client,
							)...,
						)
						workerWl := &kueue.Workload{}
						g.Expect(selected.Client.Get(selected.Ctx, wlKey, workerWl)).To(gomega.Succeed())
						g.Expect(workload.IsAdmitted(workerWl)).To(gomega.BeTrue())
						if selected.Name == workerCluster1.Name {
							reclaimedKeys, untouchedKeys = worker1TeamBKeys, worker2TeamBKeys
							reclaimedCluster, untouchedCluster = worker1TestCluster, worker2TestCluster
						} else {
							reclaimedKeys, untouchedKeys = worker2TeamBKeys, worker1TeamBKeys
							reclaimedCluster, untouchedCluster = worker2TestCluster, worker1TestCluster
						}
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Checking that the selected worker preempted its team-b workloads by cohort reclamation", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						for _, key := range reclaimedKeys {
							wl := &kueue.Workload{}
							g.Expect(reclaimedCluster.client.Get(reclaimedCluster.ctx, key, wl)).To(gomega.Succeed())
							g.Expect(wl.Status.Conditions).To(testing.HaveConditionStatusTrueAndReason(kueue.WorkloadPreempted, kueue.InCohortReclamationReason))
							g.Expect(workload.IsAdmitted(wl)).To(gomega.BeFalse())
						}
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Checking that the other worker keeps its team-b workloads admitted", func() {
					gomega.Consistently(func(g gomega.Gomega) {
						for _, key := range untouchedKeys {
							wl := &kueue.Workload{}
							g.Expect(untouchedCluster.client.Get(untouchedCluster.ctx, key, wl)).To(gomega.Succeed())
							g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
							g.Expect(wl.Status.Conditions).NotTo(testing.HaveCondition(kueue.WorkloadPreempted))
						}
						g.Expect(untouchedCluster.client.Get(untouchedCluster.ctx, wlKey, &kueue.Workload{})).To(testing.BeNotFoundError())
					}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
				})
			})
		})
	})
})
