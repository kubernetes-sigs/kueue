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

package rayservice

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
	"sigs.k8s.io/kueue/pkg/util/podset"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	testingrayservice "sigs.k8s.io/kueue/pkg/util/testingjobs/rayservice"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

// KEP-12100: two worker groups pinned to separate resource flavors by their node selectors. The
// order-based shrink spends its whole budget from the last group before it can touch the first,
// draining a group whose own flavor was never the constraint - so the give-back phase has to
// restore it (Scenario D). Because the groups draw on separate capacity, one can be out of room
// while the other still has plenty, which is where a scale-up has to make progress across the
// Workload rather than group by group.
var _ = ginkgo.Describe("RayService with partial replica scale-up across resource flavors", ginkgo.Label("job:ray", "area:jobs"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	const (
		instanceTypeLabel = "instance-type"
		reservationGroup  = "workers-reservation"
		spotGroup         = "workers-spot"
	)

	var (
		ns           *corev1.Namespace
		reservation  *kueue.ResourceFlavor
		spot         *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
	)

	scaleWorkerGroups := func(service *rayv1.RayService, reservationReplicas, spotReplicas int32) {
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(service), service)).Should(gomega.Succeed())
			g.Expect(service.Spec.RayClusterSpec.WorkerGroupSpecs).Should(gomega.HaveLen(2))
			service.Spec.RayClusterSpec.WorkerGroupSpecs[0].Replicas = new(reservationReplicas)
			service.Spec.RayClusterSpec.WorkerGroupSpecs[1].Replicas = new(spotReplicas)
			g.Expect(k8sClient.Update(ctx, service)).Should(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
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
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "rayservice-partial-")

		reservation = utiltestingapi.MakeResourceFlavor("reservation").
			NodeLabel(instanceTypeLabel, "reservation").Obj()
		behavioral.MustCreate(ctx, k8sClient, reservation)
		spot = utiltestingapi.MakeResourceFlavor("spot").
			NodeLabel(instanceTypeLabel, "spot").Obj()
		behavioral.MustCreate(ctx, k8sClient, spot)

		// The reservation flavor is the scarce one: it can hold 2 workers, so a scale-up of the
		// reservation group has to be cut. The spot flavor has room for every spot worker, so
		// anything the shrink takes from the spot group has to be given back.
		clusterQueue = utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("reservation").Resource(corev1.ResourceCPU, "2").Obj(),
				*utiltestingapi.MakeFlavorQuotas("spot").Resource(corev1.ResourceCPU, "20").Obj(),
			).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, localQueue)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, reservation, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, spot, true)
	})

	ginkgo.It("Should give back a worker group drained for another group's flavor", func() {
		// Each worker asks for 1 CPU, so a group's replica count is its CPU usage in the flavor
		// its node selector pins it to. minReplicas opts a group into partial scale-up; only its
		// presence is checked, not its value. The head asks for no CPU, which keeps it out of the
		// quota arithmetic entirely.
		testRayService := testingrayservice.MakeService("foo", ns.Name).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Annotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			Queue(localQueue.Name).
			WithWorkerGroups(
				*testingraycluster.MakeWorkerGroup(reservationGroup, 1).
					MaxReplicas(50).
					Request(corev1.ResourceCPU, "1").
					NodeSelector(instanceTypeLabel, "reservation").
					Obj(),
				*testingraycluster.MakeWorkerGroup(spotGroup, 9).
					MaxReplicas(50).
					Request(corev1.ResourceCPU, "1").
					NodeSelector(instanceTypeLabel, "spot").
					Obj(),
			).
			Obj()

		ginkgo.By("admitting the rayservice at 1 reservation and 9 spot workers")
		behavioral.MustCreate(ctx, k8sClient, testRayService)
		initialSlice := &behavioral.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, initialSlice, reservationGroup, 1)
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, initialSlice, spotGroup, 9)

		// Scaling both groups makes minCount(baseline) each group is already running at: 1 for
		// the reservation group and 9 for the spot one, matching the KEP's Scenario D shape.
		ginkgo.By("scaling to 4 reservation and 20 spot workers")
		scaleWorkerGroups(testRayService, 4, 20)

		ginkgo.By("a new slice requests the full scale-up, with a floor under each worker group")
		scaleUpSlice := behavioral.ExpectNewWorkloadSlice(ctx, k8sClient, initialSlice)
		gomega.Expect(podset.FindPodSetByName(scaleUpSlice.Spec.PodSets, kueue.NewPodSetReference(reservationGroup)).Count).
			Should(gomega.Equal(int32(4)))
		gomega.Expect(podset.FindPodSetByName(scaleUpSlice.Spec.PodSets, kueue.NewPodSetReference(spotGroup)).Count).
			Should(gomega.Equal(int32(20)))

		// The reservation flavor holds 2, so the reservation group is cut from 4 down to 2.
		// Getting there costs the shrink nearly its whole budget, which means draining the spot
		// group to its baseline of 9 first - even though the spot flavor had room for all 20.
		// Give-back restores it; without that phase the spot group would stay at 9.
		ginkgo.By("admitting the reservation group at what its flavor holds and the spot group in full")
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, scaleUpSlice, reservationGroup, 2)
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, scaleUpSlice, spotGroup, 20)

		ginkgo.By("the pre-scale-up slice is finished")
		behavioral.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKeyFromObject(initialSlice))
	})

	ginkgo.It("Should grow the group that has room while the other stays at its baseline", func() {
		// The reservation group already fills its flavor, so the scale-up cannot give it a single
		// extra worker. Demanding growth from every growing group would leave the whole scale-up
		// unadmitted; demanding it from the Workload as a whole lets the spot group grow.
		testRayService := testingrayservice.MakeService("foo", ns.Name).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Annotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			Queue(localQueue.Name).
			WithWorkerGroups(
				*testingraycluster.MakeWorkerGroup(reservationGroup, 2).
					MaxReplicas(50).
					Request(corev1.ResourceCPU, "1").
					NodeSelector(instanceTypeLabel, "reservation").
					Obj(),
				*testingraycluster.MakeWorkerGroup(spotGroup, 9).
					MaxReplicas(50).
					Request(corev1.ResourceCPU, "1").
					NodeSelector(instanceTypeLabel, "spot").
					Obj(),
			).
			Obj()

		ginkgo.By("admitting the rayservice at 2 reservation and 9 spot workers")
		behavioral.MustCreate(ctx, k8sClient, testRayService)
		initialSlice := &behavioral.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, initialSlice, reservationGroup, 2)
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, initialSlice, spotGroup, 9)

		ginkgo.By("scaling to 4 reservation and 20 spot workers")
		scaleWorkerGroups(testRayService, 4, 20)

		scaleUpSlice := behavioral.ExpectNewWorkloadSlice(ctx, k8sClient, initialSlice)

		ginkgo.By("admitting the reservation group at its baseline and the spot group in full")
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, scaleUpSlice, reservationGroup, 2)
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, scaleUpSlice, spotGroup, 20)
	})

	ginkgo.It("Should leave the scale-up pending when no group has room above its baseline", func() {
		// Both flavors are full at the counts the job is already running, so the only assignment
		// that fits is the one it already has. Admitting that would retire the running slice for
		// a replacement of the same size, gaining the job nothing.
		testRayService := testingrayservice.MakeService("foo", ns.Name).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Annotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			Queue(localQueue.Name).
			WithWorkerGroups(
				*testingraycluster.MakeWorkerGroup(reservationGroup, 2).
					MaxReplicas(50).
					Request(corev1.ResourceCPU, "1").
					NodeSelector(instanceTypeLabel, "reservation").
					Obj(),
				*testingraycluster.MakeWorkerGroup(spotGroup, 20).
					MaxReplicas(50).
					Request(corev1.ResourceCPU, "1").
					NodeSelector(instanceTypeLabel, "spot").
					Obj(),
			).
			Obj()

		ginkgo.By("admitting the rayservice at the full capacity of both flavors")
		behavioral.MustCreate(ctx, k8sClient, testRayService)
		initialSlice := &behavioral.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, initialSlice, reservationGroup, 2)
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, initialSlice, spotGroup, 20)

		ginkgo.By("scaling to 4 reservation and 25 spot workers")
		scaleWorkerGroups(testRayService, 4, 25)

		scaleUpSlice := behavioral.ExpectNewWorkloadSlice(ctx, k8sClient, initialSlice)

		ginkgo.By("the scale-up stays pending")
		behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, scaleUpSlice)

		ginkgo.By("the running slice keeps its admission")
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, initialSlice, reservationGroup, 2)
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, initialSlice, spotGroup, 20)
	})
})
