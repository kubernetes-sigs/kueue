/*
Copyright 2022 Google LLC.

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
	"gke-internal.googlesource.com/gke-batch/kueue/pkg/queue"
)

const (
	// statuses for logging purposes

	pending  = "pending"
	assigned = "assigned"
)

// QueuedWorkloadReconciler reconciles a QueuedWorkload object
type QueuedWorkloadReconciler struct {
	log    logr.Logger
	queues *queue.Manager
}

func NewQueuedWorkloadReconciler(queues *queue.Manager) *QueuedWorkloadReconciler {
	return &QueuedWorkloadReconciler{
		log:    ctrl.Log.WithName("queued-workload-reconciler"),
		queues: queues,
	}
}

//+kubebuilder:rbac:groups=kueue.gke-internal.googlesource.com,resources=queuedworkloads,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kueue.gke-internal.googlesource.com,resources=queuedworkloads/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kueue.gke-internal.googlesource.com,resources=queuedworkloads/finalizers,verbs=update

func (r *QueuedWorkloadReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// No-op. All work is done in the event handler.
	return ctrl.Result{}, nil
}

func (r *QueuedWorkloadReconciler) Create(e event.CreateEvent) bool {
	wl := e.Object.(*kueue.QueuedWorkload)
	status := workloadStatus(wl)
	log := r.log.WithValues("queuedWorkload", klog.KObj(wl), "queue", wl.Spec.QueueName, "status", status)
	log.V(2).Info("QueuedWorkload create event")
	if wl.Spec.Assignment == nil {
		if !r.queues.AddWorkload(wl) {
			log.V(2).Info("Queue for workload didn't exist; ignored for now")
		}
		return false
	}
	// TODO: add to cache
	return false
}

func (r *QueuedWorkloadReconciler) Delete(e event.DeleteEvent) bool {
	wl := e.Object.(*kueue.QueuedWorkload)
	status := "unknown"
	if !e.DeleteStateUnknown {
		status = workloadStatus(wl)
	}
	log := r.log.WithValues("queuedWorkload", klog.KObj(wl), "queue", wl.Spec.QueueName, "status", status)
	log.V(2).Info("QueuedWorkload delete event")
	// When assigning a capacity to a workload, we assume it in the cache. If
	// the state is unknown, the workload could have been assumed and we need
	// to clear it from the cache.
	if wl.Spec.Assignment != nil || e.DeleteStateUnknown {
		// TODO: remove from cache.
	}
	// Even if the state is unknown, the last cached state tells us whether the
	// workload was in the queues and should be cleared from them.
	if wl.Spec.Assignment == nil {
		r.queues.DeleteWorkload(wl)
	}
	return false
}

func (r *QueuedWorkloadReconciler) Update(e event.UpdateEvent) bool {
	wl := e.ObjectNew.(*kueue.QueuedWorkload)
	oldWl := e.ObjectOld.(*kueue.QueuedWorkload)
	status := workloadStatus(wl)
	log := r.log.WithValues("queuedWorkload", klog.KObj(wl), "queue", wl.Spec.QueueName, "status", status)
	prevQueue := oldWl.Spec.QueueName
	if prevQueue != wl.Spec.QueueName {
		log = log.WithValues("prevQueue", prevQueue)
	}
	prevStatus := workloadStatus(oldWl)
	if prevStatus != status {
		log = log.WithValues("prevStatus", status)
	}
	log.V(2).Info("QueuedWorkload update event")

	switch {
	case prevStatus == pending && status == pending:
		if !r.queues.UpdateWorkload(wl, prevQueue) {
			log.V(2).Info("Queue for updated workload didn't exist; ignoring for now")
		}

	case prevStatus == pending && status == assigned:
		r.queues.DeleteWorkload(wl)
		// TODO: add to cache.

	case prevStatus == assigned && status == pending:
		// TODO: remove from cache.
		if !r.queues.AddWorkload(wl) {
			log.V(2).Info("Queue for workload didn't exist; ignored for now")
		}

	default:
		// TODO: update in cache.
	}

	return false
}

func (r *QueuedWorkloadReconciler) Generic(e event.GenericEvent) bool {
	r.log.V(3).Info("Ignore generic event", "obj", klog.KObj(e.Object), "kind", e.Object.GetObjectKind().GroupVersionKind())
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *QueuedWorkloadReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kueue.QueuedWorkload{}).
		WithEventFilter(r).
		Complete(r)
}

func workloadStatus(w *kueue.QueuedWorkload) string {
	if w.Spec.Assignment != nil {
		return assigned
	}
	return pending
}
