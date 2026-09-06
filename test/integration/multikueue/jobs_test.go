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

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	workloadjobset "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("MultiKueue", ginkgo.Label("area:multikueue", "feature:multikueue"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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

	ginkgo.It("Should run a job on worker if admitted", func() {
		admittedMetricBefore := util.GetMultiKueueWorkloadsAdmittedTotal(f.managerCq, "worker1")
		job := testingjob.MakeJob("job", f.managerNs.Name).
			Queue(kueue.LocalQueueName(f.managerLq.Name)).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: f.managerNs.Name}

		ginkgo.By("setting workload reservation in the management cluster", func() {
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).
				Obj()
			util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission)
		})

		ginkgo.By("checking the workload creation in the worker clusters", func() {
			managerWl := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting workload reservation in worker1, AC state is updated in manager and worker2 wl is removed", func() {
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).
				Obj()
			util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlLookupKey, admission)

			util.ExpectAdmissionCheckStateWithMessage(
				managerTestCluster.ctx, managerTestCluster.client, wlLookupKey,
				f.multiKueueAC.Name,
				kueue.CheckStateReady,
				`The workload was admitted on "worker1"`,
			)
			util.ExpectEventAppeared(managerTestCluster.ctx, managerTestCluster.client, eventsv1.Event{
				Reason: "MultiKueue",
				Type:   corev1.EventTypeNormal,
				Note:   `The workload was admitted on "worker1"`,
			})

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			util.ExpectMultiKueueWorkloadsAdmittedTotalMetric(f.managerCq, "worker1", admittedMetricBefore+1)
		})

		ginkgo.By("finishing the worker job", func() {
			reachedPodsReason := "Reached expected number of succeeded pods"
			finishJobReason := "Job finished successfully"
			now := metav1.Now()
			// completedJobCondition that we will add to the remote job to indicate job completions,
			// and the same condition that we expect to see on the local job status.
			completedJobCondition := batchv1.JobCondition{
				Type:               batchv1.JobComplete,
				Status:             corev1.ConditionTrue,
				LastProbeTime:      now,
				LastTransitionTime: now,
				Message:            finishJobReason,
			}

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
					completedJobCondition)
				createdJob.Status.Succeeded = 1
				createdJob.Status.StartTime = new(now)
				createdJob.Status.CompletionTime = new(now)
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, finishJobReason)

			// Assert job complete condition.
			localJob := &batchv1.Job{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(job), localJob)).To(gomega.Succeed())
			gomega.Expect(localJob.Status.Conditions).Should(gomega.ContainElement(gomega.WithTransform(func(condition batchv1.JobCondition) batchv1.JobCondition {
				// Compare on all condition attributes excluding Time values.
				condition.LastProbeTime = completedJobCondition.LastProbeTime
				condition.LastTransitionTime = completedJobCondition.LastTransitionTime
				return condition
			}, gomega.Equal(completedJobCondition))))
		})
	})

	ginkgo.It("Should retry a Job whose remote workload finishes OutOfSync instead of finishing the manager workload", func() {
		job := testingjob.MakeJob("job-oos", f.managerNs.Name).
			Queue(kueue.LocalQueueName(f.managerLq.Name)).
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, job)).Should(gomega.Succeed())

		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: f.managerNs.Name}
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).
			Obj()

		ginkgo.By("setting workload reservation on the manager and worker1, AC becomes Ready", func() {
			util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission)
			gomega.Eventually(func(g gomega.Gomega) {
				createdWorkload := &kueue.Workload{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlLookupKey, admission)
			util.ExpectAdmissionCheckStateWithMessage(
				managerTestCluster.ctx, managerTestCluster.client, wlLookupKey,
				f.multiKueueAC.Name, kueue.CheckStateReady, `The workload was admitted on "worker1"`,
			)
		})

		ginkgo.By("finishing the remote workload OutOfSync (simulating a worker-mutated mirrored Job)", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				remoteWl := &kueue.Workload{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, remoteWl)).To(gomega.Succeed())
				apimeta.SetStatusCondition(&remoteWl.Status.Conditions, metav1.Condition{
					Type:    kueue.WorkloadFinished,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadFinishedReasonOutOfSync,
					Message: "The prebuilt workload is out of sync with its user job",
				})
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, remoteWl)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("the manager emits a reset event and does not finish the manager workload", func() {
			resetMsg := `Remote workload on worker cluster "worker1" is out of sync with its user job, resetting for re-dispatch, Previously: "Ready"`
			util.ExpectEventAppeared(managerTestCluster.ctx, managerTestCluster.client, eventsv1.Event{
				Reason: "MultiKueue",
				Type:   corev1.EventTypeWarning,
				Note:   resetMsg,
			})
			gomega.Consistently(func(g gomega.Gomega) {
				createdWorkload := &kueue.Workload{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				if finished := apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadFinished); finished != nil {
					g.Expect(finished.Reason).NotTo(gomega.Equal(kueue.WorkloadFinishedReasonOutOfSync))
				}
			}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should retry a Job whose remote workload finishes OwnerNotFound instead of finishing the manager workload", func() {
		job := testingjob.MakeJob("job-owner-not-found", f.managerNs.Name).
			Queue(kueue.LocalQueueName(f.managerLq.Name)).
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, job)).Should(gomega.Succeed())

		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: f.managerNs.Name}
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).
			Obj()

		ginkgo.By("setting workload reservation on the manager and worker1, AC becomes Ready", func() {
			util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission)
			gomega.Eventually(func(g gomega.Gomega) {
				createdWorkload := &kueue.Workload{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlLookupKey, admission)
			util.ExpectAdmissionCheckStateWithMessage(
				managerTestCluster.ctx, managerTestCluster.client, wlLookupKey,
				f.multiKueueAC.Name, kueue.CheckStateReady, `The workload was admitted on "worker1"`,
			)
		})

		ginkgo.By("finishing the remote workload OwnerNotFound (simulating a worker-side orphan-detection race)", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				remoteWl := &kueue.Workload{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, remoteWl)).To(gomega.Succeed())
				apimeta.SetStatusCondition(&remoteWl.Status.Conditions, metav1.Condition{
					Type:    kueue.WorkloadFinished,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadFinishedReasonOwnerNotFound,
					Message: "The workload's owner no longer exists",
				})
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, remoteWl)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("the manager emits a reset event and does not finish the manager workload", func() {
			resetMsg := `Remote workload on worker cluster "worker1" is out of sync with its user job, resetting for re-dispatch, Previously: "Ready"`
			util.ExpectEventAppeared(managerTestCluster.ctx, managerTestCluster.client, eventsv1.Event{
				Reason: "MultiKueue",
				Type:   corev1.EventTypeWarning,
				Note:   resetMsg,
			})
			gomega.Consistently(func(g gomega.Gomega) {
				createdWorkload := &kueue.Workload{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				if finished := apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadFinished); finished != nil {
					g.Expect(finished.Reason).NotTo(gomega.Equal(kueue.WorkloadFinishedReasonOwnerNotFound))
				}
			}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should run a job on worker if admitted (ManagedBy)", func() {
		job := testingjob.MakeJob("job", f.managerNs.Name).
			ManagedBy(kueue.MultiKueueControllerName).
			Queue(kueue.LocalQueueName(f.managerLq.Name)).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: f.managerNs.Name}

		ginkgo.By("setting workload reservation in the management cluster", func() {
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).
				Obj()
			util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission)
		})

		ginkgo.By("checking the workload creation in the worker clusters", func() {
			managerWl := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting workload reservation in worker1, AC state is updated in manager and worker2 wl is removed", func() {
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).
				Obj()
			util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlLookupKey, admission)

			util.ExpectAdmissionCheckStateWithMessage(
				managerTestCluster.ctx, managerTestCluster.client, wlLookupKey,
				f.multiKueueAC.Name,
				kueue.CheckStateReady,
				`The workload was admitted on "worker1"`,
			)
			util.ExpectEventAppeared(managerTestCluster.ctx, managerTestCluster.client, eventsv1.Event{
				Reason: "MultiKueue",
				Type:   corev1.EventTypeNormal,
				Note:   `The workload was admitted on "worker1"`,
			})

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("waiting for the Job on the manager to be unsuspended", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
				g.Expect(createdJob.Spec.Suspend).To(gomega.Equal(new(false)))
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("updating the worker job status", func() {
			startTime := metav1.NewTime(time.Now().Truncate(time.Second))
			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
				createdJob.Status.StartTime = &startTime
				createdJob.Status.Active = 1
				createdJob.Status.Ready = new(int32(1))
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
				g.Expect(ptr.Deref(createdJob.Status.StartTime, metav1.Time{})).To(gomega.Equal(startTime))
				g.Expect(ptr.Deref(createdJob.Status.Ready, 0)).To(gomega.Equal(int32(1)))
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker job", func() {
			reachedPodsReason := "Reached expected number of succeeded pods"
			finishJobReason := "Job finished successfully"

			now := metav1.Now()
			// completedJobCondition that we will add to the remote job to indicate job completions,
			// and the same condition that we expect to see on the local job status.
			completedJobCondition := batchv1.JobCondition{
				Type:               batchv1.JobComplete,
				Status:             corev1.ConditionTrue,
				LastProbeTime:      now,
				LastTransitionTime: now,
				Message:            finishJobReason,
			}

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
					}, completedJobCondition)
				createdJob.Status.Active = 0
				createdJob.Status.Ready = new(int32(0))
				createdJob.Status.Succeeded = 1
				createdJob.Status.CompletionTime = new(now)
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, finishJobReason)

			// Assert job complete condition.
			localJob := &batchv1.Job{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(job), localJob)).To(gomega.Succeed())
			gomega.Expect(localJob.Status.Conditions).Should(gomega.ContainElement(gomega.WithTransform(func(condition batchv1.JobCondition) batchv1.JobCondition {
				// Compare on all condition attributes excluding Time values.
				condition.LastProbeTime = completedJobCondition.LastProbeTime
				condition.LastTransitionTime = completedJobCondition.LastTransitionTime
				return condition
			}, gomega.Equal(completedJobCondition))))
		})
	})

	ginkgo.It("Should remove the worker's workload and job after reconnect when the managers job and workload are deleted", func() {
		job := testingjob.MakeJob("job", f.managerNs.Name).
			Queue(kueue.LocalQueueName(f.managerLq.Name)).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)
		jobLookupKey := client.ObjectKeyFromObject(job)
		createdJob := &batchv1.Job{}

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: f.managerNs.Name}

		ginkgo.By("setting workload reservation in the management cluster", func() {
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).
				Obj()
			util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission)
		})

		ginkgo.By("checking the workload creation in the worker clusters", func() {
			managerWl := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		restoreConnectionToWorker2 := util.BreakConnection(managerTestCluster.ctx, managerTestCluster.client, f.workerCluster2, managersConfigNamespace.Name)

		ginkgo.By("setting workload reservation in worker1, the job is created in worker1", func() {
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).
				Obj()
			util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlLookupKey, admission)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, jobLookupKey, createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		restoreConnectionToWorker1 := util.BreakConnection(managerTestCluster.ctx, managerTestCluster.client, f.workerCluster1, managersConfigNamespace.Name)

		ginkgo.By("removing the managers job and workload", func() {
			gomega.Expect(managerTestCluster.client.Delete(managerTestCluster.ctx, job)).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(managerTestCluster.client.Delete(managerTestCluster.ctx, createdWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError(), "workload not deleted")
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("the worker objects are still present", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, jobLookupKey, createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		restoreConnectionToWorker2()

		ginkgo.By("the worker2 wl is removed by the garbage collector", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		restoreConnectionToWorker1()

		ginkgo.By("the wl and job are removed on the worker1", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, jobLookupKey, createdJob)).To(utiltesting.BeNotFoundError())
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("should clean up the remote workload on a reconnected worker after the job finishes", framework.SlowSpec, func() {
		job := testingjob.MakeJob("job", f.managerNs.Name).
			Queue(kueue.LocalQueueName(f.managerLq.Name)).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)
		jobLookupKey := client.ObjectKeyFromObject(job)

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: f.managerNs.Name}

		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).
			Obj()

		ginkgo.By("setting workload reservation in the management cluster", func() {
			util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission)
		})

		ginkgo.By("checking the workload creation in the worker clusters", func() {
			managerWl := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		restoreConnectionToWorker1 := util.BreakConnection(managerTestCluster.ctx, managerTestCluster.client, f.workerCluster1, managersConfigNamespace.Name)

		ginkgo.By("setting workload reservation in worker2, the workload is admitted and job is created in worker2", func() {
			util.SetQuotaReservation(worker2TestCluster.ctx, worker2TestCluster.client, wlLookupKey, admission)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker2 job", func() {
			now := metav1.Now()
			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, jobLookupKey, &createdJob)).To(gomega.Succeed())
				createdJob.Status.Conditions = append(createdJob.Status.Conditions,
					batchv1.JobCondition{
						Type:               batchv1.JobSuccessCriteriaMet,
						Status:             corev1.ConditionTrue,
						LastProbeTime:      now,
						LastTransitionTime: now,
						Message:            "Reached expected number of succeeded pods",
					},
					batchv1.JobCondition{
						Type:               batchv1.JobComplete,
						Status:             corev1.ConditionTrue,
						LastProbeTime:      now,
						LastTransitionTime: now,
						Message:            "Job finished successfully",
					})
				createdJob.Status.Succeeded = 1
				createdJob.Status.StartTime = &now
				createdJob.Status.CompletionTime = &now
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("waiting for the manager workload to be finished", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeTrue())
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("the worker2 remote workload is cleaned up", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		restoreConnectionToWorker1()

		ginkgo.By("the worker1 remote workload is cleaned up after reconnect", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should requeue the workload with a delay when the connection to the admitting worker is lost", framework.SlowSpec, func() {
		jobSet := testingjobset.MakeJobSet("job-set", f.managerNs.Name).
			Queue(f.managerLq.Name).
			ManagedBy(kueue.MultiKueueControllerName).
			ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-1",
					Replicas:    1,
					Parallelism: 1,
					Completions: 1,
				}, testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-2",
					Replicas:    3,
					Parallelism: 1,
					Completions: 1,
				},
			).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, jobSet)

		wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: f.managerNs.Name}

		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("replicated-job-1").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("replicated-job-2").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)

		admitWorkloadAndCheckWorkerCopies(f.multiKueueAC.Name, wlLookupKey, admission)

		restoreConnectionToWorker2 := util.BreakConnection(managerTestCluster.ctx, managerTestCluster.client, f.workerCluster2, managersConfigNamespace.Name)

		ginkgo.By("waiting for the local workload admission check state to be set to pending and quotaReservatio removed", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdWorkload := &kueue.Workload{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				acs := admissioncheck.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(f.multiKueueAC.Name))
				g.Expect(acs).To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
					Name:       kueue.AdmissionCheckReference(f.multiKueueAC.Name),
					State:      kueue.CheckStatePending,
					RetryCount: new(int32(1)),
				}, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "Message")))

				g.Expect(createdWorkload.Status.Conditions).ToNot(utiltesting.HaveConditionStatusTrue(kueue.WorkloadQuotaReserved))
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		restoreConnectionToWorker2()

		ginkgo.By("the worker2 wl is removed since the local one no longer has a reservation", func() {
			waitForRemoteWorkloadToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, wlLookupKey, "worker2", util.LongTimeout)
		})
	})

	ginkgo.It("Should not evict admitted workloads when the connection to the admitting worker is briefly lost", framework.SlowSpec, func() {
		keys := admitJobsAndAgeAdmissionCheck(f.managerNs.Name, f.managerLq.Name, f.managerCq.Name, f.multiKueueAC.Name, 3)

		restoreConnection := util.BreakConnection(managerTestCluster.ctx, managerTestCluster.client, f.workerCluster2, managersConfigNamespace.Name)
		restoreConnection()

		ginkgo.By("no admitted workload is evicted after the brief connection loss", func() {
			expectNoEviction(keys, f.multiKueueAC.Name, testingWorkerLostTimeout*2)
		})
	})

	ginkgo.It("Should not immediately evict but eventually retry admitted workloads after a manager restart while the admitting worker is unreachable", framework.SlowSpec, func() {
		keys := admitJobsAndAgeAdmissionCheck(f.managerNs.Name, f.managerLq.Name, f.managerCq.Name, f.multiKueueAC.Name, 3)

		restoreConnection := util.BreakConnection(managerTestCluster.ctx, managerTestCluster.client, f.workerCluster2, managersConfigNamespace.Name)

		ginkgo.By("restarting the manager while worker2 is unreachable", func() {
			managerTestCluster.fwk.StopManager(managerTestCluster.ctx)
			managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
				managerAndMultiKueueSetup(ctx, mgr, 2*time.Second, defaultEnabledIntegrations, config.MultiKueueDispatcherModeAllAtOnce)
			})
		})

		ginkgo.By("the restarted manager re-anchors the grace and does not immediately evict", func() {
			expectNoEviction(keys, f.multiKueueAC.Name, testingWorkerLostTimeout*2/3)
		})

		ginkgo.By("the re-anchored grace is finite, so the workloads are eventually retried", func() {
			expectEventuallyRetried(keys, f.multiKueueAC.Name, testingWorkerLostTimeout*4)
		})

		restoreConnection()
	})

	ginkgo.It("Should redo the admission process once the workload loses Admission in the worker cluster", func() {
		admittedMetricBefore := util.GetMultiKueueWorkloadsAdmittedTotal(f.managerCq, "worker1")
		job := testingjob.MakeJob("job", f.managerNs.Name).
			ManagedBy(kueue.MultiKueueControllerName).
			Queue(kueue.LocalQueueName(f.managerLq.Name)).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: f.managerNs.Name}

		ginkgo.By("setting workload reservation in the management cluster", func() {
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).
				Obj()
			util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission)
		})

		ginkgo.By("checking the workload creation in the worker clusters", func() {
			managerWl := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting workload reservation in worker1, AC state is updated in manager and worker2 wl is removed", func() {
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).
				Obj()
			util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlLookupKey, admission)

			util.ExpectAdmissionCheckStateWithMessage(
				managerTestCluster.ctx, managerTestCluster.client, wlLookupKey,
				f.multiKueueAC.Name,
				kueue.CheckStateReady,
				`The workload was admitted on "worker1"`,
			)
			util.ExpectEventAppeared(managerTestCluster.ctx, managerTestCluster.client, eventsv1.Event{
				Reason: "MultiKueue",
				Type:   corev1.EventTypeNormal,
				Note:   `The workload was admitted on "worker1"`,
			})

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			util.ExpectMultiKueueWorkloadsAdmittedTotalMetric(f.managerCq, "worker1", admittedMetricBefore+1)
		})

		ginkgo.By("preempting workload in worker1", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdWorkload := &kueue.Workload{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(workload.SetConditionAndUpdate(worker1TestCluster.ctx, worker1TestCluster.client, createdWorkload, kueue.WorkloadEvicted, metav1.ConditionTrue, kueue.WorkloadEvictedByPreemption, "By test", "evict", util.RealClock)).
					To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("check manager's workload ClusterName reset", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				managerWl := &kueue.Workload{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
				g.Expect(managerWl.Status.ClusterName).To(gomega.BeNil())
				g.Expect(managerWl.Status.AdmissionChecks).To(gomega.ContainElement(gomega.BeComparableTo(
					kueue.AdmissionCheckState{
						Name:    kueue.AdmissionCheckReference(f.multiKueueAC.Name),
						State:   kueue.CheckStatePending,
						Message: "Reset to Pending after eviction. Previously: Retry",
					},
					cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "PodSetUpdates", "RetryCount"))))
				g.Expect(managerWl.Status.Conditions).To(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:   kueue.WorkloadRequeued,
						Status: metav1.ConditionTrue,
					}, util.IgnoreConditionTimestampsAndObservedGeneration, cmpopts.IgnoreFields(metav1.Condition{}, "Reason", "Message"))))
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting workload reservation in the management cluster again", func() {
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).
				Obj()
			util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission)
		})

		ginkgo.By("checking the workload admission process started again", func() {
			managerWl := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				createdWorkload := &kueue.Workload{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("re-admitting the workload in worker1, the admitted metric counts the second admission", func() {
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).
				Obj()
			util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlLookupKey, admission)

			util.ExpectAdmissionCheckStateWithMessage(
				managerTestCluster.ctx, managerTestCluster.client, wlLookupKey,
				f.multiKueueAC.Name,
				kueue.CheckStateReady,
				`The workload was admitted on "worker1"`,
			)
			util.ExpectMultiKueueWorkloadsAdmittedTotalMetric(f.managerCq, "worker1", admittedMetricBefore+2)
		})
	})

	ginkgo.When("an additional admission check covering only some flavors supported by the cluster queue is present", func() {
		var (
			additionalAc *kueue.AdmissionCheck
			flavor1      *kueue.ResourceFlavor
			flavor2      *kueue.ResourceFlavor
		)
		ginkgo.BeforeEach(func() {
			flavor1 = utiltestingapi.MakeResourceFlavor("flavor1").Obj()
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, flavor1)).Should(gomega.Succeed())

			flavor2 = utiltestingapi.MakeResourceFlavor("flavor2-will-not-be-assigned").Obj()
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, flavor2)).Should(gomega.Succeed())

			additionalAc = utiltestingapi.MakeAdmissionCheck("non-mk-ac").
				ControllerName(kueue.MultiKueueControllerName).
				Obj()
			util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, additionalAc)
			util.SetAdmissionCheckActive(managerTestCluster.ctx, managerTestCluster.client, additionalAc, metav1.ConditionTrue)

			flavor1Quotas := *utiltestingapi.MakeFlavorQuotas(flavor1.Name).Resource(corev1.ResourceCPU, "10").Obj()
			flavor2Quotas := *utiltestingapi.MakeFlavorQuotas(flavor2.Name).Resource(corev1.ResourceMemory, "100Gi").Obj()
			resourceGroups := []kueue.ResourceGroup{
				utiltestingapi.ResourceGroup(flavor1Quotas),
				utiltestingapi.ResourceGroup(flavor2Quotas),
			}

			rule1 := *utiltestingapi.MakeAdmissionCheckStrategyRule(
				kueue.AdmissionCheckReference(f.multiKueueAC.Name),
				kueue.ResourceFlavorReference(flavor1.Name),
				kueue.ResourceFlavorReference(flavor2.Name),
			).Obj()
			rule2 := *utiltestingapi.MakeAdmissionCheckStrategyRule(
				kueue.AdmissionCheckReference(additionalAc.Name),
				kueue.ResourceFlavorReference(flavor2.Name),
			).Obj()
			acRules := []kueue.AdmissionCheckStrategyRule{
				rule1,
				rule2,
			}

			gomega.Eventually(func(g gomega.Gomega) {
				k8sClient := managerTestCluster.client
				ctx := managerTestCluster.ctx
				updatedCq := &kueue.ClusterQueue{}
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(f.managerCq), updatedCq)).Should(gomega.Succeed())

				updatedCq.Spec.ResourceGroups = resourceGroups
				updatedCq.Spec.AdmissionChecksStrategy = &kueue.AdmissionChecksStrategy{
					AdmissionChecks: acRules,
				}
				gomega.Expect(k8sClient.Update(ctx, updatedCq)).Should(gomega.Succeed())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, f.managerCq, true)
			util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, flavor1, true)
			util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, flavor2, true)
			util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, additionalAc, true)
		})

		ginkgo.It("should assign the multikueue admission check even before admitted", framework.SlowSpec, func() {
			jobSet := testingjobset.MakeJobSet("job-set", f.managerNs.Name).
				Queue(f.managerLq.Name).
				ManagedBy(kueue.MultiKueueControllerName).
				ReplicatedJobs(
					testingjobset.ReplicatedJobRequirements{
						Name:        "replicated-job-1",
						Replicas:    1,
						Parallelism: 1,
						Completions: 1,
					}, testingjobset.ReplicatedJobRequirements{
						Name:        "replicated-job-2",
						Replicas:    3,
						Parallelism: 1,
						Completions: 1,
					},
				).
				Obj()
			util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, jobSet)

			wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: f.managerNs.Name}
			ginkgo.By("ensuring multikueue admission check reference is present and in pending state", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdWorkload := &kueue.Workload{}
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					acs := admissioncheck.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(f.multiKueueAC.Name))
					g.Expect(acs).To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
						Name:  kueue.AdmissionCheckReference(f.multiKueueAC.Name),
						State: kueue.CheckStatePending,
					}, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "Message", "RetryCount")))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should keep multikueue admission check reference and properly clean up the remote workload when the connection to an admitting worker is lost", framework.SlowSpec, func() {
			jobSet := testingjobset.MakeJobSet("job-set", f.managerNs.Name).
				Queue(f.managerLq.Name).
				ManagedBy(kueue.MultiKueueControllerName).
				ReplicatedJobs(
					testingjobset.ReplicatedJobRequirements{
						Name:        "replicated-job-1",
						Replicas:    1,
						Parallelism: 1,
						Completions: 1,
					}, testingjobset.ReplicatedJobRequirements{
						Name:        "replicated-job-2",
						Replicas:    3,
						Parallelism: 1,
						Completions: 1,
					},
				).
				Obj()
			util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, jobSet)

			wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: f.managerNs.Name}

			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).PodSets(
				kueue.PodSetAssignment{
					Name: "replicated-job-1",
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: kueue.ResourceFlavorReference(flavor1.Name),
					},
					ResourceUsage: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
					},
				}, kueue.PodSetAssignment{
					Name: "replicated-job-2",
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: kueue.ResourceFlavorReference(flavor1.Name),
					},
					ResourceUsage: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
					},
				},
			)

			admitWorkloadAndCheckWorkerCopies(f.multiKueueAC.Name, wlLookupKey, admission)

			restoreConnectionToWorker2 := util.BreakConnection(managerTestCluster.ctx, managerTestCluster.client, f.workerCluster2, managersConfigNamespace.Name)

			ginkgo.By("waiting for quotaReservation to be removed", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdWorkload := &kueue.Workload{}
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Status.Conditions).ToNot(utiltesting.HaveConditionStatusTrue(kueue.WorkloadQuotaReserved))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("ensuring multikueue admission check reference is present and in pending state after quota reservation is gone", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdWorkload := &kueue.Workload{}
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					acs := admissioncheck.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(f.multiKueueAC.Name))
					g.Expect(acs).To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
						Name:       kueue.AdmissionCheckReference(f.multiKueueAC.Name),
						State:      kueue.CheckStatePending,
						RetryCount: new(int32(1)),
					}, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "Message")))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			restoreConnectionToWorker2()

			ginkgo.By("the worker2 wl is removed since the local one no longer has a reservation", func() {
				waitForRemoteWorkloadToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, wlLookupKey, "worker2", util.LongTimeout)
			})
		})
	})
})
