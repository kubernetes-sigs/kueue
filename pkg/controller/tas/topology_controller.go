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

package tas

import (
	"context"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/queue"
)

const (
	updateChBuffer = 10
)

type topologyReconciler struct {
	log              logr.Logger
	client           client.Client
	queues           *queue.Manager
	cache            *cache.Cache
	tasCache         *cache.TASCache
	topologyUpdateCh chan event.GenericEvent
}

var _ reconcile.Reconciler = (*topologyReconciler)(nil)
var _ predicate.TypedPredicate[*kueuealpha.Topology] = (*topologyReconciler)(nil)

func newTopologyReconciler(c client.Client, queues *queue.Manager, cache *cache.Cache) *topologyReconciler {
	return &topologyReconciler{
		log:              ctrl.Log.WithName(TASTopologyController),
		client:           c,
		queues:           queues,
		cache:            cache,
		tasCache:         cache.TASCache(),
		topologyUpdateCh: make(chan event.GenericEvent, updateChBuffer),
	}
}

func (r *topologyReconciler) setupWithManager(mgr ctrl.Manager, cfg *configapi.Configuration) (string, error) {
	return TASTopologyController, builder.TypedControllerManagedBy[reconcile.Request](mgr).
		Named("tas_topology_controller").
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&kueuealpha.Topology{},
			&handler.TypedEnqueueRequestForObject[*kueuealpha.Topology]{},
			r,
		)).
		WithOptions(controller.Options{NeedLeaderElection: ptr.To(false)}).
		Watches(&kueue.ResourceFlavor{}, &resourceFlavorHandler{}).
		Complete(core.WithLeadingManager(mgr, r, &kueuealpha.Topology{}, cfg))
}

// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=topologies,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=topologies/finalizers,verbs=update

func (r *topologyReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	topology := &kueuealpha.Topology{}
	if err := r.client.Get(ctx, req.NamespacedName, topology); err != nil {
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log := ctrl.LoggerFrom(ctx)
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
	} else if controllerutil.AddFinalizer(topology, kueue.ResourceInUseFinalizerName) {
		if err := r.client.Update(ctx, topology); err != nil {
			return ctrl.Result{}, err
		}
		log.V(5).Info("Added finalizer")
	}

	return reconcile.Result{}, nil
}

func (r *topologyReconciler) Generic(event.TypedGenericEvent[*kueuealpha.Topology]) bool {
	return false
}

func (r *topologyReconciler) Create(e event.TypedCreateEvent[*kueuealpha.Topology]) bool {
	log := r.log.WithValues("topology", klog.KObj(e.Object))
	log.V(2).Info("Topology create event")

	ctx := context.Background()

	flavors := &kueue.ResourceFlavorList{}
	if err := r.client.List(ctx, flavors, client.MatchingFields{indexer.ResourceFlavorTopologyNameKey: e.Object.Name}); err != nil {
		log.Error(err, "Could not list resource flavors")
		return true
	}

	defer r.queues.NotifyTopologyUpdateWatchers(nil, e.Object)

	// Update the cache to account for the created topology, before
	// notifying the listeners.
	for _, flv := range flavors.Items {
		if flv.Spec.TopologyName == nil {
			continue
		}
		if *flv.Spec.TopologyName == kueue.TopologyReference(e.Object.Name) {
			log.V(3).Info("Updating Topology cache for flavor", "flavor", flv.Name)
			r.cache.AddOrUpdateTopologyForFlavor(e.Object, &flv)
		}
	}

	return true
}

func (r *topologyReconciler) Update(event.TypedUpdateEvent[*kueuealpha.Topology]) bool {
	return true
}

func (r *topologyReconciler) Delete(e event.TypedDeleteEvent[*kueuealpha.Topology]) bool {
	log := r.log.WithValues("topology", klog.KObj(e.Object))
	log.V(2).Info("Topology delete event")

	defer r.queues.NotifyTopologyUpdateWatchers(e.Object, nil)
	// Update the cache to account for the deleted topology, before notifying
	// the listeners.
	for flName, flCache := range r.tasCache.Clone() {
		if kueue.TopologyReference(e.Object.Name) == flCache.TopologyName {
			log.V(3).Info("Deleting topology from cache for flavor", "flavorName", flName)
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
	}, constants.UpdatesBatchPeriod)
}
