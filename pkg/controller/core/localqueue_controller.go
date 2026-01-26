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
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
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

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	afs "sigs.k8s.io/kueue/pkg/util/admissionfairsharing"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/util/resource"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
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

type LocalQueueReconcilerOptions struct {
	admissionFSConfig *config.AdmissionFairSharing
	clock             clock.Clock
	roleTracker       *roletracker.RoleTracker
}

// LocalQueueReconcilerOption configures the reconciler.
type LocalQueueReconcilerOption func(*LocalQueueReconcilerOptions)

func WithAdmissionFairSharingConfig(cfg *config.AdmissionFairSharing) LocalQueueReconcilerOption {
	return func(o *LocalQueueReconcilerOptions) {
		o.admissionFSConfig = cfg
	}
}

func WithClock(c clock.Clock) LocalQueueReconcilerOption {
	return func(o *LocalQueueReconcilerOptions) {
		o.clock = c
	}
}

func WithRoleTracker(tracker *roletracker.RoleTracker) LocalQueueReconcilerOption {
	return func(o *LocalQueueReconcilerOptions) {
		o.roleTracker = tracker
	}
}

var defaultLQOptions = LocalQueueReconcilerOptions{
	clock: realClock,
}

// LocalQueueReconciler reconciles a LocalQueue object
type LocalQueueReconciler struct {
	client            client.Client
	logName           string
	queues            *qcache.Manager
	cache             *schdcache.Cache
	wlUpdateCh        chan event.GenericEvent
	admissionFSConfig *config.AdmissionFairSharing
	clock             clock.Clock
	roleTracker       *roletracker.RoleTracker
}

var _ reconcile.Reconciler = (*LocalQueueReconciler)(nil)
var _ predicate.TypedPredicate[*kueue.LocalQueue] = (*LocalQueueReconciler)(nil)

func NewLocalQueueReconciler(
	client client.Client,
	queues *qcache.Manager,
	cache *schdcache.Cache,
	opts ...LocalQueueReconcilerOption,
) *LocalQueueReconciler {
	options := defaultLQOptions
	for _, opt := range opts {
		opt(&options)
	}
	return &LocalQueueReconciler{
		logName:           "localqueue-reconciler",
		queues:            queues,
		cache:             cache,
		client:            client,
		wlUpdateCh:        make(chan event.GenericEvent, updateChBuffer),
		admissionFSConfig: options.admissionFSConfig,
		clock:             options.clock,
		roleTracker:       options.roleTracker,
	}
}

func (r *LocalQueueReconciler) logger() logr.Logger {
	return roletracker.WithReplicaRole(ctrl.Log.WithName(r.logName), r.roleTracker)
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
	if err := r.client.Get(ctx, client.ObjectKey{Name: string(queueObj.Spec.ClusterQueue)}, &cq); err != nil {
		if apierrors.IsNotFound(err) {
			err = r.UpdateStatusIfChanged(ctx, &queueObj, metav1.ConditionFalse, "ClusterQueueDoesNotExist", clusterQueueIsInactiveMsg)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if meta.IsStatusConditionTrue(cq.Status.Conditions, kueue.ClusterQueueActive) {
		if err := r.UpdateStatusIfChanged(ctx, &queueObj, metav1.ConditionTrue, "Ready", "Can submit new workloads to localQueue"); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	} else {
		err := r.UpdateStatusIfChanged(ctx, &queueObj, metav1.ConditionFalse, clusterQueueIsInactiveReason, clusterQueueIsInactiveMsg)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if afs.Enabled(r.admissionFSConfig) {
		initNeeded := r.initializeAfsIfNeeded(&queueObj)
		lqKey := utilqueue.Key(&queueObj)
		entry, found := r.queues.AfsConsumedResources.Get(lqKey)
		if !found {
			log.V(3).Info("AFS cache entry deleted concurrently, skipping reconciliation")
			return ctrl.Result{}, nil
		}
		sinceLastUpdate := r.clock.Now().Sub(entry.LastUpdate)
		if interval := r.admissionFSConfig.UsageSamplingInterval.Duration; !initNeeded && sinceLastUpdate < interval && !r.queues.AfsEntryPenalties.HasPendingFor(lqKey) {
			return ctrl.Result{RequeueAfter: interval - sinceLastUpdate}, nil
		}
		if err := r.reconcileConsumedUsage(ctx, &queueObj); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		if err := r.queues.RebuildClusterQueue(&cq, queueObj.Name); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: r.admissionFSConfig.UsageSamplingInterval.Duration}, nil
	}
	return ctrl.Result{}, nil
}

func (r *LocalQueueReconciler) Create(e event.TypedCreateEvent[*kueue.LocalQueue]) bool {
	log := r.logger().WithValues("localQueue", klog.KObj(e.Object))
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
		recordLocalQueueUsageMetrics(e.Object, r.roleTracker)
	}

	return true
}

func (r *LocalQueueReconciler) Delete(e event.TypedDeleteEvent[*kueue.LocalQueue]) bool {
	if features.Enabled(features.LocalQueueMetrics) {
		metrics.ClearLocalQueueResourceMetrics(localQueueReferenceFromLocalQueue(e.Object))
	}
	if afs.Enabled(r.admissionFSConfig) {
		lqKey := utilqueue.Key(e.Object)
		r.queues.AfsConsumedResources.Delete(lqKey)
		r.queues.AfsEntryPenalties.Delete(lqKey)
	}

	log := r.logger().WithValues("localQueue", klog.KObj(e.Object))
	log.V(2).Info("LocalQueue delete event")
	r.queues.DeleteLocalQueue(log, e.Object)
	r.cache.DeleteLocalQueue(e.Object)
	return true
}

func (r *LocalQueueReconciler) Update(e event.TypedUpdateEvent[*kueue.LocalQueue]) bool {
	log := r.logger().WithValues("localQueue", klog.KObj(e.ObjectNew))
	log.V(2).Info("Queue update event")

	if features.Enabled(features.LocalQueueMetrics) {
		updateLocalQueueResourceMetrics(e.ObjectNew, r.roleTracker)
	}

	oldStopPolicy := ptr.Deref(e.ObjectOld.Spec.StopPolicy, kueue.None)
	newStopPolicy := ptr.Deref(e.ObjectNew.Spec.StopPolicy, kueue.None)

	if newStopPolicy == oldStopPolicy {
		if newStopPolicy == kueue.None {
			if err := r.queues.UpdateLocalQueue(log, e.ObjectNew); err != nil {
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

	r.queues.DeleteLocalQueue(log, e.ObjectOld)

	return true
}

func (r *LocalQueueReconciler) initializeAfsIfNeeded(lq *kueue.LocalQueue) bool {
	if lq.Status.FairSharing == nil {
		lq.Status.FairSharing = &kueue.LocalQueueFairSharingStatus{}
	}

	lqKey := utilqueue.Key(lq)
	hasStatus := lq.Status.FairSharing.AdmissionFairSharingStatus != nil
	_, hasCache := r.queues.AfsConsumedResources.Get(lqKey)
	if hasStatus && hasCache {
		return false
	}

	now := r.clock.Now()

	if !hasStatus {
		lq.Status.FairSharing.AdmissionFairSharingStatus = &kueue.LocalQueueAdmissionFairSharingStatus{
			LastUpdate: metav1.NewTime(now),
		}
	}

	if !hasCache {
		currentUsage := r.getCurrentUsageForLocalQueue(lq.Spec.ClusterQueue, lqKey)
		r.queues.AfsConsumedResources.Set(lqKey, currentUsage, now)
	}

	return true
}

func (r *LocalQueueReconciler) getCurrentUsageForLocalQueue(cqName kueue.ClusterQueueReference, lqKey utilqueue.LocalQueueReference) corev1.ResourceList {
	cacheLq, err := r.cache.GetCacheLocalQueue(cqName, lqKey)
	if err != nil {
		return corev1.ResourceList{}
	}
	return cacheLq.GetAdmittedUsage()
}

func (r *LocalQueueReconciler) reconcileConsumedUsage(ctx context.Context, lq *kueue.LocalQueue) error {
	log := r.logger()
	lqKey := utilqueue.Key(lq)
	halfLifeTime := r.admissionFSConfig.UsageHalfLifeTime.Seconds()
	now := r.clock.Now()

	if halfLifeTime == 0 {
		if err := r.updateAdmissionFsStatus(ctx, lq, corev1.ResourceList{}, now); err != nil {
			log.V(2).Info("Failed to reset LocalQueue status", "namespace", lq.Namespace, "name", lq.Name, "error", err)
			return err
		}
		r.queues.AfsConsumedResources.Set(lqKey, corev1.ResourceList{}, now)
		log.V(2).Info("Reset AFS consumed resources cache", "namespace", lq.Namespace, "name", lq.Name)
		return nil
	}

	entry, _ := r.queues.AfsConsumedResources.Get(lqKey)

	cacheLq, err := r.cache.GetCacheLocalQueue(lq.Spec.ClusterQueue, lqKey)
	if err != nil {
		return err
	}

	oldUsage := entry.Resources
	newUsage := cacheLq.GetAdmittedUsage()
	elapsed := now.Sub(entry.LastUpdate).Seconds()
	newConsumed := afs.CalculateDecayedConsumed(oldUsage, newUsage, elapsed, halfLifeTime)

	if err := r.updateAdmissionFsStatus(ctx, lq, newConsumed, now); err != nil {
		log.V(2).Info("Failed to update LocalQueue status", "namespace", lq.Namespace, "name", lq.Name, "error", err)
		return err
	}
	r.queues.AfsConsumedResources.Set(lqKey, newConsumed, now)
	log.V(2).Info("Updated AFS consumed resources cache", "namespace", lq.Namespace, "name", lq.Name, "consumedResources", newConsumed)
	return nil
}

func (r *LocalQueueReconciler) updateAdmissionFsStatus(ctx context.Context, lq *kueue.LocalQueue, consumedResources corev1.ResourceList, lastUpdate time.Time) error {
	lq.Status.FairSharing.AdmissionFairSharingStatus.ConsumedResources = consumedResources
	lq.Status.FairSharing.AdmissionFairSharingStatus.LastUpdate = metav1.NewTime(lastUpdate)
	return r.client.Status().Update(ctx, lq)
}

func localQueueReferenceFromLocalQueue(lq *kueue.LocalQueue) metrics.LocalQueueReference {
	return metrics.LocalQueueReference{
		Name:      kueue.LocalQueueName(lq.Name),
		Namespace: lq.Namespace,
	}
}

func recordLocalQueueUsageMetrics(queue *kueue.LocalQueue, tracker *roletracker.RoleTracker) {
	for _, flavor := range queue.Status.FlavorsUsage {
		for _, r := range flavor.Resources {
			metrics.ReportLocalQueueResourceUsage(localQueueReferenceFromLocalQueue(queue), string(flavor.Name), string(r.Name), resource.QuantityToFloat(&r.Total), tracker)
		}
	}
	for _, flavor := range queue.Status.FlavorsReservation {
		for _, r := range flavor.Resources {
			metrics.ReportLocalQueueResourceReservations(localQueueReferenceFromLocalQueue(queue), string(flavor.Name), string(r.Name), resource.QuantityToFloat(&r.Total), tracker)
		}
	}
}

func updateLocalQueueResourceMetrics(queue *kueue.LocalQueue, tracker *roletracker.RoleTracker) {
	metrics.ClearLocalQueueResourceMetrics(localQueueReferenceFromLocalQueue(queue))
	recordLocalQueueUsageMetrics(queue, tracker)
}

func (r *LocalQueueReconciler) Generic(e event.TypedGenericEvent[*kueue.LocalQueue]) bool {
	r.logger().V(2).Info("LocalQueue generic event", "localQueue", klog.KObj(e.Object))
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
			Name:      string(w.Spec.QueueName),
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
		WithOptions(controller.Options{
			NeedLeaderElection:      ptr.To(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[kueue.GroupVersion.WithKind("LocalQueue").GroupKind().String()],
			LogConstructor:          roletracker.NewLogConstructor(r.roleTracker, "localqueue-reconciler"),
		}).
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
	log := r.logger()
	oldStatus := queue.Status.DeepCopy()
	var (
		pendingWls int32
		err        error
	)
	if ptr.Deref(queue.Spec.StopPolicy, kueue.None) == kueue.None {
		pendingWls, err = r.queues.PendingWorkloads(queue)
		if err != nil {
			log.Error(err, failedUpdateLqStatusMsg)
			return err
		}
	}
	stats, err := r.cache.LocalQueueUsage(queue)
	if err != nil {
		log.Error(err, failedUpdateLqStatusMsg)
		return err
	}
	queue.Status.PendingWorkloads = pendingWls
	queue.Status.ReservingWorkloads = int32(stats.ReservingWorkloads)
	queue.Status.AdmittedWorkloads = int32(stats.AdmittedWorkloads)
	queue.Status.FlavorsReservation = stats.ReservedResources
	queue.Status.FlavorsUsage = stats.AdmittedResources
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
				Name:      kueue.LocalQueueName(queue.Name),
				Namespace: queue.Namespace,
			}, conditionStatus, r.roleTracker)
		}
	}
	if !equality.Semantic.DeepEqual(oldStatus, queue.Status) {
		return r.client.Status().Update(ctx, queue)
	}
	return nil
}
