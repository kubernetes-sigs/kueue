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
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
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
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/util/resource"
	"sigs.k8s.io/kueue/pkg/util/slices"
	"sigs.k8s.io/kueue/pkg/workload"
)

const snapshotWorkers = 5

type ClusterQueueUpdateWatcher interface {
	NotifyClusterQueueUpdate(*kueue.ClusterQueue, *kueue.ClusterQueue)
}

// ClusterQueueReconciler reconciles a ClusterQueue object
type ClusterQueueReconciler struct {
	client                               client.Client
	log                                  logr.Logger
	qManager                             *queue.Manager
	cache                                *cache.Cache
	snapshotsQueue                       workqueue.Interface
	wlUpdateCh                           chan event.GenericEvent
	rfUpdateCh                           chan event.GenericEvent
	acUpdateCh                           chan event.GenericEvent
	watchers                             []ClusterQueueUpdateWatcher
	reportResourceMetrics                bool
	queueVisibilityUpdateInterval        time.Duration
	queueVisibilityClusterQueuesMaxCount int32
}

type ClusterQueueReconcilerOptions struct {
	Watchers                             []ClusterQueueUpdateWatcher
	ReportResourceMetrics                bool
	QueueVisibilityUpdateInterval        time.Duration
	QueueVisibilityClusterQueuesMaxCount int32
}

// Option configures the reconciler.
type ClusterQueueReconcilerOption func(*ClusterQueueReconcilerOptions)

func WithWatchers(watchers ...ClusterQueueUpdateWatcher) ClusterQueueReconcilerOption {
	return func(o *ClusterQueueReconcilerOptions) {
		o.Watchers = watchers
	}
}

func WithReportResourceMetrics(report bool) ClusterQueueReconcilerOption {
	return func(o *ClusterQueueReconcilerOptions) {
		o.ReportResourceMetrics = report
	}
}

// WithQueueVisibilityUpdateInterval specifies the time interval for updates to the structure
// of the top pending workloads in the queues.
func WithQueueVisibilityUpdateInterval(interval time.Duration) ClusterQueueReconcilerOption {
	return func(o *ClusterQueueReconcilerOptions) {
		o.QueueVisibilityUpdateInterval = interval
	}
}

// WithQueueVisibilityClusterQueuesMaxCount indicates the maximal number of pending workloads exposed in the
// cluster queue status
func WithQueueVisibilityClusterQueuesMaxCount(value int32) ClusterQueueReconcilerOption {
	return func(o *ClusterQueueReconcilerOptions) {
		o.QueueVisibilityClusterQueuesMaxCount = value
	}
}

var defaultCQOptions = ClusterQueueReconcilerOptions{}

func NewClusterQueueReconciler(
	client client.Client,
	qMgr *queue.Manager,
	cache *cache.Cache,
	opts ...ClusterQueueReconcilerOption,
) *ClusterQueueReconciler {
	options := defaultCQOptions
	for _, opt := range opts {
		opt(&options)
	}
	return &ClusterQueueReconciler{
		client:                               client,
		log:                                  ctrl.Log.WithName("cluster-queue-reconciler"),
		qManager:                             qMgr,
		cache:                                cache,
		snapshotsQueue:                       workqueue.New(),
		wlUpdateCh:                           make(chan event.GenericEvent, updateChBuffer),
		rfUpdateCh:                           make(chan event.GenericEvent, updateChBuffer),
		acUpdateCh:                           make(chan event.GenericEvent, updateChBuffer),
		watchers:                             options.Watchers,
		reportResourceMetrics:                options.ReportResourceMetrics,
		queueVisibilityUpdateInterval:        options.QueueVisibilityUpdateInterval,
		queueVisibilityClusterQueuesMaxCount: options.QueueVisibilityClusterQueuesMaxCount,
	}
}

//+kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update;patch
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
	cqCondition, reason, msg := r.cache.ClusterQueueReadiness(newCQObj.Name)
	if err := r.updateCqStatusIfChanged(ctx, newCQObj, cqCondition, reason, msg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return ctrl.Result{}, nil
}

func (r *ClusterQueueReconciler) NotifyWorkloadUpdate(oldWl, newWl *kueue.Workload) {
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

func (r *ClusterQueueReconciler) notifyWatchers(oldCQ, newCQ *kueue.ClusterQueue) {
	for _, w := range r.watchers {
		w.NotifyClusterQueueUpdate(oldCQ, newCQ)
	}
}

// Updates are ignored since they have no impact on the ClusterQueue's readiness.
func (r *ClusterQueueReconciler) NotifyResourceFlavorUpdate(oldRF, newRF *kueue.ResourceFlavor) {
	// if oldRF is nil, it's a create event.
	if oldRF == nil {
		r.rfUpdateCh <- event.GenericEvent{Object: newRF}
		return
	}

	// if newRF is nil, it's a delete event.
	if newRF == nil {
		r.rfUpdateCh <- event.GenericEvent{Object: oldRF}
		return
	}
}

func (r *ClusterQueueReconciler) NotifyAdmissionCheckUpdate(oldAc, newAc *kueue.AdmissionCheck) {
	switch {
	case oldAc != nil:
		r.acUpdateCh <- event.GenericEvent{Object: oldAc}
	case newAc != nil:
		r.acUpdateCh <- event.GenericEvent{Object: newAc}
	}
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

	if r.reportResourceMetrics {
		recordResourceMetrics(cq)
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
	r.qManager.DeleteSnapshot(cq)

	metrics.ClearClusterQueueResourceMetrics(cq.Name)
	r.log.V(2).Info("Cleared resource metrics for deleted ClusterQueue.", "clusterQueue", klog.KObj(cq))

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

	if r.reportResourceMetrics {
		updateResourceMetrics(oldCq, newCq)
	}
	return true
}

func (r *ClusterQueueReconciler) Generic(e event.GenericEvent) bool {
	r.log.V(2).Info("Got generic event", "obj", klog.KObj(e.Object), "kind", e.Object.GetObjectKind().GroupVersionKind())
	return true
}

func recordResourceMetrics(cq *kueue.ClusterQueue) {
	for rgi := range cq.Spec.ResourceGroups {
		rg := &cq.Spec.ResourceGroups[rgi]
		for fqi := range rg.Flavors {
			fq := &rg.Flavors[fqi]
			for ri := range fq.Resources {
				r := &fq.Resources[ri]
				nominal := resource.QuantityToFloat(&r.NominalQuota)
				borrow := resource.QuantityToFloat(r.BorrowingLimit)
				metrics.ReportClusterQueueQuotas(cq.Spec.Cohort, cq.Name, string(fq.Name), string(r.Name), nominal, borrow)
			}
		}
	}

	for fui := range cq.Status.FlavorsUsage {
		fu := &cq.Status.FlavorsUsage[fui]
		for ri := range fu.Resources {
			r := &fu.Resources[ri]
			metrics.ReportClusterQueueResourceUsage(cq.Spec.Cohort, cq.Name, string(fu.Name), string(r.Name), resource.QuantityToFloat(&r.Total))
		}
	}
}

func updateResourceMetrics(oldCq, newCq *kueue.ClusterQueue) {
	// if the cohort changed, drop all the old metrics
	if oldCq.Spec.Cohort != newCq.Spec.Cohort {
		metrics.ClearClusterQueueResourceMetrics(oldCq.Name)
	} else {
		// selective remove
		clearOldResourceQuotas(oldCq, newCq)
	}
	recordResourceMetrics(newCq)
}

func clearOldResourceQuotas(oldCq, newCq *kueue.ClusterQueue) {
	for rgi := range oldCq.Spec.ResourceGroups {
		oldRG := &oldCq.Spec.ResourceGroups[rgi]
		newFlavors := map[kueue.ResourceFlavorReference]*kueue.FlavorQuotas{}
		if rgi < len(newCq.Spec.ResourceGroups) && len(newCq.Spec.ResourceGroups[rgi].Flavors) > 0 {
			newFlavors = slices.ToRefMap(newCq.Spec.ResourceGroups[rgi].Flavors, func(f *kueue.FlavorQuotas) kueue.ResourceFlavorReference { return f.Name })
		}

		for fi := range oldRG.Flavors {
			flavor := &oldRG.Flavors[fi]
			if newFlavor, found := newFlavors[flavor.Name]; !found || len(newFlavor.Resources) == 0 {
				metrics.ClearClusterQueueResourceQuotas(oldCq.Name, string(flavor.Name), "")
			} else {
				//check all resources
				newResources := slices.ToRefMap(newFlavor.Resources, func(r *kueue.ResourceQuota) corev1.ResourceName { return r.Name })
				for ri := range flavor.Resources {
					rname := flavor.Resources[ri].Name
					if _, found := newResources[rname]; !found {
						metrics.ClearClusterQueueResourceQuotas(oldCq.Name, string(flavor.Name), string(rname))
					}
				}
			}
		}
	}

	// usage metrics
	if len(oldCq.Status.FlavorsUsage) > 0 {
		newFlavors := map[kueue.ResourceFlavorReference]*kueue.FlavorUsage{}
		if len(newCq.Status.FlavorsUsage) > 0 {
			newFlavors = slices.ToRefMap(newCq.Status.FlavorsUsage, func(f *kueue.FlavorUsage) kueue.ResourceFlavorReference { return f.Name })
		}
		for fi := range oldCq.Status.FlavorsUsage {
			flavor := &oldCq.Status.FlavorsUsage[fi]
			if newFlavor, found := newFlavors[flavor.Name]; !found || len(newFlavor.Resources) == 0 {
				metrics.ClearClusterQueueResourceUsage(oldCq.Name, string(flavor.Name), "")
			} else {
				newResources := slices.ToRefMap(newFlavor.Resources, func(r *kueue.ResourceUsage) corev1.ResourceName { return r.Name })
				for ri := range flavor.Resources {
					rname := flavor.Resources[ri].Name
					if _, found := newResources[rname]; !found {
						metrics.ClearClusterQueueResourceUsage(oldCq.Name, string(flavor.Name), string(rname))
					}
				}
			}
		}
	}
}

// cqWorkloadHandler signals the controller to reconcile the ClusterQueue
// associated to the workload in the event.
// Since the events come from a channel Source, only the Generic handler will
// receive events.
type cqWorkloadHandler struct {
	qManager *queue.Manager
}

func (h *cqWorkloadHandler) Create(context.Context, event.CreateEvent, workqueue.RateLimitingInterface) {
}

func (h *cqWorkloadHandler) Update(context.Context, event.UpdateEvent, workqueue.RateLimitingInterface) {
}

func (h *cqWorkloadHandler) Delete(context.Context, event.DeleteEvent, workqueue.RateLimitingInterface) {
}

func (h *cqWorkloadHandler) Generic(_ context.Context, e event.GenericEvent, q workqueue.RateLimitingInterface) {
	w := e.Object.(*kueue.Workload)
	req := h.requestForWorkloadClusterQueue(w)
	if req != nil {
		q.AddAfter(*req, constants.UpdatesBatchPeriod)
	}
}

func (h *cqWorkloadHandler) requestForWorkloadClusterQueue(w *kueue.Workload) *reconcile.Request {
	var name string
	if workload.HasQuotaReservation(w) {
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

func (h *cqNamespaceHandler) Create(ctx context.Context, e event.CreateEvent, q workqueue.RateLimitingInterface) {
}

func (h *cqNamespaceHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.RateLimitingInterface) {
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
	h.qManager.QueueInadmissibleWorkloads(ctx, cqs)
}

func (h *cqNamespaceHandler) Delete(context.Context, event.DeleteEvent, workqueue.RateLimitingInterface) {
}

func (h *cqNamespaceHandler) Generic(context.Context, event.GenericEvent, workqueue.RateLimitingInterface) {
}

type cqResourceFlavorHandler struct {
	cache *cache.Cache
}

func (h *cqResourceFlavorHandler) Create(context.Context, event.CreateEvent, workqueue.RateLimitingInterface) {
}

func (h *cqResourceFlavorHandler) Update(context.Context, event.UpdateEvent, workqueue.RateLimitingInterface) {
}

func (h *cqResourceFlavorHandler) Delete(context.Context, event.DeleteEvent, workqueue.RateLimitingInterface) {
}

func (h *cqResourceFlavorHandler) Generic(_ context.Context, e event.GenericEvent, q workqueue.RateLimitingInterface) {
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

type cqAdmissionCheckHandler struct {
	cache *cache.Cache
}

func (h *cqAdmissionCheckHandler) Create(context.Context, event.CreateEvent, workqueue.RateLimitingInterface) {
}

func (h *cqAdmissionCheckHandler) Update(context.Context, event.UpdateEvent, workqueue.RateLimitingInterface) {
}

func (h *cqAdmissionCheckHandler) Delete(context.Context, event.DeleteEvent, workqueue.RateLimitingInterface) {
}

func (h *cqAdmissionCheckHandler) Generic(_ context.Context, e event.GenericEvent, q workqueue.RateLimitingInterface) {
	ac, isAc := e.Object.(*kueue.AdmissionCheck)
	if !isAc {
		return
	}

	if cqs := h.cache.ClusterQueuesUsingAdmissionCheck(ac.Name); len(cqs) != 0 {
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
	acHandler := cqAdmissionCheckHandler{
		cache: r.cache,
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&kueue.ClusterQueue{}).
		Watches(&corev1.Namespace{}, &nsHandler).
		WatchesRawSource(&source.Channel{Source: r.wlUpdateCh}, &wHandler).
		WatchesRawSource(&source.Channel{Source: r.rfUpdateCh}, &rfHandler).
		WatchesRawSource(&source.Channel{Source: r.acUpdateCh}, &acHandler).
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
	cq.Status.PendingWorkloadsStatus = r.getWorkloadsStatus(cq)
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

// Taking snapshot of cluster queue is enabled when maxcount non-zero
func (r *ClusterQueueReconciler) isVisibilityEnabled() bool {
	return features.Enabled(features.QueueVisibility) && r.queueVisibilityClusterQueuesMaxCount > 0
}

func (r *ClusterQueueReconciler) getWorkloadsStatus(cq *kueue.ClusterQueue) *kueue.ClusterQueuePendingWorkloadsStatus {
	if !r.isVisibilityEnabled() {
		return nil
	}
	if cq.Status.PendingWorkloadsStatus == nil {
		return &kueue.ClusterQueuePendingWorkloadsStatus{
			Head:           r.qManager.GetSnapshot(cq.Name),
			LastChangeTime: metav1.Time{Time: time.Now()},
		}
	}
	if time.Since(cq.Status.PendingWorkloadsStatus.LastChangeTime.Time) >= r.queueVisibilityUpdateInterval {
		pendingWorkloads := r.qManager.GetSnapshot(cq.Name)
		if !equality.Semantic.DeepEqual(cq.Status.PendingWorkloadsStatus.Head, pendingWorkloads) {
			return &kueue.ClusterQueuePendingWorkloadsStatus{
				Head:           pendingWorkloads,
				LastChangeTime: metav1.Time{Time: time.Now()},
			}
		}
	}
	return cq.Status.PendingWorkloadsStatus
}

func (r *ClusterQueueReconciler) Start(ctx context.Context) error {
	if !r.isVisibilityEnabled() {
		return nil
	}

	defer r.snapshotsQueue.ShutDown()

	for i := 0; i < snapshotWorkers; i++ {
		go wait.UntilWithContext(ctx, r.takeSnapshot, r.queueVisibilityUpdateInterval)
	}

	go wait.UntilWithContext(ctx, r.enqueueTakeSnapshot, r.queueVisibilityUpdateInterval)

	<-ctx.Done()

	return nil
}

func (r *ClusterQueueReconciler) enqueueTakeSnapshot(ctx context.Context) {
	for _, cq := range r.qManager.GetClusterQueueNames() {
		r.snapshotsQueue.Add(cq)
	}
}

func (r *ClusterQueueReconciler) takeSnapshot(ctx context.Context) {
	for r.processNextSnapshot(ctx) {
	}
}

func (r *ClusterQueueReconciler) processNextSnapshot(ctx context.Context) bool {
	log := ctrl.LoggerFrom(ctx).WithName("processNextSnapshot")

	key, quit := r.snapshotsQueue.Get()
	if quit {
		return false
	}

	startTime := time.Now()
	defer func() {
		log.V(5).Info("Finished snapshot job", "key", key, "elapsed", time.Since(startTime))
	}()

	defer r.snapshotsQueue.Done(key)
	r.qManager.UpdateSnapshot(key.(string), r.queueVisibilityClusterQueuesMaxCount)
	return true
}
