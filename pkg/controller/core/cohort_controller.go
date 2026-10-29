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
	"errors"
	"math"
	"sync"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/resourcegroups"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

type CohortUpdateWatcher interface {
	NotifyCohortUpdate(oldCohort, newCohort *kueue.Cohort)
}

type CohortReconcilerOptions struct {
	FairSharingEnabled bool
	roleTracker        *roletracker.RoleTracker
	customLabels       *metrics.CustomLabels
}

type CohortReconcilerOption func(*CohortReconcilerOptions)

func CohortReconcilerWithFairSharing(enabled bool) CohortReconcilerOption {
	return func(o *CohortReconcilerOptions) {
		o.FairSharingEnabled = enabled
	}
}

func CohortReconcilerWithRoleTracker(tracker *roletracker.RoleTracker) CohortReconcilerOption {
	return func(o *CohortReconcilerOptions) {
		o.roleTracker = tracker
	}
}

func CohortReconcilerWithCustomLabels(customLabels *metrics.CustomLabels) CohortReconcilerOption {
	return func(o *CohortReconcilerOptions) {
		o.customLabels = customLabels
	}
}

// CohortReconciler is responsible for synchronizing the in-memory
// representation of Cohorts in cache.Cache and queue.Manager with
// Cohort Kubernetes objects.
type CohortReconciler struct {
	client             client.Client
	logName            string
	cache              *schdcache.Cache
	qManager           *qcache.Manager
	cqUpdateCh         chan event.TypedGenericEvent[clusterQueueUpdate]
	watchers           []CohortUpdateWatcher
	fairSharingEnabled bool
	roleTracker        *roletracker.RoleTracker
	customLabels       *metrics.CustomLabels
	// cqNotifyPending holds the Cohorts whose ClusterQueues the next Reconcile
	// notifies: the Cohort's spec or quotas changed, or, with fair sharing, a
	// ClusterQueue under it was created, deleted or changed its spec or quotas.
	// A Cohort requeued because a ClusterQueue wrote its status does not notify
	// its ClusterQueues: that reconciled every ClusterQueue in the tree, and
	// their status writes requeued the Cohort again.
	cqNotifyPending     sets.Set[kueue.CohortReference]
	cqNotifyPendingLock sync.Mutex
}

// clusterQueueUpdate is a ClusterQueue create (oldCQ nil), update or delete
// (newCQ nil) event forwarded by the ClusterQueue controller.
type clusterQueueUpdate struct {
	oldCQ, newCQ *kueue.ClusterQueue
}

func NewCohortReconciler(
	client client.Client,
	cache *schdcache.Cache,
	qManager *qcache.Manager,
	opts ...CohortReconcilerOption,
) *CohortReconciler {
	options := CohortReconcilerOptions{}
	for _, opt := range opts {
		opt(&options)
	}

	return &CohortReconciler{
		client:             client,
		logName:            "cohort-reconciler",
		cache:              cache,
		qManager:           qManager,
		cqUpdateCh:         make(chan event.TypedGenericEvent[clusterQueueUpdate], updateChBuffer),
		fairSharingEnabled: options.FairSharingEnabled,
		roleTracker:        options.roleTracker,
		customLabels:       options.customLabels,
		cqNotifyPending:    sets.New[kueue.CohortReference](),
	}
}

func (r *CohortReconciler) markClusterQueueNotification(cohorts ...kueue.CohortReference) {
	r.cqNotifyPendingLock.Lock()
	defer r.cqNotifyPendingLock.Unlock()
	r.cqNotifyPending.Insert(cohorts...)
}

// takeClusterQueueNotification reports whether the Cohort's ClusterQueues are
// to be notified and clears the entry; Reconcile restores it when it fails
// before notifying.
func (r *CohortReconciler) takeClusterQueueNotification(cohort kueue.CohortReference) bool {
	r.cqNotifyPendingLock.Lock()
	defer r.cqNotifyPendingLock.Unlock()
	pending := r.cqNotifyPending.Has(cohort)
	r.cqNotifyPending.Delete(cohort)
	return pending
}

func (r *CohortReconciler) clearClusterQueueNotification(cohort kueue.CohortReference) {
	r.cqNotifyPendingLock.Lock()
	defer r.cqNotifyPendingLock.Unlock()
	r.cqNotifyPending.Delete(cohort)
}

func (r *CohortReconciler) AddUpdateWatcher(watchers ...CohortUpdateWatcher) {
	r.watchers = append(r.watchers, watchers...)
}

func (r *CohortReconciler) notifyWatchers(oldCohort, newCohort *kueue.Cohort) {
	for _, w := range r.watchers {
		w.NotifyCohortUpdate(oldCohort, newCohort)
	}
}

func (r *CohortReconciler) logger() logr.Logger {
	return roletracker.WithReplicaRole(ctrl.Log.WithName(r.logName), r.roleTracker)
}

func (r *CohortReconciler) SetupWithManager(mgr ctrl.Manager, cfg *config.Configuration) error {
	cqHandler := &cohortCqHandler{reconciler: r}
	return ctrl.NewControllerManagedBy(mgr).
		Named("cohort_controller").
		WatchesRawSource(r.cache.TrackInitialSync(source.TypedKind(
			mgr.GetCache(),
			&kueue.Cohort{},
			&handler.TypedEnqueueRequestForObject[*kueue.Cohort]{},
			r,
		))).
		WithOptions(controller.Options{
			NeedLeaderElection:      new(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[kueue.SchemeGroupVersion.WithKind("Cohort").GroupKind().String()],
			LogConstructor:          roletracker.NewLogConstructor(r.roleTracker, "cohort-reconciler"),
		}).
		WatchesRawSource(source.Channel(r.cqUpdateCh, cqHandler)).
		Complete(WithLeadingManager(mgr, r, &kueue.Cohort{}, cfg))
}

// The event handlers keep the Cohort in the scheduler cache and the queue
// manager on every replica, as the ClusterQueue, ResourceFlavor, and Topology
// handlers do. Reconcile runs only on the leader (see WithLeadingManager), so
// a follower that only updated the cache from Reconcile would take the lease
// without its explicit Cohorts and their quotas until each one was requeued,
// which takes up to the lease duration. Reconcile repeats the same update,
// retries on errors, notifies the ClusterQueues, and writes the status.

func (r *CohortReconciler) Create(e event.TypedCreateEvent[*kueue.Cohort]) bool {
	if e.Object != nil && features.Enabled(features.CustomMetricLabels) {
		r.customLabels.CohortStore(kueue.CohortReference(e.Object.GetName()), e.Object.GetLabels(), e.Object.GetAnnotations())
	}
	if e.Object != nil {
		r.markClusterQueueNotification(kueue.CohortReference(e.Object.GetName()))
		r.addOrUpdateCohortInMemory(e.Object)
	}
	return true
}

func (r *CohortReconciler) Update(e event.TypedUpdateEvent[*kueue.Cohort]) bool {
	log := r.logger().WithValues("cohort", klog.KObj(e.ObjectNew))

	clUpdateRequired := features.Enabled(features.CustomMetricLabels) && r.customLabels.UpdateRequired(
		config.SourceKindCohort,
		e.ObjectNew.GetName(),
		e.ObjectNew.GetLabels(),
		e.ObjectNew.GetAnnotations(),
	)

	specOrQuotaUpdated := !equality.Semantic.DeepEqual(e.ObjectOld.Spec, e.ObjectNew.Spec) ||
		!equality.Semantic.DeepEqual(resourcegroups.EffectiveCohortResourceGroups(e.ObjectOld), resourcegroups.EffectiveCohortResourceGroups(e.ObjectNew))

	if !specOrQuotaUpdated && !clUpdateRequired {
		log.V(2).Info("Skip Cohort update event as Cohort unchanged")
		return false
	}
	log.V(2).Info("Processing Cohort update event")
	if specOrQuotaUpdated {
		r.addOrUpdateCohortInMemory(e.ObjectNew)
		r.markClusterQueueNotification(kueue.CohortReference(e.ObjectNew.GetName()))
	}
	return true
}

func (r *CohortReconciler) Delete(e event.TypedDeleteEvent[*kueue.Cohort]) bool {
	if e.Object != nil && features.Enabled(features.CustomMetricLabels) {
		r.customLabels.CohortDelete(kueue.CohortReference(e.Object.GetName()))
	}
	if e.Object != nil {
		log := r.logger().WithValues("cohort", klog.KObj(e.Object))
		log.V(2).Info("Cohort delete event")
		r.removeCohortInMemory(log, kueue.CohortReference(e.Object.GetName()))
		r.clearClusterQueueNotification(kueue.CohortReference(e.Object.GetName()))
	}
	return true
}

// addOrUpdateCohortInMemory adds or updates the Cohort in the scheduler cache
// and the queue manager. Reconcile retries errors and reports a cycle to the
// ClusterQueues.
func (r *CohortReconciler) addOrUpdateCohortInMemory(cohort *kueue.Cohort) {
	log := r.logger().WithValues("cohort", klog.KObj(cohort))
	err := r.cache.AddOrUpdateCohort(cohort)
	if err != nil && !errors.Is(err, schdcache.ErrCohortHasCycle) {
		// Reconcile hits the same error and retries.
		log.V(2).Info("Skipped the queue manager update for the Cohort in the event handler", "reason", err.Error())
		return
	}
	// The cache keeps the new parent even when it creates a cycle, so the
	// queue manager records it too, the same way Reconcile does.
	r.qManager.AddOrUpdateCohort(ctrl.LoggerInto(context.Background(), log), cohort)
}

// removeCohortInMemory removes the Cohort from the scheduler cache and the
// queue manager and clears its metrics. Metrics are cleared first because
// clearing them walks the Cohort's ancestors in the cache.
func (r *CohortReconciler) removeCohortInMemory(log logr.Logger, name kueue.CohortReference) {
	r.cache.ClearCohortMetrics(log, name)
	r.cache.DeleteCohort(name)
	r.qManager.DeleteCohort(name)
	metrics.ClearCohortMetrics(name)
}

func (r *CohortReconciler) Generic(event.TypedGenericEvent[*kueue.Cohort]) bool {
	return true
}

//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=cohorts,verbs=get;list;watch
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=cohorts/status,verbs=get;update;patch

func (r *CohortReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile Cohort")

	cohortName := kueue.CohortReference(req.Name)
	notifyClusterQueues := r.takeClusterQueueNotification(cohortName)
	defer func() {
		// A return before the notification keeps it for the retry.
		if notifyClusterQueues {
			r.markClusterQueueNotification(cohortName)
		}
	}()
	var cohort kueue.Cohort
	if err := r.client.Get(ctx, req.NamespacedName, &cohort); err != nil {
		if apierrors.IsNotFound(err) {
			log.V(2).Info("Cohort is being deleted")
			r.removeCohortInMemory(log, cohortName)
			r.notifyWatchers(&kueue.Cohort{Name: req.Name}, nil)
			notifyClusterQueues = false
			if features.Enabled(features.CustomMetricLabels) {
				r.customLabels.CohortDelete(kueue.CohortReference(req.Name))
			}
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var labelsUpdated bool
	if features.Enabled(features.CustomMetricLabels) {
		labelsUpdated = r.customLabels.CohortStore(
			kueue.CohortReference(cohort.Name),
			cohort.GetLabels(), cohort.GetAnnotations(),
		)
	}

	log.V(2).Info("Cohort is being created or updated",
		"resources", resourcegroups.EffectiveCohortResourceGroups(&cohort),
		"usesEffectiveQuotas", features.Enabled(features.DynamicQuotaOrchestration) && cohort.Status.EffectiveQuotas != nil,
	)
	addErr := r.cache.AddOrUpdateCohort(&cohort)
	if addErr != nil && !errors.Is(addErr, schdcache.ErrCohortHasCycle) {
		log.V(2).Error(addErr, "Error adding or updating cohort in the cache")
		return ctrl.Result{}, addErr
	}
	// Record the new parent in the queue manager even when it creates a cycle: the
	// queue manager tolerates cycles, so its hierarchy still matches the cache when
	// another Cohort resolves the cycle. Notified ClusterQueues report a cycle as
	// their inactive reason.
	r.qManager.AddOrUpdateCohort(ctx, &cohort)
	if notifyClusterQueues {
		r.notifyWatchers(nil, &cohort)
		notifyClusterQueues = false
	}
	if errors.Is(addErr, schdcache.ErrCohortHasCycle) {
		// Skip consumers that require a valid tree.
		return ctrl.Result{}, nil
	}
	if labelsUpdated {
		metrics.ClearCohortMetrics(kueue.CohortReference(req.Name))
		r.cache.ResyncCohortGaugeMetrics(log, kueue.CohortReference(req.Name))
	} else {
		r.cache.RecordCohortMetrics(log, kueue.CohortReference(req.Name))
	}

	err := r.updateCohortStatusIfChanged(ctx, &cohort)
	return ctrl.Result{}, client.IgnoreNotFound(err)
}

// updateCohortStatusIfChanged recomputes the Cohort status from the cache and writes it to the
// API server only when it differs from the current status.
func (r *CohortReconciler) updateCohortStatusIfChanged(ctx context.Context, cohort *kueue.Cohort) error {
	log := ctrl.LoggerFrom(ctx)

	oldStatus := cohort.Status.DeepCopy()

	stats, err := r.cache.CohortStats(cohort)
	if err != nil {
		log.Error(err, "Failed getting cohort usage from cache")
		return err
	}

	if r.fairSharingEnabled {
		weightedShare := stats.WeightedShare
		if stats.ZeroWeightBorrows {
			weightedShare = math.NaN()
		}
		metrics.ReportCohortWeightedShare(kueue.CohortReference(cohort.Name), weightedShare, r.customLabels.CohortGet(kueue.CohortReference(cohort.Name)), r.roleTracker)
		if cohort.Status.FairSharing == nil {
			cohort.Status.FairSharing = &kueue.FairSharingStatus{}
		}
		cohort.Status.FairSharing.WeightedShare = WeightedShare(stats.WeightedShare)
	} else {
		cohort.Status.FairSharing = nil
	}

	if !equality.Semantic.DeepEqual(&cohort.Status, oldStatus) {
		return r.client.Status().Update(ctx, cohort)
	}

	return nil
}

func (r *CohortReconciler) NotifyClusterQueueUpdate(oldCQ, newCQ *kueue.ClusterQueue) {
	r.cqUpdateCh <- event.TypedGenericEvent[clusterQueueUpdate]{Object: clusterQueueUpdate{oldCQ: oldCQ, newCQ: newCQ}}
}

// cohortCqHandler requeues the ancestors of a changed ClusterQueue, so their
// status and metrics follow its usage. Other ClusterQueues' status depends on
// it only through fair sharing, whose weighted share divides borrowing by the
// tree's lendable quota, so only then does a ClusterQueue created, deleted or
// changed in spec or quotas make the ancestors notify their ClusterQueues.
type cohortCqHandler struct {
	reconciler *CohortReconciler
}

var _ handler.TypedEventHandler[clusterQueueUpdate, reconcile.Request] = (*cohortCqHandler)(nil)

func (h *cohortCqHandler) Create(context.Context, event.TypedCreateEvent[clusterQueueUpdate], workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *cohortCqHandler) Update(context.Context, event.TypedUpdateEvent[clusterQueueUpdate], workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *cohortCqHandler) Delete(context.Context, event.TypedDeleteEvent[clusterQueueUpdate], workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *cohortCqHandler) Generic(ctx context.Context, e event.TypedGenericEvent[clusterQueueUpdate], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	oldCQ, newCQ := e.Object.oldCQ, e.Object.newCQ
	// A delete event carries only the old ClusterQueue.
	cq := newCQ
	if cq == nil {
		cq = oldCQ
	}
	if cq == nil {
		return
	}

	ancestors := h.ancestors(ctx, cq)
	// A ClusterQueue that moved leaves its old tree to update too.
	if oldCQ != nil && newCQ != nil && oldCQ.Spec.CohortName != newCQ.Spec.CohortName {
		ancestors = append(ancestors, h.ancestors(ctx, oldCQ)...)
	}
	if h.reconciler.fairSharingEnabled && (oldCQ == nil || newCQ == nil || clusterQueueSpecOrQuotaUpdated(oldCQ, newCQ)) {
		h.reconciler.markClusterQueueNotification(ancestors...)
	}
	for _, ancestor := range ancestors {
		q.Add(reconcile.Request{Name: string(ancestor)})
	}
}

func (h *cohortCqHandler) ancestors(ctx context.Context, cq *kueue.ClusterQueue) []kueue.CohortReference {
	ancestors, err := h.reconciler.cache.ClusterQueueAncestors(cq)
	if err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed getting ancestors for cohort", "cohort", cq.Spec.CohortName)
	}
	return ancestors
}
