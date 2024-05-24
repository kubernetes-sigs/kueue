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
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/go-logr/logr"
	gocmp "github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/queue"
	utilac "sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	// statuses for logging purposes
	pending       = "pending"
	quotaReserved = "quotaReserved"
	admitted      = "admitted"
	finished      = "finished"
)

var (
	realClock = clock.RealClock{}
)

type waitForPodsReadyConfig struct {
	timeout                     time.Duration
	requeuingBackoffLimitCount  *int32
	requeuingBackoffBaseSeconds int32
	requeuingBackoffMaxDuration time.Duration
	requeuingBackoffJitter      float64
}

type options struct {
	watchers               []WorkloadUpdateWatcher
	waitForPodsReadyConfig *waitForPodsReadyConfig
}

// Option configures the reconciler.
type Option func(*options)

// WithWaitForPodsReady indicates the configuration for the WaitForPodsReady feature.
func WithWaitForPodsReady(value *waitForPodsReadyConfig) Option {
	return func(o *options) {
		o.waitForPodsReadyConfig = value
	}
}

// WithWorkloadUpdateWatchers allows to specify the workload update watchers
func WithWorkloadUpdateWatchers(value ...WorkloadUpdateWatcher) Option {
	return func(o *options) {
		o.watchers = value
	}
}

var defaultOptions = options{}

type WorkloadUpdateWatcher interface {
	NotifyWorkloadUpdate(oldWl, newWl *kueue.Workload)
}

// WorkloadReconciler reconciles a Workload object
type WorkloadReconciler struct {
	log              logr.Logger
	queues           *queue.Manager
	cache            *cache.Cache
	client           client.Client
	watchers         []WorkloadUpdateWatcher
	waitForPodsReady *waitForPodsReadyConfig
	recorder         record.EventRecorder
	clock            clock.Clock
}

func NewWorkloadReconciler(client client.Client, queues *queue.Manager, cache *cache.Cache, recorder record.EventRecorder, opts ...Option) *WorkloadReconciler {
	options := defaultOptions
	for _, opt := range opts {
		opt(&options)
	}

	return &WorkloadReconciler{
		log:              ctrl.Log.WithName("workload-reconciler"),
		client:           client,
		queues:           queues,
		cache:            cache,
		watchers:         options.watchers,
		waitForPodsReady: options.waitForPodsReadyConfig,
		recorder:         recorder,
		clock:            realClock,
	}
}

// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update;patch
// +kubebuilder:rbac:groups="",resources=limitranges,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=node.k8s.io,resources=runtimeclasses,verbs=get;list;watch

func (r *WorkloadReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var wl kueue.Workload
	if err := r.client.Get(ctx, req.NamespacedName, &wl); err != nil {
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(&wl))
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(2).Info("Reconciling Workload")

	if len(wl.ObjectMeta.OwnerReferences) == 0 && !wl.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, workload.RemoveFinalizer(ctx, r.client, &wl)
	}

	if apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadFinished) {
		return ctrl.Result{}, nil
	}

	if ptr.Deref(wl.Spec.Active, true) {
		var updated bool

		// If a deactivated workload is re-activated we need to reset the RequeueState.
		if workload.IsEvictedByDeactivation(&wl) && wl.Status.RequeueState != nil {
			wl.Status.RequeueState = nil
			updated = true
		}

		if cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadRequeued); cond != nil && cond.Status == metav1.ConditionFalse {
			switch cond.Reason {
			case kueue.WorkloadEvictedByDeactivation:
				workload.SetRequeuedCondition(&wl, kueue.WorkloadReactivated, "The workload was reactivated", true)
				updated = true
			case kueue.WorkloadEvictedByPodsReadyTimeout:
				var requeueAfter time.Duration
				if wl.Status.RequeueState != nil && wl.Status.RequeueState.RequeueAt != nil {
					requeueAfter = wl.Status.RequeueState.RequeueAt.Time.Sub(r.clock.Now())
				}
				if requeueAfter > 0 {
					return reconcile.Result{RequeueAfter: requeueAfter}, nil
				}
				if wl.Status.RequeueState != nil {
					wl.Status.RequeueState.RequeueAt = nil
				}
				workload.SetRequeuedCondition(&wl, kueue.WorkloadBackoffFinished, "The workload backoff was finished", true)
				updated = true
			}
		}

		if updated {
			return ctrl.Result{}, workload.ApplyAdmissionStatus(ctx, r.client, &wl, true)
		}
	} else if !apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadEvicted) {
		// if job is not active and does not have condition reason of WorkloadEvictedByDeactivation, update its condition
		workload.SetEvictedCondition(&wl, kueue.WorkloadEvictedByDeactivation, "The workload is deactivated")
		if err := workload.ApplyAdmissionStatus(ctx, r.client, &wl, true); err != nil {
			return ctrl.Result{}, fmt.Errorf("setting eviction: %w", err)
		}
		if wl.Status.Admission != nil {
			metrics.ReportEvictedWorkloads(string(wl.Status.Admission.ClusterQueue), kueue.WorkloadEvictedByDeactivation)
		}
		return ctrl.Result{}, nil
	}

	cqName, cqOk := r.queues.ClusterQueueForWorkload(&wl)
	if cqOk {
		// because we need to react to API cluster cq events, the list of checks from a cache can lead to race conditions
		cq := kueue.ClusterQueue{}
		if err := r.client.Get(ctx, types.NamespacedName{Name: cqName}, &cq); err != nil {
			return ctrl.Result{}, err
		}
		// If stopped cluster queue is started we need to set the WorkloadRequeued condition to true.
		if isDisabledRequeuedByClusterQueueStopped(&wl) && ptr.Deref(cq.Spec.StopPolicy, kueue.None) == kueue.None {
			workload.SetRequeuedCondition(&wl, kueue.WorkloadClusterQueueRestarted, "The ClusterQueue was restarted after being stopped", true)
			return ctrl.Result{}, workload.ApplyAdmissionStatus(ctx, r.client, &wl, true)
		}
		if updated, err := r.reconcileSyncAdmissionChecks(ctx, &wl, &cq); updated || err != nil {
			return ctrl.Result{}, err
		}
	}

	// If the workload is admitted, updating the status here would set the Admitted condition to
	// false before the workloads eviction.
	if !workload.IsAdmitted(&wl) && workload.SyncAdmittedCondition(&wl) {
		if err := workload.ApplyAdmissionStatus(ctx, r.client, &wl, true); err != nil {
			return ctrl.Result{}, err
		}
		if workload.IsAdmitted(&wl) {
			queuedWaitTime := workload.QueuedWaitTime(&wl)
			quotaReservedCondition := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			quotaReservedWaitTime := time.Since(quotaReservedCondition.LastTransitionTime.Time)
			r.recorder.Eventf(&wl, corev1.EventTypeNormal, "Admitted", "Admitted by ClusterQueue %v, wait time since reservation was %.0fs", wl.Status.Admission.ClusterQueue, quotaReservedWaitTime.Seconds())
			metrics.AdmittedWorkload(kueue.ClusterQueueReference(cqName), queuedWaitTime)
			metrics.AdmissionChecksWaitTime(kueue.ClusterQueueReference(cqName), quotaReservedWaitTime)
		}
		return ctrl.Result{}, nil
	}

	if workload.HasQuotaReservation(&wl) {
		if evictionTriggered, err := r.reconcileCheckBasedEviction(ctx, &wl); evictionTriggered || err != nil {
			return ctrl.Result{}, err
		}

		if updated, err := r.reconcileOnClusterQueueActiveState(ctx, &wl, cqName); updated || err != nil {
			return ctrl.Result{}, err
		}

		return r.reconcileNotReadyTimeout(ctx, req, &wl)
	}

	// At this point the workload is not Admitted, if it has rejected admission checks mark it as finished.
	if rejectedChecks := workload.GetRejectedChecks(&wl); len(rejectedChecks) > 0 {
		log.V(3).Info("Workload has Rejected admission checks, Finish with failure")
		err := workload.UpdateStatus(ctx, r.client, &wl, kueue.WorkloadFinished,
			metav1.ConditionTrue,
			kueue.WorkloadFinishedReasonAdmissionChecksRejected,
			fmt.Sprintf("Admission checks %v are rejected", rejectedChecks),
			constants.KueueName)
		if err == nil {
			for _, owner := range wl.OwnerReferences {
				uowner := unstructured.Unstructured{}
				uowner.SetKind(owner.Kind)
				uowner.SetAPIVersion(owner.APIVersion)
				uowner.SetName(owner.Name)
				uowner.SetNamespace(wl.Namespace)
				uowner.SetUID(owner.UID)
				r.recorder.Eventf(&uowner, corev1.EventTypeNormal, "WorkloadFinished", "Admission checks %v are rejected", rejectedChecks)
			}
		}
		return ctrl.Result{}, err
	}

	switch {
	case !r.queues.QueueForWorkloadExists(&wl):
		log.V(3).Info("Workload is inadmissible because of missing LocalQueue", "localQueue", klog.KRef(wl.Namespace, wl.Spec.QueueName))
		if workload.UnsetQuotaReservationWithCondition(&wl, "Inadmissible", fmt.Sprintf("LocalQueue %s doesn't exist", wl.Spec.QueueName)) {
			err := workload.ApplyAdmissionStatus(ctx, r.client, &wl, true)
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	case !cqOk:
		log.V(3).Info("Workload is inadmissible because of missing ClusterQueue", "clusterQueue", klog.KRef("", cqName))
		if workload.UnsetQuotaReservationWithCondition(&wl, "Inadmissible", fmt.Sprintf("ClusterQueue %s doesn't exist", cqName)) {
			err := workload.ApplyAdmissionStatus(ctx, r.client, &wl, true)
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	case !r.cache.ClusterQueueActive(cqName):
		log.V(3).Info("Workload is inadmissible because ClusterQueue is inactive", "clusterQueue", klog.KRef("", cqName))
		if workload.UnsetQuotaReservationWithCondition(&wl, "Inadmissible", fmt.Sprintf("ClusterQueue %s is inactive", cqName)) {
			err := workload.ApplyAdmissionStatus(ctx, r.client, &wl, true)
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	}

	return ctrl.Result{}, nil
}

// isDisabledRequeuedByClusterQueueStopped returns true if the workload is unset requeued by cluster queue stopped.
func isDisabledRequeuedByClusterQueueStopped(w *kueue.Workload) bool {
	cond := apimeta.FindStatusCondition(w.Status.Conditions, kueue.WorkloadRequeued)
	return cond != nil && cond.Status == metav1.ConditionFalse && cond.Reason == kueue.WorkloadEvictedByClusterQueueStopped
}

func (r *WorkloadReconciler) reconcileCheckBasedEviction(ctx context.Context, wl *kueue.Workload) (bool, error) {
	if apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadEvicted) || !workload.HasRetryOrRejectedChecks(wl) {
		return false, nil
	}
	log := ctrl.LoggerFrom(ctx)
	log.V(3).Info("Workload is evicted due to admission checks")
	workload.SetEvictedCondition(wl, kueue.WorkloadEvictedByAdmissionCheck, "At least one admission check is false")
	err := workload.ApplyAdmissionStatus(ctx, r.client, wl, true)
	if err == nil {
		cqName, _ := r.queues.ClusterQueueForWorkload(wl)
		metrics.ReportEvictedWorkloads(cqName, kueue.WorkloadEvictedByAdmissionCheck)
	}
	return true, client.IgnoreNotFound(err)
}

func (r *WorkloadReconciler) reconcileSyncAdmissionChecks(ctx context.Context, wl *kueue.Workload, cq *kueue.ClusterQueue) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	admissionChecks := workload.AdmissionChecksForWorkload(log, wl, utilac.NewAdmissionChecks(cq))
	newChecks, shouldUpdate := syncAdmissionCheckConditions(wl.Status.AdmissionChecks, admissionChecks)
	if shouldUpdate {
		log.V(3).Info("The workload needs admission checks updates", "clusterQueue", klog.KRef("", cq.Name), "admissionChecks", admissionChecks)
		wl.Status.AdmissionChecks = newChecks
		err := r.client.Status().Update(ctx, wl)
		return true, client.IgnoreNotFound(err)
	}
	return false, nil
}

func (r *WorkloadReconciler) reconcileOnClusterQueueActiveState(ctx context.Context, wl *kueue.Workload, cqName string) (bool, error) {
	queue := kueue.ClusterQueue{}
	err := r.client.Get(ctx, types.NamespacedName{Name: cqName}, &queue)
	if client.IgnoreNotFound(err) != nil {
		return false, err
	}

	queueStopPolicy := ptr.Deref(queue.Spec.StopPolicy, kueue.None)

	log := ctrl.LoggerFrom(ctx)
	if workload.IsAdmitted(wl) {
		if queueStopPolicy != kueue.HoldAndDrain {
			return false, nil
		}
		if apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadEvicted) {
			log.V(3).Info("Workload is already evicted due to clusterQueue stopped.")
			return false, nil
		}
		log.V(3).Info("Workload is evicted because the ClusterQueue is stopped", "clusterQueue", klog.KRef("", cqName))
		workload.SetEvictedCondition(wl, kueue.WorkloadEvictedByClusterQueueStopped, "The ClusterQueue is stopped")
		err := workload.ApplyAdmissionStatus(ctx, r.client, wl, true)
		if err == nil {
			metrics.ReportEvictedWorkloads(cqName, kueue.WorkloadEvictedByClusterQueueStopped)
		}
		return true, client.IgnoreNotFound(err)
	}

	if err != nil || !queue.DeletionTimestamp.IsZero() {
		log.V(3).Info("Workload is inadmissible because the ClusterQueue is terminating or missing", "clusterQueue", klog.KRef("", cqName))
		_ = workload.UnsetQuotaReservationWithCondition(wl, "Inadmissible", fmt.Sprintf("ClusterQueue %s is terminating or missing", cqName))
		return true, workload.ApplyAdmissionStatus(ctx, r.client, wl, true)
	}

	if queueStopPolicy != kueue.None {
		log.V(3).Info("Workload is inadmissible because the ClusterQueue is stopped", "clusterQueue", klog.KRef("", cqName))
		_ = workload.UnsetQuotaReservationWithCondition(wl, "Inadmissible", fmt.Sprintf("ClusterQueue %s is stopped", cqName))
		return true, workload.ApplyAdmissionStatus(ctx, r.client, wl, true)
	}

	return false, nil
}

func syncAdmissionCheckConditions(conds []kueue.AdmissionCheckState, admissionChecks sets.Set[string]) ([]kueue.AdmissionCheckState, bool) {
	if len(admissionChecks) == 0 {
		return nil, len(conds) > 0
	}

	shouldUpdate := false
	currentChecks := utilslices.ToRefMap(conds, func(c *kueue.AdmissionCheckState) string { return c.Name })
	for t := range admissionChecks {
		if _, found := currentChecks[t]; !found {
			workload.SetAdmissionCheckState(&conds, kueue.AdmissionCheckState{
				Name:  t,
				State: kueue.CheckStatePending,
			})
			shouldUpdate = true
		}
	}

	// if the workload conditions length is bigger, then some cleanup should be done
	if len(conds) > len(admissionChecks) {
		newConds := make([]kueue.AdmissionCheckState, 0, len(admissionChecks))
		shouldUpdate = true
		for i := range conds {
			c := &conds[i]
			if admissionChecks.Has(c.Name) {
				newConds = append(newConds, *c)
			}
		}
		conds = newConds
	}
	slices.SortFunc(conds, func(state1, state2 kueue.AdmissionCheckState) int {
		return cmp.Compare(state1.Name, state2.Name)
	})
	return conds, shouldUpdate
}

func (r *WorkloadReconciler) reconcileNotReadyTimeout(ctx context.Context, req ctrl.Request, wl *kueue.Workload) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	if !ptr.Deref(wl.Spec.Active, true) || apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadEvicted) {
		// the workload has already been evicted by the PodsReadyTimeout or been deactivated.
		return ctrl.Result{}, nil
	}
	countingTowardsTimeout, recheckAfter := r.admittedNotReadyWorkload(wl)
	if !countingTowardsTimeout {
		return ctrl.Result{}, nil
	}
	if recheckAfter > 0 {
		log.V(4).Info("Workload not yet ready and did not exceed its timeout", "recheckAfter", recheckAfter)
		return ctrl.Result{RequeueAfter: recheckAfter}, nil
	}
	log.V(2).Info("Start the eviction of the workload due to exceeding the PodsReady timeout")
	if deactivated, err := r.triggerDeactivationOrBackoffRequeue(ctx, wl); deactivated || err != nil {
		return ctrl.Result{}, err
	}
	workload.SetEvictedCondition(wl, kueue.WorkloadEvictedByPodsReadyTimeout, fmt.Sprintf("Exceeded the PodsReady timeout %s", req.NamespacedName.String()))
	err := workload.ApplyAdmissionStatus(ctx, r.client, wl, true)
	if err == nil {
		cqName, _ := r.queues.ClusterQueueForWorkload(wl)
		metrics.ReportEvictedWorkloads(cqName, kueue.WorkloadEvictedByPodsReadyTimeout)
	}
	return ctrl.Result{}, client.IgnoreNotFound(err)
}

// triggerDeactivationOrBackoffRequeue deactivates a workload (".spec.active"="false")
// if a re-queued number has already exceeded the limit of re-queuing backoff.
// Otherwise, it increments a re-queueing count and update a time to be re-queued.
// It returns true as a first value if a workload is deactivated.
func (r *WorkloadReconciler) triggerDeactivationOrBackoffRequeue(ctx context.Context, wl *kueue.Workload) (bool, error) {
	if !workload.HasRequeueState(wl) {
		wl.Status.RequeueState = &kueue.RequeueState{}
	}
	// If requeuingBackoffLimitCount equals to null, the workloads is repeatedly and endless re-queued.
	requeuingCount := ptr.Deref(wl.Status.RequeueState.Count, 0) + 1
	if r.waitForPodsReady.requeuingBackoffLimitCount != nil && requeuingCount > *r.waitForPodsReady.requeuingBackoffLimitCount {
		wl.Spec.Active = ptr.To(false)
		if err := r.client.Update(ctx, wl); err != nil {
			return false, err
		}
		r.recorder.Eventf(wl, corev1.EventTypeNormal, kueue.WorkloadEvictedByDeactivation,
			"Deactivated Workload %q by reached re-queue backoffLimitCount", klog.KObj(wl))
		return true, nil
	}
	// Every backoff duration is about "60s*2^(n-1)+Rand" where:
	// - "n" represents the "requeuingCount",
	// - "Rand" represents the random jitter.
	// During this time, the workload is taken as an inadmissible and other
	// workloads will have a chance to be admitted.
	backoff := &wait.Backoff{
		Duration: time.Duration(r.waitForPodsReady.requeuingBackoffBaseSeconds) * time.Second,
		Factor:   2,
		Jitter:   r.waitForPodsReady.requeuingBackoffJitter,
		Steps:    int(requeuingCount),
	}
	var waitDuration time.Duration
	for backoff.Steps > 0 {
		waitDuration = min(backoff.Step(), r.waitForPodsReady.requeuingBackoffMaxDuration)
	}

	wl.Status.RequeueState.RequeueAt = ptr.To(metav1.NewTime(r.clock.Now().Add(waitDuration)))
	wl.Status.RequeueState.Count = &requeuingCount
	return false, nil
}

func (r *WorkloadReconciler) Create(e event.CreateEvent) bool {
	wl, isWorkload := e.Object.(*kueue.Workload)
	if !isWorkload {
		// this event will be handled by the LimitRange/RuntimeClass handle
		return true
	}
	defer r.notifyWatchers(nil, wl)
	status := workloadStatus(wl)
	log := r.log.WithValues("workload", klog.KObj(wl), "queue", wl.Spec.QueueName, "status", status)
	log.V(2).Info("Workload create event")

	if status == finished {
		return true
	}

	ctx := ctrl.LoggerInto(context.Background(), log)
	wlCopy := wl.DeepCopy()
	workload.AdjustResources(ctx, r.client, wlCopy)

	if !workload.HasQuotaReservation(wl) {
		if !r.queues.AddOrUpdateWorkload(wlCopy) {
			log.V(2).Info("Queue for workload didn't exist; ignored for now")
		}
		return true
	}
	if !r.cache.AddOrUpdateWorkload(wlCopy) {
		log.V(2).Info("ClusterQueue for workload didn't exist; ignored for now")
	}

	return true
}

func (r *WorkloadReconciler) Delete(e event.DeleteEvent) bool {
	wl, isWorkload := e.Object.(*kueue.Workload)
	if !isWorkload {
		// this event will be handled by the LimitRange/RuntimeClass handle
		return true
	}
	defer r.notifyWatchers(wl, nil)
	status := "unknown"
	if !e.DeleteStateUnknown {
		status = workloadStatus(wl)
	}
	log := r.log.WithValues("workload", klog.KObj(wl), "queue", wl.Spec.QueueName, "status", status)
	log.V(2).Info("Workload delete event")
	ctx := ctrl.LoggerInto(context.Background(), log)

	// When assigning a clusterQueue to a workload, we assume it in the cache. If
	// the state is unknown, the workload could have been assumed, and we need
	// to clear it from the cache.
	if workload.HasQuotaReservation(wl) || e.DeleteStateUnknown {
		// trigger the move of associated inadmissibleWorkloads if required.
		r.queues.QueueAssociatedInadmissibleWorkloadsAfter(ctx, wl, func() {
			// Delete the workload from cache while holding the queues lock
			// to guarantee that requeued workloads are taken into account before
			// the next scheduling cycle.
			if err := r.cache.DeleteWorkload(wl); err != nil {
				if !e.DeleteStateUnknown {
					log.Error(err, "Failed to delete workload from cache")
				}
			}
		})
	}

	// Even if the state is unknown, the last cached state tells us whether the
	// workload was in the queues and should be cleared from them.
	r.queues.DeleteWorkload(wl)

	return true
}

func (r *WorkloadReconciler) Update(e event.UpdateEvent) bool {
	oldWl, isWorkload := e.ObjectOld.(*kueue.Workload)
	if !isWorkload {
		// this event will be handled by the LimitRange/RuntimeClass handle
		return true
	}
	wl := e.ObjectNew.(*kueue.Workload)
	defer r.notifyWatchers(oldWl, wl)

	status := workloadStatus(wl)
	log := r.log.WithValues("workload", klog.KObj(wl), "queue", wl.Spec.QueueName, "status", status)
	ctx := ctrl.LoggerInto(context.Background(), log)
	active := ptr.Deref(wl.Spec.Active, true)

	prevQueue := oldWl.Spec.QueueName
	if prevQueue != wl.Spec.QueueName {
		log = log.WithValues("prevQueue", prevQueue)
	}
	prevStatus := workloadStatus(oldWl)
	if prevStatus != status {
		log = log.WithValues("prevStatus", prevStatus)
	}
	if workload.HasQuotaReservation(wl) {
		log = log.WithValues("clusterQueue", wl.Status.Admission.ClusterQueue)
	}
	if workload.HasQuotaReservation(oldWl) && (!workload.HasQuotaReservation(wl) || wl.Status.Admission.ClusterQueue != oldWl.Status.Admission.ClusterQueue) {
		log = log.WithValues("prevClusterQueue", oldWl.Status.Admission.ClusterQueue)
	}
	log.V(2).Info("Workload update event")

	wlCopy := wl.DeepCopy()
	// We do not handle old workload here as it will be deleted or replaced by new one anyway.
	workload.AdjustResources(ctrl.LoggerInto(ctx, log), r.client, wlCopy)

	switch {
	case status == finished || !active:
		if !active {
			log.V(2).Info("Workload will not be queued because the workload is not active", "workload", klog.KObj(wl))
		}
		// The workload could have been in the queues if we missed an event.
		r.queues.DeleteWorkload(wl)

		// trigger the move of associated inadmissibleWorkloads, if there are any.
		r.queues.QueueAssociatedInadmissibleWorkloadsAfter(ctx, wl, func() {
			// Delete the workload from cache while holding the queues lock
			// to guarantee that requeued workloads are taken into account before
			// the next scheduling cycle.
			if err := r.cache.DeleteWorkload(oldWl); err != nil && prevStatus == admitted {
				log.Error(err, "Failed to delete workload from cache")
			}
		})

	case prevStatus == pending && status == pending:
		if !r.queues.UpdateWorkload(oldWl, wlCopy) {
			log.V(2).Info("Queue for updated workload didn't exist; ignoring for now")
		}

	case prevStatus == pending && (status == quotaReserved || status == admitted):
		r.queues.DeleteWorkload(oldWl)
		if !r.cache.AddOrUpdateWorkload(wlCopy) {
			log.V(2).Info("ClusterQueue for workload didn't exist; ignored for now")
		}
	case (prevStatus == quotaReserved || prevStatus == admitted) && status == pending:
		var backoff time.Duration
		if wlCopy.Status.RequeueState != nil && wlCopy.Status.RequeueState.RequeueAt != nil {
			backoff = time.Until(wl.Status.RequeueState.RequeueAt.Time)
		}
		immediate := backoff <= 0
		// trigger the move of associated inadmissibleWorkloads, if there are any.
		r.queues.QueueAssociatedInadmissibleWorkloadsAfter(ctx, wl, func() {
			// Delete the workload from cache while holding the queues lock
			// to guarantee that requeued workloads are taken into account before
			// the next scheduling cycle.
			if err := r.cache.DeleteWorkload(wl); err != nil {
				log.Error(err, "Failed to delete workload from cache")
			}
			// Here we don't take the lock as it is already taken by the wrapping
			// function.
			if immediate {
				if !r.queues.AddOrUpdateWorkloadWithoutLock(wlCopy) {
					log.V(2).Info("Queue for workload didn't exist; ignored for now")
				}
			}
		})

		if !immediate {
			log.V(3).Info("Workload to be requeued after backoff", "backoff", backoff, "requeueAt", wl.Status.RequeueState.RequeueAt.Time)
			time.AfterFunc(backoff, func() {
				updatedWl := kueue.Workload{}
				err := r.client.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)
				if err == nil && workloadStatus(&updatedWl) == pending {
					if !r.queues.AddOrUpdateWorkload(wlCopy) {
						log.V(2).Info("Queue for workload didn't exist; ignored for now")
					} else {
						log.V(3).Info("Workload requeued after backoff")
					}
				}
			})
		}
	case prevStatus == admitted && status == admitted && !equality.Semantic.DeepEqual(oldWl.Status.ReclaimablePods, wl.Status.ReclaimablePods):
		// trigger the move of associated inadmissibleWorkloads, if there are any.
		r.queues.QueueAssociatedInadmissibleWorkloadsAfter(ctx, wl, func() {
			// Update the workload from cache while holding the queues lock
			// to guarantee that requeued workloads are taken into account before
			// the next scheduling cycle.
			if err := r.cache.UpdateWorkload(oldWl, wlCopy); err != nil {
				log.Error(err, "Failed to delete workload from cache")
			}
		})

	default:
		// Workload update in the cache is handled here; however, some fields are immutable
		// and are not supposed to actually change anything.
		if err := r.cache.UpdateWorkload(oldWl, wlCopy); err != nil {
			log.Error(err, "Updating workload in cache")
		}
	}

	return true
}

func (r *WorkloadReconciler) Generic(e event.GenericEvent) bool {
	r.log.V(3).Info("Ignore generic event", "obj", klog.KObj(e.Object), "kind", e.Object.GetObjectKind().GroupVersionKind())
	return false
}

func (r *WorkloadReconciler) notifyWatchers(oldWl, newWl *kueue.Workload) {
	for _, w := range r.watchers {
		w.NotifyWorkloadUpdate(oldWl, newWl)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkloadReconciler) SetupWithManager(mgr ctrl.Manager, cfg *config.Configuration) error {
	ruh := &resourceUpdatesHandler{
		r: r,
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&kueue.Workload{}).
		WithOptions(controller.Options{NeedLeaderElection: ptr.To(false)}).
		Watches(&corev1.LimitRange{}, ruh).
		Watches(&nodev1.RuntimeClass{}, ruh).
		Watches(&kueue.ClusterQueue{}, &workloadCqHandler{client: r.client}).
		WithEventFilter(r).
		Complete(WithLeadingManager(mgr, r, &kueue.Workload{}, cfg))
}

// admittedNotReadyWorkload returns as a pair of values. The first boolean determines
// if the workload is currently counting towards the timeout for PodsReady, i.e.
// it has the Admitted condition True and the PodsReady condition not equal
// True (False or not set). The second value is the remaining time to exceed the
// specified timeout counted since max of the LastTransitionTime's for the
// Admitted and PodsReady conditions.
func (r *WorkloadReconciler) admittedNotReadyWorkload(wl *kueue.Workload) (bool, time.Duration) {
	if r.waitForPodsReady == nil {
		// the timeout is not configured for the workload controller
		return false, 0
	}
	if !workload.IsAdmitted(wl) {
		// the workload is not admitted so there is no need to time it out
		return false, 0
	}

	podsReadyCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadPodsReady)
	if podsReadyCond != nil && podsReadyCond.Status == metav1.ConditionTrue {
		return false, 0
	}
	admittedCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadAdmitted)
	elapsedTime := r.clock.Since(admittedCond.LastTransitionTime.Time)
	if podsReadyCond != nil && podsReadyCond.Status == metav1.ConditionFalse && podsReadyCond.LastTransitionTime.After(admittedCond.LastTransitionTime.Time) {
		elapsedTime = r.clock.Since(podsReadyCond.LastTransitionTime.Time)
	}
	waitFor := r.waitForPodsReady.timeout - elapsedTime
	if waitFor < 0 {
		waitFor = 0
	}
	return true, waitFor
}

func workloadStatus(w *kueue.Workload) string {
	if apimeta.IsStatusConditionTrue(w.Status.Conditions, kueue.WorkloadFinished) {
		return finished
	}
	if workload.IsAdmitted(w) {
		return admitted
	}
	if workload.HasQuotaReservation(w) {
		return quotaReserved
	}
	return pending
}

type resourceUpdatesHandler struct {
	r *WorkloadReconciler
}

func (h *resourceUpdatesHandler) Create(ctx context.Context, e event.CreateEvent, q workqueue.RateLimitingInterface) {
	log := ctrl.LoggerFrom(ctx).WithValues("kind", e.Object.GetObjectKind())
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(5).Info("Create event")
	h.handle(ctx, e.Object, q)
}

func (h *resourceUpdatesHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.RateLimitingInterface) {
	log := ctrl.LoggerFrom(ctx).WithValues("kind", e.ObjectNew.GetObjectKind())
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(5).Info("Update event")
	h.handle(ctx, e.ObjectNew, q)
}

func (h *resourceUpdatesHandler) Delete(ctx context.Context, e event.DeleteEvent, q workqueue.RateLimitingInterface) {
	log := ctrl.LoggerFrom(ctx).WithValues("kind", e.Object.GetObjectKind())
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(5).Info("Delete event")
	h.handle(ctx, e.Object, q)
}

func (h *resourceUpdatesHandler) Generic(_ context.Context, _ event.GenericEvent, _ workqueue.RateLimitingInterface) {
}

func (h *resourceUpdatesHandler) handle(ctx context.Context, obj client.Object, q workqueue.RateLimitingInterface) {
	switch v := obj.(type) {
	case *corev1.LimitRange:
		log := ctrl.LoggerFrom(ctx).WithValues("limitRange", klog.KObj(v))
		ctx = ctrl.LoggerInto(ctx, log)
		h.queueReconcileForPending(ctx, q, client.InNamespace(v.Namespace))
	case *nodev1.RuntimeClass:
		log := ctrl.LoggerFrom(ctx).WithValues("runtimeClass", klog.KObj(v))
		ctx = ctrl.LoggerInto(ctx, log)
		h.queueReconcileForPending(ctx, q, client.MatchingFields{indexer.WorkloadRuntimeClassKey: v.Name})
	default:
		panic(v)
	}
}

func (h *resourceUpdatesHandler) queueReconcileForPending(ctx context.Context, _ workqueue.RateLimitingInterface, opts ...client.ListOption) {
	log := ctrl.LoggerFrom(ctx)
	lst := kueue.WorkloadList{}
	opts = append(opts, client.MatchingFields{indexer.WorkloadQuotaReservedKey: string(metav1.ConditionFalse)})
	err := h.r.client.List(ctx, &lst, opts...)
	if err != nil {
		log.Error(err, "Could not list pending workloads")
	}
	log.V(4).Info("Updating pending workload requests", "count", len(lst.Items))
	for _, w := range lst.Items {
		wlCopy := w.DeepCopy()
		log := log.WithValues("workload", klog.KObj(wlCopy))
		log.V(5).Info("Queue reconcile for")
		workload.AdjustResources(ctrl.LoggerInto(ctx, log), h.r.client, wlCopy)
		if !h.r.queues.AddOrUpdateWorkload(wlCopy) {
			log.V(2).Info("Queue for workload didn't exist")
		}
	}
}

type workloadCqHandler struct {
	client client.Client
}

var _ handler.EventHandler = (*workloadCqHandler)(nil)

// Create is called in response to a create event.
func (w *workloadCqHandler) Create(ctx context.Context, ev event.CreateEvent, wq workqueue.RateLimitingInterface) {
	if cq, isQueue := ev.Object.(*kueue.ClusterQueue); isQueue {
		w.queueReconcileForWorkloads(ctx, cq.Name, wq)
	}
}

// Update is called in response to an update event.
func (w *workloadCqHandler) Update(ctx context.Context, ev event.UpdateEvent, wq workqueue.RateLimitingInterface) {
	log := ctrl.LoggerFrom(ctx).WithValues("clusterQueue", klog.KObj(ev.ObjectNew))
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(5).Info("Workload cluster queue update event")
	oldCq, oldIsQueue := ev.ObjectOld.(*kueue.ClusterQueue)
	newCq, newIsQueue := ev.ObjectNew.(*kueue.ClusterQueue)

	if !oldIsQueue || !newIsQueue {
		return
	}

	if !newCq.DeletionTimestamp.IsZero() ||
		!utilslices.CmpNoOrder(oldCq.Spec.AdmissionChecks, newCq.Spec.AdmissionChecks) ||
		!gocmp.Equal(oldCq.Spec.AdmissionChecksStrategy, newCq.Spec.AdmissionChecksStrategy) ||
		!ptr.Equal(oldCq.Spec.StopPolicy, newCq.Spec.StopPolicy) {
		w.queueReconcileForWorkloads(ctx, newCq.Name, wq)
	}
}

// Delete is called in response to a delete event.
func (w *workloadCqHandler) Delete(ctx context.Context, ev event.DeleteEvent, wq workqueue.RateLimitingInterface) {
	if cq, isQueue := ev.Object.(*kueue.ClusterQueue); isQueue {
		w.queueReconcileForWorkloads(ctx, cq.Name, wq)
	}
}

// Generic is called in response to an event of an unknown type or a synthetic event triggered as a cron or
// external trigger request.
func (w *workloadCqHandler) Generic(_ context.Context, _ event.GenericEvent, _ workqueue.RateLimitingInterface) {
	// nothing to do here
}

func (w *workloadCqHandler) queueReconcileForWorkloads(ctx context.Context, cqName string, wq workqueue.RateLimitingInterface) {
	log := ctrl.LoggerFrom(ctx)
	lst := kueue.LocalQueueList{}
	err := w.client.List(ctx, &lst, client.MatchingFields{indexer.QueueClusterQueueKey: cqName})
	if err != nil {
		log.Error(err, "Could not list cluster queues local queues")
	}
	for _, lq := range lst.Items {
		log := log.WithValues("localQueue", klog.KObj(&lq))
		ctx = ctrl.LoggerInto(ctx, log)
		w.queueReconcileForWorkloadsOfLocalQueue(ctx, lq.Namespace, lq.Name, wq)
	}
}

func (w *workloadCqHandler) queueReconcileForWorkloadsOfLocalQueue(ctx context.Context, namespace string, name string, wq workqueue.RateLimitingInterface) {
	log := ctrl.LoggerFrom(ctx)
	lst := kueue.WorkloadList{}
	err := w.client.List(ctx, &lst, &client.ListOptions{Namespace: namespace}, client.MatchingFields{indexer.WorkloadQueueKey: name})
	if err != nil {
		log.Error(err, "Could not list cluster queues workloads")
	}
	for _, wl := range lst.Items {
		log := log.WithValues("workload", klog.KObj(&wl))
		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      wl.Name,
				Namespace: wl.Namespace,
			},
		}
		wq.Add(req)
		log.V(5).Info("Queued reconcile for workload")
	}
}
