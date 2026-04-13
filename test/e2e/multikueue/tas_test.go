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
	"fmt"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

const (
	tasNodeGroupLabel = "cloud.provider.com/node-group"
	instanceType      = "tas-node"
)

func waitForTopologyAssignment(workerClient client.Client, wlLookupKey types.NamespacedName) {
	ginkgo.GinkgoHelper()
	workerWl := &kueue.Workload{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(workerClient.Get(ctx, wlLookupKey, workerWl)).To(gomega.Succeed())
		g.Expect(workerWl.Status.Admission).NotTo(gomega.BeNil())
		g.Expect(workerWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
		g.Expect(workerWl.Status.Admission.PodSetAssignments[0].TopologyAssignment).NotTo(gomega.BeNil())
		g.Expect(workerWl.Status.Admission.PodSetAssignments[0].TopologyAssignment.Levels).To(gomega.Equal([]string{corev1.LabelHostname}))
		g.Expect(tas.TotalDomainCount(workerWl.Status.Admission.PodSetAssignments[0].TopologyAssignment)).NotTo(gomega.BeZero())
	}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
}

func waitForDelayedTopologyRequestReady(wlLookupKey types.NamespacedName) {
	ginkgo.GinkgoHelper()
	managerWl := &kueue.Workload{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
		g.Expect(managerWl.Status.Admission).NotTo(gomega.BeNil())
		g.Expect(managerWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
		g.Expect(managerWl.Status.Admission.PodSetAssignments[0].DelayedTopologyRequest).To(gomega.Equal(ptr.To(kueue.DelayedTopologyRequestStateReady)))
	}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
}

var _ = ginkgo.Describe("MultiKueue with TopologyAwareScheduling", func() {
	var (
		managerNs *corev1.Namespace
		worker1Ns *corev1.Namespace
		worker2Ns *corev1.Namespace

		workerCluster1   *kueue.MultiKueueCluster
		workerCluster2   *kueue.MultiKueueCluster
		multiKueueConfig *kueue.MultiKueueConfig
		multiKueueAc     *kueue.AdmissionCheck

		managerTopology *kueue.Topology
		managerFlavor   *kueue.ResourceFlavor
		managerCq       *kueue.ClusterQueue
		managerLq       *kueue.LocalQueue

		worker1Topology *kueue.Topology
		worker1Flavor   *kueue.ResourceFlavor
		worker1Cq       *kueue.ClusterQueue
		worker1Lq       *kueue.LocalQueue

		worker2Topology *kueue.Topology
		worker2Flavor   *kueue.ResourceFlavor
		worker2Cq       *kueue.ClusterQueue
		worker2Lq       *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		managerNs = util.CreateNamespaceFromPrefixWithLog(ctx, k8sManagerClient, "multikueue-tas-")
		worker1Ns = util.CreateNamespaceWithLog(ctx, k8sWorker1Client, managerNs.Name)
		worker2Ns = util.CreateNamespaceWithLog(ctx, k8sWorker2Client, managerNs.Name)

		workerCluster1 = utiltestingapi.MakeMultiKueueCluster("worker1").KubeConfig(kueue.SecretLocationType, "multikueue1").Obj()
		util.MustCreate(ctx, k8sManagerClient, workerCluster1)

		workerCluster2 = utiltestingapi.MakeMultiKueueCluster("worker2").KubeConfig(kueue.SecretLocationType, "multikueue2").Obj()
		util.MustCreate(ctx, k8sManagerClient, workerCluster2)

		multiKueueConfig = utiltestingapi.MakeMultiKueueConfig("multikueueconfig").Clusters("worker1", "worker2").Obj()
		util.MustCreate(ctx, k8sManagerClient, multiKueueConfig)

		multiKueueAc = utiltestingapi.MakeAdmissionCheck("ac1").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", multiKueueConfig.Name).
			Obj()
		util.CreateAdmissionChecksAndWaitForActive(ctx, k8sManagerClient, multiKueueAc)

		managerTopology = utiltestingapi.MakeDefaultOneLevelTopology("default")
		util.MustCreate(ctx, k8sManagerClient, managerTopology)

		managerFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel(tasNodeGroupLabel, instanceType).
			TopologyName(managerTopology.Name).
			Obj()
		util.MustCreate(ctx, k8sManagerClient, managerFlavor)

		managerCq = utiltestingapi.MakeClusterQueue("q1").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(managerFlavor.Name).
					Resource(corev1.ResourceCPU, "8").
					Resource(corev1.ResourceMemory, "8Gi").
					Obj(),
			).
			AdmissionChecks(kueue.AdmissionCheckReference(multiKueueAc.Name)).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sManagerClient, managerCq)

		managerLq = utiltestingapi.MakeLocalQueue(managerCq.Name, managerNs.Name).ClusterQueue(managerCq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sManagerClient, managerLq)

		worker1Topology = utiltestingapi.MakeDefaultOneLevelTopology("default")
		util.MustCreate(ctx, k8sWorker1Client, worker1Topology)

		worker1Flavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel(tasNodeGroupLabel, instanceType).
			TopologyName(worker1Topology.Name).
			Obj()
		util.MustCreate(ctx, k8sWorker1Client, worker1Flavor)

		worker1Cq = utiltestingapi.MakeClusterQueue("q1").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(worker1Flavor.Name).
					Resource(corev1.ResourceCPU, "8").
					Resource(corev1.ResourceMemory, "4Gi").
					Obj(),
			).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sWorker1Client, worker1Cq)

		worker1Lq = utiltestingapi.MakeLocalQueue(worker1Cq.Name, worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sWorker1Client, worker1Lq)

		worker2Topology = utiltestingapi.MakeDefaultOneLevelTopology("default")
		util.MustCreate(ctx, k8sWorker2Client, worker2Topology)

		worker2Flavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel(tasNodeGroupLabel, instanceType).
			TopologyName(worker2Topology.Name).
			Obj()
		util.MustCreate(ctx, k8sWorker2Client, worker2Flavor)

		worker2Cq = utiltestingapi.MakeClusterQueue("q1").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(worker2Flavor.Name).
					Resource(corev1.ResourceCPU, "4").
					Resource(corev1.ResourceMemory, "8Gi").
					Obj(),
			).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sWorker2Client, worker2Cq)

		worker2Lq = utiltestingapi.MakeLocalQueue(worker2Cq.Name, worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sWorker2Client, worker2Lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sManagerClient, managerNs)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sWorker1Client, worker1Ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sWorker2Client, worker2Ns)).To(gomega.Succeed())

		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1Cq, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1Flavor, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1Topology, true, util.MediumTimeout)

		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, worker2Cq, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, worker2Flavor, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, worker2Topology, true, util.MediumTimeout)

		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerCq, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerFlavor, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerTopology, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, multiKueueAc, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, multiKueueConfig, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, workerCluster1, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, workerCluster2, true, util.MediumTimeout)

		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sManagerClient, managerNs)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sWorker1Client, worker1Ns)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sWorker2Client, worker2Ns)
	})

	ginkgo.When("Creating a Job with TAS requirements", func() {
		ginkgo.It("Should admit a Job and assign topology in the worker cluster", func() {
			job := testingjob.MakeJob("tas-job", managerNs.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				Parallelism(2).
				Completions(2).
				RequestAndLimit(corev1.ResourceCPU, "500m").
				RequestAndLimit(corev1.ResourceMemory, "200Mi").
				TerminationGracePeriod(1).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Obj()
			job = (&testingjob.JobWrapper{Job: *job}).
				PodAnnotation(kueue.PodSetRequiredTopologyAnnotation, corev1.LabelHostname).
				Obj()

			ginkgo.By("Creating the job", func() {
				util.MustCreate(ctx, k8sManagerClient, job)
				gomega.Eventually(func(g gomega.Gomega) {
					createdJob := &batchv1.Job{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
					g.Expect(ptr.Deref(createdJob.Spec.ManagedBy, "")).To(gomega.BeEquivalentTo(kueue.MultiKueueControllerName))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

			var assignedWorkerCluster client.Client
			var assignedClusterName string
			ginkgo.By("checking which worker cluster was assigned", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					managerWl := &kueue.Workload{}
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
					g.Expect(managerWl.Status.ClusterName).NotTo(gomega.BeNil())

					assignedClusterName = *managerWl.Status.ClusterName
					g.Expect(assignedClusterName).To(gomega.Or(gomega.Equal(workerCluster1.Name), gomega.Equal(workerCluster2.Name)))
					if assignedClusterName == workerCluster1.Name {
						assignedWorkerCluster = k8sWorker1Client
					} else {
						assignedWorkerCluster = k8sWorker2Client
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				util.ExpectAdmissionCheckStateWithMessage(
					ctx, k8sManagerClient, wlLookupKey,
					multiKueueAc.Name,
					kueue.CheckStateReady,
					fmt.Sprintf(`The workload got reservation on "%s"`, assignedClusterName),
				)
			})

			ginkgo.By(fmt.Sprintf("Waiting for TopologyAssignment to be computed in %s", assignedClusterName), func() {
				waitForTopologyAssignment(assignedWorkerCluster, wlLookupKey)
			})

			ginkgo.By("Waiting for DelayedTopologyRequest to be marked Ready on manager", func() {
				waitForDelayedTopologyRequestReady(wlLookupKey)
			})

			ginkgo.By("Waiting for the job to get status updates", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdJob := batchv1.Job{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
					g.Expect(createdJob.Status.StartTime).NotTo(gomega.BeNil())
					g.Expect(createdJob.Status.Active).To(gomega.Equal(int32(2)))
					g.Expect(createdJob.Status.CompletionTime).To(gomega.BeNil())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Finishing the job's pods", func() {
				listOpts := util.GetListOptsFromLabel(fmt.Sprintf("%s=%s", batchv1.JobNameLabel, job.Name))
				if assignedClusterName == workerCluster1.Name {
					util.WaitForActivePodsAndTerminate(ctx, k8sWorker1Client, worker1RestClient, worker1Cfg, job.Namespace, 2, 0, listOpts)
				} else {
					util.WaitForActivePodsAndTerminate(ctx, k8sWorker2Client, worker2RestClient, worker2Cfg, job.Namespace, 2, 0, listOpts)
				}
			})

			ginkgo.By("Waiting for the job to finish", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(kueue.WorkloadFinished, kueue.WorkloadFinishedReasonSucceeded))
				}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking no objects are left in the worker clusters and the job is completed", func() {
				util.ExpectObjectToBeDeletedOnClusters(ctx, createdWorkload, k8sWorker1Client, k8sWorker2Client)
				util.ExpectObjectToBeDeletedOnClusters(ctx, job, k8sWorker1Client, k8sWorker2Client)

				createdJob := &batchv1.Job{}
				gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
				gomega.Expect(createdJob.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(
					batchv1.JobCondition{
						Type:   batchv1.JobComplete,
						Status: corev1.ConditionTrue,
					},
					cmpopts.IgnoreFields(batchv1.JobCondition{}, "LastTransitionTime", "LastProbeTime", "Reason", "Message"))))
			})
		})

		ginkgo.It("Should handle implicit TAS", func() {
			job := testingjob.MakeJob("implicit-tas-job", managerNs.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				Parallelism(2).
				Completions(2).
				RequestAndLimit(corev1.ResourceCPU, "500m").
				RequestAndLimit(corev1.ResourceMemory, "200Mi").
				TerminationGracePeriod(1).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Obj()

			ginkgo.By("Creating the job without TAS annotation", func() {
				util.MustCreate(ctx, k8sManagerClient, job)
				gomega.Eventually(func(g gomega.Gomega) {
					createdJob := &batchv1.Job{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
					g.Expect(ptr.Deref(createdJob.Spec.ManagedBy, "")).To(gomega.BeEquivalentTo(kueue.MultiKueueControllerName))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

			var assignedClusterName string
			ginkgo.By("checking which worker cluster was assigned", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					managerWl := &kueue.Workload{}
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
					g.Expect(managerWl.Status.ClusterName).NotTo(gomega.BeNil())
					assignedClusterName = *managerWl.Status.ClusterName
					g.Expect(apimeta.FindStatusCondition(managerWl.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeComparableTo(&metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionTrue,
						Reason:  "Admitted",
						Message: "The workload is admitted",
					}, util.IgnoreConditionTimestampsAndObservedGeneration))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for DelayedTopologyRequest to be marked Ready on manager", func() {
				waitForDelayedTopologyRequestReady(wlLookupKey)
			})

			ginkgo.By("Finishing the job", func() {
				listOpts := util.GetListOptsFromLabel(fmt.Sprintf("%s=%s", batchv1.JobNameLabel, job.Name))
				if assignedClusterName == workerCluster1.Name {
					util.WaitForActivePodsAndTerminate(ctx, k8sWorker1Client, worker1RestClient, worker1Cfg, job.Namespace, 2, 0, listOpts)
				} else {
					util.WaitForActivePodsAndTerminate(ctx, k8sWorker2Client, worker2RestClient, worker2Cfg, job.Namespace, 2, 0, listOpts)
				}
			})

			ginkgo.By("Waiting for the job to finish", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeTrue())
					g.Expect(createdWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(kueue.WorkloadFinished, kueue.WorkloadFinishedReasonSucceeded))
				}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})

var _ = ginkgo.Describe("MultiKueue TAS with asymmetric quotas", func() {
	var (
		managerNs *corev1.Namespace
		worker1Ns *corev1.Namespace
		worker2Ns *corev1.Namespace

		managerTopology *kueue.Topology
		worker1Topology *kueue.Topology
		worker2Topology *kueue.Topology

		workerCluster1   *kueue.MultiKueueCluster
		workerCluster2   *kueue.MultiKueueCluster
		multiKueueConfig *kueue.MultiKueueConfig
		multiKueueAc     *kueue.AdmissionCheck

		managerTasFlavor *kueue.ResourceFlavor
		worker1TasFlavor *kueue.ResourceFlavor
		worker2TasFlavor *kueue.ResourceFlavor

		managerCq *kueue.ClusterQueue
		worker1Cq *kueue.ClusterQueue
		worker2Cq *kueue.ClusterQueue

		managerLq *kueue.LocalQueue
		worker1Lq *kueue.LocalQueue
		worker2Lq *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		managerNs = util.CreateNamespaceFromPrefixWithLog(ctx, k8sManagerClient, "multikueue-tas-")
		worker1Ns = util.CreateNamespaceWithLog(ctx, k8sWorker1Client, managerNs.Name)
		worker2Ns = util.CreateNamespaceWithLog(ctx, k8sWorker2Client, managerNs.Name)

		managerTopology = utiltestingapi.MakeDefaultOneLevelTopology("default")
		util.MustCreate(ctx, k8sManagerClient, managerTopology)

		worker1Topology = utiltestingapi.MakeDefaultOneLevelTopology("default")
		util.MustCreate(ctx, k8sWorker1Client, worker1Topology)

		worker2Topology = utiltestingapi.MakeDefaultOneLevelTopology("default")
		util.MustCreate(ctx, k8sWorker2Client, worker2Topology)

		managerTasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel(tasNodeGroupLabel, instanceType).
			TopologyName(managerTopology.Name).
			Obj()
		util.MustCreate(ctx, k8sManagerClient, managerTasFlavor)

		worker1TasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel(tasNodeGroupLabel, instanceType).
			TopologyName(worker1Topology.Name).
			Obj()
		util.MustCreate(ctx, k8sWorker1Client, worker1TasFlavor)

		worker2TasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel(tasNodeGroupLabel, instanceType).
			TopologyName(worker2Topology.Name).
			Obj()
		util.MustCreate(ctx, k8sWorker2Client, worker2TasFlavor)

		workerCluster1 = utiltestingapi.MakeMultiKueueCluster("worker1").KubeConfig(kueue.SecretLocationType, "multikueue1").Obj()
		util.MustCreate(ctx, k8sManagerClient, workerCluster1)

		workerCluster2 = utiltestingapi.MakeMultiKueueCluster("worker2").KubeConfig(kueue.SecretLocationType, "multikueue2").Obj()
		util.MustCreate(ctx, k8sManagerClient, workerCluster2)

		multiKueueConfig = utiltestingapi.MakeMultiKueueConfig("multikueueconfig").Clusters("worker1", "worker2").Obj()
		util.MustCreate(ctx, k8sManagerClient, multiKueueConfig)

		multiKueueAc = utiltestingapi.MakeAdmissionCheck("ac1").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", multiKueueConfig.Name).
			Obj()
		util.CreateAdmissionChecksAndWaitForActive(ctx, k8sManagerClient, multiKueueAc)

		managerCq = utiltestingapi.MakeClusterQueue("tas-cq").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(managerTasFlavor.Name).
					Resource(corev1.ResourceCPU, "4").
					Resource(corev1.ResourceMemory, "4Gi").
					Obj(),
			).
			AdmissionChecks(kueue.AdmissionCheckReference(multiKueueAc.Name)).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sManagerClient, managerCq)

		managerLq = utiltestingapi.MakeLocalQueue(managerCq.Name, managerNs.Name).ClusterQueue(managerCq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sManagerClient, managerLq)

		// Worker1: 2 CPU quota (can fit 1.5 CPU jobs)
		worker1Cq = utiltestingapi.MakeClusterQueue("tas-cq").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(worker1TasFlavor.Name).
					Resource(corev1.ResourceCPU, "2").
					Resource(corev1.ResourceMemory, "1Gi").
					Obj(),
			).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sWorker1Client, worker1Cq)

		worker1Lq = utiltestingapi.MakeLocalQueue(worker1Cq.Name, worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sWorker1Client, worker1Lq)

		// Worker2: 1 CPU quota (cannot fit 1.5 CPU jobs)
		worker2Cq = utiltestingapi.MakeClusterQueue("tas-cq").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(worker2TasFlavor.Name).
					Resource(corev1.ResourceCPU, "1").
					Resource(corev1.ResourceMemory, "4Gi").
					Obj(),
			).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sWorker2Client, worker2Cq)

		worker2Lq = utiltestingapi.MakeLocalQueue(worker2Cq.Name, worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sWorker2Client, worker2Lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sManagerClient, managerNs)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sWorker1Client, worker1Ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sWorker2Client, worker2Ns)).To(gomega.Succeed())

		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1Cq, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1TasFlavor, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1Topology, true, util.MediumTimeout)

		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, worker2Cq, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, worker2TasFlavor, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, worker2Topology, true, util.MediumTimeout)

		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerCq, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerTasFlavor, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerTopology, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, multiKueueAc, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, multiKueueConfig, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, workerCluster1, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, workerCluster2, true, util.MediumTimeout)

		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sManagerClient, managerNs)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sWorker1Client, worker1Ns)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sWorker2Client, worker2Ns)
	})

	ginkgo.It("Should route a TAS Job with preferred topology to worker1 deterministically", func() {
		ginkgo.By("Creating Job with preferred hostname topology on manager")
		job := testingjob.MakeJob("tas-job", managerNs.Name).
			Queue(kueue.LocalQueueName(managerLq.Name)).
			PodAnnotation(kueue.PodSetPreferredTopologyAnnotation, corev1.LabelHostname).
			Request(corev1.ResourceCPU, "1500m").
			Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
			Obj()
		util.MustCreate(ctx, k8sManagerClient, job)

		wlLookupKey := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
			Namespace: managerNs.Name,
		}
		util.ExpectWorkloadAdmittedWithCheck(ctx, wlLookupKey, multiKueueAc.Name, "worker1", k8sManagerClient)

		ginkgo.By("Waiting for TopologyAssignment to be computed on worker1", func() {
			waitForTopologyAssignment(k8sWorker1Client, wlLookupKey)
		})

		ginkgo.By("Waiting for DelayedTopologyRequest to be marked Ready on manager", func() {
			waitForDelayedTopologyRequestReady(wlLookupKey)
		})

		ginkgo.By("Finishing the job's pods on worker1")
		listOpts := util.GetListOptsFromLabel(fmt.Sprintf("%s=%s", batchv1.JobNameLabel, job.Name))
		util.WaitForActivePodsAndTerminate(ctx, k8sWorker1Client, worker1RestClient, worker1Cfg, job.Namespace, 1, 0, listOpts)

		ginkgo.By("Waiting for workload to finish")
		createdWorkload := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			g.Expect(createdWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(kueue.WorkloadFinished, kueue.WorkloadFinishedReasonSucceeded))
		}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Checking no objects are left in worker clusters and job is completed")
		util.ExpectObjectToBeDeletedOnClusters(ctx, createdWorkload, k8sWorker1Client, k8sWorker2Client)
		util.ExpectObjectToBeDeletedOnClusters(ctx, job, k8sWorker1Client, k8sWorker2Client)

		createdJob := &batchv1.Job{}
		gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
		gomega.Expect(createdJob.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(
			batchv1.JobCondition{
				Type:   batchv1.JobComplete,
				Status: corev1.ConditionTrue,
			},
			cmpopts.IgnoreFields(batchv1.JobCondition{}, "LastTransitionTime", "LastProbeTime", "Reason", "Message"))))
	})

	ginkgo.It("Should admit a TAS Job with required hostname topology", func() {
		ginkgo.By("Creating Job with required hostname topology on manager")
		job := testingjob.MakeJob("tas-required-job", managerNs.Name).
			Queue(kueue.LocalQueueName(managerLq.Name)).
			PodAnnotation(kueue.PodSetRequiredTopologyAnnotation, corev1.LabelHostname).
			Request(corev1.ResourceCPU, "500m").
			Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
			Obj()
		util.MustCreate(ctx, k8sManagerClient, job)

		wlLookupKey := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
			Namespace: managerNs.Name,
		}

		var assignedClusterName string
		ginkgo.By("Waiting for workload to be admitted and assigned to a worker")
		gomega.Eventually(func(g gomega.Gomega) {
			createdWorkload := &kueue.Workload{}
			g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			g.Expect(createdWorkload.Status.Admission).NotTo(gomega.BeNil())
			g.Expect(createdWorkload.Status.Admission.PodSetAssignments).NotTo(gomega.BeEmpty())
			g.Expect(createdWorkload.Status.ClusterName).NotTo(gomega.BeNil())
			assignedClusterName = *createdWorkload.Status.ClusterName
		}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())

		var assignedWorkerClient client.Client
		if assignedClusterName == workerCluster1.Name {
			assignedWorkerClient = k8sWorker1Client
		} else {
			assignedWorkerClient = k8sWorker2Client
		}

		ginkgo.By(fmt.Sprintf("Waiting for TopologyAssignment to be computed on %s", assignedClusterName), func() {
			waitForTopologyAssignment(assignedWorkerClient, wlLookupKey)
		})

		ginkgo.By("Waiting for DelayedTopologyRequest to be marked Ready on manager", func() {
			waitForDelayedTopologyRequestReady(wlLookupKey)
		})

		ginkgo.By("Finishing the job's pods")
		listOpts := util.GetListOptsFromLabel(fmt.Sprintf("%s=%s", batchv1.JobNameLabel, job.Name))
		if assignedClusterName == workerCluster1.Name {
			util.WaitForActivePodsAndTerminate(ctx, k8sWorker1Client, worker1RestClient, worker1Cfg, job.Namespace, 1, 0, listOpts)
		} else {
			util.WaitForActivePodsAndTerminate(ctx, k8sWorker2Client, worker2RestClient, worker2Cfg, job.Namespace, 1, 0, listOpts)
		}

		ginkgo.By("Waiting for workload to finish")
		createdWorkload := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			g.Expect(createdWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(kueue.WorkloadFinished, kueue.WorkloadFinishedReasonSucceeded))
		}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Checking cleanup on worker clusters")
		util.ExpectObjectToBeDeletedOnClusters(ctx, createdWorkload, k8sWorker1Client, k8sWorker2Client)
		util.ExpectObjectToBeDeletedOnClusters(ctx, job, k8sWorker1Client, k8sWorker2Client)
	})
})
