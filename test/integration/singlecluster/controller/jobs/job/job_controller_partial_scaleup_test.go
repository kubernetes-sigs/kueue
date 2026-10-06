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

package job

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	util "sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Job with partial replica scale-up for elastic jobs", ginkgo.Label("job:batch", "area:jobs"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns             *corev1.Namespace
		resourceFlavor *kueue.ResourceFlavor
		clusterQueue   *kueue.ClusterQueue
		localQueue     *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp, true)
		fwk.StartManager(ctx, cfg, managerAndControllersSetup(false, true, nil,
			jobframework.WithWaitForPodsReady(&configapi.WaitForPodsReady{}),
		))
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "partial-scaleup-")

		resourceFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, resourceFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("default").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).Resource(corev1.ResourceCPU, "7").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("default", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
	})

	// scaleJob emulates an external actor directly mutating .spec.parallelism and
	// .spec.completions in lockstep, exactly as upstream's "Elastic Indexed Jobs" requires for a
	// running, unsuspended Job - Kueue itself never touches these fields for an elastic Job.
	scaleJob := func(job *batchv1.Job, replicas int32) {
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).Should(gomega.Succeed())
			job.Spec.Parallelism = new(replicas)
			job.Spec.Completions = new(replicas)
			g.Expect(k8sClient.Update(ctx, job)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	}

	ginkgo.It("Should partially admit a batch Job scale-up when quota is insufficient", func() {
		testJob := testingjob.MakeJob("foo", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			Indexed(true).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(corev1.ResourceCPU, "1").
			Parallelism(5).
			Completions(5).
			Obj()

		// -------------------------------------------------------------------------------------
		// KEP Step 0: job creation at 5 replicas, which fits under the 7-pod quota, so the
		// initial workload is admitted in full - initial creation is never partially admitted
		// (Non-Goal).
		// -------------------------------------------------------------------------------------
		ginkgo.By("creating the job with 5 replicas")
		util.MustCreate(ctx, k8sClient, testJob)

		ginkgo.By("admitting the job's workload fully")
		initialSlice := &util.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		gomega.Expect(initialSlice.Spec.PodSets).Should(gomega.HaveLen(1))
		gomega.Expect(initialSlice.Spec.PodSets[0].Count).Should(gomega.Equal(int32(5)))
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, initialSlice, kueue.DefaultPodSetName, 5)

		// -------------------------------------------------------------------------------------
		// KEP Step 1: scale up from 5 to 10. The full request (10 pods) exceeds the 7-pod
		// quota, so instead of being rejected outright the scale-up is admitted partially, up
		// to the available quota.
		// -------------------------------------------------------------------------------------
		ginkgo.By("scaling the job to 10 replicas")
		scaleJob(testJob, 10)

		ginkgo.By("a new workload slice replaces the admitted one, requesting the full 10 replicas")
		partialSlice := util.ExpectNewWorkloadSlice(ctx, k8sClient, initialSlice)
		gomega.Expect(partialSlice.Spec.PodSets[0].Count).Should(gomega.Equal(int32(10)))
		// MinCount is the baseline: the previously-admitted count, which the scale-up may not
		// go below.
		gomega.Expect(partialSlice.Spec.PodSets[0].MinCount).Should(gomega.Equal(new(int32(5))))

		ginkgo.By("only 7 of the 10 requested replicas fit the quota")
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, partialSlice, kueue.DefaultPodSetName, 7)

		ginkgo.By("the old (pre-scale-up) slice is finished")
		util.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKeyFromObject(initialSlice))

		// Alongside the partially-admitted slice, a probe workload is created for the full
		// request. It is what lets the remaining replicas be admitted later, with no further
		// scale event.
		ginkgo.By("a scale-up probe workload is created for the full 10 replicas")
		probe := util.ExpectNewWorkloadSlice(ctx, k8sClient, partialSlice)
		gomega.Expect(probe.Spec.PodSets[0].Count).Should(gomega.Equal(int32(10)))

		ginkgo.By("the probe workload stays pending while the quota is exhausted")
		util.ExpectWorkloadsToBePending(ctx, k8sClient, probe)

		// -------------------------------------------------------------------------------------
		// KEP Step 2: capacity appears and the probe is admitted opportunistically, replacing
		// the partially-admitted slice.
		// -------------------------------------------------------------------------------------
		ginkgo.By("raising the ClusterQueue's CPU quota from 7 to 10")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).Should(gomega.Succeed())
			util.SetResourceNominalQuota(clusterQueue, corev1.ResourceCPU, "10")
			g.Expect(k8sClient.Update(ctx, clusterQueue)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("the probe workload is admitted with the full 10 replicas")
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, probe, kueue.DefaultPodSetName, 10)

		ginkgo.By("the partially-admitted slice is finished, having been replaced by the probe")
		util.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKeyFromObject(partialSlice))
	})

	ginkgo.It("Should partially admit a NonIndexed batch Job scale-up when quota is insufficient", func() {
		// NonIndexed Jobs don't need Indexed completion mode or the completions==parallelism
		// lockstep: validateElasticJobPartialScaleUp only enforces that for Indexed Jobs, since
		// upstream already allows mutating a NonIndexed Job's .spec.parallelism on its own.
		testJob := testingjob.MakeJob("baz", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(corev1.ResourceCPU, "1").
			Parallelism(5).
			Completions(50).
			Obj()

		ginkgo.By("creating a NonIndexed job with 5 replicas")
		util.MustCreate(ctx, k8sClient, testJob)

		ginkgo.By("admitting the job's workload fully")
		initialSlice := &util.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		gomega.Expect(initialSlice.Spec.PodSets[0].Count).Should(gomega.Equal(int32(5)))
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, initialSlice, kueue.DefaultPodSetName, 5)

		ginkgo.By("scaling parallelism alone to 10, leaving completions at 50 untouched")

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testJob), testJob)).Should(gomega.Succeed())
			testJob.Spec.Parallelism = new(int32(10))
			g.Expect(k8sClient.Update(ctx, testJob)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("a new workload slice replaces the admitted one, requesting the full 10 replicas")
		partialSlice := util.ExpectNewWorkloadSlice(ctx, k8sClient, initialSlice)
		gomega.Expect(partialSlice.Spec.PodSets[0].Count).Should(gomega.Equal(int32(10)))
		// MinCount is the baseline: the previously-admitted count, which the scale-up may not
		// go below.
		gomega.Expect(partialSlice.Spec.PodSets[0].MinCount).Should(gomega.Equal(new(int32(5))))

		ginkgo.By("only 7 of the 10 requested replicas fit the quota")
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, partialSlice, kueue.DefaultPodSetName, 7)

		ginkgo.By("the old (pre-scale-up) slice is finished")
		util.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKeyFromObject(initialSlice))
	})

	ginkgo.It("Should report PodsReady once the granted pod count is reached, not the full scale-up target", func() {
		testJob := testingjob.MakeJob("bar", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			Indexed(true).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(corev1.ResourceCPU, "1").
			Parallelism(3).
			Completions(3).
			Obj()

		ginkgo.By("creating and fully admitting the job at 3 replicas")
		util.MustCreate(ctx, k8sClient, testJob)
		initialSlice := &util.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, initialSlice, kueue.DefaultPodSetName, 3)

		ginkgo.By("scaling the job to 10 replicas, which only partially fits the 7-pod quota")
		scaleJob(testJob, 10)
		partialSlice := util.ExpectNewWorkloadSlice(ctx, k8sClient, initialSlice)
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, partialSlice, kueue.DefaultPodSetName, 7)

		ginkgo.By("the old (pre-scale-up) slice is finished")
		util.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKeyFromObject(initialSlice))

		jobKey := client.ObjectKeyFromObject(testJob)
		wlKey := client.ObjectKeyFromObject(partialSlice)

		ginkgo.By("reporting fewer ready pods than the granted count keeps PodsReady false")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, jobKey, testJob)).Should(gomega.Succeed())
			// Active must be >= Ready: the apiserver rejects a Job status update otherwise.
			testJob.Status.Active = 10
			testJob.Status.Ready = new(int32(6))
			g.Expect(k8sClient.Status().Update(ctx, testJob)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Consistently(func(g gomega.Gomega) {
			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
			if cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadPodsReady); cond != nil {
				g.Expect(cond.Status).ShouldNot(gomega.Equal(metav1.ConditionTrue))
			}
		}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())

		ginkgo.By("reaching the granted count (7), not the full scale-up target (10), reports PodsReady")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, jobKey, testJob)).Should(gomega.Succeed())
			testJob.Status.Active = 10
			testJob.Status.Ready = new(int32(7))
			g.Expect(k8sClient.Status().Update(ctx, testJob)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		util.ExpectPodsReadyCondition(ctx, k8sClient, wlKey)
	})
})
