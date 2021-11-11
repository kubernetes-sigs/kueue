/*
Copyright 2021 Google LLC.

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

	kueue "gke-internal.googlesource.com/gke-batch/kueue/api/v1alpha1"
	"gke-internal.googlesource.com/gke-batch/kueue/pkg/capacity"
)

// QueueCapacityReconciler reconciles a QueueCapacity object
type QueueCapacityReconciler struct {
	log   logr.Logger
	cache *capacity.Cache
}

func NewQueueCapacityReconciler(cache *capacity.Cache) *QueueCapacityReconciler {
	return &QueueCapacityReconciler{
		log:   ctrl.Log.WithName("queue-capacity-reconciler"),
		cache: cache,
	}
}

//+kubebuilder:rbac:groups=kueue.gke-internal.googlesource.com,resources=queuecapacities,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kueue.gke-internal.googlesource.com,resources=queuecapacities/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kueue.gke-internal.googlesource.com,resources=queuecapacities/finalizers,verbs=update

func (r *QueueCapacityReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// No-op. All work is done in the event handler.
	return ctrl.Result{}, nil
}

func (r *QueueCapacityReconciler) Create(e event.CreateEvent) bool {
	cap := e.Object.(*kueue.QueueCapacity)
	log := r.log.WithValues("queueCapacity", klog.KObj(cap))
	log.V(2).Info("QueueCapacity create event")
	if err := r.cache.AddCapacity(cap); err != nil {
		log.Error(err, "Failed to add capacity to cache")
	}
	return false
}

func (r *QueueCapacityReconciler) Delete(e event.DeleteEvent) bool {
	cap := e.Object.(*kueue.QueueCapacity)
	r.log.V(2).Info("Queue delete event", "queueCapacity", klog.KObj(cap))
	r.cache.DeleteCapacity(cap)
	return false
}

func (r *QueueCapacityReconciler) Update(e event.UpdateEvent) bool {
	cap := e.ObjectNew.(*kueue.QueueCapacity)
	log := r.log.WithValues("queue", klog.KObj(cap))
	log.V(2).Info("QueueCapacity update event")
	if err := r.cache.UpdateCapacity(cap); err != nil {
		log.Error(err, "Failed to update capacity in cache")
	}
	return false
}

func (r *QueueCapacityReconciler) Generic(e event.GenericEvent) bool {
	r.log.V(3).Info("Ignore generic event", "obj", klog.KObj(e.Object), "kind", e.Object.GetObjectKind().GroupVersionKind())
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *QueueCapacityReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kueue.QueueCapacity{}).
		WithEventFilter(r).
		Complete(r)
}
