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
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/provisioning"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("MultiKueue with ProvisioningRequest", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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

		managerRf *kueue.ResourceFlavor
		managerCq *kueue.ClusterQueue
		managerLq *kueue.LocalQueue

		worker1ProvReqConfig *kueue.ProvisioningRequestConfig
		worker1ProvReqAC     *kueue.AdmissionCheck
		worker1Rf            *kueue.ResourceFlavor
		worker1Cq            *kueue.ClusterQueue
		worker1Lq            *kueue.LocalQueue

		worker2ProvReqConfig *kueue.ProvisioningRequestConfig
		worker2ProvReqAC     *kueue.AdmissionCheck
		worker2Rf            *kueue.ResourceFlavor
		worker2Cq            *kueue.ClusterQueue
		worker2Lq            *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
			managerAndMultiKueueSetup(ctx, mgr, 2*time.Second, sets.New("batch/job"), config.MultiKueueDispatcherModeAllAtOnce)
		})
	})

	ginkgo.AfterAll(func() {
		managerTestCluster.fwk.StopManager(managerTestCluster.ctx)
	})

	ginkgo.BeforeEach(func() {
		managerNs = util.CreateNamespaceFromPrefixWithLog(managerTestCluster.ctx, managerTestCluster.client, "mk-prov-")
		worker1Ns = util.CreateNamespaceWithLog(worker1TestCluster.ctx, worker1TestCluster.client, managerNs.Name)
		worker2Ns = util.CreateNamespaceWithLog(worker2TestCluster.ctx, worker2TestCluster.client, managerNs.Name)

		w1Kubeconfig, err := worker1TestCluster.kubeConfigBytes()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		w2Kubeconfig, err := worker2TestCluster.kubeConfigBytes()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		// Setup MultiKueue resources
		managerMultiKueueSecret1 = utiltesting.MakeSecret("multikueue-prov-secret1", managersConfigNamespace.Name).Data(kueue.MultiKueueConfigSecretKey, w1Kubeconfig).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueSecret1)).To(gomega.Succeed())
		managerMultiKueueSecret2 = utiltesting.MakeSecret("multikueue-prov-secret2", managersConfigNamespace.Name).Data(kueue.MultiKueueConfigSecretKey, w2Kubeconfig).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueSecret2)).To(gomega.Succeed())

		workerCluster1 = utiltestingapi.MakeMultiKueueCluster("worker1-prov").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret1.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, workerCluster1)).To(gomega.Succeed())
		workerCluster2 = utiltestingapi.MakeMultiKueueCluster("worker2-prov").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret2.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, workerCluster2)).To(gomega.Succeed())

		managerMultiKueueConfig = utiltestingapi.MakeMultiKueueConfig("mk-prov-config").Clusters(workerCluster1.Name, workerCluster2.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueConfig)).Should(gomega.Succeed())

		multiKueueAC = utiltestingapi.MakeAdmissionCheck("mk-ac").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", managerMultiKueueConfig.Name).
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, multiKueueAC)).Should(gomega.Succeed())

		ginkgo.By("wait for multikueue admission check to be active", func() {
			util.ExpectAdmissionChecksToBeActive(managerTestCluster.ctx, managerTestCluster.client, multiKueueAC)
		})

		managerRf = utiltestingapi.MakeResourceFlavor("manager-rf").NodeLabel("instance-type", "manager-node").Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerRf)).To(gomega.Succeed())

		managerCq = utiltestingapi.MakeClusterQueue("cq-mk-prov").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(managerRf.Name).
				Resource(corev1.ResourceCPU, "5").
				Obj()).
			AdmissionChecks(kueue.AdmissionCheckReference(multiKueueAC.Name)).
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerCq)).Should(gomega.Succeed())
		util.ExpectClusterQueuesToBeActive(managerTestCluster.ctx, managerTestCluster.client, managerCq)

		managerLq = utiltestingapi.MakeLocalQueue(managerCq.Name, managerNs.Name).ClusterQueue(managerCq.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerLq)).Should(gomega.Succeed())
		util.ExpectLocalQueuesToBeActive(managerTestCluster.ctx, managerTestCluster.client, managerLq)

		// Worker 1 setup
		worker1ProvReqConfig = utiltestingapi.MakeProvisioningRequestConfig("prov-config").
			ProvisioningClass("test-provisioning-class").
			Obj()
		gomega.Expect(worker1TestCluster.client.Create(worker1TestCluster.ctx, worker1ProvReqConfig)).Should(gomega.Succeed())

		worker1ProvReqAC = utiltestingapi.MakeAdmissionCheck("prov-ac").
			ControllerName(kueue.ProvisioningRequestControllerName).
			Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", worker1ProvReqConfig.Name).
			Obj()
		gomega.Expect(worker1TestCluster.client.Create(worker1TestCluster.ctx, worker1ProvReqAC)).Should(gomega.Succeed())

		ginkgo.By("wait for worker provisioning admission check to be active", func() {
			util.ExpectAdmissionChecksToBeActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1ProvReqAC)
		})

		worker1Rf = utiltestingapi.MakeResourceFlavor("worker1-rf").NodeLabel("instance-type", "worker-node").Obj()
		gomega.Expect(worker1TestCluster.client.Create(worker1TestCluster.ctx, worker1Rf)).To(gomega.Succeed())

		worker1Cq = utiltestingapi.MakeClusterQueue("cq-mk-prov").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(worker1Rf.Name).
				Resource(corev1.ResourceCPU, "5").
				Obj()).
			AdmissionChecks(kueue.AdmissionCheckReference(worker1ProvReqAC.Name)).
			Obj()
		gomega.Expect(worker1TestCluster.client.Create(worker1TestCluster.ctx, worker1Cq)).Should(gomega.Succeed())
		util.ExpectClusterQueuesToBeActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq)

		worker1Lq = utiltestingapi.MakeLocalQueue(worker1Cq.Name, worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
		gomega.Expect(worker1TestCluster.client.Create(worker1TestCluster.ctx, worker1Lq)).Should(gomega.Succeed())
		util.ExpectLocalQueuesToBeActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Lq)

		// Worker 2 setup
		worker2ProvReqConfig = utiltestingapi.MakeProvisioningRequestConfig("prov-config").
			ProvisioningClass("test-provisioning-class").
			Obj()
		gomega.Expect(worker2TestCluster.client.Create(worker2TestCluster.ctx, worker2ProvReqConfig)).Should(gomega.Succeed())

		worker2ProvReqAC = utiltestingapi.MakeAdmissionCheck("prov-ac").
			ControllerName(kueue.ProvisioningRequestControllerName).
			Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", worker2ProvReqConfig.Name).
			Obj()
		gomega.Expect(worker2TestCluster.client.Create(worker2TestCluster.ctx, worker2ProvReqAC)).Should(gomega.Succeed())

		ginkgo.By("wait for worker provisioning admission check to be active", func() {
			util.ExpectAdmissionChecksToBeActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2ProvReqAC)
		})

		worker2Rf = utiltestingapi.MakeResourceFlavor("worker2-rf").NodeLabel("instance-type", "worker-node").Obj()
		gomega.Expect(worker2TestCluster.client.Create(worker2TestCluster.ctx, worker2Rf)).To(gomega.Succeed())

		worker2Cq = utiltestingapi.MakeClusterQueue("cq-mk-prov").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(worker2Rf.Name).
				Resource(corev1.ResourceCPU, "5").
				Obj()).
			AdmissionChecks(kueue.AdmissionCheckReference(worker2ProvReqAC.Name)).
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
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerRf, true)
		util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1Rf, true)
		util.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2Rf, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, multiKueueAC, true)
		util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1ProvReqAC, true)
		util.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2ProvReqAC, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueConfig, true)
		util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1ProvReqConfig, true)
		util.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2ProvReqConfig, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, workerCluster1, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, workerCluster2, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret1, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret2, true)
	})

	ginkgo.It("Should create workload on worker and provision resources", func() {
		job := testingjob.MakeJob("test-job", managerNs.Name).
			Queue(kueue.LocalQueueName(managerLq.Name)).
			Request(corev1.ResourceCPU, "2").
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, job)).Should(gomega.Succeed())

		managerWlKey := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
			Namespace: managerNs.Name,
		}
		worker1WlKey := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
			Namespace: worker1Ns.Name,
		}

		ginkgo.By("setting quota reservation on manager cluster", func() {
			admission := utiltestingapi.MakeAdmission(managerCq.Name).
				PodSets(
					utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(managerRf.Name), "2").
						Obj(),
				).
				Obj()
			util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, managerWlKey, admission)
		})

		ginkgo.By("verifying workload is created on worker cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				workerWl := &kueue.Workload{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, worker1WlKey, workerWl)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting quota reservation on worker cluster", func() {
			admission := utiltestingapi.MakeAdmission(worker1Cq.Name).
				PodSets(
					utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(worker1Rf.Name), "2").
						Obj(),
				).
				Obj()
			util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, worker1WlKey, admission)
		})

		provReqKey := types.NamespacedName{
			Namespace: worker1Ns.Name,
			Name:      provisioning.ProvisioningRequestName(worker1WlKey.Name, kueue.AdmissionCheckReference(worker1ProvReqAC.Name), 1),
		}

		ginkgo.By("waiting for provisioning request and marking it provisioned", func() {
			createdProvReq := &autoscaling.ProvisioningRequest{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, provReqKey, createdProvReq)).To(gomega.Succeed())
				apimeta.SetStatusCondition(&createdProvReq.Status.Conditions, metav1.Condition{
					Type:   autoscaling.Provisioned,
					Status: metav1.ConditionTrue,
					Reason: autoscaling.Provisioned,
				})
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, createdProvReq)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking the provisioning admission check is ready on worker", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				workerWl := &kueue.Workload{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, worker1WlKey, workerWl)).To(gomega.Succeed())

				provCheck := admissioncheck.FindAdmissionCheck(workerWl.Status.AdmissionChecks, kueue.AdmissionCheckReference(worker1ProvReqAC.Name))
				g.Expect(provCheck).NotTo(gomega.BeNil())
				g.Expect(provCheck.State).To(gomega.Equal(kueue.CheckStateReady))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking the multikueue admission check is ready on manager", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				managerWl := &kueue.Workload{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, managerWlKey, managerWl)).To(gomega.Succeed())

				mkCheck := admissioncheck.FindAdmissionCheck(managerWl.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAC.Name))
				g.Expect(mkCheck).NotTo(gomega.BeNil())
				g.Expect(mkCheck.State).To(gomega.Equal(kueue.CheckStateReady))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying the workload is admitted on worker", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				workerWl := &kueue.Workload{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, worker1WlKey, workerWl)).To(gomega.Succeed())
				g.Expect(workerWl.Status.Admission).NotTo(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying the workload is admitted on manager", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				managerWl := &kueue.Workload{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, managerWlKey, managerWl)).To(gomega.Succeed())
				g.Expect(managerWl.Status.Admission).NotTo(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should wait for admission before deleting non-selected worker workloads", func() {
		job := testingjob.MakeJob("test-job2", managerNs.Name).
			Queue(kueue.LocalQueueName(managerLq.Name)).
			Request(corev1.ResourceCPU, "2").
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, job)).Should(gomega.Succeed())

		managerWlKey := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
			Namespace: managerNs.Name,
		}
		worker1WlKey := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
			Namespace: worker1Ns.Name,
		}
		worker2WlKey := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
			Namespace: worker2Ns.Name,
		}

		ginkgo.By("setting quota reservation on manager cluster", func() {
			admission := utiltestingapi.MakeAdmission(managerCq.Name).
				PodSets(
					utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(managerRf.Name), "2").
						Obj(),
				).
				Obj()
			util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, managerWlKey, admission)
		})

		ginkgo.By("verifying workloads are created on both worker clusters", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				worker1Wl := &kueue.Workload{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, worker1WlKey, worker1Wl)).To(gomega.Succeed())
				worker2Wl := &kueue.Workload{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, worker2WlKey, worker2Wl)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting quota reservation on worker1 (creates ProvisioningRequest)", func() {
			admission := utiltestingapi.MakeAdmission(worker1Cq.Name).
				PodSets(
					utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(worker1Rf.Name), "2").
						Obj(),
				).
				Obj()
			util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, worker1WlKey, admission)
		})

		ginkgo.By("verifying worker2 workload still exists while worker1 is not admitted", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				worker2Wl := &kueue.Workload{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, worker2WlKey, worker2Wl)).To(gomega.Succeed())
			}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
		})

		worker1ProvReqKey := types.NamespacedName{
			Namespace: worker1Ns.Name,
			Name:      provisioning.ProvisioningRequestName(worker1WlKey.Name, kueue.AdmissionCheckReference(worker1ProvReqAC.Name), 1),
		}

		ginkgo.By("marking worker1's ProvisioningRequest as provisioned", func() {
			createdProvReq := &autoscaling.ProvisioningRequest{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, worker1ProvReqKey, createdProvReq)).To(gomega.Succeed())
				apimeta.SetStatusCondition(&createdProvReq.Status.Conditions, metav1.Condition{
					Type:   autoscaling.Provisioned,
					Status: metav1.ConditionTrue,
					Reason: autoscaling.Provisioned,
				})
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, createdProvReq)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying worker1 workload is admitted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				worker1Wl := &kueue.Workload{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, worker1WlKey, worker1Wl)).To(gomega.Succeed())
				g.Expect(apimeta.IsStatusConditionTrue(worker1Wl.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying worker2 workload is deleted after worker1 is admitted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				worker2Wl := &kueue.Workload{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, worker2WlKey, worker2Wl)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying the multikueue admission check is ready on manager", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				managerWl := &kueue.Workload{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, managerWlKey, managerWl)).To(gomega.Succeed())
				mkCheck := admissioncheck.FindAdmissionCheck(managerWl.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAC.Name))
				g.Expect(mkCheck).NotTo(gomega.BeNil())
				g.Expect(mkCheck.State).To(gomega.Equal(kueue.CheckStateReady))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
