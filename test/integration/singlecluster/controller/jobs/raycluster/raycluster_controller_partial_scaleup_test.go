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
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
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
			ScaleFirstWorkerGroup(5).
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
		// MinCount is the previously-admitted worker count plus one, i.e. the scale-up must grow
		// by at least one pod to be worth admitting at all.
		gomega.Expect(partialSlice.Spec.PodSets[workersPodSetIdx].MinCount).Should(gomega.Equal(new(int32(6))))

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
		//
		// Follows the batch/Job elastic spec "Should resume with the same slice chain after
		// eviction by a stopped ClusterQueue", but for a job that was in the middle of a partial
		// scale-up. Draining evicts the partially-admitted slice while the scale-up probe -
		// created for the full request, and pending because its floor already exceeded the quota
		// - is the only other slice in the chain.
		//
		// The probe's floor is frozen at the count admitted when it was created, so it can never
		// carry the job's re-admission on its own: nothing shrinks that floor back down to fit.
		// Recovery instead has to come from the partially-admitted slice resuming - the quota that
		// held it before the drain is still there once the ClusterQueue is resumed - keeping the
		// probe's floor meaningful again. Only reaching the probe's own (higher) floor still needs
		// more quota, which is exercised at the end of this spec.
		testRayCluster := testingraycluster.MakeCluster("foo", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			Queue(localQueue.Name).
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			ScaleFirstWorkerGroup(5).
			Obj()

		ginkgo.By("admitting the raycluster at 5 workers")
		util.MustCreate(ctx, k8sClient, testRayCluster)
		initialSlice := &util.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, initialSlice, workersGroupName, 5)

		ginkgo.By("scaling to 10 workers, of which only 6 fit")
		scaleFirstWorkerGroup(testRayCluster, 10)
		partialSlice := util.ExpectNewWorkloadSlice(ctx, k8sClient, initialSlice)
		gomega.Expect(partialSlice.Spec.PodSets[workersPodSetIdx].MinCount).Should(gomega.Equal(new(int32(6))))
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, partialSlice, workersGroupName, 6)
		expectPodsUsage(7)

		// MinCount here is 7 - the 6 workers just admitted plus one - which is above what the
		// 7-pod quota can hold, so the probe stays pending.
		ginkgo.By("the scale-up probe for the full request is pending")
		probe := util.ExpectNewWorkloadSlice(ctx, k8sClient, partialSlice)
		gomega.Expect(probe.Spec.PodSets[workersPodSetIdx].MinCount).Should(gomega.Equal(new(int32(7))))
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
		expectPodsUsage(0)

		ginkgo.By("resuming the ClusterQueue with the quota unchanged")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).Should(gomega.Succeed())
			clusterQueue.Spec.StopPolicy = new(kueue.None)
			g.Expect(k8sClient.Update(ctx, clusterQueue)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// A finished, evicted slice keeps its stale Admitted=True condition and Admission
		// counts (Finish only sets the Finished condition), so checking admitted-count alone
		// would pass spuriously against a dead slice. Rule that out explicitly first.
		ginkgo.By("the partially-admitted slice was not finished by the drain")
		gomega.Eventually(func(g gomega.Gomega) {
			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(partialSlice), wl)).Should(gomega.Succeed())
			g.Expect(workloadfinish.IsFinished(wl)).Should(gomega.BeFalse())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// The unchanged 7-pod quota is exactly what the partially-admitted slice held before the
		// drain, so it is re-admitted at the same 6 workers without any external help. The probe's
		// floor of 1 head + 7 workers is still above the 7-pod quota, so it stays pending, but its
		// floor is meaningful again now that the slice it was computed against is back.
		ginkgo.By("the partially-admitted slice resumes at 6 workers, with no change to the quota")
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, partialSlice, workersGroupName, 6)
		expectPodsUsage(7)
		util.ExpectWorkloadsToBePending(ctx, k8sClient, probe)

		// 10 pods covers the probe's floor of 1 head + 7 workers, but not its full request of 1
		// head + 10 workers, so the probe has to be partially admitted in its turn.
		ginkgo.By("growing the quota to 10 pods for the probe to be admitted")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).Should(gomega.Succeed())
			clusterQueue.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota = resource.MustParse("10")
			g.Expect(k8sClient.Update(ctx, clusterQueue)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("the probe is admitted at 9 workers, as much of the scale-up as now fits")
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, probe, workersGroupName, 9)
		expectPodsUsage(10)
	})

	ginkgo.It("Should re-admit the partially-admitted slice after being preempted", func() {
		// Same regression as above (https://github.com/kubernetes-sigs/kueue/issues/15399), but
		// evicting the partially-admitted slice through preemption instead of a ClusterQueue
		// drain. normalizeActiveSlices finishes an evicted slice unconditionally regardless of
		// why it was evicted, so the failure mode does not depend on the eviction cause - only on
		// a probe surviving whatever evicted it.
		testRayCluster := testingraycluster.MakeCluster("foo", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			Queue(localQueue.Name).
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			ScaleFirstWorkerGroup(5).
			Obj()

		ginkgo.By("admitting the raycluster at 5 workers")
		util.MustCreate(ctx, k8sClient, testRayCluster)
		initialSlice := &util.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, initialSlice, workersGroupName, 5)

		ginkgo.By("scaling to 10 workers, of which only 6 fit")
		scaleFirstWorkerGroup(testRayCluster, 10)
		partialSlice := util.ExpectNewWorkloadSlice(ctx, k8sClient, initialSlice)
		gomega.Expect(partialSlice.Spec.PodSets[workersPodSetIdx].MinCount).Should(gomega.Equal(new(int32(6))))
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, partialSlice, workersGroupName, 6)
		expectPodsUsage(7)

		// MinCount here is 7 - the 6 workers just admitted plus one - which is above what the
		// 7-pod quota can hold, so the probe stays pending.
		ginkgo.By("the scale-up probe for the full request is pending")
		probe := util.ExpectNewWorkloadSlice(ctx, k8sClient, partialSlice)
		gomega.Expect(probe.Spec.PodSets[workersPodSetIdx].MinCount).Should(gomega.Equal(new(int32(7))))
		util.ExpectWorkloadsToBePending(ctx, k8sClient, probe)

		// The RayCluster's workload has the default priority of 0, so a priority-1 workload
		// requesting the whole 7-pod quota reclaims all of it: the partially-admitted slice is
		// the only thing to preempt. It is a real job-backed workload, so - unlike a bare test
		// workload - its own RayCluster controller drives its eviction to completion; no manual
		// FinishEvictionForWorkloads step is needed here.
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

		// A finished, evicted slice keeps its stale Admitted=True condition and Admission
		// counts (Finish only sets the Finished condition), so checking admitted-count alone
		// would pass spuriously against a dead slice. Rule that out explicitly first.
		ginkgo.By("the partially-admitted slice was not finished by the preemption")
		gomega.Eventually(func(g gomega.Gomega) {
			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(partialSlice), wl)).Should(gomega.Succeed())
			g.Expect(workloadfinish.IsFinished(wl)).Should(gomega.BeFalse())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// Simulates the preemptor's job completing and releasing its quota, exactly the 7 pods
		// the partially-admitted slice held before being preempted - the same "quota unchanged"
		// scenario as the drain regression above, just arrived at through preemption.
		ginkgo.By("the preemptor finishes, releasing its quota")
		util.FinishWorkloads(ctx, k8sClient, preemptor)

		ginkgo.By("the partially-admitted slice resumes at 6 workers, with no change to the quota")
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, partialSlice, workersGroupName, 6)
		expectPodsUsage(7)
		util.ExpectWorkloadsToBePending(ctx, k8sClient, probe)
	})
})
