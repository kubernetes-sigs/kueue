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

	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

// We test the interoperability of the Cluster Queues, where one is dedicated to manager Non-MultiKueue workloads
// and the other one to MultiKueue workloads, both sharing the same namespace.
// This will be tested on both manager and one of the worker clusters.
// We assume this type of Cluster role sharing is possible.
var _ = ginkgo.Describe("MultiKueue Cluster Role Sharing", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		managerNs *corev1.Namespace
		worker1Ns *corev1.Namespace
		worker2Ns *corev1.Namespace

		managerMultiKueueSecret1 *corev1.Secret
		managerMultiKueueSecret2 *corev1.Secret
		workerCluster1           *kueue.MultiKueueCluster
		workerCluster2           *kueue.MultiKueueCluster
		managerMultiKueueConfig  *kueue.MultiKueueConfig

		multiKueueAC *kueue.AdmissionCheck
		managerAC    *kueue.AdmissionCheck
		worker1AC    *kueue.AdmissionCheck

		// MultiKueue Cluster Queues and Local Queues
		managerMkCq *kueue.ClusterQueue
		managerMkLq *kueue.LocalQueue
		worker1MkCq *kueue.ClusterQueue
		worker1MkLq *kueue.LocalQueue
		worker2MkCq *kueue.ClusterQueue
		worker2MkLq *kueue.LocalQueue

		// Non-MultiKueue Cluster Queues and Local Queues
		managerCq *kueue.ClusterQueue
		managerLq *kueue.LocalQueue
		worker1Cq *kueue.ClusterQueue
		worker1Lq *kueue.LocalQueue
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

		// MultiKueue setup
		ginkgo.By("Creating and initializing MultiKueue specific resources", func() {
			multiKueueAC = utiltestingapi.MakeAdmissionCheck("ac1").
				ControllerName(kueue.MultiKueueControllerName).
				Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", managerMultiKueueConfig.Name).
				Obj()
			util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, multiKueueAC)

			ginkgo.By("wait for check active", func() {
				updatedAc := kueue.AdmissionCheck{}
				acKey := client.ObjectKeyFromObject(multiKueueAC)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, acKey, &updatedAc)).To(gomega.Succeed())
					g.Expect(updatedAc.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.AdmissionCheckActive))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			managerMkCq = utiltestingapi.MakeClusterQueue("q1").
				AdmissionChecks(kueue.AdmissionCheckReference(multiKueueAC.Name)).
				Obj()
			util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerMkCq)
			util.ExpectClusterQueuesToBeActive(managerTestCluster.ctx, managerTestCluster.client, managerMkCq)

			managerMkLq = utiltestingapi.MakeLocalQueue(managerMkCq.Name, managerNs.Name).ClusterQueue(managerMkCq.Name).Obj()
			util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerMkLq)
			util.ExpectLocalQueuesToBeActive(managerTestCluster.ctx, managerTestCluster.client, managerMkLq)

			worker1MkCq = utiltestingapi.MakeClusterQueue("q1").Obj()
			util.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, worker1MkCq)
			util.ExpectClusterQueuesToBeActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1MkCq)
			worker1MkLq = utiltestingapi.MakeLocalQueue(worker1MkCq.Name, worker1Ns.Name).ClusterQueue(worker1MkCq.Name).Obj()
			util.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, worker1MkLq)
			util.ExpectLocalQueuesToBeActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1MkLq)

			worker2MkCq = utiltestingapi.MakeClusterQueue("q1").Obj()
			util.MustCreate(worker2TestCluster.ctx, worker2TestCluster.client, worker2MkCq)
			util.ExpectClusterQueuesToBeActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2MkCq)
			worker2MkLq = utiltestingapi.MakeLocalQueue(worker2MkCq.Name, worker2Ns.Name).ClusterQueue(worker2MkCq.Name).Obj()
			util.MustCreate(worker2TestCluster.ctx, worker2TestCluster.client, worker2MkLq)
			util.ExpectLocalQueuesToBeActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2MkLq)
		})

		// Regular Kueue setup
		ginkgo.By("Creating and initializing regular Kueue resources (Non MultiKueue)", func() {
			managerAC = utiltestingapi.MakeAdmissionCheck("manager-ac").
				ControllerName("kueue-controller").
				Obj()
			util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerAC)
			util.SetAdmissionCheckActive(managerTestCluster.ctx, managerTestCluster.client, managerAC, metav1.ConditionTrue)

			ginkgo.By("wait for check active", func() {
				updatedAc := kueue.AdmissionCheck{}
				acKey := client.ObjectKeyFromObject(managerAC)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, acKey, &updatedAc)).To(gomega.Succeed())
					g.Expect(updatedAc.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.AdmissionCheckActive))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			worker1AC = utiltestingapi.MakeAdmissionCheck("worker1-ac").
				ControllerName("kueue-controller").
				Obj()
			util.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, worker1AC)
			util.SetAdmissionCheckActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1AC, metav1.ConditionTrue)

			ginkgo.By("wait for check active", func() {
				updatedAc := kueue.AdmissionCheck{}
				acKey := client.ObjectKeyFromObject(worker1AC)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, acKey, &updatedAc)).To(gomega.Succeed())
					g.Expect(updatedAc.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.AdmissionCheckActive))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			managerCq = utiltestingapi.MakeClusterQueue("q2").Obj()
			util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerCq)
			util.ExpectClusterQueuesToBeActive(managerTestCluster.ctx, managerTestCluster.client, managerCq)
			managerLq = utiltestingapi.MakeLocalQueue(managerCq.Name, managerNs.Name).ClusterQueue(managerCq.Name).Obj()
			util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerLq)
			util.ExpectLocalQueuesToBeActive(managerTestCluster.ctx, managerTestCluster.client, managerLq)

			worker1Cq = utiltestingapi.MakeClusterQueue("q2").Obj()
			util.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq)
			util.ExpectClusterQueuesToBeActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq)
			worker1Lq = utiltestingapi.MakeLocalQueue(worker1Cq.Name, worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
			util.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, worker1Lq)
			util.ExpectLocalQueuesToBeActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Lq)
		})
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(managerTestCluster.ctx, managerTestCluster.client, managerNs)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(worker1TestCluster.ctx, worker1TestCluster.client, worker1Ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(worker2TestCluster.ctx, worker2TestCluster.client, worker2Ns)).To(gomega.Succeed())

		util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1AC, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerAC, true)

		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMkCq, true)
		util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1MkCq, true)
		util.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2MkCq, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerCq, true)
		util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq, true)

		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, multiKueueAC, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueConfig, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, workerCluster1, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, workerCluster2, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret1, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret2, true)
	})

	ginkgo.It("Share worker cluster between MultiKueue and regular Kueue", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MultiKueueBatchJobWithManagedBy, true)

		var jobMk, jobNonMk *batchv1.Job
		ginkgo.By("creating the jobs in the management cluster for MultiKueue and non-MultiKueue Cluster Queues", func() {
			jobMk = testingjob.MakeJob("job-mk", managerNs.Name).
				ManagedBy(kueue.MultiKueueControllerName).
				Queue(kueue.LocalQueueName(managerMkLq.Name)).
				Obj()
			util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, jobMk)

			jobNonMk = testingjob.MakeJob("job-kueue", worker1Ns.Name).
				Queue(kueue.LocalQueueName(worker1Lq.Name)).
				Obj()
			util.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, jobNonMk)
		})

		wlMkLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(jobMk.Name, jobMk.UID), Namespace: managerNs.Name}
		wlNonMkLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(jobNonMk.Name, jobNonMk.UID), Namespace: worker1Ns.Name}

		ginkgo.By("setting multikueue workload reservation in the management cluster", func() {
			admission := utiltestingapi.MakeAdmission(managerMkCq.Name).Obj()
			util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlMkLookupKey, admission)
		})

		ginkgo.By("setting non multikueue workload reservation in the worker1 cluster", func() {
			admission := utiltestingapi.MakeAdmission(worker1Cq.Name).Obj()
			util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlNonMkLookupKey, admission)
		})

		createdMkWorkload := &kueue.Workload{}
		createdNonMkWorkload := &kueue.Workload{}
		ginkgo.By("checking the multikueue workload creation in the worker clusters", func() {
			managerWl := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlMkLookupKey, managerWl)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlMkLookupKey, createdMkWorkload)).To(gomega.Succeed())
				g.Expect(createdMkWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlMkLookupKey, createdMkWorkload)).To(gomega.Succeed())
				g.Expect(createdMkWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking the non multikueue workload creation in the worker1 cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlNonMkLookupKey, createdNonMkWorkload)).To(gomega.Succeed())
				g.Expect(createdNonMkWorkload.Status.Conditions).Should(utiltesting.HaveCondition(kueue.WorkloadQuotaReserved))
				g.Expect(createdNonMkWorkload.Status.Conditions).Should(utiltesting.HaveCondition(kueue.WorkloadAdmitted))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting workload reservation in worker1, AC state is updated in manager and worker2 wl is removed", func() {
			admission := utiltestingapi.MakeAdmission(managerMkCq.Name).Obj()
			util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlMkLookupKey, admission)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlMkLookupKey, createdMkWorkload)).To(gomega.Succeed())
				acs := admissioncheck.FindAdmissionCheck(createdMkWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAC.Name))
				g.Expect(acs).NotTo(gomega.BeNil())
				g.Expect(acs.State).To(gomega.Equal(kueue.CheckStateReady))
				g.Expect(acs.Message).To(gomega.Equal(`The workload got reservation on "worker1"`))
				ok, err := utiltesting.HasEventAppeared(managerTestCluster.ctx, managerTestCluster.client, corev1.Event{
					Reason:  "MultiKueue",
					Type:    corev1.EventTypeNormal,
					Message: `The workload got reservation on "worker1"`,
				})
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(ok).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlMkLookupKey, createdMkWorkload)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("updating the multikueue worker job status", func() {
			startTime := metav1.NewTime(time.Now().Truncate(time.Second))
			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(jobMk), &createdJob)).To(gomega.Succeed())
				createdJob.Status.StartTime = &startTime
				createdJob.Status.Active = 1
				createdJob.Status.Ready = ptr.To[int32](1)
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(jobMk), &createdJob)).To(gomega.Succeed())
				g.Expect(ptr.Deref(createdJob.Status.StartTime, metav1.Time{})).To(gomega.Equal(startTime))
				g.Expect(ptr.Deref(createdJob.Status.Ready, 0)).To(gomega.Equal(int32(1)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("updating the regular worker job status", func() {
			startTime := metav1.NewTime(time.Now().Truncate(time.Second))
			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(jobNonMk), &createdJob)).To(gomega.Succeed())
				createdJob.Status.StartTime = &startTime
				createdJob.Status.Active = 1
				createdJob.Status.Ready = ptr.To[int32](1)
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

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
		ginkgo.By("finishing the multikueue worker job", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(jobMk), &createdJob)).To(gomega.Succeed())
				createdJob.Status.Conditions = append(createdJob.Status.Conditions,
					batchv1.JobCondition{
						Type:               batchv1.JobSuccessCriteriaMet,
						Status:             corev1.ConditionTrue,
						LastProbeTime:      now,
						LastTransitionTime: now,
						Message:            reachedPodsReason,
					}, completedJobCondition)
				createdJob.Status.Active = 0
				createdJob.Status.Ready = ptr.To[int32](0)
				createdJob.Status.Succeeded = 1
				createdJob.Status.CompletionTime = ptr.To(now)
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlMkLookupKey, finishJobReason)

			// Assert job complete condition.
			localJob := &batchv1.Job{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(jobMk), localJob)).To(gomega.Succeed())
			gomega.Expect(localJob.Status.Conditions).Should(gomega.ContainElement(gomega.WithTransform(func(condition batchv1.JobCondition) batchv1.JobCondition {
				// Compare on all condition attributes excluding Time values.
				condition.LastProbeTime = completedJobCondition.LastProbeTime
				condition.LastTransitionTime = completedJobCondition.LastTransitionTime
				return condition
			}, gomega.Equal(completedJobCondition))))
		})

		ginkgo.By("finishing the regular worker job", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(jobNonMk), &createdJob)).To(gomega.Succeed())
				createdJob.Status.Conditions = append(createdJob.Status.Conditions,
					batchv1.JobCondition{
						Type:               batchv1.JobSuccessCriteriaMet,
						Status:             corev1.ConditionTrue,
						LastProbeTime:      now,
						LastTransitionTime: now,
						Message:            reachedPodsReason,
					}, completedJobCondition)
				createdJob.Status.Active = 0
				createdJob.Status.Ready = ptr.To[int32](0)
				createdJob.Status.Succeeded = 1
				createdJob.Status.CompletionTime = ptr.To(now)
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdWorkload := &kueue.Workload{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlNonMkLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeComparableTo(&metav1.Condition{
					Type:    kueue.WorkloadFinished,
					Status:  metav1.ConditionTrue,
					Reason:  string(kftraining.JobSucceeded),
					Message: finishJobReason,
				}, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Share manager cluster between MultiKueue and regular Kueue", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MultiKueueBatchJobWithManagedBy, true)

		var jobMk, jobNonMk *batchv1.Job
		ginkgo.By("creating the jobs in the management cluster for MultiKueue and non-MultiKueue Cluster Queues", func() {
			jobMk = testingjob.MakeJob("job-mk", managerNs.Name).
				ManagedBy(kueue.MultiKueueControllerName).
				Queue(kueue.LocalQueueName(managerMkLq.Name)).
				Obj()
			util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, jobMk)

			jobNonMk = testingjob.MakeJob("job-kueue", managerNs.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				Obj()
			util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, jobNonMk)
		})

		wlMkLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(jobMk.Name, jobMk.UID), Namespace: managerNs.Name}
		wlNonMkLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(jobNonMk.Name, jobNonMk.UID), Namespace: managerNs.Name}

		ginkgo.By("setting multikueue workload reservation in the management cluster", func() {
			admission := utiltestingapi.MakeAdmission(managerMkCq.Name).Obj()
			util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlMkLookupKey, admission)
		})

		ginkgo.By("setting non multikueue workload reservation in the worker1 cluster", func() {
			admission := utiltestingapi.MakeAdmission(managerCq.Name).Obj()
			util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlNonMkLookupKey, admission)
		})

		createdMkWorkload := &kueue.Workload{}
		createdNonMkWorkload := &kueue.Workload{}
		ginkgo.By("checking the multikueue workload creation in the worker clusters", func() {
			managerWl := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlMkLookupKey, managerWl)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlMkLookupKey, createdMkWorkload)).To(gomega.Succeed())
				g.Expect(createdMkWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlMkLookupKey, createdMkWorkload)).To(gomega.Succeed())
				g.Expect(createdMkWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking the non multikueue workload creation in the manager cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlNonMkLookupKey, createdNonMkWorkload)).To(gomega.Succeed())
				g.Expect(createdNonMkWorkload.Status.Conditions).Should(utiltesting.HaveCondition(kueue.WorkloadQuotaReserved))
				g.Expect(createdNonMkWorkload.Status.Conditions).Should(utiltesting.HaveCondition(kueue.WorkloadAdmitted))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting workload reservation in worker1, AC state is updated in manager and worker2 wl is removed", func() {
			admission := utiltestingapi.MakeAdmission(managerMkCq.Name).Obj()
			util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlMkLookupKey, admission)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlMkLookupKey, createdMkWorkload)).To(gomega.Succeed())
				acs := admissioncheck.FindAdmissionCheck(createdMkWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAC.Name))
				g.Expect(acs).NotTo(gomega.BeNil())
				g.Expect(acs.State).To(gomega.Equal(kueue.CheckStateReady))
				g.Expect(acs.Message).To(gomega.Equal(`The workload got reservation on "worker1"`))
				ok, err := utiltesting.HasEventAppeared(managerTestCluster.ctx, managerTestCluster.client, corev1.Event{
					Reason:  "MultiKueue",
					Type:    corev1.EventTypeNormal,
					Message: `The workload got reservation on "worker1"`,
				})
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(ok).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlMkLookupKey, createdMkWorkload)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("updating the multikueue manager job status", func() {
			startTime := metav1.NewTime(time.Now().Truncate(time.Second))
			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(jobMk), &createdJob)).To(gomega.Succeed())
				createdJob.Status.StartTime = &startTime
				createdJob.Status.Active = 1
				createdJob.Status.Ready = ptr.To[int32](1)
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(jobMk), &createdJob)).To(gomega.Succeed())
				g.Expect(ptr.Deref(createdJob.Status.StartTime, metav1.Time{})).To(gomega.Equal(startTime))
				g.Expect(ptr.Deref(createdJob.Status.Ready, 0)).To(gomega.Equal(int32(1)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("updating the regular worker job status", func() {
			startTime := metav1.NewTime(time.Now().Truncate(time.Second))
			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(jobNonMk), &createdJob)).To(gomega.Succeed())
				createdJob.Status.StartTime = &startTime
				createdJob.Status.Active = 1
				createdJob.Status.Ready = ptr.To[int32](1)
				g.Expect(managerTestCluster.client.Status().Update(managerTestCluster.ctx, &createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

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
		ginkgo.By("finishing the multikueue worker job", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(jobMk), &createdJob)).To(gomega.Succeed())
				createdJob.Status.Conditions = append(createdJob.Status.Conditions,
					batchv1.JobCondition{
						Type:               batchv1.JobSuccessCriteriaMet,
						Status:             corev1.ConditionTrue,
						LastProbeTime:      now,
						LastTransitionTime: now,
						Message:            reachedPodsReason,
					}, completedJobCondition)
				createdJob.Status.Active = 0
				createdJob.Status.Ready = ptr.To[int32](0)
				createdJob.Status.Succeeded = 1
				createdJob.Status.CompletionTime = ptr.To(now)
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlMkLookupKey, finishJobReason)

			// Assert job complete condition.
			localJob := &batchv1.Job{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(jobMk), localJob)).To(gomega.Succeed())
			gomega.Expect(localJob.Status.Conditions).Should(gomega.ContainElement(gomega.WithTransform(func(condition batchv1.JobCondition) batchv1.JobCondition {
				// Compare on all condition attributes excluding Time values.
				condition.LastProbeTime = completedJobCondition.LastProbeTime
				condition.LastTransitionTime = completedJobCondition.LastTransitionTime
				return condition
			}, gomega.Equal(completedJobCondition))))
		})

		ginkgo.By("finishing the regular manager job", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(jobNonMk), &createdJob)).To(gomega.Succeed())
				createdJob.Status.Conditions = append(createdJob.Status.Conditions,
					batchv1.JobCondition{
						Type:               batchv1.JobSuccessCriteriaMet,
						Status:             corev1.ConditionTrue,
						LastProbeTime:      now,
						LastTransitionTime: now,
						Message:            reachedPodsReason,
					}, completedJobCondition)
				createdJob.Status.Active = 0
				createdJob.Status.Ready = ptr.To[int32](0)
				createdJob.Status.Succeeded = 1
				createdJob.Status.CompletionTime = ptr.To(now)
				g.Expect(managerTestCluster.client.Status().Update(managerTestCluster.ctx, &createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdWorkload := &kueue.Workload{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlNonMkLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeComparableTo(&metav1.Condition{
					Type:    kueue.WorkloadFinished,
					Status:  metav1.ConditionTrue,
					Reason:  string(kftraining.JobSucceeded),
					Message: finishJobReason,
				}, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
