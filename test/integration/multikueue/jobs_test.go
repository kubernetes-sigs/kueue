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
	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	gomegatypes "github.com/onsi/gomega/types"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	workloadappwrapper "sigs.k8s.io/kueue/pkg/controller/jobs/appwrapper"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	workloadjobset "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	workloadpaddlejob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/paddlejob"
	workloadpytorchjob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/pytorchjob"
	workloadtfjob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/tfjob"
	workloadxgboostjob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/xgboostjob"
	workloadmpijob "sigs.k8s.io/kueue/pkg/controller/jobs/mpijob"
	workloadpod "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	workloadraycluster "sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	workloadrayjob "sigs.k8s.io/kueue/pkg/controller/jobs/rayjob"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingaw "sigs.k8s.io/kueue/pkg/util/testingjobs/appwrapper"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	testingmpijob "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"
	testingpaddlejob "sigs.k8s.io/kueue/pkg/util/testingjobs/paddlejob"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	testingpytorchjob "sigs.k8s.io/kueue/pkg/util/testingjobs/pytorchjob"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	testingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	testingtfjob "sigs.k8s.io/kueue/pkg/util/testingjobs/tfjob"
	testingxgboostjob "sigs.k8s.io/kueue/pkg/util/testingjobs/xgboostjob"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var defaultEnabledIntegrations sets.Set[string] = sets.New(
	"batch/job", "kubeflow.org/mpijob", "ray.io/rayjob", "ray.io/raycluster",
	"jobset.x-k8s.io/jobset", "kubeflow.org/paddlejob",
	"kubeflow.org/pytorchjob", "kubeflow.org/tfjob", "kubeflow.org/xgboostjob", "kubeflow.org/jaxjob",
	"pod", "workload.codeflare.dev/appwrapper")

var _ = ginkgo.Describe("MultiKueue", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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
		managerCq                *kueue.ClusterQueue
		managerLq                *kueue.LocalQueue

		worker1Cq *kueue.ClusterQueue
		worker1Lq *kueue.LocalQueue

		worker2Cq *kueue.ClusterQueue
		worker2Lq *kueue.LocalQueue
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

		workerCluster1 = utiltesting.MakeMultiKueueCluster("worker1").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret1.Name).Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, workerCluster1)

		workerCluster2 = utiltesting.MakeMultiKueueCluster("worker2").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret2.Name).Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, workerCluster2)

		managerMultiKueueConfig = utiltesting.MakeMultiKueueConfig("multikueueconfig").Clusters(workerCluster1.Name, workerCluster2.Name).Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueConfig)

		multiKueueAC = utiltesting.MakeAdmissionCheck("ac1").
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

		managerCq = utiltesting.MakeClusterQueue("q1").
			AdmissionChecks(kueue.AdmissionCheckReference(multiKueueAC.Name)).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerCq)
		util.ExpectClusterQueuesToBeActive(managerTestCluster.ctx, managerTestCluster.client, managerCq)

		managerLq = utiltesting.MakeLocalQueue(managerCq.Name, managerNs.Name).ClusterQueue(managerCq.Name).Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerLq)
		util.ExpectLocalQueuesToBeActive(managerTestCluster.ctx, managerTestCluster.client, managerLq)

		worker1Cq = utiltesting.MakeClusterQueue("q1").Obj()
		util.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq)
		util.ExpectClusterQueuesToBeActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq)
		worker1Lq = utiltesting.MakeLocalQueue(worker1Cq.Name, worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
		util.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, worker1Lq)
		util.ExpectLocalQueuesToBeActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Lq)

		worker2Cq = utiltesting.MakeClusterQueue("q1").Obj()
		util.MustCreate(worker2TestCluster.ctx, worker2TestCluster.client, worker2Cq)
		util.ExpectClusterQueuesToBeActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2Cq)
		worker2Lq = utiltesting.MakeLocalQueue(worker2Cq.Name, worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
		util.MustCreate(worker2TestCluster.ctx, worker2TestCluster.client, worker2Lq)
		util.ExpectLocalQueuesToBeActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2Lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(managerTestCluster.ctx, managerTestCluster.client, managerNs)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(worker1TestCluster.ctx, worker1TestCluster.client, worker1Ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(worker2TestCluster.ctx, worker2TestCluster.client, worker2Ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerCq, true)
		util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq, true)
		util.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2Cq, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, multiKueueAC, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueConfig, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, workerCluster1, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, workerCluster2, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret1, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret2, true)
	})

	ginkgo.It("Should run a job on worker if admitted", func() {
		job := testingjob.MakeJob("job", managerNs.Name).
			Queue(kueue.LocalQueueName(managerLq.Name)).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

		ginkgo.By("setting workload reservation in the management cluster", func() {
			admission := utiltesting.MakeAdmission(managerCq.Name).Obj()
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, createdWorkload, admission)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
			admission := utiltesting.MakeAdmission(managerCq.Name).Obj()

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, createdWorkload, admission)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				acs := workload.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAC.Name))
				g.Expect(acs).NotTo(gomega.BeNil())
				g.Expect(acs.State).To(gomega.Equal(kueue.CheckStatePending))
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
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
				createdJob.Status.StartTime = ptr.To(now)
				createdJob.Status.CompletionTime = ptr.To(now)
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

	ginkgo.It("Should run a job on worker if admitted (ManagedBy)", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MultiKueueBatchJobWithManagedBy, true)
		job := testingjob.MakeJob("job", managerNs.Name).
			ManagedBy(kueue.MultiKueueControllerName).
			Queue(kueue.LocalQueueName(managerLq.Name)).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

		ginkgo.By("setting workload reservation in the management cluster", func() {
			admission := utiltesting.MakeAdmission(managerCq.Name).Obj()
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, createdWorkload, admission)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
			admission := utiltesting.MakeAdmission(managerCq.Name).Obj()

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, createdWorkload, admission)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				acs := workload.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAC.Name))
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
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("updating the worker job status", func() {
			startTime := metav1.NewTime(time.Now().Truncate(time.Second))
			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
				createdJob.Status.StartTime = &startTime
				createdJob.Status.Active = 1
				createdJob.Status.Ready = ptr.To[int32](1)
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
				g.Expect(ptr.Deref(createdJob.Status.StartTime, metav1.Time{})).To(gomega.Equal(startTime))
				g.Expect(ptr.Deref(createdJob.Status.Ready, 0)).To(gomega.Equal(int32(1)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
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
				createdJob.Status.Ready = ptr.To[int32](0)
				createdJob.Status.Succeeded = 1
				createdJob.Status.CompletionTime = ptr.To(now)
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

	ginkgo.It("Should run a jobSet on worker if admitted", func() {
		jobSet := testingjobset.MakeJobSet("job-set", managerNs.Name).
			Queue(managerLq.Name).
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
		wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: managerNs.Name}

		admission := utiltesting.MakeAdmission(managerCq.Name).PodSets(
			kueue.PodSetAssignment{
				Name: "replicated-job-1",
			}, kueue.PodSetAssignment{
				Name: "replicated-job-2",
			},
		)

		admitWorkloadAndCheckWorkerCopies(multiKueueAC.Name, wlLookupKey, admission)

		ginkgo.By("changing the status of the jobset in the worker, updates the manager's jobset status", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdJobSet := jobset.JobSet{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(jobSet), &createdJobSet)).To(gomega.Succeed())
				createdJobSet.Status.Restarts = 10
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdJobSet)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				createdJobSet := jobset.JobSet{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(jobSet), &createdJobSet)).To(gomega.Succeed())
				g.Expect(createdJobSet.Status.Restarts).To(gomega.Equal(int32(10)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker jobSet, the manager's wl is marked as finished and the worker2 wl removed", func() {
			finishJobReason := "JobSet finished successfully"
			gomega.Eventually(func(g gomega.Gomega) {
				createdJobSet := jobset.JobSet{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(jobSet), &createdJobSet)).To(gomega.Succeed())
				apimeta.SetStatusCondition(&createdJobSet.Status.Conditions, metav1.Condition{
					Type:    string(jobset.JobSetCompleted),
					Status:  metav1.ConditionTrue,
					Reason:  "ByTest",
					Message: finishJobReason,
				})
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdJobSet)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, finishJobReason)
		})
	})

	ginkgo.It("Should run a TFJob on worker if admitted", framework.RedundantSpec, func() {
		tfJob := testingtfjob.MakeTFJob("tfjob1", managerNs.Name).
			ManagedBy(kueue.MultiKueueControllerName).
			Queue(managerLq.Name).
			TFReplicaSpecs(
				testingtfjob.TFReplicaSpecRequirement{
					ReplicaType:   kftraining.TFJobReplicaTypeChief,
					ReplicaCount:  1,
					Name:          "tfjob-chief",
					RestartPolicy: "OnFailure",
				},
				testingtfjob.TFReplicaSpecRequirement{
					ReplicaType:   kftraining.TFJobReplicaTypePS,
					ReplicaCount:  1,
					Name:          "tfjob-ps",
					RestartPolicy: "Never",
				},
				testingtfjob.TFReplicaSpecRequirement{
					ReplicaType:   kftraining.TFJobReplicaTypeWorker,
					ReplicaCount:  3,
					Name:          "tfjob-worker",
					RestartPolicy: "OnFailure",
				},
			).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, tfJob)
		wlLookupKey := types.NamespacedName{Name: workloadtfjob.GetWorkloadNameForTFJob(tfJob.Name, tfJob.UID), Namespace: managerNs.Name}
		admission := utiltesting.MakeAdmission(managerCq.Name).PodSets(
			kueue.PodSetAssignment{
				Name: "chief",
			}, kueue.PodSetAssignment{
				Name: "ps",
			}, kueue.PodSetAssignment{
				Name: "worker",
			},
		)

		admitWorkloadAndCheckWorkerCopies(multiKueueAC.Name, wlLookupKey, admission)

		ginkgo.By("changing the status of the TFJob in the worker, updates the manager's TFJob status", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdTfJob := kftraining.TFJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(tfJob), &createdTfJob)).To(gomega.Succeed())
				createdTfJob.Status.ReplicaStatuses = map[kftraining.ReplicaType]*kftraining.ReplicaStatus{
					kftraining.TFJobReplicaTypeChief: {
						Active: 1,
					},
					kftraining.TFJobReplicaTypePS: {
						Active: 1,
					},
					kftraining.TFJobReplicaTypeWorker: {
						Active:    2,
						Succeeded: 1,
					},
				}
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdTfJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				createdTfJob := kftraining.TFJob{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(tfJob), &createdTfJob)).To(gomega.Succeed())
				g.Expect(createdTfJob.Status.ReplicaStatuses).To(gomega.Equal(
					map[kftraining.ReplicaType]*kftraining.ReplicaStatus{
						kftraining.TFJobReplicaTypeChief: {
							Active: 1,
						},
						kftraining.TFJobReplicaTypePS: {
							Active: 1,
						},
						kftraining.TFJobReplicaTypeWorker: {
							Active:    2,
							Succeeded: 1,
						},
					}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker TFJob, the manager's wl is marked as finished and the worker2 wl removed", func() {
			finishJobReason := "TFJob finished successfully"
			gomega.Eventually(func(g gomega.Gomega) {
				createdTfJob := kftraining.TFJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(tfJob), &createdTfJob)).To(gomega.Succeed())
				createdTfJob.Status.Conditions = append(createdTfJob.Status.Conditions, kftraining.JobCondition{
					Type:    kftraining.JobSucceeded,
					Status:  corev1.ConditionTrue,
					Reason:  "ByTest",
					Message: finishJobReason,
				})
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdTfJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, finishJobReason)
		})
	})

	ginkgo.It("Should run a PaddleJob on worker if admitted", framework.RedundantSpec, func() {
		paddleJob := testingpaddlejob.MakePaddleJob("paddlejob1", managerNs.Name).
			ManagedBy(kueue.MultiKueueControllerName).
			Queue(managerLq.Name).
			PaddleReplicaSpecs(
				testingpaddlejob.PaddleReplicaSpecRequirement{
					ReplicaType:   kftraining.PaddleJobReplicaTypeMaster,
					ReplicaCount:  1,
					Name:          "paddlejob-master",
					RestartPolicy: "OnFailure",
				},
				testingpaddlejob.PaddleReplicaSpecRequirement{
					ReplicaType:   kftraining.PaddleJobReplicaTypeWorker,
					ReplicaCount:  3,
					Name:          "paddlejob-worker",
					RestartPolicy: "OnFailure",
				},
			).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, paddleJob)

		wlLookupKey := types.NamespacedName{Name: workloadpaddlejob.GetWorkloadNameForPaddleJob(paddleJob.Name, paddleJob.UID), Namespace: managerNs.Name}
		admission := utiltesting.MakeAdmission(managerCq.Name).PodSets(
			kueue.PodSetAssignment{
				Name: "master",
			}, kueue.PodSetAssignment{
				Name: "worker",
			},
		)

		admitWorkloadAndCheckWorkerCopies(multiKueueAC.Name, wlLookupKey, admission)

		ginkgo.By("changing the status of the PaddleJob in the worker, updates the manager's PaddleJob status", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdPaddleJob := kftraining.PaddleJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(paddleJob), &createdPaddleJob)).To(gomega.Succeed())
				createdPaddleJob.Status.ReplicaStatuses = map[kftraining.ReplicaType]*kftraining.ReplicaStatus{
					kftraining.PaddleJobReplicaTypeMaster: {
						Active: 1,
					},
					kftraining.PaddleJobReplicaTypeWorker: {
						Active: 3,
					},
				}
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdPaddleJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				createdPaddleJob := kftraining.PaddleJob{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(paddleJob), &createdPaddleJob)).To(gomega.Succeed())
				g.Expect(createdPaddleJob.Status.ReplicaStatuses).To(gomega.Equal(
					map[kftraining.ReplicaType]*kftraining.ReplicaStatus{
						kftraining.PaddleJobReplicaTypeMaster: {
							Active: 1,
						},
						kftraining.PaddleJobReplicaTypeWorker: {
							Active: 3,
						},
					}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker PaddleJob, the manager's wl is marked as finished and the worker2 wl removed", func() {
			finishJobReason := "PaddleJob finished successfully"
			gomega.Eventually(func(g gomega.Gomega) {
				createdPaddleJob := kftraining.PaddleJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(paddleJob), &createdPaddleJob)).To(gomega.Succeed())
				createdPaddleJob.Status.Conditions = append(createdPaddleJob.Status.Conditions, kftraining.JobCondition{
					Type:    kftraining.JobSucceeded,
					Status:  corev1.ConditionTrue,
					Reason:  "ByTest",
					Message: finishJobReason,
				})
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdPaddleJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, finishJobReason)
		})
	})

	ginkgo.It("Should run a PyTorchJob on worker if admitted", func() {
		pyTorchJob := testingpytorchjob.MakePyTorchJob("pytorchjob1", managerNs.Name).
			ManagedBy(kueue.MultiKueueControllerName).
			Queue(managerLq.Name).
			PyTorchReplicaSpecs(
				testingpytorchjob.PyTorchReplicaSpecRequirement{
					ReplicaType:   kftraining.PyTorchJobReplicaTypeMaster,
					ReplicaCount:  1,
					Name:          "pytorchjob-master",
					RestartPolicy: "OnFailure",
				},
				testingpytorchjob.PyTorchReplicaSpecRequirement{
					ReplicaType:   kftraining.PyTorchJobReplicaTypeWorker,
					ReplicaCount:  1,
					Name:          "pytorchjob-worker",
					RestartPolicy: "Never",
				},
			).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, pyTorchJob)

		wlLookupKey := types.NamespacedName{Name: workloadpytorchjob.GetWorkloadNameForPyTorchJob(pyTorchJob.Name, pyTorchJob.UID), Namespace: managerNs.Name}
		admission := utiltesting.MakeAdmission(managerCq.Name).PodSets(
			kueue.PodSetAssignment{
				Name: "master",
			}, kueue.PodSetAssignment{
				Name: "worker",
			},
		)

		admitWorkloadAndCheckWorkerCopies(multiKueueAC.Name, wlLookupKey, admission)

		ginkgo.By("changing the status of the PyTorchJob in the worker, updates the manager's PyTorchJob status", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdPyTorchJob := kftraining.PyTorchJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(pyTorchJob), &createdPyTorchJob)).To(gomega.Succeed())
				createdPyTorchJob.Status.ReplicaStatuses = map[kftraining.ReplicaType]*kftraining.ReplicaStatus{
					kftraining.PyTorchJobReplicaTypeMaster: {
						Active: 1,
					},
					kftraining.PyTorchJobReplicaTypeWorker: {
						Active:    2,
						Succeeded: 1,
					},
				}
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdPyTorchJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				createdPyTorchJob := kftraining.PyTorchJob{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(pyTorchJob), &createdPyTorchJob)).To(gomega.Succeed())
				g.Expect(createdPyTorchJob.Status.ReplicaStatuses).To(gomega.Equal(
					map[kftraining.ReplicaType]*kftraining.ReplicaStatus{
						kftraining.PyTorchJobReplicaTypeMaster: {
							Active: 1,
						},
						kftraining.PyTorchJobReplicaTypeWorker: {
							Active:    2,
							Succeeded: 1,
						},
					}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker PyTorchJob, the manager's wl is marked as finished and the worker2 wl removed", func() {
			finishJobReason := "PyTorchJob finished successfully"
			gomega.Eventually(func(g gomega.Gomega) {
				createdPyTorchJob := kftraining.PyTorchJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(pyTorchJob), &createdPyTorchJob)).To(gomega.Succeed())
				createdPyTorchJob.Status.Conditions = append(createdPyTorchJob.Status.Conditions, kftraining.JobCondition{
					Type:    kftraining.JobSucceeded,
					Status:  corev1.ConditionTrue,
					Reason:  "ByTest",
					Message: finishJobReason,
				})
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdPyTorchJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, finishJobReason)
		})
	})

	ginkgo.It("Should not run a PyTorchJob on worker if set to be managed by wrong external controller", func() {
		admission := utiltesting.MakeAdmission(managerCq.Name).PodSets(
			kueue.PodSetAssignment{
				Name: "master",
			}, kueue.PodSetAssignment{
				Name: "worker",
			},
		)
		pyTorchJob := testingpytorchjob.MakePyTorchJob("pytorchjob-not-managed", managerNs.Name).
			Queue(managerLq.Name).
			ManagedBy("example.com/other-controller-not-training-operator").
			PyTorchReplicaSpecs(
				testingpytorchjob.PyTorchReplicaSpecRequirement{
					ReplicaType:   kftraining.PyTorchJobReplicaTypeMaster,
					ReplicaCount:  1,
					Name:          "pytorchjob-master",
					RestartPolicy: "OnFailure",
				},
				testingpytorchjob.PyTorchReplicaSpecRequirement{
					ReplicaType:   kftraining.PyTorchJobReplicaTypeWorker,
					ReplicaCount:  1,
					Name:          "pytorchjob-worker",
					RestartPolicy: "Never",
				},
			).
			Obj()
		ginkgo.By("create a pytorchjob with external managedBy", func() {
			util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, pyTorchJob)
		})

		wlLookupKeyNoManagedBy := types.NamespacedName{Name: workloadpytorchjob.GetWorkloadNameForPyTorchJob(pyTorchJob.Name, pyTorchJob.UID), Namespace: managerNs.Name}
		setQuotaReservationInCluster(wlLookupKeyNoManagedBy, admission)
		checkingTheWorkloadCreation(wlLookupKeyNoManagedBy, gomega.Not(gomega.Succeed()))
	})

	ginkgo.It("Should run a XGBoostJob on worker if admitted", framework.RedundantSpec, func() {
		xgBoostJob := testingxgboostjob.MakeXGBoostJob("xgboostjob1", managerNs.Name).
			ManagedBy(kueue.MultiKueueControllerName).
			Queue(managerLq.Name).
			XGBReplicaSpecs(
				testingxgboostjob.XGBReplicaSpecRequirement{
					ReplicaType:   kftraining.XGBoostJobReplicaTypeMaster,
					ReplicaCount:  1,
					Name:          "master",
					RestartPolicy: "OnFailure",
				},
				testingxgboostjob.XGBReplicaSpecRequirement{
					ReplicaType:   kftraining.XGBoostJobReplicaTypeWorker,
					ReplicaCount:  2,
					Name:          "worker",
					RestartPolicy: "Never",
				},
			).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, xgBoostJob)

		wlLookupKey := types.NamespacedName{Name: workloadxgboostjob.GetWorkloadNameForXGBoostJob(xgBoostJob.Name, xgBoostJob.UID), Namespace: managerNs.Name}
		admission := utiltesting.MakeAdmission(managerCq.Name).PodSets(
			kueue.PodSetAssignment{
				Name: "master",
			}, kueue.PodSetAssignment{
				Name: "worker",
			},
		)

		admitWorkloadAndCheckWorkerCopies(multiKueueAC.Name, wlLookupKey, admission)

		ginkgo.By("changing the status of the XGBoostJob in the worker, updates the manager's XGBoostJob status", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdXGBoostJob := kftraining.XGBoostJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(xgBoostJob), &createdXGBoostJob)).To(gomega.Succeed())
				createdXGBoostJob.Status.ReplicaStatuses = map[kftraining.ReplicaType]*kftraining.ReplicaStatus{
					kftraining.XGBoostJobReplicaTypeMaster: {
						Active: 1,
					},
					kftraining.XGBoostJobReplicaTypeWorker: {
						Active:    2,
						Succeeded: 1,
					},
				}
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdXGBoostJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				createdXGBoostJob := kftraining.XGBoostJob{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(xgBoostJob), &createdXGBoostJob)).To(gomega.Succeed())
				g.Expect(createdXGBoostJob.Status.ReplicaStatuses).To(gomega.Equal(
					map[kftraining.ReplicaType]*kftraining.ReplicaStatus{
						kftraining.XGBoostJobReplicaTypeMaster: {
							Active: 1,
						},
						kftraining.XGBoostJobReplicaTypeWorker: {
							Active:    2,
							Succeeded: 1,
						},
					}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker XGBoostJob, the manager's wl is marked as finished and the worker2 wl removed", func() {
			finishJobReason := "XGBoostJob finished successfully"
			gomega.Eventually(func(g gomega.Gomega) {
				createdXGBoostJob := kftraining.XGBoostJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(xgBoostJob), &createdXGBoostJob)).To(gomega.Succeed())
				createdXGBoostJob.Status.Conditions = append(createdXGBoostJob.Status.Conditions, kftraining.JobCondition{
					Type:    kftraining.JobSucceeded,
					Status:  corev1.ConditionTrue,
					Reason:  "ByTest",
					Message: finishJobReason,
				})
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdXGBoostJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, finishJobReason)
		})
	})

	ginkgo.It("Should run an appwrapper on worker if admitted", func() {
		aw := testingaw.MakeAppWrapper("aw", managerNs.Name).
			Component(testingaw.Component{
				Template: testingjob.MakeJob("job-1", managerNs.Name).SetTypeMeta().Parallelism(1).Obj(),
			}).
			Queue(managerLq.Name).
			ManagedBy(kueue.MultiKueueControllerName).
			Obj()

		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, aw)
		wlLookupKey := types.NamespacedName{Name: workloadappwrapper.GetWorkloadNameForAppWrapper(aw.Name, aw.UID), Namespace: managerNs.Name}

		admission := utiltesting.MakeAdmission(managerCq.Name).PodSets(
			kueue.PodSetAssignment{
				Name: "aw-0",
			},
		)

		admitWorkloadAndCheckWorkerCopies(multiKueueAC.Name, wlLookupKey, admission)

		ginkgo.By("changing the status of the appwrapper in the worker, updates the manager's appwrappers status", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdAppWrapper := awv1beta2.AppWrapper{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(aw), &createdAppWrapper)).To(gomega.Succeed())
				createdAppWrapper.Status.Phase = awv1beta2.AppWrapperRunning
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdAppWrapper)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				createdAppWrapper := awv1beta2.AppWrapper{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(aw), &createdAppWrapper)).To(gomega.Succeed())
				g.Expect(createdAppWrapper.Status.Phase).To(gomega.Equal(awv1beta2.AppWrapperRunning))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker appwrapper, the manager's wl is marked as finished and the worker2 wl removed", func() {
			finishJobReason := "AppWrapper finished successfully"
			gomega.Eventually(func(g gomega.Gomega) {
				createdAppWrapper := awv1beta2.AppWrapper{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(aw), &createdAppWrapper)).To(gomega.Succeed())
				createdAppWrapper.Status.Phase = awv1beta2.AppWrapperSucceeded
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdAppWrapper)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, finishJobReason)
		})
	})

	ginkgo.It("Should not run a MPIJob on worker if set to be managed by external controller", func() {
		admission := utiltesting.MakeAdmission(managerCq.Name).PodSets(
			kueue.PodSetAssignment{
				Name: "launcher",
			}, kueue.PodSetAssignment{
				Name: "worker",
			},
		)
		mpijobNoManagedBy := testingmpijob.MakeMPIJob("mpijob2", managerNs.Name).
			Queue(managerLq.Name).
			ManagedBy("example.com/other-controller-not-mpi-operator").
			MPIJobReplicaSpecs(
				testingmpijob.MPIJobReplicaSpecRequirement{
					ReplicaType:   kfmpi.MPIReplicaTypeLauncher,
					ReplicaCount:  1,
					RestartPolicy: corev1.RestartPolicyOnFailure,
				},
				testingmpijob.MPIJobReplicaSpecRequirement{
					ReplicaType:   kfmpi.MPIReplicaTypeWorker,
					ReplicaCount:  1,
					RestartPolicy: corev1.RestartPolicyNever,
				},
			).
			Obj()
		ginkgo.By("create a mpijob with external managedBy", func() {
			util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, mpijobNoManagedBy)
		})

		wlLookupKeyNoManagedBy := types.NamespacedName{Name: workloadmpijob.GetWorkloadNameForMPIJob(mpijobNoManagedBy.Name, mpijobNoManagedBy.UID), Namespace: managerNs.Name}
		setQuotaReservationInCluster(wlLookupKeyNoManagedBy, admission)
		checkingTheWorkloadCreation(wlLookupKeyNoManagedBy, gomega.Not(gomega.Succeed()))
	})

	ginkgo.It("Should run a MPIJob on worker if admitted", func() {
		admission := utiltesting.MakeAdmission(managerCq.Name).PodSets(
			kueue.PodSetAssignment{
				Name: "launcher",
			}, kueue.PodSetAssignment{
				Name: "worker",
			},
		)
		mpijob := testingmpijob.MakeMPIJob("mpijob1", managerNs.Name).
			Queue(managerLq.Name).
			ManagedBy(kueue.MultiKueueControllerName).
			MPIJobReplicaSpecs(
				testingmpijob.MPIJobReplicaSpecRequirement{
					ReplicaType:   kfmpi.MPIReplicaTypeLauncher,
					ReplicaCount:  1,
					RestartPolicy: corev1.RestartPolicyOnFailure,
				},
				testingmpijob.MPIJobReplicaSpecRequirement{
					ReplicaType:   kfmpi.MPIReplicaTypeWorker,
					ReplicaCount:  1,
					RestartPolicy: corev1.RestartPolicyNever,
				},
			).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, mpijob)
		wlLookupKey := types.NamespacedName{Name: workloadmpijob.GetWorkloadNameForMPIJob(mpijob.Name, mpijob.UID), Namespace: managerNs.Name}
		admitWorkloadAndCheckWorkerCopies(multiKueueAC.Name, wlLookupKey, admission)

		ginkgo.By("changing the status of the MPIJob in the worker, updates the manager's MPIJob status", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdMPIJob := kfmpi.MPIJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(mpijob), &createdMPIJob)).To(gomega.Succeed())
				createdMPIJob.Status.ReplicaStatuses = map[kfmpi.MPIReplicaType]*kfmpi.ReplicaStatus{
					kfmpi.MPIReplicaTypeLauncher: {
						Active: 1,
					},
					kfmpi.MPIReplicaTypeWorker: {
						Active:    1,
						Succeeded: 1,
					},
				}
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdMPIJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				createdMPIJob := kfmpi.MPIJob{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(mpijob), &createdMPIJob)).To(gomega.Succeed())
				g.Expect(createdMPIJob.Status.ReplicaStatuses).To(gomega.Equal(
					map[kfmpi.MPIReplicaType]*kfmpi.ReplicaStatus{
						kfmpi.MPIReplicaTypeLauncher: {
							Active: 1,
						},
						kfmpi.MPIReplicaTypeWorker: {
							Active:    1,
							Succeeded: 1,
						},
					}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker MPIJob, the manager's wl is marked as finished and the worker2 wl removed", func() {
			finishJobReason := "MPIJob finished successfully"
			gomega.Eventually(func(g gomega.Gomega) {
				createdMPIJob := kfmpi.MPIJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(mpijob), &createdMPIJob)).To(gomega.Succeed())
				createdMPIJob.Status.Conditions = append(createdMPIJob.Status.Conditions, kfmpi.JobCondition{
					Type:    kfmpi.JobSucceeded,
					Status:  corev1.ConditionTrue,
					Reason:  "ByTest",
					Message: finishJobReason,
				})
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdMPIJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, finishJobReason)
		})
	})

	ginkgo.It("Should create a pod on worker if admitted", func() {
		pod := testingpod.MakePod("pod1", managerNs.Name).
			Queue(managerLq.Name).
			ManagedByKueueLabel().
			KueueSchedulingGate().
			Obj()

		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, pod)

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadpod.GetWorkloadNameForPod(pod.Name, pod.UID), Namespace: managerNs.Name}

		ginkgo.By("setting workload reservation in the management cluster", func() {
			admission := utiltesting.MakeAdmission(managerCq.Name).Obj()
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, createdWorkload, admission)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
			admission := utiltesting.MakeAdmission(managerCq.Name).Obj()

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, createdWorkload, admission)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				acs := workload.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAC.Name))
				g.Expect(acs).NotTo(gomega.BeNil())
				g.Expect(acs.State).To(gomega.Equal(kueue.CheckStatePending))
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
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker pod", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdPod := corev1.Pod{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(pod), &createdPod)).To(gomega.Succeed())
				createdPod.Status.Phase = corev1.PodSucceeded
				createdPod.Status.Conditions = append(createdPod.Status.Conditions,
					corev1.PodCondition{
						Type:               corev1.PodReadyToStartContainers,
						Status:             corev1.ConditionFalse,
						LastProbeTime:      metav1.Now(),
						LastTransitionTime: metav1.Now(),
						Reason:             "",
					},
					corev1.PodCondition{
						Type:               corev1.PodInitialized,
						Status:             corev1.ConditionTrue,
						LastProbeTime:      metav1.Now(),
						LastTransitionTime: metav1.Now(),
						Reason:             string(corev1.PodSucceeded),
					},
					corev1.PodCondition{
						Type:               corev1.PodReady,
						Status:             corev1.ConditionFalse,
						LastProbeTime:      metav1.Now(),
						LastTransitionTime: metav1.Now(),
						Reason:             string(corev1.PodSucceeded),
					},
					corev1.PodCondition{
						Type:               corev1.ContainersReady,
						Status:             corev1.ConditionFalse,
						LastProbeTime:      metav1.Now(),
						LastTransitionTime: metav1.Now(),
						Reason:             string(corev1.PodSucceeded),
					},
					corev1.PodCondition{
						Type:               corev1.PodScheduled,
						Status:             corev1.ConditionTrue,
						LastProbeTime:      metav1.Now(),
						LastTransitionTime: metav1.Now(),
						Reason:             "",
					},
				)
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdPod)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, "")
		})
	})

	ginkgo.It("Should create a pod group on worker if admitted", func() {
		groupName := "test-group"
		podgroup := testingpod.MakePod(groupName, managerNs.Name).
			Queue(managerLq.Name).
			ManagedByKueueLabel().
			KueueFinalizer().
			KueueSchedulingGate().
			MakeGroup(3)

		for _, p := range podgroup {
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, p)).Should(gomega.Succeed())
		}

		// any pod should give the same workload Key
		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: groupName, Namespace: managerNs.Name}
		admission := utiltesting.MakeAdmission(managerCq.Name).
			PodSets(
				kueue.PodSetAssignment{
					Name:  "bf90803c",
					Count: ptr.To[int32](3),
				},
			).Obj()
		ginkgo.By("setting workload reservation in the management cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				gomega.Expect(createdWorkload.Spec.PodSets[0].Count).To(gomega.Equal(int32(3)))
				g.Expect(util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, createdWorkload, admission)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, createdWorkload, admission)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				acs := workload.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAC.Name))
				g.Expect(acs).NotTo(gomega.BeNil())
				g.Expect(acs.State).To(gomega.Equal(kueue.CheckStatePending))
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
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		pods := corev1.PodList{}
		gomega.Expect(managerTestCluster.client.List(managerTestCluster.ctx, &pods)).To(gomega.Succeed())

		ginkgo.By("finishing the worker pod", func() {
			pods := corev1.PodList{}
			gomega.Expect(worker1TestCluster.client.List(worker1TestCluster.ctx, &pods)).To(gomega.Succeed())
			for _, p := range podgroup {
				gomega.Eventually(func(g gomega.Gomega) {
					createdPod := corev1.Pod{}
					g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(p), &createdPod)).To(gomega.Succeed())
					createdPod.Status.Phase = corev1.PodSucceeded
					createdPod.Status.Conditions = append(createdPod.Status.Conditions,
						corev1.PodCondition{
							Type:   corev1.PodReadyToStartContainers,
							Status: corev1.ConditionFalse,
							Reason: "",
						},
						corev1.PodCondition{
							Type:   corev1.PodInitialized,
							Status: corev1.ConditionTrue,
							Reason: string(corev1.PodSucceeded),
						},
						corev1.PodCondition{
							Type:   corev1.PodReady,
							Status: corev1.ConditionFalse,
							Reason: string(corev1.PodSucceeded),
						},
						corev1.PodCondition{
							Type:   corev1.ContainersReady,
							Status: corev1.ConditionFalse,
							Reason: string(corev1.PodSucceeded),
						},
						corev1.PodCondition{
							Type:   corev1.PodScheduled,
							Status: corev1.ConditionTrue,
							Reason: "",
						},
					)
					g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdPod)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			}
			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, "Pods succeeded: 3/3.")
		})
	})

	ginkgo.It("Should remove the worker's workload and job after reconnect when the managers job and workload are deleted", func() {
		job := testingjob.MakeJob("job", managerNs.Name).
			Queue(kueue.LocalQueueName(managerLq.Name)).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)
		jobLookupKey := client.ObjectKeyFromObject(job)
		createdJob := &batchv1.Job{}

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

		ginkgo.By("setting workload reservation in the management cluster", func() {
			admission := utiltesting.MakeAdmission(managerCq.Name).Obj()
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, createdWorkload, admission)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
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

		ginkgo.By("breaking the connection to worker2", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster2), createdCluster)).To(gomega.Succeed())
				createdCluster.Spec.KubeConfig.Location = "bad-secret"
				g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, createdCluster)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster2), createdCluster)).To(gomega.Succeed())
				activeCondition := apimeta.FindStatusCondition(createdCluster.Status.Conditions, kueue.MultiKueueClusterActive)
				g.Expect(activeCondition).To(gomega.BeComparableTo(&metav1.Condition{
					Type:   kueue.MultiKueueClusterActive,
					Status: metav1.ConditionFalse,
					Reason: "BadConfig",
				}, util.IgnoreConditionMessage, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting workload reservation in worker1, the job is created in worker1", func() {
			admission := utiltesting.MakeAdmission(managerCq.Name).Obj()

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, createdWorkload, admission)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, jobLookupKey, createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("breaking the connection to worker1", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster1), createdCluster)).To(gomega.Succeed())
				createdCluster.Spec.KubeConfig.Location = "bad-secret"
				g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, createdCluster)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster1), createdCluster)).To(gomega.Succeed())
				activeCondition := apimeta.FindStatusCondition(createdCluster.Status.Conditions, kueue.MultiKueueClusterActive)
				g.Expect(activeCondition).To(gomega.BeComparableTo(&metav1.Condition{
					Type:   kueue.MultiKueueClusterActive,
					Status: metav1.ConditionFalse,
					Reason: "BadConfig",
				}, util.IgnoreConditionMessage, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

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

		ginkgo.By("restoring the connection to worker2", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster2), createdCluster)).To(gomega.Succeed())
				createdCluster.Spec.KubeConfig.Location = managerMultiKueueSecret2.Name
				g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, createdCluster)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster2), createdCluster)).To(gomega.Succeed())
				activeCondition := apimeta.FindStatusCondition(createdCluster.Status.Conditions, kueue.MultiKueueClusterActive)
				g.Expect(activeCondition).To(gomega.BeComparableTo(&metav1.Condition{
					Type:   kueue.MultiKueueClusterActive,
					Status: metav1.ConditionTrue,
					Reason: "Active",
				}, util.IgnoreConditionMessage, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("the worker2 wl is removed by the garbage collector", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("restoring the connection to worker1", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster1), createdCluster)).To(gomega.Succeed())
				createdCluster.Spec.KubeConfig.Location = managerMultiKueueSecret1.Name
				g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, createdCluster)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster1), createdCluster)).To(gomega.Succeed())
				activeCondition := apimeta.FindStatusCondition(createdCluster.Status.Conditions, kueue.MultiKueueClusterActive)
				g.Expect(activeCondition).To(gomega.BeComparableTo(&metav1.Condition{
					Type:   kueue.MultiKueueClusterActive,
					Status: metav1.ConditionTrue,
					Reason: "Active",
				}, util.IgnoreConditionMessage, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("the wl and job are removed on the worker1", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, jobLookupKey, createdJob)).To(utiltesting.BeNotFoundError())
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should requeue the workload with a delay when the connection to the admitting worker is lost", framework.SlowSpec, func() {
		jobSet := testingjobset.MakeJobSet("job-set", managerNs.Name).
			Queue(managerLq.Name).
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

		wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: managerNs.Name}

		admission := utiltesting.MakeAdmission(managerCq.Name).PodSets(
			kueue.PodSetAssignment{
				Name: "replicated-job-1",
			}, kueue.PodSetAssignment{
				Name: "replicated-job-2",
			},
		)

		admitWorkloadAndCheckWorkerCopies(multiKueueAC.Name, wlLookupKey, admission)

		var disconnectedTime time.Time
		ginkgo.By("breaking the connection to worker2", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster2), createdCluster)).To(gomega.Succeed())
				createdCluster.Spec.KubeConfig.Location = "bad-secret"
				g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, createdCluster)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster2), createdCluster)).To(gomega.Succeed())
				activeCondition := apimeta.FindStatusCondition(createdCluster.Status.Conditions, kueue.MultiKueueClusterActive)
				g.Expect(activeCondition).To(gomega.BeComparableTo(&metav1.Condition{
					Type:   kueue.MultiKueueClusterActive,
					Status: metav1.ConditionFalse,
					Reason: "BadConfig",
				}, util.IgnoreConditionMessage, util.IgnoreConditionTimestampsAndObservedGeneration))
				disconnectedTime = activeCondition.LastTransitionTime.Time
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("waiting for the local workload admission check state to be set to pending and quotaReservatio removed", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdWorkload := &kueue.Workload{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				acs := workload.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAC.Name))
				g.Expect(acs).To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
					Name:  kueue.AdmissionCheckReference(multiKueueAC.Name),
					State: kueue.CheckStatePending,
				}, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "Message")))

				// The transition interval should be close to testingKeepReadyTimeout (taking into account the resolution of the LastTransitionTime field)
				g.Expect(acs.LastTransitionTime.Time).To(gomega.BeComparableTo(disconnectedTime.Add(testingWorkerLostTimeout), cmpopts.EquateApproxTime(2*time.Second)))

				g.Expect(createdWorkload.Status.Conditions).ToNot(utiltesting.HaveConditionStatusTrue(kueue.WorkloadQuotaReserved))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("restoring the connection to worker2", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster2), createdCluster)).To(gomega.Succeed())
				createdCluster.Spec.KubeConfig.Location = managerMultiKueueSecret2.Name
				g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, createdCluster)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster2), createdCluster)).To(gomega.Succeed())
				activeCondition := apimeta.FindStatusCondition(createdCluster.Status.Conditions, kueue.MultiKueueClusterActive)
				g.Expect(activeCondition).To(gomega.BeComparableTo(&metav1.Condition{
					Type:   kueue.MultiKueueClusterActive,
					Status: metav1.ConditionTrue,
					Reason: "Active",
				}, util.IgnoreConditionMessage, util.IgnoreConditionTimestampsAndObservedGeneration))
				disconnectedTime = activeCondition.LastTransitionTime.Time
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("the worker2 wl is removed since the local one no longer has a reservation", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdWorkload := &kueue.Workload{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should run a RayJob on worker if admitted", func() {
		admission := utiltesting.MakeAdmission(managerCq.Name).PodSets(
			kueue.PodSetAssignment{
				Name: "head",
			}, kueue.PodSetAssignment{
				Name: "workers-group-0",
			},
		)
		rayjob := testingrayjob.MakeJob("rayjob1", managerNs.Name).
			WithSubmissionMode(rayv1.InteractiveMode).
			Queue(managerLq.Name).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, rayjob)
		wlLookupKey := types.NamespacedName{Name: workloadrayjob.GetWorkloadNameForRayJob(rayjob.Name, rayjob.UID), Namespace: managerNs.Name}
		gomega.Eventually(func(g gomega.Gomega) {
			createdWorkload := &kueue.Workload{}
			g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			g.Expect(util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, createdWorkload, admission.Obj())).To(gomega.Succeed())
		}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

		admitWorkloadAndCheckWorkerCopies(multiKueueAC.Name, wlLookupKey, admission)

		ginkgo.By("changing the status of the RayJob in the worker, updates the manager's RayJob status", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdRayJob := rayv1.RayJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(rayjob), &createdRayJob)).To(gomega.Succeed())
				createdRayJob.Status.JobDeploymentStatus = rayv1.JobDeploymentStatusRunning
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdRayJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				createdRayJob := rayv1.RayJob{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(rayjob), &createdRayJob)).To(gomega.Succeed())
				g.Expect(createdRayJob.Status.JobDeploymentStatus).To(gomega.Equal(rayv1.JobDeploymentStatusRunning))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker RayJob, the manager's wl is marked as finished and the worker2 wl removed", func() {
			finishJobReason := ""
			gomega.Eventually(func(g gomega.Gomega) {
				createdRayJob := rayv1.RayJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(rayjob), &createdRayJob)).To(gomega.Succeed())
				createdRayJob.Status.JobStatus = rayv1.JobStatusSucceeded
				createdRayJob.Status.JobDeploymentStatus = rayv1.JobDeploymentStatusComplete
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdRayJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, finishJobReason)
		})
	})

	ginkgo.It("Should run a RayCluster on worker if admitted", func() {
		admission := utiltesting.MakeAdmission(managerCq.Name).PodSets(
			kueue.PodSetAssignment{
				Name: "head",
			}, kueue.PodSetAssignment{
				Name: "workers-group-0",
			},
		)
		raycluster := testingraycluster.MakeCluster("raycluster1", managerNs.Name).
			Queue(managerLq.Name).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, raycluster)
		wlLookupKey := types.NamespacedName{Name: workloadraycluster.GetWorkloadNameForRayCluster(raycluster.Name, raycluster.UID), Namespace: managerNs.Name}
		gomega.Eventually(func(g gomega.Gomega) {
			createdWorkload := &kueue.Workload{}
			g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			g.Expect(util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, createdWorkload, admission.Obj())).To(gomega.Succeed())
		}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

		admitWorkloadAndCheckWorkerCopies(multiKueueAC.Name, wlLookupKey, admission)

		ginkgo.By("changing the status of the RayCluster in the worker, updates the manager's RayCluster status", func() {
			createdRayCluster := rayv1.RayCluster{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(raycluster), &createdRayCluster)).To(gomega.Succeed())
				createdRayCluster.Status.DesiredWorkerReplicas = 1
				createdRayCluster.Status.ReadyWorkerReplicas = 1
				createdRayCluster.Status.AvailableWorkerReplicas = 1
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdRayCluster)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(raycluster), &createdRayCluster)).To(gomega.Succeed())
				g.Expect(createdRayCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(1)))
				g.Expect(createdRayCluster.Status.ReadyWorkerReplicas).To(gomega.Equal(int32(1)))
				g.Expect(createdRayCluster.Status.AvailableWorkerReplicas).To(gomega.Equal(int32(1)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})

func admitWorkloadAndCheckWorkerCopies(acName string, wlLookupKey types.NamespacedName, admission *utiltesting.AdmissionWrapper) {
	ginkgo.By("setting workload reservation in the management cluster", func() {
		createdWorkload := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			g.Expect(util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, createdWorkload, admission.Obj())).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.By("checking the workload creation in the worker clusters", func() {
		managerWl := &kueue.Workload{}
		createdWorkload := &kueue.Workload{}
		gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
			g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.By("setting workload reservation in worker2, the workload is admitted in manager and worker1 wl is removed", func() {
		createdWorkload := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			g.Expect(util.SetQuotaReservation(worker2TestCluster.ctx, worker2TestCluster.client, createdWorkload, admission.Obj())).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			acs := workload.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(acName))
			g.Expect(acs).NotTo(gomega.BeNil())
			g.Expect(acs.State).To(gomega.Equal(kueue.CheckStateReady))
			g.Expect(acs.Message).To(gomega.Equal(`The workload got reservation on "worker2"`))
			ok, err := utiltesting.HasEventAppeared(managerTestCluster.ctx, managerTestCluster.client, corev1.Event{
				Reason:  "MultiKueue",
				Type:    corev1.EventTypeNormal,
				Message: `The workload got reservation on "worker2"`,
			})
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(ok).To(gomega.BeTrue())

			g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeComparableTo(&metav1.Condition{
				Type:    kueue.WorkloadAdmitted,
				Status:  metav1.ConditionTrue,
				Reason:  "Admitted",
				Message: "The workload is admitted",
			}, util.IgnoreConditionTimestampsAndObservedGeneration))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
}

func waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey types.NamespacedName, finishJobReason string) {
	gomega.Eventually(func(g gomega.Gomega) {
		createdWorkload := &kueue.Workload{}
		g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
		g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeComparableTo(&metav1.Condition{
			Type:    kueue.WorkloadFinished,
			Status:  metav1.ConditionTrue,
			Reason:  string(kftraining.JobSucceeded),
			Message: finishJobReason,
		}, util.IgnoreConditionTimestampsAndObservedGeneration))
	}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

	gomega.Eventually(func(g gomega.Gomega) {
		createdWorkload := &kueue.Workload{}
		g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
	}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

	gomega.Eventually(func(g gomega.Gomega) {
		createdWorkload := &kueue.Workload{}
		g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
	}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
}

func setQuotaReservationInCluster(wlLookupKey types.NamespacedName, admission *utiltesting.AdmissionWrapper) {
	ginkgo.By("setting workload reservation in the management cluster", func() {
		createdWorkload := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			g.Expect(util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, createdWorkload, admission.Obj())).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
}

func checkingTheWorkloadCreation(wlLookupKey types.NamespacedName, matcher gomegatypes.GomegaMatcher) {
	ginkgo.By("checking the workload creation in the worker clusters", func() {
		managerWl := &kueue.Workload{}
		createdWorkload := &kueue.Workload{}
		gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
			g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
		}, util.Timeout, util.Interval).Should(matcher)
	})
}
