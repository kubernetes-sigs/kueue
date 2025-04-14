/*
Copyright The Kubernetes Authors.

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
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/util/resource"
)

const (
	localQueueIsInactiveMsg   = "LocalQueue is stopped"
	clusterQueueIsInactiveMsg = "Can't submit new workloads to clusterQueue"
	failedUpdateLqStatusMsg   = "Failed to retrieve localQueue status"
)

const (
	StoppedReason                = "Stopped"
	clusterQueueIsInactiveReason = "ClusterQueueIsInactive"
)

// LocalQueueReconciler reconciles a LocalQueue object
type LocalQueueReconciler struct {
	client     client.Client
	log        logr.Logger
	queues     *queue.Manager
	cache      *cache.Cache
	wlUpdateCh chan event.GenericEvent
}

var _ reconcile.Reconciler = (*LocalQueueReconciler)(nil)
var _ predicate.TypedPredicate[*kueue.LocalQueue] = (*LocalQueueReconciler)(nil)

func NewLocalQueueReconciler(
	client client.Client,
	queues *queue.Manager,
	cache *cache.Cache,
) *LocalQueueReconciler {
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

// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=localqueues,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=localqueues/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=localqueues/finalizers,verbs=update

func (r *LocalQueueReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var queueObj kueue.LocalQueue
	if err := r.client.Get(ctx, req.NamespacedName, &queueObj); err != nil {
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile LocalQueue")

	if ptr.Deref(queueObj.Spec.StopPolicy, kueue.None) != kueue.None {
		err := r.UpdateStatusIfChanged(ctx, &queueObj, metav1.ConditionFalse, StoppedReason, localQueueIsInactiveMsg)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var cq kueue.ClusterQueue
	err := r.client.Get(ctx, client.ObjectKey{Name: string(queueObj.Spec.ClusterQueue)}, &cq)
	if err != nil {
		if apierrors.IsNotFound(err) {
			err = r.UpdateStatusIfChanged(ctx, &queueObj, metav1.ConditionFalse, "ClusterQueueDoesNotExist", clusterQueueIsInactiveMsg)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if meta.IsStatusConditionTrue(cq.Status.Conditions, kueue.ClusterQueueActive) {
		err = r.UpdateStatusIfChanged(ctx, &queueObj, metav1.ConditionTrue, "Ready", "Can submit new workloads to localQueue")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	err = r.UpdateStatusIfChanged(ctx, &queueObj, metav1.ConditionFalse, clusterQueueIsInactiveReason, clusterQueueIsInactiveMsg)
	return ctrl.Result{}, client.IgnoreNotFound(err)
}

func (r *LocalQueueReconciler) Create(e event.TypedCreateEvent[*kueue.LocalQueue]) bool {
	log := r.log.WithValues("localQueue", klog.KObj(e.Object))
	log.V(2).Info("LocalQueue create event")

	if ptr.Deref(e.Object.Spec.StopPolicy, kueue.None) == kueue.None {
		ctx := logr.NewContext(context.Background(), log)
		if err := r.queues.AddLocalQueue(ctx, e.Object); err != nil {
			log.Error(err, "Failed to add localQueue to the queueing system")
		}
	}

	if err := r.cache.AddLocalQueue(e.Object); err != nil {
		log.Error(err, "Failed to add localQueue to the cache")
	}

	if features.Enabled(features.LocalQueueMetrics) {
		recordLocalQueueUsageMetrics(e.Object)
	}

	return true
}

func (r *LocalQueueReconciler) Delete(e event.TypedDeleteEvent[*kueue.LocalQueue]) bool {
	if features.Enabled(features.LocalQueueMetrics) {
		metrics.ClearLocalQueueResourceMetrics(localQueueReferenceFromLocalQueue(e.Object))
	}

	r.log.V(2).Info("LocalQueue delete event", "localQueue", klog.KObj(e.Object))
	r.queues.DeleteLocalQueue(e.Object)
	r.cache.DeleteLocalQueue(e.Object)
	return true
}

func (r *LocalQueueReconciler) Update(e event.TypedUpdateEvent[*kueue.LocalQueue]) bool {
	log := r.log.WithValues("localQueue", klog.KObj(e.ObjectNew))
	log.V(2).Info("Queue update event")

	if features.Enabled(features.LocalQueueMetrics) {
		updateLocalQueueResourceMetrics(e.ObjectNew)
	}

	oldStopPolicy := ptr.Deref(e.ObjectOld.Spec.StopPolicy, kueue.None)
	newStopPolicy := ptr.Deref(e.ObjectNew.Spec.StopPolicy, kueue.None)

	if newStopPolicy == oldStopPolicy {
		if newStopPolicy == kueue.None {
			if err := r.queues.UpdateLocalQueue(e.ObjectNew); err != nil {
				log.Error(err, "Failed to update queue in the queueing system")
			}
		}
		if err := r.cache.UpdateLocalQueue(e.ObjectOld, e.ObjectNew); err != nil {
			log.Error(err, "Failed to update localQueue in the cache")
		}
		return true
	}

	if newStopPolicy == kueue.None {
		ctx := logr.NewContext(context.Background(), log)
		if err := r.queues.AddLocalQueue(ctx, e.ObjectNew); err != nil {
			log.Error(err, "Failed to add localQueue to the queueing system")
		}
		return true
	}

	r.queues.DeleteLocalQueue(e.ObjectOld)

	return true
}

func localQueueReferenceFromLocalQueue(lq *kueue.LocalQueue) metrics.LocalQueueReference {
	return metrics.LocalQueueReference{
		Name:      lq.Name,
		Namespace: lq.Namespace,
	}
}

func recordLocalQueueUsageMetrics(queue *kueue.LocalQueue) {
	for _, flavor := range queue.Status.FlavorUsage {
		for _, r := range flavor.Resources {
			metrics.ReportLocalQueueResourceUsage(localQueueReferenceFromLocalQueue(queue), string(flavor.Name), string(r.Name), resource.QuantityToFloat(&r.Total))
		}
	}
	for _, flavor := range queue.Status.FlavorsReservation {
		for _, r := range flavor.Resources {
			metrics.ReportLocalQueueResourceReservations(localQueueReferenceFromLocalQueue(queue), string(flavor.Name), string(r.Name), resource.QuantityToFloat(&r.Total))
		}
	}
}

func updateLocalQueueResourceMetrics(queue *kueue.LocalQueue) {
	metrics.ClearLocalQueueResourceMetrics(localQueueReferenceFromLocalQueue(queue))
	recordLocalQueueUsageMetrics(queue)
}

func (r *LocalQueueReconciler) Generic(e event.TypedGenericEvent[*kueue.LocalQueue]) bool {
	r.log.V(2).Info("LocalQueue generic event", "localQueue", klog.KObj(e.Object))
	return true
}

// qWorkloadHandler signals the controller to reconcile the Queue associated
// to the workload in the event.
// Since the events come from a channel Source, only the Generic handler will
// receive events.
type qWorkloadHandler struct{}

func (h *qWorkloadHandler) Create(context.Context, event.CreateEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *qWorkloadHandler) Update(context.Context, event.UpdateEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *qWorkloadHandler) Delete(context.Context, event.DeleteEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *qWorkloadHandler) Generic(_ context.Context, e event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
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

func (h *qCQHandler) Create(ctx context.Context, e event.CreateEvent, wq workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	cq, ok := e.Object.(*kueue.ClusterQueue)
	if !ok {
		return
	}
	h.addLocalQueueToWorkQueue(ctx, cq, wq)
}

func (h *qCQHandler) Update(ctx context.Context, e event.UpdateEvent, wq workqueue.TypedRateLimitingInterface[reconcile.Request]) {
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
	h.addLocalQueueToWorkQueue(ctx, newCq, wq)
}

func (h *qCQHandler) Delete(ctx context.Context, e event.DeleteEvent, wq workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	cq, ok := e.Object.(*kueue.ClusterQueue)
	if !ok {
		return
	}
	h.addLocalQueueToWorkQueue(ctx, cq, wq)
}

func (h *qCQHandler) Generic(context.Context, event.GenericEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *qCQHandler) addLocalQueueToWorkQueue(ctx context.Context, cq *kueue.ClusterQueue, wq workqueue.TypedRateLimitingInterface[reconcile.Request]) {
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
func (r *LocalQueueReconciler) SetupWithManager(mgr ctrl.Manager, cfg *config.Configuration) error {
	queueCQHandler := qCQHandler{
		client: r.client,
	}
	return builder.TypedControllerManagedBy[reconcile.Request](mgr).
		Named("localqueue_controller").
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&kueue.LocalQueue{},
			&handler.TypedEnqueueRequestForObject[*kueue.LocalQueue]{},
			r,
		)).
		WithOptions(controller.Options{NeedLeaderElection: ptr.To(false)}).
		WatchesRawSource(source.Channel(r.wlUpdateCh, &qWorkloadHandler{})).
		Watches(&kueue.ClusterQueue{}, &queueCQHandler).
		Complete(WithLeadingManager(mgr, r, &kueue.LocalQueue{}, cfg))
}

func (r *LocalQueueReconciler) UpdateStatusIfChanged(
	ctx context.Context,
	queue *kueue.LocalQueue,
	conditionStatus metav1.ConditionStatus,
	reason, msg string,
) error {
	oldStatus := queue.Status.DeepCopy()
	var (
		pendingWls int32
		err        error
	)
	if ptr.Deref(queue.Spec.StopPolicy, kueue.None) == kueue.None {
		pendingWls, err = r.queues.PendingWorkloads(queue)
		if err != nil {
			r.log.Error(err, failedUpdateLqStatusMsg)
			return err
		}
	}
	stats, err := r.cache.LocalQueueUsage(queue)
	if err != nil {
		r.log.Error(err, failedUpdateLqStatusMsg)
		return err
	}
	queue.Status.PendingWorkloads = pendingWls
	queue.Status.ReservingWorkloads = int32(stats.ReservingWorkloads)
	queue.Status.AdmittedWorkloads = int32(stats.AdmittedWorkloads)
	queue.Status.FlavorsReservation = stats.ReservedResources
	queue.Status.FlavorUsage = stats.AdmittedResources
	queue.Status.Flavors = stats.Flavors
	if len(conditionStatus) != 0 && len(reason) != 0 && len(msg) != 0 {
		meta.SetStatusCondition(&queue.Status.Conditions, metav1.Condition{
			Type:               kueue.LocalQueueActive,
			Status:             conditionStatus,
			Reason:             reason,
			Message:            msg,
			ObservedGeneration: queue.Generation,
		})
		if features.Enabled(features.LocalQueueMetrics) {
			metrics.ReportLocalQueueStatus(metrics.LocalQueueReference{
				Name:      queue.Name,
				Namespace: queue.Namespace,
			}, conditionStatus)
		}
	}
	if !equality.Semantic.DeepEqual(oldStatus, queue.Status) {
		return r.client.Status().Update(ctx, queue)
	}
	return nil
}
