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
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
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

	// podsUsage reports the ClusterQueue's usage of the "pods" resource, which is the only
	// constrained resource in these specs. Looked up by name rather than by index, since the
	// resource group declares more than one resource.
	podsUsage := func(g gomega.Gomega) int64 {
		cq := &kueue.ClusterQueue{}
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), cq)).Should(gomega.Succeed())
		for _, flavorUsage := range cq.Status.FlavorsUsage {
			for _, r := range flavorUsage.Resources {
				if r.Name == corev1.ResourcePods {
					return r.Total.Value()
				}
			}
		}
		g.Expect(false).Should(gomega.BeTrue(), "no pods usage reported for the ClusterQueue")
		return 0
	}

	// setPodsQuota rewrites the nominal quota of the "pods" resource.
	setPodsQuota := func(quota string) {
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).Should(gomega.Succeed())
			resources := clusterQueue.Spec.ResourceGroups[0].Flavors[0].Resources
			for i := range resources {
				if resources[i].Name == corev1.ResourcePods {
					resources[i].NominalQuota = resource.MustParse(quota)
				}
			}
			g.Expect(k8sClient.Update(ctx, clusterQueue)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	}

	// scaleFirstWorkerGroup emulates the KubeRay controller, which is what updates .replicas in
	// production. envtest runs no KubeRay operator, so the spec drives the scale event itself,
	// exactly like the existing scale-up/scale-down specs in this package do.
	scaleFirstWorkerGroup := func(rayCluster *rayv1.RayCluster, replicas int32) {
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayCluster), rayCluster)).Should(gomega.Succeed())
			rayCluster.Spec.WorkerGroupSpecs[0].Replicas = ptr.To(replicas)
			g.Expect(k8sClient.Update(ctx, rayCluster)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	}

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
			g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
			g.Expect(workloads.Items).Should(gomega.HaveLen(1))
			wl := &workloads.Items[0]
			g.Expect(wl.Spec.PodSets).Should(gomega.HaveLen(2))
			g.Expect(wl.Spec.PodSets[1].Count).Should(gomega.Equal(int32(5)))
			g.Expect(workload.IsAdmitted(wl)).Should(gomega.BeTrue())
			g.Expect(wl.Status.Admission.PodSetAssignments[1].Count).Should(gomega.Equal(int32(5)))
			admittedSliceBeforeScaleUp = wl
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("quota usage reflects the full 6 pods (1 head + 5 workers)")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(podsUsage(g)).Should(gomega.Equal(int64(6)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// Step 1: the scale-up cannot be fully admitted (1 + 10 = 11 pods > 7), so it is admitted
		// partially, up to the available quota.
		ginkgo.By("scaling the worker group to 10 replicas")
		scaleFirstWorkerGroup(testRayCluster, 10)

		ginkgo.By("a new workload slice replaces the admitted one, requesting the full 10 workers")
		gomega.Eventually(func(g gomega.Gomega) {
			partialSlice = nil
			workloads := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
			for i := range workloads.Items {
				wl := &workloads.Items[i]
				if wl.Name != admittedSliceBeforeScaleUp.Name && workload.IsAdmitted(wl) {
					partialSlice = wl
				}
			}
			g.Expect(partialSlice).ShouldNot(gomega.BeNil())
			g.Expect(partialSlice.Spec.PodSets[1].Count).Should(gomega.Equal(int32(10)))
			// MinCount is the previously-admitted worker count plus one, i.e. the scale-up must
			// grow by at least one pod to be worth admitting at all.
			g.Expect(partialSlice.Spec.PodSets[1].MinCount).Should(gomega.Equal(ptr.To(int32(6))))
			// Only 6 of the 10 requested workers fit: 1 head + 6 workers = 7 = the whole quota.
			g.Expect(partialSlice.Status.Admission.PodSetAssignments[1].Count).Should(gomega.Equal(int32(6)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("the old (pre-scale-up) slice is finished")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(admittedSliceBeforeScaleUp), admittedSliceBeforeScaleUp)).Should(gomega.Succeed())
			g.Expect(workloadfinish.IsFinished(admittedSliceBeforeScaleUp)).Should(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("quota usage reflects exactly 7 pods (1 head + 6 workers), full utilization")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(podsUsage(g)).Should(gomega.Equal(int64(7)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// Step 2: the probe workload represents the full, not-yet-satisfiable request. It is what
		// lets the remaining replicas be admitted later, without a further scale event.
		ginkgo.By("a scale-up probe workload is created for the full 10 workers")
		gomega.Eventually(func(g gomega.Gomega) {
			probeWorkload = nil
			workloads := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
			// The finished pre-scale-up slice, the partially-admitted slice, and the probe.
			g.Expect(workloads.Items).Should(gomega.HaveLen(3))
			for i := range workloads.Items {
				wl := &workloads.Items[i]
				if wl.Name != admittedSliceBeforeScaleUp.Name && wl.Name != partialSlice.Name {
					probeWorkload = wl
				}
			}
			g.Expect(probeWorkload).ShouldNot(gomega.BeNil())
			g.Expect(probeWorkload.Spec.PodSets[1].Count).Should(gomega.Equal(int32(10)))
			// The probe must point at the slice it would replace, otherwise it is treated as an
			// out-of-sync slice and finished before it can ever be admitted.
			g.Expect(probeWorkload.Annotations).Should(gomega.HaveKeyWithValue(
				workloadslicing.WorkloadSliceReplacementFor, string(workload.Key(partialSlice))))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("the probe workload stays pending while the quota is exhausted")
		util.ExpectWorkloadsToBePending(ctx, k8sClient, probeWorkload)

		// Step 3: opportunistic admission once capacity appears. Modelled as a ClusterQueue quota
		// increase, mirroring the KEP's own worked example, rather than a sibling workload
		// finishing - that is a separate claim and belongs in its own spec.
		ginkgo.By("raising the ClusterQueue's pod quota from 7 to 15")
		setPodsQuota("15")

		ginkgo.By("the probe workload is admitted with the full 10 workers")
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, probeWorkload)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(probeWorkload), probeWorkload)).Should(gomega.Succeed())
			g.Expect(probeWorkload.Status.Admission.PodSetAssignments[1].Count).Should(gomega.Equal(int32(10)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("the partially-admitted slice is finished, having been replaced by the probe")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(partialSlice), partialSlice)).Should(gomega.Succeed())
			g.Expect(workloadfinish.IsFinished(partialSlice)).Should(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("quota usage reflects the full 11 pods (1 head + 10 workers)")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(podsUsage(g)).Should(gomega.Equal(int64(11)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should partially admit a RayCluster scale-up by preempting a lower-priority workload",
		framework.SlowSpec, func() {
			var (
				admittedSliceBeforeScaleUp *kueue.Workload
				partialSlice               *kueue.Workload
			)

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

			gomega.Eventually(func(g gomega.Gomega) {
				admittedSliceBeforeScaleUp = nil
				workloads := &kueue.WorkloadList{}
				g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
				for i := range workloads.Items {
					wl := &workloads.Items[i]
					if wl.Name != victim.Name && workload.IsAdmitted(wl) {
						admittedSliceBeforeScaleUp = wl
					}
				}
				g.Expect(admittedSliceBeforeScaleUp).ShouldNot(gomega.BeNil())
				g.Expect(admittedSliceBeforeScaleUp.Status.Admission.PodSetAssignments[1].Count).Should(gomega.Equal(int32(2)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(podsUsage(g)).Should(gomega.Equal(int64(7)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			// The scale-up wants 1 + 6 = 7 pods. Replacing its own slice frees 3, which is not
			// enough to reach MinCount (2 admitted + 1 = 3 workers, i.e. 4 pods), so the scale-up
			// only fits by preempting the victim.
			ginkgo.By("scaling the worker group to 6 replicas")
			scaleFirstWorkerGroup(testRayCluster, 6)

			ginkgo.By("the lower-priority workload is preempted")
			util.ExpectWorkloadsToBePreempted(ctx, k8sClient, victim)

			ginkgo.By("the scale-up is admitted partially, using the reclaimed quota")
			gomega.Eventually(func(g gomega.Gomega) {
				partialSlice = nil
				workloads := &kueue.WorkloadList{}
				g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
				for i := range workloads.Items {
					wl := &workloads.Items[i]
					if wl.Name == victim.Name || wl.Name == admittedSliceBeforeScaleUp.Name {
						continue
					}
					if workload.IsAdmitted(wl) {
						partialSlice = wl
					}
				}
				g.Expect(partialSlice).ShouldNot(gomega.BeNil())
				g.Expect(partialSlice.Spec.PodSets[1].Count).Should(gomega.Equal(int32(6)))
				g.Expect(partialSlice.Spec.PodSets[1].MinCount).Should(gomega.Equal(ptr.To(int32(3))))
				// The whole 7-pod quota is now the RayCluster's: 1 head + 6 workers.
				g.Expect(partialSlice.Status.Admission.PodSetAssignments[1].Count).Should(gomega.Equal(int32(6)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("quota usage still reflects exactly 7 pods")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(podsUsage(g)).Should(gomega.Equal(int64(7)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
})
