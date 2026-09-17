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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadpaddlejob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/paddlejob"
	workloadpytorchjob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/pytorchjob"
	workloadtfjob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/tfjob"
	workloadxgboostjob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/xgboostjob"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpaddlejob "sigs.k8s.io/kueue/pkg/util/testingjobs/paddlejob"
	testingpytorchjob "sigs.k8s.io/kueue/pkg/util/testingjobs/pytorchjob"
	testingtfjob "sigs.k8s.io/kueue/pkg/util/testingjobs/tfjob"
	testingxgboostjob "sigs.k8s.io/kueue/pkg/util/testingjobs/xgboostjob"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("MultiKueue TrainingOperator", ginkgo.Label("area:multikueue", "feature:multikueue"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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

	ginkgo.It("Should run a TFJob on worker if admitted", framework.RedundantSpec, func() {
		tfJob := testingtfjob.MakeTFJob("tfjob1", f.managerNs.Name).
			ManagedBy(kueue.MultiKueueControllerName).
			Queue(f.managerLq.Name).
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
		wlLookupKey := types.NamespacedName{Name: workloadtfjob.GetWorkloadNameForTFJob(tfJob.Name, tfJob.UID), Namespace: f.managerNs.Name}
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("chief").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("ps").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("worker").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)

		admitWorkloadAndCheckWorkerCopies(f.multiKueueAC.Name, wlLookupKey, admission)

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
		paddleJob := testingpaddlejob.MakePaddleJob("paddlejob1", f.managerNs.Name).
			ManagedBy(kueue.MultiKueueControllerName).
			Queue(f.managerLq.Name).
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

		wlLookupKey := types.NamespacedName{Name: workloadpaddlejob.GetWorkloadNameForPaddleJob(paddleJob.Name, paddleJob.UID), Namespace: f.managerNs.Name}
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("master").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("worker").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)

		admitWorkloadAndCheckWorkerCopies(f.multiKueueAC.Name, wlLookupKey, admission)

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
		pyTorchJob := testingpytorchjob.MakePyTorchJob("pytorchjob1", f.managerNs.Name).
			ManagedBy(kueue.MultiKueueControllerName).
			Queue(f.managerLq.Name).
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

		wlLookupKey := types.NamespacedName{Name: workloadpytorchjob.GetWorkloadNameForPyTorchJob(pyTorchJob.Name, pyTorchJob.UID), Namespace: f.managerNs.Name}
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("master").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("worker").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)

		admitWorkloadAndCheckWorkerCopies(f.multiKueueAC.Name, wlLookupKey, admission)

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
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("master").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("worker").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)
		pyTorchJob := testingpytorchjob.MakePyTorchJob("pytorchjob-not-managed", f.managerNs.Name).
			Queue(f.managerLq.Name).
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

		wlLookupKeyNoManagedBy := types.NamespacedName{Name: workloadpytorchjob.GetWorkloadNameForPyTorchJob(pyTorchJob.Name, pyTorchJob.UID), Namespace: f.managerNs.Name}
		setQuotaReservationInCluster(wlLookupKeyNoManagedBy, admission)
		checkingTheWorkloadCreation(wlLookupKeyNoManagedBy, gomega.Not(gomega.Succeed()))
	})

	ginkgo.It("Should run a XGBoostJob on worker if admitted", framework.RedundantSpec, func() {
		xgBoostJob := testingxgboostjob.MakeXGBoostJob("xgboostjob1", f.managerNs.Name).
			ManagedBy(kueue.MultiKueueControllerName).
			Queue(f.managerLq.Name).
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

		wlLookupKey := types.NamespacedName{Name: workloadxgboostjob.GetWorkloadNameForXGBoostJob(xgBoostJob.Name, xgBoostJob.UID), Namespace: f.managerNs.Name}
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("master").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
			utiltestingapi.MakePodSetAssignment("worker").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)

		admitWorkloadAndCheckWorkerCopies(f.multiKueueAC.Name, wlLookupKey, admission)

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
})
