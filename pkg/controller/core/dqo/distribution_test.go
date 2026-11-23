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

package dqo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestDynamicQuotaOrchestratorDistribution(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, true)

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
		"distribution: unreported resources of orchestrated flavors are distributed as zero": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-zero").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-1").
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
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
						).
						Obj()).
					Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").
							Resource(corev1.ResourceCPU, "50").
							Resource(corev1.ResourceMemory, "10Gi").
							Obj(),
					).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("gpu-flavor").Resource("nvidia.com/gpu", "8").Obj(),
					).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("static-flavor").Resource("example.com/license", "5").Obj(),
					).
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-zero").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-1").
				EffectiveCapacity(utiltestingalpha.MakeEffectiveCapacity().
					Flavors(
						*utiltestingalpha.MakeEffectiveCapacityFlavor("default-flavor").
							Resource(corev1.ResourceCPU, "200").
							Obj(),
						*utiltestingalpha.MakeEffectiveCapacityFlavor("gpu-flavor").Obj(),
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
						*utiltestingapi.MakeFlavorQuotas("default-flavor").
							Resource(corev1.ResourceCPU, "50").
							Resource(corev1.ResourceMemory, "10Gi").
							Obj(),
					).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("gpu-flavor").Resource("nvidia.com/gpu", "8").Obj(),
					).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("static-flavor").Resource("example.com/license", "5").Obj(),
					).
					EffectiveQuotaStatus(
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("dqo-zero").
							ResourceGroups(
								utiltestingapi.ResourceGroup(
									*utiltestingapi.MakeFlavorQuotas("default-flavor").
										Resource(corev1.ResourceCPU, "200").
										Resource(corev1.ResourceMemory, "0").
										Obj(),
								),
								utiltestingapi.ResourceGroup(
									*utiltestingapi.MakeFlavorQuotas("gpu-flavor").Resource("nvidia.com/gpu", "0").Obj(),
								),
								utiltestingapi.ResourceGroup(
									*utiltestingapi.MakeFlavorQuotas("static-flavor").Resource("example.com/license", "5").Obj(),
								),
							).
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
		"soft validation: takeover when managing DQO is Distributed=False": {
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
					Condition(metav1.Condition{
						Type:   kueuealpha.DynamicQuotaOrchestratorDistributed,
						Status: metav1.ConditionFalse,
						Reason: kueuealpha.DynamicQuotaOrchestratorReasonEffectiveCapacityNotComputed,
					}).
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
							Name("dqo-1").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "100").Obj(),
							)).
							Obj(),
					).
					Obj(),
			},
			wantErr: false,
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
		"soft validation: grandchild CQ DQO deactivated when ancestor DQO distributes to grandparent cohort": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("child-dqo").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "child-cq").
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
				utiltestingapi.MakeCohort("grandparent-cohort").Obj(),
				utiltestingapi.MakeCohort("parent-cohort").Parent("grandparent-cohort").Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("child-cq").
					Cohort("parent-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					Obj(),
			},
			otherDQOs: []*kueuealpha.DynamicQuotaOrchestrator{
				utiltestingalpha.MakeDynamicQuotaOrchestrator("grandparent-dqo").
					DiscoveryProvider("cp-1", nil).
					SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "grandparent-cohort").
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("child-dqo").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "child-cq").
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
					Message: "Conflicts with ancestor DynamicQuotaOrchestrator \"grandparent-dqo\"",
				}).
				Obj(),
			wantClusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("child-cq").
					Cohort("parent-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					Obj(),
			},
			wantErr: false,
		},
		"soft validation: ancestor DQO takes over effective quotas on multi-level descendant CQ": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("grandparent-dqo").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "grandparent-cohort").
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
				utiltestingapi.MakeCohort("grandparent-cohort").Obj(),
				utiltestingapi.MakeCohort("parent-cohort").Parent("grandparent-cohort").Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("child-cq").
					Cohort("parent-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
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
					SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "child-cq").
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("grandparent-dqo").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "grandparent-cohort").
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
					Cohort("parent-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					EffectiveQuotaStatus(
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("grandparent-dqo").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "100").Obj(),
							)).
							Obj(),
					).
					Obj(),
			},
			wantErr: false,
		},
		"soft validation: sibling cohorts do not conflict and both distribute successfully": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-a").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "cohort-a").
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
				utiltestingapi.MakeCohort("cohort-a").Parent("root-cohort").Obj(),
				utiltestingapi.MakeCohort("cohort-b").Parent("root-cohort").Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-a").
					Cohort("cohort-a").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					Obj(),
				utiltestingapi.MakeClusterQueue("cq-b").
					Cohort("cohort-b").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					Obj(),
			},
			otherDQOs: []*kueuealpha.DynamicQuotaOrchestrator{
				utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-b").
					DiscoveryProvider("cp-1", nil).
					SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "cohort-b").
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-a").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "cohort-a").
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
				utiltestingapi.MakeClusterQueue("cq-a").
					Cohort("cohort-a").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					EffectiveQuotaStatus(
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("dqo-a").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "100").Obj(),
							)).
							Obj(),
					).
					Obj(),
			},
			wantErr: false,
		},
		"soft validation: separate ClusterQueues do not conflict (ClusterQueue cannot be ancestor)": {
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
					Obj(),
				utiltestingapi.MakeClusterQueue("cq-2").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					Obj(),
			},
			otherDQOs: []*kueuealpha.DynamicQuotaOrchestrator{
				utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-2").
					DiscoveryProvider("cp-1", nil).
					SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-2").
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
							Name("dqo-1").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "100").Obj(),
							)).
							Obj(),
					).
					Obj(),
			},
			wantErr: false,
		},
		"soft validation: cyclic cohort hierarchy terminates safely without hanging": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-a").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "cycle-a").
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
				utiltestingapi.MakeCohort("cycle-a").Parent("cycle-b").Obj(),
				utiltestingapi.MakeCohort("cycle-b").Parent("cycle-a").Obj(),
				utiltestingapi.MakeCohort("other-cohort").Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-a").
					Cohort("cycle-a").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					Obj(),
			},
			otherDQOs: []*kueuealpha.DynamicQuotaOrchestrator{
				utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-other").
					DiscoveryProvider("cp-1", nil).
					SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "other-cohort").
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-a").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "cycle-a").
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
				utiltestingapi.MakeClusterQueue("cq-a").
					Cohort("cycle-a").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					EffectiveQuotaStatus(
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("dqo-a").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "100").Obj(),
							)).
							Obj(),
					).
					Obj(),
			},
			wantErr: false,
		},
		"soft validation: managed conflict where other DQO root is non-existent returns conflict error": {
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
					SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "non-existent-cohort").
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
			wantErr: false,
		},
		"distribution: root ClusterQueue not found sets Misconfigured condition": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-cq-not-found").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "missing-cq").
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
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-cq-not-found").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "missing-cq").
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
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonMisconfigured,
					Message: "ClusterQueue \"missing-cq\" not found",
				}).
				Obj(),
			wantErr: false,
		},
		"distribution: root Cohort not found sets Misconfigured condition": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-cohort-not-found").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "missing-cohort").
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
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-cohort-not-found").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "missing-cohort").
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
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonMisconfigured,
					Message: "Cohort \"missing-cohort\" not found",
				}).
				Obj(),
			wantErr: false,
		},
		"distribution: unsupported subtree root kind sets Misconfigured condition": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-unsupported-kind").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot("UnknownKind", "foo").
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
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-unsupported-kind").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot("UnknownKind", "foo").
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
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonMisconfigured,
					Message: "unsupported subtree root kind \"UnknownKind\"",
				}).
				Obj(),
			wantErr: false,
		},
		"distribution: to standalone Cohort with no child queues or cohorts": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-standalone-cohort").
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
				utiltestingapi.MakeCohort("root-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-standalone-cohort").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "root-cohort").
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
			wantCohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("root-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					EffectiveQuotaStatus(
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("dqo-standalone-cohort").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "100").Obj(),
							)).
							Obj(),
					).
					Obj(),
			},
			wantErr: false,
		},
		"distribution: multi-level hierarchy excludes disjoint cohort trees": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-multi-level").
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
				utiltestingapi.MakeCohort("child-cohort").Parent("root-cohort").Obj(),
				utiltestingapi.MakeCohort("other-root").Obj(),
				utiltestingapi.MakeCohort("disjoint-child").Parent("other-root").Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("root-cq").
					Cohort("root-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					Obj(),
				utiltestingapi.MakeClusterQueue("child-cq").
					Cohort("child-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					Obj(),
				utiltestingapi.MakeClusterQueue("disjoint-cq").
					Cohort("disjoint-child").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-multi-level").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "root-cohort").
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
				utiltestingapi.MakeClusterQueue("root-cq").
					Cohort("root-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					EffectiveQuotaStatus(
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("dqo-multi-level").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
							)).
							Obj(),
					).
					Obj(),
				utiltestingapi.MakeClusterQueue("child-cq").
					Cohort("child-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					EffectiveQuotaStatus(
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("dqo-multi-level").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
							)).
							Obj(),
					).
					Obj(),
				utiltestingapi.MakeClusterQueue("disjoint-cq").
					Cohort("disjoint-child").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					Obj(),
			},
			wantErr: false,
		},
		"distribution: intermediate cohort only distributes to descendants, excludes ancestors and siblings": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-mid").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "mid-cohort").
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
				utiltestingapi.MakeCohort("top-root").Obj(),
				utiltestingapi.MakeCohort("mid-cohort").Parent("top-root").Obj(),
				utiltestingapi.MakeCohort("sibling-cohort").Parent("top-root").Obj(),
				utiltestingapi.MakeCohort("leaf-cohort").Parent("mid-cohort").Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("top-cq").
					Cohort("top-root").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					Obj(),
				utiltestingapi.MakeClusterQueue("mid-cq").
					Cohort("mid-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					Obj(),
				utiltestingapi.MakeClusterQueue("sibling-cq").
					Cohort("sibling-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					Obj(),
				utiltestingapi.MakeClusterQueue("leaf-cq").
					Cohort("leaf-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-mid").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "mid-cohort").
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
				utiltestingapi.MakeClusterQueue("top-cq").
					Cohort("top-root").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					Obj(),
				utiltestingapi.MakeClusterQueue("mid-cq").
					Cohort("mid-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					EffectiveQuotaStatus(
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("dqo-mid").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
							)).
							Obj(),
					).
					Obj(),
				utiltestingapi.MakeClusterQueue("sibling-cq").
					Cohort("sibling-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					Obj(),
				utiltestingapi.MakeClusterQueue("leaf-cq").
					Cohort("leaf-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					EffectiveQuotaStatus(
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("dqo-mid").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
							)).
							Obj(),
					).
					Obj(),
			},
			wantErr: false,
		},
		"distribution: cyclic cohort hierarchy in subtree resolution terminates safely": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-cycle").
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
				utiltestingapi.MakeCohort("root-cohort").Parent("child-cohort").Obj(),
				utiltestingapi.MakeCohort("child-cohort").Parent("root-cohort").Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").
					Cohort("root-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					Obj(),
				utiltestingapi.MakeClusterQueue("cq-2").
					Cohort("child-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-cycle").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "root-cohort").
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
					Cohort("root-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					EffectiveQuotaStatus(
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("dqo-cycle").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
							)).
							Obj(),
					).
					Obj(),
				utiltestingapi.MakeClusterQueue("cq-2").
					Cohort("child-cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
					).
					EffectiveQuotaStatus(
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("dqo-cycle").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "50").Obj(),
							)).
							Obj(),
					).
					Obj(),
			},
			wantErr: false,
		},
		"proportional distribution: zero sum spec nominal quota allocates zero effective quota": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-zero-sum").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "cohort-1").
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
				utiltestingapi.MakeCohort("cohort-1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "0").Obj(),
					).
					Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").
					Cohort("cohort-1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "0").Obj(),
					).
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-zero-sum").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "cohort-1").
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
			wantCohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("cohort-1").
					EffectiveQuotaStatus(
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("dqo-zero-sum").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "0").Obj(),
							)).
							Obj(),
					).
					Obj(),
			},
			wantClusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").
					Cohort("cohort-1").
					EffectiveQuotaStatus(
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("dqo-zero-sum").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "0").Obj(),
							)).
							Obj(),
					).
					Obj(),
			},
		},
		"proportional distribution: remainder tie-breaker uses UUID ordering per KEP-12382": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-tie-breaker").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "cohort-1").
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
								Resource(corev1.ResourceCPU, "10").
								Obj(),
						).
						Obj()).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("cohort-1").
					UID("uid-b").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "1").Obj(),
					).
					Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").
					UID("uid-c").
					Cohort("cohort-1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "1").Obj(),
					).
					Obj(),
				utiltestingapi.MakeClusterQueue("cq-2").
					UID("uid-a").
					Cohort("cohort-1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "1").Obj(),
					).
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-tie-breaker").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "cohort-1").
				EffectiveCapacity(utiltestingalpha.MakeEffectiveCapacity().
					Flavors(
						*utiltestingalpha.MakeEffectiveCapacityFlavor("default-flavor").
							Resource(corev1.ResourceCPU, "10").
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
				utiltestingapi.MakeCohort("cohort-1").
					EffectiveQuotaStatus(
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("dqo-tie-breaker").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "3333m").Obj(),
							)).
							Obj(),
					).
					Obj(),
			},
			wantClusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").
					Cohort("cohort-1").
					EffectiveQuotaStatus(
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("dqo-tie-breaker").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "3333m").Obj(),
							)).
							Obj(),
					).
					Obj(),
				utiltestingapi.MakeClusterQueue("cq-2").
					Cohort("cohort-1").
					EffectiveQuotaStatus(
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("dqo-tie-breaker").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "3334m").Obj(),
							)).
							Obj(),
					).
					Obj(),
			},
		},
		"proportional distribution: scalar resource distribution uses integer unit (scale 0)": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-scalar-scale").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "cohort-1").
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
								Resource(corev1.ResourceMemory, "10").
								Obj(),
						).
						Obj()).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("cohort-1").
					UID("uid-b").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceMemory, "1").Obj(),
					).
					Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").
					UID("uid-c").
					Cohort("cohort-1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceMemory, "1").Obj(),
					).
					Obj(),
				utiltestingapi.MakeClusterQueue("cq-2").
					UID("uid-a").
					Cohort("cohort-1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceMemory, "1").Obj(),
					).
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-scalar-scale").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "cohort-1").
				EffectiveCapacity(utiltestingalpha.MakeEffectiveCapacity().
					Flavors(
						*utiltestingalpha.MakeEffectiveCapacityFlavor("default-flavor").
							Resource(corev1.ResourceMemory, "10").
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
				utiltestingapi.MakeCohort("cohort-1").
					EffectiveQuotaStatus(
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("dqo-scalar-scale").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceMemory, "3").Obj(),
							)).
							Obj(),
					).
					Obj(),
			},
			wantClusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-1").
					Cohort("cohort-1").
					EffectiveQuotaStatus(
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("dqo-scalar-scale").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceMemory, "3").Obj(),
							)).
							Obj(),
					).
					Obj(),
				utiltestingapi.MakeClusterQueue("cq-2").
					Cohort("cohort-1").
					EffectiveQuotaStatus(
						utiltestingapi.MakeEffectiveQuotaStatus().
							Name("dqo-scalar-scale").
							ResourceGroups(utiltestingapi.ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceMemory, "4").Obj(),
							)).
							Obj(),
					).
					Obj(),
			},
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
			r := NewReconciler(cl)

			ctx, _ := utiltesting.ContextWithLog(t)
			_, err := r.Reconcile(ctx, reconcile.Request{
				Name: tc.dqo.Name,
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
	t.Run("ownership handoff", func(t *testing.T) {
		t.Run("previous owner subtree coverage", testPreviousOwnerSubtreeCoverage)
		t.Run("strict ancestor with implicit parents and cycles", testStrictAncestorWithImplicitParentsAndCycles)
		t.Run("subtree coverage propagates ancestor read error", testSubtreeCoveragePropagatesAncestorReadError)
		t.Run("foreign owner with same name is protected", testForeignOwnerWithSameNameIsProtected)
		t.Run("retargeted queue root allows takeover", testRetargetedQueueRootAllowsTakeover)
		t.Run("ownership lookup failure retries without changing distribution", testOwnershipLookupFailureRetriesWithoutChangingDistribution)
	})
}

func testPreviousOwnerSubtreeCoverage(t *testing.T) {
	cohortRoot := kueuealpha.CapacityDistributionSubtreeRootRef{Kind: kueuealpha.CohortSubtreeRootRefKind, Name: "old-root"}
	queueRoot := kueuealpha.CapacityDistributionSubtreeRootRef{Kind: kueuealpha.ClusterQueueSubtreeRootRefKind, Name: "root-cq"}
	tests := map[string]struct {
		root    kueuealpha.CapacityDistributionSubtreeRootRef
		kind    kueuealpha.SubtreeRootRefKind
		name    string
		objects []client.Object
		covered bool
	}{
		"queue under old root": {
			root: cohortRoot, kind: kueuealpha.ClusterQueueSubtreeRootRefKind, name: "target",
			objects: []client.Object{utiltestingapi.MakeCohort("old-root").Obj(), utiltestingapi.MakeClusterQueue("target").Cohort("old-root").Obj()}, covered: true,
		},
		"cohort under old root": {
			root: cohortRoot, kind: kueuealpha.CohortSubtreeRootRefKind, name: "child",
			objects: []client.Object{utiltestingapi.MakeCohort("old-root").Obj(), utiltestingapi.MakeCohort("child").Parent("old-root").Obj()}, covered: true,
		},
		"queue under nested cohort of old root": {
			root: cohortRoot, kind: kueuealpha.ClusterQueueSubtreeRootRefKind, name: "target",
			objects: []client.Object{
				utiltestingapi.MakeCohort("old-root").Parent("implicit").Obj(),
				utiltestingapi.MakeCohort("child").Parent("old-root").Obj(),
				utiltestingapi.MakeClusterQueue("target").Cohort("child").Obj(),
			}, covered: true,
		},
		"queue moved outside old root": {
			root: cohortRoot,
			kind: kueuealpha.ClusterQueueSubtreeRootRefKind,
			name: "target",
			objects: []client.Object{
				utiltestingapi.MakeCohort("old-root").Obj(),
				utiltestingapi.MakeCohort("new-root").Obj(),
				utiltestingapi.MakeClusterQueue("target").Cohort("new-root").Obj(),
			},
			covered: false,
		},
		"missing root is inconclusive": {
			root: cohortRoot, kind: kueuealpha.ClusterQueueSubtreeRootRefKind, name: "target",
			objects: []client.Object{utiltestingapi.MakeClusterQueue("target").Obj()}, covered: true,
		},
		"missing target is inconclusive": {
			root: cohortRoot, kind: kueuealpha.ClusterQueueSubtreeRootRefKind, name: "target",
			objects: []client.Object{utiltestingapi.MakeCohort("old-root").Obj()}, covered: true,
		},
		"implicit parent terminates the hierarchy": {
			root: cohortRoot, kind: kueuealpha.ClusterQueueSubtreeRootRefKind, name: "target",
			objects: []client.Object{utiltestingapi.MakeCohort("old-root").Obj(), utiltestingapi.MakeClusterQueue("target").Cohort("implicit").Obj()}, covered: false,
		},
		"cohort under implicit parent is outside old root": {
			root: cohortRoot, kind: kueuealpha.CohortSubtreeRootRefKind, name: "target",
			objects: []client.Object{utiltestingapi.MakeCohort("old-root").Obj(), utiltestingapi.MakeCohort("target").Parent("implicit").Obj()}, covered: false,
		},
		"missing cohort target is inconclusive": {
			root: cohortRoot, kind: kueuealpha.CohortSubtreeRootRefKind, name: "target",
			objects: []client.Object{utiltestingapi.MakeCohort("old-root").Obj()}, covered: true,
		},
		"cycle is inconclusive": {
			root: cohortRoot,
			kind: kueuealpha.CohortSubtreeRootRefKind,
			name: "child",
			objects: []client.Object{
				utiltestingapi.MakeCohort("old-root").Obj(),
				utiltestingapi.MakeCohort("child").Parent("other").Obj(),
				utiltestingapi.MakeCohort("other").Parent("child").Obj(),
			},
			covered: true,
		},
		"queue root covers itself": {
			root: queueRoot, kind: kueuealpha.ClusterQueueSubtreeRootRefKind, name: "root-cq",
			objects: []client.Object{utiltestingapi.MakeClusterQueue("root-cq").Obj()}, covered: true,
		},
		"queue root does not cover another queue": {
			root: queueRoot, kind: kueuealpha.ClusterQueueSubtreeRootRefKind, name: "target",
			objects: []client.Object{utiltestingapi.MakeClusterQueue("root-cq").Obj(), utiltestingapi.MakeClusterQueue("target").Obj()}, covered: false,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			r := NewReconciler(utiltesting.NewClientBuilder().WithObjects(tc.objects...).Build())
			got, err := r.subtreeCovers(ctx, tc.root, tc.kind, tc.name)
			if err != nil || got != tc.covered {
				t.Fatalf("subtreeCovers() = %t, %v; want %t, nil", got, err, tc.covered)
			}
		})
	}
}

func testStrictAncestorWithImplicitParentsAndCycles(t *testing.T) {
	cohort := func(name string) kueuealpha.CapacityDistributionSubtreeRootRef {
		return kueuealpha.CapacityDistributionSubtreeRootRef{Kind: kueuealpha.CohortSubtreeRootRefKind, Name: name}
	}
	queue := func(name string) kueuealpha.CapacityDistributionSubtreeRootRef {
		return kueuealpha.CapacityDistributionSubtreeRootRef{Kind: kueuealpha.ClusterQueueSubtreeRootRefKind, Name: name}
	}
	tests := map[string]struct {
		candidate kueuealpha.CapacityDistributionSubtreeRootRef
		target    kueuealpha.CapacityDistributionSubtreeRootRef
		objects   []client.Object
		want      bool
	}{
		"implicit parent is an ancestor of a queue": {
			candidate: cohort("implicit"), target: queue("target"),
			objects: []client.Object{utiltestingapi.MakeClusterQueue("target").Cohort("implicit").Obj()}, want: true,
		},
		"implicit parent is an ancestor of a cohort": {
			candidate: cohort("implicit"), target: cohort("child"),
			objects: []client.Object{utiltestingapi.MakeCohort("child").Parent("implicit").Obj()}, want: true,
		},
		"separate implicit parent is not an ancestor": {
			candidate: cohort("other"), target: cohort("child"),
			objects: []client.Object{utiltestingapi.MakeCohort("child").Parent("implicit").Obj()},
		},
		"missing target is not established as a descendant": {
			candidate: cohort("other"), target: cohort("missing"),
		},
		"cycle does not establish an unrelated ancestor": {
			candidate: cohort("other"), target: cohort("child"),
			objects: []client.Object{utiltestingapi.MakeCohort("child").Parent("parent").Obj(), utiltestingapi.MakeCohort("parent").Parent("child").Obj()},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			r := NewReconciler(utiltesting.NewClientBuilder().WithObjects(tc.objects...).Build())
			got, err := r.isStrictAncestor(ctx, tc.candidate, tc.target)
			if err != nil || got != tc.want {
				t.Fatalf("isStrictAncestor() = %t, %v; want %t, nil", got, err, tc.want)
			}
		})
	}
}

func testSubtreeCoveragePropagatesAncestorReadError(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	lookupErr := errors.New("injected ancestor lookup failure")
	cl := utiltesting.NewClientBuilder().WithObjects(
		utiltestingapi.MakeCohort("old-root").Obj(),
		utiltestingapi.MakeClusterQueue("target").Cohort("parent").Obj(),
	).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*kueue.Cohort); ok && key.Name == "parent" {
				return lookupErr
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}).Build()
	r := NewReconciler(cl)
	root := kueuealpha.CapacityDistributionSubtreeRootRef{Kind: kueuealpha.CohortSubtreeRootRefKind, Name: "old-root"}
	_, err := r.subtreeCovers(ctx, root, kueuealpha.ClusterQueueSubtreeRootRefKind, "target")
	if !errors.Is(err, lookupErr) {
		t.Fatalf("subtreeCovers() error = %v, want %v", err, lookupErr)
	}
}

func testForeignOwnerWithSameNameIsProtected(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	r := NewReconciler(utiltesting.NewClientBuilder().Build())
	orchestrator := utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo").Obj()
	for _, ref := range []kueue.EffectiveQuotaStatusOrchestratorRef{
		{APIGroup: "example.com", Kind: "Other", Name: "dqo"},
		{APIGroup: "example.com", Kind: dynamicQuotaOrchestratorKind, Name: "dqo"},
	} {
		msg, err := r.checkManagedConflict(
			ctx,
			orchestrator,
			kueuealpha.CapacityDistributionSubtreeRootRef{},
			kueuealpha.ClusterQueueSubtreeRootRefKind,
			"target",
			&kueue.EffectiveQuotaStatus{OrchestratorRef: ref},
			nil,
		)
		if err != nil || msg == "" {
			t.Errorf("foreign owner %+v: message %q, error %v; want conflict", ref, msg, err)
		}
	}
}

func testRetargetedQueueRootAllowsTakeover(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, true)
	ctx, _ := utiltesting.ContextWithLog(t)
	cpA := makeSynchronizedProvider("cp-a", "flavor", 1)
	cpB := makeSynchronizedProvider("cp-b", "flavor", 1)
	dqoA := utiltestingalpha.MakeDynamicQuotaOrchestrator("owner-a").DiscoveryProvider(cpA.Name, nil).
		SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "x").Obj()
	dqoB := utiltestingalpha.MakeDynamicQuotaOrchestrator("owner-b").DiscoveryProvider(cpB.Name, nil).
		SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "x").Obj()
	queue := func(name string) *kueue.ClusterQueue {
		return utiltestingapi.MakeClusterQueue(name).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor-00").Resource(corev1.ResourceCPU, "1").Obj()).Obj()
	}
	x, y := queue("x"), queue("y")
	objects := []client.Object{cpA, cpB, dqoA, x, y}
	statusObjects := append(append([]client.Object{}, objects...), dqoB)
	cl := utiltesting.NewClientBuilder().WithObjects(objects...).WithStatusSubresource(statusObjects...).Build()
	r := NewReconciler(cl)
	if _, err := r.Reconcile(ctx, reconcile.Request{Name: dqoA.Name}); err != nil {
		t.Fatal(err)
	}
	var latest kueuealpha.DynamicQuotaOrchestrator
	if err := cl.Get(ctx, client.ObjectKeyFromObject(dqoA), &latest); err != nil {
		t.Fatal(err)
	}
	latest.Spec.CapacityDistribution.SubtreeRootQuotaRef.Name = y.Name
	if err := cl.Update(ctx, &latest); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, reconcile.Request{Name: dqoA.Name}); err != nil {
		t.Fatal(err)
	}
	if err := cl.Create(ctx, dqoB); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, reconcile.Request{Name: dqoB.Name}); err != nil {
		t.Fatal(err)
	}
	var actual kueue.ClusterQueue
	if err := cl.Get(ctx, client.ObjectKeyFromObject(x), &actual); err != nil {
		t.Fatal(err)
	}
	if actual.Status.EffectiveQuotas == nil || actual.Status.EffectiveQuotas.OrchestratorRef.Name != dqoB.Name {
		t.Fatalf("x effective quotas = %+v, want owner-b", actual.Status.EffectiveQuotas)
	}
}

func testOwnershipLookupFailureRetriesWithoutChangingDistribution(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, true)
	ctx, _ := utiltesting.ContextWithLog(t)
	provider := makeSynchronizedProvider("cp", "flavor", 1)
	current := utiltestingalpha.MakeDynamicQuotaOrchestrator("current").
		DiscoveryProvider(provider.Name, nil).
		SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "new-root").
		Condition(metav1.Condition{Type: kueuealpha.DynamicQuotaOrchestratorDistributed, Status: metav1.ConditionTrue, Reason: kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed, Message: "previously distributed"}).
		Obj()
	previous := utiltestingalpha.MakeDynamicQuotaOrchestrator("previous").
		DiscoveryProvider(provider.Name, nil).
		SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "old-root").Obj()
	queue := utiltestingapi.MakeClusterQueue("target").Cohort("new-root").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor-00").Resource(corev1.ResourceCPU, "1").Obj()).
		EffectiveQuotaStatus(utiltestingapi.MakeEffectiveQuotaStatus().Name("previous").Obj()).Obj()
	objects := []client.Object{provider, current, previous, queue, utiltestingapi.MakeCohort("old-root").Obj(), utiltestingapi.MakeCohort("new-root").Obj()}
	lookupErr := errors.New("injected owner-root lookup failure")
	failLookup := true
	cl := utiltesting.NewClientBuilder().WithObjects(objects...).WithStatusSubresource(objects...).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*kueue.Cohort); ok && key.Name == "old-root" && failLookup {
					return lookupErr
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).Build()
	r := NewReconciler(cl)
	req := reconcile.Request{Name: current.Name}
	if _, err := r.Reconcile(ctx, req); !errors.Is(err, lookupErr) {
		t.Fatalf("Reconcile error = %v, want injected lookup error", err)
	}
	var got kueuealpha.DynamicQuotaOrchestrator
	if err := cl.Get(ctx, client.ObjectKeyFromObject(current), &got); err != nil {
		t.Fatal(err)
	}
	condition := apimeta.FindStatusCondition(got.Status.Conditions, kueuealpha.DynamicQuotaOrchestratorDistributed)
	if condition == nil || condition.Status != metav1.ConditionTrue || condition.Reason != kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed {
		t.Fatalf("Distributed after lookup failure = %+v, want previous True condition", condition)
	}
	var actual kueue.ClusterQueue
	if err := cl.Get(ctx, client.ObjectKeyFromObject(queue), &actual); err != nil {
		t.Fatal(err)
	}
	if actual.Status.EffectiveQuotas == nil || actual.Status.EffectiveQuotas.OrchestratorRef.Name != "previous" {
		t.Fatalf("queue ownership changed after lookup failure: %+v", actual.Status.EffectiveQuotas)
	}
	failLookup = false
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(queue), &actual); err != nil {
		t.Fatal(err)
	}
	if actual.Status.EffectiveQuotas == nil || actual.Status.EffectiveQuotas.OrchestratorRef.Name != "current" {
		t.Fatalf("queue ownership after retry = %+v, want current", actual.Status.EffectiveQuotas)
	}
}
