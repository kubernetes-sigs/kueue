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

	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadmpijob "sigs.k8s.io/kueue/pkg/controller/jobs/mpijob"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingmpijob "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("MultiKueue MPIJob", ginkgo.Label("area:multikueue", "feature:multikueue"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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

	ginkgo.It("Should not run a MPIJob on worker if set to be managed by external controller", func() {
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("launcher").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("worker").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)
		mpijobNoManagedBy := testingmpijob.MakeMPIJob("mpijob2", f.managerNs.Name).
			Queue(f.managerLq.Name).
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

		wlLookupKeyNoManagedBy := types.NamespacedName{Name: workloadmpijob.GetWorkloadNameForMPIJob(mpijobNoManagedBy.Name, mpijobNoManagedBy.UID), Namespace: f.managerNs.Name}
		setQuotaReservationInCluster(wlLookupKeyNoManagedBy, admission)
		checkingTheWorkloadCreation(wlLookupKeyNoManagedBy, gomega.Not(gomega.Succeed()))
	})

	ginkgo.It("Should run a MPIJob on worker if admitted", func() {
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("launcher").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("worker").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)
		mpijob := testingmpijob.MakeMPIJob("mpijob1", f.managerNs.Name).
			Queue(f.managerLq.Name).
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
		wlLookupKey := types.NamespacedName{Name: workloadmpijob.GetWorkloadNameForMPIJob(mpijob.Name, mpijob.UID), Namespace: f.managerNs.Name}
		admitWorkloadAndCheckWorkerCopies(f.multiKueueAC.Name, wlLookupKey, admission)

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
})
