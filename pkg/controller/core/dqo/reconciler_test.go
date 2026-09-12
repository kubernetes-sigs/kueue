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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
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
		"discovery-only: provider reports flavor with empty resources": {
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-empty-res").
				DiscoveryProvider("cp-1", nil).
				Obj(),
			capacityProviders: []*kueuealpha.CapacityProvider{
				utiltestingalpha.MakeCapacityProvider("cp-1").
					OrchestratedFlavors("empty-flavor").
					Condition(metav1.Condition{
						Type:   kueuealpha.CapacityProviderCapacitySynchronized,
						Status: metav1.ConditionTrue,
						Reason: kueuealpha.CapacityProviderReasonSynchronized,
					}).
					Capacity(utiltestingalpha.MakeNormalizedCapacity().
						Flavors(utiltestingalpha.MakeNormalizedCapacityFlavor("empty-flavor").Obj()).
						Obj(),
					).
					Obj(),
			},
			wantDQO: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-empty-res").
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

func TestOtherDQOUpdatePredicate(t *testing.T) {
	now := metav1.Now()
	later := metav1.NewTime(now.Time.Add(time.Minute))

	trueDistributed := utiltestingalpha.MakeDynamicQuotaOrchestrator("a").
		DiscoveryProvider("cp-1", nil).
		SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-x").
		EffectiveCapacity(utiltestingalpha.MakeEffectiveCapacity().
			Flavors(*utiltestingalpha.MakeEffectiveCapacityFlavor("f1").Resource(corev1.ResourceCPU, "100").Obj()).
			Obj()).
		Condition(metav1.Condition{
			Type:               kueuealpha.DynamicQuotaOrchestratorDistributed,
			Status:             metav1.ConditionTrue,
			Reason:             kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed,
			Message:            "Quotas successfully distributed",
			LastTransitionTime: now,
		}).
		Obj()
	trueDistributed.Generation = 2

	cases := map[string]struct {
		old  client.Object
		new  client.Object
		want bool
	}{
		"generation change": {
			old: trueDistributed,
			new: func() *kueuealpha.DynamicQuotaOrchestrator {
				d := trueDistributed.DeepCopy()
				d.Generation = 3
				d.Spec.CapacityDistribution.SubtreeRootQuotaRef.Name = "cq-y"
				return d
			}(),
			want: true,
		},
		"deletion timestamp set": {
			old: trueDistributed,
			new: func() *kueuealpha.DynamicQuotaOrchestrator {
				d := trueDistributed.DeepCopy()
				d.DeletionTimestamp = &now
				return d
			}(),
			want: true,
		},
		"distributed true to false": {
			old: trueDistributed,
			new: func() *kueuealpha.DynamicQuotaOrchestrator {
				d := trueDistributed.DeepCopy()
				d.Status.Conditions[0].Status = metav1.ConditionFalse
				d.Status.Conditions[0].Reason = kueuealpha.DynamicQuotaOrchestratorReasonEffectiveCapacityNotComputed
				d.Status.Conditions[0].Message = "Capacity discovery not ready"
				d.Status.Conditions[0].LastTransitionTime = later
				return d
			}(),
			want: true,
		},
		"distributed false to true": {
			old: func() *kueuealpha.DynamicQuotaOrchestrator {
				d := trueDistributed.DeepCopy()
				d.Status.Conditions[0].Status = metav1.ConditionFalse
				d.Status.Conditions[0].Reason = kueuealpha.DynamicQuotaOrchestratorReasonEffectiveCapacityNotComputed
				return d
			}(),
			new:  trueDistributed,
			want: true,
		},
		"absent distributed to false": {
			old: func() *kueuealpha.DynamicQuotaOrchestrator {
				d := utiltestingalpha.MakeDynamicQuotaOrchestrator("a").
					DiscoveryProvider("cp-1", nil).
					SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-x").
					Obj()
				d.Generation = 2
				return d
			}(),
			new: func() *kueuealpha.DynamicQuotaOrchestrator {
				d := trueDistributed.DeepCopy()
				d.Status.Conditions[0].Status = metav1.ConditionFalse
				d.Status.Conditions[0].Reason = kueuealpha.DynamicQuotaOrchestratorReasonEffectiveCapacityNotComputed
				return d
			}(),
			want: true,
		},
		"false reason change only": {
			old: func() *kueuealpha.DynamicQuotaOrchestrator {
				d := trueDistributed.DeepCopy()
				d.Status.Conditions[0].Status = metav1.ConditionFalse
				d.Status.Conditions[0].Reason = kueuealpha.DynamicQuotaOrchestratorReasonEffectiveCapacityNotComputed
				d.Status.Conditions[0].Message = "Capacity discovery not ready"
				return d
			}(),
			new: func() *kueuealpha.DynamicQuotaOrchestrator {
				d := trueDistributed.DeepCopy()
				d.Status.Conditions[0].Status = metav1.ConditionFalse
				d.Status.Conditions[0].Reason = kueuealpha.DynamicQuotaOrchestratorReasonMisconfigured
				d.Status.Conditions[0].Message = "Capacity discovery not ready"
				return d
			}(),
			want: false,
		},
		"false message change only": {
			old: func() *kueuealpha.DynamicQuotaOrchestrator {
				d := trueDistributed.DeepCopy()
				d.Status.Conditions[0].Status = metav1.ConditionFalse
				d.Status.Conditions[0].Reason = kueuealpha.DynamicQuotaOrchestratorReasonEffectiveCapacityNotComputed
				d.Status.Conditions[0].Message = "old"
				return d
			}(),
			new: func() *kueuealpha.DynamicQuotaOrchestrator {
				d := trueDistributed.DeepCopy()
				d.Status.Conditions[0].Status = metav1.ConditionFalse
				d.Status.Conditions[0].Reason = kueuealpha.DynamicQuotaOrchestratorReasonEffectiveCapacityNotComputed
				d.Status.Conditions[0].Message = "new"
				return d
			}(),
			want: false,
		},
		"false timestamp change only": {
			old: func() *kueuealpha.DynamicQuotaOrchestrator {
				d := trueDistributed.DeepCopy()
				d.Status.Conditions[0].Status = metav1.ConditionFalse
				d.Status.Conditions[0].LastTransitionTime = now
				return d
			}(),
			new: func() *kueuealpha.DynamicQuotaOrchestrator {
				d := trueDistributed.DeepCopy()
				d.Status.Conditions[0].Status = metav1.ConditionFalse
				d.Status.Conditions[0].LastTransitionTime = later
				return d
			}(),
			want: false,
		},
		"true with effective capacity rewrite": {
			old: trueDistributed,
			new: func() *kueuealpha.DynamicQuotaOrchestrator {
				d := trueDistributed.DeepCopy()
				d.Status.EffectiveCapacity = utiltestingalpha.MakeEffectiveCapacity().
					Flavors(*utiltestingalpha.MakeEffectiveCapacityFlavor("f1").Resource(corev1.ResourceCPU, "150").Obj()).
					Obj()
				return d
			}(),
			want: false,
		},
		"nil objects": {
			want: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := otherDQOUpdatePredicate.Update(event.UpdateEvent{
				ObjectOld: tc.old,
				ObjectNew: tc.new,
			})
			if got != tc.want {
				t.Errorf("Update() = %v, want %v", got, tc.want)
			}
		})
	}
}
