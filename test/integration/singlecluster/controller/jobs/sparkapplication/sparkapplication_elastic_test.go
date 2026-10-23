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
	"fmt"
	"slices"

	sparkv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingsparkapplication "sigs.k8s.io/kueue/pkg/util/testingjobs/sparkapplication"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

const executorPodSetName = "executor"

var _ = ginkgo.Describe("SparkApplication with elastic jobs via workload-slices support", ginkgo.Label("job:sparkapplication", "area:jobs"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns             *corev1.Namespace
		resourceFlavor *kueue.ResourceFlavor
		clusterQueue   *kueue.ClusterQueue
		localQueue     *kueue.LocalQueue
	)

	// expectPodsUsage asserts the ClusterQueue's usage of "pods", the only constrained resource in
	// these specs: one driver pod plus one pod per executor.
	expectPodsUsage := func(pods int64) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			cq := &kueue.ClusterQueue{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), cq)).Should(gomega.Succeed())
			g.Expect(cq.Status.FlavorsUsage).Should(gomega.HaveLen(1))
			resources := cq.Status.FlavorsUsage[0].Resources
			idx := slices.IndexFunc(resources, func(r kueue.ResourceUsage) bool {
				return r.Name == corev1.ResourcePods
			})
			g.Expect(idx).ShouldNot(gomega.Equal(-1), "ClusterQueue reports no pods usage, only %v", resources)
			g.Expect(resources[idx].Total.Value()).Should(gomega.Equal(pods))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}
	// expectExecutorCount asserts the executor PodSet count requested by the Workload.
	expectExecutorCount := func(wl *kueue.Workload, count int32) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).Should(gomega.Succeed())
			idx := slices.IndexFunc(wl.Spec.PodSets, func(ps kueue.PodSet) bool {
				return ps.Name == executorPodSetName
			})
			g.Expect(idx).ShouldNot(gomega.Equal(-1))
			g.Expect(wl.Spec.PodSets[idx].Count).Should(gomega.Equal(count))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}
	// setAppStatus emulates the Spark Operator, which mirrors the driver state and the executor
	// pods into the SparkApplication status in production. envtest runs no Spark Operator.
	setAppStatus := func(sparkApp *sparkv1beta2.SparkApplication, state sparkv1beta2.ApplicationStateType, executors ...sparkv1beta2.ExecutorState) {
		ginkgo.GinkgoHelper()
		executorState := make(map[string]sparkv1beta2.ExecutorState, len(executors))
		for i, st := range executors {
			executorState[fmt.Sprintf("%s-exec-%d", sparkApp.Name, i+1)] = st
		}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sparkApp), sparkApp)).Should(gomega.Succeed())
			sparkApp.Status.AppState.State = state
			sparkApp.Status.SubmissionAttempts = 1
			sparkApp.Status.ExecutorState = executorState
			g.Expect(k8sClient.Status().Update(ctx, sparkApp)).Should(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}
	expectSuspended := func(sparkApp *sparkv1beta2.SparkApplication, suspended bool) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sparkApp), sparkApp)).Should(gomega.Succeed())
			g.Expect(ptr.Deref(sparkApp.Spec.Suspend, false)).Should(gomega.Equal(suspended))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}
	// createElasticSparkApp creates an elastic SparkApplication with 1 initial executor and
	// returns its first workload slice once it is admitted.
	createElasticSparkApp := func(maxExecutors int32) (*sparkv1beta2.SparkApplication, *kueue.Workload) {
		ginkgo.GinkgoHelper()
		sparkApp := testingsparkapplication.MakeSparkApplication("elastic", ns.Name).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Queue(localQueue.Name).
			ExecutorInstances(1).
			DynamicAllocation(&sparkv1beta2.DynamicAllocation{Enabled: true, MaxExecutors: new(maxExecutors)}).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, sparkApp)
		initialSlice := &behavioral.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, initialSlice, executorPodSetName, 1)
		expectSuspended(sparkApp, false)
		expectPodsUsage(2)
		return sparkApp, initialSlice
	}

	ginkgo.BeforeAll(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(false))
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "elastic-")

		resourceFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		behavioral.MustCreate(ctx, k8sClient, resourceFlavor)

		// "pods" is the constrained resource, so every assertion is a plain pod count. cpu and
		// memory are declared because Kueue only admits a Workload whose every requested resource
		// is covered, and they are generous so that "pods" stays the binding constraint.
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

	ginkgo.It("Should support executor scale-up and scale-down", func() {
		sparkApp, initialSlice := createElasticSparkApp(3)

		ginkgo.By("the driver starts with the initial executor, which keeps the admitted slice")
		setAppStatus(sparkApp, sparkv1beta2.ApplicationStateRunning, sparkv1beta2.ExecutorStateRunning)
		gomega.Consistently(func(g gomega.Gomega) {
			list := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, list, client.InNamespace(ns.Name))).Should(gomega.Succeed())
			g.Expect(list.Items).Should(gomega.HaveLen(1))
		}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())

		ginkgo.By("the driver adds 2 executors, and a new slice for 3 executors replaces the admitted one")
		setAppStatus(sparkApp, sparkv1beta2.ApplicationStateRunning,
			sparkv1beta2.ExecutorStateRunning, sparkv1beta2.ExecutorStatePending, sparkv1beta2.ExecutorStatePending)
		scaleUpSlice := behavioral.ExpectNewWorkloadSlice(ctx, k8sClient, initialSlice)
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, scaleUpSlice, executorPodSetName, 3)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(initialSlice), initialSlice)).Should(gomega.Succeed())
			g.Expect(initialSlice.Status.Conditions).Should(
				utiltesting.HaveConditionStatusTrueAndReason(kueue.WorkloadFinished, kueue.WorkloadSliceReplaced))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		expectPodsUsage(4)
		expectSuspended(sparkApp, false)

		ginkgo.By("the driver removes an idle executor, and the admitted slice shrinks in place")
		setAppStatus(sparkApp, sparkv1beta2.ApplicationStateRunning,
			sparkv1beta2.ExecutorStateRunning, sparkv1beta2.ExecutorStateCompleted, sparkv1beta2.ExecutorStateRunning)
		expectExecutorCount(scaleUpSlice, 2)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, scaleUpSlice)
		expectPodsUsage(3)

		ginkgo.By("the driver removes every executor")
		setAppStatus(sparkApp, sparkv1beta2.ApplicationStateRunning,
			sparkv1beta2.ExecutorStateCompleted, sparkv1beta2.ExecutorStateCompleted, sparkv1beta2.ExecutorStateCompleted)
		expectExecutorCount(scaleUpSlice, 0)
		expectPodsUsage(1)

		// Falling back to the initial executors on completion would look like a scale-up,
		// so the active slice would never be finished and the driver's quota would leak.
		ginkgo.By("the SparkApplication completes, and all quota is released")
		setAppStatus(sparkApp, sparkv1beta2.ApplicationStateCompleted,
			sparkv1beta2.ExecutorStateCompleted, sparkv1beta2.ExecutorStateCompleted, sparkv1beta2.ExecutorStateCompleted)
		behavioral.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKeyFromObject(scaleUpSlice))
		expectPodsUsage(0)
	})

	ginkgo.It("Should keep a scale-up pending until there is quota for it", func() {
		sparkApp, initialSlice := createElasticSparkApp(5)

		ginkgo.By("the driver adds 4 executors, more than the quota allows")
		setAppStatus(sparkApp, sparkv1beta2.ApplicationStateRunning,
			sparkv1beta2.ExecutorStateRunning, sparkv1beta2.ExecutorStatePending, sparkv1beta2.ExecutorStatePending,
			sparkv1beta2.ExecutorStatePending, sparkv1beta2.ExecutorStatePending)

		ginkgo.By("the new slice for 5 executors stays pending and the admitted slice keeps its quota")
		scaleUpSlice := behavioral.ExpectNewWorkloadSlice(ctx, k8sClient, initialSlice)
		expectExecutorCount(scaleUpSlice, 5)
		behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, scaleUpSlice)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, initialSlice)
		expectPodsUsage(2)

		ginkgo.By("raising the pods quota, the new slice is admitted and replaces the old one")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).Should(gomega.Succeed())
			behavioral.SetResourceNominalQuota(clusterQueue, corev1.ResourcePods, "6")
			g.Expect(k8sClient.Update(ctx, clusterQueue)).Should(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, scaleUpSlice, executorPodSetName, 5)
		behavioral.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKeyFromObject(initialSlice))
		expectPodsUsage(6)

		ginkgo.By("deactivating the workload suspends the SparkApplication and keeps its executor instances")
		behavioral.DeactivateWorkload(ctx, k8sClient, client.ObjectKeyFromObject(scaleUpSlice))
		expectSuspended(sparkApp, true)
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sparkApp), sparkApp)).Should(gomega.Succeed())
			g.Expect(sparkApp.Spec.Executor.Instances).Should(gomega.Equal(new(int32(1))))
		}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
	})
})
