/*
Copyright 2022 The Kubernetes Authors.

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

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/queue"
)

type ResourceFlavorUpdateWatcher interface {
	NotifyResourceFlavorUpdate(oldRF, newRF *kueue.ResourceFlavor)
}

// ResourceFlavorReconciler reconciles a ResourceFlavor object
type ResourceFlavorReconciler struct {
	log        logr.Logger
	qManager   *queue.Manager
	cache      *cache.Cache
	client     client.Client
	cqUpdateCh chan event.GenericEvent
	watchers   []ResourceFlavorUpdateWatcher
}

func NewResourceFlavorReconciler(
	client client.Client,
	qMgr *queue.Manager,
	cache *cache.Cache,
) *ResourceFlavorReconciler {
	return &ResourceFlavorReconciler{
		log:        ctrl.Log.WithName("resourceflavor-reconciler"),
		cache:      cache,
		client:     client,
		qManager:   qMgr,
		cqUpdateCh: make(chan event.GenericEvent, updateChBuffer),
	}
}

// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch;update;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors/finalizers,verbs=update

func (r *ResourceFlavorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var flavor kueue.ResourceFlavor
	if err := r.client.Get(ctx, req.NamespacedName, &flavor); err != nil {
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log := ctrl.LoggerFrom(ctx).WithValues("resourceFlavor", klog.KObj(&flavor))
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(2).Info("Reconciling ResourceFlavor")

	if flavor.DeletionTimestamp.IsZero() {
		// Although we'll add the finalizer via webhook mutation now, this is still useful
		// as a fallback.
		if controllerutil.AddFinalizer(&flavor, kueue.ResourceInUseFinalizerName) {
			if err := r.client.Update(ctx, &flavor); err != nil {
				return ctrl.Result{}, err
			}
			log.V(5).Info("Added finalizer")
		}
	} else {
		if controllerutil.ContainsFinalizer(&flavor, kueue.ResourceInUseFinalizerName) {
			if cqs := r.cache.ClusterQueuesUsingFlavor(kueue.ResourceFlavorReference(flavor.Name)); len(cqs) != 0 {
				log.V(3).Info("resourceFlavor is still in use", "ClusterQueues", cqs)
				// We avoid to return error here to prevent backoff requeue, which is passive and wasteful.
				// Instead, we drive the removal of finalizer by ClusterQueue Update/Delete events
				// when resourceFlavor is no longer in use.
				return ctrl.Result{}, nil
			}

			controllerutil.RemoveFinalizer(&flavor, kueue.ResourceInUseFinalizerName)
			if err := r.client.Update(ctx, &flavor); err != nil {
				return ctrl.Result{}, err
			}
			log.V(5).Info("Removed finalizer")
		}
	}

	return ctrl.Result{}, nil
}

func (r *ResourceFlavorReconciler) AddUpdateWatcher(watchers ...ResourceFlavorUpdateWatcher) {
	r.watchers = append(r.watchers, watchers...)
}

func (r *ResourceFlavorReconciler) notifyWatchers(oldRF, newRF *kueue.ResourceFlavor) {
	for _, w := range r.watchers {
		w.NotifyResourceFlavorUpdate(oldRF, newRF)
	}
}

func (r *ResourceFlavorReconciler) Create(e event.CreateEvent) bool {
	flv, match := e.Object.(*kueue.ResourceFlavor)
	if !match {
		return false
	}
	defer r.notifyWatchers(nil, flv)

	log := r.log.WithValues("resourceFlavor", klog.KObj(flv))
	log.V(2).Info("ResourceFlavor create event")

	// As long as one clusterQueue becomes active,
	// we should inform clusterQueue controller to broadcast the event.
	if cqNames := r.cache.AddOrUpdateResourceFlavor(flv.DeepCopy()); len(cqNames) > 0 {
		r.qManager.QueueInadmissibleWorkloads(context.Background(), cqNames)
		// If at least one CQ becomes active, then those CQs should now get evaluated by the scheduler;
		// note that the workloads in those CQs are not necessarily "inadmissible", and hence we trigger a
		// broadcast here in all cases.
		r.qManager.Broadcast()
	}
	return true
}

func (r *ResourceFlavorReconciler) Delete(e event.DeleteEvent) bool {
	flv, match := e.Object.(*kueue.ResourceFlavor)
	if !match {
		return false
	}
	defer r.notifyWatchers(flv, nil)

	log := r.log.WithValues("resourceFlavor", klog.KObj(flv))
	log.V(2).Info("ResourceFlavor delete event")

	if cqNames := r.cache.DeleteResourceFlavor(flv); len(cqNames) > 0 {
		r.qManager.QueueInadmissibleWorkloads(context.Background(), cqNames)
	}
	return false
}

func (r *ResourceFlavorReconciler) Update(e event.UpdateEvent) bool {
	oldFlv, match := e.ObjectOld.(*kueue.ResourceFlavor)
	if !match {
		return false
	}
	newFlv, match := e.ObjectNew.(*kueue.ResourceFlavor)
	if !match {
		return false
	}
	defer r.notifyWatchers(oldFlv, newFlv)

	log := r.log.WithValues("resourceFlavor", klog.KObj(newFlv))
	log.V(2).Info("ResourceFlavor update event")

	if newFlv.DeletionTimestamp != nil {
		return true
	}

	if cqNames := r.cache.AddOrUpdateResourceFlavor(newFlv.DeepCopy()); len(cqNames) > 0 {
		r.qManager.QueueInadmissibleWorkloads(context.Background(), cqNames)
	}
	return false
}

func (r *ResourceFlavorReconciler) Generic(e event.GenericEvent) bool {
	r.log.V(2).Info("Got generic event", "obj", klog.KObj(e.Object), "kind", e.Object.GetObjectKind().GroupVersionKind())
	return true
}

// NotifyClusterQueueUpdate will listen for the update/delete events of clusterQueues to help
// verifying whether resourceFlavors are no longer in use by clusterQueues. There are mainly
// two reasons for this, 1) a clusterQueue is deleted 2) a clusterQueue is updated with
// the resourceFlavors in use.
func (r *ResourceFlavorReconciler) NotifyClusterQueueUpdate(oldCQ, newCQ *kueue.ClusterQueue) {
	// if oldCQ is nil, it's a create event.
	if oldCQ == nil {
		return
	}

	// if newCQ is nil, it's a delete event.
	if newCQ == nil {
		r.cqUpdateCh <- event.GenericEvent{Object: oldCQ}
		return
	}

	oldFlavors := resourceFlavors(oldCQ)
	newFlavors := resourceFlavors(newCQ)
	if !oldFlavors.Equal(newFlavors) {
		r.cqUpdateCh <- event.GenericEvent{Object: oldCQ}
	}
}

// cqHandler signals the controller to reconcile the resourceFlavor
// associated to the clusterQueue in the event.
// Since the events come from a channel Source, only the Generic handler will
// receive events.
type cqHandler struct {
	cache *cache.Cache
}

func (h *cqHandler) Create(context.Context, event.CreateEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *cqHandler) Update(context.Context, event.UpdateEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *cqHandler) Delete(context.Context, event.DeleteEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

// Generic accepts update/delete events from clusterQueue via channel.
// For update events, we only check the old obj to see whether old resourceFlavors
// are still in use since new resourceFlavors are always in use.
// For delete events, we check the original obj since new obj is nil.
func (h *cqHandler) Generic(_ context.Context, e event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	cq := e.Object.(*kueue.ClusterQueue)
	if cq.Name == "" {
		return
	}

	for _, rg := range cq.Spec.ResourceGroups {
		for _, flavor := range rg.Flavors {
			if cqs := h.cache.ClusterQueuesUsingFlavor(flavor.Name); len(cqs) == 0 {
				req := reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name: string(flavor.Name),
					},
				}
				q.Add(req)
			}
		}
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ResourceFlavorReconciler) SetupWithManager(mgr ctrl.Manager, cfg *config.Configuration) error {
	handler := cqHandler{
		cache: r.cache,
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&kueue.ResourceFlavor{}).
		WithOptions(controller.Options{NeedLeaderElection: ptr.To(false)}).
		WatchesRawSource(source.Channel(r.cqUpdateCh, &handler)).
		WithEventFilter(r).
		Complete(WithLeadingManager(mgr, r, &kueue.ResourceFlavor{}, cfg))
}

func resourceFlavors(cq *kueue.ClusterQueue) sets.Set[kueue.ResourceFlavorReference] {
	flavors := sets.New[kueue.ResourceFlavorReference]()
	for _, rg := range cq.Spec.ResourceGroups {
		for _, flavor := range rg.Flavors {
			flavors.Insert(flavor.Name)
		}
	}
	return flavors
}
