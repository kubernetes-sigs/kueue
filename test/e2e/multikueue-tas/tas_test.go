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

package mktas

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

const (
	tasNodeGroupLabel = "cloud.provider.com/node-group"
	tasInstanceType   = "tas-node"
)

type objAsPtr[T any] interface {
	client.Object
	*T
}

func expectObjectToBeDeletedOnWorkerClusters[PtrT objAsPtr[T], T any](ctx context.Context, obj PtrT) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		util.ExpectObjectToBeDeleted(ctx, k8sWorker1Client, obj, false)
		util.ExpectObjectToBeDeleted(ctx, k8sWorker2Client, obj, false)
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
}

func waitForJobAdmitted(wlLookupKey types.NamespacedName, acName, workerName string) {
	ginkgo.GinkgoHelper()
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		createdWorkload := &kueue.Workload{}
		g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
		g.Expect(admissioncheck.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(acName))).To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
			Name:    kueue.AdmissionCheckReference(acName),
			State:   kueue.CheckStateReady,
			Message: fmt.Sprintf(`The workload got reservation on "%s"`, workerName),
		}, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "PodSetUpdates")))
	}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
}

var _ = ginkgo.Describe("MultiKueue with TAS", ginkgo.Label("feature:tas", "area:multikueue", "feature:multikueue"), ginkgo.Ordered, func() {
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

		// One-level topology (hostname) on all clusters
		managerTopology = utiltestingapi.MakeDefaultOneLevelTopology("default")
		util.MustCreate(ctx, k8sManagerClient, managerTopology)

		worker1Topology = utiltestingapi.MakeDefaultOneLevelTopology("default")
		util.MustCreate(ctx, k8sWorker1Client, worker1Topology)

		worker2Topology = utiltestingapi.MakeDefaultOneLevelTopology("default")
		util.MustCreate(ctx, k8sWorker2Client, worker2Topology)

		// TAS ResourceFlavors on all clusters
		managerTasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel(tasNodeGroupLabel, tasInstanceType).
			TopologyName(managerTopology.Name).
			Obj()
		util.MustCreate(ctx, k8sManagerClient, managerTasFlavor)

		worker1TasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel(tasNodeGroupLabel, tasInstanceType).
			TopologyName(worker1Topology.Name).
			Obj()
		util.MustCreate(ctx, k8sWorker1Client, worker1TasFlavor)

		worker2TasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel(tasNodeGroupLabel, tasInstanceType).
			TopologyName(worker2Topology.Name).
			Obj()
		util.MustCreate(ctx, k8sWorker2Client, worker2TasFlavor)

		// MultiKueue setup on manager
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

		// Manager: 4 CPU quota with MultiKueue admission check
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

		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1Cq, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1TasFlavor, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1Topology, true, util.LongTimeout)

		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, worker2Cq, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, worker2TasFlavor, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, worker2Topology, true, util.LongTimeout)

		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerCq, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerTasFlavor, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerTopology, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, multiKueueAc, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, multiKueueConfig, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, workerCluster1, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, workerCluster2, true, util.LongTimeout)

		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sManagerClient, managerNs)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sWorker1Client, worker1Ns)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sWorker2Client, worker2Ns)
	})

	ginkgo.It("Should admit and run a TAS Job on worker via MultiKueue", func() {
		// Job requires 1.5 CPU, so it can only be admitted on worker1 (2 CPU quota).
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
		waitForJobAdmitted(wlLookupKey, multiKueueAc.Name, "worker1")

		ginkgo.By("Waiting for TopologyAssignment to be computed on worker1", func() {
			workerWlLookupKey := types.NamespacedName{Name: wlLookupKey.Name, Namespace: managerNs.Name}
			workerWl := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sWorker1Client.Get(ctx, workerWlLookupKey, workerWl)).To(gomega.Succeed())
				g.Expect(workerWl.Status.Admission).NotTo(gomega.BeNil())
				g.Expect(workerWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
				g.Expect(workerWl.Status.Admission.PodSetAssignments[0].TopologyAssignment).NotTo(gomega.BeNil())
				g.Expect(workerWl.Status.Admission.PodSetAssignments[0].TopologyAssignment.Levels).To(gomega.Equal([]string{corev1.LabelHostname}))
				g.Expect(tas.TotalDomainCount(workerWl.Status.Admission.PodSetAssignments[0].TopologyAssignment)).NotTo(gomega.BeZero())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for DelayedTopologyRequest to be marked Ready on manager", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				managerWl := &kueue.Workload{}
				g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
				g.Expect(managerWl.Status.Admission).NotTo(gomega.BeNil())
				g.Expect(managerWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
				g.Expect(managerWl.Status.Admission.PodSetAssignments[0].DelayedTopologyRequest).To(gomega.Equal(ptr.To(kueue.DelayedTopologyRequestStateReady)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Finishing the job's pods on worker1")
		listOpts := util.GetListOptsFromLabel(fmt.Sprintf("batch.kubernetes.io/job-name=%s", job.Name))
		util.WaitForActivePodsAndTerminate(ctx, k8sWorker1Client, worker1RestClient, worker1Cfg, job.Namespace, 1, 0, listOpts)

		ginkgo.By("Waiting for workload to finish")
		createdWorkload := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			g.Expect(createdWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(kueue.WorkloadFinished, kueue.WorkloadFinishedReasonSucceeded))
		}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Checking no objects are left in worker clusters and job is completed")
		expectObjectToBeDeletedOnWorkerClusters(ctx, createdWorkload)
		expectObjectToBeDeletedOnWorkerClusters(ctx, job)

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
		}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

		var assignedWorkerClient client.WithWatch
		if assignedClusterName == workerCluster1.Name {
			assignedWorkerClient = k8sWorker1Client
		} else {
			assignedWorkerClient = k8sWorker2Client
		}

		ginkgo.By(fmt.Sprintf("Waiting for TopologyAssignment to be computed on %s", assignedClusterName), func() {
			workerWlLookupKey := types.NamespacedName{Name: wlLookupKey.Name, Namespace: managerNs.Name}
			workerWl := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(assignedWorkerClient.Get(ctx, workerWlLookupKey, workerWl)).To(gomega.Succeed())
				g.Expect(workerWl.Status.Admission).NotTo(gomega.BeNil())
				g.Expect(workerWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
				g.Expect(workerWl.Status.Admission.PodSetAssignments[0].TopologyAssignment).NotTo(gomega.BeNil())
				g.Expect(workerWl.Status.Admission.PodSetAssignments[0].TopologyAssignment.Levels).To(gomega.Equal([]string{corev1.LabelHostname}))
				g.Expect(tas.TotalDomainCount(workerWl.Status.Admission.PodSetAssignments[0].TopologyAssignment)).NotTo(gomega.BeZero())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for DelayedTopologyRequest to be marked Ready on manager", func() {
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Status.Admission).NotTo(gomega.BeNil())
				g.Expect(createdWorkload.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
				g.Expect(createdWorkload.Status.Admission.PodSetAssignments[0].DelayedTopologyRequest).To(gomega.Equal(ptr.To(kueue.DelayedTopologyRequestStateReady)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Finishing the job's pods")
		listOpts := util.GetListOptsFromLabel(fmt.Sprintf("batch.kubernetes.io/job-name=%s", job.Name))
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
		}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Checking cleanup on worker clusters")
		expectObjectToBeDeletedOnWorkerClusters(ctx, createdWorkload)
		expectObjectToBeDeletedOnWorkerClusters(ctx, job)
	})
})
