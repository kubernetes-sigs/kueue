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

	kftrainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadtrainjob "sigs.k8s.io/kueue/pkg/controller/jobs/trainjob"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	testingtrainjob "sigs.k8s.io/kueue/pkg/util/testingjobs/trainjob"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("MultiKueue TrainJob", ginkgo.Label("area:multikueue", "feature:multikueue"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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

	ginkgo.It("Should run a TrainJob on worker if admitted", func() {
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("node").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)
		testJobSet := testingjobset.MakeJobSet("", "").ReplicatedJobs(
			testingjobset.ReplicatedJobRequirements{
				Name:     "node",
				Replicas: 1,
			}).
			Obj()
		testCtr := testingtrainjob.MakeClusterTrainingRuntime("test", testJobSet.Spec)
		trainJob := testingtrainjob.MakeTrainJob("trainjob1", f.managerNs.Name).RuntimeRef(kftrainer.RuntimeRef{
			APIGroup: new("trainer.kubeflow.org"),
			Name:     "test",
			Kind:     new("ClusterTrainingRuntime"),
		}).
			Queue(f.managerLq.Name).
			Obj()

		util.MustCreate(worker2TestCluster.ctx, worker2TestCluster.client, testCtr.DeepCopy())
		util.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, testCtr.DeepCopy())
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, testCtr)
		util.MustCreateWithRetry(managerTestCluster.ctx, managerTestCluster.client, trainJob)
		wlLookupKey := types.NamespacedName{Name: workloadtrainjob.GetWorkloadNameForTrainJob(trainJob.Name, trainJob.UID), Namespace: f.managerNs.Name}
		gomega.Eventually(func(g gomega.Gomega) {
			createdWorkload := &kueue.Workload{}
			g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
		}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())

		util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission.Obj())
		admitWorkloadAndCheckWorkerCopies(f.multiKueueAC.Name, wlLookupKey, admission)

		ginkgo.By("changing the status of the TrainJob in the worker, updates the manager's TrainJob status", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdTrainJob := kftrainer.TrainJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(trainJob), &createdTrainJob)).To(gomega.Succeed())
				createdTrainJob.Status.JobsStatus = []kftrainer.JobStatus{
					testingtrainjob.MakeJobStatus("foo").Obj(),
				}
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdTrainJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				createdTrainJob := kftrainer.TrainJob{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(trainJob), &createdTrainJob)).To(gomega.Succeed())
				g.Expect(createdTrainJob.Status.JobsStatus).To(gomega.HaveLen(1))
				g.Expect(createdTrainJob.Status.JobsStatus[0].Name).To(gomega.Equal("foo"))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker TrainJob, the manager's wl is marked as finished and the worker2 wl removed", func() {
			finishJobReason := ""
			gomega.Eventually(func(g gomega.Gomega) {
				createdTrainJob := kftrainer.TrainJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(trainJob), &createdTrainJob)).To(gomega.Succeed())
				apimeta.SetStatusCondition(&createdTrainJob.Status.Conditions, metav1.Condition{
					Type:   kftrainer.TrainJobComplete,
					Status: metav1.ConditionTrue,
					Reason: "ByTest",
				})
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdTrainJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, finishJobReason)
		})
	})
})
