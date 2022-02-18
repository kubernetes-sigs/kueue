/*
Copyright 2021 The Kubernetes Authors.

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
	"sigs.k8s.io/kueue/pkg/queue"
)

// QueueReconciler reconciles a Queue object
type QueueReconciler struct {
	log    logr.Logger
	queues *queue.Manager
}

func NewQueueReconciler(queues *queue.Manager) *QueueReconciler {
	return &QueueReconciler{
		log:    ctrl.Log.WithName("queue-reconciler"),
		queues: queues,
	}
}

//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=queues,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=queues/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=queues/finalizers,verbs=update

func (r *QueueReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// No-op. All work is done in the event handler.
	return ctrl.Result{}, nil
}

func (r *QueueReconciler) Create(e event.CreateEvent) bool {
	q := e.Object.(*kueue.Queue)
	log := r.log.WithValues("queue", klog.KObj(q))
	log.V(2).Info("Queue create event")
	ctx := logr.NewContext(context.Background(), log)
	if err := r.queues.AddQueue(ctx, q); err != nil {
		log.Error(err, "Failed to add queue to system")
	}
	return false
}

func (r *QueueReconciler) Delete(e event.DeleteEvent) bool {
	q := e.Object.(*kueue.Queue)
	r.log.V(2).Info("Queue delete event", "queue", klog.KObj(q))
	r.queues.DeleteQueue(q)
	return false
}

func (r *QueueReconciler) Update(e event.UpdateEvent) bool {
	q := e.ObjectNew.(*kueue.Queue)
	log := r.log.WithValues("queue", klog.KObj(q))
	log.V(2).Info("Queue update event")
	if err := r.queues.UpdateQueue(q); err != nil {
		log.Error(err, "Failed to update queue in system")
	}
	return false
}

func (r *QueueReconciler) Generic(e event.GenericEvent) bool {
	r.log.V(3).Info("Ignore generic event", "obj", klog.KObj(e.Object), "kind", e.Object.GetObjectKind().GroupVersionKind())
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *QueueReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kueue.Queue{}).
		WithEventFilter(r).
		Complete(r)
}
