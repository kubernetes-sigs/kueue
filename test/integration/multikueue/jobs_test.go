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
	"fmt"
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	kftrainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	gomegatypes "github.com/onsi/gomega/types"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
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
	workloadtrainjob "sigs.k8s.io/kueue/pkg/controller/jobs/trainjob"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
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
	testingtrainjob "sigs.k8s.io/kueue/pkg/util/testingjobs/trainjob"
	testingxgboostjob "sigs.k8s.io/kueue/pkg/util/testingjobs/xgboostjob"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

// sliceStabilityWindow spans several reconcile cycles so slice churn (a
// spurious replacement slice or a torn-down worker copy) has time to surface;
// util.ConsistentDuration is too short for that.
const sliceStabilityWindow = 10 * time.Second

var defaultEnabledIntegrations = sets.New(
	"batch/job", "kubeflow.org/mpijob", "ray.io/rayjob", "ray.io/raycluster",
	"jobset.x-k8s.io/jobset", "kubeflow.org/paddlejob",
	"kubeflow.org/pytorchjob", "kubeflow.org/tfjob", "kubeflow.org/xgboostjob", "kubeflow.org/jaxjob",
	"pod", "workload.codeflare.dev/appwrapper", "trainer.kubeflow.org/trainjob")

var _ = ginkgo.Describe("MultiKueue", ginkgo.Label("area:multikueue", "feature:multikueue"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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
		managerFlavor            *kueue.ResourceFlavor

		worker1Cq     *kueue.ClusterQueue
		worker1Lq     *kueue.LocalQueue
		worker1Flavor *kueue.ResourceFlavor

		worker2Cq     *kueue.ClusterQueue
		worker2Lq     *kueue.LocalQueue
		worker2Flavor *kueue.ResourceFlavor
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

		managerMultiKueueSecret1 = utiltesting.MakeSecret("multikueue1", managersConfigNamespace.Name).Data(kueue.MultiKueueConfigSecretKey, w1Kubeconfig).Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret1)

		managerMultiKueueSecret2 = utiltesting.MakeSecret("multikueue2", managersConfigNamespace.Name).Data(kueue.MultiKueueConfigSecretKey, w2Kubeconfig).Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret2)

		workerCluster1 = utiltestingapi.MakeMultiKueueCluster("worker1").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret1.Name).Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, workerCluster1)

		workerCluster2 = utiltestingapi.MakeMultiKueueCluster("worker2").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret2.Name).Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, workerCluster2)

		managerMultiKueueConfig = utiltestingapi.MakeMultiKueueConfig("multikueueconfig").Clusters(workerCluster1.Name, workerCluster2.Name).Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueConfig)

		multiKueueAC = utiltestingapi.MakeAdmissionCheck("ac1").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", managerMultiKueueConfig.Name).
			Obj()
		util.CreateAdmissionChecksAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, multiKueueAC)

		managerFlavor = utiltestingapi.MakeResourceFlavor(string(multikueueTestFlavor)).Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerFlavor)

		managerCq = utiltestingapi.MakeClusterQueue("q1").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(string(multikueueTestFlavor)).Resource(corev1.ResourceCPU, "5").Obj()).
			AdmissionChecks(kueue.AdmissionCheckReference(multiKueueAC.Name)).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, managerCq)

		managerLq = utiltestingapi.MakeLocalQueue(managerCq.Name, managerNs.Name).ClusterQueue(managerCq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, managerLq)

		worker1Flavor = utiltestingapi.MakeResourceFlavor(string(multikueueTestFlavor)).Obj()
		util.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, worker1Flavor)
		worker1Cq = utiltestingapi.MakeClusterQueue("q1").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(string(multikueueTestFlavor)).Resource(corev1.ResourceCPU, "5").Obj()).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq)
		worker1Lq = utiltestingapi.MakeLocalQueue(worker1Cq.Name, worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Lq)

		worker2Flavor = utiltestingapi.MakeResourceFlavor(string(multikueueTestFlavor)).Obj()
		util.MustCreate(worker2TestCluster.ctx, worker2TestCluster.client, worker2Flavor)
		worker2Cq = utiltestingapi.MakeClusterQueue("q1").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(string(multikueueTestFlavor)).Resource(corev1.ResourceCPU, "5").Obj()).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2Cq)
		worker2Lq = utiltestingapi.MakeLocalQueue(worker2Cq.Name, worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
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
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).
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
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).
				Obj()
			util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlLookupKey, admission)

			util.ExpectAdmissionCheckStateWithMessage(
				managerTestCluster.ctx, managerTestCluster.client, wlLookupKey,
				multiKueueAC.Name,
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
		job := testingjob.MakeJob("job-oos", managerNs.Name).
			Queue(kueue.LocalQueueName(managerLq.Name)).
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, job)).Should(gomega.Succeed())

		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).
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
				multiKueueAC.Name, kueue.CheckStateReady, `The workload was admitted on "worker1"`,
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

	ginkgo.It("Should run a job on worker if admitted (ManagedBy)", func() {
		job := testingjob.MakeJob("job", managerNs.Name).
			ManagedBy(kueue.MultiKueueControllerName).
			Queue(kueue.LocalQueueName(managerLq.Name)).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

		ginkgo.By("setting workload reservation in the management cluster", func() {
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).
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
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).
				Obj()
			util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlLookupKey, admission)

			util.ExpectAdmissionCheckStateWithMessage(
				managerTestCluster.ctx, managerTestCluster.client, wlLookupKey,
				multiKueueAC.Name,
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
				createdJob.Status.Ready = ptr.To[int32](1)
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
				createdJob.Status.Ready = ptr.To[int32](0)
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

		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("replicated-job-1").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("replicated-job-2").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
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
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("chief").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("ps").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("worker").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
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
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("master").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("worker").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
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
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("master").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("worker").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
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
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("master").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("worker").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
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
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("master").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("worker").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
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

		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("aw-0").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
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
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("launcher").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("worker").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
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
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("launcher").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("worker").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
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
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).
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
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).
				Obj()
			util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlLookupKey, admission)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.ExpectAdmissionCheckStateWithMessage(
				managerTestCluster.ctx, managerTestCluster.client, wlLookupKey,
				multiKueueAC.Name,
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
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).
			PodSets(
				utiltestingapi.MakePodSetAssignment("bf90803c").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Count(3).Obj(),
			).Obj()
		ginkgo.By("setting workload reservation in the management cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec.PodSets[0].Count).To(gomega.Equal(int32(3)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
			util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlLookupKey, admission)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.ExpectAdmissionCheckStateWithMessage(
				managerTestCluster.ctx, managerTestCluster.client, wlLookupKey,
				multiKueueAC.Name,
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

	ginkgo.It("Should handle a pod group admission with extra non-multikueue admission checks defined", func() {
		var testAc, testAc2 *kueue.AdmissionCheck

		ginkgo.By("creating non-multikueue ACs and adding them to the CQ", func() {
			testAc = utiltestingapi.MakeAdmissionCheck("test-ac").
				ControllerName("test-controller").
				Active(metav1.ConditionTrue).
				Obj()
			util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, testAc)

			testAc2 = utiltestingapi.MakeAdmissionCheck("test-ac-2").
				ControllerName("test-controller-2").
				Active(metav1.ConditionTrue).
				Obj()
			util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, testAc2)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(managerCq), managerCq)).To(gomega.Succeed())
				managerCq.Spec.AdmissionChecksStrategy.AdmissionChecks = append(
					managerCq.Spec.AdmissionChecksStrategy.AdmissionChecks,
					kueue.AdmissionCheckStrategyRule{Name: kueue.AdmissionCheckReference(testAc.Name)},
					kueue.AdmissionCheckStrategyRule{Name: kueue.AdmissionCheckReference(testAc2.Name)},
				)
				g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, managerCq)).To(gomega.Succeed())
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
		})

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

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: groupName, Namespace: managerNs.Name}

		ginkgo.By("checking workload in manager is set up correctly", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(admissioncheck.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAC.Name))).
					To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
						Name:  kueue.AdmissionCheckReference(multiKueueAC.Name),
						State: kueue.CheckStatePending,
					}, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "Message")))
				g.Expect(admissioncheck.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(testAc.Name))).
					To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
						Name:  kueue.AdmissionCheckReference(testAc.Name),
						State: kueue.CheckStatePending,
					}, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "Message")))
				g.Expect(admissioncheck.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(testAc2.Name))).
					To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
						Name:  kueue.AdmissionCheckReference(testAc2.Name),
						State: kueue.CheckStatePending,
					}, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "Message")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).
			PodSets(
				utiltestingapi.MakePodSetAssignment("bf90803c").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Count(3).Obj(),
			).Obj()
		ginkgo.By("setting workload reservation in the management cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				gomega.Expect(createdWorkload.Spec.PodSets[0].Count).To(gomega.Equal(int32(3)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission)
		})

		ginkgo.By("checking the workload creation in the worker clusters didn't happen due to pending non-multikueue ACs", func() {
			managerWl := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Update first pending AC to Ready", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				updatedWorkload := &kueue.Workload{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, updatedWorkload)).To(gomega.Succeed())
				acs := admissioncheck.FindAdmissionCheck(updatedWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(testAc.Name))
				g.Expect(acs).NotTo(gomega.BeNil())
				acs.State = kueue.CheckStateReady
				acs.Message = "Test AC is ready"
				g.Expect(managerTestCluster.client.Status().Update(managerTestCluster.ctx, updatedWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking the workload creation in the worker clusters still didn't happen due to second pending AC", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Update second pending AC to Ready", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				updatedWorkload := &kueue.Workload{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, updatedWorkload)).To(gomega.Succeed())
				acs := admissioncheck.FindAdmissionCheck(updatedWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(testAc2.Name))
				g.Expect(acs).NotTo(gomega.BeNil())
				acs.State = kueue.CheckStateReady
				acs.Message = "Test AC 2 is ready"
				g.Expect(managerTestCluster.client.Status().Update(managerTestCluster.ctx, updatedWorkload)).To(gomega.Succeed())
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
			util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlLookupKey, admission)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				acs := admissioncheck.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAC.Name))
				g.Expect(acs).NotTo(gomega.BeNil())
				g.Expect(acs.State).To(gomega.Equal(kueue.CheckStateReady))
				g.Expect(acs.Message).To(gomega.Equal(`The workload was admitted on "worker1"`))
				g.Expect(apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeTrue())
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			util.ExpectEventAppeared(managerTestCluster.ctx, managerTestCluster.client, eventsv1.Event{
				Reason: "MultiKueue",
				Type:   corev1.EventTypeNormal,
				Note:   `The workload was admitted on "worker1"`,
			})

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
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).
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

		restoreConnectionToWorker2 := util.BreakConnection(managerTestCluster.ctx, managerTestCluster.client, workerCluster2, managersConfigNamespace.Name)

		ginkgo.By("setting workload reservation in worker1, the job is created in worker1", func() {
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).
				Obj()
			util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlLookupKey, admission)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, jobLookupKey, createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		restoreConnectionToWorker1 := util.BreakConnection(managerTestCluster.ctx, managerTestCluster.client, workerCluster1, managersConfigNamespace.Name)

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
		job := testingjob.MakeJob("job", managerNs.Name).
			Queue(kueue.LocalQueueName(managerLq.Name)).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)
		jobLookupKey := client.ObjectKeyFromObject(job)

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).
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

		restoreConnectionToWorker1 := util.BreakConnection(managerTestCluster.ctx, managerTestCluster.client, workerCluster1, managersConfigNamespace.Name)

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

		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("replicated-job-1").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("replicated-job-2").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)

		admitWorkloadAndCheckWorkerCopies(multiKueueAC.Name, wlLookupKey, admission)

		restoreConnectionToWorker2 := util.BreakConnection(managerTestCluster.ctx, managerTestCluster.client, workerCluster2, managersConfigNamespace.Name)

		ginkgo.By("waiting for the local workload admission check state to be set to pending and quotaReservatio removed", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdWorkload := &kueue.Workload{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				acs := admissioncheck.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAC.Name))
				g.Expect(acs).To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
					Name:       kueue.AdmissionCheckReference(multiKueueAC.Name),
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
		keys := admitJobsAndAgeAdmissionCheck(managerNs.Name, managerLq.Name, managerCq.Name, multiKueueAC.Name, 3)

		restoreConnection := util.BreakConnection(managerTestCluster.ctx, managerTestCluster.client, workerCluster2, managersConfigNamespace.Name)
		restoreConnection()

		ginkgo.By("no admitted workload is evicted after the brief connection loss", func() {
			expectNoEviction(keys, multiKueueAC.Name, testingWorkerLostTimeout*2)
		})
	})

	ginkgo.It("Should not immediately evict but eventually retry admitted workloads after a manager restart while the admitting worker is unreachable", framework.SlowSpec, func() {
		keys := admitJobsAndAgeAdmissionCheck(managerNs.Name, managerLq.Name, managerCq.Name, multiKueueAC.Name, 3)

		restoreConnection := util.BreakConnection(managerTestCluster.ctx, managerTestCluster.client, workerCluster2, managersConfigNamespace.Name)

		ginkgo.By("restarting the manager while worker2 is unreachable", func() {
			managerTestCluster.fwk.StopManager(managerTestCluster.ctx)
			managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
				managerAndMultiKueueSetup(ctx, mgr, 2*time.Second, defaultEnabledIntegrations, config.MultiKueueDispatcherModeAllAtOnce)
			})
		})

		ginkgo.By("the restarted manager re-anchors the grace and does not immediately evict", func() {
			expectNoEviction(keys, multiKueueAC.Name, testingWorkerLostTimeout*2/3)
		})

		ginkgo.By("the re-anchored grace is finite, so the workloads are eventually retried", func() {
			expectEventuallyRetried(keys, multiKueueAC.Name, testingWorkerLostTimeout*4)
		})

		restoreConnection()
	})

	ginkgo.It("Should run a RayJob on worker if admitted", func() {
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("head").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("workers-group-0").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)
		rayjob := testingrayjob.MakeJob("rayjob1", managerNs.Name).
			WithSubmissionMode(rayv1.InteractiveMode).
			Queue(managerLq.Name).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, rayjob)
		wlLookupKey := types.NamespacedName{Name: workloadrayjob.GetWorkloadNameForRayJob(rayjob.Name, rayjob.UID), Namespace: managerNs.Name}
		util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission.Obj())

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
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("head").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("workers-group-0").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)
		raycluster := testingraycluster.MakeCluster("raycluster1", managerNs.Name).
			Queue(managerLq.Name).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, raycluster)
		wlLookupKey := types.NamespacedName{Name: workloadraycluster.GetWorkloadNameForRayCluster(raycluster.Name, raycluster.UID), Namespace: managerNs.Name}
		util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission.Obj())

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

	// Control group for the fixed-size autoscaling stability test below: identical
	// elastic RayCluster but WITHOUT enableInTreeAutoscaling. If this one stays
	// stable while the autoscaling one churns, the trigger is autoscaling-specific.
	ginkgo.It("Should keep an admitted elastic RayCluster without autoscaling (fixed-size control) stable", func() {
		manager := managerTestCluster
		worker2 := worker2TestCluster

		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)

		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("head").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("workers-group-0").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)

		raycluster := testingraycluster.MakeCluster("raycluster-elastic-control", managerNs.Name).
			Queue(managerLq.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			FirstWorkerGroupReplicas(1, 1, 1).
			ElasticSchedulingGates().
			Obj()
		util.MustCreate(manager.ctx, manager.client, raycluster)

		createdCluster := &rayv1.RayCluster{}
		gomega.Expect(manager.client.Get(manager.ctx, client.ObjectKeyFromObject(raycluster), createdCluster)).To(gomega.Succeed())
		wlLookupKey := types.NamespacedName{
			Name: jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(
				createdCluster.Name, createdCluster.UID, rayv1.GroupVersion.WithKind("RayCluster"), createdCluster.GetGeneration()),
			Namespace: createdCluster.Namespace,
		}

		admitWorkloadAndCheckWorkerCopies(multiKueueAC.Name, wlLookupKey, admission)

		ginkgo.By("the worker RayCluster is created and unsuspended on worker2", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				workerCluster := &rayv1.RayCluster{}
				g.Expect(worker2.client.Get(worker2.ctx, client.ObjectKeyFromObject(raycluster), workerCluster)).To(gomega.Succeed())
				g.Expect(ptr.Deref(workerCluster.Spec.Suspend, false)).To(gomega.BeFalse())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("no resize was requested, so the admitted slice and its worker copy remain stable", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				managerWl := &kueue.Workload{}
				g.Expect(manager.client.Get(manager.ctx, wlLookupKey, managerWl)).To(gomega.Succeed(), "the admitted workload slice must not disappear from the manager")
				g.Expect(workloadfinish.IsFinished(managerWl)).To(gomega.BeFalse(), "the admitted workload slice must not be declared finished")

				remoteWl := &kueue.Workload{}
				g.Expect(worker2.client.Get(worker2.ctx, wlLookupKey, remoteWl)).To(gomega.Succeed(), "the remote workload must not be deleted from the worker")

				workerCluster := &rayv1.RayCluster{}
				g.Expect(worker2.client.Get(worker2.ctx, client.ObjectKeyFromObject(raycluster), workerCluster)).To(gomega.Succeed())
				g.Expect(ptr.Deref(workerCluster.Spec.Suspend, false)).To(gomega.BeFalse(), "the worker RayCluster must not get suspended")
			}, sliceStabilityWindow, util.Interval).Should(gomega.Succeed())
		})
	})

	// A MultiKueue-dispatched elastic RayCluster with in-tree autoscaling and a
	// fixed-size worker group (replicas == minReplicas == maxReplicas): the
	// autoscaler running on the worker has no room to resize, so no slice
	// replacement should ever happen after admission. The autoscaler sidecar is
	// accounted on both clusters, so the prebuilt workload stays in sync.
	ginkgo.It("Should keep an admitted elastic RayCluster with a fixed-size autoscaling worker group stable", func() {
		manager := managerTestCluster
		worker2 := worker2TestCluster

		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)

		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("head").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("workers-group-0").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)

		raycluster := testingraycluster.MakeCluster("raycluster-elastic-fixed", managerNs.Name).
			Queue(managerLq.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			WithEnableAutoscaling(ptr.To(true)).
			FirstWorkerGroupReplicas(1, 1, 1).
			ElasticSchedulingGates().
			Obj()
		util.MustCreate(manager.ctx, manager.client, raycluster)

		createdCluster := &rayv1.RayCluster{}
		gomega.Expect(manager.client.Get(manager.ctx, client.ObjectKeyFromObject(raycluster), createdCluster)).To(gomega.Succeed())
		wlLookupKey := types.NamespacedName{
			Name: jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(
				createdCluster.Name, createdCluster.UID, rayv1.GroupVersion.WithKind("RayCluster"), createdCluster.GetGeneration()),
			Namespace: createdCluster.Namespace,
		}

		admitWorkloadAndCheckWorkerCopies(multiKueueAC.Name, wlLookupKey, admission)

		ginkgo.By("the manager RayCluster is unsuspended after admission", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(manager.client.Get(manager.ctx, client.ObjectKeyFromObject(raycluster), createdCluster)).To(gomega.Succeed())
				g.Expect(ptr.Deref(createdCluster.Spec.Suspend, false)).To(gomega.BeFalse())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("the worker RayCluster is created and unsuspended on worker2", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				workerCluster := &rayv1.RayCluster{}
				g.Expect(worker2.client.Get(worker2.ctx, client.ObjectKeyFromObject(raycluster), workerCluster)).To(gomega.Succeed())
				g.Expect(ptr.Deref(workerCluster.Spec.Suspend, false)).To(gomega.BeFalse())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("no resize was requested, so the admitted slice and its worker copy remain stable", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				managerWl := &kueue.Workload{}
				g.Expect(manager.client.Get(manager.ctx, wlLookupKey, managerWl)).To(gomega.Succeed(), "the admitted workload slice must not disappear from the manager")
				g.Expect(workloadfinish.IsFinished(managerWl)).To(gomega.BeFalse(), "the admitted workload slice must not be declared finished")

				remoteWl := &kueue.Workload{}
				g.Expect(worker2.client.Get(worker2.ctx, wlLookupKey, remoteWl)).To(gomega.Succeed(), "the remote workload must not be deleted from the worker")

				workerCluster := &rayv1.RayCluster{}
				g.Expect(worker2.client.Get(worker2.ctx, client.ObjectKeyFromObject(raycluster), workerCluster)).To(gomega.Succeed())
				g.Expect(ptr.Deref(workerCluster.Spec.Suspend, false)).To(gomega.BeFalse(), "the worker RayCluster must not get suspended")
			}, sliceStabilityWindow, util.Interval).Should(gomega.Succeed())
		})
	})

	// Exercises the worker-to-manager reverse sync end to end: the autoscaler-driven
	// worker resize is written back onto the manager RayCluster, the manager's
	// slicing machinery produces a count-2 replacement slice, and once that slice is
	// admitted the MultiKueue handover completes — the prebuilt label is repointed
	// and the worker RayCluster keeps the autoscaled size. The worker's jobframework
	// tolerates the transient scale-up mismatch during the handover instead of
	// finishing the remote workload OutOfSync (see jobResizeAgainstWorkloadPending).
	//
	// This suite has no running scheduler, so the replacement slice is admitted
	// manually via SetQuotaReservation, exactly as admitWorkloadAndCheckWorkerCopies
	// does for the initial slice; on a real cluster the scheduler admits it.
	ginkgo.It("Should reflect an autoscaler-driven resize of an elastic RayCluster on the manager", func() {
		manager := managerTestCluster
		worker2 := worker2TestCluster

		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)

		rayClusterGVK := rayv1.GroupVersion.WithKind("RayCluster")
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("head").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("workers-group-0").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)

		raycluster := testingraycluster.MakeCluster("raycluster-autoscale", managerNs.Name).
			Queue(managerLq.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			WithEnableAutoscaling(ptr.To(true)).
			FirstWorkerGroupReplicas(1, 1, 3).
			ElasticSchedulingGates().
			Obj()
		util.MustCreate(manager.ctx, manager.client, raycluster)

		createdCluster := &rayv1.RayCluster{}
		gomega.Expect(manager.client.Get(manager.ctx, client.ObjectKeyFromObject(raycluster), createdCluster)).To(gomega.Succeed())
		wlKeyForGeneration := func(generation int64) types.NamespacedName {
			return types.NamespacedName{
				Name: jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(
					createdCluster.Name, createdCluster.UID, rayClusterGVK, generation),
				Namespace: createdCluster.Namespace,
			}
		}
		wlLookupKey := wlKeyForGeneration(createdCluster.GetGeneration())

		admitWorkloadAndCheckWorkerCopies(multiKueueAC.Name, wlLookupKey, admission)

		ginkgo.By("simulating the Ray autoscaler on the worker: scale workers-group-0 from 1 to 2", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				rc := &rayv1.RayCluster{}
				g.Expect(worker2.client.Get(worker2.ctx, client.ObjectKeyFromObject(raycluster), rc)).To(gomega.Succeed())
				rc.Spec.WorkerGroupSpecs[0].Replicas = ptr.To[int32](2)
				g.Expect(worker2.client.Update(worker2.ctx, rc)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("the autoscaled size is written back onto the manager's RayCluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				rc := &rayv1.RayCluster{}
				g.Expect(manager.client.Get(manager.ctx, client.ObjectKeyFromObject(raycluster), rc)).To(gomega.Succeed())
				g.Expect(ptr.Deref(rc.Spec.WorkerGroupSpecs[0].Replicas, -1)).To(gomega.BeEquivalentTo(int32(2)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		var newWlKey types.NamespacedName
		ginkgo.By("the manager created a count-2 replacement slice", func() {
			newWlKey = expectReplacementSlice(managerNs.Name, "workers-group-0", 2)
		})

		// The real scheduler is not running in this suite; admit the replacement
		// slice manually, exactly as admitWorkloadAndCheckWorkerCopies does for the
		// initial slice, so the product handover (MultiKueue dispatch, prebuilt
		// label repoint, old-slice cleanup) can run.
		ginkgo.By("admitting the replacement slice on the manager and worker2", func() {
			util.SetQuotaReservation(manager.ctx, manager.client, newWlKey, admission.Obj())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2.client.Get(worker2.ctx, newWlKey, &kueue.Workload{})).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.SetQuotaReservation(worker2.ctx, worker2.client, newWlKey, admission.Obj())
		})

		ginkgo.By("the replacement slice is admitted and the worker RayCluster keeps the autoscaled size with the repointed prebuilt label", func() {
			util.ExpectAdmissionCheckStateWithMessage(
				manager.ctx, manager.client, newWlKey,
				multiKueueAC.Name,
				kueue.CheckStateReady,
				`The workload was admitted on "worker2"`,
			)
			gomega.Eventually(func(g gomega.Gomega) {
				rc := &rayv1.RayCluster{}
				g.Expect(worker2.client.Get(worker2.ctx, client.ObjectKeyFromObject(raycluster), rc)).To(gomega.Succeed())
				g.Expect(ptr.Deref(rc.Spec.WorkerGroupSpecs[0].Replicas, -1)).To(gomega.BeEquivalentTo(int32(2)))
				g.Expect(jobframework.PrebuiltWorkloadNameFor(rc)).To(gomega.Equal(newWlKey.Name))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	// Scale-DOWN counterpart of the reverse-sync test above. A scale-down is
	// handled differently by the slicing machinery: the existing slice's count is
	// updated in place (EnsureWorkloadSlices ScaledDown branch), so no new slice is
	// created and no prebuilt-label repoint is needed. Verifies the write-back
	// lowers the manager count on the same admitted slice and the worker RayCluster
	// stays up.
	ginkgo.It("Should reflect an autoscaler-driven scale-down of an elastic RayCluster on the manager", func() {
		manager := managerTestCluster
		worker2 := worker2TestCluster

		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)

		rayClusterGVK := rayv1.GroupVersion.WithKind("RayCluster")
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("head").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("workers-group-0").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)

		raycluster := testingraycluster.MakeCluster("raycluster-autoscale-down", managerNs.Name).
			Queue(managerLq.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			WithEnableAutoscaling(ptr.To(true)).
			FirstWorkerGroupReplicas(2, 1, 3).
			ElasticSchedulingGates().
			Obj()
		util.MustCreate(manager.ctx, manager.client, raycluster)

		createdCluster := &rayv1.RayCluster{}
		gomega.Expect(manager.client.Get(manager.ctx, client.ObjectKeyFromObject(raycluster), createdCluster)).To(gomega.Succeed())
		wlLookupKey := types.NamespacedName{
			Name: jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(
				createdCluster.Name, createdCluster.UID, rayClusterGVK, createdCluster.GetGeneration()),
			Namespace: createdCluster.Namespace,
		}

		admitWorkloadAndCheckWorkerCopies(multiKueueAC.Name, wlLookupKey, admission)

		ginkgo.By("simulating the Ray autoscaler on the worker: scale workers-group-0 down from 2 to 1", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				rc := &rayv1.RayCluster{}
				g.Expect(worker2.client.Get(worker2.ctx, client.ObjectKeyFromObject(raycluster), rc)).To(gomega.Succeed())
				rc.Spec.WorkerGroupSpecs[0].Replicas = ptr.To[int32](1)
				g.Expect(worker2.client.Update(worker2.ctx, rc)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("the scaled-down size is written back onto the manager's RayCluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				rc := &rayv1.RayCluster{}
				g.Expect(manager.client.Get(manager.ctx, client.ObjectKeyFromObject(raycluster), rc)).To(gomega.Succeed())
				g.Expect(ptr.Deref(rc.Spec.WorkerGroupSpecs[0].Replicas, -1)).To(gomega.BeEquivalentTo(int32(1)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("the admitted slice's count is lowered in place (no new slice) and stays admitted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				wl := &kueue.Workload{}
				g.Expect(manager.client.Get(manager.ctx, wlLookupKey, wl)).To(gomega.Succeed(), "the originally admitted slice must remain (scale-down updates it in place)")
				g.Expect(workloadfinish.IsFinished(wl)).To(gomega.BeFalse(), "the slice must not be finished on scale-down")
				g.Expect(workload.ExtractPodSetCountsFromWorkload(wl)["workers-group-0"]).To(gomega.BeEquivalentTo(int32(1)))
				g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("the worker RayCluster keeps running at the scaled-down size", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				rc := &rayv1.RayCluster{}
				g.Expect(worker2.client.Get(worker2.ctx, client.ObjectKeyFromObject(raycluster), rc)).To(gomega.Succeed(), "worker RayCluster must not be torn down on scale-down")
				g.Expect(ptr.Deref(rc.Spec.Suspend, false)).To(gomega.BeFalse())
				g.Expect(ptr.Deref(rc.Spec.WorkerGroupSpecs[0].Replicas, -1)).To(gomega.BeEquivalentTo(int32(1)))
			}, sliceStabilityWindow, util.Interval).Should(gomega.Succeed())
		})
	})

	// Worker-side autoscaling for a RayJob: the autoscaler resizes the child
	// RayCluster KubeRay created on the worker (both simulated here, since
	// KubeRay does not run in this suite). The MultiKueue workload controller
	// reads the child's per-group counts and generation and reflects them onto
	// the manager RayJob as annotations; the manager's PodSet derivation falls
	// back to them (the child never exists on the manager) and produces a
	// count-2 replacement slice. The suite runs no scheduler, so the replacement
	// slice is admitted manually, after which the prebuilt label is repointed.
	ginkgo.It("Should reflect an autoscaler-driven resize of a RayJob's child RayCluster on the manager", func() {
		manager := managerTestCluster
		worker2 := worker2TestCluster

		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)

		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("head").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("workers-group-0").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)

		rayjob := testingrayjob.MakeJob("rayjob-autoscale", managerNs.Name).
			WithSubmissionMode(rayv1.InteractiveMode).
			Queue(managerLq.Name).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			EnableInTreeAutoscaling().
			MaxWorkerReplicas(3).
			Obj()
		util.MustCreate(manager.ctx, manager.client, rayjob)

		createdRayJob := &rayv1.RayJob{}
		gomega.Expect(manager.client.Get(manager.ctx, client.ObjectKeyFromObject(rayjob), createdRayJob)).To(gomega.Succeed())
		wlLookupKey := types.NamespacedName{
			Name: jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(
				createdRayJob.Name, createdRayJob.UID, rayv1.GroupVersion.WithKind("RayJob"), createdRayJob.GetGeneration()),
			Namespace: createdRayJob.Namespace,
		}

		admitWorkloadAndCheckWorkerCopies(multiKueueAC.Name, wlLookupKey, admission)

		childKey := types.NamespacedName{Name: "rayjob-autoscale-raycluster", Namespace: managerNs.Name}
		ginkgo.By("simulating KubeRay on worker2: create the child RayCluster and record it in the RayJob status", func() {
			child := testingraycluster.MakeCluster(childKey.Name, childKey.Namespace).
				FirstWorkerGroupReplicas(1, 1, 3).
				Obj()
			util.MustCreate(worker2.ctx, worker2.client, child)
			gomega.Eventually(func(g gomega.Gomega) {
				workerRayJob := &rayv1.RayJob{}
				g.Expect(worker2.client.Get(worker2.ctx, client.ObjectKeyFromObject(rayjob), workerRayJob)).To(gomega.Succeed())
				workerRayJob.Status.RayClusterName = childKey.Name
				g.Expect(worker2.client.Status().Update(worker2.ctx, workerRayJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		var childRevision string
		ginkgo.By("simulating the Ray autoscaler on worker2: scale the child's workers-group-0 from 1 to 2", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				child := &rayv1.RayCluster{}
				g.Expect(worker2.client.Get(worker2.ctx, childKey, child)).To(gomega.Succeed())
				child.Spec.WorkerGroupSpecs[0].Replicas = ptr.To[int32](2)
				g.Expect(worker2.client.Update(worker2.ctx, child)).To(gomega.Succeed())
				childRevision = fmt.Sprintf("%s-%d", child.UID, child.Generation)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		// KubeRay reflects the child's status into the RayJob's
		// status.rayClusterStatus; that RayJob update is what triggers the
		// MultiKueue remote watch (the child itself carries no prebuilt-workload
		// marker, so its events do not map to a workload). Simulate it.
		ginkgo.By("simulating KubeRay on worker2: reflect the child's status into the RayJob status", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				workerRayJob := &rayv1.RayJob{}
				g.Expect(worker2.client.Get(worker2.ctx, client.ObjectKeyFromObject(rayjob), workerRayJob)).To(gomega.Succeed())
				workerRayJob.Status.RayClusterStatus.DesiredWorkerReplicas = 2
				g.Expect(worker2.client.Status().Update(worker2.ctx, workerRayJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("the child's counts and revision are reflected onto the manager RayJob as annotations", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				managerRayJob := &rayv1.RayJob{}
				g.Expect(manager.client.Get(manager.ctx, client.ObjectKeyFromObject(rayjob), managerRayJob)).To(gomega.Succeed())
				g.Expect(managerRayJob.Annotations[workloadraycluster.MultiKueueRuntimePodSetReplicaSizesAnnotation]).
					To(gomega.Equal(`[{"name":"workers-group-0","count":2}]`))
				g.Expect(managerRayJob.Annotations[workloadraycluster.RayClusterGenerationAnnotation]).
					To(gomega.Equal(childRevision))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		var newWlKey types.NamespacedName
		ginkgo.By("the manager created a count-2 replacement slice", func() {
			newWlKey = expectReplacementSlice(managerNs.Name, "workers-group-0", 2)
		})

		// The real scheduler is not running in this suite; admit the replacement
		// slice manually so the product handover can run.
		ginkgo.By("admitting the replacement slice on the manager and worker2", func() {
			util.SetQuotaReservation(manager.ctx, manager.client, newWlKey, admission.Obj())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2.client.Get(worker2.ctx, newWlKey, &kueue.Workload{})).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.SetQuotaReservation(worker2.ctx, worker2.client, newWlKey, admission.Obj())
		})

		ginkgo.By("the replacement slice is admitted and the worker RayJob keeps its child with the repointed prebuilt label", func() {
			util.ExpectAdmissionCheckStateWithMessage(
				manager.ctx, manager.client, newWlKey,
				multiKueueAC.Name,
				kueue.CheckStateReady,
				`The workload was admitted on "worker2"`,
			)
			gomega.Eventually(func(g gomega.Gomega) {
				workerRayJob := &rayv1.RayJob{}
				g.Expect(worker2.client.Get(worker2.ctx, client.ObjectKeyFromObject(rayjob), workerRayJob)).To(gomega.Succeed())
				g.Expect(jobframework.PrebuiltWorkloadNameFor(workerRayJob)).To(gomega.Equal(newWlKey.Name))
				g.Expect(workerRayJob.Spec.Suspend).To(gomega.BeFalse(), "the worker RayJob must not get suspended mid-handover")
				child := &rayv1.RayCluster{}
				g.Expect(worker2.client.Get(worker2.ctx, childKey, child)).To(gomega.Succeed(), "the child RayCluster must survive the handover")
				g.Expect(ptr.Deref(child.Spec.WorkerGroupSpecs[0].Replicas, -1)).To(gomega.BeEquivalentTo(int32(2)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should run a TrainJob on worker if admitted", func() {
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("node").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)
		testJobSet := testingjobset.MakeJobSet("", "").ReplicatedJobs(
			testingjobset.ReplicatedJobRequirements{
				Name:     "node",
				Replicas: 1,
			}).
			Obj()
		testCtr := testingtrainjob.MakeClusterTrainingRuntime("test", testJobSet.Spec)
		trainJob := testingtrainjob.MakeTrainJob("trainjob1", managerNs.Name).RuntimeRef(kftrainer.RuntimeRef{
			APIGroup: new("trainer.kubeflow.org"),
			Name:     "test",
			Kind:     new("ClusterTrainingRuntime"),
		}).
			Queue(managerLq.Name).
			Obj()

		util.MustCreate(worker2TestCluster.ctx, worker2TestCluster.client, testCtr.DeepCopy())
		util.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, testCtr.DeepCopy())
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, testCtr)
		util.MustCreateWithRetry(managerTestCluster.ctx, managerTestCluster.client, trainJob)
		wlLookupKey := types.NamespacedName{Name: workloadtrainjob.GetWorkloadNameForTrainJob(trainJob.Name, trainJob.UID), Namespace: managerNs.Name}
		gomega.Eventually(func(g gomega.Gomega) {
			createdWorkload := &kueue.Workload{}
			g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
		}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())

		util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission.Obj())
		admitWorkloadAndCheckWorkerCopies(multiKueueAC.Name, wlLookupKey, admission)

		ginkgo.By("changing the status of the TrainJob in the worker, updates the manager's TrainJob status", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdTrainJob := kftrainer.TrainJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(trainJob), &createdTrainJob)).To(gomega.Succeed())
				createdTrainJob.Status.JobsStatus = []kftrainer.JobStatus{
					testingtrainjob.MakeJobStatus("foo").Obj(),
				}
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdTrainJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				createdTrainJob := kftrainer.TrainJob{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(trainJob), &createdTrainJob)).To(gomega.Succeed())
				g.Expect(createdTrainJob.Status.JobsStatus).To(gomega.HaveLen(1))
				g.Expect(createdTrainJob.Status.JobsStatus[0].Name).To(gomega.Equal("foo"))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker TrainJob, the manager's wl is marked as finished and the worker2 wl removed", func() {
			finishJobReason := ""
			gomega.Eventually(func(g gomega.Gomega) {
				createdTrainJob := kftrainer.TrainJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(trainJob), &createdTrainJob)).To(gomega.Succeed())
				apimeta.SetStatusCondition(&createdTrainJob.Status.Conditions, metav1.Condition{
					Type:   kftrainer.TrainJobComplete,
					Status: metav1.ConditionTrue,
					Reason: "ByTest",
				})
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdTrainJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, finishJobReason)
		})
	})

	ginkgo.It("Should run an ElasticJob on worker if admitted", func() {
		manager := managerTestCluster
		worker1 := worker1TestCluster
		worker2 := worker2TestCluster

		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)

		jobGVK := batchv1.SchemeGroupVersion.WithKind("Job")

		getJob := func(ctx context.Context, clnt client.Client, job *batchv1.Job) {
			ginkgo.GinkgoHelper()
			gomega.Expect(clnt.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
		}
		getWorkloadKey := func(job *batchv1.Job) types.NamespacedName {
			ginkgo.GinkgoHelper()
			getJob(manager.ctx, manager.client, job)
			return types.NamespacedName{Name: jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(job.Name, job.UID, jobGVK, job.GetGeneration()), Namespace: job.Namespace}
		}
		getWorkload := func(g gomega.Gomega, ctx context.Context, clnt client.Client, key types.NamespacedName) *kueue.Workload {
			ginkgo.GinkgoHelper()
			workload := &kueue.Workload{}
			g.Expect(clnt.Get(ctx, key, workload)).To(gomega.Succeed())
			return workload
		}

		job := testingjob.MakeJob("job", managerNs.Name).
			Parallelism(1).
			Completions(2).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Queue(kueue.LocalQueueName(managerLq.Name)).
			Obj()
		util.MustCreate(manager.ctx, manager.client, job)

		ginkgo.By("observe: the job is created in the manager cluster", func() {
			getJob(manager.ctx, manager.client, job)
			gomega.Expect(job.Spec.Suspend).To(gomega.Equal(new(true)))
		})

		ginkgo.By("observe: a new workload is created in the manager cluster")
		workloadKey := getWorkloadKey(job)
		gomega.Eventually(func(g gomega.Gomega) {
			getWorkload(g, manager.ctx, manager.client, workloadKey)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("admit workload on the manager cluster")
		util.SetQuotaReservation(manager.ctx, manager.client, workloadKey,
			utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).Obj())

		ginkgo.By("observe: workload is created on all worker clusters", func() {
			localWorkload := getWorkload(gomega.Default, manager.ctx, manager.client, workloadKey)
			gomega.Eventually(func(g gomega.Gomega) {
				workload := getWorkload(g, worker1.ctx, worker1.client, workloadKey)
				g.Expect(workload.Spec).To(gomega.BeComparableTo(localWorkload.Spec))
				workload = getWorkload(g, worker2.ctx, worker2.client, workloadKey)
				g.Expect(workload.Spec).To(gomega.BeComparableTo(localWorkload.Spec))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("admit the workload on the worker1 cluster")
		util.SetQuotaReservation(worker1.ctx, worker1.client, workloadKey,
			utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).Obj())

		ginkgo.By("observe: the local workload admission check and local events reflect reservation on the worker1 cluster")
		util.ExpectAdmissionCheckStateWithMessage(
			manager.ctx, manager.client, workloadKey,
			multiKueueAC.Name,
			kueue.CheckStateReady,
			`The workload was admitted on "worker1"`,
		)
		util.ExpectEventAppeared(manager.ctx, manager.client, eventsv1.Event{
			Reason: "MultiKueue",
			Type:   corev1.EventTypeNormal,
			Note:   `The workload was admitted on "worker1"`,
		})

		ginkgo.By("observe: job is synced to the worker1 cluster and is active", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				remoteJob := job.DeepCopy()
				getJob(worker1.ctx, worker1.client, remoteJob)
				g.Expect(remoteJob.Spec.Suspend).To(gomega.Equal(new(false)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("observe: the workload is removed from the worker2 cluster")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(worker2.client.Get(worker2.ctx, workloadKey, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("observe: there are no jobs in the worker2 cluster", func() {
			list := &batchv1.JobList{}
			gomega.Expect(worker2.client.List(worker2.ctx, list, client.InNamespace(job.Namespace))).To(gomega.Succeed())
			gomega.Expect(list.Items).To(gomega.BeEmpty())
		})

		ginkgo.By("observe: job is no longer suspended in the manager cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				getJob(manager.ctx, manager.client, job)
				g.Expect(job.Spec.Suspend).To(gomega.Equal(new(false)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		/*
			Scale-up Section
		*/

		ginkgo.By("scale-up the job", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				getJob(manager.ctx, manager.client, job)
				job.Spec.Parallelism = new(int32(2))
				g.Expect(manager.client.Update(manager.ctx, job)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("observe: a new workload slice is created")
		newWorkloadKey := getWorkloadKey(job)
		gomega.Eventually(func(g gomega.Gomega) {
			getWorkload(g, manager.ctx, manager.client, newWorkloadKey)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("copy clusterName from the old workload to the new workload", func() {
			oldWorkload := getWorkload(gomega.Default, manager.ctx, manager.client, workloadKey)
			newWorkload := getWorkload(gomega.Default, manager.ctx, manager.client, newWorkloadKey)
			// This step is done by the scheduler during the new slice admission and the old slice replacement.
			// Since we are not "running" scheduler for this test suit, we need to "emulate" this step.
			newWorkload.Status.ClusterName = oldWorkload.Status.ClusterName
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(manager.client.Status().Update(manager.ctx, newWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			newWorkload = getWorkload(gomega.Default, manager.ctx, manager.client, newWorkloadKey)
			gomega.Expect(newWorkload.Status.ClusterName).Should(gomega.BeEquivalentTo(oldWorkload.Status.ClusterName))
		})

		ginkgo.By("admit the new workload and finish the old workload in the manager cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				oldWorkload := getWorkload(g, manager.ctx, manager.client, workloadKey)
				g.Expect(workloadfinish.Finish(manager.ctx, manager.client, oldWorkload, kueue.WorkloadSliceReplaced, "Replaced to accommodate a new slice", util.RealClock)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.SetQuotaReservation(manager.ctx, manager.client, newWorkloadKey, utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).Obj())
		})

		ginkgo.By("observe: the new workload is created in the worker1 cluster")
		gomega.Eventually(func(g gomega.Gomega) {
			local := getWorkload(g, manager.ctx, manager.client, newWorkloadKey)
			remote := getWorkload(g, worker1.ctx, worker1.client, newWorkloadKey)
			g.Expect(remote.Spec).To(gomega.BeComparableTo(local.Spec))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("observe: there are no workloads or jobs in the worker2 cluster", func() {
			workloads := &kueue.WorkloadList{}
			gomega.Expect(worker2.client.List(worker2.ctx, workloads, client.InNamespace(job.Namespace))).To(gomega.Succeed())
			gomega.Expect(workloads.Items).To(gomega.BeEmpty())
			jobs := &batchv1.JobList{}
			gomega.Expect(worker2.client.List(worker2.ctx, jobs, client.InNamespace(job.Namespace))).To(gomega.Succeed())
			gomega.Expect(jobs.Items).To(gomega.BeEmpty())
		})

		ginkgo.By("observe: the old workload is still admitted in the worker1 cluster", func() {
			workload := getWorkload(gomega.Default, worker1.ctx, worker1.client, workloadKey)
			util.ExpectWorkloadsToBeAdmitted(worker1.ctx, worker1.client, workload)
		})

		ginkgo.By("observe: the remote job is still active and has old parallelism count", func() {
			remoteJob := job.DeepCopy()
			getJob(worker1.ctx, worker1.client, remoteJob)
			gomega.Expect(remoteJob.Spec.Suspend).To(gomega.Equal(new(false)))
			gomega.Expect(remoteJob.Spec.Parallelism).To(gomega.BeEquivalentTo(new(int32(1))))
		})

		ginkgo.By("admit the new workload replacing the old workload in the worker1 cluster", func() {
			util.SetQuotaReservation(worker1.ctx, worker1.client, newWorkloadKey, utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).Obj())
			gomega.Eventually(func(g gomega.Gomega) {
				wl := getWorkload(g, worker1.ctx, worker1.client, workloadKey)
				g.Expect(workloadfinish.Finish(worker1.ctx, worker1.client, wl, kueue.WorkloadSliceReplaced, "Replaced to accommodate a new slice", util.RealClock)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("observe: the new local workload admission check and local events reflect reservation in the worker1 cluster")
		util.ExpectAdmissionCheckStateWithMessage(
			manager.ctx, manager.client, newWorkloadKey,
			multiKueueAC.Name,
			kueue.CheckStateReady,
			`The workload was admitted on "worker1"`,
		)
		util.ExpectEventAppeared(manager.ctx, manager.client, eventsv1.Event{
			Reason: "MultiKueue",
			Type:   corev1.EventTypeNormal,
			Note:   `The workload was admitted on "worker1"`,
		})

		ginkgo.By("observe: job changes are synced to the worker1 cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				remoteJob := job.DeepCopy()
				getJob(worker1.ctx, worker1.client, remoteJob)
				g.Expect(remoteJob.Spec.Suspend).To(gomega.Equal(new(false)))
				g.Expect(remoteJob.Spec.Parallelism).To(gomega.BeEquivalentTo(new(int32(2))))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		/*
			Scale-down Section.
			Note: Scaling down does not create a new workload slice, so we continue using the previously generated `newWorkloadKey`.
		*/
		ginkgo.By("scale-down the job", func() {
			getJob(manager.ctx, manager.client, job)
			job.Spec.Parallelism = new(int32(1))
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(manager.client.Update(manager.ctx, job)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
		ginkgo.By("observe: workload changed in the manager cluster", func() {
			getJob(manager.ctx, manager.client, job)
			gomega.Eventually(func(g gomega.Gomega) {
				workload := getWorkload(g, manager.ctx, manager.client, newWorkloadKey)
				g.Expect(workload.Spec.PodSets[0].Count).To(gomega.BeEquivalentTo(int32(1)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
		ginkgo.By("observe: there are no new workloads created in response to scale-down even in the manager cluster", func() {
			list := &kueue.WorkloadList{}
			gomega.Expect(manager.client.List(manager.ctx, list, client.InNamespace(job.Namespace))).To(gomega.Succeed())
			gomega.Expect(list.Items).To(gomega.HaveLen(2))
		})
		ginkgo.By("observe: job changed in the worker1 cluster", func() {
			remoteJob := job.DeepCopy()
			gomega.Eventually(func(g gomega.Gomega) {
				getJob(worker1.ctx, worker1.client, remoteJob)
				g.Expect(remoteJob.Spec.Parallelism).To(gomega.BeEquivalentTo(new(int32(1))))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
		ginkgo.By("observe: there are no new workloads created in response to scale-down even in the worker1 cluster", func() {
			list := &kueue.WorkloadList{}
			gomega.Expect(worker1.client.List(worker1.ctx, list, client.InNamespace(job.Namespace))).To(gomega.Succeed())
			gomega.Expect(list.Items).To(gomega.HaveLen(2))
		})
		ginkgo.By("observe: there are still no workloads or jobs in the worker2 cluster", func() {
			workloads := &kueue.WorkloadList{}
			gomega.Expect(worker2.client.List(worker2.ctx, workloads, client.InNamespace(job.Namespace))).To(gomega.Succeed())
			gomega.Expect(workloads.Items).To(gomega.BeEmpty())
			jobs := &batchv1.JobList{}
			gomega.Expect(worker2.client.List(worker2.ctx, jobs, client.InNamespace(job.Namespace))).To(gomega.Succeed())
			gomega.Expect(jobs.Items).To(gomega.BeEmpty())
		})

		/*
			Finish Job Section.
		*/
		ginkgo.By("finishing the job in the worker1 cluster", func() {
			now := metav1.Now()
			completedJobCondition := batchv1.JobCondition{
				Type:               batchv1.JobComplete,
				Status:             corev1.ConditionTrue,
				LastProbeTime:      now,
				LastTransitionTime: now,
				Message:            "Job finished successfully",
			}

			gomega.Eventually(func(g gomega.Gomega) {
				remoteJob := job.DeepCopy()
				getJob(worker1.ctx, worker1.client, remoteJob)
				remoteJob.Status.Conditions = append(remoteJob.Status.Conditions,
					completedJobCondition,
					batchv1.JobCondition{
						Type:               batchv1.JobSuccessCriteriaMet,
						Status:             corev1.ConditionTrue,
						LastProbeTime:      now,
						LastTransitionTime: now,
						Message:            "Reached expected number of succeeded pods",
					})
				remoteJob.Status.Succeeded = 1
				remoteJob.Status.StartTime = new(now)
				remoteJob.Status.CompletionTime = new(now)
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, remoteJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(newWorkloadKey, completedJobCondition.Message)

			getJob(manager.ctx, manager.client, job)
			gomega.Expect(job.Status.Conditions).Should(gomega.ContainElement(gomega.WithTransform(func(condition batchv1.JobCondition) batchv1.JobCondition {
				condition.LastProbeTime = now
				condition.LastTransitionTime = now
				return condition
			}, gomega.Equal(completedJobCondition))))
		})
	})

	ginkgo.It("Should scale an elastic RayCluster on the worker if admitted", func() {
		manager := managerTestCluster
		worker1 := worker1TestCluster
		worker2 := worker2TestCluster

		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)

		rayGVK := rayv1.GroupVersion.WithKind("RayCluster")
		headPodSet := utiltestingapi.MakePodSetAssignment("head").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()
		workerPodSet := utiltestingapi.MakePodSetAssignment("workers-group-0").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()
		admission := func() *utiltestingapi.AdmissionWrapper {
			return utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).PodSets(headPodSet, workerPodSet)
		}

		getRayCluster := func(ctx context.Context, clnt client.Client, rc *rayv1.RayCluster) {
			ginkgo.GinkgoHelper()
			gomega.Expect(clnt.Get(ctx, client.ObjectKeyFromObject(rc), rc)).To(gomega.Succeed())
		}
		getWorkloadKey := func(rc *rayv1.RayCluster) types.NamespacedName {
			ginkgo.GinkgoHelper()
			getRayCluster(manager.ctx, manager.client, rc)
			return types.NamespacedName{Name: jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(rc.Name, rc.UID, rayGVK, rc.GetGeneration()), Namespace: rc.Namespace}
		}
		getWorkload := func(g gomega.Gomega, ctx context.Context, clnt client.Client, key types.NamespacedName) *kueue.Workload {
			ginkgo.GinkgoHelper()
			wl := &kueue.Workload{}
			g.Expect(clnt.Get(ctx, key, wl)).To(gomega.Succeed())
			return wl
		}

		raycluster := testingraycluster.MakeCluster("raycluster1", managerNs.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Queue(managerLq.Name).
			ScaleFirstWorkerGroup(1).
			Obj()
		util.MustCreate(manager.ctx, manager.client, raycluster)

		ginkgo.By("observe: the elastic RayCluster is created suspended in the manager cluster", func() {
			getRayCluster(manager.ctx, manager.client, raycluster)
			gomega.Expect(raycluster.Spec.Suspend).To(gomega.Equal(new(true)))
		})

		ginkgo.By("observe: a workload is created in the manager cluster")
		workloadKey := getWorkloadKey(raycluster)
		gomega.Eventually(func(g gomega.Gomega) {
			getWorkload(g, manager.ctx, manager.client, workloadKey)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("admit the workload on the manager cluster")
		util.SetQuotaReservation(manager.ctx, manager.client, workloadKey, admission().Obj())

		ginkgo.By("observe: the workload is created on all worker clusters", func() {
			localWorkload := getWorkload(gomega.Default, manager.ctx, manager.client, workloadKey)
			gomega.Eventually(func(g gomega.Gomega) {
				wl := getWorkload(g, worker1.ctx, worker1.client, workloadKey)
				g.Expect(wl.Spec).To(gomega.BeComparableTo(localWorkload.Spec))
				wl = getWorkload(g, worker2.ctx, worker2.client, workloadKey)
				g.Expect(wl.Spec).To(gomega.BeComparableTo(localWorkload.Spec))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("admit the workload on the worker1 cluster")
		util.SetQuotaReservation(worker1.ctx, worker1.client, workloadKey, admission().Obj())

		ginkgo.By("observe: the local admission check reflects the admission on the worker1 cluster")
		util.ExpectAdmissionCheckStateWithMessage(
			manager.ctx, manager.client, workloadKey,
			multiKueueAC.Name,
			kueue.CheckStateReady,
			`The workload was admitted on "worker1"`,
		)

		ginkgo.By("observe: the RayCluster is synced to the worker1 cluster and is not suspended", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				remote := raycluster.DeepCopy()
				getRayCluster(worker1.ctx, worker1.client, remote)
				g.Expect(remote.Spec.Suspend).To(gomega.Equal(new(false)))
				g.Expect(remote.Spec.WorkerGroupSpecs[0].Replicas).To(gomega.BeEquivalentTo(new(int32(1))))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("observe: the workload is removed from the worker2 cluster")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(worker2.client.Get(worker2.ctx, workloadKey, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		/*
			Scale-up Section.
		*/
		ginkgo.By("scale-up the RayCluster's first worker group to 3", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				getRayCluster(manager.ctx, manager.client, raycluster)
				raycluster.Spec.WorkerGroupSpecs[0].Replicas = new(int32(3))
				g.Expect(manager.client.Update(manager.ctx, raycluster)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("observe: a new workload slice is created in the manager cluster")
		newWorkloadKey := getWorkloadKey(raycluster)
		gomega.Eventually(func(g gomega.Gomega) {
			getWorkload(g, manager.ctx, manager.client, newWorkloadKey)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("emulate the scheduler: copy clusterName from the old slice to the new slice", func() {
			oldWorkload := getWorkload(gomega.Default, manager.ctx, manager.client, workloadKey)
			gomega.Eventually(func(g gomega.Gomega) {
				newWorkload := getWorkload(g, manager.ctx, manager.client, newWorkloadKey)
				newWorkload.Status.ClusterName = oldWorkload.Status.ClusterName
				g.Expect(manager.client.Status().Update(manager.ctx, newWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("emulate the scheduler: admit the new slice and finish the old slice in the manager cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				oldWorkload := getWorkload(g, manager.ctx, manager.client, workloadKey)
				g.Expect(workloadfinish.Finish(manager.ctx, manager.client, oldWorkload, kueue.WorkloadSliceReplaced, "Replaced to accommodate a new slice", util.RealClock)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.SetQuotaReservation(manager.ctx, manager.client, newWorkloadKey, admission().Obj())
		})

		ginkgo.By("emulate the scheduler: admit the new slice and finish the old slice in the worker1 cluster", func() {
			util.SetQuotaReservation(worker1.ctx, worker1.client, newWorkloadKey, admission().Obj())
			gomega.Eventually(func(g gomega.Gomega) {
				wl := getWorkload(g, worker1.ctx, worker1.client, workloadKey)
				g.Expect(workloadfinish.Finish(worker1.ctx, worker1.client, wl, kueue.WorkloadSliceReplaced, "Replaced to accommodate a new slice", util.RealClock)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("observe: the increased worker replicas are synced to the worker1 cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				remote := raycluster.DeepCopy()
				getRayCluster(worker1.ctx, worker1.client, remote)
				g.Expect(remote.Spec.WorkerGroupSpecs[0].Replicas).To(gomega.BeEquivalentTo(new(int32(3))))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		/*
			Scale-down Section.
			Note: Scaling down does not create a new workload slice, so we continue using newWorkloadKey.
		*/
		ginkgo.By("scale-down the RayCluster's first worker group to 1", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				getRayCluster(manager.ctx, manager.client, raycluster)
				raycluster.Spec.WorkerGroupSpecs[0].Replicas = new(int32(1))
				g.Expect(manager.client.Update(manager.ctx, raycluster)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("observe: no new workload is created in response to scale-down in the manager cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				wl := getWorkload(g, manager.ctx, manager.client, newWorkloadKey)
				g.Expect(wl.Spec.PodSets[1].Count).To(gomega.BeEquivalentTo(int32(1)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			list := &kueue.WorkloadList{}
			gomega.Expect(manager.client.List(manager.ctx, list, client.InNamespace(raycluster.Namespace))).To(gomega.Succeed())
			gomega.Expect(list.Items).To(gomega.HaveLen(2))
		})

		ginkgo.By("observe: the reduced worker replicas are synced to the worker1 cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				remote := raycluster.DeepCopy()
				getRayCluster(worker1.ctx, worker1.client, remote)
				g.Expect(remote.Spec.WorkerGroupSpecs[0].Replicas).To(gomega.BeEquivalentTo(new(int32(1))))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("observe: there are still no workloads in the worker2 cluster", func() {
			workloads := &kueue.WorkloadList{}
			gomega.Expect(worker2.client.List(worker2.ctx, workloads, client.InNamespace(raycluster.Namespace))).To(gomega.Succeed())
			gomega.Expect(workloads.Items).To(gomega.BeEmpty())
		})
	})

	ginkgo.It("Should redo the admission process once the workload loses Admission in the worker cluster", func() {
		job := testingjob.MakeJob("job", managerNs.Name).
			ManagedBy(kueue.MultiKueueControllerName).
			Queue(kueue.LocalQueueName(managerLq.Name)).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

		ginkgo.By("setting workload reservation in the management cluster", func() {
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).
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
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).
				Obj()
			util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlLookupKey, admission)

			util.ExpectAdmissionCheckStateWithMessage(
				managerTestCluster.ctx, managerTestCluster.client, wlLookupKey,
				multiKueueAC.Name,
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
						Name:    kueue.AdmissionCheckReference(multiKueueAC.Name),
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
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).
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
				kueue.AdmissionCheckReference(multiKueueAC.Name),
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
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(managerCq), updatedCq)).Should(gomega.Succeed())

				updatedCq.Spec.ResourceGroups = resourceGroups
				updatedCq.Spec.AdmissionChecksStrategy = &kueue.AdmissionChecksStrategy{
					AdmissionChecks: acRules,
				}
				gomega.Expect(k8sClient.Update(ctx, updatedCq)).Should(gomega.Succeed())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerCq, true)
			util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, flavor1, true)
			util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, flavor2, true)
			util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, additionalAc, true)
		})

		ginkgo.It("should assign the multikueue admission check even before admitted", framework.SlowSpec, func() {
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
			ginkgo.By("ensuring multikueue admission check reference is present and in pending state", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdWorkload := &kueue.Workload{}
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					acs := admissioncheck.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAC.Name))
					g.Expect(acs).To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
						Name:  kueue.AdmissionCheckReference(multiKueueAC.Name),
						State: kueue.CheckStatePending,
					}, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "Message", "RetryCount")))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should keep multikueue admission check reference and properly clean up the remote workload when the connection to an admitting worker is lost", framework.SlowSpec, func() {
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

			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).PodSets(
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

			admitWorkloadAndCheckWorkerCopies(multiKueueAC.Name, wlLookupKey, admission)

			restoreConnectionToWorker2 := util.BreakConnection(managerTestCluster.ctx, managerTestCluster.client, workerCluster2, managersConfigNamespace.Name)

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
					acs := admissioncheck.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAC.Name))
					g.Expect(acs).To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
						Name:       kueue.AdmissionCheckReference(multiKueueAC.Name),
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

func waitForRemoteWorkloadToBeDeleted(workerCtx context.Context, workerClient client.Client, wlLookupKey types.NamespacedName, workerName string, timeout time.Duration) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func() error {
		createdWorkload := &kueue.Workload{}
		err := workerClient.Get(workerCtx, wlLookupKey, createdWorkload)
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if createdWorkload.DeletionTimestamp != nil {
			return fmt.Errorf("%s workload deletion is in progress: uid=%s finalizers=%v deletionTimestamp=%v",
				workerName, createdWorkload.UID, createdWorkload.Finalizers, createdWorkload.DeletionTimestamp)
		}

		return fmt.Errorf("%s workload still exists and deletion has not started: uid=%s finalizers=%v deletionTimestamp=%v",
			workerName, createdWorkload.UID, createdWorkload.Finalizers, createdWorkload.DeletionTimestamp)
	}, timeout, util.Interval).Should(gomega.Succeed())
}

// expectReplacementSlice returns the key of the not-finished replacement slice
// (the workload carrying the replacement annotation) whose given pod set
// reached count on the manager.
func expectReplacementSlice(ns string, podSet kueue.PodSetReference, count int32) types.NamespacedName {
	ginkgo.GinkgoHelper()
	var key types.NamespacedName
	gomega.Eventually(func(g gomega.Gomega) {
		list := &kueue.WorkloadList{}
		g.Expect(managerTestCluster.client.List(managerTestCluster.ctx, list, client.InNamespace(ns))).To(gomega.Succeed())
		found := false
		for i := range list.Items {
			w := &list.Items[i]
			if workloadfinish.IsFinished(w) ||
				w.Annotations[workloadslicing.WorkloadSliceReplacementFor] == "" ||
				workload.ExtractPodSetCountsFromWorkload(w)[podSet] != count {
				continue
			}
			key = client.ObjectKeyFromObject(w)
			found = true
		}
		g.Expect(found).To(gomega.BeTrue(), "expected a not-finished replacement slice on the manager")
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
	return key
}

func admitWorkloadAndCheckWorkerCopies(acName string, wlLookupKey types.NamespacedName, admission *utiltestingapi.AdmissionWrapper) {
	ginkgo.GinkgoHelper()
	ginkgo.By("setting workload reservation in the management cluster", func() {
		util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission.Obj())
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
		util.SetQuotaReservation(worker2TestCluster.ctx, worker2TestCluster.client, wlLookupKey, admission.Obj())

		createdWorkload := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeComparableTo(&metav1.Condition{
				Type:    kueue.WorkloadAdmitted,
				Status:  metav1.ConditionTrue,
				Reason:  "Admitted",
				Message: "The workload is admitted",
			}, util.IgnoreConditionTimestampsAndObservedGeneration))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		util.ExpectAdmissionCheckStateWithMessage(
			managerTestCluster.ctx, managerTestCluster.client, wlLookupKey,
			acName,
			kueue.CheckStateReady,
			`The workload was admitted on "worker2"`,
		)
		util.ExpectEventAppeared(managerTestCluster.ctx, managerTestCluster.client, eventsv1.Event{
			Reason: "MultiKueue",
			Type:   corev1.EventTypeNormal,
			Note:   `The workload was admitted on "worker2"`,
		})

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
}

func admitJobsAndAgeAdmissionCheck(managerNsName, lqName, cqName, acName string, count int) []types.NamespacedName {
	ginkgo.GinkgoHelper()
	keys := make([]types.NamespacedName, 0, count)
	for i := range count {
		job := testingjob.MakeJob(fmt.Sprintf("job-%d", i), managerNsName).
			Queue(kueue.LocalQueueName(lqName)).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)
		key := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNsName}
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(cqName)).
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj())
		admitWorkloadAndCheckWorkerCopies(acName, key, admission)
		keys = append(keys, key)
	}

	ginkgo.By("waiting until each admission check's transition time is older than the worker-lost timeout", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			for _, key := range keys {
				wl := &kueue.Workload{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, key, wl)).To(gomega.Succeed())
				acs := admissioncheck.FindAdmissionCheck(wl.Status.AdmissionChecks, kueue.AdmissionCheckReference(acName))
				g.Expect(acs).NotTo(gomega.BeNil())
				g.Expect(acs.State).To(gomega.Equal(kueue.CheckStateReady))
				g.Expect(time.Since(acs.LastTransitionTime.Time)).To(gomega.BeNumerically(">", testingWorkerLostTimeout))
			}
		}, testingWorkerLostTimeout*3, util.Interval).Should(gomega.Succeed())
	})
	return keys
}

// expectNoEviction asserts that, for the given duration, none of the workloads lose their quota
// reservation or have their MultiKueue admission check moved off Ready.
func expectNoEviction(keys []types.NamespacedName, acName string, within time.Duration) {
	ginkgo.GinkgoHelper()
	gomega.Consistently(func(g gomega.Gomega) {
		for _, key := range keys {
			wl := &kueue.Workload{}
			g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, key, wl)).To(gomega.Succeed())
			g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadQuotaReserved))
			acs := admissioncheck.FindAdmissionCheck(wl.Status.AdmissionChecks, kueue.AdmissionCheckReference(acName))
			g.Expect(acs).NotTo(gomega.BeNil())
			g.Expect(acs.State).To(gomega.Equal(kueue.CheckStateReady))
			g.Expect(ptr.Deref(acs.RetryCount, 0)).To(gomega.BeZero())
		}
	}, within, util.Interval).Should(gomega.Succeed())
}

func expectEventuallyRetried(keys []types.NamespacedName, acName string, within time.Duration) {
	ginkgo.GinkgoHelper()

	baseRetryCount := make(map[types.NamespacedName]int32, len(keys))
	for _, key := range keys {
		wl := &kueue.Workload{}
		gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, key, wl)).To(gomega.Succeed())
		acs := admissioncheck.FindAdmissionCheck(wl.Status.AdmissionChecks, kueue.AdmissionCheckReference(acName))
		gomega.Expect(acs).NotTo(gomega.BeNil())
		baseRetryCount[key] = ptr.Deref(acs.RetryCount, 0)
	}

	gomega.Eventually(func(g gomega.Gomega) {
		for _, key := range keys {
			wl := &kueue.Workload{}
			g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, key, wl)).To(gomega.Succeed())
			acs := admissioncheck.FindAdmissionCheck(wl.Status.AdmissionChecks, kueue.AdmissionCheckReference(acName))
			g.Expect(acs).NotTo(gomega.BeNil())
			g.Expect(ptr.Deref(acs.RetryCount, 0)).To(gomega.BeNumerically(">=", baseRetryCount[key]+1))
		}
	}, within, util.Interval).Should(gomega.Succeed())
}

func waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey types.NamespacedName, finishJobReason string) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		createdWorkload := &kueue.Workload{}
		g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
		g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeComparableTo(&metav1.Condition{
			Type:    kueue.WorkloadFinished,
			Status:  metav1.ConditionTrue,
			Reason:  string(kftraining.JobSucceeded),
			Message: finishJobReason,
		}, util.IgnoreConditionTimestampsAndObservedGeneration))
	}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())

	gomega.Eventually(func(g gomega.Gomega) {
		createdWorkload := &kueue.Workload{}
		g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
	}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())

	gomega.Eventually(func(g gomega.Gomega) {
		createdWorkload := &kueue.Workload{}
		g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
	}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
}

func setQuotaReservationInCluster(wlLookupKey types.NamespacedName, admission *utiltestingapi.AdmissionWrapper) {
	ginkgo.GinkgoHelper()
	ginkgo.By("setting workload reservation in the management cluster", func() {
		util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission.Obj())
	})
}

func checkingTheWorkloadCreation(wlLookupKey types.NamespacedName, matcher gomegatypes.GomegaMatcher) {
	ginkgo.GinkgoHelper()
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
