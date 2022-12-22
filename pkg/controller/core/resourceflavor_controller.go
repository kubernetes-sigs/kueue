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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/queue"
)

// ResourceFlavorReconciler reconciles a ResourceFlavor object
type ResourceFlavorReconciler struct {
	log        logr.Logger
	qManager   *queue.Manager
	cache      *cache.Cache
	client     client.Client
	cqUpdateCh chan event.GenericEvent
}

func NewResourceFlavorReconciler(client client.Client, qMgr *queue.Manager, cache *cache.Cache) *ResourceFlavorReconciler {
	return &ResourceFlavorReconciler{
		log:        ctrl.Log.WithName("resourceflavor-reconciler"),
		cache:      cache,
		client:     client,
		qManager:   qMgr,
		cqUpdateCh: make(chan event.GenericEvent, updateChBuffer),
	}
}

//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch;update;delete
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors/finalizers,verbs=update

func (r *ResourceFlavorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var flavor kueue.ResourceFlavor
	if err := r.client.Get(ctx, req.NamespacedName, &flavor); err != nil {
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log := ctrl.LoggerFrom(ctx).WithValues("resourceFlavor", klog.KObj(&flavor))
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(2).Info("Reconciling ResourceFlavor")

	if flavor.ObjectMeta.DeletionTimestamp.IsZero() {
		// Although we'll add the finalizer via webhook mutation now, this is still useful
		// as a fallback.
		if !controllerutil.ContainsFinalizer(&flavor, kueue.ResourceInUseFinalizerName) {
			controllerutil.AddFinalizer(&flavor, kueue.ResourceInUseFinalizerName)
			if err := r.client.Update(ctx, &flavor); err != nil {
				return ctrl.Result{}, err
			}
		}
	} else {
		if controllerutil.ContainsFinalizer(&flavor, kueue.ResourceInUseFinalizerName) {
			if cqs := r.cache.ClusterQueuesUsingFlavor(flavor.Name); len(cqs) != 0 {
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
		}
	}

	return ctrl.Result{}, nil
}

func (r *ResourceFlavorReconciler) Create(e event.CreateEvent) bool {
	flv, match := e.Object.(*kueue.ResourceFlavor)
	if !match {
		return false
	}

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

	log := r.log.WithValues("resourceFlavor", klog.KObj(flv))
	log.V(2).Info("ResourceFlavor delete event")

	if cqNames := r.cache.DeleteResourceFlavor(flv); len(cqNames) > 0 {
		r.qManager.QueueInadmissibleWorkloads(context.Background(), cqNames)
	}
	return false
}

func (r *ResourceFlavorReconciler) Update(e event.UpdateEvent) bool {
	flv, match := e.ObjectNew.(*kueue.ResourceFlavor)
	if !match {
		return false
	}

	log := r.log.WithValues("resourceFlavor", klog.KObj(flv))
	log.V(2).Info("ResourceFlavor update event")

	if flv.DeletionTimestamp != nil {
		return true
	}

	if cqNames := r.cache.AddOrUpdateResourceFlavor(flv.DeepCopy()); len(cqNames) > 0 {
		r.qManager.QueueInadmissibleWorkloads(context.Background(), cqNames)
	}
	return false
}

func (r *ResourceFlavorReconciler) Generic(e event.GenericEvent) bool {
	r.log.V(2).Info("Got generic event", "obj", klog.KObj(e.Object), "kind", e.Object.GetObjectKind().GroupVersionKind())
	return true
}

// NotifyClusterQueueUpdate will listen for the update/delete events of clusterQueues to help
// verifying whether resourceFlavors are no longer in use by clusterQueues. There're mainly
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

func (h *cqHandler) Create(event.CreateEvent, workqueue.RateLimitingInterface) {
}

func (h *cqHandler) Update(event.UpdateEvent, workqueue.RateLimitingInterface) {
}

func (h *cqHandler) Delete(event.DeleteEvent, workqueue.RateLimitingInterface) {
}

// Generic accepts update/delete events from clusterQueue via channel.
// For update events, we only check the old obj to see whether old resourceFlavors
// are still in use since new resourceFlavors are always in use.
// For delete events, we check the original obj since new obj is nil.
func (h *cqHandler) Generic(e event.GenericEvent, q workqueue.RateLimitingInterface) {
	cq := e.Object.(*kueue.ClusterQueue)
	if cq.Name == "" {
		return
	}

	for _, resource := range cq.Spec.Resources {
		for _, flavor := range resource.Flavors {
			if cqs := h.cache.ClusterQueuesUsingFlavor(string(flavor.Name)); len(cqs) == 0 {
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
func (r *ResourceFlavorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	handler := cqHandler{
		cache: r.cache,
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&kueue.ResourceFlavor{}).
		Watches(&source.Channel{Source: r.cqUpdateCh}, &handler).
		WithEventFilter(r).
		Complete(r)
}

func resourceFlavors(cq *kueue.ClusterQueue) sets.String {
	flavors := sets.NewString()
	for _, resource := range cq.Spec.Resources {
		for _, flavor := range resource.Flavors {
			flavors.Insert(string(flavor.Name))
		}
	}
	return flavors
}
