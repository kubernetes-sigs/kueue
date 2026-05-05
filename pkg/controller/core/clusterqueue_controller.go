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
	"iter"
	"math"
	"slices"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
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
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/workload"
)

type ClusterQueueUpdateWatcher interface {
	NotifyClusterQueueUpdate(*kueue.ClusterQueue, *kueue.ClusterQueue)
}

// ClusterQueueReconciler reconciles a ClusterQueue object
type ClusterQueueReconciler struct {
	client                client.Client
	logName               string
	qManager              *qcache.Manager
	cache                 *schdcache.Cache
	nonCQObjectUpdateCh   chan event.TypedGenericEvent[iter.Seq[kueue.ClusterQueueReference]]
	watchers              []ClusterQueueUpdateWatcher
	reportResourceMetrics bool
	fairSharingEnabled    bool
	clock                 clock.Clock
	roleTracker           *roletracker.RoleTracker
	customLabels          *metrics.CustomLabels
}

var _ reconcile.Reconciler = (*ClusterQueueReconciler)(nil)
var _ predicate.TypedPredicate[*kueue.ClusterQueue] = (*ClusterQueueReconciler)(nil)

type ClusterQueueReconcilerOptions struct {
	Watchers              []ClusterQueueUpdateWatcher
	ReportResourceMetrics bool
	FairSharingEnabled    bool
	clock                 clock.Clock
	roleTracker           *roletracker.RoleTracker
	customLabels          *metrics.CustomLabels
}

// ClusterQueueReconcilerOption configures the reconciler.
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

func WithFairSharing(enabled bool) ClusterQueueReconcilerOption {
	return func(o *ClusterQueueReconcilerOptions) {
		o.FairSharingEnabled = enabled
	}
}

func WithClusterQueueRoleTracker(tracker *roletracker.RoleTracker) ClusterQueueReconcilerOption {
	return func(o *ClusterQueueReconcilerOptions) {
		o.roleTracker = tracker
	}
}

func WithClusterQueueCustomLabels(customLabels *metrics.CustomLabels) ClusterQueueReconcilerOption {
	return func(o *ClusterQueueReconcilerOptions) {
		o.customLabels = customLabels
	}
}

var defaultCQOptions = ClusterQueueReconcilerOptions{
	clock: realClock,
}

func NewClusterQueueReconciler(
	client client.Client,
	qMgr *qcache.Manager,
	cache *schdcache.Cache,
	opts ...ClusterQueueReconcilerOption,
) *ClusterQueueReconciler {
	options := defaultCQOptions
	for _, opt := range opts {
		opt(&options)
	}
	return &ClusterQueueReconciler{
		client:                client,
		logName:               "cluster-queue-reconciler",
		qManager:              qMgr,
		cache:                 cache,
		nonCQObjectUpdateCh:   make(chan event.TypedGenericEvent[iter.Seq[kueue.ClusterQueueReference]], updateChBuffer),
		watchers:              options.Watchers,
		reportResourceMetrics: options.ReportResourceMetrics,
		fairSharingEnabled:    options.FairSharingEnabled,
		clock:                 options.clock,
		roleTracker:           options.roleTracker,
		customLabels:          options.customLabels,
	}
}

func (r *ClusterQueueReconciler) logger() logr.Logger {
	return roletracker.WithReplicaRole(ctrl.Log.WithName(r.logName), r.roleTracker)
}

// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=clusterqueues,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=clusterqueues/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=clusterqueues/finalizers,verbs=update

func (r *ClusterQueueReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var cqObj kueue.ClusterQueue
	if err := r.client.Get(ctx, req.NamespacedName, &cqObj); err != nil {
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile ClusterQueue")

	if features.Enabled(features.CustomMetricLabels) {
		r.customLabels.CQStoreAndClear(kueue.ClusterQueueReference(cqObj.Name),
			cqObj.GetLabels(), cqObj.GetAnnotations(),
			func() {
				cqRef := kueue.ClusterQueueReference(cqObj.Name)
				metrics.ClearClusterQueueMetrics(cqRef)
				metrics.ClearClusterQueueMetricsOnLabelChange(cqRef)
				metrics.ClearCacheMetrics(cqObj.Name)
				metrics.ClearClusterQueueResourceMetrics(cqObj.Name)
			})
	}

	if cqObj.DeletionTimestamp.IsZero() {
		// Although we'll add the finalizer via webhook mutation now, this is still useful
		// as a fallback.
		if !controllerutil.ContainsFinalizer(&cqObj, kueue.ResourceInUseFinalizerName) {
			controllerutil.AddFinalizer(&cqObj, kueue.ResourceInUseFinalizerName)
			if err := r.client.Update(ctx, &cqObj); err != nil {
				return ctrl.Result{}, client.IgnoreNotFound(err)
			}
		}

		r.cache.RecordCohortMetrics(log, cqObj.Spec.CohortName)
	} else {
		if !r.cache.ClusterQueueTerminating(kueue.ClusterQueueReference(cqObj.Name)) {
			r.cache.TerminateClusterQueue(kueue.ClusterQueueReference(cqObj.Name))
		}

		if controllerutil.ContainsFinalizer(&cqObj, kueue.ResourceInUseFinalizerName) {
			// The clusterQueue is being deleted, remove the finalizer only if
			// there are no active reserving workloads.
			if r.cache.ClusterQueueEmpty(kueue.ClusterQueueReference(cqObj.Name)) {
				controllerutil.RemoveFinalizer(&cqObj, kueue.ResourceInUseFinalizerName)
				if err := r.client.Update(ctx, &cqObj); err != nil {
					return ctrl.Result{}, client.IgnoreNotFound(err)
				}
			}
			return ctrl.Result{}, nil
		}
	}

	newCQObj := cqObj.DeepCopy()
	cqCondition, reason, msg := r.cache.ClusterQueueReadiness(kueue.ClusterQueueReference(newCQObj.Name))
	if err := r.updateCqStatusIfChanged(ctx, newCQObj, cqCondition, reason, msg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return ctrl.Result{}, nil
}

// NotifyTopologyUpdate triggers a topology update event only on creation or deletion,
// as these are the only changes affecting the ClusterQueue's active state.
func (r *ClusterQueueReconciler) NotifyTopologyUpdate(oldTopology, newTopology *kueue.Topology) {
	var topology *kueue.Topology
	switch {
	case oldTopology == nil:
		// Create Event.
		topology = newTopology
	case newTopology == nil:
		// Delete Event.
		topology = oldTopology
	default:
		return
	}
	cqNames := r.cache.ClusterQueuesUsingTopology(kueue.TopologyReference(topology.Name))
	r.nonCQObjectUpdateCh <- event.TypedGenericEvent[iter.Seq[kueue.ClusterQueueReference]]{
		Object: slices.Values(cqNames),
	}
	// On topology creation, CQs may transition from pending to active.
	// Broadcast to ensure the scheduler re-evaluates pending workloads.
	if oldTopology == nil {
		qcache.NotifyRetryInadmissible(r.qManager, sets.New(cqNames...))
		r.qManager.Broadcast()
	}
}

// NotifyWorkloadUpdate signals the controller to reconcile the ClusterQueue
// associated to the workload in the event.
func (r *ClusterQueueReconciler) NotifyWorkloadUpdate(oldWl, newWl *kueue.Workload) {
	var wls []*kueue.Workload
	switch {
	case oldWl != nil && newWl != nil:
		// Update Event
		wls = []*kueue.Workload{oldWl}
		if oldWl.Spec.QueueName != newWl.Spec.QueueName {
			wls = append(wls, newWl)
		}
	case oldWl == nil:
		// Create Event
		wls = []*kueue.Workload{newWl}
	default:
		// Delete Event
		wls = []*kueue.Workload{oldWl}
	}
	r.nonCQObjectUpdateCh <- event.TypedGenericEvent[iter.Seq[kueue.ClusterQueueReference]]{
		Object: r.requestCQForWL(wls),
	}
}

func (r *ClusterQueueReconciler) requestCQForWL(wls []*kueue.Workload) iter.Seq[kueue.ClusterQueueReference] {
	return func(yield func(kueue.ClusterQueueReference) bool) {
		for _, wl := range wls {
			var req kueue.ClusterQueueReference
			if workload.HasQuotaReservation(wl) {
				req = wl.Status.Admission.ClusterQueue
			} else if cqName, ok := r.qManager.ClusterQueueForWorkload(wl); ok {
				req = cqName
			}
			if len(req) > 0 {
				if !yield(req) {
					return
				}
			}
		}
	}
}

func (r *ClusterQueueReconciler) notifyWatchers(oldCQ, newCQ *kueue.ClusterQueue) {
	for _, w := range r.watchers {
		w.NotifyClusterQueueUpdate(oldCQ, newCQ)
	}
}

// NotifyResourceFlavorUpdate ignores updates since they have no impact on the ClusterQueue's readiness.
func (r *ClusterQueueReconciler) NotifyResourceFlavorUpdate(oldRF, newRF *kueue.ResourceFlavor) {
	var rfName string
	switch {
	case oldRF == nil:
		// Create Event.
		rfName = newRF.Name
	case newRF == nil:
		// Delete Event.
		rfName = oldRF.Name
	default:
		return
	}
	r.nonCQObjectUpdateCh <- event.TypedGenericEvent[iter.Seq[kueue.ClusterQueueReference]]{
		Object: slices.Values(r.cache.ClusterQueuesUsingFlavor(kueue.ResourceFlavorReference(rfName))),
	}
}

func (r *ClusterQueueReconciler) NotifyAdmissionCheckUpdate(oldAc, newAc *kueue.AdmissionCheck) {
	var acName kueue.AdmissionCheckReference
	switch {
	case oldAc != nil:
		// Delete or Update Event.
		acName = kueue.AdmissionCheckReference(oldAc.Name)
	case newAc != nil:
		// Create Event.
		acName = kueue.AdmissionCheckReference(newAc.Name)
	default:
		return
	}
	r.nonCQObjectUpdateCh <- event.TypedGenericEvent[iter.Seq[kueue.ClusterQueueReference]]{
		Object: slices.Values(r.cache.ClusterQueuesUsingAdmissionCheck(acName)),
	}
}

// Event handlers return true to signal the controller to reconcile the
// ClusterQueue associated with the event.

func (r *ClusterQueueReconciler) Create(e event.TypedCreateEvent[*kueue.ClusterQueue]) bool {
	defer r.notifyWatchers(nil, e.Object)

	log := r.logger().WithValues("clusterQueue", klog.KObj(e.Object))
	log.V(2).Info("ClusterQueue create event")
	ctx := ctrl.LoggerInto(context.Background(), log)
	if err := r.cache.AddClusterQueue(ctx, e.Object); err != nil {
		log.Error(err, "Failed to add clusterQueue to cache")
	}

	if err := r.qManager.AddClusterQueue(ctx, e.Object); err != nil {
		log.Error(err, "Failed to add clusterQueue to queue manager")
	}

	if features.Enabled(features.CustomMetricLabels) {
		r.customLabels.CQStore(kueue.ClusterQueueReference(e.Object.GetName()), e.Object.GetLabels(), e.Object.GetAnnotations())
	}

	if r.reportResourceMetrics {
		r.cache.RecordClusterQueueResourceMetrics(log, kueue.ClusterQueueReference(e.Object.Name))
	}

	return true
}

func (r *ClusterQueueReconciler) Delete(e event.TypedDeleteEvent[*kueue.ClusterQueue]) bool {
	defer r.notifyWatchers(e.Object, nil)

	log := r.logger()
	log.V(2).Info("ClusterQueue delete event", "clusterQueue", klog.KObj(e.Object))
	r.cache.ClearCohortMetrics(log, e.Object.Spec.CohortName)
	r.cache.DeleteClusterQueue(e.Object)
	r.qManager.DeleteClusterQueue(e.Object)

	metrics.ClearClusterQueueResourceMetrics(e.Object.Name)
	if features.Enabled(features.CustomMetricLabels) {
		r.customLabels.CQDelete(kueue.ClusterQueueReference(e.Object.GetName()))
	}
	log.V(2).Info("Cleared resource metrics for deleted ClusterQueue.", "clusterQueue", klog.KObj(e.Object))

	return true
}

func (r *ClusterQueueReconciler) Update(e event.TypedUpdateEvent[*kueue.ClusterQueue]) bool {
	log := r.logger().WithValues("clusterQueue", klog.KObj(e.ObjectNew))
	log.V(2).Info("ClusterQueue update event")

	if !e.ObjectNew.DeletionTimestamp.IsZero() {
		return true
	}
	defer r.notifyWatchers(e.ObjectOld, e.ObjectNew)
	specUpdated := !equality.Semantic.DeepEqual(e.ObjectOld.Spec, e.ObjectNew.Spec)

	var labelsUpdated bool
	if features.Enabled(features.CustomMetricLabels) {
		labelsUpdated = r.customLabels.CQStoreAndClear(
			kueue.ClusterQueueReference(e.ObjectNew.GetName()),
			e.ObjectNew.GetLabels(), e.ObjectNew.GetAnnotations(),
			func() {
				cqRef := kueue.ClusterQueueReference(e.ObjectNew.Name)
				metrics.ClearClusterQueueMetrics(cqRef)
				metrics.ClearClusterQueueMetricsOnLabelChange(cqRef)
				metrics.ClearCacheMetrics(e.ObjectNew.Name)
				metrics.ClearClusterQueueResourceMetrics(e.ObjectNew.Name)
			})
	}

	if err := r.cache.UpdateClusterQueue(log, e.ObjectNew); err != nil {
		log.Error(err, "Failed to update clusterQueue in cache")
	}
	if err := r.qManager.UpdateClusterQueue(context.Background(), e.ObjectNew, specUpdated, labelsUpdated); err != nil {
		log.Error(err, "Failed to update clusterQueue in queue manager")
	}

	if e.ObjectOld.Spec.CohortName != e.ObjectNew.Spec.CohortName {
		// refresh metrics - clear existing series for the cohort and record current values from cache state
		r.cache.ClearCohortMetrics(log, e.ObjectOld.Spec.CohortName)
		r.cache.RecordCohortMetrics(log, e.ObjectOld.Spec.CohortName)
	}

	if r.reportResourceMetrics {
		r.updateResourceMetrics(log, e.ObjectOld, e.ObjectNew)
	}
	return true
}

func (r *ClusterQueueReconciler) Generic(e event.TypedGenericEvent[*kueue.ClusterQueue]) bool {
	r.logger().V(3).Info("Got ClusterQueue generic event", "clusterQueue", klog.KObj(e.Object))
	return true
}

func (r *ClusterQueueReconciler) updateResourceMetrics(log logr.Logger, oldCq, newCq *kueue.ClusterQueue) {
	// if the cohort changed, drop all the old metrics
	if oldCq.Spec.CohortName != newCq.Spec.CohortName {
		metrics.ClearClusterQueueResourceMetrics(oldCq.Name)
	} else {
		// selective remove
		r.cache.ClearClusterQueueOldResourceMetrics(log, oldCq)
	}
	r.cache.RecordClusterQueueResourceMetrics(log, kueue.ClusterQueueReference(newCq.Name))
}

// cqNamespaceHandler handles namespace update events.
type cqNamespaceHandler struct {
	qManager *qcache.Manager
	cache    *schdcache.Cache
}

func (h *cqNamespaceHandler) Create(_ context.Context, _ event.CreateEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *cqNamespaceHandler) Update(ctx context.Context, e event.UpdateEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	oldNs := e.ObjectOld.(*corev1.Namespace)
	oldMatchingCqs := h.cache.MatchingClusterQueues(oldNs.Labels)
	newNs := e.ObjectNew.(*corev1.Namespace)
	newMatchingCqs := h.cache.MatchingClusterQueues(newNs.Labels)
	cqs := sets.New[kueue.ClusterQueueReference]()
	for cq := range newMatchingCqs {
		if !oldMatchingCqs.Has(cq) {
			cqs.Insert(cq)
		}
	}
	qcache.NotifyRetryInadmissible(h.qManager, cqs)
}

func (h *cqNamespaceHandler) Delete(context.Context, event.DeleteEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *cqNamespaceHandler) Generic(context.Context, event.GenericEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

type nonCQObjectHandler struct{}

var _ handler.TypedEventHandler[iter.Seq[kueue.ClusterQueueReference], reconcile.Request] = (*nonCQObjectHandler)(nil)

func (h *nonCQObjectHandler) Create(context.Context, event.TypedCreateEvent[iter.Seq[kueue.ClusterQueueReference]], workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}
func (h *nonCQObjectHandler) Update(context.Context, event.TypedUpdateEvent[iter.Seq[kueue.ClusterQueueReference]], workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}
func (h *nonCQObjectHandler) Delete(context.Context, event.TypedDeleteEvent[iter.Seq[kueue.ClusterQueueReference]], workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}
func (h *nonCQObjectHandler) Generic(_ context.Context, e event.TypedGenericEvent[iter.Seq[kueue.ClusterQueueReference]], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	for cq := range e.Object {
		q.AddAfter(reconcile.Request{NamespacedName: types.NamespacedName{
			Name: string(cq),
		}}, constants.UpdatesBatchPeriod)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterQueueReconciler) SetupWithManager(mgr ctrl.Manager, cfg *config.Configuration) error {
	nsHandler := cqNamespaceHandler{
		qManager: r.qManager,
		cache:    r.cache,
	}
	return builder.TypedControllerManagedBy[reconcile.Request](mgr).
		Named("clusterqueue_controller").
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&kueue.ClusterQueue{},
			&handler.TypedEnqueueRequestForObject[*kueue.ClusterQueue]{},
			r,
		)).
		WithOptions(controller.Options{
			NeedLeaderElection:      new(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[kueue.GroupVersion.WithKind("ClusterQueue").GroupKind().String()],
			LogConstructor:          roletracker.NewLogConstructor(r.roleTracker, "clusterqueue-reconciler"),
		}).
		Watches(&corev1.Namespace{}, &nsHandler).
		WatchesRawSource(source.Channel(r.nonCQObjectUpdateCh, &nonCQObjectHandler{})).
		Complete(WithLeadingManager(mgr, r, &kueue.ClusterQueue{}, cfg))
}

func (r *ClusterQueueReconciler) updateCqStatusIfChanged(
	ctx context.Context,
	cq *kueue.ClusterQueue,
	conditionStatus metav1.ConditionStatus,
	reason, msg string,
) error {
	log := r.logger()
	oldStatus := cq.Status.DeepCopy()
	pendingWorkloads, err := r.qManager.Pending(cq)
	if err != nil {
		log.Error(err, "Failed getting pending workloads from queue manager")
		return err
	}
	stats, err := r.cache.Usage(cq)
	if err != nil {
		log.Error(err, "Failed getting usage from cache")
		// This is likely because the cluster queue was recently removed,
		// but we didn't process that event yet.
		return err
	}
	cq.Status.FlavorsReservation = stats.ReservedResources
	cq.Status.FlavorsUsage = stats.AdmittedResources
	cq.Status.ReservingWorkloads = int32(stats.ReservingWorkloads)
	cq.Status.AdmittedWorkloads = int32(stats.AdmittedWorkloads)
	cq.Status.PendingWorkloads = int32(pendingWorkloads)
	meta.SetStatusCondition(&cq.Status.Conditions, metav1.Condition{
		Type:               kueue.ClusterQueueActive,
		Status:             conditionStatus,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: cq.Generation,
	})
	if r.fairSharingEnabled {
		if r.reportResourceMetrics {
			weightedShare := stats.WeightedShare
			if weightedShare == math.Inf(1) {
				weightedShare = math.NaN()
			}
			metrics.ReportClusterQueueWeightedShare(kueue.ClusterQueueReference(cq.Name), cq.Spec.CohortName, weightedShare, r.customLabels.CQGet(kueue.ClusterQueueReference(cq.Name)), r.roleTracker)
		}
		if cq.Status.FairSharing == nil {
			cq.Status.FairSharing = &kueue.FairSharingStatus{}
		}
		cq.Status.FairSharing.WeightedShare = WeightedShare(stats.WeightedShare)
	} else {
		cq.Status.FairSharing = nil
	}
	if !equality.Semantic.DeepEqual(cq.Status, oldStatus) {
		return r.client.Status().Update(ctx, cq)
	}
	return nil
}
