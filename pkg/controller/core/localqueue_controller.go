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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/queue"
)

const (
	queueIsInactiveMsg = "Can't submit new workloads to clusterQueue"
)

// LocalQueueReconciler reconciles a LocalQueue object
type LocalQueueReconciler struct {
	client     client.Client
	log        logr.Logger
	queues     *queue.Manager
	cache      *cache.Cache
	wlUpdateCh chan event.GenericEvent
}

func NewLocalQueueReconciler(client client.Client, queues *queue.Manager, cache *cache.Cache) *LocalQueueReconciler {
	return &LocalQueueReconciler{
		log:        ctrl.Log.WithName("localqueue-reconciler"),
		queues:     queues,
		cache:      cache,
		client:     client,
		wlUpdateCh: make(chan event.GenericEvent, updateChBuffer),
	}
}

func (r *LocalQueueReconciler) NotifyWorkloadUpdate(oldWl, newWl *kueue.Workload) {
	if oldWl != nil {
		r.wlUpdateCh <- event.GenericEvent{Object: oldWl}
		if newWl != nil && oldWl.Spec.QueueName != newWl.Spec.QueueName {
			r.wlUpdateCh <- event.GenericEvent{Object: newWl}
		}
		return
	}
	if newWl != nil {
		r.wlUpdateCh <- event.GenericEvent{Object: newWl}
	}
}

//+kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=localqueues,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=localqueues/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=localqueues/finalizers,verbs=update

func (r *LocalQueueReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var queueObj kueue.LocalQueue
	if err := r.client.Get(ctx, req.NamespacedName, &queueObj); err != nil {
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log := ctrl.LoggerFrom(ctx).WithValues("localQueue", klog.KObj(&queueObj))
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(2).Info("Reconciling LocalQueue")

	var cq kueue.ClusterQueue
	err := r.client.Get(ctx, client.ObjectKey{Name: string(queueObj.Spec.ClusterQueue)}, &cq)
	if err != nil {
		if apierrors.IsNotFound(err) {
			err = r.UpdateStatusIfChanged(ctx, &queueObj, metav1.ConditionFalse, "ClusterQueueDoesNotExist", queueIsInactiveMsg)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if meta.IsStatusConditionTrue(cq.Status.Conditions, kueue.ClusterQueueActive) {
		err = r.UpdateStatusIfChanged(ctx, &queueObj, metav1.ConditionTrue, "Ready", "Can submit new workloads to clusterQueue")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	err = r.UpdateStatusIfChanged(ctx, &queueObj, metav1.ConditionFalse, "ClusterQueueIsInactive", queueIsInactiveMsg)
	return ctrl.Result{}, client.IgnoreNotFound(err)
}

func (r *LocalQueueReconciler) Create(e event.CreateEvent) bool {
	q, match := e.Object.(*kueue.LocalQueue)
	if !match {
		// No need to interact with the queue manager for other objects.
		return true
	}
	log := r.log.WithValues("localQueue", klog.KObj(q))
	log.V(2).Info("LocalQueue create event")
	ctx := logr.NewContext(context.Background(), log)
	if err := r.queues.AddLocalQueue(ctx, q); err != nil {
		log.Error(err, "Failed to add localQueue to the queueing system")
	}
	if err := r.cache.AddLocalQueue(q); err != nil {
		log.Error(err, "Failed to add localQueue to the cache")
	}
	return true
}

func (r *LocalQueueReconciler) Delete(e event.DeleteEvent) bool {
	q, match := e.Object.(*kueue.LocalQueue)
	if !match {
		// No need to interact with the queue manager for other objects.
		return true
	}
	r.log.V(2).Info("LocalQueue delete event", "localQueue", klog.KObj(q))
	r.queues.DeleteLocalQueue(q)
	r.cache.DeleteLocalQueue(q)
	return true
}

func (r *LocalQueueReconciler) Update(e event.UpdateEvent) bool {
	q, match := e.ObjectNew.(*kueue.LocalQueue)
	if !match {
		// No need to interact with the queue manager for other objects.
		return true
	}
	log := r.log.WithValues("localQueue", klog.KObj(q))
	log.V(2).Info("Queue update event")
	if err := r.queues.UpdateLocalQueue(q); err != nil {
		log.Error(err, "Failed to update queue in the queueing system")
	}
	oldQ := e.ObjectOld.(*kueue.LocalQueue)
	if err := r.cache.UpdateLocalQueue(oldQ, q); err != nil {
		log.Error(err, "Failed to update localQueue in the cache")
	}
	return true
}

func (r *LocalQueueReconciler) Generic(e event.GenericEvent) bool {
	r.log.V(3).Info("Got Workload event", "workload", klog.KObj(e.Object))
	return true
}

// qWorkloadHandler signals the controller to reconcile the Queue associated
// to the workload in the event.
// Since the events come from a channel Source, only the Generic handler will
// receive events.
type qWorkloadHandler struct{}

func (h *qWorkloadHandler) Create(event.CreateEvent, workqueue.RateLimitingInterface) {
}

func (h *qWorkloadHandler) Update(event.UpdateEvent, workqueue.RateLimitingInterface) {
}

func (h *qWorkloadHandler) Delete(event.DeleteEvent, workqueue.RateLimitingInterface) {
}

func (h *qWorkloadHandler) Generic(e event.GenericEvent, q workqueue.RateLimitingInterface) {
	w := e.Object.(*kueue.Workload)
	if w.Name == "" {
		return
	}
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      w.Spec.QueueName,
			Namespace: w.Namespace,
		},
	}
	q.AddAfter(req, constants.UpdatesBatchPeriod)
}

// qCQHandler signals the controller to reconcile the Queue associated
// to the workload in the event.
type qCQHandler struct {
	client client.Client
}

func (h *qCQHandler) Create(e event.CreateEvent, wq workqueue.RateLimitingInterface) {
	cq, ok := e.Object.(*kueue.ClusterQueue)
	if !ok {
		return
	}
	h.addLocalQueueToWorkQueue(cq, wq)
}

func (h *qCQHandler) Update(e event.UpdateEvent, wq workqueue.RateLimitingInterface) {
	newCq, ok := e.ObjectNew.(*kueue.ClusterQueue)
	if !ok {
		return
	}
	oldCq, ok := e.ObjectOld.(*kueue.ClusterQueue)
	if !ok {
		return
	}
	// Iff .status.conditions of the clusterQueue is updated,
	// this handler sends all queues related to the clusterQueue to workqueue.
	if equality.Semantic.DeepEqual(oldCq.Status.Conditions, newCq.Status.Conditions) {
		return
	}
	h.addLocalQueueToWorkQueue(newCq, wq)
}

func (h *qCQHandler) Delete(e event.DeleteEvent, wq workqueue.RateLimitingInterface) {
	cq, ok := e.Object.(*kueue.ClusterQueue)
	if !ok {
		return
	}
	h.addLocalQueueToWorkQueue(cq, wq)
}

func (h *qCQHandler) Generic(event.GenericEvent, workqueue.RateLimitingInterface) {
}

func (h *qCQHandler) addLocalQueueToWorkQueue(cq *kueue.ClusterQueue, wq workqueue.RateLimitingInterface) {
	ctx := context.TODO()
	log := ctrl.LoggerFrom(ctx).WithValues("clusterQueue", klog.KObj(cq))
	ctx = ctrl.LoggerInto(ctx, log)

	var queues kueue.LocalQueueList
	err := h.client.List(ctx, &queues, client.MatchingFields{indexer.QueueClusterQueueKey: cq.Name})
	if err != nil {
		log.Error(err, "Could not list queues that match the clusterQueue")
		return
	}
	for _, q := range queues.Items {
		wq.Add(reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&q)})
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *LocalQueueReconciler) SetupWithManager(mgr ctrl.Manager) error {
	queueCQHandler := qCQHandler{
		client: r.client,
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&kueue.LocalQueue{}).
		Watches(&source.Channel{Source: r.wlUpdateCh}, &qWorkloadHandler{}).
		Watches(&source.Kind{Type: &kueue.ClusterQueue{}}, &queueCQHandler).
		WithEventFilter(r).
		Complete(r)
}

func (r *LocalQueueReconciler) UpdateStatusIfChanged(
	ctx context.Context,
	queue *kueue.LocalQueue,
	conditionStatus metav1.ConditionStatus,
	reason, msg string,
) error {
	oldStatus := queue.Status.DeepCopy()
	pendingWls, err := r.queues.PendingWorkloads(queue)
	if err != nil {
		r.log.Error(err, "Failed to retrieve localQueue status")
		return err
	}
	queue.Status.PendingWorkloads = pendingWls
	queue.Status.AdmittedWorkloads = r.cache.AdmittedWorkloadsInLocalQueue(queue)
	if len(conditionStatus) != 0 && len(reason) != 0 && len(msg) != 0 {
		meta.SetStatusCondition(&queue.Status.Conditions, metav1.Condition{
			Type:    kueue.LocalQueueActive,
			Status:  conditionStatus,
			Reason:  reason,
			Message: msg,
		})
	}
	if !equality.Semantic.DeepEqual(oldStatus, queue.Status) {
		return r.client.Status().Update(ctx, queue)
	}
	return nil
}
