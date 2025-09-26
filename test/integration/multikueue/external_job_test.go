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
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/multikueue"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/multikueue/externalframeworks"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadrayjob "sigs.k8s.io/kueue/pkg/controller/jobs/rayjob"
	dispatcher "sigs.k8s.io/kueue/pkg/controller/workloaddispatcher"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	"sigs.k8s.io/kueue/pkg/webhooks"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("MultiKueue", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.When("the external RayJob adapter is enabled", func() {
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
				// Set up core controllers and RayJob webhook (but not MultiKueue integration)
				err := indexer.Setup(ctx, mgr.GetFieldIndexer())
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				cCache := schdcache.New(mgr.GetClient())
				queues := qcache.NewManager(mgr.GetClient(), cCache)

				configuration := &config.Configuration{}
				mgr.GetScheme().Default(configuration)

				failedCtrl, err := core.SetupControllers(mgr, queues, cCache, configuration)
				gomega.Expect(err).ToNot(gomega.HaveOccurred(), "controller", failedCtrl)

				failedWebhook, err := webhooks.Setup(mgr, ptr.Deref(configuration.MultiKueue.DispatcherName, config.MultiKueueDispatcherModeAllAtOnce))
				gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)

				// Set up RayJob webhook (but not MultiKueue integration)
				err = workloadrayjob.SetupIndexes(ctx, mgr.GetFieldIndexer())
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				rayjobReconciler := workloadrayjob.NewReconciler(
					mgr.GetClient(),
					mgr.GetEventRecorderFor(constants.JobControllerName))
				err = rayjobReconciler.SetupWithManager(mgr)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				err = workloadrayjob.SetupRayJobWebhook(mgr, jobframework.WithCache(cCache), jobframework.WithQueues(queues))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				// Set up multikueue with external frameworks only
				err = multikueue.SetupIndexer(ctx, mgr.GetFieldIndexer(), managersConfigNamespace.Name)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				cfg := &config.Configuration{}
				mgr.GetScheme().Default(cfg)
				cfg.MultiKueue.ExternalFrameworks = []config.MultiKueueExternalFramework{
					{
						Name: "RayJob.v1.ray.io",
					},
				}

				// Get external adapters for MultiKueue synchronization
				externalAdapters, err := externalframeworks.NewAdapters(cfg.MultiKueue.ExternalFrameworks)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				adapters := make(map[string]jobframework.MultiKueueAdapter)
				for _, adapter := range externalAdapters {
					gvk := adapter.GVK()
					adapters[gvk.String()] = adapter
				}

				err = multikueue.SetupControllers(mgr, managersConfigNamespace.Name,
					multikueue.WithGCInterval(2*time.Second),
					multikueue.WithWorkerLostTimeout(testingWorkerLostTimeout),
					multikueue.WithEventsBatchPeriod(100*time.Millisecond),
					multikueue.WithAdapters(adapters),
					multikueue.WithDispatcherName(string(config.MultiKueueDispatcherModeAllAtOnce)),
				)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				_, err = dispatcher.SetupControllers(mgr, configuration, string(config.MultiKueueDispatcherModeAllAtOnce))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			})
		})

		ginkgo.AfterAll(func() {
			managerTestCluster.fwk.StopManager(managerTestCluster.ctx)
		})

		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MultiKueueAdaptersForCustomJobs, true)
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

		ginkgo.It("Should run a RayJob on worker if admitted (ManagedBy)", func() {
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
				ManagedBy(kueue.MultiKueueControllerName).
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

		ginkgo.It("Should remove the worker's workload and RayJob after reconnect when the managers RayJob and workload are deleted", func() {
			rayjob := testingrayjob.MakeJob("rayjob1", managerNs.Name).
				WithSubmissionMode(rayv1.InteractiveMode).
				Queue(managerLq.Name).
				Obj()
			util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, rayjob)
			rayjobLookupKey := client.ObjectKeyFromObject(rayjob)
			createdRayJob := &rayv1.RayJob{}

			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadrayjob.GetWorkloadNameForRayJob(rayjob.Name, rayjob.UID), Namespace: managerNs.Name}

			ginkgo.By("setting workload reservation in the management cluster", func() {
				admission := utiltesting.MakeAdmission(managerCq.Name).PodSets(
					kueue.PodSetAssignment{
						Name: "head",
					}, kueue.PodSetAssignment{
						Name: "workers-group-0",
					},
				)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission.Obj())
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
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
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

			ginkgo.By("setting workload reservation in worker1, the RayJob is created in worker1", func() {
				admission := utiltesting.MakeAdmission(managerCq.Name).PodSets(
					kueue.PodSetAssignment{
						Name: "head",
					}, kueue.PodSetAssignment{
						Name: "workers-group-0",
					},
				)

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlLookupKey, admission.Obj())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, rayjobLookupKey, createdRayJob)).To(gomega.Succeed())
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

			ginkgo.By("removing the managers RayJob and workload", func() {
				gomega.Expect(managerTestCluster.client.Delete(managerTestCluster.ctx, rayjob)).Should(gomega.Succeed())
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
					g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, rayjobLookupKey, createdRayJob)).To(gomega.Succeed())
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

			ginkgo.By("the wl and RayJob are removed on the worker1", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, rayjobLookupKey, createdRayJob)).To(utiltesting.BeNotFoundError())
					g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
