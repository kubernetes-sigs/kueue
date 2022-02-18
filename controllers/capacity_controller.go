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

package controllers

import (
	"context"

	"github.com/go-logr/logr"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kueue "sigs.k8s.io/kueue/api/v1alpha1"
	"sigs.k8s.io/kueue/pkg/capacity"
)

// CapacityReconciler reconciles a Capacity object
type CapacityReconciler struct {
	log   logr.Logger
	cache *capacity.Cache
}

func NewCapacityReconciler(cache *capacity.Cache) *CapacityReconciler {
	return &CapacityReconciler{
		log:   ctrl.Log.WithName("capacity-reconciler"),
		cache: cache,
	}
}

//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=capacities,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=capacities/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=capacities/finalizers,verbs=update

func (r *CapacityReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// No-op. All work is done in the event handler.
	return ctrl.Result{}, nil
}

func (r *CapacityReconciler) Create(e event.CreateEvent) bool {
	cap := e.Object.(*kueue.Capacity)
	log := r.log.WithValues("capacity", klog.KObj(cap))
	log.V(2).Info("Capacity create event")
	ctx := ctrl.LoggerInto(context.Background(), log)
	if err := r.cache.AddCapacity(ctx, cap); err != nil {
		log.Error(err, "Failed to add capacity to cache")
	}
	return false
}

func (r *CapacityReconciler) Delete(e event.DeleteEvent) bool {
	cap := e.Object.(*kueue.Capacity)
	r.log.V(2).Info("Queue delete event", "capacity", klog.KObj(cap))
	r.cache.DeleteCapacity(cap)
	return false
}

func (r *CapacityReconciler) Update(e event.UpdateEvent) bool {
	cap := e.ObjectNew.(*kueue.Capacity)
	log := r.log.WithValues("queue", klog.KObj(cap))
	log.V(2).Info("Capacity update event")
	if err := r.cache.UpdateCapacity(cap); err != nil {
		log.Error(err, "Failed to update capacity in cache")
	}
	return false
}

func (r *CapacityReconciler) Generic(e event.GenericEvent) bool {
	r.log.V(3).Info("Ignore generic event", "obj", klog.KObj(e.Object), "kind", e.Object.GetObjectKind().GroupVersionKind())
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *CapacityReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kueue.Capacity{}).
		WithEventFilter(r).
		Complete(r)
}
