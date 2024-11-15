/*
Copyright 2023 The Kubernetes Authors.

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
	"fmt"
	"os/exec"

	"github.com/google/go-cmp/cmp/cmpopts"
	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	versionutil "k8s.io/apimachinery/pkg/util/version"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	workloadjobset "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	workloadpytorchjob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/pytorchjob"
	workloadmpijob "sigs.k8s.io/kueue/pkg/controller/jobs/mpijob"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	testingmpijob "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"
	testingpytorchjob "sigs.k8s.io/kueue/pkg/util/testingjobs/pytorchjob"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("MultiKueue", func() {
	var (
		managerNs *corev1.Namespace
		worker1Ns *corev1.Namespace
		worker2Ns *corev1.Namespace

		workerCluster1   *kueue.MultiKueueCluster
		workerCluster2   *kueue.MultiKueueCluster
		multiKueueConfig *kueue.MultiKueueConfig
		multiKueueAc     *kueue.AdmissionCheck
		managerFlavor    *kueue.ResourceFlavor
		managerCq        *kueue.ClusterQueue
		managerLq        *kueue.LocalQueue

		worker1Flavor *kueue.ResourceFlavor
		worker1Cq     *kueue.ClusterQueue
		worker1Lq     *kueue.LocalQueue

		worker2Flavor *kueue.ResourceFlavor
		worker2Cq     *kueue.ClusterQueue
		worker2Lq     *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		managerNs = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "multikueue-",
			},
		}
		gomega.Expect(k8sManagerClient.Create(ctx, managerNs)).To(gomega.Succeed())

		worker1Ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: managerNs.Name,
			},
		}
		gomega.Expect(k8sWorker1Client.Create(ctx, worker1Ns)).To(gomega.Succeed())

		worker2Ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: managerNs.Name,
			},
		}
		gomega.Expect(k8sWorker2Client.Create(ctx, worker2Ns)).To(gomega.Succeed())

		workerCluster1 = utiltesting.MakeMultiKueueCluster("worker1").KubeConfig(kueue.SecretLocationType, "multikueue1").Obj()
		gomega.Expect(k8sManagerClient.Create(ctx, workerCluster1)).To(gomega.Succeed())

		workerCluster2 = utiltesting.MakeMultiKueueCluster("worker2").KubeConfig(kueue.SecretLocationType, "multikueue2").Obj()
		gomega.Expect(k8sManagerClient.Create(ctx, workerCluster2)).To(gomega.Succeed())

		multiKueueConfig = utiltesting.MakeMultiKueueConfig("multikueueconfig").Clusters("worker1", "worker2").Obj()
		gomega.Expect(k8sManagerClient.Create(ctx, multiKueueConfig)).Should(gomega.Succeed())

		multiKueueAc = utiltesting.MakeAdmissionCheck("ac1").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", multiKueueConfig.Name).
			Obj()
		gomega.Expect(k8sManagerClient.Create(ctx, multiKueueAc)).Should(gomega.Succeed())

		ginkgo.By("wait for check active", func() {
			updatedAc := kueue.AdmissionCheck{}
			acKey := client.ObjectKeyFromObject(multiKueueAc)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sManagerClient.Get(ctx, acKey, &updatedAc)).To(gomega.Succeed())
				g.Expect(updatedAc.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.AdmissionCheckActive))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
		managerFlavor = utiltesting.MakeResourceFlavor("default").Obj()
		gomega.Expect(k8sManagerClient.Create(ctx, managerFlavor)).Should(gomega.Succeed())

		managerCq = utiltesting.MakeClusterQueue("q1").
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas(managerFlavor.Name).
					Resource(corev1.ResourceCPU, "2").
					Resource(corev1.ResourceMemory, "2G").
					Obj(),
			).
			AdmissionChecks(multiKueueAc.Name).
			Obj()
		gomega.Expect(k8sManagerClient.Create(ctx, managerCq)).Should(gomega.Succeed())

		managerLq = utiltesting.MakeLocalQueue(managerCq.Name, managerNs.Name).ClusterQueue(managerCq.Name).Obj()
		gomega.Expect(k8sManagerClient.Create(ctx, managerLq)).Should(gomega.Succeed())

		worker1Flavor = utiltesting.MakeResourceFlavor("default").Obj()
		gomega.Expect(k8sWorker1Client.Create(ctx, worker1Flavor)).Should(gomega.Succeed())

		worker1Cq = utiltesting.MakeClusterQueue("q1").
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas(worker1Flavor.Name).
					Resource(corev1.ResourceCPU, "2").
					Resource(corev1.ResourceMemory, "1G").
					Obj(),
			).
			Obj()
		gomega.Expect(k8sWorker1Client.Create(ctx, worker1Cq)).Should(gomega.Succeed())

		worker1Lq = utiltesting.MakeLocalQueue(worker1Cq.Name, worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
		gomega.Expect(k8sWorker1Client.Create(ctx, worker1Lq)).Should(gomega.Succeed())

		worker2Flavor = utiltesting.MakeResourceFlavor("default").Obj()
		gomega.Expect(k8sWorker2Client.Create(ctx, worker2Flavor)).Should(gomega.Succeed())

		worker2Cq = utiltesting.MakeClusterQueue("q1").
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas(worker2Flavor.Name).
					Resource(corev1.ResourceCPU, "1").
					Resource(corev1.ResourceMemory, "2G").
					Obj(),
			).
			Obj()
		gomega.Expect(k8sWorker2Client.Create(ctx, worker2Cq)).Should(gomega.Succeed())

		worker2Lq = utiltesting.MakeLocalQueue(worker2Cq.Name, worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
		gomega.Expect(k8sWorker2Client.Create(ctx, worker2Lq)).Should(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sManagerClient, managerNs)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sWorker1Client, worker1Ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sWorker2Client, worker2Ns)).To(gomega.Succeed())

		util.ExpectObjectToBeDeleted(ctx, k8sWorker1Client, worker1Cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sWorker1Client, worker1Flavor, true)

		util.ExpectObjectToBeDeleted(ctx, k8sWorker2Client, worker2Cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sWorker2Client, worker2Flavor, true)

		util.ExpectObjectToBeDeleted(ctx, k8sManagerClient, managerCq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sManagerClient, managerFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sManagerClient, multiKueueAc, true)
		util.ExpectObjectToBeDeleted(ctx, k8sManagerClient, multiKueueConfig, true)
		util.ExpectObjectToBeDeleted(ctx, k8sManagerClient, workerCluster1, true)
		util.ExpectObjectToBeDeleted(ctx, k8sManagerClient, workerCluster2, true)
	})

	ginkgo.When("Creating a multikueue admission check", func() {
		ginkgo.It("Should run a job on worker if admitted", func() {
			if managerK8SVersion.LessThan(versionutil.MustParseSemantic("1.30.0")) {
				ginkgo.Skip("the managers kubernetes version is less then 1.30")
			}
			// Since it requires 2G of memory, this job can only be admitted in worker 2.
			job := testingjob.MakeJob("job", managerNs.Name).
				Queue(managerLq.Name).
				Request("cpu", "1").
				Request("memory", "2G").
				// Give it the time to be observed Active in the live status update step.
				Image(util.E2eTestSleepImage, []string{"5s"}).
				Obj()

			ginkgo.By("Creating the job", func() {
				gomega.Expect(k8sManagerClient.Create(ctx, job)).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					createdJob := &batchv1.Job{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
					g.Expect(ptr.Deref(createdJob.Spec.ManagedBy, "")).To(gomega.BeEquivalentTo(kueue.MultiKueueControllerName))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			createdLeaderWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

			// the execution should be given to the worker
			ginkgo.By("Waiting to be admitted in worker2, and the manager's job unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdLeaderWorkload)).To(gomega.Succeed())
					g.Expect(workload.FindAdmissionCheck(createdLeaderWorkload.Status.AdmissionChecks, multiKueueAc.Name)).To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
						Name:    multiKueueAc.Name,
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker2"`,
					}, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime")))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					createdJob := &batchv1.Job{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
					g.Expect(ptr.Deref(createdJob.Spec.Suspend, false)).To(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for the job to get status updates", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdJob := batchv1.Job{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
					g.Expect(createdJob.Status.StartTime).NotTo(gomega.BeNil())
					g.Expect(createdJob.Status.Active).To(gomega.Equal(int32(1)))
					g.Expect(createdJob.Status.CompletionTime).To(gomega.BeNil())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for the job to finish", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdLeaderWorkload)).To(gomega.Succeed())

					g.Expect(apimeta.FindStatusCondition(createdLeaderWorkload.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeComparableTo(&metav1.Condition{
						Type:   kueue.WorkloadFinished,
						Status: metav1.ConditionTrue,
						Reason: kueue.WorkloadFinishedReasonSucceeded,
					}, util.IgnoreConditionMessage, util.IgnoreConditionTimestampsAndObservedGeneration))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking no objects are left in the worker clusters and the job is completed", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					workerWl := &kueue.Workload{}
					g.Expect(k8sWorker1Client.Get(ctx, wlLookupKey, workerWl)).To(utiltesting.BeNotFoundError())
					g.Expect(k8sWorker2Client.Get(ctx, wlLookupKey, workerWl)).To(utiltesting.BeNotFoundError())
					workerJob := &batchv1.Job{}
					g.Expect(k8sWorker1Client.Get(ctx, client.ObjectKeyFromObject(job), workerJob)).To(utiltesting.BeNotFoundError())
					g.Expect(k8sWorker2Client.Get(ctx, client.ObjectKeyFromObject(job), workerJob)).To(utiltesting.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				createdJob := &batchv1.Job{}
				gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
				gomega.Expect(createdJob.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(
					batchv1.JobCondition{
						Type:   batchv1.JobComplete,
						Status: corev1.ConditionTrue,
					},
					cmpopts.IgnoreFields(batchv1.JobCondition{}, "LastTransitionTime", "LastProbeTime"))))
			})
		})
		ginkgo.It("Should run a jobSet on worker if admitted", func() {
			// Since it requires 2 CPU in total, this jobset can only be admitted in worker 1.
			jobSet := testingjobset.MakeJobSet("job-set", managerNs.Name).
				Queue(managerLq.Name).
				ReplicatedJobs(
					testingjobset.ReplicatedJobRequirements{
						Name:        "replicated-job-1",
						Replicas:    2,
						Parallelism: 2,
						Completions: 2,
						Image:       util.E2eTestSleepImage,
						// Give it the time to be observed Active in the live status update step.
						Args: []string{"5s"},
					},
				).
				Request("replicated-job-1", "cpu", "500m").
				Request("replicated-job-1", "memory", "200M").
				Obj()

			ginkgo.By("Creating the jobSet", func() {
				gomega.Expect(k8sManagerClient.Create(ctx, jobSet)).Should(gomega.Succeed())
			})

			createdLeaderWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: managerNs.Name}

			// the execution should be given to the worker
			ginkgo.By("Waiting to be admitted in worker1 and manager", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdLeaderWorkload)).To(gomega.Succeed())
					g.Expect(workload.FindAdmissionCheck(createdLeaderWorkload.Status.AdmissionChecks, multiKueueAc.Name)).To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
						Name:    multiKueueAc.Name,
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime")))
					g.Expect(apimeta.FindStatusCondition(createdLeaderWorkload.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeComparableTo(&metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionTrue,
						Reason:  "Admitted",
						Message: "The workload is admitted",
					}, util.IgnoreConditionTimestampsAndObservedGeneration))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for the jobSet to get status updates", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdJobset := &jobset.JobSet{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(jobSet), createdJobset)).To(gomega.Succeed())

					g.Expect(createdJobset.Status.ReplicatedJobsStatus).To(gomega.BeComparableTo([]jobset.ReplicatedJobStatus{
						{
							Name:   "replicated-job-1",
							Ready:  2,
							Active: 2,
						},
					}, cmpopts.IgnoreFields(jobset.ReplicatedJobStatus{}, "Succeeded", "Failed")))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for the jobSet to finish", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdLeaderWorkload)).To(gomega.Succeed())

					g.Expect(apimeta.FindStatusCondition(createdLeaderWorkload.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeComparableTo(&metav1.Condition{
						Type:    kueue.WorkloadFinished,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadFinishedReasonSucceeded,
						Message: "jobset completed successfully",
					}, util.IgnoreConditionTimestampsAndObservedGeneration))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking no objects are left in the worker clusters and the jobSet is completed", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					workerWl := &kueue.Workload{}
					g.Expect(k8sWorker1Client.Get(ctx, wlLookupKey, workerWl)).To(utiltesting.BeNotFoundError())
					g.Expect(k8sWorker2Client.Get(ctx, wlLookupKey, workerWl)).To(utiltesting.BeNotFoundError())
					workerJobSet := &jobset.JobSet{}
					g.Expect(k8sWorker1Client.Get(ctx, client.ObjectKeyFromObject(jobSet), workerJobSet)).To(utiltesting.BeNotFoundError())
					g.Expect(k8sWorker2Client.Get(ctx, client.ObjectKeyFromObject(jobSet), workerJobSet)).To(utiltesting.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				createdJobSet := &jobset.JobSet{}
				gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(jobSet), createdJobSet)).To(gomega.Succeed())
				gomega.Expect(ptr.Deref(createdJobSet.Spec.Suspend, true)).To(gomega.BeFalse())
				gomega.Expect(createdJobSet.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(
					metav1.Condition{
						Type:    string(jobset.JobSetCompleted),
						Status:  metav1.ConditionTrue,
						Reason:  "AllJobsCompleted",
						Message: "jobset completed successfully",
					},
					util.IgnoreConditionTimestampsAndObservedGeneration)))
			})
		})

		ginkgo.It("Should run a kubeflow PyTorchJob on worker if admitted", func() {
			// Since it requires 1600M of memory, this job can only be admitted in worker 2.
			pyTorchJob := testingpytorchjob.MakePyTorchJob("pytorchjob1", managerNs.Name).
				Queue(managerLq.Name).
				PyTorchReplicaSpecs(
					testingpytorchjob.PyTorchReplicaSpecRequirement{
						ReplicaType:   kftraining.PyTorchJobReplicaTypeMaster,
						ReplicaCount:  1,
						RestartPolicy: "Never",
					},
					testingpytorchjob.PyTorchReplicaSpecRequirement{
						ReplicaType:   kftraining.PyTorchJobReplicaTypeWorker,
						ReplicaCount:  1,
						RestartPolicy: "OnFailure",
					},
				).
				Request(kftraining.PyTorchJobReplicaTypeMaster, corev1.ResourceCPU, "0.2").
				Request(kftraining.PyTorchJobReplicaTypeMaster, corev1.ResourceMemory, "800M").
				Request(kftraining.PyTorchJobReplicaTypeWorker, corev1.ResourceCPU, "0.5").
				Request(kftraining.PyTorchJobReplicaTypeWorker, corev1.ResourceMemory, "800M").
				Image(kftraining.PyTorchJobReplicaTypeMaster, util.E2eTestSleepImage, []string{"1ms"}).
				Image(kftraining.PyTorchJobReplicaTypeWorker, util.E2eTestSleepImage, []string{"1ms"}).
				Obj()

			ginkgo.By("Creating the PyTorchJob", func() {
				gomega.Expect(k8sManagerClient.Create(ctx, pyTorchJob)).Should(gomega.Succeed())
			})

			wlLookupKey := types.NamespacedName{Name: workloadpytorchjob.GetWorkloadNameForPyTorchJob(pyTorchJob.Name, pyTorchJob.UID), Namespace: managerNs.Name}

			// the execution should be given to the worker 2
			waitForJobAdmitted(wlLookupKey, multiKueueAc.Name, "worker2")

			ginkgo.By("Waiting for the PyTorchJob to finish", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdPyTorchJob := &kftraining.PyTorchJob{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(pyTorchJob), createdPyTorchJob)).To(gomega.Succeed())
					g.Expect(createdPyTorchJob.Status.ReplicaStatuses[kftraining.PyTorchJobReplicaTypeMaster]).To(gomega.BeComparableTo(
						&kftraining.ReplicaStatus{
							Active:    0,
							Succeeded: 1,
							Selector:  fmt.Sprintf("training.kubeflow.org/job-name=%s,training.kubeflow.org/operator-name=pytorchjob-controller,training.kubeflow.org/replica-type=master", createdPyTorchJob.Name),
						},
					))

					finishReasonMessage := fmt.Sprintf("PyTorchJob %s is successfully completed.", pyTorchJob.Name)
					checkFinishStatusCondition(g, wlLookupKey, finishReasonMessage)
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking no objects are left in the worker clusters and the PyTorchJob is completed", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					workerWl := &kueue.Workload{}
					g.Expect(k8sWorker1Client.Get(ctx, wlLookupKey, workerWl)).To(utiltesting.BeNotFoundError())
					g.Expect(k8sWorker2Client.Get(ctx, wlLookupKey, workerWl)).To(utiltesting.BeNotFoundError())
					workerPyTorchJob := &kftraining.PyTorchJob{}
					g.Expect(k8sWorker1Client.Get(ctx, client.ObjectKeyFromObject(pyTorchJob), workerPyTorchJob)).To(utiltesting.BeNotFoundError())
					g.Expect(k8sWorker2Client.Get(ctx, client.ObjectKeyFromObject(pyTorchJob), workerPyTorchJob)).To(utiltesting.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should run a MPIJob on worker if admitted", func() {
			// Since it requires 1.5 CPU, this job can only be admitted in worker 1.
			mpijob := testingmpijob.MakeMPIJob("mpijob1", managerNs.Name).
				Queue(managerLq.Name).
				ManagedBy(kueue.MultiKueueControllerName).
				MPIJobReplicaSpecs(
					testingmpijob.MPIJobReplicaSpecRequirement{
						ReplicaType:   kfmpi.MPIReplicaTypeLauncher,
						ReplicaCount:  1,
						RestartPolicy: "OnFailure",
					},
					testingmpijob.MPIJobReplicaSpecRequirement{
						ReplicaType:   kfmpi.MPIReplicaTypeWorker,
						ReplicaCount:  1,
						RestartPolicy: "OnFailure",
					},
				).
				Request(kfmpi.MPIReplicaTypeLauncher, corev1.ResourceCPU, "1").
				Request(kfmpi.MPIReplicaTypeLauncher, corev1.ResourceMemory, "200M").
				Request(kfmpi.MPIReplicaTypeWorker, corev1.ResourceCPU, "0.5").
				Request(kfmpi.MPIReplicaTypeWorker, corev1.ResourceMemory, "100M").
				Image(kfmpi.MPIReplicaTypeLauncher, util.E2eTestSleepImage, []string{"1ms"}).
				Image(kfmpi.MPIReplicaTypeWorker, util.E2eTestSleepImage, []string{"1ms"}).
				Obj()

			ginkgo.By("Creating the MPIJob", func() {
				gomega.Expect(k8sManagerClient.Create(ctx, mpijob)).Should(gomega.Succeed())
			})

			wlLookupKey := types.NamespacedName{Name: workloadmpijob.GetWorkloadNameForMPIJob(mpijob.Name, mpijob.UID), Namespace: managerNs.Name}

			// the execution should be given to the worker1
			waitForJobAdmitted(wlLookupKey, multiKueueAc.Name, "worker1")

			ginkgo.By("Waiting for the MPIJob to finish", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdMPIJob := &kfmpi.MPIJob{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(mpijob), createdMPIJob)).To(gomega.Succeed())
					g.Expect(createdMPIJob.Status.ReplicaStatuses[kfmpi.MPIReplicaTypeLauncher]).To(gomega.BeComparableTo(
						&kfmpi.ReplicaStatus{
							Active:    0,
							Succeeded: 1,
						},
					))

					finishReasonMessage := fmt.Sprintf("MPIJob %s successfully completed.", client.ObjectKeyFromObject(mpijob).String())
					checkFinishStatusCondition(g, wlLookupKey, finishReasonMessage)
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking no objects are left in the worker clusters and the MPIJob is completed", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					workerWl := &kueue.Workload{}
					g.Expect(k8sWorker1Client.Get(ctx, wlLookupKey, workerWl)).To(utiltesting.BeNotFoundError())
					g.Expect(k8sWorker2Client.Get(ctx, wlLookupKey, workerWl)).To(utiltesting.BeNotFoundError())
					workerMPIJob := &kfmpi.MPIJob{}
					g.Expect(k8sWorker1Client.Get(ctx, client.ObjectKeyFromObject(mpijob), workerMPIJob)).To(utiltesting.BeNotFoundError())
					g.Expect(k8sWorker2Client.Get(ctx, client.ObjectKeyFromObject(mpijob), workerMPIJob)).To(utiltesting.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
	ginkgo.When("The connection to a worker cluster is unreliable", func() {
		ginkgo.It("Should update the cluster status to reflect the connection state", func() {
			worker1Cq2 := utiltesting.MakeClusterQueue("q2").
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas(worker1Flavor.Name).
						Resource(corev1.ResourceCPU, "2").
						Resource(corev1.ResourceMemory, "1G").
						Obj(),
				).
				Obj()
			gomega.Expect(k8sWorker1Client.Create(ctx, worker1Cq2)).Should(gomega.Succeed())

			worker1Container := fmt.Sprintf("%s-control-plane", worker1ClusterName)
			worker1ClusterKey := client.ObjectKeyFromObject(workerCluster1)

			ginkgo.By("Disconnecting worker1 container from the kind network", func() {
				cmd := exec.Command("docker", "network", "disconnect", "kind", worker1Container)
				output, err := cmd.CombinedOutput()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)

				podList := &corev1.PodList{}
				podListOptions := client.InNamespace("kueue-system")
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.List(ctx, podList, podListOptions)).Should(gomega.Succeed())
				}, util.LongTimeout, util.Interval).ShouldNot(gomega.Succeed())
			})

			ginkgo.By("Waiting for the cluster to become inactive", func() {
				readClient := &kueue.MultiKueueCluster{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, worker1ClusterKey, readClient)).To(gomega.Succeed())
					g.Expect(readClient.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(
						metav1.Condition{
							Type:   kueue.MultiKueueClusterActive,
							Status: metav1.ConditionFalse,
							Reason: "ClientConnectionFailed",
						},
						util.IgnoreConditionTimestampsAndObservedGeneration, util.IgnoreConditionMessage)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Reconnecting worker1 container to the kind network", func() {
				cmd := exec.Command("docker", "network", "connect", "kind", worker1Container)
				output, err := cmd.CombinedOutput()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(util.DeleteObject(ctx, k8sWorker1Client, worker1Cq2)).Should(gomega.Succeed())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

				// After reconnecting the container to the network, when we try to get pods,
				// we get it with the previous values (as before disconnect). Therefore, it
				// takes some time for the cluster to restore them, and we got actually values.
				// To be sure that the leader of kueue-control-manager successfully recovered
				// we can check it by removing already created Cluster Queue.
				var cq kueue.ClusterQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, client.ObjectKeyFromObject(worker1Cq2), &cq)).Should(utiltesting.BeNotFoundError())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for the cluster do become active", func() {
				readClient := &kueue.MultiKueueCluster{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, worker1ClusterKey, readClient)).To(gomega.Succeed())
					g.Expect(readClient.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(
						metav1.Condition{
							Type:    kueue.MultiKueueClusterActive,
							Status:  metav1.ConditionTrue,
							Reason:  "Active",
							Message: "Connected",
						},
						util.IgnoreConditionTimestampsAndObservedGeneration)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})

func waitForJobAdmitted(wlLookupKey types.NamespacedName, acName, workerName string) {
	ginkgo.By(fmt.Sprintf("Waiting to be admitted in %s and manager", workerName), func() {
		gomega.Eventually(func(g gomega.Gomega) {
			createdWorkload := &kueue.Workload{}
			g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeComparableTo(&metav1.Condition{
				Type:    kueue.WorkloadAdmitted,
				Status:  metav1.ConditionTrue,
				Reason:  "Admitted",
				Message: "The workload is admitted",
			}, util.IgnoreConditionTimestampsAndObservedGeneration))
			g.Expect(workload.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, acName)).To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
				Name:    acName,
				State:   kueue.CheckStateReady,
				Message: fmt.Sprintf(`The workload got reservation on "%s"`, workerName),
			}, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime")))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
}

func checkFinishStatusCondition(g gomega.Gomega, wlLookupKey types.NamespacedName, finishReasonMessage string) {
	createdWorkload := &kueue.Workload{}
	g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
	g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeComparableTo(&metav1.Condition{
		Type:    kueue.WorkloadFinished,
		Status:  metav1.ConditionTrue,
		Reason:  kueue.WorkloadFinishedReasonSucceeded,
		Message: finishReasonMessage,
	}, util.IgnoreConditionTimestampsAndObservedGeneration))
}
