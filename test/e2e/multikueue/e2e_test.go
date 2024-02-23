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
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/multikueue"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	workloadjobset "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

// +kubebuilder:docs-gen:collapse=Imports

var _ = ginkgo.Describe("MultiKueue", func() {
	var (
		managerNs *corev1.Namespace
		worker1Ns *corev1.Namespace
		worker2Ns *corev1.Namespace

		workerCluster1   *kueuealpha.MultiKueueCluster
		workerCluster2   *kueuealpha.MultiKueueCluster
		multiKueueConfig *kueuealpha.MultiKueueConfig
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

		workerCluster1 = utiltesting.MakeMultiKueueCluster("worker1").KubeConfig(kueuealpha.SecretLocationType, "multikueue1").Obj()
		gomega.Expect(k8sManagerClient.Create(ctx, workerCluster1)).To(gomega.Succeed())

		workerCluster2 = utiltesting.MakeMultiKueueCluster("worker2").KubeConfig(kueuealpha.SecretLocationType, "multikueue2").Obj()
		gomega.Expect(k8sManagerClient.Create(ctx, workerCluster2)).To(gomega.Succeed())

		multiKueueConfig = utiltesting.MakeMultiKueueConfig("multikueueconfig").Clusters("worker1", "worker2").Obj()
		gomega.Expect(k8sManagerClient.Create(ctx, multiKueueConfig)).Should(gomega.Succeed())

		multiKueueAc = utiltesting.MakeAdmissionCheck("ac1").
			ControllerName(multikueue.ControllerName).
			Parameters(kueuealpha.GroupVersion.Group, "MultiKueueConfig", multiKueueConfig.Name).
			Obj()
		gomega.Expect(k8sManagerClient.Create(ctx, multiKueueAc)).Should(gomega.Succeed())

		ginkgo.By("wait for check active", func() {
			updatetedAc := kueue.AdmissionCheck{}
			acKey := client.ObjectKeyFromObject(multiKueueAc)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sManagerClient.Get(ctx, acKey, &updatetedAc)).To(gomega.Succeed())
				g.Expect(apimeta.IsStatusConditionTrue(updatetedAc.Status.Conditions, kueue.AdmissionCheckActive)).To(gomega.BeTrue())
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

		util.ExpectClusterQueueToBeDeleted(ctx, k8sWorker1Client, worker1Cq, true)
		util.ExpectResourceFlavorToBeDeleted(ctx, k8sWorker1Client, worker1Flavor, true)

		util.ExpectClusterQueueToBeDeleted(ctx, k8sWorker2Client, worker2Cq, true)
		util.ExpectResourceFlavorToBeDeleted(ctx, k8sWorker2Client, worker2Flavor, true)

		util.ExpectClusterQueueToBeDeleted(ctx, k8sManagerClient, managerCq, true)
		util.ExpectResourceFlavorToBeDeleted(ctx, k8sManagerClient, managerFlavor, true)
		util.ExpectAdmissionCheckToBeDeleted(ctx, k8sManagerClient, multiKueueAc, true)
		gomega.Expect(k8sManagerClient.Delete(ctx, multiKueueConfig)).To(gomega.Succeed())
		gomega.Expect(k8sManagerClient.Delete(ctx, workerCluster1)).To(gomega.Succeed())
		gomega.Expect(k8sManagerClient.Delete(ctx, workerCluster2)).To(gomega.Succeed())
	})

	ginkgo.When("Creating a multikueue admission check", func() {
		ginkgo.It("Should run a job on worker if admitted", func() {
			// Since it requires 2 CPU, this job can only be admitted in worker 1.
			job := testingjob.MakeJob("job", managerNs.Name).
				Queue(managerLq.Name).
				Request("cpu", "2").
				Request("memory", "1G").
				Image("gcr.io/k8s-staging-perf-tests/sleep:v0.1.0", []string{"1ms"}).
				Obj()

			ginkgo.By("Creating the job", func() {
				gomega.Expect(k8sManagerClient.Create(ctx, job)).Should(gomega.Succeed())
			})

			createdLeaderWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

			// the execution should be given to the worker
			ginkgo.By("Waiting to be admitted in worker1", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdLeaderWorkload)).To(gomega.Succeed())
					g.Expect(workload.FindAdmissionCheck(createdLeaderWorkload.Status.AdmissionChecks, multiKueueAc.Name)).To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
						Name:    multiKueueAc.Name,
						State:   kueue.CheckStatePending,
						Message: `The workload got reservation on "worker1"`,
					}, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime")))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for the job to finish", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdLeaderWorkload)).To(gomega.Succeed())

					g.Expect(apimeta.FindStatusCondition(createdLeaderWorkload.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeComparableTo(&metav1.Condition{
						Type:    kueue.WorkloadFinished,
						Status:  metav1.ConditionTrue,
						Reason:  "JobFinished",
						Message: `Job finished successfully`,
					}, cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")))
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
				gomega.Expect(ptr.Deref(createdJob.Spec.Suspend, false)).To(gomega.BeTrue())
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
						Image:       "gcr.io/k8s-staging-perf-tests/sleep:v0.1.0",
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
					}, cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")))
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
						Reason:  "JobSetFinished",
						Message: "JobSet finished successfully",
					}, cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")))
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
					cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"))))
			})
		})
	})
})
