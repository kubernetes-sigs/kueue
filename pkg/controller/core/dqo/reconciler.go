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

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

const (
	dqoControllerName            = "dynamicquotaorchestrator-reconciler"
	dynamicQuotaOrchestratorKind = "DynamicQuotaOrchestrator"
)

type Reconciler struct {
	client      client.Client
	roleTracker *roletracker.RoleTracker
	logName     string
}

type Option func(*Reconciler)

// WithRoleTracker configures the RoleTracker for the reconciler.
func WithRoleTracker(rt *roletracker.RoleTracker) Option {
	return func(r *Reconciler) {
		r.roleTracker = rt
	}
}

// NewReconciler instantiates a new DynamicQuotaOrchestrator reconciler.
func NewReconciler(client client.Client, opts ...Option) *Reconciler {
	r := &Reconciler{
		client:  client,
		logName: dqoControllerName,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func (r *Reconciler) logger() logr.Logger {
	return roletracker.WithReplicaRole(ctrl.Log.WithName(r.logName), r.roleTracker)
}

// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=dynamicquotaorchestrators,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=dynamicquotaorchestrators/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=capacityproviders,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=clusterqueues,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=clusterqueues/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=cohorts,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=cohorts/status,verbs=get;update;patch

// SetupWithManager registers the DynamicQuotaOrchestrator controller and its watches with the manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kueuealpha.DynamicQuotaOrchestrator{}).
		Watches(
			&kueuealpha.CapacityProvider{},
			handler.EnqueueRequestsFromMapFunc(r.mapCapacityProviderToDQOs),
		).
		Watches(
			&kueue.Cohort{},
			handler.EnqueueRequestsFromMapFunc(r.mapDistributingDQOs),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		Watches(
			&kueue.ClusterQueue{},
			handler.EnqueueRequestsFromMapFunc(r.mapDistributingDQOs),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		Watches(
			&kueuealpha.DynamicQuotaOrchestrator{},
			handler.EnqueueRequestsFromMapFunc(r.mapOtherDistributingDQOs),
			builder.WithPredicates(otherDQOUpdatePredicate),
		).
		Complete(r)
}

// otherDQOUpdatePredicate filters updates that can affect other orchestrators.
// Changes to Distributed=False affect takeover of retained effective quotas.
var otherDQOUpdatePredicate = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		if e.ObjectOld == nil || e.ObjectNew == nil {
			return false
		}
		if e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration() {
			return true
		}
		if e.ObjectOld.GetDeletionTimestamp().IsZero() != e.ObjectNew.GetDeletionTimestamp().IsZero() {
			return true
		}
		oldDQO, oldOK := e.ObjectOld.(*kueuealpha.DynamicQuotaOrchestrator)
		newDQO, newOK := e.ObjectNew.(*kueuealpha.DynamicQuotaOrchestrator)
		return oldOK && newOK && isDistributedFalse(oldDQO) != isDistributedFalse(newDQO)
	},
}

// mapCapacityProviderToDQOs maps a CapacityProvider event to reconcile requests for all DynamicQuotaOrchestrators referencing it.
func (r *Reconciler) mapCapacityProviderToDQOs(ctx context.Context, obj client.Object) []ctrl.Request {
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

// mapDistributingDQOs maps Cohort or ClusterQueue events to reconcile requests for all distributing DynamicQuotaOrchestrators.
func (r *Reconciler) mapDistributingDQOs(ctx context.Context, _ client.Object) []ctrl.Request {
	distributingDQOs, err := r.listDistributingDQOs(ctx)
	if err != nil {
		r.logger().Error(err, "Failed to list distributing DynamicQuotaOrchestrators")
		return nil
	}
	requests := make([]ctrl.Request, 0, len(distributingDQOs))
	for _, orchestrator := range distributingDQOs {
		requests = append(requests, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: orchestrator.Name},
		})
	}
	return requests
}

// mapOtherDistributingDQOs maps a DynamicQuotaOrchestrator event to reconcile requests for other distributing DynamicQuotaOrchestrators.
func (r *Reconciler) mapOtherDistributingDQOs(ctx context.Context, obj client.Object) []ctrl.Request {
	if obj == nil {
		return nil
	}
	orchestrator, ok := obj.(*kueuealpha.DynamicQuotaOrchestrator)
	if !ok || orchestrator == nil {
		return nil
	}
	distributingDQOs, err := r.listDistributingDQOs(ctx)
	if err != nil {
		r.logger().Error(err, "Failed to list distributing DynamicQuotaOrchestrators")
		return nil
	}
	requests := make([]ctrl.Request, 0, len(distributingDQOs))
	for _, item := range distributingDQOs {
		if item.Name == orchestrator.Name {
			continue
		}
		requests = append(requests, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: item.Name},
		})
	}
	return requests
}

// Reconcile coordinates capacity discovery and quota distribution for a DynamicQuotaOrchestrator.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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
	if discoveryErr := r.reconcileDiscovery(ctx, &orchestrator); discoveryErr != nil {
		err := r.updateStatus(ctx, &orchestrator, oldStatus)
		return ctrl.Result{}, errors.Join(discoveryErr, err)
	}

	// Phase 2: Quota Distribution
	var distributionErr error
	switch {
	case orchestrator.Spec.CapacityDistribution == nil:
		apimeta.RemoveStatusCondition(&orchestrator.Status.Conditions, kueuealpha.DynamicQuotaOrchestratorDistributed)
	case orchestrator.Status.EffectiveCapacity == nil:
		apimeta.SetStatusCondition(&orchestrator.Status.Conditions, metav1.Condition{
			Type:               kueuealpha.DynamicQuotaOrchestratorDistributed,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: orchestrator.Generation,
			Reason:             kueuealpha.DynamicQuotaOrchestratorReasonEffectiveCapacityNotComputed,
			Message:            "Capacity discovery not ready",
		})
	default:
		if err := r.reconcileDistribution(ctx, &orchestrator, orchestrator.Status.EffectiveCapacity); err != nil {
			log.Error(err, "Failed to distribute quotas")
			distributionErr = err
		}
	}

	if err := r.updateStatus(ctx, &orchestrator, oldStatus); err != nil {
		return ctrl.Result{}, err
	}
	if distributionErr != nil {
		return ctrl.Result{}, distributionErr
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) updateStatus(ctx context.Context, orchestrator *kueuealpha.DynamicQuotaOrchestrator, oldStatus *kueuealpha.DynamicQuotaOrchestratorStatus) error {
	if equality.Semantic.DeepEqual(oldStatus, &orchestrator.Status) {
		return nil
	}
	return r.client.Status().Update(ctx, orchestrator)
}
