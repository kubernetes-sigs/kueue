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

package raycluster

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

// KEP-12100: Partial Replica ScaleUp for ElasticJob.
var _ = ginkgo.Describe("RayCluster with partial replica scale-up for elastic jobs", ginkgo.Label("job:ray", "area:jobs"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns             *corev1.Namespace
		resourceFlavor *kueue.ResourceFlavor
		clusterQueue   *kueue.ClusterQueue
		localQueue     *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		gomega.Expect(utilfeature.DefaultMutableFeatureGate.SetFromMap(map[string]bool{
			string(features.ElasticJobsViaWorkloadSlices):                          true,
			string(features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp): true,
		})).Should(gomega.Succeed())
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup())
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "scale-up-")

		resourceFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, resourceFlavor)

		// Quota expressed purely in pod count: intially 7 total pods (1 head + N workers).
		clusterQueue = utiltestingapi.MakeClusterQueue("default").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).Resource(corev1.ResourcePods, "7").Obj()).
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

	ginkgo.It("Should partially admit a RayCluster scale-up when quota is insufficient, "+
		"then opportunistically admit the rest once quota frees up", framework.SlowSpec, func() {
		var (
			admittedSliceBeforeScaleUp *kueue.Workload
			partialSlice               *kueue.Workload
			probeWorkload              *kueue.Workload
		)

		testRayCluster := testingraycluster.MakeCluster("foo", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			Queue(localQueue.Name).
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			ScaleFirstWorkerGroup(5).
			Obj()

		// Step 0: create at 5 replicas. Total requested pods = 1 (head) + 5 (workers) = 6,
		// fits under the 7-pod quota. No partial admission at creation (Non-Goal).
		ginkgo.By("creating the raycluster with 5 worker replicas")
		util.MustCreate(ctx, k8sClient, testRayCluster)

		ginkgo.By("admitting the raycluster's workload fully")
		gomega.Eventually(func(g gomega.Gomega) {
			workloads := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(testRayCluster.Namespace))).Should(gomega.Succeed())
			g.Expect(workloads.Items).Should(gomega.HaveLen(1))
			admittedSliceBeforeScaleUp = &workloads.Items[0]
			g.Expect(admittedSliceBeforeScaleUp.Spec.PodSets).Should(gomega.HaveLen(2))
			g.Expect(admittedSliceBeforeScaleUp.Spec.PodSets[1].Count).Should(gomega.Equal(int32(5)))
			g.Expect(workload.IsAdmitted(admittedSliceBeforeScaleUp)).Should(gomega.BeTrue())
			g.Expect(admittedSliceBeforeScaleUp.Status.Admission.PodSetAssignments[1].Count).Should(gomega.Equal(int32(5)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("resource quota usage reflects the full 6 pods (1 head + 5 workers)")
		gomega.Eventually(func(g gomega.Gomega) {
			cq := &kueue.ClusterQueue{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), cq)).Should(gomega.Succeed())
			g.Expect(cq.Status.FlavorsUsage).Should(gomega.HaveLen(1))
			g.Expect(cq.Status.FlavorsUsage[0].Resources[0].Total).Should(gomega.Equal(resource.MustParse("6")))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// Step 1: scale-up trigger. In production KubeRay's controller updates .replicas
		// envtest runs no real KubeRay operator, so the test plays that role directly,
		// exactly like the existing scale-up/scale-down test does for testRayCluster.Spec.WorkerGroupSpecs[0].Replicas.
		ginkgo.By("scaling the worker group to 10 replicas (KubeRay controller stand-in)")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testRayCluster), testRayCluster)).Should(gomega.Succeed())
			testRayCluster.Spec.WorkerGroupSpecs[0].Replicas = ptr.To(int32(10))
			g.Expect(k8sClient.Update(ctx, testRayCluster)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("a new workload slice replaces the admitted one, requesting the full 10 workers")
		gomega.Eventually(func(g gomega.Gomega) {
			workloads := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(testRayCluster.Namespace))).Should(gomega.Succeed())

			for i := range workloads.Items {
				wl := &workloads.Items[i]
				if wl.Name == admittedSliceBeforeScaleUp.Name {
					continue
				}
				if wl.Spec.PodSets[1].Count == 10 {
					partialSlice = wl
				}
			}
			g.Expect(partialSlice).ShouldNot(gomega.BeNil())
			g.Expect(partialSlice.Spec.PodSets[1].Count).Should(gomega.Equal(int32(10)))
			// MinCount = admitted.count of the previously-admitted slice.
			g.Expect(partialSlice.Spec.PodSets[1].MinCount).Should(gomega.Equal(ptr.To(int32(5))))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("the new slice is admitted partially: only 6 of the 10 requested workers fit")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(partialSlice), partialSlice)).Should(gomega.Succeed())
			g.Expect(workload.IsAdmitted(partialSlice)).Should(gomega.BeTrue())
			g.Expect(partialSlice.Status.Admission.PodSetAssignments[1].Count).Should(gomega.Equal(int32(6)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("the old (pre-scale-up) slice is finished")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(admittedSliceBeforeScaleUp), admittedSliceBeforeScaleUp)).Should(gomega.Succeed())
			g.Expect(workloadfinish.IsFinished(admittedSliceBeforeScaleUp)).Should(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("resource quota usage reflects exactly 7 pods (1 head + 6 workers), full utilization")
		gomega.Eventually(func(g gomega.Gomega) {
			cq := &kueue.ClusterQueue{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), cq)).Should(gomega.Succeed())
			g.Expect(cq.Status.FlavorsUsage[0].Resources[0].Total).Should(gomega.Equal(resource.MustParse("7")))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("exactly 1 additional worker pod is ungated, bringing the running total to 6")
		gomega.Eventually(func(g gomega.Gomega) {
			pods := &corev1.PodList{}
			g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).Should(gomega.Succeed())
			var ungated, gated int
			for i := range pods.Items {
				p := &pods.Items[i]
				if len(p.Spec.SchedulingGates) == 0 {
					ungated++
				} else {
					gated++
				}
			}
			// 1 head + 6 ungated workers = 7 ungated total; 4 workers remain gated (10 - 6).
			g.Expect(ungated).Should(gomega.Equal(7))
			g.Expect(gated).Should(gomega.Equal(4))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// TODO(KEP-12100): tighten this to an exact-name assertion once the
		// probe-workload naming constant/extended helper lands.
		ginkgo.By("a full-scaleup-probe workload is created, pending, requesting the full 10 workers")
		gomega.Eventually(func(g gomega.Gomega) {
			workloads := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(testRayCluster.Namespace))).Should(gomega.Succeed())

			for i := range workloads.Items {
				wl := &workloads.Items[i]
				if wl.Name == admittedSliceBeforeScaleUp.Name || wl.Name == partialSlice.Name {
					continue
				}
				probeWorkload = wl
			}
			g.Expect(probeWorkload).ShouldNot(gomega.BeNil())
			g.Expect(probeWorkload.Spec.PodSets[1].Count).Should(gomega.Equal(int32(10)))
			util.ExpectWorkloadsToBePending(ctx, k8sClient, probeWorkload)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// Step 3: opportunistic admission once quota frees up. 
		// Chosen as a ClusterQueue quota edit (not a sibling workload finishing) to mirror
		// the KEP's own Step 3 wording exactly and keep this test to a single
		// RayCluster with no second workload's scheduling nondeterminism.
		ginkgo.By("raising the ClusterQueue's pod quota from 7 to 15")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).Should(gomega.Succeed())
			clusterQueue.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota = resource.MustParse("15")
			g.Expect(k8sClient.Update(ctx, clusterQueue)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("the probe workload is admitted with the full 10 workers")
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, probeWorkload)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(probeWorkload), probeWorkload)).Should(gomega.Succeed())
			g.Expect(probeWorkload.Status.Admission.PodSetAssignments[1].Count).Should(gomega.Equal(int32(10)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("the partially-admitted slice is finished as WorkloadSliceReplaced")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(partialSlice), partialSlice)).Should(gomega.Succeed())
			g.Expect(workloadfinish.IsFinished(partialSlice)).Should(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("all 10 worker pods are ungated/running, quota usage reflects the full 11 pods")
		gomega.Eventually(func(g gomega.Gomega) {
			pods := &corev1.PodList{}
			g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).Should(gomega.Succeed())
			var ungated int
			for i := range pods.Items {
				if len(pods.Items[i].Spec.SchedulingGates) == 0 {
					ungated++
				}
			}
			g.Expect(ungated).Should(gomega.Equal(11))

			cq := &kueue.ClusterQueue{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), cq)).Should(gomega.Succeed())
			g.Expect(cq.Status.FlavorsUsage[0].Resources[0].Total).Should(gomega.Equal(resource.MustParse("11")))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
})
