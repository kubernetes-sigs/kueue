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

package leaderworkerset

import (
	"strconv"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingleaderworkerset "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("AdmissionGatedBy controls whether LeaderWorkerSet is admissible", ginkgo.Label("admissiongatedby"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns             *corev1.Namespace
		resourceFlavor *kueue.ResourceFlavor
		clusterQueue   *kueue.ClusterQueue
		localQueue     *kueue.LocalQueue
	)
	cpuNominalQuota := 5

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerAndControllersSetup(false, true, nil))
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")

		resourceFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, resourceFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("default").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).Resource(corev1.ResourceCPU, strconv.Itoa(cpuNominalQuota)).Obj()).
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

	ginkgo.It("Should not admit LeaderWorkerSet while AdmissionGatedBy is present", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.AdmissionGatedBy, true)
		gateValue := "example.com/controller1"

		ginkgo.By("Creating a LeaderWorkerSet with admission gate annotation")
		lws := testingleaderworkerset.MakeLeaderWorkerSet("gated-lws", ns.Name).
			Queue(localQueue.Name).
			Annotation(constants.AdmissionGatedByAnnotation, gateValue).
			Request(corev1.ResourceCPU, "0.1").
			RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
			Obj()
		util.MustCreate(ctx, k8sClient, lws)

		wlLookupKey := types.NamespacedName{
			Name:      leaderworkerset.GetWorkloadName(lws.UID, lws.Name, "0"),
			Namespace: ns.Name,
		}
		util.VerifyAdmissionGatedByJobIsNonAdmissible(ctx, k8sClient, wlLookupKey, gateValue)
	})

	ginkgo.It("Should admit LeaderWorkerSet when AdmissionGatedBy is removed", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.AdmissionGatedBy, true)
		gateValue := "example.com/controller1"

		ginkgo.By("Creating a LeaderWorkerSet with admission gate annotation")
		lws := testingleaderworkerset.MakeLeaderWorkerSet("gated-lws-2", ns.Name).
			Queue(localQueue.Name).
			Annotation(constants.AdmissionGatedByAnnotation, gateValue).
			Request(corev1.ResourceCPU, "0.1").
			RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
			Obj()
		util.MustCreate(ctx, k8sClient, lws)

		wlLookupKey := types.NamespacedName{
			Name:      leaderworkerset.GetWorkloadName(lws.UID, lws.Name, "0"),
			Namespace: ns.Name,
		}
		util.VerifyAdmissionGatedByJobIsNonAdmissible(ctx, k8sClient, wlLookupKey, gateValue)

		util.VerifyAdmissionGatedByJobBecomesAdmissibleWhenGateRemoved(ctx, k8sClient, lws, wlLookupKey)

		ginkgo.By("Checking the LeaderWorkerSet replicas are restored")
		lwsLookupKey := types.NamespacedName{Name: lws.Name, Namespace: ns.Name}
		createdLWS := &leaderworkersetv1.LeaderWorkerSet{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lwsLookupKey, createdLWS)).Should(gomega.Succeed())
			g.Expect(createdLWS.Spec.Replicas).Should(gomega.Equal(ptr.To[int32](1)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should allow removing one gate from a LeaderWorkerSet with multiple gates", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.AdmissionGatedBy, true)
		initialGateValue := "example.com/controller1,example.com/controller2"
		updatedGateValue := "example.com/controller1"

		ginkgo.By("Creating a LeaderWorkerSet with two admission gates")
		lws := testingleaderworkerset.MakeLeaderWorkerSet("multi-gated-lws", ns.Name).
			Queue(localQueue.Name).
			Annotation(constants.AdmissionGatedByAnnotation, initialGateValue).
			Request(corev1.ResourceCPU, "0.1").
			RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
			Obj()
		util.MustCreate(ctx, k8sClient, lws)

		wlLookupKey := types.NamespacedName{
			Name:      leaderworkerset.GetWorkloadName(lws.UID, lws.Name, "0"),
			Namespace: ns.Name,
		}
		ginkgo.By("Removing one gate and verifying the LeaderWorkerSet remains inadmissible")
		util.VerifyAdmissionGatedByJobAllowsRemovingOneGate(ctx, k8sClient, lws, wlLookupKey, initialGateValue, updatedGateValue)
	})

	ginkgo.It("Should admit LeaderWorkerSet when AdmissionGatedBy annotation is present but feature gate is disabled", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.AdmissionGatedBy, false)

		gateValue := "example.com/controller1"

		ginkgo.By("Creating a LeaderWorkerSet with admission gate annotation but feature gate disabled")
		lws := testingleaderworkerset.MakeLeaderWorkerSet("gated-lws-feature-disabled", ns.Name).
			Queue(localQueue.Name).
			Annotation(constants.AdmissionGatedByAnnotation, gateValue).
			Request(corev1.ResourceCPU, "0.1").
			RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
			Obj()
		util.MustCreate(ctx, k8sClient, lws)

		wlLookupKey := types.NamespacedName{
			Name:      leaderworkerset.GetWorkloadName(lws.UID, lws.Name, "0"),
			Namespace: ns.Name,
		}

		util.VerifyJobAdmittedWhenFeatureGateDisabled(ctx, k8sClient, lws, wlLookupKey)
	})
})
