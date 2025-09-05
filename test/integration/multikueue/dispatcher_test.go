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
})

var _ = ginkgo.Describe("MultiKueueConfig Re-evaluation", ginkgo.Ordered, func() {
	var (
		testMultiKueueConfig *kueue.MultiKueueConfig
		testAdmissionCheck   *kueue.AdmissionCheck
		managerClusterQueue  *kueue.ClusterQueue
		managerLocalQueue    *kueue.LocalQueue
		testJob              *batchv1.Job
		workloadLookupKey    types.NamespacedName
		testNamespace        *corev1.Namespace
		
		// Cluster connection secrets
		worker1Secret *corev1.Secret
		worker2Secret *corev1.Secret
		worker1Cluster *kueue.MultiKueueCluster
		worker2Cluster *kueue.MultiKueueCluster
		
		// Worker cluster queues  
		worker1ClusterQueue *kueue.ClusterQueue
		worker2ClusterQueue *kueue.ClusterQueue
	)

	ginkgo.BeforeAll(func() {
		managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
			managerAndMultiKueueSetup(ctx, mgr, 2*time.Second, defaultEnabledIntegrations, config.MultiKueueDispatcherModeAllAtOnce)
		})
		
		// Create isolated namespace for this test suite
		testNamespace = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "multikueue-re-eval-",
			},
		}
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, testNamespace)).To(gomega.Succeed())

		// Create corresponding namespaces on worker clusters
		worker1Namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: testNamespace.Name,
			},
		}
		gomega.Expect(worker1TestCluster.client.Create(worker1TestCluster.ctx, worker1Namespace)).To(gomega.Succeed())

		worker2Namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: testNamespace.Name,
			},
		}
		gomega.Expect(worker2TestCluster.client.Create(worker2TestCluster.ctx, worker2Namespace)).To(gomega.Succeed())

		// Set up cluster connection credentials
		worker1KubeConfig, err := worker1TestCluster.kubeConfigBytes()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		worker2KubeConfig, err := worker2TestCluster.kubeConfigBytes()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		worker1Secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multikueue1-re-eval",
				Namespace: managersConfigNamespace.Name,
			},
			Data: map[string][]byte{
				kueue.MultiKueueConfigSecretKey: worker1KubeConfig,
			},
		}
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, worker1Secret)).To(gomega.Succeed())

		worker2Secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multikueue2-re-eval",
				Namespace: managersConfigNamespace.Name,
			},
			Data: map[string][]byte{
				kueue.MultiKueueConfigSecretKey: worker2KubeConfig,
			},
		}
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, worker2Secret)).To(gomega.Succeed())

		worker1Cluster = utiltesting.MakeMultiKueueCluster("worker1-re-eval").KubeConfig(kueue.SecretLocationType, worker1Secret.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, worker1Cluster)).To(gomega.Succeed())

		worker2Cluster = utiltesting.MakeMultiKueueCluster("worker2-re-eval").KubeConfig(kueue.SecretLocationType, worker2Secret.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, worker2Cluster)).To(gomega.Succeed())

		// Create worker cluster queues
		worker1ClusterQueue = utiltesting.MakeClusterQueue("q1-re-eval").Obj()
		gomega.Expect(worker1TestCluster.client.Create(worker1TestCluster.ctx, worker1ClusterQueue)).Should(gomega.Succeed())
		
		worker2ClusterQueue = utiltesting.MakeClusterQueue("q1-re-eval").Obj()
		gomega.Expect(worker2TestCluster.client.Create(worker2TestCluster.ctx, worker2ClusterQueue)).Should(gomega.Succeed())

		// Create MultiKueueConfig with only worker1 initially (worker2 will be added during test)
		testMultiKueueConfig = utiltesting.MakeMultiKueueConfig("isolated-config").Clusters(worker1Cluster.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, testMultiKueueConfig)).To(gomega.Succeed())

		// Create admission check for the config
		testAdmissionCheck = utiltesting.MakeAdmissionCheck("isolated-ac").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", testMultiKueueConfig.Name).
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, testAdmissionCheck)).To(gomega.Succeed())

		// Wait for admission check to become active
		gomega.Eventually(func(g gomega.Gomega) {
			updatedAdmissionCheck := kueue.AdmissionCheck{}
			g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(testAdmissionCheck), &updatedAdmissionCheck)).To(gomega.Succeed())
			g.Expect(updatedAdmissionCheck.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.AdmissionCheckActive))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// Create cluster queue with the admission check
		managerClusterQueue = utiltesting.MakeClusterQueue("isolated-cq").
			AdmissionChecks(kueue.AdmissionCheckReference(testAdmissionCheck.Name)).
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerClusterQueue)).To(gomega.Succeed())
		util.ExpectClusterQueuesToBeActive(managerTestCluster.ctx, managerTestCluster.client, managerClusterQueue)

		// Create local queue
		managerLocalQueue = utiltesting.MakeLocalQueue("isolated-lq", testNamespace.Name).ClusterQueue(managerClusterQueue.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerLocalQueue)).To(gomega.Succeed())
		util.ExpectLocalQueuesToBeActive(managerTestCluster.ctx, managerTestCluster.client, managerLocalQueue)

		// Configure worker clusters with different resource constraints
		// Worker1: 50m CPU (insufficient for our 100m test workload)
		worker1ResourceFlavor := utiltesting.MakeResourceFlavor("w1-re-eval-flavor").Obj()
		gomega.Expect(worker1TestCluster.client.Create(worker1TestCluster.ctx, worker1ResourceFlavor)).To(gomega.Succeed())
		gomega.Eventually(func() error {
			if err := worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(worker1ClusterQueue), worker1ClusterQueue); err != nil {
				return err
			}
			worker1ClusterQueue.Spec.ResourceGroups = []kueue.ResourceGroup{
				{
					CoveredResources: []corev1.ResourceName{corev1.ResourceCPU},
					Flavors: []kueue.FlavorQuotas{
						*utiltesting.MakeFlavorQuotas("w1-re-eval-flavor").Resource(corev1.ResourceCPU, "50m").Obj(),
					},
				},
			}
			return worker1TestCluster.client.Update(worker1TestCluster.ctx, worker1ClusterQueue)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// Worker2: 200m CPU (sufficient for our 100m test workload)  
		worker2ResourceFlavor := utiltesting.MakeResourceFlavor("w2-re-eval-flavor").Obj()
		gomega.Expect(worker2TestCluster.client.Create(worker2TestCluster.ctx, worker2ResourceFlavor)).To(gomega.Succeed())
		gomega.Eventually(func() error {
			if err := worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(worker2ClusterQueue), worker2ClusterQueue); err != nil {
				return err
			}
			worker2ClusterQueue.Spec.ResourceGroups = []kueue.ResourceGroup{
				{
					CoveredResources: []corev1.ResourceName{corev1.ResourceCPU},
					Flavors: []kueue.FlavorQuotas{
						*utiltesting.MakeFlavorQuotas("w2-re-eval-flavor").Resource(corev1.ResourceCPU, "200m").Obj(),
					},
				},
			}
			return worker2TestCluster.client.Update(worker2TestCluster.ctx, worker2ClusterQueue)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		util.ExpectClusterQueuesToBeActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1ClusterQueue)
		util.ExpectClusterQueuesToBeActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2ClusterQueue)
	})

	ginkgo.AfterAll(func() {
		// Clean up resources while manager is still running to avoid webhook issues
		if testNamespace != nil {
			// Delete namespace first - this will cascade delete most resources
			gomega.Expect(util.DeleteNamespace(managerTestCluster.ctx, managerTestCluster.client, testNamespace)).To(gomega.Succeed())
			// Also cleanup worker cluster namespaces
			workerNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace.Name}}
			gomega.Expect(util.DeleteNamespace(worker1TestCluster.ctx, worker1TestCluster.client, workerNamespace)).To(gomega.Succeed())
			gomega.Expect(util.DeleteNamespace(worker2TestCluster.ctx, worker2TestCluster.client, workerNamespace)).To(gomega.Succeed())
		}
		
		// Delete cluster-scoped resources using ExpectObjectToBeDeleted like other tests
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerClusterQueue, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, testAdmissionCheck, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, testMultiKueueConfig, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, worker1Cluster, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, worker2Cluster, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, worker1Secret, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, worker2Secret, true)
		util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1ClusterQueue, true)
		util.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2ClusterQueue, true)
		
		// Stop manager last after cleanup is complete
		managerTestCluster.fwk.StopManager(managerTestCluster.ctx)
	})

	ginkgo.It("should create workload that requires more CPU than worker1 can provide", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MultiKueueBatchJobWithManagedBy, true)
		testJob = testingjob.MakeJob("cpu-job", testNamespace.Name).
			ManagedBy(kueue.MultiKueueControllerName).
			Queue(kueue.LocalQueueName(managerLocalQueue.Name)).
			Request(corev1.ResourceCPU, "100m"). // More than worker1's 50m limit
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, testJob)).To(gomega.Succeed())
		workloadLookupKey = types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(testJob.Name, testJob.UID), Namespace: testNamespace.Name}
	})

	ginkgo.It("should initially nominate only worker1 and set reservation", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MultiKueueBatchJobWithManagedBy, true)
		createdWorkload := &kueue.Workload{}
		admission := utiltesting.MakeAdmission(managerClusterQueue.Name).Obj()
		
		// Set quota reservation to trigger MultiKueue processing
		gomega.Eventually(func() error {
			if err := managerTestCluster.client.Get(managerTestCluster.ctx, workloadLookupKey, createdWorkload); err != nil {
				return err
			}
			return util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, createdWorkload, admission)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// Verify workload is nominated to worker1 only and created on worker1
		gomega.Eventually(func(g gomega.Gomega) {
			managerWorkload := &kueue.Workload{}
			g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, workloadLookupKey, managerWorkload)).To(gomega.Succeed())
			g.Expect(managerWorkload.Status.NominatedClusterNames).To(gomega.ConsistOf(worker1Cluster.Name))
			g.Expect(managerWorkload.Status.ClusterName).To(gomega.BeNil())

			// Verify workload created on worker1
			remoteWorkload := &kueue.Workload{}
			g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, workloadLookupKey, remoteWorkload)).To(gomega.Succeed())

			// Verify workload NOT created on worker2 (not in config yet)
			g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, workloadLookupKey, remoteWorkload)).To(utiltesting.BeNotFoundError())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("should re-evaluate existing workload when worker2 is added to config", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MultiKueueBatchJobWithManagedBy, true)
		// Add worker2 to the MultiKueueConfig
		gomega.Eventually(func() error {
			if err := managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(testMultiKueueConfig), testMultiKueueConfig); err != nil {
				return err
			}
			testMultiKueueConfig.Spec.Clusters = []string{worker1Cluster.Name, worker2Cluster.Name}
			return managerTestCluster.client.Update(managerTestCluster.ctx, testMultiKueueConfig)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// Wait for admission check to remain active with both clusters
		gomega.Eventually(func(g gomega.Gomega) {
			updatedAdmissionCheck := kueue.AdmissionCheck{}
			g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(testAdmissionCheck), &updatedAdmissionCheck)).To(gomega.Succeed())
			g.Expect(updatedAdmissionCheck.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.AdmissionCheckActive))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// Verify existing workload gets re-evaluated and sees both workers
		gomega.Eventually(func(g gomega.Gomega) {
			cluster2 := &kueue.MultiKueueCluster{}
			g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(worker2Cluster), cluster2)).To(gomega.Succeed())
			g.Expect(cluster2.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.MultiKueueClusterActive))
		}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

		gomega.Eventually(func(g gomega.Gomega) {
			managerWorkload := &kueue.Workload{}
			g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, workloadLookupKey, managerWorkload)).To(gomega.Succeed())
			actualClusters := sets.New(managerWorkload.Status.NominatedClusterNames...)
			g.Expect(actualClusters.Has(worker2Cluster.Name)).To(gomega.BeTrue(),
				"workload should see worker2 after it's added to MultiKueueConfig")
			g.Expect(actualClusters.Has(worker1Cluster.Name)).To(gomega.BeTrue(),
				"workload should still see worker1")
		}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

		// Verify workloads get created on both worker clusters
		gomega.Eventually(func(g gomega.Gomega) {
			remoteWorkload1 := &kueue.Workload{}
			g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, workloadLookupKey, remoteWorkload1)).To(gomega.Succeed())
			remoteWorkload2 := &kueue.Workload{}
			g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, workloadLookupKey, remoteWorkload2)).To(gomega.Succeed())
		}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("should assign workload to worker2 after reservation", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MultiKueueBatchJobWithManagedBy, true)
		// Set workload reservation on worker2 (which has sufficient CPU)
		admission := utiltesting.MakeAdmission(managerClusterQueue.Name).Obj()
		gomega.Eventually(func(g gomega.Gomega) {
			remoteWorkload := &kueue.Workload{}
			g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, workloadLookupKey, remoteWorkload)).To(gomega.Succeed())
			g.Expect(util.SetQuotaReservation(worker2TestCluster.ctx, worker2TestCluster.client, remoteWorkload, admission)).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// Verify workload on worker1 gets removed (non-reserving cluster)
		gomega.Eventually(func(g gomega.Gomega) {
			remoteWorkload := &kueue.Workload{}
			g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, workloadLookupKey, remoteWorkload)).To(utiltesting.BeNotFoundError())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// Verify workload gets assigned to worker2 in manager cluster
		gomega.Eventually(func(g gomega.Gomega) {
			managerWorkload := &kueue.Workload{}
			g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, workloadLookupKey, managerWorkload)).To(gomega.Succeed())
			g.Expect(managerWorkload.Status.ClusterName).ToNot(gomega.BeNil(),
				"workload should be assigned to a worker cluster")
			assignedCluster := *managerWorkload.Status.ClusterName
			g.Expect(assignedCluster).To(gomega.Equal(worker2Cluster.Name),
				"workload should be assigned to worker2 (200m CPU) not worker1 (50m CPU)")
			g.Expect(managerWorkload.Status.NominatedClusterNames).To(gomega.BeEmpty(),
				"nominated clusters should be cleared when workload is assigned")
			admissionCheckState := admissioncheck.FindAdmissionCheck(managerWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(testAdmissionCheck.Name))
			g.Expect(admissionCheckState).ToNot(gomega.BeNil())
			g.Expect(admissionCheckState.State).To(gomega.Equal(kueue.CheckStateReady))
		}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
	})
})
