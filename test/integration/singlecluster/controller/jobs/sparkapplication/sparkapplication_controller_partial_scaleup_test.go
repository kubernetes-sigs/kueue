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

package sparkapplication

import (
	"slices"

	sparkv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingsparkapplication "sigs.k8s.io/kueue/pkg/util/testingjobs/sparkapplication"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

// KEP-12100: partial replica scale-up for a SparkApplication. This suite exists because
// SparkApplication derives its workload slice name from its executor IDs, so the scale-up probe
// has to be verified separately for it.
var _ = ginkgo.Describe("SparkApplication with partial replica scale-up for elastic jobs", ginkgo.Label("job:sparkapplication", "area:jobs"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	// The SparkApplication workload has the driver PodSet at index 0 and the executor PodSet at index 1.
	const executorsPodSet = 1

	var (
		ns             *corev1.Namespace
		resourceFlavor *kueue.ResourceFlavor
		clusterQueue   *kueue.ClusterQueue
		localQueue     *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		features.SetFeatureGatesDuringTest(ginkgo.GinkgoTB(), map[featuregate.Feature]bool{
			features.ElasticJobsViaWorkloadSlicesForSparkApplication:       true,
			features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp: true,
		})
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(false))
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "sparkapplication-scale-up-")

		resourceFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		behavioral.MustCreate(ctx, k8sClient, resourceFlavor)

		// "pods" is the constrained resource so every assertion is a plain pod count; cpu and
		// memory are declared generously because Kueue only admits a Workload whose every requested
		// resource is covered by a resource group.
		clusterQueue = utiltestingapi.MakeClusterQueue("default").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
				Resource(corev1.ResourcePods, "4").
				Resource(corev1.ResourceCPU, "100").
				Resource(corev1.ResourceMemory, "100Gi").
				Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("default", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, localQueue)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
	})

	ginkgo.It("Should partially admit a SparkApplication scale-up and create a scale-up probe", func() {
		// 1 driver + 2 executors = 3 pods, fitting the 4-pod quota. The suite installs no webhook
		// configurations, so the executor template is gated here, as the SparkApplication webhook
		// does in production.
		testSparkApp := testingsparkapplication.MakeSparkApplication("foo", ns.Name).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Annotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			ExecutorSchedulingGates(corev1.PodSchedulingGate{Name: kueue.ElasticJobSchedulingGate}).
			Queue(localQueue.Name).
			ExecutorInstances(2).
			DynamicAllocation(&sparkv1beta2.DynamicAllocation{Enabled: true, MaxExecutors: new(int32(5))}).
			Obj()

		ginkgo.By("creating the sparkapplication with 2 executors")
		behavioral.MustCreate(ctx, k8sClient, testSparkApp)

		ginkgo.By("admitting the sparkapplication's workload fully")
		initialSlice := &behavioral.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		gomega.Expect(initialSlice.Spec.PodSets).Should(gomega.HaveLen(2))
		gomega.Expect(initialSlice.Spec.PodSets[executorsPodSet].Count).Should(gomega.Equal(int32(2)))
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, initialSlice, executorPodSetName, 2)

		// The full request is 1 + 5 = 6 pods against a 4-pod quota, so only 3 executors fit.
		ginkgo.By("the driver scaling up to 5 executors")
		setAppStatus(ctx, k8sClient, testSparkApp, sparkv1beta2.ApplicationStateRunning, slices.Repeat([]sparkv1beta2.ExecutorState{sparkv1beta2.ExecutorStateRunning}, 5)...)

		ginkgo.By("a new workload slice replaces the admitted one, requesting the full 5 executors")
		partialSlice := behavioral.ExpectNewWorkloadSlice(ctx, k8sClient, initialSlice)
		gomega.Expect(partialSlice.Spec.PodSets[executorsPodSet].Count).Should(gomega.Equal(int32(5)))
		// The scale-up slice keeps the floor of the first slice: its 2 executors.
		gomega.Expect(partialSlice.Spec.PodSets[executorsPodSet].MinCount).Should(gomega.Equal(new(int32(2))))

		ginkgo.By("only 3 of the 5 requested executors fit: 1 driver + 3 executors = the whole quota")
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, partialSlice, executorPodSetName, 3)

		// SparkApplication overrides the workload slice name extra part with its executor IDs, so if
		// that discards the probe's extra parameter the probe cannot get a distinct name and never
		// appears.
		ginkgo.By("a scale-up probe workload is created for the full 5 executors")
		probe := behavioral.ExpectNewWorkloadSlice(ctx, k8sClient, partialSlice)
		gomega.Expect(probe.Spec.PodSets[executorsPodSet].Count).Should(gomega.Equal(int32(5)))

		ginkgo.By("the probe workload stays pending while the quota is exhausted")
		behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, probe)
	})

	ginkgo.It("Should admit the first workload all-or-nothing", func() {
		ginkgo.By("another sparkapplication taking 2 of the 4 pods: 1 driver + 1 executor")
		blocker := testingsparkapplication.MakeSparkApplication("blocker", ns.Name).
			Queue(localQueue.Name).
			ExecutorInstances(1).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, blocker)
		blockerWl := &behavioral.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, blockerWl)

		// The driver starts with 3 executors: 1 driver + 3 executors = 4 pods, while only 2 are free.
		// The SparkApplication is created suspended, as the webhook does in production.
		testSparkApp := testingsparkapplication.MakeSparkApplication("foo", ns.Name).
			Suspend(true).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Annotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			ExecutorSchedulingGates(corev1.PodSchedulingGate{Name: kueue.ElasticJobSchedulingGate}).
			Queue(localQueue.Name).
			ExecutorInstances(3).
			DynamicAllocation(&sparkv1beta2.DynamicAllocation{Enabled: true, MinExecutors: new(int32(1)), MaxExecutors: new(int32(3))}).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, testSparkApp)

		ginkgo.By("the first workload asks for all 3 executors and stays pending, with no scale-up probe")
		var wl *kueue.Workload
		gomega.Eventually(func(g gomega.Gomega) {
			wls := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, wls, client.InNamespace(ns.Name))).Should(gomega.Succeed())
			g.Expect(wls.Items).Should(gomega.HaveLen(2))
			idx := slices.IndexFunc(wls.Items, func(w kueue.Workload) bool { return w.Name != blockerWl.Name })
			wl = &wls.Items[idx]
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		gomega.Expect(wl.Spec.PodSets[executorsPodSet].Count).Should(gomega.Equal(int32(3)))
		gomega.Expect(wl.Spec.PodSets[executorsPodSet].MinCount).Should(gomega.Equal(new(int32(3))))
		behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
		behavioral.ConsistentlyActiveWorkloadNames(ctx, k8sClient, ns.Name, blockerWl.Name, wl.Name)
		expectSparkAppSuspended(ctx, k8sClient, testSparkApp, true)

		ginkgo.By("the other sparkapplication is deleted, so all 3 executors fit")
		gomega.Expect(k8sClient.Delete(ctx, blocker)).Should(gomega.Succeed())
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, wl, executorPodSetName, 3)
		expectSparkAppSuspended(ctx, k8sClient, testSparkApp, false)
	})

	ginkgo.It("Should update a pending scale-up probe when the executors drop below its minCount", func() {
		testSparkApp := testingsparkapplication.MakeSparkApplication("foo", ns.Name).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Annotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			ExecutorSchedulingGates(corev1.PodSchedulingGate{Name: kueue.ElasticJobSchedulingGate}).
			Queue(localQueue.Name).
			ExecutorInstances(2).
			DynamicAllocation(&sparkv1beta2.DynamicAllocation{Enabled: true, MaxExecutors: new(int32(5))}).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, testSparkApp)
		initialSlice := &behavioral.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, initialSlice, executorPodSetName, 2)

		ginkgo.By("the driver scaling up to 5 executors, of which 3 fit")
		setAppStatus(ctx, k8sClient, testSparkApp, sparkv1beta2.ApplicationStateRunning, slices.Repeat([]sparkv1beta2.ExecutorState{sparkv1beta2.ExecutorStateRunning}, 5)...)
		partialSlice := behavioral.ExpectNewWorkloadSlice(ctx, k8sClient, initialSlice)
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, partialSlice, executorPodSetName, 3)
		probe := behavioral.ExpectNewWorkloadSlice(ctx, k8sClient, partialSlice)
		gomega.Expect(probe.Spec.PodSets[executorsPodSet].MinCount).Should(gomega.Equal(new(int32(2))))
		behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, probe)

		ginkgo.By("the driver removes 4 of its 5 executors, below the probe's minCount of 2")
		setAppStatus(ctx, k8sClient, testSparkApp, sparkv1beta2.ApplicationStateRunning,
			append([]sparkv1beta2.ExecutorState{sparkv1beta2.ExecutorStateRunning}, slices.Repeat([]sparkv1beta2.ExecutorState{sparkv1beta2.ExecutorStateCompleted}, 4)...)...)

		ginkgo.By("the probe follows the executors")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(probe), probe)).Should(gomega.Succeed())
			g.Expect(probe.Spec.PodSets[executorsPodSet].Count).Should(gomega.Equal(int32(1)))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should scale down the admitted workload below its initial executor count", func() {
		testSparkApp := testingsparkapplication.MakeSparkApplication("foo", ns.Name).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Annotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			ExecutorSchedulingGates(corev1.PodSchedulingGate{Name: kueue.ElasticJobSchedulingGate}).
			Queue(localQueue.Name).
			ExecutorInstances(3).
			DynamicAllocation(&sparkv1beta2.DynamicAllocation{Enabled: true, MinExecutors: new(int32(1)), MaxExecutors: new(int32(3))}).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, testSparkApp)
		wl := &behavioral.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, wl, executorPodSetName, 3)
		setAppStatus(ctx, k8sClient, testSparkApp, sparkv1beta2.ApplicationStateRunning, slices.Repeat([]sparkv1beta2.ExecutorState{sparkv1beta2.ExecutorStateRunning}, 3)...)

		ginkgo.By("the driver removes 2 of its 3 executors")
		setAppStatus(ctx, k8sClient, testSparkApp, sparkv1beta2.ApplicationStateRunning,
			sparkv1beta2.ExecutorStateRunning, sparkv1beta2.ExecutorStateCompleted, sparkv1beta2.ExecutorStateCompleted)

		ginkgo.By("the admitted workload is lowered to 1 executor in place")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).Should(gomega.Succeed())
			g.Expect(wl.Spec.PodSets[executorsPodSet].Count).Should(gomega.Equal(int32(1)))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
		behavioral.ConsistentlyActiveWorkloadNames(ctx, k8sClient, ns.Name, wl.Name)
	})

	// https://github.com/kubernetes-sigs/kueue/issues/19524
	// After an eviction a SparkApplication asks for its initial executors again, while the minCount stays lowered.
	// The slice is then admitted partially and a scale-up probe is created for the suspended app.
	ginkgo.It("Should start the app on the partially readmitted slice after a scale-down below its initial executors and an eviction", func() {
		ginkgo.By("the cluster queue lets a higher-priority workload preempt a lower-priority one")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).Should(gomega.Succeed())
			clusterQueue.Spec.Preemption = &kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}
			g.Expect(k8sClient.Update(ctx, clusterQueue)).Should(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		// The suite installs no webhook configurations, so the app is suspended and its executor template gated here.
		testSparkApp := testingsparkapplication.MakeSparkApplication("foo", ns.Name).
			Suspend(true).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Annotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			ExecutorSchedulingGates(corev1.PodSchedulingGate{Name: kueue.ElasticJobSchedulingGate}).
			Queue(localQueue.Name).
			ExecutorInstances(1).
			DynamicAllocation(&sparkv1beta2.DynamicAllocation{
				Enabled:          true,
				InitialExecutors: new(int32(3)),
				MinExecutors:     new(int32(1)),
				MaxExecutors:     new(int32(3)),
			}).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, testSparkApp)

		ginkgo.By("the first slice is admitted with the 3 initial executors: 1 driver + 3 executors = the whole quota")
		wl := &behavioral.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, wl, executorPodSetName, 3)
		expectSparkAppSuspended(ctx, k8sClient, testSparkApp, false)
		setAppStatus(ctx, k8sClient, testSparkApp, sparkv1beta2.ApplicationStateRunning, slices.Repeat([]sparkv1beta2.ExecutorState{sparkv1beta2.ExecutorStateRunning}, 3)...)

		ginkgo.By("the driver keeps 1 of its 3 executors, so the slice is lowered to 1 executor with minCount 1")
		setAppStatus(ctx, k8sClient, testSparkApp, sparkv1beta2.ApplicationStateRunning,
			sparkv1beta2.ExecutorStateRunning, sparkv1beta2.ExecutorStateCompleted, sparkv1beta2.ExecutorStateCompleted)
		behavioral.ExpectWorkloadPodSet(ctx, k8sClient, client.ObjectKeyFromObject(wl), executorPodSetName, 1, new(int32(1)))
		behavioral.ExpectClusterQueueResourceUsage(ctx, k8sClient, client.ObjectKeyFromObject(clusterQueue), corev1.ResourcePods, 2)

		ginkgo.By("a higher-priority workload needing 3 of the 4 pods evicts the app as a whole")
		preemptor := utiltestingapi.MakeWorkload("preemptor", ns.Name).
			Priority(1).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			PodSets(*utiltestingapi.MakePodSet("main", 3).Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, preemptor)
		expectSparkAppSuspended(ctx, k8sClient, testSparkApp, true)

		// The Spark Operator stops the driver and its executors when the app is suspended.
		// The quota of the evicted slice is released once they are gone.
		setAppStatus(ctx, k8sClient, testSparkApp, sparkv1beta2.ApplicationStateSuspended,
			sparkv1beta2.ExecutorStateCompleted, sparkv1beta2.ExecutorStateCompleted, sparkv1beta2.ExecutorStateCompleted)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, preemptor)

		ginkgo.By("the requeued slice asks for the 3 initial executors again, while its minCount stays 1")
		behavioral.ExpectWorkloadPodSet(ctx, k8sClient, client.ObjectKeyFromObject(wl), executorPodSetName, 3, new(int32(1)))
		behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl)

		ginkgo.By("one more pod of quota leaves room for the driver + 1 executor")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).Should(gomega.Succeed())
			resources := clusterQueue.Spec.ResourceGroups[0].Flavors[0].Resources
			idx := slices.IndexFunc(resources, func(r kueue.ResourceQuota) bool { return r.Name == corev1.ResourcePods })
			resources[idx].NominalQuota = resource.MustParse("5")
			g.Expect(k8sClient.Update(ctx, clusterQueue)).Should(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("the slice is admitted partially, and a scale-up probe for the 3 executors stays pending")
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, wl, executorPodSetName, 1)
		probe := behavioral.ExpectNewWorkloadSlice(ctx, k8sClient, wl)
		gomega.Expect(probe.Spec.PodSets[executorsPodSet].Count).Should(gomega.Equal(int32(3)))
		behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, probe)

		ginkgo.By("the app starts on the partially admitted slice")
		expectSparkAppSuspended(ctx, k8sClient, testSparkApp, false)
		behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, probe)
	})

	// The executor count follows the executor pods, so it drops to minExecutors when they exit.
	ginkgo.DescribeTable("Should finish the workload when the executors exit",
		func(minExecutors *int32) {
			testSparkApp := testingsparkapplication.MakeSparkApplication("foo", ns.Name).
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				Annotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
				ExecutorSchedulingGates(corev1.PodSchedulingGate{Name: kueue.ElasticJobSchedulingGate}).
				Queue(localQueue.Name).
				ExecutorInstances(2).
				DynamicAllocation(&sparkv1beta2.DynamicAllocation{Enabled: true, MinExecutors: minExecutors, MaxExecutors: new(int32(3))}).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, testSparkApp)
			wl := &behavioral.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
			behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, wl, executorPodSetName, 2)
			setAppStatus(ctx, k8sClient, testSparkApp, sparkv1beta2.ApplicationStateRunning, sparkv1beta2.ExecutorStateRunning, sparkv1beta2.ExecutorStateRunning)

			ginkgo.By("the executors exit and the sparkapplication completes")
			setAppStatus(ctx, k8sClient, testSparkApp, sparkv1beta2.ApplicationStateCompleted, sparkv1beta2.ExecutorStateCompleted, sparkv1beta2.ExecutorStateCompleted)

			behavioral.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKeyFromObject(wl))
		},
		ginkgo.Entry("without minExecutors", nil),
		ginkgo.Entry("with minExecutors", new(int32(1))),
	)
})
