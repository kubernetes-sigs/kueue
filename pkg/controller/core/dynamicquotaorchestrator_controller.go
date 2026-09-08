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
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	utilresource "sigs.k8s.io/kueue/pkg/util/resource"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

const (
	dqoControllerName = "dynamicquotaorchestrator-reconciler"
)

type DynamicQuotaOrchestratorReconciler struct {
	client      client.Client
	roleTracker *roletracker.RoleTracker
	logName     string
}

type DynamicQuotaOrchestratorReconcilerOption func(*DynamicQuotaOrchestratorReconciler)

// WithDynamicQuotaOrchestratorRoleTracker configures the RoleTracker for the reconciler.
func WithDynamicQuotaOrchestratorRoleTracker(rt *roletracker.RoleTracker) DynamicQuotaOrchestratorReconcilerOption {
	return func(r *DynamicQuotaOrchestratorReconciler) {
		r.roleTracker = rt
	}
}

// NewDynamicQuotaOrchestratorReconciler instantiates a new DynamicQuotaOrchestrator reconciler.
func NewDynamicQuotaOrchestratorReconciler(client client.Client, opts ...DynamicQuotaOrchestratorReconcilerOption) *DynamicQuotaOrchestratorReconciler {
	r := &DynamicQuotaOrchestratorReconciler{
		client:  client,
		logName: dqoControllerName,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func (r *DynamicQuotaOrchestratorReconciler) logger() logr.Logger {
	return roletracker.WithReplicaRole(ctrl.Log.WithName(r.logName), r.roleTracker)
}

// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=dynamicquotaorchestrators,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=dynamicquotaorchestrators/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=capacityproviders,verbs=get;list;watch

// SetupWithManager registers the DynamicQuotaOrchestrator controller and its watches with the manager.
func (r *DynamicQuotaOrchestratorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kueuealpha.DynamicQuotaOrchestrator{}).
		Watches(
			&kueuealpha.CapacityProvider{},
			handler.EnqueueRequestsFromMapFunc(r.mapCapacityProviderToDQOs),
		).
		Complete(r)
}

// mapCapacityProviderToDQOs maps a CapacityProvider event to reconcile requests for all DynamicQuotaOrchestrators referencing it.
func (r *DynamicQuotaOrchestratorReconciler) mapCapacityProviderToDQOs(ctx context.Context, obj client.Object) []ctrl.Request {
	capacityProvider, ok := obj.(*kueuealpha.CapacityProvider)
	if !ok || capacityProvider == nil {
		return nil
	}
	var orchestratorList kueuealpha.DynamicQuotaOrchestratorList
	if err := r.client.List(ctx, &orchestratorList, client.MatchingFields{
		indexer.DynamicQuotaOrchestratorCapacityProviderKey: capacityProvider.Name,
	}); err != nil {
		r.logger().Error(err, "Failed to list DynamicQuotaOrchestrators for CapacityProvider", "capacityProvider", capacityProvider.Name)
		return nil
	}
	requests := make([]ctrl.Request, 0, len(orchestratorList.Items))
	for _, orchestrator := range orchestratorList.Items {
		requests = append(requests, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: orchestrator.Name},
		})
	}
	return requests
}

// Reconcile coordinates capacity discovery for a DynamicQuotaOrchestrator.
func (r *DynamicQuotaOrchestratorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if !features.Enabled(features.DynamicQuotaOrchestration) {
		return ctrl.Result{}, nil
	}

	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile DynamicQuotaOrchestrator")

	var orchestrator kueuealpha.DynamicQuotaOrchestrator
	if err := r.client.Get(ctx, req.NamespacedName, &orchestrator); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !orchestrator.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	oldStatus := orchestrator.Status.DeepCopy()

	// Phase 1: Capacity Discovery
	discoveryErr := r.reconcileDiscovery(ctx, &orchestrator)
	err := r.updateStatus(ctx, &orchestrator, oldStatus)
	return ctrl.Result{}, errors.Join(discoveryErr, err)
}

// reconcileDiscovery performs Phase 1 reconciliation: aggregates normalized capacities across referenced CapacityProviders.
func (r *DynamicQuotaOrchestratorReconciler) reconcileDiscovery(ctx context.Context, orchestrator *kueuealpha.DynamicQuotaOrchestrator) error {
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

func (r *DynamicQuotaOrchestratorReconciler) setDiscoveryCondition(orchestrator *kueuealpha.DynamicQuotaOrchestrator, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&orchestrator.Status.Conditions, metav1.Condition{
		Type:               kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
		Status:             status,
		ObservedGeneration: orchestrator.Generation,
		Reason:             reason,
		Message:            message,
	})
}

func (r *DynamicQuotaOrchestratorReconciler) updateStatus(ctx context.Context, orchestrator *kueuealpha.DynamicQuotaOrchestrator, oldStatus *kueuealpha.DynamicQuotaOrchestratorStatus) error {
	if equality.Semantic.DeepEqual(oldStatus, &orchestrator.Status) {
		return nil
	}
	return r.client.Status().Update(ctx, orchestrator)
}
