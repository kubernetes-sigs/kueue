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
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("MultiKueue ElasticJob", ginkgo.Label("area:multikueue", "feature:multikueue"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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

	ginkgo.It("Should run an ElasticJob on worker if admitted", func() {
		manager := managerTestCluster
		worker1 := worker1TestCluster
		worker2 := worker2TestCluster

		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)

		jobGVK := batchv1.SchemeGroupVersion.WithKind("Job")

		getJob := func(ctx context.Context, clnt client.Client, job *batchv1.Job) {
			ginkgo.GinkgoHelper()
			gomega.Expect(clnt.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
		}
		getWorkloadKey := func(job *batchv1.Job) types.NamespacedName {
			ginkgo.GinkgoHelper()
			getJob(manager.ctx, manager.client, job)
			return types.NamespacedName{Name: jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(job.Name, job.UID, jobGVK, job.GetGeneration()), Namespace: job.Namespace}
		}
		getWorkload := func(g gomega.Gomega, ctx context.Context, clnt client.Client, key types.NamespacedName) *kueue.Workload {
			ginkgo.GinkgoHelper()
			workload := &kueue.Workload{}
			g.Expect(clnt.Get(ctx, key, workload)).To(gomega.Succeed())
			return workload
		}

		job := testingjob.MakeJob("job", f.managerNs.Name).
			Parallelism(1).
			Completions(2).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Queue(kueue.LocalQueueName(f.managerLq.Name)).
			Obj()
		util.MustCreate(manager.ctx, manager.client, job)

		ginkgo.By("observe: the job is created in the manager cluster", func() {
			getJob(manager.ctx, manager.client, job)
			gomega.Expect(job.Spec.Suspend).To(gomega.Equal(new(true)))
		})

		ginkgo.By("observe: a new workload is created in the manager cluster")
		workloadKey := getWorkloadKey(job)
		gomega.Eventually(func(g gomega.Gomega) {
			getWorkload(g, manager.ctx, manager.client, workloadKey)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("admit workload on the manager cluster")
		util.SetQuotaReservation(manager.ctx, manager.client, workloadKey,
			utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).Obj())

		ginkgo.By("observe: workload is created on all worker clusters", func() {
			localWorkload := getWorkload(gomega.Default, manager.ctx, manager.client, workloadKey)
			gomega.Eventually(func(g gomega.Gomega) {
				workload := getWorkload(g, worker1.ctx, worker1.client, workloadKey)
				g.Expect(workload.Spec).To(gomega.BeComparableTo(localWorkload.Spec))
				workload = getWorkload(g, worker2.ctx, worker2.client, workloadKey)
				g.Expect(workload.Spec).To(gomega.BeComparableTo(localWorkload.Spec))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("admit the workload on the worker1 cluster")
		util.SetQuotaReservation(worker1.ctx, worker1.client, workloadKey,
			utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).Obj())

		ginkgo.By("observe: the local workload admission check and local events reflect reservation on the worker1 cluster")
		util.ExpectAdmissionCheckStateWithMessage(
			manager.ctx, manager.client, workloadKey,
			f.multiKueueAC.Name,
			kueue.CheckStateReady,
			`The workload was admitted on "worker1"`,
		)
		util.ExpectEventAppeared(manager.ctx, manager.client, eventsv1.Event{
			Reason: "MultiKueue",
			Type:   corev1.EventTypeNormal,
			Note:   `The workload was admitted on "worker1"`,
		})

		ginkgo.By("observe: job is synced to the worker1 cluster and is active", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				remoteJob := job.DeepCopy()
				getJob(worker1.ctx, worker1.client, remoteJob)
				g.Expect(remoteJob.Spec.Suspend).To(gomega.Equal(new(false)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("observe: the workload is removed from the worker2 cluster")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(worker2.client.Get(worker2.ctx, workloadKey, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("observe: there are no jobs in the worker2 cluster", func() {
			list := &batchv1.JobList{}
			gomega.Expect(worker2.client.List(worker2.ctx, list, client.InNamespace(job.Namespace))).To(gomega.Succeed())
			gomega.Expect(list.Items).To(gomega.BeEmpty())
		})

		ginkgo.By("observe: job is no longer suspended in the manager cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				getJob(manager.ctx, manager.client, job)
				g.Expect(job.Spec.Suspend).To(gomega.Equal(new(false)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		/*
			Scale-up Section
		*/

		ginkgo.By("scale-up the job", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				getJob(manager.ctx, manager.client, job)
				job.Spec.Parallelism = new(int32(2))
				g.Expect(manager.client.Update(manager.ctx, job)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("observe: a new workload slice is created")
		newWorkloadKey := getWorkloadKey(job)
		gomega.Eventually(func(g gomega.Gomega) {
			getWorkload(g, manager.ctx, manager.client, newWorkloadKey)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("copy clusterName from the old workload to the new workload", func() {
			oldWorkload := getWorkload(gomega.Default, manager.ctx, manager.client, workloadKey)
			newWorkload := getWorkload(gomega.Default, manager.ctx, manager.client, newWorkloadKey)
			// This step is done by the scheduler during the new slice admission and the old slice replacement.
			// Since we are not "running" scheduler for this test suit, we need to "emulate" this step.
			newWorkload.Status.ClusterName = oldWorkload.Status.ClusterName
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(manager.client.Status().Update(manager.ctx, newWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			newWorkload = getWorkload(gomega.Default, manager.ctx, manager.client, newWorkloadKey)
			gomega.Expect(newWorkload.Status.ClusterName).Should(gomega.BeEquivalentTo(oldWorkload.Status.ClusterName))
		})

		ginkgo.By("admit the new workload and finish the old workload in the manager cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				oldWorkload := getWorkload(g, manager.ctx, manager.client, workloadKey)
				g.Expect(workloadfinish.Finish(manager.ctx, manager.client, oldWorkload, kueue.WorkloadSliceReplaced, "Replaced to accommodate a new slice", util.RealClock)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.SetQuotaReservation(manager.ctx, manager.client, newWorkloadKey, utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Flavor(corev1.ResourceCPU, multikueueTestFlavor).Count(2).Obj()).Obj())
		})

		ginkgo.By("observe: the new workload is created in the worker1 cluster")
		gomega.Eventually(func(g gomega.Gomega) {
			local := getWorkload(g, manager.ctx, manager.client, newWorkloadKey)
			remote := getWorkload(g, worker1.ctx, worker1.client, newWorkloadKey)
			g.Expect(remote.Spec).To(gomega.BeComparableTo(local.Spec))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("observe: there are no workloads or jobs in the worker2 cluster", func() {
			workloads := &kueue.WorkloadList{}
			gomega.Expect(worker2.client.List(worker2.ctx, workloads, client.InNamespace(job.Namespace))).To(gomega.Succeed())
			gomega.Expect(workloads.Items).To(gomega.BeEmpty())
			jobs := &batchv1.JobList{}
			gomega.Expect(worker2.client.List(worker2.ctx, jobs, client.InNamespace(job.Namespace))).To(gomega.Succeed())
			gomega.Expect(jobs.Items).To(gomega.BeEmpty())
		})

		ginkgo.By("observe: the old workload is still admitted in the worker1 cluster", func() {
			workload := getWorkload(gomega.Default, worker1.ctx, worker1.client, workloadKey)
			util.ExpectWorkloadsToBeAdmitted(worker1.ctx, worker1.client, workload)
		})

		ginkgo.By("observe: the remote job is still active and has old parallelism count", func() {
			remoteJob := job.DeepCopy()
			getJob(worker1.ctx, worker1.client, remoteJob)
			gomega.Expect(remoteJob.Spec.Suspend).To(gomega.Equal(new(false)))
			gomega.Expect(remoteJob.Spec.Parallelism).To(gomega.BeEquivalentTo(new(int32(1))))
		})

		ginkgo.By("admit the new workload replacing the old workload in the worker1 cluster", func() {
			util.SetQuotaReservation(worker1.ctx, worker1.client, newWorkloadKey, utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Flavor(corev1.ResourceCPU, multikueueTestFlavor).Count(2).Obj()).Obj())
			gomega.Eventually(func(g gomega.Gomega) {
				wl := getWorkload(g, worker1.ctx, worker1.client, workloadKey)
				g.Expect(workloadfinish.Finish(worker1.ctx, worker1.client, wl, kueue.WorkloadSliceReplaced, "Replaced to accommodate a new slice", util.RealClock)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("observe: the new local workload admission check and local events reflect reservation in the worker1 cluster")
		util.ExpectAdmissionCheckStateWithMessage(
			manager.ctx, manager.client, newWorkloadKey,
			f.multiKueueAC.Name,
			kueue.CheckStateReady,
			`The workload was admitted on "worker1"`,
		)
		util.ExpectEventAppeared(manager.ctx, manager.client, eventsv1.Event{
			Reason: "MultiKueue",
			Type:   corev1.EventTypeNormal,
			Note:   `The workload was admitted on "worker1"`,
		})

		ginkgo.By("observe: job changes are synced to the worker1 cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				remoteJob := job.DeepCopy()
				getJob(worker1.ctx, worker1.client, remoteJob)
				g.Expect(remoteJob.Spec.Suspend).To(gomega.Equal(new(false)))
				g.Expect(remoteJob.Spec.Parallelism).To(gomega.BeEquivalentTo(new(int32(2))))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		/*
			Scale-down Section.
			Note: Scaling down does not create a new workload slice, so we continue using the previously generated `newWorkloadKey`.
		*/
		ginkgo.By("scale-down the job", func() {
			getJob(manager.ctx, manager.client, job)
			job.Spec.Parallelism = new(int32(1))
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(manager.client.Update(manager.ctx, job)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
		ginkgo.By("observe: workload changed in the manager cluster", func() {
			getJob(manager.ctx, manager.client, job)
			gomega.Eventually(func(g gomega.Gomega) {
				workload := getWorkload(g, manager.ctx, manager.client, newWorkloadKey)
				g.Expect(workload.Spec.PodSets[0].Count).To(gomega.BeEquivalentTo(int32(1)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
		ginkgo.By("observe: there are no new workloads created in response to scale-down even in the manager cluster", func() {
			list := &kueue.WorkloadList{}
			gomega.Expect(manager.client.List(manager.ctx, list, client.InNamespace(job.Namespace))).To(gomega.Succeed())
			gomega.Expect(list.Items).To(gomega.HaveLen(2))
		})
		ginkgo.By("observe: job changed in the worker1 cluster", func() {
			remoteJob := job.DeepCopy()
			gomega.Eventually(func(g gomega.Gomega) {
				getJob(worker1.ctx, worker1.client, remoteJob)
				g.Expect(remoteJob.Spec.Parallelism).To(gomega.BeEquivalentTo(new(int32(1))))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
		ginkgo.By("observe: there are no new workloads created in response to scale-down even in the worker1 cluster", func() {
			list := &kueue.WorkloadList{}
			gomega.Expect(worker1.client.List(worker1.ctx, list, client.InNamespace(job.Namespace))).To(gomega.Succeed())
			gomega.Expect(list.Items).To(gomega.HaveLen(2))
		})
		ginkgo.By("observe: there are still no workloads or jobs in the worker2 cluster", func() {
			workloads := &kueue.WorkloadList{}
			gomega.Expect(worker2.client.List(worker2.ctx, workloads, client.InNamespace(job.Namespace))).To(gomega.Succeed())
			gomega.Expect(workloads.Items).To(gomega.BeEmpty())
			jobs := &batchv1.JobList{}
			gomega.Expect(worker2.client.List(worker2.ctx, jobs, client.InNamespace(job.Namespace))).To(gomega.Succeed())
			gomega.Expect(jobs.Items).To(gomega.BeEmpty())
		})

		/*
			Finish Job Section.
		*/
		ginkgo.By("finishing the job in the worker1 cluster", func() {
			now := metav1.Now()
			completedJobCondition := batchv1.JobCondition{
				Type:               batchv1.JobComplete,
				Status:             corev1.ConditionTrue,
				LastProbeTime:      now,
				LastTransitionTime: now,
				Message:            "Job finished successfully",
			}

			gomega.Eventually(func(g gomega.Gomega) {
				remoteJob := job.DeepCopy()
				getJob(worker1.ctx, worker1.client, remoteJob)
				remoteJob.Status.Conditions = append(remoteJob.Status.Conditions,
					completedJobCondition,
					batchv1.JobCondition{
						Type:               batchv1.JobSuccessCriteriaMet,
						Status:             corev1.ConditionTrue,
						LastProbeTime:      now,
						LastTransitionTime: now,
						Message:            "Reached expected number of succeeded pods",
					})
				remoteJob.Status.Succeeded = 1
				remoteJob.Status.StartTime = new(now)
				remoteJob.Status.CompletionTime = new(now)
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, remoteJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(newWorkloadKey, completedJobCondition.Message)

			getJob(manager.ctx, manager.client, job)
			gomega.Expect(job.Status.Conditions).Should(gomega.ContainElement(gomega.WithTransform(func(condition batchv1.JobCondition) batchv1.JobCondition {
				condition.LastProbeTime = now
				condition.LastTransitionTime = now
				return condition
			}, gomega.Equal(completedJobCondition))))
		})
	})

	ginkgo.It("Should keep a replaced elastic-job slice's worker objects when normalizeActiveSlices finishes it", func() {
		manager := managerTestCluster
		worker1 := worker1TestCluster

		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)

		jobGVK := batchv1.SchemeGroupVersion.WithKind("Job")

		job := testingjob.MakeJob("job", f.managerNs.Name).
			Parallelism(1).
			Completions(2).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Queue(kueue.LocalQueueName(f.managerLq.Name)).
			Obj()
		util.MustCreate(manager.ctx, manager.client, job)

		// sliceKey refreshes the job and returns the current slice's workload key.
		// Elastic slice names embed the job generation, so scaling up yields a new key.
		sliceKey := func() types.NamespacedName {
			ginkgo.GinkgoHelper()
			gomega.Expect(manager.client.Get(manager.ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
			return types.NamespacedName{Name: jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(job.Name, job.UID, jobGVK, job.GetGeneration()), Namespace: job.Namespace}
		}

		ginkgo.By("observe: the old workload slice is created in the manager cluster")
		oldWorkloadKey := sliceKey()
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(manager.client.Get(manager.ctx, oldWorkloadKey, &kueue.Workload{})).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// This suite does not run a scheduler, so the steps a scheduler would perform
		// (reserving quota to admit a slice) are emulated with SetQuotaReservation.
		ginkgo.By("emulate the scheduler reserving quota for the old slice on the manager cluster", func() {
			util.SetQuotaReservation(manager.ctx, manager.client, oldWorkloadKey,
				utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).Obj())
		})

		ginkgo.By("emulate the scheduler reserving quota for the old slice on the worker1 cluster, and observe it is dispatched there", func() {
			util.SetQuotaReservation(worker1.ctx, worker1.client, oldWorkloadKey,
				utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).Obj())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1.client.Get(worker1.ctx, oldWorkloadKey, &kueue.Workload{})).To(gomega.Succeed())
				g.Expect(worker1.client.Get(worker1.ctx, client.ObjectKeyFromObject(job), &batchv1.Job{})).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("scale-up the job so a replacement slice is created", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(manager.client.Get(manager.ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
				job.Spec.Parallelism = new(int32(2))
				g.Expect(manager.client.Update(manager.ctx, job)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("observe: the replacement slice is created in the manager cluster")
		newWorkloadKey := sliceKey()
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(manager.client.Get(manager.ctx, newWorkloadKey, &kueue.Workload{})).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("observe: both the old and the replacement slice exist in the manager cluster", func() {
			list := &kueue.WorkloadList{}
			gomega.Expect(manager.client.List(manager.ctx, list, client.InNamespace(job.Namespace))).To(gomega.Succeed())
			gomega.Expect(list.Items).To(gomega.HaveLen(2))
		})

		ginkgo.By("emulate the scheduler admitting the replacement slice (clusterName + quota reservation), but leave the old slice for normalizeActiveSlices to finish", func() {
			oldWorkload := &kueue.Workload{}
			gomega.Expect(manager.client.Get(manager.ctx, oldWorkloadKey, oldWorkload)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				newWorkload := &kueue.Workload{}
				g.Expect(manager.client.Get(manager.ctx, newWorkloadKey, newWorkload)).To(gomega.Succeed())
				newWorkload.Status.ClusterName = oldWorkload.Status.ClusterName
				g.Expect(manager.client.Status().Update(manager.ctx, newWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.SetQuotaReservation(manager.ctx, manager.client, newWorkloadKey,
				utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).Obj())
		})

		ginkgo.By("observe: normalizeActiveSlices finishes the old slice with reason WorkloadSliceReplaced (not OutOfSync)", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				oldWorkload := &kueue.Workload{}
				g.Expect(manager.client.Get(manager.ctx, oldWorkloadKey, oldWorkload)).To(gomega.Succeed())
				finished := apimeta.FindStatusCondition(oldWorkload.Status.Conditions, kueue.WorkloadFinished)
				g.Expect(finished).NotTo(gomega.BeNil())
				g.Expect(finished.Status).To(gomega.Equal(metav1.ConditionTrue))
				g.Expect(finished.Reason).To(gomega.Equal(kueue.WorkloadSliceReplaced))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("observe: the replaced slice's worker1 objects are kept during the handover (not deleted by MultiKueue)", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(worker1.client.Get(worker1.ctx, oldWorkloadKey, &kueue.Workload{})).To(gomega.Succeed())
				remoteJob := &batchv1.Job{}
				g.Expect(worker1.client.Get(worker1.ctx, client.ObjectKeyFromObject(job), remoteJob)).To(gomega.Succeed())
			}, util.ShortConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should scale an elastic RayCluster on the worker if admitted", func() {
		manager := managerTestCluster
		worker1 := worker1TestCluster
		worker2 := worker2TestCluster

		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)

		rayGVK := rayv1.GroupVersion.WithKind("RayCluster")
		headPodSet := utiltestingapi.MakePodSetAssignment("head").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()
		admission := func(workerCount int32) *utiltestingapi.AdmissionWrapper {
			return utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
				PodSets(
					headPodSet,
					utiltestingapi.MakePodSetAssignment("workers-group-0").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Count(workerCount).Obj(),
				)
		}

		getRayCluster := func(ctx context.Context, clnt client.Client, rc *rayv1.RayCluster) {
			ginkgo.GinkgoHelper()
			gomega.Expect(clnt.Get(ctx, client.ObjectKeyFromObject(rc), rc)).To(gomega.Succeed())
		}
		getWorkloadKey := func(rc *rayv1.RayCluster) types.NamespacedName {
			ginkgo.GinkgoHelper()
			getRayCluster(manager.ctx, manager.client, rc)
			return types.NamespacedName{Name: jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(rc.Name, rc.UID, rayGVK, rc.GetGeneration()), Namespace: rc.Namespace}
		}
		getWorkload := func(g gomega.Gomega, ctx context.Context, clnt client.Client, key types.NamespacedName) *kueue.Workload {
			ginkgo.GinkgoHelper()
			wl := &kueue.Workload{}
			g.Expect(clnt.Get(ctx, key, wl)).To(gomega.Succeed())
			return wl
		}

		raycluster := testingraycluster.MakeCluster("raycluster1", f.managerNs.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Queue(f.managerLq.Name).
			ScaleFirstWorkerGroup(1).
			Obj()
		util.MustCreate(manager.ctx, manager.client, raycluster)

		ginkgo.By("observe: the elastic RayCluster is created suspended in the manager cluster", func() {
			getRayCluster(manager.ctx, manager.client, raycluster)
			gomega.Expect(raycluster.Spec.Suspend).To(gomega.Equal(new(true)))
		})

		ginkgo.By("observe: a workload is created in the manager cluster")
		workloadKey := getWorkloadKey(raycluster)
		gomega.Eventually(func(g gomega.Gomega) {
			getWorkload(g, manager.ctx, manager.client, workloadKey)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("admit the workload on the manager cluster")
		util.SetQuotaReservation(manager.ctx, manager.client, workloadKey, admission(1).Obj())

		ginkgo.By("observe: the workload is created on all worker clusters", func() {
			localWorkload := getWorkload(gomega.Default, manager.ctx, manager.client, workloadKey)
			gomega.Eventually(func(g gomega.Gomega) {
				wl := getWorkload(g, worker1.ctx, worker1.client, workloadKey)
				g.Expect(wl.Spec).To(gomega.BeComparableTo(localWorkload.Spec))
				wl = getWorkload(g, worker2.ctx, worker2.client, workloadKey)
				g.Expect(wl.Spec).To(gomega.BeComparableTo(localWorkload.Spec))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("admit the workload on the worker1 cluster")
		util.SetQuotaReservation(worker1.ctx, worker1.client, workloadKey, admission(1).Obj())

		ginkgo.By("observe: the local admission check reflects the admission on the worker1 cluster")
		util.ExpectAdmissionCheckStateWithMessage(
			manager.ctx, manager.client, workloadKey,
			f.multiKueueAC.Name,
			kueue.CheckStateReady,
			`The workload was admitted on "worker1"`,
		)

		ginkgo.By("observe: the RayCluster is synced to the worker1 cluster and is not suspended", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				remote := raycluster.DeepCopy()
				getRayCluster(worker1.ctx, worker1.client, remote)
				g.Expect(remote.Spec.Suspend).To(gomega.Equal(new(false)))
				g.Expect(remote.Spec.WorkerGroupSpecs[0].Replicas).To(gomega.BeEquivalentTo(new(int32(1))))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("observe: the workload is removed from the worker2 cluster")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(worker2.client.Get(worker2.ctx, workloadKey, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		/*
			Scale-up Section.
		*/
		ginkgo.By("scale-up the RayCluster's first worker group to 3", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				getRayCluster(manager.ctx, manager.client, raycluster)
				raycluster.Spec.WorkerGroupSpecs[0].Replicas = new(int32(3))
				g.Expect(manager.client.Update(manager.ctx, raycluster)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("observe: a new workload slice is created in the manager cluster")
		newWorkloadKey := getWorkloadKey(raycluster)
		gomega.Eventually(func(g gomega.Gomega) {
			getWorkload(g, manager.ctx, manager.client, newWorkloadKey)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("emulate the scheduler: copy clusterName from the old slice to the new slice", func() {
			oldWorkload := getWorkload(gomega.Default, manager.ctx, manager.client, workloadKey)
			gomega.Eventually(func(g gomega.Gomega) {
				newWorkload := getWorkload(g, manager.ctx, manager.client, newWorkloadKey)
				newWorkload.Status.ClusterName = oldWorkload.Status.ClusterName
				g.Expect(manager.client.Status().Update(manager.ctx, newWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("emulate the scheduler: admit the new slice and finish the old slice in the manager cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				oldWorkload := getWorkload(g, manager.ctx, manager.client, workloadKey)
				g.Expect(workloadfinish.Finish(manager.ctx, manager.client, oldWorkload, kueue.WorkloadSliceReplaced, "Replaced to accommodate a new slice", util.RealClock)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.SetQuotaReservation(manager.ctx, manager.client, newWorkloadKey, admission(3).Obj())
		})

		ginkgo.By("emulate the scheduler: admit the new slice and finish the old slice in the worker1 cluster", func() {
			util.SetQuotaReservation(worker1.ctx, worker1.client, newWorkloadKey, admission(3).Obj())
			gomega.Eventually(func(g gomega.Gomega) {
				wl := getWorkload(g, worker1.ctx, worker1.client, workloadKey)
				g.Expect(workloadfinish.Finish(worker1.ctx, worker1.client, wl, kueue.WorkloadSliceReplaced, "Replaced to accommodate a new slice", util.RealClock)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("observe: the increased worker replicas are synced to the worker1 cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				remote := raycluster.DeepCopy()
				getRayCluster(worker1.ctx, worker1.client, remote)
				g.Expect(remote.Spec.WorkerGroupSpecs[0].Replicas).To(gomega.BeEquivalentTo(new(int32(3))))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		/*
			Scale-down Section.
			Note: Scaling down does not create a new workload slice, so we continue using newWorkloadKey.
		*/
		ginkgo.By("scale-down the RayCluster's first worker group to 1", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				getRayCluster(manager.ctx, manager.client, raycluster)
				raycluster.Spec.WorkerGroupSpecs[0].Replicas = new(int32(1))
				g.Expect(manager.client.Update(manager.ctx, raycluster)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("observe: no new workload is created in response to scale-down in the manager cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				wl := getWorkload(g, manager.ctx, manager.client, newWorkloadKey)
				g.Expect(wl.Spec.PodSets[1].Count).To(gomega.BeEquivalentTo(int32(1)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			list := &kueue.WorkloadList{}
			gomega.Expect(manager.client.List(manager.ctx, list, client.InNamespace(raycluster.Namespace))).To(gomega.Succeed())
			gomega.Expect(list.Items).To(gomega.HaveLen(2))
		})

		ginkgo.By("observe: the reduced worker replicas are synced to the worker1 cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				remote := raycluster.DeepCopy()
				getRayCluster(worker1.ctx, worker1.client, remote)
				g.Expect(remote.Spec.WorkerGroupSpecs[0].Replicas).To(gomega.BeEquivalentTo(new(int32(1))))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("observe: there are still no workloads in the worker2 cluster", func() {
			workloads := &kueue.WorkloadList{}
			gomega.Expect(worker2.client.List(worker2.ctx, workloads, client.InNamespace(raycluster.Namespace))).To(gomega.Succeed())
			gomega.Expect(workloads.Items).To(gomega.BeEmpty())
		})
	})
})
