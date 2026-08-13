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

	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	gomegatypes "github.com/onsi/gomega/types"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

var defaultEnabledIntegrations = sets.New(
	"batch/job", "kubeflow.org/mpijob", "ray.io/rayjob", "ray.io/raycluster", "ray.io/rayservice",
	"jobset.x-k8s.io/jobset", "kubeflow.org/paddlejob",
	"kubeflow.org/pytorchjob", "kubeflow.org/tfjob", "kubeflow.org/xgboostjob", "kubeflow.org/jaxjob",
	"pod", "workload.codeflare.dev/appwrapper", "trainer.kubeflow.org/trainjob")

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

// multiKueueFixture holds the manager/worker resources that every MultiKueue
// integration spec needs: namespaces, kubeconfig secrets, MultiKueueClusters,
// the MultiKueueConfig, the MultiKueue AdmissionCheck, and the ClusterQueue,
// LocalQueue and ResourceFlavor on the manager and both workers.
//
// Per-framework test files create it in BeforeEach via setupMultiKueueFixture
// and release it in AfterEach via teardown, so the common setup lives in one
// place instead of being duplicated in every file.
type multiKueueFixture struct {
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
}

func setupMultiKueueFixture() *multiKueueFixture {
	ginkgo.GinkgoHelper()
	f := &multiKueueFixture{}

	f.managerNs = util.CreateNamespaceFromPrefixWithLog(managerTestCluster.ctx, managerTestCluster.client, "multikueue-")
	f.worker1Ns = util.CreateNamespaceWithLog(worker1TestCluster.ctx, worker1TestCluster.client, f.managerNs.Name)
	f.worker2Ns = util.CreateNamespaceWithLog(worker2TestCluster.ctx, worker2TestCluster.client, f.managerNs.Name)

	w1Kubeconfig, err := worker1TestCluster.kubeConfigBytes()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	w2Kubeconfig, err := worker2TestCluster.kubeConfigBytes()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	f.managerMultiKueueSecret1 = utiltesting.MakeSecret("multikueue1", managersConfigNamespace.Name).Data(kueue.MultiKueueConfigSecretKey, w1Kubeconfig).Obj()
	util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, f.managerMultiKueueSecret1)

	f.managerMultiKueueSecret2 = utiltesting.MakeSecret("multikueue2", managersConfigNamespace.Name).Data(kueue.MultiKueueConfigSecretKey, w2Kubeconfig).Obj()
	util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, f.managerMultiKueueSecret2)

	f.workerCluster1 = utiltestingapi.MakeMultiKueueCluster("worker1").KubeConfig(kueue.SecretLocationType, f.managerMultiKueueSecret1.Name).Obj()
	util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, f.workerCluster1)

	f.workerCluster2 = utiltestingapi.MakeMultiKueueCluster("worker2").KubeConfig(kueue.SecretLocationType, f.managerMultiKueueSecret2.Name).Obj()
	util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, f.workerCluster2)

	f.managerMultiKueueConfig = utiltestingapi.MakeMultiKueueConfig("multikueueconfig").Clusters(f.workerCluster1.Name, f.workerCluster2.Name).Obj()
	util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, f.managerMultiKueueConfig)

	f.multiKueueAC = utiltestingapi.MakeAdmissionCheck("ac1").
		ControllerName(kueue.MultiKueueControllerName).
		Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", f.managerMultiKueueConfig.Name).
		Obj()
	util.CreateAdmissionChecksAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, f.multiKueueAC)

	f.managerFlavor = utiltestingapi.MakeResourceFlavor(string(multikueueTestFlavor)).Obj()
	util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, f.managerFlavor)

	f.managerCq = utiltestingapi.MakeClusterQueue("q1").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas(string(multikueueTestFlavor)).Resource(corev1.ResourceCPU, "5").Obj()).
		AdmissionChecks(kueue.AdmissionCheckReference(f.multiKueueAC.Name)).
		Obj()
	util.CreateClusterQueuesAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, f.managerCq)

	f.managerLq = utiltestingapi.MakeLocalQueue(f.managerCq.Name, f.managerNs.Name).ClusterQueue(f.managerCq.Name).Obj()
	util.CreateLocalQueuesAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, f.managerLq)

	f.worker1Flavor = utiltestingapi.MakeResourceFlavor(string(multikueueTestFlavor)).Obj()
	util.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, f.worker1Flavor)
	f.worker1Cq = utiltestingapi.MakeClusterQueue("q1").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas(string(multikueueTestFlavor)).Resource(corev1.ResourceCPU, "5").Obj()).
		Obj()
	util.CreateClusterQueuesAndWaitForActive(worker1TestCluster.ctx, worker1TestCluster.client, f.worker1Cq)
	f.worker1Lq = utiltestingapi.MakeLocalQueue(f.worker1Cq.Name, f.worker1Ns.Name).ClusterQueue(f.worker1Cq.Name).Obj()
	util.CreateLocalQueuesAndWaitForActive(worker1TestCluster.ctx, worker1TestCluster.client, f.worker1Lq)

	f.worker2Flavor = utiltestingapi.MakeResourceFlavor(string(multikueueTestFlavor)).Obj()
	util.MustCreate(worker2TestCluster.ctx, worker2TestCluster.client, f.worker2Flavor)
	f.worker2Cq = utiltestingapi.MakeClusterQueue("q1").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas(string(multikueueTestFlavor)).Resource(corev1.ResourceCPU, "5").Obj()).
		Obj()
	util.CreateClusterQueuesAndWaitForActive(worker2TestCluster.ctx, worker2TestCluster.client, f.worker2Cq)
	f.worker2Lq = utiltestingapi.MakeLocalQueue(f.worker2Cq.Name, f.worker2Ns.Name).ClusterQueue(f.worker2Cq.Name).Obj()
	util.CreateLocalQueuesAndWaitForActive(worker2TestCluster.ctx, worker2TestCluster.client, f.worker2Lq)

	return f
}

func (f *multiKueueFixture) teardown() {
	ginkgo.GinkgoHelper()
	gomega.Expect(util.DeleteNamespace(managerTestCluster.ctx, managerTestCluster.client, f.managerNs)).To(gomega.Succeed())
	gomega.Expect(util.DeleteNamespace(worker1TestCluster.ctx, worker1TestCluster.client, f.worker1Ns)).To(gomega.Succeed())
	gomega.Expect(util.DeleteNamespace(worker2TestCluster.ctx, worker2TestCluster.client, f.worker2Ns)).To(gomega.Succeed())
	util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, f.managerCq, true)
	util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, f.worker1Cq, true)
	util.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, f.worker2Cq, true)
	util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, f.managerFlavor, true)
	util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, f.worker1Flavor, true)
	util.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, f.worker2Flavor, true)
	util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, f.multiKueueAC, true)
	util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, f.managerMultiKueueConfig, true)
	util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, f.workerCluster1, true)
	util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, f.workerCluster2, true)
	util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, f.managerMultiKueueSecret1, true)
	util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, f.managerMultiKueueSecret2, true)
}
