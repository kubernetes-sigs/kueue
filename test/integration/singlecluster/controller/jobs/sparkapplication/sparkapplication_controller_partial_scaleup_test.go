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

	sparkv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
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

	// runExecutors emulates the Spark driver and the Spark Operator: the driver runs and the given
	// number of executor pods exist, all reported as running. envtest runs neither of them.
	runExecutors := func(sparkApp *sparkv1beta2.SparkApplication, executors int) {
		ginkgo.GinkgoHelper()
		executorState := make(map[string]sparkv1beta2.ExecutorState, executors)
		for i := range executors {
			name := fmt.Sprintf("%s-exec-%d", sparkApp.Name, i+1)
			executorState[name] = sparkv1beta2.ExecutorStateRunning
			setExecutorPodPhase(ctx, k8sClient, sparkApp, name, corev1.PodRunning)
		}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sparkApp), sparkApp)).Should(gomega.Succeed())
			sparkApp.Status.AppState.State = sparkv1beta2.ApplicationStateRunning
			sparkApp.Status.SubmissionAttempts = 1
			sparkApp.Status.SubmissionID = submissionID
			sparkApp.Status.ExecutorState = executorState
			g.Expect(k8sClient.Status().Update(ctx, sparkApp)).Should(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}

	ginkgo.BeforeAll(func() {
		features.SetFeatureGatesDuringTest(ginkgo.GinkgoTB(), map[featuregate.Feature]bool{
			features.ElasticJobsViaWorkloadSlices:                          true,
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
		// 1 driver + 2 executors = 3 pods, fitting the 4-pod quota.
		testSparkApp := testingsparkapplication.MakeSparkApplication("foo", ns.Name).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Annotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
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
		runExecutors(testSparkApp, 5)

		ginkgo.By("a new workload slice replaces the admitted one, requesting the full 5 executors")
		partialSlice := behavioral.ExpectNewWorkloadSlice(ctx, k8sClient, initialSlice)
		gomega.Expect(partialSlice.Spec.PodSets[executorsPodSet].Count).Should(gomega.Equal(int32(5)))
		// MinCount is copied forward from initialSlice's own floor, which was bootstrapped
		// to its own request (2) at creation - minExecutors is not consulted.
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
})
