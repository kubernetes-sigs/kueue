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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadraycluster "sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	workloadrayjob "sigs.k8s.io/kueue/pkg/controller/jobs/rayjob"
	workloadrayservice "sigs.k8s.io/kueue/pkg/controller/jobs/rayservice"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	testingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	testingrayservice "sigs.k8s.io/kueue/pkg/util/testingjobs/rayservice"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("MultiKueue Kuberay", ginkgo.Label("area:multikueue", "feature:multikueue"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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

	ginkgo.It("Should run a RayJob on worker if admitted", func() {
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("head").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("workers-group-0").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)
		rayjob := testingrayjob.MakeJob("rayjob1", f.managerNs.Name).
			WithSubmissionMode(rayv1.InteractiveMode).
			Queue(f.managerLq.Name).
			WithHistoryServerOptions(&rayv1.HistoryServerOptions{
				CollectorOptions: &rayv1.CollectorOptions{
					Image: new("quay.io/kuberay/collector:v1.7.0"),
				},
			}).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, rayjob)
		wlLookupKey := types.NamespacedName{Name: workloadrayjob.GetWorkloadNameForRayJob(rayjob.Name, rayjob.UID), Namespace: f.managerNs.Name}
		util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission.Obj())

		admitWorkloadAndCheckWorkerCopies(f.multiKueueAC.Name, wlLookupKey, admission)

		ginkgo.By("propagating history server options to the worker RayJob", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdRayJob := rayv1.RayJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(rayjob), &createdRayJob)).To(gomega.Succeed())
				g.Expect(createdRayJob.Spec.RayClusterSpec.HistoryServerOptions).To(gomega.Equal(rayjob.Spec.RayClusterSpec.HistoryServerOptions))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

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
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("head").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("workers-group-0").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)
		raycluster := testingraycluster.MakeCluster("raycluster1", f.managerNs.Name).
			Queue(f.managerLq.Name).
			WithHistoryServerOptions(&rayv1.HistoryServerOptions{
				CollectorOptions: &rayv1.CollectorOptions{
					Image: new("quay.io/kuberay/collector:v1.7.0"),
				},
			}).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, raycluster)
		wlLookupKey := types.NamespacedName{Name: workloadraycluster.GetWorkloadNameForRayCluster(raycluster.Name, raycluster.UID), Namespace: f.managerNs.Name}
		util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission.Obj())

		admitWorkloadAndCheckWorkerCopies(f.multiKueueAC.Name, wlLookupKey, admission)

		ginkgo.By("propagating history server options to the worker RayCluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdRayCluster := rayv1.RayCluster{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(raycluster), &createdRayCluster)).To(gomega.Succeed())
				g.Expect(createdRayCluster.Spec.HistoryServerOptions).To(gomega.Equal(raycluster.Spec.HistoryServerOptions))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

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

	ginkgo.It("Should forward a serveConfigV2 update to the RayService on the worker if admitted", func() {
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("head").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("workers-group-0").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)
		rayService := testingrayservice.MakeService("rayservice1", f.managerNs.Name).
			Queue(f.managerLq.Name).
			WithServeConfigV2("serve-config-v1").
			WithHistoryServerOptions(&rayv1.HistoryServerOptions{
				CollectorOptions: &rayv1.CollectorOptions{
					Image: new("quay.io/kuberay/collector:v1.7.0"),
				},
			}).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, rayService)
		wlLookupKey := types.NamespacedName{Name: workloadrayservice.GetWorkloadNameForRayService(rayService.Name, rayService.UID), Namespace: f.managerNs.Name}
		util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission.Obj())

		admitWorkloadAndCheckWorkerCopies(f.multiKueueAC.Name, wlLookupKey, admission)

		ginkgo.By("checking the remote RayService is created with the initial serveConfigV2", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdRayService := rayv1.RayService{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(rayService), &createdRayService)).To(gomega.Succeed())
				g.Expect(createdRayService.Spec.ServeConfigV2).To(gomega.Equal("serve-config-v1"))
				g.Expect(createdRayService.Spec.RayClusterSpec.HistoryServerOptions).To(gomega.Equal(rayService.Spec.RayClusterSpec.HistoryServerOptions))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("updating serveConfigV2 on the manager, the change is forwarded to the remote RayService", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdRayService := rayv1.RayService{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(rayService), &createdRayService)).To(gomega.Succeed())
				createdRayService.Spec.ServeConfigV2 = "serve-config-v2"
				g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, &createdRayService)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdRayService := rayv1.RayService{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(rayService), &createdRayService)).To(gomega.Succeed())
				g.Expect(createdRayService.Spec.ServeConfigV2).To(gomega.Equal("serve-config-v2"))
				g.Expect(createdRayService.Spec.RayClusterSpec.HistoryServerOptions).To(gomega.Equal(rayService.Spec.RayClusterSpec.HistoryServerOptions))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
