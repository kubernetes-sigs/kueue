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
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/queue"
)

type ClusterQueueUpdateWatcher interface {
	NotifyClusterQueueUpdate(*kueue.ClusterQueue, *kueue.ClusterQueue)
}

// ClusterQueueReconciler reconciles a ClusterQueue object
type ClusterQueueReconciler struct {
	client     client.Client
	log        logr.Logger
	qManager   *queue.Manager
	cache      *cache.Cache
	wlUpdateCh chan event.GenericEvent
	rfUpdateCh chan event.GenericEvent
	watchers   []ClusterQueueUpdateWatcher
}

func NewClusterQueueReconciler(
	client client.Client,
	qMgr *queue.Manager,
	cache *cache.Cache,
	watchers ...ClusterQueueUpdateWatcher,
) *ClusterQueueReconciler {
	return &ClusterQueueReconciler{
		client:     client,
		log:        ctrl.Log.WithName("cluster-queue-reconciler"),
		qManager:   qMgr,
		cache:      cache,
		wlUpdateCh: make(chan event.GenericEvent, updateChBuffer),
		rfUpdateCh: make(chan event.GenericEvent, updateChBuffer),
		watchers:   watchers,
	}
}

//+kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=clusterqueues,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=clusterqueues/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=clusterqueues/finalizers,verbs=update

func (r *ClusterQueueReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var cqObj kueue.ClusterQueue
	if err := r.client.Get(ctx, req.NamespacedName, &cqObj); err != nil {
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log := ctrl.LoggerFrom(ctx).WithValues("clusterQueue", klog.KObj(&cqObj))
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(2).Info("Reconciling ClusterQueue")

	if cqObj.ObjectMeta.DeletionTimestamp.IsZero() {
		// Although we'll add the finalizer via webhook mutation now, this is still useful
		// as a fallback.
		if !controllerutil.ContainsFinalizer(&cqObj, kueue.ResourceInUseFinalizerName) {
			controllerutil.AddFinalizer(&cqObj, kueue.ResourceInUseFinalizerName)
			if err := r.client.Update(ctx, &cqObj); err != nil {
				return ctrl.Result{}, client.IgnoreNotFound(err)
			}
		}
	} else {
		if !r.cache.ClusterQueueTerminating(cqObj.Name) {
			r.cache.TerminateClusterQueue(cqObj.Name)
		}

		if controllerutil.ContainsFinalizer(&cqObj, kueue.ResourceInUseFinalizerName) {
			// The clusterQueue is being deleted, remove the finalizer only if
			// there are no active admitted workloads.
			if r.cache.ClusterQueueEmpty(cqObj.Name) {
				controllerutil.RemoveFinalizer(&cqObj, kueue.ResourceInUseFinalizerName)
				if err := r.client.Update(ctx, &cqObj); err != nil {
					return ctrl.Result{}, client.IgnoreNotFound(err)
				}
			}
			return ctrl.Result{}, nil
		}
	}

	newCQObj := cqObj.DeepCopy()
	if r.cache.ClusterQueueActive(newCQObj.Name) {
		msg := "Can admit new workloads"
		if err := r.updateCqStatusIfChanged(ctx, newCQObj, metav1.ConditionTrue, "Ready", msg); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	} else if r.cache.ClusterQueueTerminating(newCQObj.Name) {
		msg := "Can't admit new workloads; clusterQueue is terminating"
		if err := r.updateCqStatusIfChanged(ctx, newCQObj, metav1.ConditionFalse, "Terminating", msg); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	} else {
		msg := "Can't admit new workloads; some flavors are not found"
		if err := r.updateCqStatusIfChanged(ctx, newCQObj, metav1.ConditionFalse, "FlavorNotFound", msg); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	}

	return ctrl.Result{}, nil
}

func (r *ClusterQueueReconciler) NotifyWorkloadUpdate(oldWl, newWl *kueue.Workload) {
	if oldWl != nil {
		r.wlUpdateCh <- event.GenericEvent{Object: oldWl}
	}
	if newWl != nil {
		r.wlUpdateCh <- event.GenericEvent{Object: newWl}
	}
}

func (r *ClusterQueueReconciler) notifyWatchers(oldCQ, newCQ *kueue.ClusterQueue) {
	for _, w := range r.watchers {
		w.NotifyClusterQueueUpdate(oldCQ, newCQ)
	}
}

func (r *ClusterQueueReconciler) NotifyResourceFlavorUpdate(rf *kueue.ResourceFlavor) {
	r.rfUpdateCh <- event.GenericEvent{Object: rf}
}

// Event handlers return true to signal the controller to reconcile the
// ClusterQueue associated with the event.

func (r *ClusterQueueReconciler) Create(e event.CreateEvent) bool {
	cq, match := e.Object.(*kueue.ClusterQueue)
	if !match {
		// No need to interact with the cache for other objects.
		return true
	}
	defer r.notifyWatchers(nil, cq)

	log := r.log.WithValues("clusterQueue", klog.KObj(cq))
	log.V(2).Info("ClusterQueue create event")
	ctx := ctrl.LoggerInto(context.Background(), log)
	if err := r.cache.AddClusterQueue(ctx, cq); err != nil {
		log.Error(err, "Failed to add clusterQueue to cache")
	}

	if err := r.qManager.AddClusterQueue(ctx, cq); err != nil {
		log.Error(err, "Failed to add clusterQueue to queue manager")
	}
	return true
}

func (r *ClusterQueueReconciler) Delete(e event.DeleteEvent) bool {
	cq, match := e.Object.(*kueue.ClusterQueue)
	if !match {
		// No need to interact with the cache for other objects.
		return true
	}
	defer r.notifyWatchers(cq, nil)

	r.log.V(2).Info("ClusterQueue delete event", "clusterQueue", klog.KObj(cq))
	r.cache.DeleteClusterQueue(cq)
	r.qManager.DeleteClusterQueue(cq)
	return true
}

func (r *ClusterQueueReconciler) Update(e event.UpdateEvent) bool {
	oldCq, match := e.ObjectOld.(*kueue.ClusterQueue)
	if !match {
		// No need to interact with the cache for other objects.
		return true
	}
	newCq, match := e.ObjectNew.(*kueue.ClusterQueue)
	if !match {
		// No need to interact with the cache for other objects.
		return true
	}

	log := r.log.WithValues("clusterQueue", klog.KObj(newCq))
	log.V(2).Info("ClusterQueue update event")

	if newCq.DeletionTimestamp != nil {
		return true
	}
	defer r.notifyWatchers(oldCq, newCq)

	if err := r.cache.UpdateClusterQueue(newCq); err != nil {
		log.Error(err, "Failed to update clusterQueue in cache")
	}
	if err := r.qManager.UpdateClusterQueue(context.Background(), newCq); err != nil {
		log.Error(err, "Failed to update clusterQueue in queue manager")
	}
	return true
}

func (r *ClusterQueueReconciler) Generic(e event.GenericEvent) bool {
	r.log.V(2).Info("Got generic event", "obj", klog.KObj(e.Object), "kind", e.Object.GetObjectKind().GroupVersionKind())
	return true
}

// cqWorkloadHandler signals the controller to reconcile the ClusterQueue
// associated to the workload in the event.
// Since the events come from a channel Source, only the Generic handler will
// receive events.
type cqWorkloadHandler struct {
	qManager *queue.Manager
}

func (h *cqWorkloadHandler) Create(event.CreateEvent, workqueue.RateLimitingInterface) {
}

func (h *cqWorkloadHandler) Update(event.UpdateEvent, workqueue.RateLimitingInterface) {
}

func (h *cqWorkloadHandler) Delete(event.DeleteEvent, workqueue.RateLimitingInterface) {
}

func (h *cqWorkloadHandler) Generic(e event.GenericEvent, q workqueue.RateLimitingInterface) {
	w := e.Object.(*kueue.Workload)
	req := h.requestForWorkloadClusterQueue(w)
	if req != nil {
		q.AddAfter(*req, constants.UpdatesBatchPeriod)
	}
}

func (h *cqWorkloadHandler) requestForWorkloadClusterQueue(w *kueue.Workload) *reconcile.Request {
	var name string
	if w.Status.Admission != nil {
		name = string(w.Status.Admission.ClusterQueue)
	} else {
		var ok bool
		name, ok = h.qManager.ClusterQueueForWorkload(w)
		if !ok {
			return nil
		}
	}
	return &reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: name,
		},
	}
}

// cqNamespaceHandler handles namespace update events.
type cqNamespaceHandler struct {
	qManager *queue.Manager
	cache    *cache.Cache
}

func (h *cqNamespaceHandler) Create(e event.CreateEvent, q workqueue.RateLimitingInterface) {
}

func (h *cqNamespaceHandler) Update(e event.UpdateEvent, q workqueue.RateLimitingInterface) {
	oldNs := e.ObjectOld.(*corev1.Namespace)
	oldMatchingCqs := h.cache.MatchingClusterQueues(oldNs.Labels)
	newNs := e.ObjectNew.(*corev1.Namespace)
	newMatchingCqs := h.cache.MatchingClusterQueues(newNs.Labels)
	cqs := sets.New[string]()
	for cq := range newMatchingCqs {
		if !oldMatchingCqs.Has(cq) {
			cqs.Insert(cq)
		}
	}
	h.qManager.QueueInadmissibleWorkloads(context.Background(), cqs)
}

func (h *cqNamespaceHandler) Delete(event.DeleteEvent, workqueue.RateLimitingInterface) {
}

func (h *cqNamespaceHandler) Generic(event.GenericEvent, workqueue.RateLimitingInterface) {
}

type cqResourceFlavorHandler struct {
	cache *cache.Cache
}

func (h *cqResourceFlavorHandler) Create(event.CreateEvent, workqueue.RateLimitingInterface) {
}

func (h *cqResourceFlavorHandler) Update(event.UpdateEvent, workqueue.RateLimitingInterface) {
}

func (h *cqResourceFlavorHandler) Delete(event.DeleteEvent, workqueue.RateLimitingInterface) {
}

func (h *cqResourceFlavorHandler) Generic(e event.GenericEvent, q workqueue.RateLimitingInterface) {
	rf, ok := e.Object.(*kueue.ResourceFlavor)
	if !ok {
		return
	}

	if cqs := h.cache.ClusterQueuesUsingFlavor(rf.Name); len(cqs) != 0 {
		for _, cq := range cqs {
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: cq,
				}}
			q.Add(req)
		}
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterQueueReconciler) SetupWithManager(mgr ctrl.Manager) error {
	wHandler := cqWorkloadHandler{
		qManager: r.qManager,
	}
	nsHandler := cqNamespaceHandler{
		qManager: r.qManager,
		cache:    r.cache,
	}
	rfHandler := cqResourceFlavorHandler{
		cache: r.cache,
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&kueue.ClusterQueue{}).
		Watches(&source.Kind{Type: &corev1.Namespace{}}, &nsHandler).
		Watches(&source.Channel{Source: r.wlUpdateCh}, &wHandler).
		Watches(&source.Channel{Source: r.rfUpdateCh}, &rfHandler).
		WithEventFilter(r).
		Complete(r)
}

func (r *ClusterQueueReconciler) updateCqStatusIfChanged(
	ctx context.Context,
	cq *kueue.ClusterQueue,
	conditionStatus metav1.ConditionStatus,
	reason, msg string,
) error {
	oldStatus := cq.Status.DeepCopy()
	pendingWorkloads := r.qManager.Pending(cq)
	usage, workloads, err := r.cache.Usage(cq)
	if err != nil {
		r.log.Error(err, "Failed getting usage from cache")
		// This is likely because the cluster queue was recently removed,
		// but we didn't process that event yet.
		return err
	}
	cq.Status.FlavorsUsage = usage
	cq.Status.AdmittedWorkloads = int32(workloads)
	cq.Status.PendingWorkloads = int32(pendingWorkloads)
	meta.SetStatusCondition(&cq.Status.Conditions, metav1.Condition{
		Type:    kueue.ClusterQueueActive,
		Status:  conditionStatus,
		Reason:  reason,
		Message: msg,
	})
	if !equality.Semantic.DeepEqual(cq.Status, oldStatus) {
		return r.client.Status().Update(ctx, cq)
	}
	return nil
}
