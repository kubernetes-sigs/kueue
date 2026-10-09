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
)

var _ = ginkgo.Describe("MultiKueue cluster cordon", ginkgo.Label("area:multikueue", "feature:multikueue"), ginkgo.Ordered, func() {
	ginkgo.DescribeTable("stops new dispatch and preserves existing jobs", func(mode string) {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MultiKueueClusterCordon, true)
		managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
			managerAndMultiKueueSetup(ctx, mgr, 2*time.Second, defaultEnabledIntegrations, mode)
		})
		ginkgo.DeferCleanup(func() { managerTestCluster.fwk.StopManager(managerTestCluster.ctx) })
		f := setupMultiKueueFixture()
		ginkgo.DeferCleanup(f.teardown)

		createJob := func(name string) types.NamespacedName {
			job := testingjob.MakeJob(name, f.managerNs.Name).Queue(kueue.LocalQueueName(f.managerLq.Name)).Obj()
			behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)
			key := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: job.Namespace}
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).Obj()
			behavioral.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, key, admission)
			return key
		}
		setCordon := func(cluster *kueue.MultiKueueCluster, value bool) {
			gomega.Eventually(func(g gomega.Gomega) {
				current := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(cluster), current)).To(gomega.Succeed())
				before := current.DeepCopy()
				current.Spec.Unschedulable = new(value)
				g.Expect(managerTestCluster.client.Patch(managerTestCluster.ctx, current, client.MergeFrom(before))).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				current := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(cluster), current)).To(gomega.Succeed())
				g.Expect(current.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.MultiKueueClusterActive))
				g.Expect(current.Status.Conditions[0].ObservedGeneration).To(gomega.Equal(current.Generation))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		}

		ginkgo.By("dispatching an existing job before cordon")
		existingKey := createJob("existing")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, existingKey, &kueue.Workload{})).To(gomega.Succeed())
			g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, existingKey, &kueue.Workload{})).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("cordoning both workers without disconnecting them")
		setCordon(f.workerCluster1, true)
		setCordon(f.workerCluster2, true)
		waitingKey := createJob("waiting")
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, waitingKey, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
			g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, waitingKey, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
			g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, existingKey, &kueue.Workload{})).To(gomega.Succeed())
			g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, existingKey, &kueue.Workload{})).To(gomega.Succeed())
		}, behavioral.LongConsistentDuration, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("admitting and completing the existing job on a cordoned worker")
		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.worker1Cq.Name)).
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj()).Obj()
		behavioral.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, existingKey, admission)
		behavioral.ExpectAdmissionCheckStateWithMessage(managerTestCluster.ctx, managerTestCluster.client, existingKey, f.multiKueueAC.Name,
			kueue.CheckStateReady, `The workload was admitted on "worker1"`)
		gomega.Eventually(func(g gomega.Gomega) {
			job := &batchv1.Job{}
			g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, types.NamespacedName{Name: "existing", Namespace: f.worker1Ns.Name}, job)).To(gomega.Succeed())
			now := metav1.Now()
			job.Status.Succeeded = 1
			job.Status.StartTime = &now
			job.Status.CompletionTime = &now
			job.Status.Conditions = []batchv1.JobCondition{
				{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue, LastTransitionTime: now},
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: now, Message: "Job finished successfully"},
			}
			g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, job)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(existingKey, "Job finished successfully")

		ginkgo.By("uncordoning worker1 resumes dispatch of the waiting job")
		setCordon(f.workerCluster1, false)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, waitingKey, &kueue.Workload{})).To(gomega.Succeed())
			g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, waitingKey, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	},
		ginkgo.Entry("all at once dispatcher", config.MultiKueueDispatcherModeAllAtOnce),
		ginkgo.Entry("incremental dispatcher", config.MultiKueueDispatcherModeIncremental),
	)
})
