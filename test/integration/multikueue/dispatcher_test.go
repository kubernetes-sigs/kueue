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

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("MultiKueueDispatcherIncremental", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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
		realClock = clock.RealClock{}
	)

	ginkgo.BeforeAll(func() {
		managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
			managerAndMultiKueueSetup(ctx, mgr, 2*time.Second, defaultEnabledIntegrations, config.MultiKueueDispatcherModeIncremental)
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
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueSecret1)).To(gomega.Succeed())

		managerMultiKueueSecret2 = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multikueue2",
				Namespace: managersConfigNamespace.Name,
			},
			Data: map[string][]byte{
				kueue.MultiKueueConfigSecretKey: w2Kubeconfig,
			},
		}
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueSecret2)).To(gomega.Succeed())

		workerCluster1 = utiltesting.MakeMultiKueueCluster("worker1").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret1.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, workerCluster1)).To(gomega.Succeed())

		workerCluster2 = utiltesting.MakeMultiKueueCluster("worker2").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret2.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, workerCluster2)).To(gomega.Succeed())

		managerMultiKueueConfig = utiltesting.MakeMultiKueueConfig("multikueueconfig").Clusters(workerCluster1.Name, workerCluster2.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueConfig)).Should(gomega.Succeed())

		multiKueueAC = utiltesting.MakeAdmissionCheck("ac1").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", managerMultiKueueConfig.Name).
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, multiKueueAC)).Should(gomega.Succeed())

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
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerCq)).Should(gomega.Succeed())

		managerLq = utiltesting.MakeLocalQueue(managerCq.Name, managerNs.Name).ClusterQueue(managerCq.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerLq)).Should(gomega.Succeed())

		worker1Cq = utiltesting.MakeClusterQueue("q1").Obj()
		gomega.Expect(worker1TestCluster.client.Create(worker1TestCluster.ctx, worker1Cq)).Should(gomega.Succeed())
		worker1Lq = utiltesting.MakeLocalQueue(worker1Cq.Name, worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
		gomega.Expect(worker1TestCluster.client.Create(worker1TestCluster.ctx, worker1Lq)).Should(gomega.Succeed())

		worker2Cq = utiltesting.MakeClusterQueue("q1").Obj()
		gomega.Expect(worker2TestCluster.client.Create(worker2TestCluster.ctx, worker2Cq)).Should(gomega.Succeed())
		worker2Lq = utiltesting.MakeLocalQueue(worker2Cq.Name, worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
		gomega.Expect(worker2TestCluster.client.Create(worker2TestCluster.ctx, worker2Lq)).Should(gomega.Succeed())
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

		ginkgo.By("checking the workload creation in the worker clusters 1 and 2", func() {
			managerWl := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				// The workload should be created in worker2 as well, since the job is managed
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				// nominated workers should be updated in the manager
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
				ginkgo.GinkgoLogr.Info(fmt.Sprintf("Workload status in manager: %s, %v", managerWl.Status.NominatedClusterNames, managerWl.Status.Conditions))
				g.Expect(managerWl.Status.NominatedClusterNames).To(gomega.ContainElements(workerCluster1.Name, workerCluster2.Name))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting workload reservation in worker1, workload in worker2 is removed", func() {
			admission := utiltesting.MakeAdmission(managerCq.Name).Obj()
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, createdWorkload, admission)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking the workload ClusterName in the management cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Status.ClusterName).To(gomega.HaveValue(gomega.Equal(workerCluster1.Name)))
				g.Expect(createdWorkload.Status.NominatedClusterNames).To(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting the check conditions for eviction", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				wlPatch := workload.PrepareWorkloadPatch(createdWorkload, true, realClock)
				workload.SetAdmissionCheckState(&wlPatch.Status.AdmissionChecks, kueue.AdmissionCheckState{
					Name:    kueue.AdmissionCheckReference(multiKueueAC.Name),
					State:   kueue.CheckStateRejected,
					Message: "check rejected",
				}, realClock)
				g.Expect(managerTestCluster.client.Status().Patch(managerTestCluster.ctx, wlPatch, client.Apply, client.FieldOwner(constants.AdmissionName), client.ForceOwnership)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking the workload ClusterName and NominatedClusterNames are reset in the management cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Status.NominatedClusterNames).To(gomega.BeNil())
				g.Expect(createdWorkload.Status.ClusterName).To(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})

var _ = ginkgo.Describe("MultiKueueDispatcherExternal", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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
		realClock                = clock.RealClock{}

		worker1Cq *kueue.ClusterQueue
		worker1Lq *kueue.LocalQueue

		worker2Cq *kueue.ClusterQueue
		worker2Lq *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
			managerAndMultiKueueSetup(ctx, mgr, 2*time.Second, defaultEnabledIntegrations, "example.com/custom-mk-dispatcher")
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
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueSecret1)).To(gomega.Succeed())

		managerMultiKueueSecret2 = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multikueue2",
				Namespace: managersConfigNamespace.Name,
			},
			Data: map[string][]byte{
				kueue.MultiKueueConfigSecretKey: w2Kubeconfig,
			},
		}
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueSecret2)).To(gomega.Succeed())

		workerCluster1 = utiltesting.MakeMultiKueueCluster("worker1").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret1.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, workerCluster1)).To(gomega.Succeed())

		workerCluster2 = utiltesting.MakeMultiKueueCluster("worker2").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret2.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, workerCluster2)).To(gomega.Succeed())

		managerMultiKueueConfig = utiltesting.MakeMultiKueueConfig("multikueueconfig").Clusters(workerCluster1.Name, workerCluster2.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueConfig)).Should(gomega.Succeed())

		multiKueueAC = utiltesting.MakeAdmissionCheck("ac1").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", managerMultiKueueConfig.Name).
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, multiKueueAC)).Should(gomega.Succeed())

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
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerCq)).Should(gomega.Succeed())

		managerLq = utiltesting.MakeLocalQueue(managerCq.Name, managerNs.Name).ClusterQueue(managerCq.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerLq)).Should(gomega.Succeed())

		worker1Cq = utiltesting.MakeClusterQueue("q1").Obj()
		gomega.Expect(worker1TestCluster.client.Create(worker1TestCluster.ctx, worker1Cq)).Should(gomega.Succeed())
		worker1Lq = utiltesting.MakeLocalQueue(worker1Cq.Name, worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
		gomega.Expect(worker1TestCluster.client.Create(worker1TestCluster.ctx, worker1Lq)).Should(gomega.Succeed())

		worker2Cq = utiltesting.MakeClusterQueue("q1").Obj()
		gomega.Expect(worker2TestCluster.client.Create(worker2TestCluster.ctx, worker2Cq)).Should(gomega.Succeed())
		worker2Lq = utiltesting.MakeLocalQueue(worker2Cq.Name, worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
		gomega.Expect(worker2TestCluster.client.Create(worker2TestCluster.ctx, worker2Lq)).Should(gomega.Succeed())
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

		ginkgo.By("checking the workload was not created in any worker cluster", func() {
			managerWl := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
			gomega.Expect(managerWl.Status.ClusterName).To(gomega.BeNil())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("updating workload nomination by cluster2", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				managerWl := &kueue.Workload{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
				wlPatch := workload.PrepareWorkloadPatch(managerWl, true, realClock)
				wlPatch.Status.NominatedClusterNames = []string{workerCluster2.Name}
				g.Expect(managerTestCluster.client.Status().Patch(managerTestCluster.ctx, wlPatch, client.Apply, client.FieldOwner(constants.AdmissionName), client.ForceOwnership)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking the workload creation in the worker clusters 2", func() {
			managerWl := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())

				// The workload should be only created in worker2
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))

				// nominated workers should be updated in the manager
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
				ginkgo.GinkgoLogr.Info(fmt.Sprintf("Workload status in manager: %s, %v", managerWl.Status.NominatedClusterNames, managerWl.Status.Conditions))
				g.Expect(managerWl.Status.NominatedClusterNames).To(gomega.ContainElements(workerCluster2.Name))
				g.Expect(managerWl.Status.ClusterName).To(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("updating workload nomination by cluster1 and cluster2", func() {
			managerWl := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
				wlPatch := workload.PrepareWorkloadPatch(managerWl, true, realClock)
				wlPatch.Status.NominatedClusterNames = []string{workerCluster1.Name, workerCluster2.Name}
				g.Expect(managerTestCluster.client.Status().Patch(managerTestCluster.ctx, wlPatch, client.Apply, client.FieldOwner(constants.AdmissionName), client.ForceOwnership)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking the workload creation in the worker clusters 1 and 2", func() {
			managerWl := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				// nominated workers should be updated in the manager
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
				ginkgo.GinkgoLogr.Info(fmt.Sprintf("Workload status in manager: %s, %v", managerWl.Status.NominatedClusterNames, managerWl.Status.Conditions))
				g.Expect(managerWl.Status.NominatedClusterNames).To(gomega.ContainElements(workerCluster1.Name, workerCluster2.Name))
				g.Expect(managerWl.Status.ClusterName).To(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting workload reservation in worker2, workload in worker1 is removed", func() {
			admission := utiltesting.MakeAdmission(managerCq.Name).Obj()
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(worker2TestCluster.ctx, worker2TestCluster.client, createdWorkload, admission)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking the workload ClusterName in the management cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Status.ClusterName).To(gomega.HaveValue(gomega.Equal(workerCluster2.Name)))
				g.Expect(createdWorkload.Status.NominatedClusterNames).To(gomega.BeEmpty())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting the check conditions for eviction", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				wlPatch := workload.PrepareWorkloadPatch(createdWorkload, true, realClock)
				workload.SetAdmissionCheckState(&wlPatch.Status.AdmissionChecks, kueue.AdmissionCheckState{
					Name:    kueue.AdmissionCheckReference(multiKueueAC.Name),
					State:   kueue.CheckStateRejected,
					Message: "check rejected",
				}, realClock)
				g.Expect(managerTestCluster.client.Status().Patch(managerTestCluster.ctx, wlPatch, client.Apply, client.FieldOwner(constants.AdmissionName), client.ForceOwnership)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking the workload ClusterName and NominatedClusterNames are reset in the management cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Status.NominatedClusterNames).To(gomega.BeNil())
				g.Expect(createdWorkload.Status.ClusterName).To(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})

var _ = ginkgo.Describe("MultiKueueDispatcherAllAtOnce", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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
		realClock = clock.RealClock{}
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
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueSecret1)).To(gomega.Succeed())

		managerMultiKueueSecret2 = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multikueue2",
				Namespace: managersConfigNamespace.Name,
			},
			Data: map[string][]byte{
				kueue.MultiKueueConfigSecretKey: w2Kubeconfig,
			},
		}
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueSecret2)).To(gomega.Succeed())

		workerCluster1 = utiltesting.MakeMultiKueueCluster("worker1").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret1.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, workerCluster1)).To(gomega.Succeed())

		workerCluster2 = utiltesting.MakeMultiKueueCluster("worker2").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret2.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, workerCluster2)).To(gomega.Succeed())

		managerMultiKueueConfig = utiltesting.MakeMultiKueueConfig("multikueueconfig").Clusters(workerCluster1.Name, workerCluster2.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueConfig)).Should(gomega.Succeed())

		multiKueueAC = utiltesting.MakeAdmissionCheck("ac1").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", managerMultiKueueConfig.Name).
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, multiKueueAC)).Should(gomega.Succeed())

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
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerCq)).Should(gomega.Succeed())

		managerLq = utiltesting.MakeLocalQueue(managerCq.Name, managerNs.Name).ClusterQueue(managerCq.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerLq)).Should(gomega.Succeed())

		worker1Cq = utiltesting.MakeClusterQueue("q1").Obj()
		gomega.Expect(worker1TestCluster.client.Create(worker1TestCluster.ctx, worker1Cq)).Should(gomega.Succeed())
		worker1Lq = utiltesting.MakeLocalQueue(worker1Cq.Name, worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
		gomega.Expect(worker1TestCluster.client.Create(worker1TestCluster.ctx, worker1Lq)).Should(gomega.Succeed())

		worker2Cq = utiltesting.MakeClusterQueue("q1").Obj()
		gomega.Expect(worker2TestCluster.client.Create(worker2TestCluster.ctx, worker2Cq)).Should(gomega.Succeed())
		worker2Lq = utiltesting.MakeLocalQueue(worker2Cq.Name, worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
		gomega.Expect(worker2TestCluster.client.Create(worker2TestCluster.ctx, worker2Lq)).Should(gomega.Succeed())
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

	ginkgo.It("Should run a job on worker if admitted after the upgrade to MultiKueue Dispatcher", func() {
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

		ginkgo.By("checking the workload creation in the worker clusters 1 and 2", func() {
			managerWl := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				// The workload should be created in worker2 as well, since the job is managed
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				// nominated workers should be updated in the manager
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
				ginkgo.GinkgoLogr.Info(fmt.Sprintf("Workload status in manager: %s, %v", managerWl.Status.NominatedClusterNames, managerWl.Status.Conditions))
				g.Expect(managerWl.Status.NominatedClusterNames).To(gomega.ContainElements(workerCluster1.Name, workerCluster2.Name))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting workload reservation in worker1, workload in worker2 is removed", func() {
			admission := utiltesting.MakeAdmission(managerCq.Name).Obj()
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, createdWorkload, admission)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking workload status conditions", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Status.Conditions).To(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadQuotaReserved,
						Message: fmt.Sprintf("Quota reserved in ClusterQueue %s", managerLq.Name),
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadAdmitted,
						Message: "The workload is admitted",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
				))
				state := admissioncheck.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAC.Name))
				g.Expect(state).NotTo(gomega.BeNil())
				g.Expect(state.State).To(gomega.Equal(kueue.CheckStateReady))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking the workload ClusterName in the management cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Status.ClusterName).To(gomega.HaveValue(gomega.Equal(workerCluster1.Name)))
				g.Expect(createdWorkload.Status.NominatedClusterNames).To(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Resetting ClusterName field in the manager", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				wlPatch := workload.PrepareWorkloadPatch(createdWorkload, true, realClock)
				wlPatch.Status.ClusterName = nil
				g.Expect(managerTestCluster.client.Status().Patch(managerTestCluster.ctx, wlPatch, client.Apply, client.FieldOwner(constants.AdmissionName), client.ForceOwnership)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking the workload ClusterName and NominatedClusterNames are reset in the management cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Status.NominatedClusterNames).To(gomega.BeNil())
				g.Expect(createdWorkload.Status.ClusterName).To(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking workload status conditions again", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Status.Conditions).To(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadQuotaReserved,
						Message: fmt.Sprintf("Quota reserved in ClusterQueue %s", managerLq.Name),
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadAdmitted,
						Message: "The workload is admitted",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
				))
				state := admissioncheck.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAC.Name))
				g.Expect(state).NotTo(gomega.BeNil())
				g.Expect(state.State).To(gomega.Equal(kueue.CheckStateReady))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		time.Sleep(5 * time.Second)
	})

	ginkgo.It("Should re-evaluate existing workloads when clusters are added to MultiKueueConfig", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MultiKueueBatchJobWithManagedBy, true)

		var (
			isolatedMultiKueueConfig *kueue.MultiKueueConfig
			isolatedAdmissionCheck   *kueue.AdmissionCheck
			isolatedCq               *kueue.ClusterQueue
			isolatedLq               *kueue.LocalQueue
		)

		ginkgo.By("creating isolated MultiKueueConfig with only worker1", func() {
			isolatedMultiKueueConfig = utiltesting.MakeMultiKueueConfig("isolated-config").Clusters(workerCluster1.Name).Obj()
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, isolatedMultiKueueConfig)).To(gomega.Succeed())

			isolatedAdmissionCheck = utiltesting.MakeAdmissionCheck("isolated-ac").
				ControllerName(kueue.MultiKueueControllerName).
				Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", isolatedMultiKueueConfig.Name).
				Obj()
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, isolatedAdmissionCheck)).To(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				updatedAc := kueue.AdmissionCheck{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(isolatedAdmissionCheck), &updatedAc)).To(gomega.Succeed())
				g.Expect(updatedAc.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.AdmissionCheckActive))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			isolatedCq = utiltesting.MakeClusterQueue("isolated-cq").
				AdmissionChecks(kueue.AdmissionCheckReference(isolatedAdmissionCheck.Name)).
				Obj()
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, isolatedCq)).To(gomega.Succeed())
			util.ExpectClusterQueuesToBeActive(managerTestCluster.ctx, managerTestCluster.client, isolatedCq)

			isolatedLq = utiltesting.MakeLocalQueue("isolated-lq", managerNs.Name).ClusterQueue(isolatedCq.Name).Obj()
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, isolatedLq)).To(gomega.Succeed())
			util.ExpectLocalQueuesToBeActive(managerTestCluster.ctx, managerTestCluster.client, isolatedLq)
		})

		ginkgo.By("configuring worker clusters with different resource limits", func() {
			worker1Flavor := utiltesting.MakeResourceFlavor("w1-flavor").Obj()
			gomega.Expect(worker1TestCluster.client.Create(worker1TestCluster.ctx, worker1Flavor)).To(gomega.Succeed())

			gomega.Eventually(func() error {
				if err := worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(worker1Cq), worker1Cq); err != nil {
					return err
				}
				worker1Cq.Spec.ResourceGroups = []kueue.ResourceGroup{
					{
						CoveredResources: []corev1.ResourceName{corev1.ResourceCPU},
						Flavors: []kueue.FlavorQuotas{
							*utiltesting.MakeFlavorQuotas("w1-flavor").Resource(corev1.ResourceCPU, "50m").Obj(),
						},
					},
				}
				return worker1TestCluster.client.Update(worker1TestCluster.ctx, worker1Cq)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			worker2Flavor := utiltesting.MakeResourceFlavor("w2-flavor").Obj()
			gomega.Expect(worker2TestCluster.client.Create(worker2TestCluster.ctx, worker2Flavor)).To(gomega.Succeed())

			gomega.Eventually(func() error {
				if err := worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(worker2Cq), worker2Cq); err != nil {
					return err
				}
				worker2Cq.Spec.ResourceGroups = []kueue.ResourceGroup{
					{
						CoveredResources: []corev1.ResourceName{corev1.ResourceCPU},
						Flavors: []kueue.FlavorQuotas{
							*utiltesting.MakeFlavorQuotas("w2-flavor").Resource(corev1.ResourceCPU, "200m").Obj(),
						},
					},
				}
				return worker2TestCluster.client.Update(worker2TestCluster.ctx, worker2Cq)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			util.ExpectClusterQueuesToBeActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq)
			util.ExpectClusterQueuesToBeActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2Cq)
		})

		var oldWlLookupKey types.NamespacedName
		ginkgo.By("creating initial workload that requires more CPU than worker1 can provide", func() {
			job := testingjob.MakeJob("cpu-job", managerNs.Name).
				ManagedBy(kueue.MultiKueueControllerName).
				Queue(kueue.LocalQueueName(isolatedLq.Name)).
				Request(corev1.ResourceCPU, "100m").
				Obj()
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, job)).To(gomega.Succeed())

			oldWlLookupKey = types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

			createdWorkload := &kueue.Workload{}
			admission := utiltesting.MakeAdmission(isolatedCq.Name).Obj()
			gomega.Eventually(func() error {
				if err := managerTestCluster.client.Get(managerTestCluster.ctx, oldWlLookupKey, createdWorkload); err != nil {
					return err
				}
				return util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, createdWorkload, admission)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func() bool {
				managerWl := &kueue.Workload{}
				if err := managerTestCluster.client.Get(managerTestCluster.ctx, oldWlLookupKey, managerWl); err != nil {
					return false
				}
				if len(managerWl.Status.NominatedClusterNames) != 1 || managerWl.Status.NominatedClusterNames[0] != workerCluster1.Name {
					return false
				}
				if managerWl.Status.ClusterName != nil {
					return false
				}
				remoteWl := &kueue.Workload{}
				if err := worker1TestCluster.client.Get(worker1TestCluster.ctx, oldWlLookupKey, remoteWl); err != nil {
					return false
				}
				if err := worker2TestCluster.client.Get(worker2TestCluster.ctx, oldWlLookupKey, remoteWl); !apierrors.IsNotFound(err) {
					return false
				}
				return true
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		})

		ginkgo.By("adding worker2 to MultiKueueConfig", func() {
			gomega.Eventually(func() error {
				if err := managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(isolatedMultiKueueConfig), isolatedMultiKueueConfig); err != nil {
					return err
				}
				isolatedMultiKueueConfig.Spec.Clusters = []string{workerCluster1.Name, workerCluster2.Name}
				return managerTestCluster.client.Update(managerTestCluster.ctx, isolatedMultiKueueConfig)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				updatedAc := kueue.AdmissionCheck{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(isolatedAdmissionCheck), &updatedAc)).To(gomega.Succeed())
				g.Expect(updatedAc.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.AdmissionCheckActive))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying existing workload gets re-evaluated to see worker2", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				cluster2 := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster2), cluster2)).To(gomega.Succeed())
				g.Expect(cluster2.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.MultiKueueClusterActive))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				managerWl := &kueue.Workload{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, oldWlLookupKey, managerWl)).To(gomega.Succeed())

				actualClusters := sets.New(managerWl.Status.NominatedClusterNames...)
				g.Expect(actualClusters.Has(workerCluster2.Name)).To(gomega.BeTrue(),
					"workload should see worker2 after it's added to MultiKueueConfig")
				g.Expect(actualClusters.Has(workerCluster1.Name)).To(gomega.BeTrue(),
					"workload should still see worker1")
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying workloads get created on both worker clusters", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				remoteWl1 := &kueue.Workload{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, oldWlLookupKey, remoteWl1)).To(gomega.Succeed())

				remoteWl2 := &kueue.Workload{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, oldWlLookupKey, remoteWl2)).To(gomega.Succeed())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting workload reservation in worker2", func() {
			admission := utiltesting.MakeAdmission(isolatedCq.Name).Obj()
			gomega.Eventually(func(g gomega.Gomega) {
				remoteWl := &kueue.Workload{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, oldWlLookupKey, remoteWl)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(worker2TestCluster.ctx, worker2TestCluster.client, remoteWl, admission)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				remoteWl := &kueue.Workload{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, oldWlLookupKey, remoteWl)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying workload gets assigned to worker2 after reservation", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				managerWl := &kueue.Workload{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, oldWlLookupKey, managerWl)).To(gomega.Succeed())

				g.Expect(managerWl.Status.ClusterName).ToNot(gomega.BeNil(),
					"workload should be assigned to a worker cluster")

				assignedCluster := *managerWl.Status.ClusterName
				g.Expect(assignedCluster).To(gomega.Equal(workerCluster2.Name),
					"workload should be assigned to worker2 (200m CPU) not worker1 (50m CPU)")

				g.Expect(managerWl.Status.NominatedClusterNames).To(gomega.BeEmpty(),
					"nominated clusters should be cleared when workload is assigned")

				acState := admissioncheck.FindAdmissionCheck(managerWl.Status.AdmissionChecks, kueue.AdmissionCheckReference(isolatedAdmissionCheck.Name))
				g.Expect(acState).ToNot(gomega.BeNil())
				g.Expect(acState.State).To(gomega.Equal(kueue.CheckStateReady))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying new workloads see both clusters", func() {
			job := &batchv1.Job{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, types.NamespacedName{Name: "cpu-job", Namespace: managerNs.Name}, job)).To(gomega.Succeed())
			gomega.Expect(managerTestCluster.client.Delete(managerTestCluster.ctx, job)).To(gomega.Succeed())

			gomega.Eventually(func() error {
				wl := &kueue.Workload{}
				if err := managerTestCluster.client.Get(managerTestCluster.ctx, oldWlLookupKey, wl); err != nil {
					return err
				}
				return managerTestCluster.client.Delete(managerTestCluster.ctx, wl)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func() bool {
				wl := &kueue.Workload{}
				return apierrors.IsNotFound(managerTestCluster.client.Get(managerTestCluster.ctx, oldWlLookupKey, wl))
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())

			newJob := testingjob.MakeJob("new-cpu-job", managerNs.Name).
				ManagedBy(kueue.MultiKueueControllerName).
				Queue(kueue.LocalQueueName(isolatedLq.Name)).
				Request(corev1.ResourceCPU, "100m").
				Obj()
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, newJob)).To(gomega.Succeed())

			newWlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(newJob.Name, newJob.UID), Namespace: managerNs.Name}

			createdWorkload := &kueue.Workload{}
			admission := utiltesting.MakeAdmission(isolatedCq.Name).Obj()
			gomega.Eventually(func() error {
				if err := managerTestCluster.client.Get(managerTestCluster.ctx, newWlLookupKey, createdWorkload); err != nil {
					return err
				}
				return util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, createdWorkload, admission)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func() bool {
				managerWl := &kueue.Workload{}
				if err := managerTestCluster.client.Get(managerTestCluster.ctx, newWlLookupKey, managerWl); err != nil {
					return false
				}
				if len(managerWl.Status.NominatedClusterNames) != 2 {
					return false
				}
				expectedClusters := sets.New(workerCluster1.Name, workerCluster2.Name)
				actualClusters := sets.New(managerWl.Status.NominatedClusterNames...)
				if !expectedClusters.Equal(actualClusters) {
					return false
				}
				// Verify workload is dispatched to both workers
				remoteWl1 := &kueue.Workload{}
				if err := worker1TestCluster.client.Get(worker1TestCluster.ctx, newWlLookupKey, remoteWl1); err != nil {
					return false
				}
				remoteWl2 := &kueue.Workload{}
				return worker2TestCluster.client.Get(worker2TestCluster.ctx, newWlLookupKey, remoteWl2) == nil
			}, util.LongTimeout, util.Interval).Should(gomega.BeTrue())

			gomega.Eventually(func() error {
				if err := worker2TestCluster.client.Get(worker2TestCluster.ctx, newWlLookupKey, createdWorkload); err != nil {
					return err
				}
				return util.SetQuotaReservation(worker2TestCluster.ctx, worker2TestCluster.client, createdWorkload, admission)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func() bool {
				if err := managerTestCluster.client.Get(managerTestCluster.ctx, newWlLookupKey, createdWorkload); err != nil {
					return false
				}
				acs := admissioncheck.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(isolatedAdmissionCheck.Name))
				if acs == nil || acs.State != kueue.CheckStateReady {
					return false
				}
				return createdWorkload.Status.ClusterName != nil && *createdWorkload.Status.ClusterName == workerCluster2.Name
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		})

		ginkgo.By("cleaning up test resources", func() {
			gomega.Expect(client.IgnoreNotFound(managerTestCluster.client.Delete(managerTestCluster.ctx, isolatedLq))).To(gomega.Succeed())
			gomega.Expect(client.IgnoreNotFound(managerTestCluster.client.Delete(managerTestCluster.ctx, isolatedCq))).To(gomega.Succeed())
			gomega.Expect(client.IgnoreNotFound(managerTestCluster.client.Delete(managerTestCluster.ctx, isolatedAdmissionCheck))).To(gomega.Succeed())
			gomega.Expect(client.IgnoreNotFound(managerTestCluster.client.Delete(managerTestCluster.ctx, isolatedMultiKueueConfig))).To(gomega.Succeed())
		})
	})
})
