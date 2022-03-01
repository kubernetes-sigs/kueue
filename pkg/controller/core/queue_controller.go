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
	"sigs.k8s.io/kueue/pkg/queue"
)

// QueueReconciler reconciles a Queue object
type QueueReconciler struct {
	client client.Client
	log    logr.Logger
	queues *queue.Manager
}

func NewQueueReconciler(client client.Client, queues *queue.Manager) *QueueReconciler {
	return &QueueReconciler{
		log:    ctrl.Log.WithName("queue-reconciler"),
		queues: queues,
		client: client,
	}
}

//+kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=queues,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=queues/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=queues/finalizers,verbs=update

func (r *QueueReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var queueObj kueue.Queue
	if err := r.client.Get(ctx, req.NamespacedName, &queueObj); err != nil {
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log := ctrl.LoggerFrom(ctx).WithValues("queue", klog.KObj(&queueObj))
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(2).Info("Reconciling Queue")

	pendingJobs, err := r.queues.Status(&queueObj)
	if err != nil {
		r.log.Error(err, "Failed to retrieve queue status")
		return ctrl.Result{}, err
	}

	oldStatus := queueObj.Status
	queueObj.Status.PendingWorkloads = int32(pendingJobs)
	if !equality.Semantic.DeepEqual(oldStatus, queueObj.Status) {
		err := r.client.Status().Update(ctx, &queueObj)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return ctrl.Result{}, nil
}

func (r *QueueReconciler) Create(e event.CreateEvent) bool {
	q, match := e.Object.(*kueue.Queue)
	if !match {
		// No need to interact with the queue manager for other objects.
		return true
	}
	log := r.log.WithValues("queue", klog.KObj(q))
	log.V(2).Info("Queue create event")
	ctx := logr.NewContext(context.Background(), log)
	if err := r.queues.AddQueue(ctx, q); err != nil {
		log.Error(err, "Failed to add queue to system")
	}
	return true
}

func (r *QueueReconciler) Delete(e event.DeleteEvent) bool {
	q, match := e.Object.(*kueue.Queue)
	if !match {
		// No need to interact with the queue manager for other objects.
		return true
	}
	r.log.V(2).Info("Queue delete event", "queue", klog.KObj(q))
	r.queues.DeleteQueue(q)
	return true
}

func (r *QueueReconciler) Update(e event.UpdateEvent) bool {
	q, match := e.ObjectNew.(*kueue.Queue)
	if !match {
		// No need to interact with the queue manager for other objects.
		return true
	}
	log := r.log.WithValues("queue", klog.KObj(q))
	log.V(2).Info("Queue update event")
	if err := r.queues.UpdateQueue(q); err != nil {
		log.Error(err, "Failed to update queue in system")
	}
	return true
}

func (r *QueueReconciler) Generic(e event.GenericEvent) bool {
	r.log.V(3).Info("Ignore generic event", "obj", klog.KObj(e.Object), "kind", e.Object.GetObjectKind().GroupVersionKind())
	return true
}

type queuedWorkloadHandler struct{}

func (h *queuedWorkloadHandler) Create(e event.CreateEvent, q workqueue.RateLimitingInterface) {
	qw := e.Object.(*kueue.QueuedWorkload)
	if qw.Spec.AssignedCapacity != "" {
		q.Add(requestForQueueStatus(qw))
	}
}

func (h *queuedWorkloadHandler) Update(e event.UpdateEvent, q workqueue.RateLimitingInterface) {
	oldQW := e.ObjectOld.(*kueue.QueuedWorkload)
	newQW := e.ObjectOld.(*kueue.QueuedWorkload)
	q.Add(requestForQueueStatus(newQW))
	if newQW.Spec.QueueName != oldQW.Spec.QueueName {
		q.Add(requestForQueueStatus(oldQW))
	}
}

func (h *queuedWorkloadHandler) Delete(e event.DeleteEvent, q workqueue.RateLimitingInterface) {
	qw := e.Object.(*kueue.QueuedWorkload)
	if qw.Spec.AssignedCapacity == "" {
		q.Add(requestForQueueStatus(qw))
	}
}

func (h *queuedWorkloadHandler) Generic(e event.GenericEvent, q workqueue.RateLimitingInterface) {
}

func requestForQueueStatus(w *kueue.QueuedWorkload) reconcile.Request {
	return reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      w.Spec.QueueName,
			Namespace: w.Namespace,
		},
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *QueueReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kueue.Queue{}).
		Watches(&source.Kind{Type: &kueue.QueuedWorkload{}}, &queuedWorkloadHandler{}).
		WithEventFilter(r).
		Complete(r)
}
