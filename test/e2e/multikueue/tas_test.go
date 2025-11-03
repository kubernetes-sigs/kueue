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

package mke2e

import (
	"context"
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
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
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
		util.MustCreate(ctx, k8sManagerClient, multiKueueAc)

		ginkgo.By("wait for check active", func() {
			updatedAc := kueue.AdmissionCheck{}
			acKey := client.ObjectKeyFromObject(multiKueueAc)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sManagerClient.Get(ctx, acKey, &updatedAc)).To(gomega.Succeed())
				g.Expect(updatedAc.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.AdmissionCheckActive))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		managerTopology = utiltestingapi.MakeTopology("default").Levels(corev1.LabelHostname).Obj()
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
		util.MustCreate(ctx, k8sManagerClient, managerCq)

		managerLq = utiltestingapi.MakeLocalQueue(managerCq.Name, managerNs.Name).ClusterQueue(managerCq.Name).Obj()
		util.MustCreate(ctx, k8sManagerClient, managerLq)

		worker1Topology = utiltestingapi.MakeTopology("default").Levels(corev1.LabelHostname).Obj()
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
		util.MustCreate(ctx, k8sWorker1Client, worker1Cq)

		worker1Lq = utiltestingapi.MakeLocalQueue(worker1Cq.Name, worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
		util.MustCreate(ctx, k8sWorker1Client, worker1Lq)

		worker2Topology = utiltestingapi.MakeTopology("default").Levels(corev1.LabelHostname).Obj()
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
		util.MustCreate(ctx, k8sWorker2Client, worker2Cq)

		worker2Lq = utiltestingapi.MakeLocalQueue(worker2Cq.Name, worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
		util.MustCreate(ctx, k8sWorker2Client, worker2Lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sManagerClient, managerNs)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sWorker1Client, worker1Ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sWorker2Client, worker2Ns)).To(gomega.Succeed())

		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1Cq, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1Flavor, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1Topology, true, util.LongTimeout)

		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, worker2Cq, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, worker2Flavor, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, worker2Topology, true, util.LongTimeout)

		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerCq, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerFlavor, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerTopology, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, multiKueueAc, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, multiKueueConfig, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, workerCluster1, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, workerCluster2, true, util.LongTimeout)

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
			var assignedWorkerCtx context.Context
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
						assignedWorkerCtx = ctx
					} else {
						assignedWorkerCluster = k8sWorker2Client
						assignedWorkerCtx = ctx
					}

					g.Expect(admissioncheck.FindAdmissionCheck(managerWl.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAc.Name))).To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
						Name:    kueue.AdmissionCheckReference(multiKueueAc.Name),
						State:   kueue.CheckStateReady,
						Message: fmt.Sprintf(`The workload got reservation on "%s"`, assignedClusterName),
					}, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime")))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By(fmt.Sprintf("Waiting for TopologyAssignment to be computed in %s", assignedClusterName), func() {
				workerWlLookupKey := types.NamespacedName{Name: wlLookupKey.Name, Namespace: managerNs.Name}
				workerWl := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(assignedWorkerCluster.Get(assignedWorkerCtx, workerWlLookupKey, workerWl)).To(gomega.Succeed())
					g.Expect(workerWl.Status.Admission).NotTo(gomega.BeNil())
					g.Expect(workerWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
					g.Expect(workerWl.Status.Admission.PodSetAssignments[0].TopologyAssignment).NotTo(gomega.BeNil())
					g.Expect(workerWl.Status.Admission.PodSetAssignments[0].TopologyAssignment.Levels).To(gomega.Equal([]string{corev1.LabelHostname}))
					g.Expect(workerWl.Status.Admission.PodSetAssignments[0].TopologyAssignment.Domains).NotTo(gomega.BeEmpty())
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

			ginkgo.By("Waiting for the job to get status updates", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdJob := batchv1.Job{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
					g.Expect(createdJob.Status.StartTime).NotTo(gomega.BeNil())
					g.Expect(createdJob.Status.Active).To(gomega.Equal(int32(2)))
					g.Expect(createdJob.Status.CompletionTime).To(gomega.BeNil())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Finishing the job's pods", func() {
				listOpts := util.GetListOptsFromLabel(fmt.Sprintf("batch.kubernetes.io/job-name=%s", job.Name))
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
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking no objects are left in the worker clusters and the job is completed", func() {
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
				gomega.Eventually(func(g gomega.Gomega) {
					managerWl := &kueue.Workload{}
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
					g.Expect(managerWl.Status.Admission).NotTo(gomega.BeNil())
					g.Expect(managerWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
					g.Expect(managerWl.Status.Admission.PodSetAssignments[0].DelayedTopologyRequest).To(gomega.Equal(ptr.To(kueue.DelayedTopologyRequestStateReady)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Finishing the job", func() {
				listOpts := util.GetListOptsFromLabel(fmt.Sprintf("batch.kubernetes.io/job-name=%s", job.Name))
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
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
