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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kueue "sigs.k8s.io/kueue/api/v1alpha1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/queue"
)

const (
	// statuses for logging purposes
	pending                 = "pending"
	admitted                = "admitted"
	finished                = "finished"
	queueDoesntExist        = "Queue for updated workload didn't exist; ignoring for now"
	clusterQueueDoesntExist = "ClusterQueue for workload didn't exist; ignored for now"
)

// QueuedWorkloadReconciler reconciles a QueuedWorkload object
type QueuedWorkloadReconciler struct {
	log    logr.Logger
	queues *queue.Manager
	cache  *cache.Cache
}

func NewQueuedWorkloadReconciler(queues *queue.Manager, cache *cache.Cache) *QueuedWorkloadReconciler {
	return &QueuedWorkloadReconciler{
		log:    ctrl.Log.WithName("queued-workload-reconciler"),
		queues: queues,
		cache:  cache,
	}
}

//+kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=queuedworkloads,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=queuedworkloads/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=queuedworkloads/finalizers,verbs=update

func (r *QueuedWorkloadReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// No-op. All work is done in the event handler.
	return ctrl.Result{}, nil
}

func (r *QueuedWorkloadReconciler) Create(e event.CreateEvent) bool {
	wl := e.Object.(*kueue.QueuedWorkload)
	status := workloadStatus(wl)
	log := r.log.WithValues("queuedWorkload", klog.KObj(wl), "queue", wl.Spec.QueueName, "status", status)
	log.V(2).Info("QueuedWorkload create event")

	if status == finished {
		return false
	}

	if wl.Spec.Admission == nil {
		if !r.queues.AddOrUpdateWorkload(wl.DeepCopy()) {
			log.V(2).Info("Queue for workload didn't exist; ignored for now")
		}
		return false
	}
	if !r.cache.AddOrUpdateWorkload(wl.DeepCopy()) {
		log.V(2).Info("ClusterQueue for workload didn't exist; ignored for now")
	}

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
	// When assigning a clusterQueue to a workload, we assume it in the cache. If
	// the state is unknown, the workload could have been assumed and we need
	// to clear it from the cache.
	if wl.Spec.Admission != nil || e.DeleteStateUnknown {
		if err := r.cache.DeleteWorkload(wl); err != nil {
			if !e.DeleteStateUnknown {
				log.Error(err, "Failed to delete workload from cache")
			}
		}
	}
	// Even if the state is unknown, the last cached state tells us whether the
	// workload was in the queues and should be cleared from them.
	if wl.Spec.Admission == nil {
		r.queues.DeleteWorkload(wl)
	}
	return false
}

func (r *QueuedWorkloadReconciler) Update(e event.UpdateEvent) bool {
	wl := e.ObjectNew.(*kueue.QueuedWorkload)
	oldWl := e.ObjectOld.(*kueue.QueuedWorkload)
	status := workloadStatus(wl)
	log := r.log.WithValues("queuedWorkload", klog.KObj(wl), "queue", wl.Spec.QueueName, "status", status)
	ctx := ctrl.LoggerInto(context.Background(), log)
	prevQueue := oldWl.Spec.QueueName
	if prevQueue != wl.Spec.QueueName {
		log = log.WithValues("prevQueue", prevQueue)
	}
	prevStatus := workloadStatus(oldWl)
	if prevStatus != status {
		log = log.WithValues("prevStatus", prevStatus)
	}
	if wl.Spec.Admission != nil {
		log = log.WithValues("clusterQueue", wl.Spec.Admission.ClusterQueue)
	}
	if oldWl.Spec.Admission != nil && (wl.Spec.Admission == nil || wl.Spec.Admission.ClusterQueue != oldWl.Spec.Admission.ClusterQueue) {
		log = log.WithValues("prevClusterQueue", oldWl.Spec.Admission.ClusterQueue)
	}
	log.V(2).Info("QueuedWorkload update event")

	switch {
	case status == finished:
		if err := r.cache.DeleteWorkload(oldWl); err != nil && prevStatus == admitted {
			log.Error(err, "Failed to delete workload from cache")
		}
		r.queues.DeleteWorkload(oldWl)

	case prevStatus == pending && status == admitted:
		if !r.queues.UpdateWorkload(oldWl, wl.DeepCopy()) {
			_ = r.cache.UpdateWorkloadStatus(ctx, wl.DeepCopy(), kueue.QueuedWorkloadPending, queueDoesntExist, "Updating")
			log.V(2).Info(queueDoesntExist)
		}

	case prevStatus == pending && status == admitted:
		r.queues.DeleteWorkload(oldWl)
		if !r.cache.AddOrUpdateWorkload(wl.DeepCopy()) {
			log.V(2).Info(clusterQueueDoesntExist)
			_ = r.cache.UpdateWorkloadStatus(ctx, wl.DeepCopy(), kueue.QueuedWorkloadPending, clusterQueueDoesntExist, "Updating")
		}

	case prevStatus == admitted && status == pending:
		if err := r.cache.DeleteWorkload(oldWl); err != nil {
			log.Error(err, "Failed to delete workload from cache")
		}
		if !r.queues.AddOrUpdateWorkload(wl.DeepCopy()) {
			_ = r.cache.UpdateWorkloadStatus(ctx, wl.DeepCopy(), kueue.QueuedWorkloadPending, queueDoesntExist, "Updating")
			log.V(2).Info(queueDoesntExist)
		}

	default:
		// Workload update in the cache is handled here; however, some fields are immutable
		// and are not supposed to actually change anything.
		if err := r.cache.UpdateWorkload(oldWl, wl.DeepCopy()); err != nil {
			log.Error(err, "Failed to update workload in cache")
		}
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
	if workloadFinished(w) {
		return finished
	}
	if w.Spec.Admission != nil {
		return admitted
	}
	return pending
}

func workloadFinished(w *kueue.QueuedWorkload) bool {
	for _, c := range w.Status.Conditions {
		if c.Type == kueue.QueuedWorkloadFinished {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
