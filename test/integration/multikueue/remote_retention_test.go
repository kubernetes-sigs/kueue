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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/multikueue"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	workloadjobset "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("MultiKueue remote object retention", ginkgo.Ordered, ginkgo.ContinueOnFailure, ginkgo.Label("area:multikueue", "feature:multikueue"), func() {
	var f *multiKueueFixture
	var remoteObjectsAfterFinished time.Duration

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MultiKueueRemoteObjectRetention, true)
		// Keep objects longer than util.LongConsistentDuration, but expire them
		// within util.MediumTimeout.
		remoteObjectsAfterFinished = 15 * time.Second
	})

	ginkgo.JustBeforeEach(func() {
		managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
			managerAndMultiKueueSetup(ctx, mgr, 2*time.Second, defaultEnabledIntegrations, config.MultiKueueDispatcherModeAllAtOnce,
				multikueue.WithRemoteObjectsAfterFinished(remoteObjectsAfterFinished))
		})

		f = setupMultiKueueFixture()
	})

	ginkgo.AfterEach(func() {
		f.teardown()
		managerTestCluster.fwk.StopManager(managerTestCluster.ctx)
	})

	ginkgo.It("Should keep the remote objects of a finished workload until the retention elapses", framework.SlowSpec, func() {
		job, wlLookupKey := admitJobOnWorker1(f)
		jobLookupKey := client.ObjectKeyFromObject(job)

		ginkgo.By("finishing the worker1 job, the manager workload finishes", func() {
			finishWorker1Job(jobLookupKey)

			gomega.Eventually(func(g gomega.Gomega) {
				createdWorkload := &kueue.Workload{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeTrue())
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("keeping the worker1 workload and job while the retention holds", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				createdWorkload := &kueue.Workload{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				createdJob := batchv1.Job{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, jobLookupKey, &createdJob)).To(gomega.Succeed())
			}, util.LongConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.By("deleting the worker1 workload and job once the retention elapsed", func() {
			expectWorker1ObjectsDeleted(jobLookupKey, wlLookupKey, util.MediumTimeout)
		})

		ginkgo.By("keeping the manager job and workload", func() {
			createdWorkload := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			createdJob := batchv1.Job{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, jobLookupKey, &createdJob)).To(gomega.Succeed())
		})
	})

	ginkgo.It("Should keep a remote JobSet and workload until the retention elapses", framework.SlowSpec, func() {
		jobSet := testingjobset.MakeJobSet("job-set", f.managerNs.Name).
			Queue(f.managerLq.Name).
			ManagedBy(kueue.MultiKueueControllerName).
			ReplicatedJobs(testingjobset.ReplicatedJobRequirements{
				Name:        "replicated-job",
				Replicas:    1,
				Parallelism: 1,
				Completions: 1,
			}).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, jobSet)

		jobSetLookupKey := client.ObjectKeyFromObject(jobSet)
		wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: f.managerNs.Name}
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
			PodSets(utiltestingapi.MakePodSetAssignment("replicated-job").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj())

		setQuotaReservationInCluster(wlLookupKey, admission)
		checkingTheWorkloadCreation(wlLookupKey, gomega.Succeed())

		ginkgo.By("admitting the workload in worker1, the JobSet is created there", func() {
			util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlLookupKey, admission.Obj())
			gomega.Eventually(func(g gomega.Gomega) {
				createdJobSet := &jobset.JobSet{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, jobSetLookupKey, createdJobSet)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker1 JobSet, the manager workload finishes", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdJobSet := &jobset.JobSet{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, jobSetLookupKey, createdJobSet)).To(gomega.Succeed())
				apimeta.SetStatusCondition(&createdJobSet.Status.Conditions, metav1.Condition{
					Type:    string(jobset.JobSetCompleted),
					Status:  metav1.ConditionTrue,
					Reason:  "ByTest",
					Message: "JobSet finished successfully",
				})
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, createdJobSet)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdWorkload := &kueue.Workload{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeTrue())
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("keeping the worker1 workload and JobSet while the retention holds", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				createdWorkload := &kueue.Workload{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				createdJobSet := &jobset.JobSet{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, jobSetLookupKey, createdJobSet)).To(gomega.Succeed())
			}, util.LongConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.By("deleting the worker1 workload and JobSet once the retention elapsed", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdWorkload := &kueue.Workload{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
				createdJobSet := &jobset.JobSet{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, jobSetLookupKey, createdJobSet)).To(utiltesting.BeNotFoundError())
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should replace a leftover same-name remote Job from an earlier run", func() {
		const jobName = "retained-job"
		jobLookupKey := types.NamespacedName{Name: jobName, Namespace: f.managerNs.Name}

		var leftoverUID types.UID
		ginkgo.By("creating a leftover MultiKueue Job on worker1 from an earlier run", func() {
			leftover := testingjob.MakeJob(jobName, f.worker1Ns.Name).
				Label(kueue.MultiKueueOriginLabel, config.DefaultMultiKueueOrigin).
				PrebuiltWorkloadAnnotation("old-workload").
				Obj()
			util.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, leftover)
			leftoverUID = leftover.UID
		})

		job := testingjob.MakeJob(jobName, f.managerNs.Name).
			Queue(kueue.LocalQueueName(f.managerLq.Name)).
			Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)
		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: f.managerNs.Name}
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj())

		setQuotaReservationInCluster(wlLookupKey, admission)
		checkingTheWorkloadCreation(wlLookupKey, gomega.Succeed())

		ginkgo.By("admitting the workload in worker1, the leftover Job is replaced", func() {
			util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlLookupKey, admission.Obj())
			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, jobLookupKey, &createdJob)).To(gomega.Succeed())
				g.Expect(createdJob.UID).NotTo(gomega.Equal(leftoverUID))
				g.Expect(createdJob.Annotations[controllerconstants.PrebuiltWorkloadAnnotation]).To(gomega.Equal(wlLookupKey.Name))
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("completion races with eviction", func() {
		ginkgo.BeforeEach(func() {
			remoteObjectsAfterFinished = time.Hour
		})

		ginkgo.It("Should immediately delete retained objects when the finished manager Workload is evicted", func() {
			job, wlLookupKey := admitJobOnWorker1(f)
			jobLookupKey := client.ObjectKeyFromObject(job)
			finishWorker1Job(jobLookupKey)

			ginkgo.By("observing completion while remote objects are retained", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					wl := &kueue.Workload{}
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, wl)).To(gomega.Succeed())
					g.Expect(apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				gomega.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, jobLookupKey, &batchv1.Job{})).To(gomega.Succeed())
			})

			ginkgo.By("reconciling the eviction condition alongside completion", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					wl := &kueue.Workload{}
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, wl)).To(gomega.Succeed())
					workloadevict.SetEvictedCondition(wl, time.Now(), kueue.WorkloadEvictedByPreemption, "preempted")
					wl.Status.ClusterName = nil
					g.Expect(managerTestCluster.client.Status().Update(managerTestCluster.ctx, wl)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			expectWorker1ObjectsDeleted(jobLookupKey, wlLookupKey, util.Timeout)
		})
	})
})

// admitJobOnWorker1 creates a job in the manager cluster and admits its workload on
// worker1, so that the job is mirrored there. It returns the manager job and the key
// of its workload, which is the same in both clusters.
func admitJobOnWorker1(f *multiKueueFixture) (*batchv1.Job, types.NamespacedName) {
	ginkgo.GinkgoHelper()

	job := testingjob.MakeJob("job", f.managerNs.Name).
		Queue(kueue.LocalQueueName(f.managerLq.Name)).
		Obj()
	util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)

	jobLookupKey := client.ObjectKeyFromObject(job)
	wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: f.managerNs.Name}
	admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
		PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj())

	setQuotaReservationInCluster(wlLookupKey, admission)
	checkingTheWorkloadCreation(wlLookupKey, gomega.Succeed())

	ginkgo.By("admitting the workload in worker1, the job is created there", func() {
		util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlLookupKey, admission.Obj())
		gomega.Eventually(func(g gomega.Gomega) {
			createdJob := batchv1.Job{}
			g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, jobLookupKey, &createdJob)).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	return job, wlLookupKey
}

// finishWorker1Job completes the worker1 mirror of the job, which is what makes the
// manager workload finish and its remote objects eligible for deletion.
func finishWorker1Job(jobLookupKey types.NamespacedName) {
	ginkgo.GinkgoHelper()

	now := metav1.Now()
	gomega.Eventually(func(g gomega.Gomega) {
		createdJob := batchv1.Job{}
		g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, jobLookupKey, &createdJob)).To(gomega.Succeed())
		createdJob.Status.Conditions = append(createdJob.Status.Conditions,
			batchv1.JobCondition{
				Type:               batchv1.JobSuccessCriteriaMet,
				Status:             corev1.ConditionTrue,
				LastProbeTime:      now,
				LastTransitionTime: now,
				Message:            "Reached expected number of succeeded pods",
			},
			batchv1.JobCondition{
				Type:               batchv1.JobComplete,
				Status:             corev1.ConditionTrue,
				LastProbeTime:      now,
				LastTransitionTime: now,
				Message:            "Job finished successfully",
			})
		createdJob.Status.Succeeded = 1
		createdJob.Status.StartTime = new(now)
		createdJob.Status.CompletionTime = new(now)
		g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdJob)).To(gomega.Succeed())
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
}

func expectWorker1ObjectsDeleted(jobLookupKey, wlLookupKey types.NamespacedName, timeout time.Duration) {
	ginkgo.GinkgoHelper()

	gomega.Eventually(func(g gomega.Gomega) {
		createdWorkload := &kueue.Workload{}
		g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
		createdJob := batchv1.Job{}
		g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, jobLookupKey, &createdJob)).To(utiltesting.BeNotFoundError())
	}, timeout, util.Interval).Should(gomega.Succeed())
}
