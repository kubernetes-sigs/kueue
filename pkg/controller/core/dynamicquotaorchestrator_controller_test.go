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

package core

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestDynamicQuotaOrchestratorReconcile(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, true)
	halfMultiplier := resource.MustParse("0.5")

	timeNow := time.Now()
	timeEarlier := timeNow.Add(-10 * time.Minute)

	cases := map[string]struct {
		enableFeatureGate *bool
		dqo               *kueuealpha.DynamicQuotaOrchestrator
		capacityProviders []*kueuealpha.CapacityProvider
		cohorts           []*kueue.Cohort
		clusterQueues     []*kueue.ClusterQueue
		otherDQOs         []*kueuealpha.DynamicQuotaOrchestrator
		wantDQO           *kueuealpha.DynamicQuotaOrchestrator
		wantCohorts       []*kueue.Cohort
		wantClusterQueues []*kueue.ClusterQueue
		wantErr           bool
	}{
		"discovery-only: provider not found": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-1").
				DiscoveryProvider("non-existent-provider", nil).
				Obj(),
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-1").
				DiscoveryProvider("non-existent-provider", nil).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionFalse,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonMisconfigured,
					Message: "CapacityProvider \"non-existent-provider\" not found",
				}).
				Obj(),
			wantErr: false,
		},
		"discovery-only: provider not ready (no condition)": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-1").
				DiscoveryProvider("cp-1", nil).
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-1").
				DiscoveryProvider("cp-1", nil).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionFalse,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonProviderNotReady,
					Message: "CapacityProvider \"cp-1\" is not synchronized",
				}).
				Obj(),
			wantErr: false,
		},
		"discovery-only: single provider aggregated successfully": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-1").
				DiscoveryProvider("cp-1", nil).
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					OrchestratedFlavors("default-flavor").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavors(
							utiltestingalpha.MakeNormalizedCapacityFlavor("default-flavor").
								Resource(corev1.ResourceCPU, "100").
								Resource(corev1.ResourceMemory, "50Gi").
								Obj(),
						).
						Obj()).
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-1").
				DiscoveryProvider("cp-1", nil).
				EffectiveCapacity(utiltestingalpha.MakeEffectiveCapacity().
					Flavors(
						*utiltestingalpha.MakeEffectiveCapacityFlavor("default-flavor").
							Resource(corev1.ResourceCPU, "100").
							Resource(corev1.ResourceMemory, "50Gi").
							Obj(),
					).
					Obj(),
				).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonComputed,
					Message: "Aggregated capacity successfully computed",
				}).
				Obj(),
		},
		"discovery-only: multiple providers with multipliers": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-1").
				DiscoveryProvider("cp-1", &halfMultiplier).
				DiscoveryProvider("cp-2", &halfMultiplier).
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					OrchestratedFlavors("default-flavor").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavors(
							utiltestingalpha.MakeNormalizedCapacityFlavor("default-flavor").
								Resource(corev1.ResourceCPU, "100").
								Obj(),
						).
						Obj()).
					Obj(),
				utiltestingalpha.MakeCapacityProvider("cp-2").
					OrchestratedFlavors("default-flavor", "gpu-flavor").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavors(
							utiltestingalpha.MakeNormalizedCapacityFlavor("default-flavor").
								Resource(corev1.ResourceCPU, "200").
								Obj(),
							utiltestingalpha.MakeNormalizedCapacityFlavor("gpu-flavor").
								Resource("nvidia.com/gpu", "8").
								Obj(),
						).
						Obj()).
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-1").
				DiscoveryProvider("cp-1", &halfMultiplier).
				DiscoveryProvider("cp-2", &halfMultiplier).
				EffectiveCapacity(utiltestingalpha.MakeEffectiveCapacity().
					Flavors(
						*utiltestingalpha.MakeEffectiveCapacityFlavor("default-flavor").
							Resource(corev1.ResourceCPU, "150").
							Obj(),
						*utiltestingalpha.MakeEffectiveCapacityFlavor("gpu-flavor").
							Resource("nvidia.com/gpu", "4").
							Obj(),
					).
					Obj(),
				).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonComputed,
					Message: "Aggregated capacity successfully computed",
				}).
				Obj(),
		},
		"discovery-only: filters flavors not declared in spec.orchestratedFlavors": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-filter").
				DiscoveryProvider("cp-1", nil).
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					OrchestratedFlavors("allowed-flavor").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavors(
							utiltestingalpha.MakeNormalizedCapacityFlavor("allowed-flavor").
								Resource(corev1.ResourceCPU, "50").
								Obj(),
							utiltestingalpha.MakeNormalizedCapacityFlavor("unorchestrated-flavor").
								Resource(corev1.ResourceCPU, "50").
								Obj(),
						).
						Obj()).
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-filter").
				DiscoveryProvider("cp-1", nil).
				EffectiveCapacity(utiltestingalpha.MakeEffectiveCapacity().
					Flavors(
						*utiltestingalpha.MakeEffectiveCapacityFlavor("allowed-flavor").
							Resource(corev1.ResourceCPU, "50").
							Obj(),
					).
					Obj(),
				).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonComputed,
					Message: "Aggregated capacity successfully computed",
				}).
				Obj(),
		},
		"discovery-only: no matching orchestrated flavors": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-no-match").
				DiscoveryProvider("cp-1", nil).
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					OrchestratedFlavors("other-flavor").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavors(
							utiltestingalpha.MakeNormalizedCapacityFlavor("provider-flavor").
								Resource(corev1.ResourceCPU, "10").
								Obj(),
						).
						Obj()).
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-no-match").
				DiscoveryProvider("cp-1", nil).
				EffectiveCapacity(utiltestingalpha.MakeEffectiveCapacity().
					Flavors().
					Obj(),
				).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonComputed,
					Message: "Aggregated capacity successfully computed",
				}).
				Obj(),
		},
		"discovery-only: provider reports empty capacity": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-empty").
				DiscoveryProvider("cp-1", nil).
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					OrchestratedFlavors("default-flavor").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().Obj()).
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-empty").
				DiscoveryProvider("cp-1", nil).
				EffectiveCapacity(utiltestingalpha.MakeEffectiveCapacity().
					Flavors().
					Obj(),
				).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonComputed,
					Message: "Aggregated capacity successfully computed",
				}).
				Obj(),
		},
		"discovery-only: provider reports nil capacity with synchronized condition": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-nil-capacity").
				DiscoveryProvider("cp-1", nil).
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					OrchestratedFlavors("default-flavor").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-nil-capacity").
				DiscoveryProvider("cp-1", nil).
				EffectiveCapacity(utiltestingalpha.MakeEffectiveCapacity().
					Flavors().
					Obj(),
				).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonComputed,
					Message: "Aggregated capacity successfully computed",
				}).
				Obj(),
		},
		"distribution: discovery not ready sets EffectiveCapacityNotComputed condition": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-dist-not-ready").
				DiscoveryProvider("non-existent-provider", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-1").
				Obj(),
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-dist-not-ready").
				DiscoveryProvider("non-existent-provider", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-1").
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionFalse,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonMisconfigured,
					Message: "CapacityProvider \"non-existent-provider\" not found",
				}).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorDistributed,
					Status:  metav1.ConditionFalse,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonEffectiveCapacityNotComputed,
					Message: "Capacity discovery not ready",
				}).
				Obj(),
			wantClusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").Obj(),
			},
			wantErr: false,
		},
		"distribution: to single ClusterQueue": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-cq").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-1").
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					OrchestratedFlavors("default-flavor").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavors(
							utiltestingalpha.MakeNormalizedCapacityFlavor("default-flavor").
								Resource(corev1.ResourceCPU, "200").
								Obj(),
						).
						Obj()).
					Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50", "20").Obj(),
					).
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-cq").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-1").
				EffectiveCapacity(utiltestingalpha.MakeEffectiveCapacity().
					Flavors(
						*utiltestingalpha.MakeEffectiveCapacityFlavor("default-flavor").
							Resource(corev1.ResourceCPU, "200").
							Obj(),
					).
					Obj()).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonComputed,
					Message: "Aggregated capacity successfully computed",
				}).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorDistributed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed,
					Message: "Quotas successfully distributed",
				}).
				Obj(),
			wantClusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").
					EffectiveQuotaStatus(
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("dqo-cq").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "200", "20").Obj(),
							)).
							Obj(),
					).
					Obj(),
			},
		},
		"distribution: caps lendingLimit on ClusterQueue at effective nominalQuota, but preserves lendingLimit on Cohort": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-lending").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "root-cohort").
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					OrchestratedFlavors("default-flavor").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavors(
							utiltestingalpha.MakeNormalizedCapacityFlavor("default-flavor").
								Resource(corev1.ResourceCPU, "80").
								Obj(),
						).
						Obj()).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("root-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "100", "", "80").Obj(),
					).
					Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").
					Cohort("root-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "100", "", "80").Obj(),
					).
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-lending").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "root-cohort").
				EffectiveCapacity(utiltestingalpha.MakeEffectiveCapacity().
					Flavors(
						*utiltestingalpha.MakeEffectiveCapacityFlavor("default-flavor").
							Resource(corev1.ResourceCPU, "80").
							Obj(),
					).
					Obj()).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonComputed,
					Message: "Aggregated capacity successfully computed",
				}).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorDistributed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed,
					Message: "Quotas successfully distributed",
				}).
				Obj(),
			wantCohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("root-cohort").
					EffectiveQuotaStatus(
						// For Cohort, non-null lendingLimit is preserved unchanged (80) even when nominalQuota is 40.
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("dqo-lending").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "40", "", "80").Obj(),
							)).
							Obj(),
					).
					Obj(),
			},
			wantClusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").
					Cohort("root-cohort").
					EffectiveQuotaStatus(
						// For ClusterQueue, non-null lendingLimit is capped at effective nominalQuota (40).
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("dqo-lending").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "40", "", "40").Obj(),
							)).
							Obj(),
					).
					Obj(),
			},
		},
		"distribution: empty spec.resourceGroups sets empty resourceGroups in effectiveQuotas": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-empty").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-empty").
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					OrchestratedFlavors("default-flavor").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavors(
							utiltestingalpha.MakeNormalizedCapacityFlavor("default-flavor").
								Resource(corev1.ResourceCPU, "100").
								Obj(),
						).
						Obj()).
					Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-empty").Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-empty").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-empty").
				EffectiveCapacity(utiltestingalpha.MakeEffectiveCapacity().
					Flavors(
						*utiltestingalpha.MakeEffectiveCapacityFlavor("default-flavor").
							Resource(corev1.ResourceCPU, "100").
							Obj(),
					).
					Obj()).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonComputed,
					Message: "Aggregated capacity successfully computed",
				}).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorDistributed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed,
					Message: "Quotas successfully distributed",
				}).
				Obj(),
			wantClusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-empty").
					EffectiveQuotaStatus(utiltestingapi.MakeEffectiveQuotaStatus().Name("dqo-empty").Obj()).
					Obj(),
			},
		},
		"distribution: previously managed CQ removed from cohort retains effective quotas as stale": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-1").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "root-cohort").
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					OrchestratedFlavors("default-flavor").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavors(
							utiltestingalpha.MakeNormalizedCapacityFlavor("default-flavor").
								Resource(corev1.ResourceCPU, "100").
								Obj(),
						).
						Obj()).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("root-cohort").Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").
					Cohort("root-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").
							Resource(corev1.ResourceCPU, "100").
							Obj(),
					).
					Obj(),
				utiltestingapi.MakeClusterQueue("cq-removed").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					EffectiveQuotaStatus(utiltestingapi.MakeEffectiveQuotaStatus().Name("dqo-1").Obj()).
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-1").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "root-cohort").
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonComputed,
					Message: "Aggregated capacity successfully computed",
				}).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorDistributed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed,
					Message: "Quotas successfully distributed",
				}).
				EffectiveCapacity(utiltestingalpha.MakeEffectiveCapacity().
					Flavors(
						*utiltestingalpha.MakeEffectiveCapacityFlavor("default-flavor").
							Resource(corev1.ResourceCPU, "100").
							Obj(),
					).
					Obj()).
				Obj(),
			wantClusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").
					EffectiveQuotaStatus(
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("dqo-1").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "100").Obj(),
							)).
							Obj(),
					).
					Obj(),
				utiltestingapi.MakeClusterQueue("cq-removed").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					EffectiveQuotaStatus(utiltestingapi.MakeEffectiveQuotaStatus().Name("dqo-1").Obj()).
					Obj(),
			},
			wantErr: false,
		},
		"soft validation: child DQO deactivated when ancestor DQO is distributing": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-child").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "child-cohort").
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					OrchestratedFlavors("default-flavor").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavors(
							utiltestingalpha.MakeNormalizedCapacityFlavor("default-flavor").
								Resource(corev1.ResourceCPU, "100").
								Obj(),
						).
						Obj()).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("root-cohort").Obj(),
				utiltestingapi.MakeCohort("child-cohort").Parent("root-cohort").Obj(),
			},
			otherDQOs: []*kueuealpha.DynamicQuotaOrchestrator{
				utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-ancestor").
					DiscoveryProvider("cp-1", nil).
					SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "root-cohort").
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-child").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "child-cohort").
				EffectiveCapacity(utiltestingalpha.MakeEffectiveCapacity().
					Flavors(
						*utiltestingalpha.MakeEffectiveCapacityFlavor("default-flavor").
							Resource(corev1.ResourceCPU, "100").
							Obj(),
					).
					Obj()).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonComputed,
					Message: "Aggregated capacity successfully computed",
				}).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorDistributed,
					Status:  metav1.ConditionFalse,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonConflictingDynamicQuotaOrchestrator,
					Message: "Conflicts with ancestor DynamicQuotaOrchestrator \"dqo-ancestor\"",
				}).
				Obj(),
		},
		"soft validation: ancestor DQO takes precedence and overwrites quotas set by descendant DQO": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("parent-dqo").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "parent-cohort").
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					OrchestratedFlavors("default-flavor").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavors(
							utiltestingalpha.MakeNormalizedCapacityFlavor("default-flavor").
								Resource(corev1.ResourceCPU, "100").
								Obj(),
						).
						Obj()).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("parent-cohort").Obj(),
				utiltestingapi.MakeCohort("child-cohort").Parent("parent-cohort").Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("child-cq").
					Cohort("child-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "100").Obj(),
					).
					EffectiveQuotaStatus(
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("child-dqo").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
							)).
							Obj(),
					).
					Obj(),
			},
			otherDQOs: []*kueuealpha.DynamicQuotaOrchestrator{
				utiltestingalpha.MakeDynamicQuotaOrchestrator("child-dqo").
					DiscoveryProvider("cp-1", nil).
					SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "child-cohort").
					Condition(metav1.Condition{
						Type:   kueuealpha.DynamicQuotaOrchestratorDistributed,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed,
					}).
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("parent-dqo").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "parent-cohort").
				EffectiveCapacity(utiltestingalpha.MakeEffectiveCapacity().
					Flavors(
						*utiltestingalpha.MakeEffectiveCapacityFlavor("default-flavor").
							Resource(corev1.ResourceCPU, "100").
							Obj(),
					).
					Obj()).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonComputed,
					Message: "Aggregated capacity successfully computed",
				}).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorDistributed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed,
					Message: "Quotas successfully distributed",
				}).
				Obj(),
			wantClusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("child-cq").
					Cohort("child-cohort").
					EffectiveQuotaStatus(
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("parent-dqo").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "100").Obj(),
							)).
							Obj(),
					).
					Obj(),
			},
		},
		"soft validation: duplicate root DQO deactivated by creation timestamp tie-break": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-newer").
				Creation(timeNow).
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-1").
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					OrchestratedFlavors("default-flavor").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavors(
							utiltestingalpha.MakeNormalizedCapacityFlavor("default-flavor").
								Resource(corev1.ResourceCPU, "100").
								Obj(),
						).
						Obj()).
					Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					Obj(),
			},
			otherDQOs: []*kueuealpha.DynamicQuotaOrchestrator{
				utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-older").
					Creation(timeEarlier).
					DiscoveryProvider("cp-1", nil).
					SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-1").
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-newer").
				Creation(timeNow).
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-1").
				EffectiveCapacity(utiltestingalpha.MakeEffectiveCapacity().
					Flavors(
						*utiltestingalpha.MakeEffectiveCapacityFlavor("default-flavor").
							Resource(corev1.ResourceCPU, "100").
							Obj(),
					).
					Obj()).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonComputed,
					Message: "Aggregated capacity successfully computed",
				}).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorDistributed,
					Status:  metav1.ConditionFalse,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonConflictingDynamicQuotaOrchestrator,
					Message: "Conflicts with older DynamicQuotaOrchestrator \"dqo-older\"",
				}).
				Obj(),
		},
		"soft validation: older DQO takes over effective quotas on same root from younger DQO": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-older").
				Creation(timeEarlier).
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-1").
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					OrchestratedFlavors("default-flavor").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavors(
							utiltestingalpha.MakeNormalizedCapacityFlavor("default-flavor").
								Resource(corev1.ResourceCPU, "100").
								Obj(),
						).
						Obj()).
					Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					EffectiveQuotaStatus(
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("dqo-younger").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
							)).
							Obj(),
					).
					Obj(),
			},
			otherDQOs: []*kueuealpha.DynamicQuotaOrchestrator{
				utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-younger").
					Creation(timeNow).
					DiscoveryProvider("cp-1", nil).
					SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-1").
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-older").
				Creation(timeEarlier).
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-1").
				EffectiveCapacity(utiltestingalpha.MakeEffectiveCapacity().
					Flavors(
						*utiltestingalpha.MakeEffectiveCapacityFlavor("default-flavor").
							Resource(corev1.ResourceCPU, "100").
							Obj(),
					).
					Obj()).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonComputed,
					Message: "Aggregated capacity successfully computed",
				}).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorDistributed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed,
					Message: "Quotas successfully distributed",
				}).
				Obj(),
			wantClusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					EffectiveQuotaStatus(
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("dqo-older").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "100").Obj(),
							)).
							Obj(),
					).
					Obj(),
			},
			wantErr: false,
		},
		"soft validation: child DQO deactivates and retains previously managed effective quotas when ancestor DQO distributes": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("child-dqo").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "child-cohort").
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					OrchestratedFlavors("default-flavor").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavors(
							utiltestingalpha.MakeNormalizedCapacityFlavor("default-flavor").
								Resource(corev1.ResourceCPU, "100").
								Obj(),
						).
						Obj()).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("parent-cohort").Obj(),
				utiltestingapi.MakeCohort("child-cohort").Parent("parent-cohort").Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("child-cq").
					Cohort("child-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					EffectiveQuotaStatus(utiltestingapi.MakeEffectiveQuotaStatus().Name("child-dqo").Obj()).
					Obj(),
			},
			otherDQOs: []*kueuealpha.DynamicQuotaOrchestrator{
				utiltestingalpha.MakeDynamicQuotaOrchestrator("parent-dqo").
					DiscoveryProvider("cp-1", nil).
					SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "parent-cohort").
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("child-dqo").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "child-cohort").
				EffectiveCapacity(utiltestingalpha.MakeEffectiveCapacity().
					Flavors(
						*utiltestingalpha.MakeEffectiveCapacityFlavor("default-flavor").
							Resource(corev1.ResourceCPU, "100").
							Obj(),
					).
					Obj()).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonComputed,
					Message: "Aggregated capacity successfully computed",
				}).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorDistributed,
					Status:  metav1.ConditionFalse,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonConflictingDynamicQuotaOrchestrator,
					Message: "Conflicts with ancestor DynamicQuotaOrchestrator \"parent-dqo\"",
				}).
				Obj(),
			wantClusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("child-cq").
					Cohort("child-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					EffectiveQuotaStatus(utiltestingapi.MakeEffectiveQuotaStatus().Name("child-dqo").Obj()).
					Obj(),
			},
			wantErr: false,
		},
		"soft validation: effective quotas conflict when managed by another DynamicQuotaOrchestrator": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-1").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-1").
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					OrchestratedFlavors("default-flavor").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavors(
							utiltestingalpha.MakeNormalizedCapacityFlavor("default-flavor").
								Resource(corev1.ResourceCPU, "100").
								Obj(),
						).
						Obj()).
					Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					EffectiveQuotaStatus(utiltestingapi.MakeEffectiveQuotaStatus().Name("other-dqo").Obj()).
					Obj(),
			},
			otherDQOs: []*kueuealpha.DynamicQuotaOrchestrator{
				utiltestingalpha.MakeDynamicQuotaOrchestrator("other-dqo").
					DiscoveryProvider("cp-1", nil).
					SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-other").
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-1").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-1").
				EffectiveCapacity(utiltestingalpha.MakeEffectiveCapacity().
					Flavors(
						*utiltestingalpha.MakeEffectiveCapacityFlavor("default-flavor").
							Resource(corev1.ResourceCPU, "100").
							Obj(),
					).
					Obj()).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonComputed,
					Message: "Aggregated capacity successfully computed",
				}).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorDistributed,
					Status:  metav1.ConditionFalse,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonEffectiveQuotasConflict,
					Message: "ClusterQueue \"cq-1\" already managed by DynamicQuotaOrchestrator/other-dqo",
				}).
				Obj(),
		},
		"transition: switch to discovery-only preserves stale effective quotas and removes Distributed condition": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-discovery-only").
				DiscoveryProvider("cp-1", nil).
				Condition(metav1.Condition{
					Type:   kueuealpha.DynamicQuotaOrchestratorDistributed,
					Status: metav1.ConditionTrue,
					Reason: kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed,
				}).
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					OrchestratedFlavors("default-flavor").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavors(
							utiltestingalpha.MakeNormalizedCapacityFlavor("default-flavor").
								Resource(corev1.ResourceCPU, "100").
								Obj(),
						).
						Obj()).
					Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					EffectiveQuotaStatus(utiltestingapi.MakeEffectiveQuotaStatus().Name("dqo-discovery-only").Obj()).
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-discovery-only").
				DiscoveryProvider("cp-1", nil).
				EffectiveCapacity(utiltestingalpha.MakeEffectiveCapacity().
					Flavors(
						*utiltestingalpha.MakeEffectiveCapacityFlavor("default-flavor").
							Resource(corev1.ResourceCPU, "100").
							Obj(),
					).
					Obj()).
				Condition(metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					Status:  metav1.ConditionTrue,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonComputed,
					Message: "Aggregated capacity successfully computed",
				}).
				Obj(),
			wantClusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					EffectiveQuotaStatus(utiltestingapi.MakeEffectiveQuotaStatus().Name("dqo-discovery-only").Obj()).
					Obj(),
			},
			wantErr: false,
		},
		"feature gate disabled": {
			enableFeatureGate: new(bool),
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-disabled").
				DiscoveryProvider("cp-1", nil).
				Obj(),
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-disabled").
				DiscoveryProvider("cp-1", nil).
				Obj(),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if tc.enableFeatureGate != nil {
				features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, *tc.enableFeatureGate)
			}
			builder := utiltesting.NewClientBuilder()

			objs := []client.Object{tc.dqo}
			for _, cp := range tc.capacityProviders {
				objs = append(objs, cp)
			}
			for _, co := range tc.cohorts {
				objs = append(objs, co)
			}
			for _, cq := range tc.clusterQueues {
				objs = append(objs, cq)
			}
			for _, other := range tc.otherDQOs {
				objs = append(objs, other)
			}

			cl := builder.WithObjects(objs...).WithStatusSubresource(objs...).Build()
			r := NewDynamicQuotaOrchestratorReconciler(cl)

			ctx, _ := utiltesting.ContextWithLog(t)
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: tc.dqo.Name},
			})
			if (err != nil) != tc.wantErr {
				t.Fatalf("Reconcile error = %v, wantErr %v", err, tc.wantErr)
			}

			var gotDQO kueuealpha.DynamicQuotaOrchestrator
			if err := cl.Get(ctx, types.NamespacedName{Name: tc.dqo.Name}, &gotDQO); err != nil {
				t.Fatalf("Failed to get DQO: %v", err)
			}

			if diff := cmp.Diff(tc.wantDQO, &gotDQO,
				cmpopts.IgnoreTypes(metav1.TypeMeta{}),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "CreationTimestamp", "Finalizers"),
				cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
				cmpopts.EquateEmpty(),
			); diff != "" {
				t.Errorf("Unexpected DQO (-want +got):\n%s", diff)
			}

			for _, wantCQ := range tc.wantClusterQueues {
				var gotCQ kueue.ClusterQueue
				if err := cl.Get(ctx, types.NamespacedName{Name: wantCQ.Name}, &gotCQ); err != nil {
					t.Errorf("Failed to get ClusterQueue %s: %v", wantCQ.Name, err)
					continue
				}
				if diff := cmp.Diff(wantCQ.Status.EffectiveQuotas, gotCQ.Status.EffectiveQuotas, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("Unexpected EffectiveQuotas for ClusterQueue %s (-want +got):\n%s", wantCQ.Name, diff)
				}
			}

			for _, wantCohort := range tc.wantCohorts {
				var gotCohort kueue.Cohort
				if err := cl.Get(ctx, types.NamespacedName{Name: wantCohort.Name}, &gotCohort); err != nil {
					t.Errorf("Failed to get Cohort %s: %v", wantCohort.Name, err)
					continue
				}
				if diff := cmp.Diff(wantCohort.Status.EffectiveQuotas, gotCohort.Status.EffectiveQuotas, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("Unexpected EffectiveQuotas for Cohort %s (-want +got):\n%s", wantCohort.Name, diff)
				}
			}
		})
	}
}

func TestDistributeCapacityProportionally(t *testing.T) {
	cases := map[string]struct {
		resource     corev1.ResourceName
		capacity     resource.Quantity
		participants []quotaParticipant
		want         map[string]resource.Quantity
	}{
		"empty participants": {
			resource:     corev1.ResourceCPU,
			capacity:     resource.MustParse("100"),
			participants: nil,
			want:         map[string]resource.Quantity{},
		},
		"zero sum spec nominal quota": {
			resource: corev1.ResourceCPU,
			capacity: resource.MustParse("100"),
			participants: []quotaParticipant{
				{kind: kueuealpha.ClusterQueueSubtreeRootRefKind, name: "cq-1", specNominalQuota: resource.MustParse("0")},
				{kind: kueuealpha.CohortSubtreeRootRefKind, name: "cohort-1", specNominalQuota: resource.MustParse("0")},
			},
			want: map[string]resource.Quantity{
				"ClusterQueue/cq-1": resource.MustParse("0"),
				"Cohort/cohort-1":   resource.MustParse("0"),
			},
		},
		"remainder tie-breaker: UUID ordering per KEP-12382": {
			resource: corev1.ResourceCPU,
			capacity: resource.MustParse("10"),
			participants: []quotaParticipant{
				{kind: kueuealpha.ClusterQueueSubtreeRootRefKind, name: "cq-1", uid: "uid-c", specNominalQuota: resource.MustParse("1")},
				{kind: kueuealpha.ClusterQueueSubtreeRootRefKind, name: "cq-2", uid: "uid-a", specNominalQuota: resource.MustParse("1")},
				{kind: kueuealpha.CohortSubtreeRootRefKind, name: "cohort-1", uid: "uid-b", specNominalQuota: resource.MustParse("1")},
			},
			want: map[string]resource.Quantity{
				"ClusterQueue/cq-1": resource.MustParse("3333m"),
				"ClusterQueue/cq-2": resource.MustParse("3334m"),
				"Cohort/cohort-1":   resource.MustParse("3333m"),
			},
		},
		"scalar resource distribution uses integer unit (scale 0)": {
			resource: corev1.ResourceMemory,
			capacity: resource.MustParse("10"),
			participants: []quotaParticipant{
				{kind: kueuealpha.ClusterQueueSubtreeRootRefKind, name: "cq-1", uid: "uid-c", specNominalQuota: resource.MustParse("1")},
				{kind: kueuealpha.ClusterQueueSubtreeRootRefKind, name: "cq-2", uid: "uid-a", specNominalQuota: resource.MustParse("1")},
				{kind: kueuealpha.CohortSubtreeRootRefKind, name: "cohort-1", uid: "uid-b", specNominalQuota: resource.MustParse("1")},
			},
			want: map[string]resource.Quantity{
				"ClusterQueue/cq-1": resource.MustParse("3"),
				"ClusterQueue/cq-2": resource.MustParse("4"),
				"Cohort/cohort-1":   resource.MustParse("3"),
			},
		},
		"proportional allocation without remainders across cohorts and clusterqueues": {
			resource: corev1.ResourceCPU,
			capacity: resource.MustParse("50"),
			participants: []quotaParticipant{
				{kind: kueuealpha.CohortSubtreeRootRefKind, name: "parent-cohort", uid: "uid-1", specNominalQuota: resource.MustParse("20")},
				{kind: kueuealpha.ClusterQueueSubtreeRootRefKind, name: "cq-1", uid: "uid-2", specNominalQuota: resource.MustParse("10")},
				{kind: kueuealpha.ClusterQueueSubtreeRootRefKind, name: "cq-2", uid: "uid-3", specNominalQuota: resource.MustParse("10")},
			},
			want: map[string]resource.Quantity{
				"Cohort/parent-cohort": resource.MustParse("25"),
				"ClusterQueue/cq-1":    resource.MustParse("12500m"),
				"ClusterQueue/cq-2":    resource.MustParse("12500m"),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := distributeCapacityProportionally(tc.resource, tc.capacity, tc.participants)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Unexpected distribution (-want +got):\n%s", diff)
			}
		})
	}
}
