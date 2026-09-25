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
	"slices"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util"
)

// The RayCluster workload has two PodSets: the head, which cannot be shrunk, and the single
// worker group, which is the one partial scale-up reduces.
const (
	workersPodSetIdx = 1
	workersGroupName = "workers-group-0"
)

// KEP-12100: Partial Replica ScaleUp for ElasticJob.
var _ = ginkgo.Describe("RayCluster with partial replica scale-up for elastic jobs", ginkgo.Label("job:ray", "area:jobs"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns             *corev1.Namespace
		resourceFlavor *kueue.ResourceFlavor
		clusterQueue   *kueue.ClusterQueue
		localQueue     *kueue.LocalQueue
	)

	// expectPodsUsage asserts the ClusterQueue's usage of "pods", the only constrained resource in
	// these specs. Looked up by resource name rather than by index, since the resource group
	// declares more than one resource.
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
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	}
	// scaleFirstWorkerGroup emulates the KubeRay controller, which is what updates .replicas in
	// production. envtest runs no KubeRay operator, so the spec drives the scale event itself,
	// exactly like the existing scale-up/scale-down specs in this package do.
	scaleFirstWorkerGroup := func(rayCluster *rayv1.RayCluster, replicas int32) {
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayCluster), rayCluster)).Should(gomega.Succeed())
			rayCluster.Spec.WorkerGroupSpecs[0].Replicas = new(replicas)
			g.Expect(k8sClient.Update(ctx, rayCluster)).Should(gomega.Succeed())
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
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "scale-up-")

		resourceFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, resourceFlavor)

		// "pods" is the constrained resource, so every assertion is a plain pod count. The pods
		// carry CPU requests too, and Kueue only admits a Workload whose every requested resource
		// is covered by a resource group, so cpu is declared as well - deliberately generous, so
		// that "pods" stays the binding constraint.
		clusterQueue = utiltestingapi.MakeClusterQueue("default").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
				Resource(corev1.ResourcePods, "7").
				Resource(corev1.ResourceCPU, "100").
				Obj()).
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

	ginkgo.It("Should partially admit a RayCluster scale-up when quota is insufficient", func() {
		testRayCluster := testingraycluster.MakeCluster("foo", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			Queue(localQueue.Name).
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			FirstWorkerGroupReplicas(5, 10, 10).
			Obj()

		// -------------------------------------------------------------------------------------
		// KEP Step 0: job creation at 5 replicas. Total requested pods = 1 (head) + 5 (workers)
		// = 6, which fits under the 7-pod quota, so the initial workload is admitted in full -
		// initial creation is never partially admitted (Non-Goal).
		// -------------------------------------------------------------------------------------
		ginkgo.By("creating the raycluster with 5 worker replicas")
		util.MustCreate(ctx, k8sClient, testRayCluster)

		ginkgo.By("admitting the raycluster's workload fully")
		initialSlice := &util.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		gomega.Expect(initialSlice.Spec.PodSets).Should(gomega.HaveLen(2))
		gomega.Expect(initialSlice.Spec.PodSets[workersPodSetIdx].Count).Should(gomega.Equal(int32(5)))
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, initialSlice, workersGroupName, 5)

		ginkgo.By("quota usage reflects the full 6 pods (1 head + 5 workers)")
		expectPodsUsage(6)

		// -------------------------------------------------------------------------------------
		// KEP Step 1: scale up from 5 to 10. The full request (1 + 10 = 11 pods) exceeds the
		// 7-pod quota, so instead of being rejected outright the scale-up is admitted partially,
		// up to the available quota (KEP wl-B).
		// -------------------------------------------------------------------------------------
		ginkgo.By("scaling the worker group to 10 replicas")
		scaleFirstWorkerGroup(testRayCluster, 10)

		ginkgo.By("a new workload slice replaces the admitted one, requesting the full 10 workers")
		partialSlice := util.ExpectNewWorkloadSlice(ctx, k8sClient, initialSlice)
		gomega.Expect(partialSlice.Spec.PodSets[workersPodSetIdx].Count).Should(gomega.Equal(int32(10)))
		// MinCount is the baseline: the previously-admitted worker count, which the scale-up may
		// not go below. That the scale-up has to grow by at least one pod somewhere to be worth
		// admitting is enforced by the scheduler across the whole Workload, not per PodSet.
		gomega.Expect(partialSlice.Spec.PodSets[workersPodSetIdx].MinCount).Should(gomega.Equal(new(int32(5))))

		ginkgo.By("only 6 of the 10 requested workers fit: 1 head + 6 workers = the whole quota")
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, partialSlice, workersGroupName, 6)
		expectPodsUsage(7)

		ginkgo.By("the old (pre-scale-up) slice is finished")
		util.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKeyFromObject(initialSlice))

		// Still Step 1: alongside the partially-admitted slice, a probe workload is created for
		// the full request. It is what lets the remaining replicas be admitted later, with no
		// further scale event (KEP wl-C). Looking it up as the replacement of the partial slice
		// also covers the linkage it depends on: without that annotation the probe would be
		// treated as an out-of-sync slice and finished before it could ever be admitted.
		ginkgo.By("a scale-up probe workload is created for the full 10 workers")
		probe := util.ExpectNewWorkloadSlice(ctx, k8sClient, partialSlice)
		gomega.Expect(probe.Spec.PodSets[workersPodSetIdx].Count).Should(gomega.Equal(int32(10)))

		ginkgo.By("the probe workload stays pending while the quota is exhausted")
		util.ExpectWorkloadsToBePending(ctx, k8sClient, probe)

		// -------------------------------------------------------------------------------------
		// KEP Step 2: a further scale-up arrives while the probe is still pending. The probe is
		// updated in place to carry the new target, rather than a second probe being created, so
		// the job never accumulates a chain of never-admitted workloads. The partially-admitted
		// slice is untouched, and the probe stays pending because the quota is still exhausted.
		// -------------------------------------------------------------------------------------
		probeKey := client.ObjectKeyFromObject(probe)

		ginkgo.By("scaling the worker group to 12 replicas while the probe is pending")
		scaleFirstWorkerGroup(testRayCluster, 12)

		ginkgo.By("the existing probe workload is updated in place to the full 12 workers")
		gomega.Eventually(func(g gomega.Gomega) {
			updatedProbe := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, probeKey, updatedProbe)).Should(gomega.Succeed())
			g.Expect(updatedProbe.Spec.PodSets[workersPodSetIdx].Count).Should(gomega.Equal(int32(12)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("no additional workload is created for the second scale-up")
		gomega.Consistently(func(g gomega.Gomega) {
			list := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, list, client.InNamespace(ns.Name))).Should(gomega.Succeed())
			// The finished pre-scale-up slice, the partially-admitted slice, and the probe.
			g.Expect(list.Items).Should(gomega.HaveLen(3))
		}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())

		ginkgo.By("the partially-admitted slice still holds its 6 admitted workers")
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, partialSlice, workersGroupName, 6)

		ginkgo.By("the probe workload is still pending")
		util.ExpectWorkloadsToBePendingByKeys(ctx, k8sClient, probeKey)

		// -------------------------------------------------------------------------------------
		// KEP Step 3: capacity appears and the probe is admitted opportunistically, replacing
		// the partially-admitted slice. Modelled as a ClusterQueue quota increase, mirroring the
		// KEP's own wording, rather than a sibling workload finishing - that is a separate claim
		// and belongs in its own spec.
		// -------------------------------------------------------------------------------------
		ginkgo.By("raising the ClusterQueue's pod quota from 7 to 15")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).Should(gomega.Succeed())
			util.SetResourceNominalQuota(clusterQueue, corev1.ResourcePods, "15")
			g.Expect(k8sClient.Update(ctx, clusterQueue)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("the probe workload is admitted with the full 12 workers")
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, probe, workersGroupName, 12)

		ginkgo.By("the partially-admitted slice is finished, having been replaced by the probe")
		util.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKeyFromObject(partialSlice))

		ginkgo.By("quota usage reflects the full 13 pods (1 head + 12 workers)")
		expectPodsUsage(13)

		// TODO: 12100
		// KEP Step 4 (scale down, e.g. 12 -> 8, where spec.podSets.count drops while
		// status.admission.count stays put) is not covered yet, and neither are the multi-PodSet
		// order-based scenarios A-D, which need the order-based reducer and its give-back phase.
	})

	ginkgo.It("Should give the spare capacity to the earlier worker group rather than spread it", func() {
		// Two worker groups drawing on the same quota, so they compete. Both want two more
		// workers and only two pods are spare, which forces a choice: the earlier group is the
		// higher-priority one and should take both, rather than each group growing by one.
		const groupA, groupB = "workers-a", "workers-b"
		testRayCluster := testingraycluster.MakeCluster("foo", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			Queue(localQueue.Name).
			WithWorkerGroups(
				*testingraycluster.MakeWorkerGroup(groupA, 2).Request(corev1.ResourceCPU, "1").Obj(),
				*testingraycluster.MakeWorkerGroup(groupB, 2).Request(corev1.ResourceCPU, "1").Obj(),
			).
			Obj()

		ginkgo.By("admitting the raycluster at 2 workers in each group")
		util.MustCreate(ctx, k8sClient, testRayCluster)
		initialSlice := &util.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, initialSlice, groupA, 2)
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, initialSlice, groupB, 2)

		ginkgo.By("quota usage reflects the full 5 pods (1 head + 2 + 2 workers)")
		expectPodsUsage(5)

		// 1 head + 4 + 4 = 9 pods against the 7-pod quota, so only two of the four requested
		// workers fit.
		ginkgo.By("scaling both worker groups to 4 replicas")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testRayCluster), testRayCluster)).Should(gomega.Succeed())
			g.Expect(testRayCluster.Spec.WorkerGroupSpecs).Should(gomega.HaveLen(2))
			testRayCluster.Spec.WorkerGroupSpecs[0].Replicas = new(int32(4))
			testRayCluster.Spec.WorkerGroupSpecs[1].Replicas = new(int32(4))
			g.Expect(k8sClient.Update(ctx, testRayCluster)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("both spare pods go to the earlier group, the later one stays at its baseline")
		scaleUpSlice := util.ExpectNewWorkloadSlice(ctx, k8sClient, initialSlice)
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, scaleUpSlice, groupA, 4)
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, scaleUpSlice, groupB, 2)
		// Usage settling at the full 7-pod quota also says the pre-scale-up slice released its
		// own 5 pods: were both live, usage would read 12, which the quota cannot hold.
		expectPodsUsage(7)
	})

	ginkgo.It("Should partially admit a RayCluster scale-up by preempting a lower-priority workload", func() {
		// The victim occupies 4 of the 7 pods and is lower priority than the RayCluster, whose
		// workload has the default priority of 0.
		ginkgo.By("admitting a lower-priority workload that occupies 4 pods")
		victim := utiltestingapi.MakeWorkload("victim", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Priority(-1).
			PodSets(*utiltestingapi.MakePodSet("main", 4).Request(corev1.ResourceCPU, "1").Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, victim)
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, victim)

		// 1 head + 2 workers = 3 pods, which together with the victim's 4 exactly fills the
		// 7-pod quota.
		ginkgo.By("admitting a RayCluster with 2 worker replicas, filling the quota")
		testRayCluster := testingraycluster.MakeCluster("foo", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			Queue(localQueue.Name).
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			ScaleFirstWorkerGroup(2).
			Obj()
		util.MustCreate(ctx, k8sClient, testRayCluster)

		workloads := util.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 2)
		initialSliceIdx := slices.IndexFunc(workloads, func(wl kueue.Workload) bool {
			return wl.Name != victim.Name
		})
		gomega.Expect(initialSliceIdx).ShouldNot(gomega.Equal(-1), "no RayCluster slice alongside the victim")
		initialSlice := &workloads[initialSliceIdx]
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, initialSlice, workersGroupName, 2)
		expectPodsUsage(7)

		// The scale-up wants 1 + 10 = 11 pods, which does not fit in the 7-pod quota even after
		// reclaiming everything below it, so it cannot be admitted in full and has to be cut
		// down. The largest count that does fit requires the victim's 4 pods, so this exercises
		// preemption and partial admission together: had the full request been satisfiable by
		// preemption alone, the scheduler would have admitted it whole and never reduced it.
		ginkgo.By("scaling the worker group to 10 replicas")
		scaleFirstWorkerGroup(testRayCluster, 10)

		ginkgo.By("the lower-priority workload is preempted")
		util.ExpectWorkloadsToBePreempted(ctx, k8sClient, victim)

		// Eviction only sets the condition; releasing the quota is the job controller's job when
		// it suspends the job. envtest runs no controller for a bare Workload, so the spec has to
		// complete the eviction itself - otherwise the victim keeps its quota, the scheduler
		// waits on a preemption expectation that can never clear, and nothing is ever admitted.
		ginkgo.By("completing the victim's eviction so its quota is released")
		util.FinishEvictionForWorkloads(ctx, k8sClient, victim)

		ginkgo.By("the scale-up is admitted partially, using the reclaimed quota")
		partialSlice := util.ExpectNewWorkloadSlice(ctx, k8sClient, initialSlice)
		gomega.Expect(partialSlice.Spec.PodSets[workersPodSetIdx].Count).Should(gomega.Equal(int32(10)))

		// The whole 7-pod quota is now the RayCluster's: 1 head + 6 workers. MinCount is asserted
		// in the spec above; this one is about the preemption interaction.
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, partialSlice, workersGroupName, 6)
		expectPodsUsage(7)
	})
	ginkgo.It("Should re-admit the partially-admitted slice after a ClusterQueue drain", func() {
		// Regression coverage for https://github.com/kubernetes-sigs/kueue/issues/15399.
		// Draining finishes the evicted slice but keeps its pending scale-up probe, which then
		// re-admits itself at its own floor via the scheduler's mustGrow-aware reducer (#15776).
		testRayCluster := testingraycluster.MakeCluster("foo", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			Queue(localQueue.Name).
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			FirstWorkerGroupReplicas(5, 10, 10).
			Obj()

		ginkgo.By("admitting the raycluster at 5 workers")
		util.MustCreate(ctx, k8sClient, testRayCluster)
		initialSlice := &util.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, initialSlice, workersGroupName, 5)

		ginkgo.By("scaling to 10 workers, of which only 6 fit")
		scaleFirstWorkerGroup(testRayCluster, 10)
		partialSlice := util.ExpectNewWorkloadSlice(ctx, k8sClient, initialSlice)
		gomega.Expect(partialSlice.Spec.PodSets[workersPodSetIdx].MinCount).Should(gomega.Equal(new(int32(5))))
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, partialSlice, workersGroupName, 6)
		expectPodsUsage(7)

		ginkgo.By("the scale-up probe for the full request is pending")
		probe := util.ExpectNewWorkloadSlice(ctx, k8sClient, partialSlice)
		gomega.Expect(probe.Spec.PodSets[workersPodSetIdx].MinCount).Should(gomega.Equal(new(int32(5))))
		util.ExpectWorkloadsToBePending(ctx, k8sClient, probe)

		ginkgo.By("draining the ClusterQueue to evict the partially-admitted slice")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).Should(gomega.Succeed())
			clusterQueue.Spec.StopPolicy = new(kueue.HoldAndDrain)
			g.Expect(k8sClient.Update(ctx, clusterQueue)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		gomega.Eventually(func(g gomega.Gomega) {
			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(partialSlice), wl)).Should(gomega.Succeed())
			g.Expect(wl.Status.Conditions).Should(utiltesting.HaveConditionStatusTrueAndReason(
				kueue.WorkloadEvicted, kueue.WorkloadEvictedByClusterQueueStopped))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("the evicted slice is finished; the probe survives")
		util.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKeyFromObject(partialSlice))
		expectPodsUsage(0)

		ginkgo.By("resuming the ClusterQueue with the quota unchanged")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).Should(gomega.Succeed())
			clusterQueue.Spec.StopPolicy = new(kueue.None)
			g.Expect(k8sClient.Update(ctx, clusterQueue)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("the probe itself is admitted at 6 workers, with no change to the quota")
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, probe, workersGroupName, 6)
		expectPodsUsage(7)

		ginkgo.By("a new scale-up probe is created for the full request again")
		newProbe := util.ExpectNewWorkloadSlice(ctx, k8sClient, probe)
		gomega.Expect(newProbe.Spec.PodSets[workersPodSetIdx].MinCount).Should(gomega.Equal(new(int32(5))))
		util.ExpectWorkloadsToBePending(ctx, k8sClient, newProbe)

		ginkgo.By("growing the quota to 10 pods for the new probe to be admitted")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).Should(gomega.Succeed())
			clusterQueue.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota = resource.MustParse("10")
			g.Expect(k8sClient.Update(ctx, clusterQueue)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("the new probe is admitted at 9 workers, as much of the scale-up as now fits")
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, newProbe, workersGroupName, 9)
		expectPodsUsage(10)
	})

	ginkgo.It("Should re-admit the partially-admitted slice after being preempted", func() {
		// Same regression as above, but evicting through preemption instead of a drain.
		testRayCluster := testingraycluster.MakeCluster("foo", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			Queue(localQueue.Name).
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			FirstWorkerGroupReplicas(5, 10, 10).
			Obj()

		ginkgo.By("admitting the raycluster at 5 workers")
		util.MustCreate(ctx, k8sClient, testRayCluster)
		initialSlice := &util.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, initialSlice, workersGroupName, 5)

		ginkgo.By("scaling to 10 workers, of which only 6 fit")
		scaleFirstWorkerGroup(testRayCluster, 10)
		partialSlice := util.ExpectNewWorkloadSlice(ctx, k8sClient, initialSlice)
		gomega.Expect(partialSlice.Spec.PodSets[workersPodSetIdx].MinCount).Should(gomega.Equal(new(int32(5))))
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, partialSlice, workersGroupName, 6)
		expectPodsUsage(7)

		ginkgo.By("the scale-up probe for the full request is pending")
		probe := util.ExpectNewWorkloadSlice(ctx, k8sClient, partialSlice)
		gomega.Expect(probe.Spec.PodSets[workersPodSetIdx].MinCount).Should(gomega.Equal(new(int32(5))))
		util.ExpectWorkloadsToBePending(ctx, k8sClient, probe)

		// It's a real job-backed workload, so its own RayCluster controller drives eviction to
		// completion; unlike a bare test workload, no manual FinishEvictionForWorkloads is needed.
		ginkgo.By("a higher-priority workload preempts the partially-admitted slice")
		preemptor := utiltestingapi.MakeWorkload("preemptor", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Priority(1).
			PodSets(*utiltestingapi.MakePodSet("main", 7).Request(corev1.ResourceCPU, "1").Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, preemptor)

		gomega.Eventually(func(g gomega.Gomega) {
			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(partialSlice), wl)).Should(gomega.Succeed())
			g.Expect(wl.Status.Conditions).Should(utiltesting.HaveConditionStatusTrueAndReason(
				kueue.WorkloadEvicted, kueue.WorkloadEvictedByPreemption))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("the preemptor is admitted with the reclaimed quota")
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, preemptor)
		expectPodsUsage(7)

		ginkgo.By("the preempted slice is finished; the probe survives, still pending")
		util.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKeyFromObject(partialSlice))
		util.ExpectWorkloadsToBePending(ctx, k8sClient, probe)

		ginkgo.By("the preemptor finishes, releasing its quota")
		util.FinishWorkloads(ctx, k8sClient, preemptor)

		ginkgo.By("the probe itself is admitted at 6 workers, with no change to the quota")
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, probe, workersGroupName, 6)
		expectPodsUsage(7)

		ginkgo.By("a new scale-up probe is created for the full request again")
		newProbe := util.ExpectNewWorkloadSlice(ctx, k8sClient, probe)
		gomega.Expect(newProbe.Spec.PodSets[workersPodSetIdx].MinCount).Should(gomega.Equal(new(int32(5))))
		util.ExpectWorkloadsToBePending(ctx, k8sClient, newProbe)
	})

	ginkgo.It("Should stay pending, not degrade, if a ClusterQueue drain also shrinks the quota below the chain's origin", func() {
		// Known gap, not a regression: the probe's floor is copied forward from its
		// predecessor's own recorded floor at creation (see the previous spec), tracing back
		// through the whole chain to the count the job was first admitted at - here, 5. Once
		// quota shrinks below even that, the probe still can't recover. Reaching lower would
		// mean recording a floor below what the job has ever actually run at, e.g. after an
		// explicit scale-down; nothing in the codebase weaves that into the chain's floor today.
		testRayCluster := testingraycluster.MakeCluster("foo", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			Queue(localQueue.Name).
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			FirstWorkerGroupReplicas(5, 10, 10).
			Obj()

		ginkgo.By("admitting the raycluster at 5 workers")
		util.MustCreate(ctx, k8sClient, testRayCluster)
		initialSlice := &util.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, initialSlice, workersGroupName, 5)

		ginkgo.By("scaling to 10 workers, of which only 6 fit")
		scaleFirstWorkerGroup(testRayCluster, 10)
		partialSlice := util.ExpectNewWorkloadSlice(ctx, k8sClient, initialSlice)
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, partialSlice, workersGroupName, 6)
		expectPodsUsage(7)

		ginkgo.By("the scale-up probe for the full request is pending")
		probe := util.ExpectNewWorkloadSlice(ctx, k8sClient, partialSlice)
		gomega.Expect(probe.Spec.PodSets[workersPodSetIdx].MinCount).Should(gomega.Equal(new(int32(5))))
		util.ExpectWorkloadsToBePending(ctx, k8sClient, probe)

		ginkgo.By("draining the ClusterQueue to evict the partially-admitted slice")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).Should(gomega.Succeed())
			clusterQueue.Spec.StopPolicy = new(kueue.HoldAndDrain)
			g.Expect(k8sClient.Update(ctx, clusterQueue)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		gomega.Eventually(func(g gomega.Gomega) {
			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(partialSlice), wl)).Should(gomega.Succeed())
			g.Expect(wl.Status.Conditions).Should(utiltesting.HaveConditionStatusTrueAndReason(
				kueue.WorkloadEvicted, kueue.WorkloadEvictedByClusterQueueStopped))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("the evicted slice is finished; the probe survives, its floor already 5")
		util.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKeyFromObject(partialSlice))
		expectPodsUsage(0)

		ginkgo.By("resuming the ClusterQueue with the quota shrunk below the chain's origin")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).Should(gomega.Succeed())
			clusterQueue.Spec.StopPolicy = new(kueue.None)
			clusterQueue.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota = resource.MustParse("4")
			g.Expect(k8sClient.Update(ctx, clusterQueue)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("the probe stays pending: 3 workers would fit, but its floor demands 5")
		util.ExpectWorkloadsToBePending(ctx, k8sClient, probe)
		expectPodsUsage(0)

		ginkgo.By("growing the quota back to the old baseline lets the probe recover")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).Should(gomega.Succeed())
			clusterQueue.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota = resource.MustParse("7")
			g.Expect(k8sClient.Update(ctx, clusterQueue)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, probe, workersGroupName, 6)
		expectPodsUsage(7)
	})

	ginkgo.It("Should recover at a worker count the job already ran at, even below the probe's predecessor", func() {
		// The probe's floor (5) is copied forward from its predecessor's own recorded floor at
		// creation - not recomputed from the predecessor's live grant (6) - so it already traces
		// back to the job's original, pre-scale-up count before eviction ever happens (see
		// prepareWorkloadSliceForScaleUp). So once quota shrinks to fit only that original
		// count, the probe recovers there, even though its predecessor only ever ran at 6.
		testRayCluster := testingraycluster.MakeCluster("foo", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			Queue(localQueue.Name).
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			FirstWorkerGroupReplicas(5, 10, 10).
			Obj()

		ginkgo.By("admitting the raycluster at 5 workers")
		util.MustCreate(ctx, k8sClient, testRayCluster)
		initialSlice := &util.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, initialSlice, workersGroupName, 5)

		ginkgo.By("scaling to 10 workers, of which only 6 fit")
		scaleFirstWorkerGroup(testRayCluster, 10)
		partialSlice := util.ExpectNewWorkloadSlice(ctx, k8sClient, initialSlice)
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, partialSlice, workersGroupName, 6)
		expectPodsUsage(7)

		ginkgo.By("the scale-up probe for the full request is pending, floored at 5 workers")
		probe := util.ExpectNewWorkloadSlice(ctx, k8sClient, partialSlice)
		gomega.Expect(probe.Spec.PodSets[workersPodSetIdx].MinCount).Should(gomega.Equal(new(int32(5))))
		util.ExpectWorkloadsToBePending(ctx, k8sClient, probe)

		ginkgo.By("draining the ClusterQueue to evict the partially-admitted slice")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).Should(gomega.Succeed())
			clusterQueue.Spec.StopPolicy = new(kueue.HoldAndDrain)
			g.Expect(k8sClient.Update(ctx, clusterQueue)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		gomega.Eventually(func(g gomega.Gomega) {
			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(partialSlice), wl)).Should(gomega.Succeed())
			g.Expect(wl.Status.Conditions).Should(utiltesting.HaveConditionStatusTrueAndReason(
				kueue.WorkloadEvicted, kueue.WorkloadEvictedByClusterQueueStopped))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("the evicted slice is finished; the probe survives, still pending")
		util.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKeyFromObject(partialSlice))
		expectPodsUsage(0)

		// 6 pods is exactly 1 head + 5 workers - the level the job was admitted at, and ran at
		// successfully, before it ever scaled up. That's exactly the probe's frozen floor,
		// while its predecessor's own eventual grant (6 workers, 7 pods) is unreachable here.
		ginkgo.By("resuming the ClusterQueue with just enough quota for the job's original count")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).Should(gomega.Succeed())
			clusterQueue.Spec.StopPolicy = new(kueue.None)
			clusterQueue.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota = resource.MustParse("6")
			g.Expect(k8sClient.Update(ctx, clusterQueue)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("the job recovers at the 5 workers it already proved it could run")
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, probe, workersGroupName, 5)
		expectPodsUsage(6)
	})
})
