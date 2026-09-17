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
	"fmt"
	"maps"
	"slices"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	utilresource "sigs.k8s.io/kueue/pkg/util/resource"
)

// reconcileDiscovery performs Phase 1 reconciliation: aggregates normalized capacities across referenced CapacityProviders.
func (r *Reconciler) reconcileDiscovery(ctx context.Context, orchestrator *kueuealpha.DynamicQuotaOrchestrator) error {
	aggregatedCapacity := make(map[kueuealpha.ResourceFlavorReference]corev1.ResourceList)

	for _, providerContribution := range orchestrator.Spec.CapacityDiscovery.Providers {
		var capacityProvider kueuealpha.CapacityProvider
		if err := r.client.Get(ctx, types.NamespacedName{Name: string(providerContribution.Name)}, &capacityProvider); err != nil {
			if apierrors.IsNotFound(err) {
				r.setDiscoveryCondition(
					orchestrator,
					metav1.ConditionFalse,
					kueuealpha.DynamicQuotaOrchestratorReasonMisconfigured,
					fmt.Sprintf("CapacityProvider %q not found", providerContribution.Name),
				)
				orchestrator.Status.EffectiveCapacity = nil
				return nil
			}
			return err
		}

		if !apimeta.IsStatusConditionTrue(capacityProvider.Status.Conditions, kueuealpha.CapacityProviderCapacitySynchronized) {
			r.setDiscoveryCondition(
				orchestrator,
				metav1.ConditionFalse,
				kueuealpha.DynamicQuotaOrchestratorReasonProviderNotReady,
				fmt.Sprintf("CapacityProvider %q is not synchronized", providerContribution.Name),
			)
			orchestrator.Status.EffectiveCapacity = nil
			return nil
		}

		aggregateProviderCapacity(capacityProvider.Status.Capacity, capacityProvider.Spec.OrchestratedFlavors, providerContribution.EffectiveCapacityMultiplier, aggregatedCapacity)
	}

	effectiveCapacityFlavors := make([]kueuealpha.EffectiveCapacityFlavor, 0, len(aggregatedCapacity))
	for _, flavorName := range slices.Sorted(maps.Keys(aggregatedCapacity)) {
		effectiveCapacityFlavors = append(effectiveCapacityFlavors, kueuealpha.EffectiveCapacityFlavor{
			Name:      flavorName,
			Resources: aggregatedCapacity[flavorName],
		})
	}

	orchestrator.Status.EffectiveCapacity = &kueuealpha.EffectiveCapacity{
		Flavors: effectiveCapacityFlavors,
	}
	r.setDiscoveryCondition(orchestrator, metav1.ConditionTrue, kueuealpha.DynamicQuotaOrchestratorReasonComputed, "Aggregated capacity successfully computed")
	return nil
}

// aggregateProviderCapacity scales and adds flavor resource quantities from a single CapacityProvider into the running aggregated total,
// filtering exclusively by the flavors declared in the CapacityProvider's spec.orchestratedFlavors.
func aggregateProviderCapacity(
	capacity *kueuealpha.CapacityProviderNormalizedCapacity,
	orchestratedFlavors []kueuealpha.CapacityProviderOrchestratedFlavor,
	multiplier *resource.Quantity,
	aggregatedCapacity map[kueuealpha.ResourceFlavorReference]corev1.ResourceList,
) {
	if capacity == nil {
		return
	}
	allowedFlavors := sets.New[kueuealpha.ResourceFlavorReference]()
	for _, f := range orchestratedFlavors {
		allowedFlavors.Insert(f.Name)
	}
	for _, flavor := range capacity.Flavors {
		if !allowedFlavors.Has(flavor.Name) {
			continue
		}
		res := flavor.Resources
		if len(res) == 0 {
			continue
		}
		if multiplier != nil {
			res = make(corev1.ResourceList, len(flavor.Resources))
			for k, v := range flavor.Resources {
				res[k] = utilresource.MultiplyQuantity(v, *multiplier)
			}
		}
		aggregatedCapacity[flavor.Name] = utilresource.MergeResourceListKeepSum(aggregatedCapacity[flavor.Name], res)
	}
}

// setDiscoveryCondition sets the EffectiveCapacityComputed condition on the orchestrator status.
func (r *Reconciler) setDiscoveryCondition(orchestrator *kueuealpha.DynamicQuotaOrchestrator, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&orchestrator.Status.Conditions, metav1.Condition{
		Type:               kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
		Status:             status,
		ObservedGeneration: orchestrator.Generation,
		Reason:             reason,
		Message:            message,
	})
}
