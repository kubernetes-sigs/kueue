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
	"sigs.k8s.io/kueue/test/util"
)

// KEP-12100, Scenario D: two worker groups pinned to separate resource flavors by their node
// selectors. The order-based shrink spends its whole budget from the last group before it can
// touch the first, draining a group whose own flavor was never the constraint - so the give-back
// phase has to restore it.
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
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "rayservice-partial-")

		reservation = utiltestingapi.MakeResourceFlavor("reservation").
			NodeLabel(instanceTypeLabel, "reservation").Obj()
		util.MustCreate(ctx, k8sClient, reservation)
		spot = utiltestingapi.MakeResourceFlavor("spot").
			NodeLabel(instanceTypeLabel, "spot").Obj()
		util.MustCreate(ctx, k8sClient, spot)

		// The reservation flavor is the scarce one: it can hold 2 workers, so a scale-up of the
		// reservation group has to be cut. The spot flavor has room for every spot worker, so
		// anything the shrink takes from the spot group has to be given back.
		clusterQueue = utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("reservation").Resource(corev1.ResourceCPU, "2").Obj(),
				*utiltestingapi.MakeFlavorQuotas("spot").Resource(corev1.ResourceCPU, "20").Obj(),
			).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, reservation, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, spot, true)
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
		util.MustCreate(ctx, k8sClient, testRayService)
		initialSlice := &util.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, initialSlice, reservationGroup, 1)
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, initialSlice, spotGroup, 9)

		// Scaling both groups makes minCount = admitted + 1 for each: 2 for the reservation
		// group and 10 for the spot one, matching the KEP's Scenario D shape.
		ginkgo.By("scaling to 4 reservation and 20 spot workers")
		scaleWorkerGroups(testRayService, 4, 20)

		ginkgo.By("a new slice requests the full scale-up, with a floor under each worker group")
		scaleUpSlice := util.ExpectNewWorkloadSlice(ctx, k8sClient, initialSlice)
		gomega.Expect(podset.FindPodSetByName(scaleUpSlice.Spec.PodSets, kueue.NewPodSetReference(reservationGroup)).Count).
			Should(gomega.Equal(int32(4)))
		gomega.Expect(podset.FindPodSetByName(scaleUpSlice.Spec.PodSets, kueue.NewPodSetReference(spotGroup)).Count).
			Should(gomega.Equal(int32(20)))

		// The reservation flavor holds 2, so the reservation group is cut from 4 to its floor of
		// 2. Reaching that floor costs the shrink its whole budget, which means draining the spot
		// group to its own floor of 10 first - even though the spot flavor had room for all 20.
		// Give-back restores it; without that phase the spot group would stay at 10.
		ginkgo.By("admitting the reservation group at its floor and the spot group in full")
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, scaleUpSlice, reservationGroup, 2)
		util.ExpectPodSetAdmittedCount(ctx, k8sClient, scaleUpSlice, spotGroup, 20)

		ginkgo.By("the pre-scale-up slice is finished")
		util.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKeyFromObject(initialSlice))
	})
})
