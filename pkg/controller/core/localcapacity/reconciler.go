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

// Package localcapacity implements the built-in "kueue.x-k8s.io/local-capacity"
// CapacityProvider controller. It publishes, per ResourceFlavor, the sum of
// allocatable resources of the eligible Nodes matching the flavor's nodeLabels,
// so that Dynamic Quota Orchestration can derive quota from the cluster's nodes.
package localcapacity

import (
	"context"
	"errors"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/api"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

const (
	// ControllerName is the CapacityProvider.spec.controllerName served by this controller.
	ControllerName kueuealpha.CapacityProviderControllerName = "kueue.x-k8s.io/local-capacity"

	reconcilerName = "localcapacity-reconciler"
)

type Reconciler struct {
	client      client.Client
	roleTracker *roletracker.RoleTracker
}

type Option func(*Reconciler)

// WithRoleTracker configures the RoleTracker for the reconciler.
func WithRoleTracker(rt *roletracker.RoleTracker) Option {
	return func(r *Reconciler) {
		r.roleTracker = rt
	}
}

// NewReconciler instantiates a new local-capacity CapacityProvider reconciler.
func NewReconciler(client client.Client, opts ...Option) *Reconciler {
	r := &Reconciler{client: client}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func (r *Reconciler) logger() logr.Logger {
	return roletracker.WithReplicaRole(ctrl.Log.WithName(reconcilerName), r.roleTracker)
}

// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=topologies,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=capacityproviders,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=capacityproviders/status,verbs=get;update;patch

// SetupWithManager registers the controller and its watches with the manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named(reconcilerName).
		For(&kueuealpha.CapacityProvider{}, builder.WithPredicates(
			predicate.NewPredicateFuncs(isLocalCapacityProvider),
			predicate.GenerationChangedPredicate{},
		)).
		// A provider being created, deleted or changing its flavors can start or
		// resolve an overlap with any other local-capacity provider.
		Watches(
			&kueuealpha.CapacityProvider{},
			handler.EnqueueRequestsFromMapFunc(r.mapToAllProviders),
			builder.WithPredicates(
				predicate.NewPredicateFuncs(isLocalCapacityProvider),
				predicate.GenerationChangedPredicate{},
			),
		).
		Watches(
			&corev1.Node{},
			r.nodeEventHandler(),
			builder.WithPredicates(nodeCapacityChangedPredicate),
		).
		Watches(
			&kueue.ResourceFlavor{},
			handler.EnqueueRequestsFromMapFunc(r.mapToAllProviders),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		Watches(
			&kueue.Topology{},
			handler.EnqueueRequestsFromMapFunc(r.mapToAllProviders),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		Complete(r)
}

func isLocalCapacityProvider(obj client.Object) bool {
	cp, ok := obj.(*kueuealpha.CapacityProvider)
	return ok && cp.Spec.ControllerName == ControllerName
}

// nodeCapacityChangedPredicate filters out Node updates that cannot change the
// published capacity, most notably periodic kubelet heartbeats.
var nodeCapacityChangedPredicate = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		oldNode, okOld := e.ObjectOld.(*corev1.Node)
		newNode, okNew := e.ObjectNew.(*corev1.Node)
		if !okOld || !okNew {
			return true
		}
		return !equality.Semantic.DeepEqual(oldNode.Labels, newNode.Labels) ||
			oldNode.Spec.Unschedulable != newNode.Spec.Unschedulable ||
			oldNode.DeletionTimestamp.IsZero() != newNode.DeletionTimestamp.IsZero() ||
			!equality.Semantic.DeepEqual(oldNode.Status.Allocatable, newNode.Status.Allocatable) ||
			utiltas.IsNodeStatusConditionTrue(oldNode.Status.Conditions, corev1.NodeReady) != utiltas.IsNodeStatusConditionTrue(newNode.Status.Conditions, corev1.NodeReady)
	},
}

// nodeEventHandler enqueues only the providers a Node event can affect.
// On updates both the old and the new Node are considered, because a label
// change can move a Node out of one provider's flavor and into another's.
func (r *Reconciler) nodeEventHandler() handler.EventHandler {
	enqueue := func(ctx context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request], nodes ...client.Object) {
		for _, req := range r.mapNodesToProviders(ctx, nodes...) {
			q.Add(req)
		}
	}
	return handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(ctx, q, e.Object)
		},
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(ctx, q, e.ObjectOld, e.ObjectNew)
		},
		DeleteFunc: func(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(ctx, q, e.Object)
		},
		GenericFunc: func(ctx context.Context, e event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(ctx, q, e.Object)
		},
	}
}

// mapNodesToProviders returns the providers that own at least one flavor
// matched by any of the given Nodes. A Node matching no orchestrated flavor
// affects no provider. Overlaps need no special handling: a Node matching
// flavors of several providers enqueues all of them.
//
// Nodes are matched with nodeMatchesFlavor, the rule used by computeCapacity.
// Flavors and Topologies are read from the informer cache, one Get per claimed
// flavor. If any read fails for a reason other than NotFound, all providers are
// enqueued rather than dropping the event.
func (r *Reconciler) mapNodesToProviders(ctx context.Context, nodes ...client.Object) []reconcile.Request {
	providers, err := r.listLocalCapacityProviders(ctx)
	if err != nil {
		r.logger().Error(err, "Failed to list CapacityProviders")
		return nil
	}
	owners := make(map[kueuealpha.ResourceFlavorReference][]string)
	for _, p := range providers {
		for _, f := range p.Spec.OrchestratedFlavors {
			owners[f.Name] = append(owners[f.Name], p.Name)
		}
	}

	flavors := make(map[kueuealpha.ResourceFlavorReference]*kueue.ResourceFlavor, len(owners))
	topologyLevels := make(map[kueue.TopologyReference][]string)
	for name := range owners {
		var rf kueue.ResourceFlavor
		if err := r.client.Get(ctx, client.ObjectKey{Name: string(name)}, &rf); err != nil {
			if apierrors.IsNotFound(err) {
				// A missing flavor matches no Node. Its creation is handled by the ResourceFlavor watch.
				continue
			}
			return r.fallbackToAllProviders(ctx, err, "Failed to get ResourceFlavor", "resourceFlavor", name)
		}
		flavors[name] = &rf
		if rf.Spec.TopologyName == nil {
			continue
		}
		if _, ok := topologyLevels[*rf.Spec.TopologyName]; ok {
			continue
		}
		var topology kueue.Topology
		if err := r.client.Get(ctx, client.ObjectKey{Name: string(*rf.Spec.TopologyName)}, &topology); err != nil {
			if apierrors.IsNotFound(err) {
				// A flavor with a missing Topology matches no Node, as in computeCapacity.
				continue
			}
			return r.fallbackToAllProviders(ctx, err, "Failed to get Topology", "topology", *rf.Spec.TopologyName)
		}
		topologyLevels[*rf.Spec.TopologyName] = utiltas.Levels(&topology)
	}

	affected := sets.New[string]()
	for _, obj := range nodes {
		node, ok := obj.(*corev1.Node)
		if !ok || node == nil {
			return r.fallbackToAllProviders(ctx, nil, "Unexpected object in Node event")
		}
		for name, rf := range flavors {
			if nodeMatchesFlavor(node, rf, topologyLevels) {
				affected.Insert(owners[name]...)
			}
		}
	}
	requests := make([]reconcile.Request, 0, len(affected))
	for _, name := range sets.List(affected) {
		requests = append(requests, reconcile.Request{Name: name})
	}
	return requests
}

func (r *Reconciler) fallbackToAllProviders(ctx context.Context, err error, msg string, keysAndValues ...any) []reconcile.Request {
	r.logger().Error(err, msg+"; enqueueing all CapacityProviders", keysAndValues...)
	return r.mapToAllProviders(ctx, nil)
}

// mapToAllProviders enqueues every local-capacity CapacityProvider. Any Node,
// ResourceFlavor, Topology or CapacityProvider change may affect any provider, including
// through overlap detection.
func (r *Reconciler) mapToAllProviders(ctx context.Context, _ client.Object) []ctrl.Request {
	providers, err := r.listLocalCapacityProviders(ctx)
	if err != nil {
		r.logger().Error(err, "Failed to list CapacityProviders")
		return nil
	}
	requests := make([]ctrl.Request, 0, len(providers))
	for _, p := range providers {
		requests = append(requests, ctrl.Request{Name: p.Name})
	}
	return requests
}

func (r *Reconciler) listLocalCapacityProviders(ctx context.Context) ([]kueuealpha.CapacityProvider, error) {
	var list kueuealpha.CapacityProviderList
	if err := r.client.List(ctx, &list, client.MatchingFields{indexer.CapacityProviderControllerNameKey: string(ControllerName)}); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// Reconcile recomputes the capacity published by a single local-capacity CapacityProvider.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if !features.Enabled(features.DQOLocalCapacityProvider) {
		return ctrl.Result{}, nil
	}
	log := ctrl.LoggerFrom(ctx)

	var provider kueuealpha.CapacityProvider
	if err := r.client.Get(ctx, req.NamespacedName, &provider); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// The provider can be deleted and recreated for another controller while its
	// old name is still queued.
	if !provider.DeletionTimestamp.IsZero() || provider.Spec.ControllerName != ControllerName {
		return ctrl.Result{}, nil
	}
	log.V(3).Info("Reconcile local-capacity CapacityProvider")

	oldStatus := provider.Status.DeepCopy()
	in, err := r.snapshot(ctx)
	if err != nil {
		// Keep the last published capacity: a failure to observe the cluster is
		// not an observation of zero capacity.
		setSynchronizedCondition(&provider, metav1.ConditionFalse, kueuealpha.CapacityProviderReasonSourceUnavailable, err.Error())
		return ctrl.Result{}, errors.Join(err, r.updateStatus(ctx, &provider, oldStatus))
	}

	res := computeCapacity(&provider, in)
	if res.misconfigured != "" {
		setSynchronizedCondition(&provider, metav1.ConditionFalse, kueuealpha.CapacityProviderReasonMisconfigured, res.misconfigured)
	} else {
		provider.Status.Capacity = res.capacity
		setSynchronizedCondition(&provider, metav1.ConditionTrue, kueuealpha.CapacityProviderReasonSynchronized, res.summary)
	}
	return ctrl.Result{}, r.updateStatus(ctx, &provider, oldStatus)
}

// snapshot reads all inputs needed to compute capacity from the informer cache.
func (r *Reconciler) snapshot(ctx context.Context) (*inputs, error) {
	providers, err := r.listLocalCapacityProviders(ctx)
	if err != nil {
		return nil, err
	}
	var flavors kueue.ResourceFlavorList
	if err := r.client.List(ctx, &flavors); err != nil {
		return nil, err
	}
	var topologies kueue.TopologyList
	if err := r.client.List(ctx, &topologies); err != nil {
		return nil, err
	}
	var nodes corev1.NodeList
	if err := r.client.List(ctx, &nodes); err != nil {
		return nil, err
	}
	in := &inputs{
		providers:      providers,
		flavors:        make(map[kueuealpha.ResourceFlavorReference]*kueue.ResourceFlavor, len(flavors.Items)),
		topologyLevels: make(map[kueue.TopologyReference][]string, len(topologies.Items)),
		nodes:          nodes.Items,
	}
	for i := range flavors.Items {
		in.flavors[kueuealpha.ResourceFlavorReference(flavors.Items[i].Name)] = &flavors.Items[i]
	}
	for _, t := range topologies.Items {
		in.topologyLevels[kueue.TopologyReference(t.Name)] = utiltas.Levels(&t)
	}
	return in, nil
}

func setSynchronizedCondition(provider *kueuealpha.CapacityProvider, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&provider.Status.Conditions, metav1.Condition{
		Type:               kueuealpha.CapacityProviderCapacitySynchronized,
		Status:             status,
		ObservedGeneration: provider.Generation,
		Reason:             reason,
		Message:            api.TruncateConditionMessage(message),
	})
}

func (r *Reconciler) updateStatus(ctx context.Context, provider *kueuealpha.CapacityProvider, oldStatus *kueuealpha.CapacityProviderStatus) error {
	if equality.Semantic.DeepEqual(oldStatus, &provider.Status) {
		return nil
	}
	return r.client.Status().Update(ctx, provider)
}
