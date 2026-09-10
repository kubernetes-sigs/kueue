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

package rayjob

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util"
)

// KEP-12100: partial replica scale-up for a RayJob. The RayCluster integration is covered in its
// own suite; this one exists because RayJob derives its workload slice name differently, so the
// scale-up probe has to be verified separately for it.
var _ = ginkgo.Describe("RayJob with partial replica scale-up for elastic jobs", ginkgo.Label("job:ray", "area:jobs"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	// The RayJob workload has the head PodSet at index 0 and the single worker group at index 1;
	// the submitter PodSet is only added in K8sJobMode, which these specs avoid.
	const workersPodSet = 1

	var (
		ns             *corev1.Namespace
		resourceFlavor *kueue.ResourceFlavor
		clusterQueue   *kueue.ClusterQueue
		localQueue     *kueue.LocalQueue
	)

	scaleFirstWorkerGroup := func(job *rayv1.RayJob, replicas int32) {
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).Should(gomega.Succeed())
			job.Spec.RayClusterSpec.WorkerGroupSpecs[0].Replicas = new(replicas)
			g.Expect(k8sClient.Update(ctx, job)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	}

	expectAdmittedWorkers := func(wl *kueue.Workload, count int32) {
		ginkgo.GinkgoHelper()
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).Should(gomega.Succeed())
			g.Expect(wl.Status.Admission.PodSetAssignments[workersPodSet].Count).Should(gomega.Equal(new(count)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	}

	ginkgo.BeforeAll(func() {
		features.SetFeatureGatesDuringTest(ginkgo.GinkgoTB(), map[featuregate.Feature]bool{
			features.ElasticJobsViaWorkloadSlices:                          true,
			features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp: true,
		})
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup())
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "rayjob-scale-up-")

		resourceFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, resourceFlavor)

		// "pods" is the constrained resource so every assertion is a plain pod count; cpu is
		// declared generously because Kueue only admits a Workload whose every requested resource
		// is covered by a resource group.
		clusterQueue = utiltestingapi.MakeClusterQueue("default").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
				Resource(corev1.ResourcePods, "4").
				Resource(corev1.ResourceCPU, "100").
				Obj()).
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

	ginkgo.It("Should partially admit a RayJob scale-up and create a scale-up probe", func() {
		// 1 head + 2 workers = 3 pods, fitting the 4-pod quota.
		testRayJob := testingrayjob.MakeJob("foo", ns.Name).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Annotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			Queue(localQueue.Name).
			WithSubmissionMode(rayv1.InteractiveMode).
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			Obj()
		testRayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].Replicas = new(int32(2))

		ginkgo.By("creating the rayjob with 2 worker replicas")
		util.MustCreate(ctx, k8sClient, testRayJob)
		setInitStatus(testRayJob.Name, ns.Name)

		ginkgo.By("admitting the rayjob's workload fully")
		initialSlice := &util.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		gomega.Expect(initialSlice.Spec.PodSets).Should(gomega.HaveLen(2))
		gomega.Expect(initialSlice.Spec.PodSets[workersPodSet].Count).Should(gomega.Equal(int32(2)))
		expectAdmittedWorkers(initialSlice, 2)

		// The full request is 1 + 5 = 6 pods against a 4-pod quota, so only 3 workers fit.
		ginkgo.By("scaling the worker group to 5 replicas")
		scaleFirstWorkerGroup(testRayJob, 5)

		ginkgo.By("a new workload slice replaces the admitted one, requesting the full 5 workers")
		partialSlice := util.ExpectNewWorkloadSlice(ctx, k8sClient, initialSlice)
		gomega.Expect(partialSlice.Spec.PodSets[workersPodSet].Count).Should(gomega.Equal(int32(5)))
		gomega.Expect(partialSlice.Spec.PodSets[workersPodSet].MinCount).Should(gomega.Equal(new(int32(3))))

		ginkgo.By("only 3 of the 5 requested workers fit: 1 head + 3 workers = the whole quota")
		expectAdmittedWorkers(partialSlice, 3)

		// This is what the RayCluster suite verifies too. RayJob overrides the workload slice name
		// extra part with its own generation-derived value, so if that discards the probe's extra
		// parameter the probe cannot get a distinct name and never appears.
		ginkgo.By("a scale-up probe workload is created for the full 5 workers")
		probe := util.ExpectNewWorkloadSlice(ctx, k8sClient, partialSlice)
		gomega.Expect(probe.Spec.PodSets[workersPodSet].Count).Should(gomega.Equal(int32(5)))

		ginkgo.By("the probe workload stays pending while the quota is exhausted")
		util.ExpectWorkloadsToBePending(ctx, k8sClient, probe)
	})
})
