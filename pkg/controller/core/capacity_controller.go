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
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	kueue "sigs.k8s.io/kueue/api/v1alpha1"
	"sigs.k8s.io/kueue/pkg/capacity"
)

// CapacityReconciler reconciles a Capacity object
type CapacityReconciler struct {
	client client.Client
	log    logr.Logger
	cache  *capacity.Cache
}

func NewCapacityReconciler(client client.Client, cache *capacity.Cache) *CapacityReconciler {
	return &CapacityReconciler{
		client: client,
		log:    ctrl.Log.WithName("capacity-reconciler"),
		cache:  cache,
	}
}

//+kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=capacities,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=capacities/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=capacities/finalizers,verbs=update

func (r *CapacityReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var capObj kueue.Capacity
	if err := r.client.Get(ctx, req.NamespacedName, &capObj); err != nil {
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log := ctrl.LoggerFrom(ctx).WithValues("capacity", klog.KObj(&capObj))
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(2).Info("Reconciling Capacity")

	usage, workloads, err := r.cache.Usage(&capObj)
	if err != nil {
		log.Error(err, "Failed getting usage from capacity cache")
		// This is likely because the capacity was recently removed and we didn't
		// process that event yet.
		return ctrl.Result{}, err
	}
	// Shallow copy enough for now.
	oldStatus := capObj.Status
	capObj.Status.UsedResources = usage
	capObj.Status.AssignedWorkloads = int32(workloads)
	if !equality.Semantic.DeepEqual(oldStatus, capObj.Status) {
		err = r.client.Status().Update(ctx, &capObj)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return ctrl.Result{}, nil
}

// Event handlers return true to signal the controller to reconcile the
// Capacity associated with the event.

func (r *CapacityReconciler) Create(e event.CreateEvent) bool {
	c, match := e.Object.(*kueue.Capacity)
	if !match {
		// No need to interact with the cache for other objects.
		return true
	}
	log := r.log.WithValues("capacity", klog.KObj(c))
	log.V(2).Info("Capacity create event")
	ctx := ctrl.LoggerInto(context.Background(), log)
	if err := r.cache.AddCapacity(ctx, c); err != nil {
		log.Error(err, "Failed to add capacity to cache")
	}
	return true
}

func (r *CapacityReconciler) Delete(e event.DeleteEvent) bool {
	c, match := e.Object.(*kueue.Capacity)
	if !match {
		// No need to interact with the cache for other objects.
		return true
	}
	r.log.V(2).Info("Queue delete event", "capacity", klog.KObj(c))
	r.cache.DeleteCapacity(c)
	return true
}

func (r *CapacityReconciler) Update(e event.UpdateEvent) bool {
	c, match := e.ObjectNew.(*kueue.Capacity)
	if !match {
		// No need to interact with the cache for other objects.
		return true
	}
	log := r.log.WithValues("capacity", klog.KObj(c))
	log.V(2).Info("Capacity update event")
	if err := r.cache.UpdateCapacity(c); err != nil {
		log.Error(err, "Failed to update capacity in cache")
	}
	return true
}

func (r *CapacityReconciler) Generic(e event.GenericEvent) bool {
	r.log.V(3).Info("Ignore generic event", "obj", klog.KObj(e.Object), "kind", e.Object.GetObjectKind().GroupVersionKind())
	return true
}

// assignedWorkloadHandler signals the controller to reconcile the Capacity
// assigned to the workload in the event.
type assignedWorkloadHandler struct{}

func (h *assignedWorkloadHandler) Create(e event.CreateEvent, q workqueue.RateLimitingInterface) {
	w := e.Object.(*kueue.QueuedWorkload)
	if w.Spec.AssignedCapacity != "" {
		q.Add(requestForWorkloadCapacity(w))
	}
}

func (h *assignedWorkloadHandler) Update(e event.UpdateEvent, q workqueue.RateLimitingInterface) {
	oldW := e.ObjectOld.(*kueue.QueuedWorkload)
	if oldW.Spec.AssignedCapacity != "" {
		q.Add(requestForWorkloadCapacity(oldW))
	}
	newW := e.ObjectNew.(*kueue.QueuedWorkload)
	if newW.Spec.AssignedCapacity != "" && newW.Spec.AssignedCapacity != oldW.Spec.AssignedCapacity {
		q.Add(requestForWorkloadCapacity(newW))
	}
}

func (h *assignedWorkloadHandler) Delete(e event.DeleteEvent, q workqueue.RateLimitingInterface) {
	w := e.Object.(*kueue.QueuedWorkload)
	if w.Spec.AssignedCapacity != "" {
		q.Add(requestForWorkloadCapacity(w))
	}
}

func (h *assignedWorkloadHandler) Generic(e event.GenericEvent, q workqueue.RateLimitingInterface) {
}

func requestForWorkloadCapacity(w *kueue.QueuedWorkload) reconcile.Request {
	return reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: string(w.Spec.AssignedCapacity),
		},
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *CapacityReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kueue.Capacity{}).
		Watches(&source.Kind{Type: &kueue.QueuedWorkload{}}, &assignedWorkloadHandler{}).
		WithEventFilter(r).
		Complete(r)
}
