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

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
)

func TestDynamicQuotaOrchestratorReconcile(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, true)
	halfMultiplier := resource.MustParse("0.5")

	cases := map[string]struct {
		enableFeatureGate *bool
		dqo               *kueuealpha.DynamicQuotaOrchestrator
		capacityProviders []*kueuealpha.CapacityProvider
		wantDQO           *kueuealpha.DynamicQuotaOrchestrator
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
		})
	}
}
