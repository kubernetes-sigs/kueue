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
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe("MultiKueue Reconnect", ginkgo.Label("area:multikueue", "feature:multikueue"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var f *multiKueueFixture

	ginkgo.BeforeAll(func() {
		managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
			managerAndMultiKueueSetup(ctx, mgr, 2*time.Second, defaultEnabledIntegrations, config.MultiKueueDispatcherModeAllAtOnce)
		})
	})

	ginkgo.AfterAll(func() {
		managerTestCluster.fwk.StopManager(managerTestCluster.ctx)
	})

	ginkgo.BeforeEach(func() {
		f = setupMultiKueueFixture()
	})

	ginkgo.AfterEach(func() {
		f.teardown()
	})

	ginkgo.It("Should resync workloads after worker reconnects", func() {
		job := testingjob.MakeJob("reconnect-job", f.managerNs.Name).
			Queue(kueue.LocalQueueName(f.managerLq.Name)).
			Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)

		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: f.managerNs.Name}
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).
			Obj()

		ginkgo.By("admitting workload on manager and worker1", func() {
			integration.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, &kueue.Workload{})).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			integration.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlLookupKey, admission)
			behavioral.ExpectAdmissionCheckStateWithMessage(
				managerTestCluster.ctx, managerTestCluster.client, wlLookupKey,
				f.multiKueueAC.Name, kueue.CheckStateReady, `The workload was admitted on "worker1"`,
			)
		})

		restoreConnectionToWorker1 := behavioral.BreakConnection(managerTestCluster.ctx, managerTestCluster.client, f.workerCluster1, managersConfigNamespace.Name)

		finishJobReason := "Job finished successfully"
		ginkgo.By("finishing the worker job while disconnected", func() {
			reachedPodsReason := "Reached expected number of succeeded pods"
			now := metav1.Now()
			gomega.Eventually(func(g gomega.Gomega) {
				workerJob := &batchv1.Job{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(job), workerJob)).To(gomega.Succeed())
				workerJob.Status.Conditions = append(workerJob.Status.Conditions,
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
					},
				)
				workerJob.Status.Succeeded = 1
				workerJob.Status.StartTime = new(now)
				workerJob.Status.CompletionTime = new(now)
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, workerJob)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				workerWl := &kueue.Workload{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, workerWl)).To(gomega.Succeed())
				g.Expect(apimeta.FindStatusCondition(workerWl.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeComparableTo(&metav1.Condition{
					Type:    kueue.WorkloadFinished,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadFinishedReasonSucceeded,
					Message: finishJobReason,
				}, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying manager workload does not have Finished condition while disconnected", func() {
			managerWl := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
			gomega.Expect(apimeta.FindStatusCondition(managerWl.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeNil())
		})

		ginkgo.By("verifying the manager remains admitted while disconnected", func() {
			behavioral.ExpectAdmissionCheckStateWithMessage(
				managerTestCluster.ctx, managerTestCluster.client, wlLookupKey,
				f.multiKueueAC.Name, kueue.CheckStateReady, `The workload was admitted on "worker1"`,
			)
		})

		ginkgo.By("restoring connection to worker1", func() {
			restoreConnectionToWorker1()
		})

		ginkgo.By("verifying manager workload receives Finished state promptly after reconnect", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				managerWl := &kueue.Workload{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
				g.Expect(apimeta.FindStatusCondition(managerWl.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeComparableTo(&metav1.Condition{
					Type:    kueue.WorkloadFinished,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadFinishedReasonSucceeded,
					Message: finishJobReason,
				}, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
			}, testingWorkerLostTimeout*2/3, behavioral.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should resync workloads after worker reconnects when remote workload was deleted", func() {
		job := testingjob.MakeJob("reconnect-deleted-job", f.managerNs.Name).
			Queue(kueue.LocalQueueName(f.managerLq.Name)).
			Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)

		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: f.managerNs.Name}
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).
			Obj()

		ginkgo.By("admitting workload on manager and worker1", func() {
			integration.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, &kueue.Workload{})).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			integration.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlLookupKey, admission)
			behavioral.ExpectAdmissionCheckStateWithMessage(
				managerTestCluster.ctx, managerTestCluster.client, wlLookupKey,
				f.multiKueueAC.Name, kueue.CheckStateReady, `The workload was admitted on "worker1"`,
			)
		})

		restoreConnectionToWorker1 := behavioral.BreakConnection(managerTestCluster.ctx, managerTestCluster.client, f.workerCluster1, managersConfigNamespace.Name)

		ginkgo.By("deleting the worker job and workload while disconnected", func() {
			workerJob := &batchv1.Job{}
			gomega.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(job), workerJob)).To(gomega.Succeed())
			gomega.Expect(worker1TestCluster.client.Delete(worker1TestCluster.ctx, workerJob)).To(gomega.Succeed())

			workerWl := &kueue.Workload{}
			gomega.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, workerWl)).To(gomega.Succeed())
			gomega.Expect(worker1TestCluster.client.Delete(worker1TestCluster.ctx, workerWl)).To(gomega.Succeed())
		})

		ginkgo.By("verifying the manager remains admitted while disconnected", func() {
			behavioral.ExpectAdmissionCheckStateWithMessage(
				managerTestCluster.ctx, managerTestCluster.client, wlLookupKey,
				f.multiKueueAC.Name, kueue.CheckStateReady, `The workload was admitted on "worker1"`,
			)
		})

		ginkgo.By("restoring connection to worker1", func() {
			restoreConnectionToWorker1()
		})

		ginkgo.By("verifying manager workload is evicted promptly after reconnect", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				managerWl := &kueue.Workload{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
				evictedCond := apimeta.FindStatusCondition(managerWl.Status.Conditions, kueue.WorkloadEvicted)
				g.Expect(evictedCond).NotTo(gomega.BeNil())
				g.Expect(evictedCond.Status).To(gomega.Equal(metav1.ConditionTrue))
				g.Expect(evictedCond.Reason).To(gomega.Equal(kueue.WorkloadEvictedByAdmissionCheck))
				g.Expect(evictedCond.Message).To(gomega.ContainSubstring("Admitting remote no longer exists"))
			}, testingWorkerLostTimeout*2/3, behavioral.Interval).Should(gomega.Succeed())
		})
	})
})
