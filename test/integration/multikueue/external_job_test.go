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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/multikueue"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/multikueue/externalframeworks"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadrayjob "sigs.k8s.io/kueue/pkg/controller/jobs/rayjob"
	"sigs.k8s.io/kueue/pkg/controller/workloaddispatcher"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	"sigs.k8s.io/kueue/pkg/webhooks"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe(
	"MultiKueue",
	ginkgo.Label("area:multikueue", "feature:multikueue"),
	ginkgo.Ordered,
	ginkgo.ContinueOnFailure,
	func() {
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
				managerFlavor            *kueue.ResourceFlavor

				worker1Cq *kueue.ClusterQueue
				worker1Lq *kueue.LocalQueue

				worker2Cq *kueue.ClusterQueue
				worker2Lq *kueue.LocalQueue
			)

			ginkgo.BeforeAll(func() {
				managerTestCluster.fwk.StartManager(
					managerTestCluster.ctx,
					managerTestCluster.cfg,
					func(ctx context.Context, mgr manager.Manager) {
						// Set up core controllers and RayJob webhook (but not MultiKueue integration)
						err := indexer.Setup(ctx, mgr.GetFieldIndexer())
						gomega.Expect(err).NotTo(gomega.HaveOccurred())

						cCache := schdcache.New(mgr.GetClient())
						preemptionExpectations := preemptexpectations.New()
						queueOptions := []qcache.Option{qcache.WithPreemptionExpectations(preemptionExpectations)}
						queues := integration.NewManager(ctx, mgr.GetClient(), cCache, queueOptions...)

						configuration := &config.Configuration{}
						mgr.GetScheme().Default(configuration)

						failedCtrl, err := core.SetupControllers(
							mgr,
							queues,
							cCache,
							configuration,
							core.SetupControllersOpts{PreemptionExpectations: preemptionExpectations},
						)
						gomega.Expect(err).ToNot(gomega.HaveOccurred(), "controller", failedCtrl)

						failedWebhook, err := webhooks.Setup(mgr, nil)
						gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)

						// Set up RayJob webhook (but not MultiKueue integration)
						err = workloadrayjob.SetupIndexes(ctx, mgr.GetFieldIndexer())
						gomega.Expect(err).NotTo(gomega.HaveOccurred())

						rayjobReconciler, err := workloadrayjob.NewReconciler(
							ctx,
							mgr.GetClient(),
							mgr.GetFieldIndexer(),
							mgr.GetEventRecorder(constants.JobControllerName))
						gomega.Expect(err).NotTo(gomega.HaveOccurred())
						err = rayjobReconciler.SetupWithManager(mgr)
						gomega.Expect(err).NotTo(gomega.HaveOccurred())

						err = workloadrayjob.SetupRayJobWebhook(
							mgr,
							jobframework.WithCache(cCache),
							jobframework.WithQueues(queues),
						)
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
							multikueue.WithEventsBatchPeriod(250*time.Millisecond),
							multikueue.WithAdapters(adapters),
							multikueue.WithDispatcherName(config.MultiKueueDispatcherModeAllAtOnce),
						)
						gomega.Expect(err).NotTo(gomega.HaveOccurred())

						_, err = workloaddispatcher.SetupControllers(mgr, configuration, nil)
						gomega.Expect(err).NotTo(gomega.HaveOccurred())
					},
				)
			})

			ginkgo.AfterAll(func() {
				managerTestCluster.fwk.StopManager(managerTestCluster.ctx)
			})

			ginkgo.BeforeEach(func() {
				managerNs = behavioral.CreateNamespaceFromPrefixWithLog(
					managerTestCluster.ctx,
					managerTestCluster.client,
					"multikueue-",
				)
				worker1Ns = behavioral.CreateNamespaceWithLog(
					worker1TestCluster.ctx,
					worker1TestCluster.client,
					managerNs.Name,
				)
				worker2Ns = behavioral.CreateNamespaceWithLog(
					worker2TestCluster.ctx,
					worker2TestCluster.client,
					managerNs.Name,
				)

				w1Kubeconfig, err := worker1TestCluster.kubeConfigBytes()
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				w2Kubeconfig, err := worker2TestCluster.kubeConfigBytes()
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				managerMultiKueueSecret1 = utiltesting.MakeSecret("multikueue1", managersConfigNamespace.Name).
					Data(kueue.MultiKueueConfigSecretKey, w1Kubeconfig).
					Obj()
				behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret1)

				managerMultiKueueSecret2 = utiltesting.MakeSecret("multikueue2", managersConfigNamespace.Name).
					Data(kueue.MultiKueueConfigSecretKey, w2Kubeconfig).
					Obj()
				behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret2)

				workerCluster1 = utiltestingapi.MakeMultiKueueCluster("worker1").
					KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret1.Name).
					Obj()
				behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, workerCluster1)

				workerCluster2 = utiltestingapi.MakeMultiKueueCluster("worker2").
					KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret2.Name).
					Obj()
				behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, workerCluster2)

				managerMultiKueueConfig = utiltestingapi.MakeMultiKueueConfig("multikueueconfig").
					Clusters(workerCluster1.Name, workerCluster2.Name).
					Obj()
				behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueConfig)

				multiKueueAC = utiltestingapi.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", managerMultiKueueConfig.Name).
					Obj()
				behavioral.CreateAdmissionChecksAndWaitForActive(
					managerTestCluster.ctx,
					managerTestCluster.client,
					multiKueueAC,
				)

				managerFlavor = utiltestingapi.MakeResourceFlavor(string(multikueueTestFlavor)).Obj()
				behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerFlavor)

				managerCq = utiltestingapi.MakeClusterQueue("q1").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(string(multikueueTestFlavor)).Resource(corev1.ResourceCPU, "5").Obj()).
					AdmissionChecks(kueue.AdmissionCheckReference(multiKueueAC.Name)).
					Obj()
				behavioral.CreateClusterQueuesAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, managerCq)

				managerLq = utiltestingapi.MakeLocalQueue(managerCq.Name, managerNs.Name).
					ClusterQueue(managerCq.Name).
					Obj()
				behavioral.CreateLocalQueuesAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, managerLq)

				worker1Cq = utiltestingapi.MakeClusterQueue("q1").Obj()
				behavioral.CreateClusterQueuesAndWaitForActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq)
				worker1Lq = utiltestingapi.MakeLocalQueue(worker1Cq.Name, worker1Ns.Name).
					ClusterQueue(worker1Cq.Name).
					Obj()
				behavioral.CreateLocalQueuesAndWaitForActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Lq)

				worker2Cq = utiltestingapi.MakeClusterQueue("q1").Obj()
				behavioral.CreateClusterQueuesAndWaitForActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2Cq)
				worker2Lq = utiltestingapi.MakeLocalQueue(worker2Cq.Name, worker2Ns.Name).
					ClusterQueue(worker2Cq.Name).
					Obj()
				behavioral.CreateLocalQueuesAndWaitForActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2Lq)
			})

			ginkgo.AfterEach(func() {
				gomega.Expect(behavioral.DeleteNamespace(managerTestCluster.ctx, managerTestCluster.client, managerNs)).
					To(gomega.Succeed())
				gomega.Expect(behavioral.DeleteNamespace(worker1TestCluster.ctx, worker1TestCluster.client, worker1Ns)).
					To(gomega.Succeed())
				gomega.Expect(behavioral.DeleteNamespace(worker2TestCluster.ctx, worker2TestCluster.client, worker2Ns)).
					To(gomega.Succeed())
				behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerCq, true)
				behavioral.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq, true)
				behavioral.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2Cq, true)
				behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerFlavor, true)
				behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, multiKueueAC, true)
				behavioral.ExpectObjectToBeDeleted(
					managerTestCluster.ctx,
					managerTestCluster.client,
					managerMultiKueueConfig,
					true,
				)
				behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, workerCluster1, true)
				behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, workerCluster2, true)
				behavioral.ExpectObjectToBeDeleted(
					managerTestCluster.ctx,
					managerTestCluster.client,
					managerMultiKueueSecret1,
					true,
				)
				behavioral.ExpectObjectToBeDeleted(
					managerTestCluster.ctx,
					managerTestCluster.client,
					managerMultiKueueSecret2,
					true,
				)
			})

			ginkgo.It("Should run a RayJob on worker if admitted", func() {
				admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).PodSets(
					utiltestingapi.MakePodSetAssignment("head").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
					utiltestingapi.MakePodSetAssignment("workers-group-0").
						Flavor(corev1.ResourceCPU, multikueueTestFlavor).
						Obj(),
				)
				rayjob := testingrayjob.MakeJob("rayjob1", managerNs.Name).
					WithSubmissionMode(rayv1.InteractiveMode).
					Queue(managerLq.Name).
					Obj()
				behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, rayjob)
				wlLookupKey := types.NamespacedName{
					Name:      workloadrayjob.GetWorkloadNameForRayJob(rayjob.Name, rayjob.UID),
					Namespace: managerNs.Name,
				}
				integration.SetQuotaReservation(
					managerTestCluster.ctx,
					managerTestCluster.client,
					wlLookupKey,
					admission.Obj(),
				)

				admitWorkloadAndCheckWorkerCopies(multiKueueAC.Name, wlLookupKey, admission)

				ginkgo.By(
					"changing the status of the RayJob in the worker, updates the manager's RayJob status",
					func() {
						gomega.Eventually(func(g gomega.Gomega) {
							createdRayJob := rayv1.RayJob{}
							g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(rayjob), &createdRayJob)).
								To(gomega.Succeed())
							createdRayJob.Status.JobDeploymentStatus = rayv1.JobDeploymentStatusRunning
							g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdRayJob)).
								To(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
						gomega.Eventually(func(g gomega.Gomega) {
							createdRayJob := rayv1.RayJob{}
							g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(rayjob), &createdRayJob)).
								To(gomega.Succeed())
							g.Expect(createdRayJob.Status.JobDeploymentStatus).To(gomega.Equal(rayv1.JobDeploymentStatusRunning))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					},
				)

				ginkgo.By(
					"finishing the worker RayJob, the manager's wl is marked as finished and the worker2 wl removed",
					func() {
						finishJobReason := ""
						gomega.Eventually(func(g gomega.Gomega) {
							createdRayJob := rayv1.RayJob{}
							g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(rayjob), &createdRayJob)).
								To(gomega.Succeed())
							createdRayJob.Status.JobStatus = rayv1.JobStatusSucceeded
							createdRayJob.Status.JobDeploymentStatus = rayv1.JobDeploymentStatusComplete
							g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdRayJob)).
								To(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

						waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, finishJobReason)
					},
				)
			})

			ginkgo.It("Should create the remote RayJob without the manager's ownerReferences", func() {
				admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).PodSets(
					utiltestingapi.MakePodSetAssignment("head").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
					utiltestingapi.MakePodSetAssignment("workers-group-0").
						Flavor(corev1.ResourceCPU, multikueueTestFlavor).
						Obj(),
				)
				rayjob := testingrayjob.MakeJob("rayjob1", managerNs.Name).
					WithSubmissionMode(rayv1.InteractiveMode).
					Queue(managerLq.Name).
					Obj()
				// The owner UID exists only on the manager, so a worker GC would delete a copy that kept it.
				rayjob.OwnerReferences = []metav1.OwnerReference{{
					APIVersion: "example.com/v1",
					Kind:       "FakeOwner",
					Name:       "fake-owner",
					UID:        "11111111-1111-1111-1111-111111111111",
				}}
				behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, rayjob)
				wlLookupKey := types.NamespacedName{
					Name:      workloadrayjob.GetWorkloadNameForRayJob(rayjob.Name, rayjob.UID),
					Namespace: managerNs.Name,
				}

				admitWorkloadAndCheckWorkerCopies(multiKueueAC.Name, wlLookupKey, admission)

				// uid/resourceVersion are server-assigned, so only owner-refs/finalizers/status are checked.
				ginkgo.By("checking the remote RayJob carries no source-cluster metadata", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						createdRayJob := rayv1.RayJob{}
						g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(rayjob), &createdRayJob)).
							To(gomega.Succeed())
						g.Expect(createdRayJob.OwnerReferences).To(gomega.BeEmpty())
						g.Expect(createdRayJob.Finalizers).To(gomega.BeEmpty())
						g.Expect(createdRayJob.Status).To(gomega.BeZero())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.It("Should run a RayJob on worker if admitted (ManagedBy)", func() {
				admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).PodSets(
					utiltestingapi.MakePodSetAssignment("head").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
					utiltestingapi.MakePodSetAssignment("workers-group-0").
						Flavor(corev1.ResourceCPU, multikueueTestFlavor).
						Obj(),
				)
				rayjob := testingrayjob.MakeJob("rayjob1", managerNs.Name).
					WithSubmissionMode(rayv1.InteractiveMode).
					Queue(managerLq.Name).
					ManagedBy(kueue.MultiKueueControllerName).
					Obj()
				behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, rayjob)
				wlLookupKey := types.NamespacedName{
					Name:      workloadrayjob.GetWorkloadNameForRayJob(rayjob.Name, rayjob.UID),
					Namespace: managerNs.Name,
				}
				integration.SetQuotaReservation(
					managerTestCluster.ctx,
					managerTestCluster.client,
					wlLookupKey,
					admission.Obj(),
				)

				admitWorkloadAndCheckWorkerCopies(multiKueueAC.Name, wlLookupKey, admission)

				ginkgo.By(
					"changing the status of the RayJob in the worker, updates the manager's RayJob status",
					func() {
						gomega.Eventually(func(g gomega.Gomega) {
							createdRayJob := rayv1.RayJob{}
							g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(rayjob), &createdRayJob)).
								To(gomega.Succeed())
							createdRayJob.Status.JobDeploymentStatus = rayv1.JobDeploymentStatusRunning
							g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdRayJob)).
								To(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
						gomega.Eventually(func(g gomega.Gomega) {
							createdRayJob := rayv1.RayJob{}
							g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(rayjob), &createdRayJob)).
								To(gomega.Succeed())
							g.Expect(createdRayJob.Status.JobDeploymentStatus).To(gomega.Equal(rayv1.JobDeploymentStatusRunning))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					},
				)

				ginkgo.By(
					"finishing the worker RayJob, the manager's wl is marked as finished and the worker2 wl removed",
					func() {
						finishJobReason := ""
						gomega.Eventually(func(g gomega.Gomega) {
							createdRayJob := rayv1.RayJob{}
							g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(rayjob), &createdRayJob)).
								To(gomega.Succeed())
							createdRayJob.Status.JobStatus = rayv1.JobStatusSucceeded
							createdRayJob.Status.JobDeploymentStatus = rayv1.JobDeploymentStatusComplete
							g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdRayJob)).
								To(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

						waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, finishJobReason)
					},
				)
			})

			ginkgo.It(
				"Should remove the worker's workload and RayJob after reconnect when the manager's RayJob and workload are deleted",
				func() {
					rayjob := testingrayjob.MakeJob("rayjob1", managerNs.Name).
						WithSubmissionMode(rayv1.InteractiveMode).
						Queue(managerLq.Name).
						Obj()
					behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, rayjob)
					rayjobLookupKey := client.ObjectKeyFromObject(rayjob)
					createdRayJob := &rayv1.RayJob{}

					createdWorkload := &kueue.Workload{}
					wlLookupKey := types.NamespacedName{
						Name:      workloadrayjob.GetWorkloadNameForRayJob(rayjob.Name, rayjob.UID),
						Namespace: managerNs.Name,
					}

					ginkgo.By("setting workload reservation in the management cluster", func() {
						admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).PodSets(
							utiltestingapi.MakePodSetAssignment("head").
								Flavor(corev1.ResourceCPU, multikueueTestFlavor).
								Obj(),
							utiltestingapi.MakePodSetAssignment("workers-group-0").
								Flavor(corev1.ResourceCPU, multikueueTestFlavor).
								Obj(),
						)
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).
								To(gomega.Succeed())
							integration.SetQuotaReservation(
								managerTestCluster.ctx,
								managerTestCluster.client,
								wlLookupKey,
								admission.Obj(),
							)
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("checking the workload creation in the worker clusters", func() {
						managerWl := &kueue.Workload{}
						gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).
							To(gomega.Succeed())
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).
								To(gomega.Succeed())
							behavioral.ExpectRemoteWorkloadSpec(g, createdWorkload, managerWl)
							g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).
								To(gomega.Succeed())
							behavioral.ExpectRemoteWorkloadSpec(g, createdWorkload, managerWl)
						}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
					})

					restoreConnectionToWorker2 := behavioral.BreakConnection(
						managerTestCluster.ctx,
						managerTestCluster.client,
						workerCluster2,
						managersConfigNamespace.Name,
					)

					ginkgo.By("setting workload reservation in worker1, the RayJob is created in worker1", func() {
						admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(managerCq.Name)).PodSets(
							utiltestingapi.MakePodSetAssignment("head").
								Flavor(corev1.ResourceCPU, multikueueTestFlavor).
								Obj(),
							utiltestingapi.MakePodSetAssignment("workers-group-0").
								Flavor(corev1.ResourceCPU, multikueueTestFlavor).
								Obj(),
						)

						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).
								To(gomega.Succeed())
							integration.SetQuotaReservation(
								worker1TestCluster.ctx,
								worker1TestCluster.client,
								wlLookupKey,
								admission.Obj(),
							)
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, rayjobLookupKey, createdRayJob)).
								To(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					restoreConnectionToWorker1 := behavioral.BreakConnection(
						managerTestCluster.ctx,
						managerTestCluster.client,
						workerCluster1,
						managersConfigNamespace.Name,
					)

					ginkgo.By("removing the manager's RayJob and workload", func() {
						gomega.Expect(managerTestCluster.client.Delete(managerTestCluster.ctx, rayjob)).
							Should(gomega.Succeed())
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).
								To(gomega.Succeed())
							g.Expect(managerTestCluster.client.Delete(managerTestCluster.ctx, createdWorkload)).
								To(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).
								To(utiltesting.BeNotFoundError(), "workload not deleted")
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("the worker objects are still present", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).
								To(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, rayjobLookupKey, createdRayJob)).
								To(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).
								To(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("restoring the connection to worker2", func() {
						restoreConnectionToWorker2()
					})

					ginkgo.By("the worker2 wl is removed by the garbage collector", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).
								To(utiltesting.BeNotFoundError())
						}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("restoring the connection to worker1", func() {
						restoreConnectionToWorker1()
					})

					ginkgo.By("the wl and RayJob are removed on the worker1", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, rayjobLookupKey, createdRayJob)).
								To(utiltesting.BeNotFoundError())
							g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).
								To(utiltesting.BeNotFoundError())
						}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
					})
				},
			)
		})
	},
)
