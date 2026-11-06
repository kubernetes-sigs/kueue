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
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe("MultiKueue with the remote workload cache", ginkgo.Label("area:multikueue", "feature:multikueue"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var f *multiKueueFixture

	ginkgo.BeforeAll(func() {
		// The cache is built when a worker connects, so the gate must be set before the manager starts.
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MultiKueueRemoteWorkloadCache, true)
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

	ginkgo.It("Should run a job on worker if admitted, reading the remote workloads from the cache", func() {
		job := testingjob.MakeJob("job", f.managerNs.Name).
			Queue(kueue.LocalQueueName(f.managerLq.Name)).
			Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: f.managerNs.Name}

		ginkgo.By("setting workload reservation in the management cluster", func() {
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).
				Obj()
			integration.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission)
		})

		ginkgo.By("checking the workload creation in the worker clusters", func() {
			managerWl := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				behavioral.ExpectRemoteWorkloadSpec(g, createdWorkload, managerWl)
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				behavioral.ExpectRemoteWorkloadSpec(g, createdWorkload, managerWl)
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting workload reservation in worker1, AC state is updated in manager and worker2 wl is removed", func() {
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).
				Obj()
			integration.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, wlLookupKey, admission)

			behavioral.ExpectAdmissionCheckStateWithMessage(
				managerTestCluster.ctx, managerTestCluster.client, wlLookupKey,
				f.multiKueueAC.Name,
				kueue.CheckStateReady,
				`The workload was admitted on "worker1"`,
			)
			behavioral.ExpectEventAppeared(managerTestCluster.ctx, managerTestCluster.client, eventsv1.Event{
				Reason: "MultiKueue",
				Type:   corev1.EventTypeNormal,
				Note:   `The workload was admitted on "worker1"`,
			})

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker job", func() {
			reachedPodsReason := "Reached expected number of succeeded pods"
			finishJobReason := "Job finished successfully"
			now := metav1.Now()
			// completedJobCondition that we will add to the remote job to indicate job completions,
			// and the same condition that we expect to see on the local job status.
			completedJobCondition := batchv1.JobCondition{
				Type:               batchv1.JobComplete,
				Status:             corev1.ConditionTrue,
				LastProbeTime:      now,
				LastTransitionTime: now,
				Message:            finishJobReason,
			}

			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
				createdJob.Status.Conditions = append(createdJob.Status.Conditions,
					batchv1.JobCondition{
						Type:               batchv1.JobSuccessCriteriaMet,
						Status:             corev1.ConditionTrue,
						LastProbeTime:      now,
						LastTransitionTime: now,
						Message:            reachedPodsReason,
					},
					completedJobCondition)
				createdJob.Status.Succeeded = 1
				createdJob.Status.StartTime = new(now)
				createdJob.Status.CompletionTime = new(now)
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdJob)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, finishJobReason)

			// Assert job complete condition.
			localJob := &batchv1.Job{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(job), localJob)).To(gomega.Succeed())
			gomega.Expect(localJob.Status.Conditions).Should(gomega.ContainElement(gomega.WithTransform(func(condition batchv1.JobCondition) batchv1.JobCondition {
				// Compare on all condition attributes excluding Time values.
				condition.LastProbeTime = completedJobCondition.LastProbeTime
				condition.LastTransitionTime = completedJobCondition.LastTransitionTime
				return condition
			}, gomega.Equal(completedJobCondition))))
		})
	})
})
