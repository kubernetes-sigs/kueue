/*
Copyright 2024 The Kubernetes Authors.

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

package tas

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/queue"
)

const (
	updateChBuffer = 10
)

type topologyReconciler struct {
	client                client.Client
	queues                *queue.Manager
	cache                 *cache.Cache
	tasCache              *cache.TASCache
	topologyUpdateCh      chan event.GenericEvent
	resourceFlavorHandler *resourceFlavorHandler
}

var _ reconcile.Reconciler = (*topologyReconciler)(nil)
var _ predicate.Predicate = (*topologyReconciler)(nil)

func newTopologyReconciler(c client.Client, queues *queue.Manager, cache *cache.Cache) *topologyReconciler {
	return &topologyReconciler{
		client:                c,
		queues:                queues,
		cache:                 cache,
		tasCache:              cache.TASCache(),
		topologyUpdateCh:      make(chan event.GenericEvent, updateChBuffer),
		resourceFlavorHandler: &resourceFlavorHandler{},
	}
}

func (r *topologyReconciler) setupWithManager(mgr ctrl.Manager, cfg *configapi.Configuration) (string, error) {
	return TASTopologyController, ctrl.NewControllerManagedBy(mgr).
		Named(TASTopologyController).
		For(&kueuealpha.Topology{}).
		WithOptions(controller.Options{NeedLeaderElection: ptr.To(false)}).
		Watches(&kueue.ResourceFlavor{}, r.resourceFlavorHandler).
		WithEventFilter(r).
		Complete(core.WithLeadingManager(mgr, r, &kueuealpha.Topology{}, cfg))
}

// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=topology,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=topology/finalizers,verbs=update

func (r topologyReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	topology := &kueuealpha.Topology{}
	if err := r.client.Get(ctx, req.NamespacedName, topology); err != nil {
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log := ctrl.LoggerFrom(ctx).WithValues("name", req.NamespacedName.Name)
	log.V(2).Info("Reconcile Topology")

	if !topology.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(topology, kueue.ResourceInUseFinalizerName) {
			var flavors []kueue.ResourceFlavorReference
			for flName, flCache := range r.tasCache.Clone() {
				if flCache.TopologyName == kueue.TopologyReference(topology.Name) {
					flavors = append(flavors, flName)
				}
			}
			if len(flavors) != 0 {
				log.V(3).Info("topology is still in use", "ResourceFlavors", flavors)
				return ctrl.Result{}, nil
			}
			controllerutil.RemoveFinalizer(topology, kueue.ResourceInUseFinalizerName)
			if err := r.client.Update(ctx, topology); err != nil {
				return ctrl.Result{}, err
			}
			log.V(5).Info("Removed finalizer")
		}
	}

	if controllerutil.AddFinalizer(topology, kueue.ResourceInUseFinalizerName) {
		if err := r.client.Update(ctx, topology); err != nil {
			return ctrl.Result{}, err
		}
		log.V(5).Info("Added finalizer")
	}

	return reconcile.Result{}, nil
}

func (r *topologyReconciler) Generic(event.GenericEvent) bool {
	return false
}

func (r *topologyReconciler) Create(e event.CreateEvent) bool {
	topology, isTopology := e.Object.(*kueuealpha.Topology)
	if !isTopology {
		return true
	}

	ctx := context.Background()

	flavors := &kueue.ResourceFlavorList{}
	if err := r.client.List(ctx, flavors, client.MatchingFields{indexer.ResourceFlavorTopologyNameKey: topology.Name}); err != nil {
		log := ctrl.LoggerFrom(ctx).WithValues("topology", klog.KObj(topology))
		log.Error(err, "Could not list resource flavors")
		return true
	}

	defer r.queues.NotifyTopologyUpdateWatchers(nil, topology)

	// Update the cache to account for the created topology, before
	// notifying the listeners.
	for _, flv := range flavors.Items {
		if flv.Spec.TopologyName == nil {
			continue
		}
		if *flv.Spec.TopologyName == kueue.TopologyReference(topology.Name) {
			r.cache.AddOrUpdateTopologyForFlavor(topology, &flv)
		}
	}

	return true
}

func (r *topologyReconciler) Update(event.UpdateEvent) bool {
	return true
}

func (r *topologyReconciler) Delete(e event.DeleteEvent) bool {
	topology, isTopology := e.Object.(*kueuealpha.Topology)
	if !isTopology {
		return true
	}
	defer r.queues.NotifyTopologyUpdateWatchers(topology, nil)
	// Update the cache to account for the deleted topology, before notifying
	// the listeners.
	for flName, flCache := range r.tasCache.Clone() {
		if kueue.TopologyReference(topology.Name) == flCache.TopologyName {
			r.cache.DeleteTopologyForFlavor(flName)
		}
	}
	return true
}

var _ handler.EventHandler = (*resourceFlavorHandler)(nil)

type resourceFlavorHandler struct{}

func (h *resourceFlavorHandler) Generic(_ context.Context, e event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *resourceFlavorHandler) Create(context.Context, event.CreateEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *resourceFlavorHandler) Update(context.Context, event.UpdateEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *resourceFlavorHandler) Delete(_ context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	resourceFlavor, isResourceFlavor := e.Object.(*kueue.ResourceFlavor)
	if !isResourceFlavor || resourceFlavor.Spec.TopologyName == nil {
		return
	}
	q.AddAfter(reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: string(*resourceFlavor.Spec.TopologyName),
		},
	}, nodeBatchPeriod)
}
