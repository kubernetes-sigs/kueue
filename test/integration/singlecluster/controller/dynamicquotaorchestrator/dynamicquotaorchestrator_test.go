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

package dynamicquotaorchestrator

import (
	"context"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("DynamicQuotaOrchestrator controller", ginkgo.Label("controller:dynamicquotaorchestrator", "area:dynamicquotaorchestration"), func() {
	var (
		cps              []*kueuealpha.CapacityProvider
		dqo              *kueuealpha.DynamicQuotaOrchestrator
		ancestorDQO      *kueuealpha.DynamicQuotaOrchestrator
		childDQO         *kueuealpha.DynamicQuotaOrchestrator
		cq               *kueue.ClusterQueue
		cq1              *kueue.ClusterQueue
		cq2              *kueue.ClusterQueue
		cq3              *kueue.ClusterQueue
		rootCohort       *kueue.Cohort
		childCohort      *kueue.Cohort
		grandchildCohort *kueue.Cohort
	)

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DynamicQuotaOrchestration, true)
	})

	ginkgo.AfterEach(func() {
		util.ExpectObjectToBeDeleted(ctx, k8sClient, childDQO, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, ancestorDQO, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, dqo, true)
		for _, cp := range cps {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cp, true)
		}
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq1, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq2, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq3, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, grandchildCohort, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, childCohort, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rootCohort, true)
		childDQO, ancestorDQO, dqo = nil, nil, nil
		cq, cq1, cq2, cq3 = nil, nil, nil, nil
		childCohort, grandchildCohort, rootCohort = nil, nil, nil
		cps = nil
	})

	ginkgo.It("Should discover capacity from CapacityProvider and dynamically update", func() {
		cp := utiltestingalpha.MakeCapacityProvider("discovery-cp").
			ControllerName("example.com/test-provider").
			OrchestratedFlavors("f1").
			Capacity(
				utiltestingalpha.MakeNormalizedCapacity().
					Flavors(
						utiltestingalpha.MakeNormalizedCapacityFlavor("f1").
							Resource(corev1.ResourceCPU, "100").
							Obj(),
					).
					Obj(),
			).
			Condition(metav1.Condition{
				Type:               kueuealpha.CapacityProviderCapacitySynchronized,
				Status:             metav1.ConditionTrue,
				Reason:             kueuealpha.CapacityProviderReasonSynchronized,
				Message:            "Capacity synchronized successfully",
				LastTransitionTime: metav1.Now(),
			}).
			Obj()
		cps = append(cps, cp)
		createCapacityProvider(ctx, k8sClient, cp)

		dqo = utiltestingalpha.MakeDynamicQuotaOrchestrator("discovery-dqo").
			DiscoveryProvider(cp.Name, nil).
			Obj()
		util.MustCreate(ctx, k8sClient, dqo)

		dqoKey := types.NamespacedName{Name: dqo.Name}
		latestDQO := &kueuealpha.DynamicQuotaOrchestrator{}

		ginkgo.By("Verifying initial discovery aggregation", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, dqoKey, latestDQO)).Should(gomega.Succeed())
				g.Expect(latestDQO.Status.Conditions).Should(utiltesting.HaveConditionStatusTrueAndReason(
					kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					kueuealpha.DynamicQuotaOrchestratorReasonComputed,
				))

				wantCapacity := utiltestingalpha.MakeEffectiveCapacity().
					Flavors(
						*utiltestingalpha.MakeEffectiveCapacityFlavor("f1").
							Resource(corev1.ResourceCPU, "100").
							Obj(),
					).
					Obj()
				g.Expect(cmp.Diff(wantCapacity, latestDQO.Status.EffectiveCapacity, cmpopts.EquateEmpty())).Should(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Updating capacity in CapacityProvider and verifying dynamic DQO update", func() {
			updatedCapacity := utiltestingalpha.MakeNormalizedCapacity().
				Flavors(
					utiltestingalpha.MakeNormalizedCapacityFlavor("f1").
						Resource(corev1.ResourceCPU, "250").
						Obj()).
				Obj()
			setCapacityProviderCapacity(ctx, k8sClient, cp, updatedCapacity)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, dqoKey, latestDQO)).Should(gomega.Succeed())
				wantUpdatedCapacity := utiltestingalpha.MakeEffectiveCapacity().
					Flavors(
						*utiltestingalpha.MakeEffectiveCapacityFlavor("f1").
							Resource(corev1.ResourceCPU, "250").
							Obj(),
					).
					Obj()
				g.Expect(cmp.Diff(wantUpdatedCapacity, latestDQO.Status.EffectiveCapacity, cmpopts.EquateEmpty())).Should(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should transition condition between Computed and ProviderNotReady when CapacityProvider synchronization changes", func() {
		cp := utiltestingalpha.MakeCapacityProvider("transition-cp").
			ControllerName("example.com/test-provider").
			OrchestratedFlavors("f1").
			Capacity(
				utiltestingalpha.MakeNormalizedCapacity().
					Flavors(
						utiltestingalpha.MakeNormalizedCapacityFlavor("f1").
							Resource(corev1.ResourceCPU, "100").
							Obj(),
					).
					Obj(),
			).
			Condition(metav1.Condition{
				Type:               kueuealpha.CapacityProviderCapacitySynchronized,
				Status:             metav1.ConditionTrue,
				Reason:             kueuealpha.CapacityProviderReasonSynchronized,
				Message:            "Capacity synchronized successfully",
				LastTransitionTime: metav1.Now(),
			}).
			Obj()
		cps = append(cps, cp)
		createCapacityProvider(ctx, k8sClient, cp)

		dqo = utiltestingalpha.MakeDynamicQuotaOrchestrator("transition-dqo").
			DiscoveryProvider(cp.Name, nil).
			Obj()
		util.MustCreate(ctx, k8sClient, dqo)

		dqoKey := types.NamespacedName{Name: dqo.Name}
		latestDQO := &kueuealpha.DynamicQuotaOrchestrator{}

		ginkgo.By("Verifying initial discovery aggregation", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, dqoKey, latestDQO)).Should(gomega.Succeed())
				g.Expect(latestDQO.Status.Conditions).Should(utiltesting.HaveConditionStatusTrueAndReason(
					kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					kueuealpha.DynamicQuotaOrchestratorReasonComputed,
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Marking CapacityProvider as unsynchronized and verifying DQO transitions to ProviderNotReady", func() {
			setCapacityProviderSyncCondition(ctx, k8sClient, cp, metav1.ConditionFalse, kueuealpha.CapacityProviderReasonSourceUnavailable, "Backend source is unreachable")

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, dqoKey, latestDQO)).Should(gomega.Succeed())
				g.Expect(latestDQO.Status.Conditions).Should(utiltesting.HaveConditionStatusFalseAndReason(
					kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					kueuealpha.DynamicQuotaOrchestratorReasonProviderNotReady,
				))
				cond := apimeta.FindStatusCondition(latestDQO.Status.Conditions, kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed)
				g.Expect(cond.Message).Should(gomega.ContainSubstring("\"transition-cp\" is not synchronized"))
				g.Expect(latestDQO.Status.EffectiveCapacity).Should(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Re-synchronizing CapacityProvider and verifying DQO recovers to Computed", func() {
			setCapacityProviderSyncCondition(ctx, k8sClient, cp, metav1.ConditionTrue, kueuealpha.CapacityProviderReasonSynchronized, "Capacity synchronized successfully")

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, dqoKey, latestDQO)).Should(gomega.Succeed())
				g.Expect(latestDQO.Status.Conditions).Should(utiltesting.HaveConditionStatusTrueAndReason(
					kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					kueuealpha.DynamicQuotaOrchestratorReasonComputed,
				))

				wantCapacity := utiltestingalpha.MakeEffectiveCapacity().
					Flavors(
						*utiltestingalpha.MakeEffectiveCapacityFlavor("f1").
							Resource(corev1.ResourceCPU, "100").
							Obj(),
					).
					Obj()
				g.Expect(cmp.Diff(wantCapacity, latestDQO.Status.EffectiveCapacity, cmpopts.EquateEmpty())).Should(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should aggregate capacity from multiple CapacityProviders and update when either provider changes", func() {
		cp1 := utiltestingalpha.MakeCapacityProvider("multi-cp-1").
			ControllerName("example.com/p1").
			OrchestratedFlavors("f1").
			Capacity(
				utiltestingalpha.MakeNormalizedCapacity().
					Flavors(
						utiltestingalpha.MakeNormalizedCapacityFlavor("f1").
							Resource(corev1.ResourceCPU, "100").
							Obj(),
					).
					Obj(),
			).
			Condition(metav1.Condition{
				Type:               kueuealpha.CapacityProviderCapacitySynchronized,
				Status:             metav1.ConditionTrue,
				Reason:             kueuealpha.CapacityProviderReasonSynchronized,
				Message:            "Capacity synchronized successfully",
				LastTransitionTime: metav1.Now(),
			}).
			Obj()
		cps = append(cps, cp1)
		createCapacityProvider(ctx, k8sClient, cp1)

		cp2 := utiltestingalpha.MakeCapacityProvider("multi-cp-2").
			ControllerName("example.com/p2").
			OrchestratedFlavors("f1", "f2").
			Capacity(
				utiltestingalpha.MakeNormalizedCapacity().
					Flavors(
						utiltestingalpha.MakeNormalizedCapacityFlavor("f1").
							Resource(corev1.ResourceCPU, "50").
							Obj(),
						utiltestingalpha.MakeNormalizedCapacityFlavor("f2").
							Resource(corev1.ResourceMemory, "20Gi").
							Obj(),
					).
					Obj(),
			).
			Condition(metav1.Condition{
				Type:               kueuealpha.CapacityProviderCapacitySynchronized,
				Status:             metav1.ConditionTrue,
				Reason:             kueuealpha.CapacityProviderReasonSynchronized,
				Message:            "Capacity synchronized successfully",
				LastTransitionTime: metav1.Now(),
			}).
			Obj()
		cps = append(cps, cp2)
		createCapacityProvider(ctx, k8sClient, cp2)

		dqo = utiltestingalpha.MakeDynamicQuotaOrchestrator("multi-dqo").
			DiscoveryProvider("multi-cp-1", nil).
			DiscoveryProvider("multi-cp-2", nil).
			Obj()
		util.MustCreate(ctx, k8sClient, dqo)

		dqoKey := types.NamespacedName{Name: dqo.Name}
		latestDQO := &kueuealpha.DynamicQuotaOrchestrator{}

		ginkgo.By("Verifying combined effective capacity aggregation", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, dqoKey, latestDQO)).Should(gomega.Succeed())
				g.Expect(latestDQO.Status.Conditions).Should(utiltesting.HaveConditionStatusTrueAndReason(
					kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					kueuealpha.DynamicQuotaOrchestratorReasonComputed,
				))

				wantCapacity := utiltestingalpha.MakeEffectiveCapacity().
					Flavors(
						*utiltestingalpha.MakeEffectiveCapacityFlavor("f1").
							Resource(corev1.ResourceCPU, "150").
							Obj(),
						*utiltestingalpha.MakeEffectiveCapacityFlavor("f2").
							Resource(corev1.ResourceMemory, "20Gi").
							Obj(),
					).
					Obj()
				g.Expect(cmp.Diff(wantCapacity, latestDQO.Status.EffectiveCapacity, cmpopts.EquateEmpty())).Should(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Updating capacity in one provider and verifying dynamic re-aggregation", func() {
			updatedFlavor := utiltestingalpha.MakeNormalizedCapacityFlavor("f1").
				Resource(corev1.ResourceCPU, "200").
				Obj()
			updatedCapacity := utiltestingalpha.MakeNormalizedCapacity().
				Flavors(updatedFlavor).
				Obj()
			setCapacityProviderCapacity(ctx, k8sClient, cp1, updatedCapacity)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, dqoKey, latestDQO)).Should(gomega.Succeed())
				wantUpdatedCapacity := utiltestingalpha.MakeEffectiveCapacity().
					Flavors(
						*utiltestingalpha.MakeEffectiveCapacityFlavor("f1").
							Resource(corev1.ResourceCPU, "250").
							Obj(),
						*utiltestingalpha.MakeEffectiveCapacityFlavor("f2").
							Resource(corev1.ResourceMemory, "20Gi").
							Obj(),
					).
					Obj()
				g.Expect(cmp.Diff(wantUpdatedCapacity, latestDQO.Status.EffectiveCapacity, cmpopts.EquateEmpty())).Should(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should report Misconfigured when referenced CapacityProvider is missing, and recover when it is created", func() {
		dqo = utiltestingalpha.MakeDynamicQuotaOrchestrator("missing-provider-dqo").
			DiscoveryProvider("late-cp", nil).
			Obj()
		util.MustCreate(ctx, k8sClient, dqo)

		dqoKey := types.NamespacedName{Name: dqo.Name}
		latestDQO := &kueuealpha.DynamicQuotaOrchestrator{}

		ginkgo.By("Verifying DQO reports Misconfigured when provider does not exist", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, dqoKey, latestDQO)).Should(gomega.Succeed())
				g.Expect(latestDQO.Status.Conditions).Should(utiltesting.HaveConditionStatusFalseAndReason(
					kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					kueuealpha.DynamicQuotaOrchestratorReasonMisconfigured,
				))
				cond := apimeta.FindStatusCondition(latestDQO.Status.Conditions, kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed)
				g.Expect(cond.Message).Should(gomega.ContainSubstring("CapacityProvider \"late-cp\" not found"))
				g.Expect(latestDQO.Status.EffectiveCapacity).Should(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Creating late-arriving CapacityProvider and verifying recovery", func() {
			cp := utiltestingalpha.MakeCapacityProvider("late-cp").
				ControllerName("example.com/late-provider").
				OrchestratedFlavors("f1").
				Capacity(
					utiltestingalpha.MakeNormalizedCapacity().
						Flavors(
							utiltestingalpha.MakeNormalizedCapacityFlavor("f1").
								Resource(corev1.ResourceCPU, "75").
								Obj(),
						).
						Obj(),
				).
				Condition(metav1.Condition{
					Type:               kueuealpha.CapacityProviderCapacitySynchronized,
					Status:             metav1.ConditionTrue,
					Reason:             kueuealpha.CapacityProviderReasonSynchronized,
					Message:            "Capacity synchronized successfully",
					LastTransitionTime: metav1.Now(),
				}).
				Obj()
			cps = append(cps, cp)
			createCapacityProvider(ctx, k8sClient, cp)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, dqoKey, latestDQO)).Should(gomega.Succeed())
				g.Expect(latestDQO.Status.Conditions).Should(utiltesting.HaveConditionStatusTrueAndReason(
					kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					kueuealpha.DynamicQuotaOrchestratorReasonComputed,
				))

				wantCapacity := utiltestingalpha.MakeEffectiveCapacity().
					Flavors(
						*utiltestingalpha.MakeEffectiveCapacityFlavor("f1").
							Resource(corev1.ResourceCPU, "75").
							Obj(),
					).
					Obj()
				g.Expect(cmp.Diff(wantCapacity, latestDQO.Status.EffectiveCapacity, cmpopts.EquateEmpty())).Should(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should filter unmanaged flavors and scale quantities using effectiveCapacityMultiplier", func() {
		cp := utiltestingalpha.MakeCapacityProvider("filter-cp").
			ControllerName("example.com/test-provider").
			OrchestratedFlavors("allowed-flavor").
			Capacity(
				utiltestingalpha.MakeNormalizedCapacity().
					Flavors(
						utiltestingalpha.MakeNormalizedCapacityFlavor("allowed-flavor").
							Resource(corev1.ResourceCPU, "100").
							Resource(corev1.ResourceMemory, "50Gi").
							Obj(),
						utiltestingalpha.MakeNormalizedCapacityFlavor("unmanaged-flavor").
							Resource(corev1.ResourceCPU, "500").
							Obj(),
					).
					Obj(),
			).
			Condition(metav1.Condition{
				Type:               kueuealpha.CapacityProviderCapacitySynchronized,
				Status:             metav1.ConditionTrue,
				Reason:             kueuealpha.CapacityProviderReasonSynchronized,
				Message:            "Capacity synchronized successfully",
				LastTransitionTime: metav1.Now(),
			}).
			Obj()
		cps = append(cps, cp)
		createCapacityProvider(ctx, k8sClient, cp)

		halfMultiplier := resource.MustParse("0.5")
		dqo = utiltestingalpha.MakeDynamicQuotaOrchestrator("filter-dqo").
			DiscoveryProvider("filter-cp", &halfMultiplier).
			Obj()
		util.MustCreate(ctx, k8sClient, dqo)

		dqoKey := types.NamespacedName{Name: dqo.Name}
		latestDQO := &kueuealpha.DynamicQuotaOrchestrator{}

		ginkgo.By("Verifying unmanaged flavor is ignored and multiplier scales resources", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, dqoKey, latestDQO)).Should(gomega.Succeed())
				g.Expect(latestDQO.Status.Conditions).Should(utiltesting.HaveConditionStatusTrueAndReason(
					kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					kueuealpha.DynamicQuotaOrchestratorReasonComputed,
				))

				wantCapacity := utiltestingalpha.MakeEffectiveCapacity().
					Flavors(
						*utiltestingalpha.MakeEffectiveCapacityFlavor("allowed-flavor").
							Resource(corev1.ResourceCPU, "50").
							Resource(corev1.ResourceMemory, "25Gi").
							Obj(),
					).
					Obj()
				g.Expect(cmp.Diff(wantCapacity, latestDQO.Status.EffectiveCapacity, cmpopts.EquateEmpty())).Should(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should distribute capacity to a single ClusterQueue and update dynamically", func() {
		cp := utiltestingalpha.MakeCapacityProvider("dist-cq-cp").
			ControllerName("example.com/test-provider").
			OrchestratedFlavors("f1").
			Capacity(
				utiltestingalpha.MakeNormalizedCapacity().
					Flavors(
						utiltestingalpha.MakeNormalizedCapacityFlavor("f1").
							Resource(corev1.ResourceCPU, "100").
							Resource(corev1.ResourceMemory, "50Gi").
							Obj(),
					).
					Obj(),
			).
			Condition(metav1.Condition{
				Type:    kueuealpha.CapacityProviderCapacitySynchronized,
				Status:  metav1.ConditionTrue,
				Reason:  kueuealpha.CapacityProviderReasonSynchronized,
				Message: "Capacity synchronized successfully",
			}).
			Obj()
		cps = append(cps, cp)
		createCapacityProvider(ctx, k8sClient, cp)

		cq = utiltestingapi.MakeClusterQueue("dist-cq").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("f1").
					Resource(corev1.ResourceCPU, "10").
					Resource(corev1.ResourceMemory, "20Gi").
					Obj(),
			).
			Obj()
		util.MustCreate(ctx, k8sClient, cq)

		dqo = utiltestingalpha.MakeDynamicQuotaOrchestrator("dist-dqo").
			DiscoveryProvider("dist-cq-cp", nil).
			SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "dist-cq").
			Obj()
		util.MustCreate(ctx, k8sClient, dqo)

		cqKey := types.NamespacedName{Name: cq.Name}
		latestCQ := &kueue.ClusterQueue{}

		ginkgo.By("Verifying initial effective quota distribution", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, cqKey, latestCQ)).Should(gomega.Succeed())
				g.Expect(latestCQ.Status.EffectiveQuotas).ShouldNot(gomega.BeNil())
				g.Expect(latestCQ.Status.EffectiveQuotas.OrchestratorRef).Should(gomega.Equal(kueue.EffectiveQuotaStatusOrchestratorRef{
					APIGroup: "kueue.x-k8s.io",
					Kind:     "DynamicQuotaOrchestrator",
					Name:     "dist-dqo",
				}))

				wantResourceGroups := []kueue.ResourceGroup{
					utiltestingapi.ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("f1").
							Resource(corev1.ResourceCPU, "100").
							Resource(corev1.ResourceMemory, "50Gi").
							Obj(),
					),
				}
				g.Expect(cmp.Diff(wantResourceGroups, latestCQ.Status.EffectiveQuotas.ResourceGroups, cmpopts.EquateEmpty())).Should(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Updating capacity and verifying ClusterQueue effective quota updates", func() {
			updatedCapacity := utiltestingalpha.MakeNormalizedCapacity().
				Flavors(
					utiltestingalpha.MakeNormalizedCapacityFlavor("f1").
						Resource(corev1.ResourceCPU, "200").
						Resource(corev1.ResourceMemory, "80Gi").
						Obj(),
				).
				Obj()
			setCapacityProviderCapacity(ctx, k8sClient, cp, updatedCapacity)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, cqKey, latestCQ)).Should(gomega.Succeed())
				wantUpdatedResourceGroups := []kueue.ResourceGroup{
					utiltestingapi.ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("f1").
							Resource(corev1.ResourceCPU, "200").
							Resource(corev1.ResourceMemory, "80Gi").
							Obj(),
					),
				}
				g.Expect(cmp.Diff(wantUpdatedResourceGroups, latestCQ.Status.EffectiveQuotas.ResourceGroups, cmpopts.EquateEmpty())).Should(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	// Cohort tree hierarchy (3 levels):
	//
	//              root-cohort (Level 1)
	//             /           \
	//           cq-1       child-cohort (Level 2, 20 CPU)
	//          (10 CPU)   /            \
	//                   cq-2     grandchild-cohort (Level 3)
	//                  (10 CPU)         |
	//                                  cq-3
	//                                 (30 CPU)
	ginkgo.It("Should distribute capacity proportionally across 3-level Cohort tree with Cohort and CQ participants", func() {
		cp := utiltestingalpha.MakeCapacityProvider("cohort-tree-cp").
			ControllerName("example.com/test-provider").
			OrchestratedFlavors("f1").
			Capacity(
				utiltestingalpha.MakeNormalizedCapacity().
					Flavors(
						utiltestingalpha.MakeNormalizedCapacityFlavor("f1").
							Resource(corev1.ResourceCPU, "70").
							Obj(),
					).
					Obj(),
			).
			Condition(metav1.Condition{
				Type:    kueuealpha.CapacityProviderCapacitySynchronized,
				Status:  metav1.ConditionTrue,
				Reason:  kueuealpha.CapacityProviderReasonSynchronized,
				Message: "Capacity synchronized successfully",
			}).
			Obj()
		cps = append(cps, cp)
		createCapacityProvider(ctx, k8sClient, cp)

		rootCohort = utiltestingapi.MakeCohort("root-cohort").Obj()
		util.MustCreate(ctx, k8sClient, rootCohort)

		childCohort = utiltestingapi.MakeCohort("child-cohort").
			Parent("root-cohort").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("f1").Resource(corev1.ResourceCPU, "20").Obj(),
			).
			Obj()
		util.MustCreate(ctx, k8sClient, childCohort)

		grandchildCohort = utiltestingapi.MakeCohort("grandchild-cohort").Parent("child-cohort").Obj()
		util.MustCreate(ctx, k8sClient, grandchildCohort)

		cq1 = utiltestingapi.MakeClusterQueue("cq-1").
			Cohort("root-cohort").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("f1").Resource(corev1.ResourceCPU, "10").Obj(),
			).
			Obj()
		util.MustCreate(ctx, k8sClient, cq1)

		cq2 = utiltestingapi.MakeClusterQueue("cq-2").
			Cohort("child-cohort").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("f1").Resource(corev1.ResourceCPU, "10").Obj(),
			).
			Obj()
		util.MustCreate(ctx, k8sClient, cq2)

		cq3 = utiltestingapi.MakeClusterQueue("cq-3").
			Cohort("grandchild-cohort").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("f1").Resource(corev1.ResourceCPU, "30").Obj(),
			).
			Obj()
		util.MustCreate(ctx, k8sClient, cq3)

		dqo = utiltestingalpha.MakeDynamicQuotaOrchestrator("cohort-dqo").
			DiscoveryProvider("cohort-tree-cp", nil).
			SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "root-cohort").
			Obj()
		util.MustCreate(ctx, k8sClient, dqo)

		childCohortKey := types.NamespacedName{Name: childCohort.Name}
		cq1Key := types.NamespacedName{Name: cq1.Name}
		cq2Key := types.NamespacedName{Name: cq2.Name}
		cq3Key := types.NamespacedName{Name: cq3.Name}

		ginkgo.By("Verifying proportional distribution to Cohorts and CQs across all 3 levels of the cohort tree", func() {
			var gotChildCohort kueue.Cohort
			var gotCQ1, gotCQ2, gotCQ3 kueue.ClusterQueue
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, childCohortKey, &gotChildCohort)).Should(gomega.Succeed())
				g.Expect(gotChildCohort.Status.EffectiveQuotas).ShouldNot(gomega.BeNil())
				g.Expect(gotChildCohort.Status.EffectiveQuotas.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota).
					Should(gomega.Equal(resource.MustParse("20")))

				g.Expect(k8sClient.Get(ctx, cq1Key, &gotCQ1)).Should(gomega.Succeed())
				g.Expect(gotCQ1.Status.EffectiveQuotas).ShouldNot(gomega.BeNil())
				g.Expect(gotCQ1.Status.EffectiveQuotas.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota).
					Should(gomega.Equal(resource.MustParse("10")))

				g.Expect(k8sClient.Get(ctx, cq2Key, &gotCQ2)).Should(gomega.Succeed())
				g.Expect(gotCQ2.Status.EffectiveQuotas).ShouldNot(gomega.BeNil())
				g.Expect(gotCQ2.Status.EffectiveQuotas.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota).
					Should(gomega.Equal(resource.MustParse("10")))

				g.Expect(k8sClient.Get(ctx, cq3Key, &gotCQ3)).Should(gomega.Succeed())
				g.Expect(gotCQ3.Status.EffectiveQuotas).ShouldNot(gomega.BeNil())
				g.Expect(gotCQ3.Status.EffectiveQuotas.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota).
					Should(gomega.Equal(resource.MustParse("30")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should soft-validate overlapping DQOs and activate once conflict is removed", func() {
		cp := utiltestingalpha.MakeCapacityProvider("overlap-cp").
			ControllerName("example.com/test-provider").
			OrchestratedFlavors("f1").
			Capacity(
				utiltestingalpha.MakeNormalizedCapacity().
					Flavors(
						utiltestingalpha.MakeNormalizedCapacityFlavor("f1").
							Resource(corev1.ResourceCPU, "100").
							Obj(),
					).
					Obj(),
			).
			Condition(metav1.Condition{
				Type:    kueuealpha.CapacityProviderCapacitySynchronized,
				Status:  metav1.ConditionTrue,
				Reason:  kueuealpha.CapacityProviderReasonSynchronized,
				Message: "Capacity synchronized successfully",
			}).
			Obj()
		cps = append(cps, cp)
		createCapacityProvider(ctx, k8sClient, cp)

		rootCohort = utiltestingapi.MakeCohort("overlap-root").Obj()
		util.MustCreate(ctx, k8sClient, rootCohort)

		childCohort = utiltestingapi.MakeCohort("overlap-child").Parent("overlap-root").Obj()
		util.MustCreate(ctx, k8sClient, childCohort)

		cq = utiltestingapi.MakeClusterQueue("overlap-cq").
			Cohort("overlap-child").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("f1").Resource(corev1.ResourceCPU, "50").Obj(),
			).
			Obj()
		util.MustCreate(ctx, k8sClient, cq)

		ancestorDQO = utiltestingalpha.MakeDynamicQuotaOrchestrator("ancestor-dqo").
			DiscoveryProvider("overlap-cp", nil).
			SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "overlap-root").
			Obj()
		util.MustCreate(ctx, k8sClient, ancestorDQO)

		childDQO = utiltestingalpha.MakeDynamicQuotaOrchestrator("child-dqo").
			DiscoveryProvider("overlap-cp", nil).
			SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "overlap-child").
			Obj()
		util.MustCreate(ctx, k8sClient, childDQO)

		childDQOKey := types.NamespacedName{Name: childDQO.Name}
		latestChildDQO := &kueuealpha.DynamicQuotaOrchestrator{}

		ginkgo.By("Verifying child DQO is deactivated due to ancestor conflict", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, childDQOKey, latestChildDQO)).Should(gomega.Succeed())
				g.Expect(latestChildDQO.Status.Conditions).Should(utiltesting.HaveConditionStatusFalseAndReason(
					kueuealpha.DynamicQuotaOrchestratorDistributed,
					kueuealpha.DynamicQuotaOrchestratorReasonConflictingDynamicQuotaOrchestrator,
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Deleting ancestor DQO and verifying child DQO becomes active", func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, ancestorDQO, true)
			ancestorDQO = nil

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, childDQOKey, latestChildDQO)).Should(gomega.Succeed())
				g.Expect(latestChildDQO.Status.Conditions).Should(utiltesting.HaveConditionStatusTrueAndReason(
					kueuealpha.DynamicQuotaOrchestratorDistributed,
					kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed,
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should soft-validate DQOs targeting the same subtree root and assign deterministic owner", func() {
		cp := utiltestingalpha.MakeCapacityProvider("identical-cp").
			ControllerName("example.com/test-provider").
			OrchestratedFlavors("f1").
			Capacity(
				utiltestingalpha.MakeNormalizedCapacity().
					Flavors(
						utiltestingalpha.MakeNormalizedCapacityFlavor("f1").
							Resource(corev1.ResourceCPU, "100").
							Obj(),
					).
					Obj(),
			).
			Condition(metav1.Condition{
				Type:    kueuealpha.CapacityProviderCapacitySynchronized,
				Status:  metav1.ConditionTrue,
				Reason:  kueuealpha.CapacityProviderReasonSynchronized,
				Message: "Capacity synchronized successfully",
			}).
			Obj()
		cps = append(cps, cp)
		createCapacityProvider(ctx, k8sClient, cp)

		rootCohort = utiltestingapi.MakeCohort("identical-root").Obj()
		util.MustCreate(ctx, k8sClient, rootCohort)

		cq = utiltestingapi.MakeClusterQueue("identical-cq").
			Cohort("identical-root").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("f1").Resource(corev1.ResourceCPU, "50").Obj(),
			).
			Obj()
		util.MustCreate(ctx, k8sClient, cq)

		ancestorDQO = utiltestingalpha.MakeDynamicQuotaOrchestrator("older-dqo").
			DiscoveryProvider("identical-cp", nil).
			SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "identical-root").
			Obj()
		util.MustCreate(ctx, k8sClient, ancestorDQO)

		olderDQOKey := types.NamespacedName{Name: ancestorDQO.Name}
		latestOlderDQO := &kueuealpha.DynamicQuotaOrchestrator{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, olderDQOKey, latestOlderDQO)).Should(gomega.Succeed())
			g.Expect(latestOlderDQO.Status.Conditions).Should(utiltesting.HaveConditionStatusTrueAndReason(
				kueuealpha.DynamicQuotaOrchestratorDistributed,
				kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed,
			))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// Ensure newer-dqo gets a strictly later CreationTimestamp.
		time.Sleep(1 * time.Second)

		childDQO = utiltestingalpha.MakeDynamicQuotaOrchestrator("newer-dqo").
			DiscoveryProvider("identical-cp", nil).
			SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "identical-root").
			Obj()
		util.MustCreate(ctx, k8sClient, childDQO)

		newerDQOKey := types.NamespacedName{Name: childDQO.Name}
		latestNewerDQO := &kueuealpha.DynamicQuotaOrchestrator{}

		ginkgo.By("Verifying newer DQO is deactivated due to identical root conflict", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, newerDQOKey, latestNewerDQO)).Should(gomega.Succeed())
				g.Expect(latestNewerDQO.Status.Conditions).Should(utiltesting.HaveConditionStatusFalseAndReason(
					kueuealpha.DynamicQuotaOrchestratorDistributed,
					kueuealpha.DynamicQuotaOrchestratorReasonConflictingDynamicQuotaOrchestrator,
				))

				g.Expect(k8sClient.Get(ctx, olderDQOKey, latestOlderDQO)).Should(gomega.Succeed())
				g.Expect(latestOlderDQO.Status.Conditions).Should(utiltesting.HaveConditionStatusTrueAndReason(
					kueuealpha.DynamicQuotaOrchestratorDistributed,
					kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed,
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Deleting older DQO and verifying newer DQO becomes active", func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, ancestorDQO, true)
			ancestorDQO = nil

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, newerDQOKey, latestNewerDQO)).Should(gomega.Succeed())
				g.Expect(latestNewerDQO.Status.Conditions).Should(utiltesting.HaveConditionStatusTrueAndReason(
					kueuealpha.DynamicQuotaOrchestratorDistributed,
					kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed,
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})

func createCapacityProvider(
	ctx context.Context,
	k8sClient client.Client,
	cp *kueuealpha.CapacityProvider,
) {
	ginkgo.GinkgoHelper()
	status := cp.Status.DeepCopy()
	util.MustCreate(ctx, k8sClient, cp)
	if status.Capacity != nil || len(status.Conditions) > 0 {
		for i := range status.Conditions {
			if status.Conditions[i].LastTransitionTime.IsZero() {
				status.Conditions[i].LastTransitionTime = metav1.Now()
			}
		}
		cp.Status = *status
		gomega.Expect(k8sClient.Status().Update(ctx, cp)).Should(gomega.Succeed())
	}
}

func setCapacityProviderCapacity(
	ctx context.Context,
	k8sClient client.Client,
	cp *kueuealpha.CapacityProvider,
	capacity *kueuealpha.CapacityProviderNormalizedCapacity,
) {
	var latestCp kueuealpha.CapacityProvider
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cp), &latestCp)).Should(gomega.Succeed())
		latestCp.Status.Capacity = capacity
		apimeta.SetStatusCondition(&latestCp.Status.Conditions, metav1.Condition{
			Type:               kueuealpha.CapacityProviderCapacitySynchronized,
			Status:             metav1.ConditionTrue,
			Reason:             kueuealpha.CapacityProviderReasonSynchronized,
			Message:            "Capacity synchronized successfully",
			ObservedGeneration: latestCp.Generation,
		})
		g.Expect(k8sClient.Status().Update(ctx, &latestCp)).Should(gomega.Succeed())
	}, util.Timeout, util.Interval).Should(gomega.Succeed(), util.AssertMsg("Failed to update CapacityProvider status", &latestCp))
}

func setCapacityProviderSyncCondition(
	ctx context.Context,
	k8sClient client.Client,
	cp *kueuealpha.CapacityProvider,
	status metav1.ConditionStatus,
	reason, message string,
) {
	var latestCp kueuealpha.CapacityProvider
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cp), &latestCp)).Should(gomega.Succeed())
		apimeta.SetStatusCondition(&latestCp.Status.Conditions, metav1.Condition{
			Type:               kueuealpha.CapacityProviderCapacitySynchronized,
			Status:             status,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: latestCp.Generation,
		})
		g.Expect(k8sClient.Status().Update(ctx, &latestCp)).Should(gomega.Succeed())
	}, util.Timeout, util.Interval).Should(gomega.Succeed(), util.AssertMsg("Failed to update CapacityProvider condition", &latestCp))
}
