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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
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
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/dra"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	qutil "sigs.k8s.io/kueue/pkg/util/queue"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
	stringsutils "sigs.k8s.io/kueue/pkg/util/strings"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

var (
	realClock = clock.RealClock{}
)

type waitForPodsReadyConfig struct {
	timeout                     time.Duration
	recoveryTimeout             *time.Duration
	requeuingBackoffLimitCount  *int32
	requeuingBackoffBaseSeconds int32
	requeuingBackoffMaxDuration time.Duration
	requeuingBackoffJitter      float64
}

type workloadRetentionConfig struct {
	afterFinished *time.Duration
}

// Option configures the reconciler.
type Option func(*WorkloadReconciler)

// WithWaitForPodsReady indicates the configuration for the WaitForPodsReady feature.
func WithWaitForPodsReady(value *waitForPodsReadyConfig) Option {
	return func(r *WorkloadReconciler) {
		r.waitForPodsReady = value
	}
}

// WithWorkloadUpdateWatchers allows to specify the workload update watchers
func WithWorkloadUpdateWatchers(value ...WorkloadUpdateWatcher) Option {
	return func(r *WorkloadReconciler) {
		r.watchers = value
	}
}

// WithWorkloadRetention allows to specify retention for workload resources
func WithWorkloadRetention(value *workloadRetentionConfig) Option {
	return func(r *WorkloadReconciler) {
		r.workloadRetention = value
	}
}

type WorkloadUpdateWatcher interface {
	NotifyWorkloadUpdate(oldWl, newWl *kueue.Workload)
}

// WorkloadReconciler reconciles a Workload object
type WorkloadReconciler struct {
	log                 logr.Logger
	queues              *qcache.Manager
	cache               *schdcache.Cache
	client              client.Client
	watchers            []WorkloadUpdateWatcher
	waitForPodsReady    *waitForPodsReadyConfig
	recorder            record.EventRecorder
	clock               clock.Clock
	workloadRetention   *workloadRetentionConfig
	draReconcileChannel chan event.TypedGenericEvent[*kueue.Workload]
}

var _ reconcile.Reconciler = (*WorkloadReconciler)(nil)
var _ predicate.TypedPredicate[*kueue.Workload] = (*WorkloadReconciler)(nil)

func NewWorkloadReconciler(client client.Client, queues *qcache.Manager, cache *schdcache.Cache, recorder record.EventRecorder, options ...Option) *WorkloadReconciler {
	r := &WorkloadReconciler{
		log:                 ctrl.Log.WithName("workload-reconciler"),
		client:              client,
		queues:              queues,
		cache:               cache,
		recorder:            recorder,
		clock:               realClock,
		draReconcileChannel: make(chan event.TypedGenericEvent[*kueue.Workload], updateChBuffer),
	}
	for _, option := range options {
		option(r)
	}
	return r
}

// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update;patch
// +kubebuilder:rbac:groups="",resources=limitranges,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=node.k8s.io,resources=runtimeclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=resource.k8s.io,resources=resourceclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups=resource.k8s.io,resources=resourceclaimtemplates,verbs=get;list;watch

func (r *WorkloadReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var wl kueue.Workload
	if err := r.client.Get(ctx, req.NamespacedName, &wl); err != nil {
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile Workload")

	if len(wl.OwnerReferences) == 0 && !wl.DeletionTimestamp.IsZero() {
		// manual deletion triggered by the user
		err := workload.RemoveFinalizer(ctx, r.client, &wl)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	finishedCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadFinished)
	if finishedCond != nil && finishedCond.Status == metav1.ConditionTrue {
		if !features.Enabled(features.ObjectRetentionPolicies) || r.workloadRetention == nil || r.workloadRetention.afterFinished == nil {
			return ctrl.Result{}, nil
		}

		now := r.clock.Now()
		expirationTime := finishedCond.LastTransitionTime.Add(*r.workloadRetention.afterFinished)
		if now.Before(expirationTime) {
			remainingTime := expirationTime.Sub(now)
			log.V(3).Info("Requeueing workload for deletion after retention period", "remainingTime", remainingTime)
			return ctrl.Result{RequeueAfter: remainingTime}, nil
		}

		log.V(2).Info("Deleting workload because it has finished and the retention period has elapsed", "retention", *r.workloadRetention.afterFinished)
		if err := r.client.Delete(ctx, &wl); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		r.recorder.Eventf(&wl, corev1.EventTypeNormal, "Deleted", "Deleted finished workload due to elapsed retention")
		return ctrl.Result{}, nil
	}

	if workload.IsAdmitted(&wl) && workload.HasUnhealthyNodes(&wl) {
		log.V(3).Info("Skipping reconcile of a workload with nodes to replace", "unhealthyNodes", workload.UnhealthyNodeNames(&wl))
		return ctrl.Result{}, nil
	}

	if features.Enabled(features.DynamicResourceAllocation) && workload.Status(&wl) == workload.StatusPending &&
		workload.HasDRA(&wl) {
		workload.AdjustResources(ctx, r.client, &wl)
		if workload.HasResourceClaim(&wl) {
			log.V(3).Info("Workload is inadmissible because it uses resource claims which is not supported")
			if workload.UnsetQuotaReservationWithCondition(&wl, kueue.WorkloadInadmissible, "DynamicResourceAllocation feature does not support use of resource claims", r.clock.Now()) {
				workload.SetRequeuedCondition(&wl, kueue.WorkloadInadmissible, "DRA resource claims not supported", false)
				if err := workload.ApplyAdmissionStatus(ctx, r.client, &wl, true, r.clock); err != nil {
					return ctrl.Result{}, fmt.Errorf("failed to update workload status for DRA resource claims error: %w", err)
				}
			}
			return ctrl.Result{}, nil
		}

		log.V(3).Info("Processing DRA resources for workload")
		draResources, fieldErrs := dra.GetResourceRequestsForResourceClaimTemplates(ctx, r.client, &wl)
		if len(fieldErrs) > 0 {
			err := fieldErrs.ToAggregate()
			log.Error(err, "Failed to process DRA resources for workload")
			if workload.UnsetQuotaReservationWithCondition(&wl, kueue.WorkloadInadmissible, err.Error(), r.clock.Now()) {
				workload.SetRequeuedCondition(&wl, kueue.WorkloadInadmissible, err.Error(), false)
				if updateErr := workload.ApplyAdmissionStatus(ctx, r.client, &wl, true, r.clock); updateErr != nil {
					return ctrl.Result{}, fmt.Errorf("failed to update workload status for DRA error: %w", updateErr)
				}
			}
			return ctrl.Result{}, err
		}

		quotaReservedCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
		requeuedCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadRequeued)

		var conditionsCleared bool
		if quotaReservedCond != nil && quotaReservedCond.Status == metav1.ConditionFalse {
			apimeta.RemoveStatusCondition(&wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			conditionsCleared = true
		}
		if requeuedCond != nil && requeuedCond.Status == metav1.ConditionFalse {
			apimeta.RemoveStatusCondition(&wl.Status.Conditions, kueue.WorkloadRequeued)
			conditionsCleared = true
		}

		if conditionsCleared {
			log.V(3).Info("Cleared previous inadmissible conditions after successful DRA processing")
		}

		var queueOptions []workload.InfoOption
		if len(draResources) > 0 {
			queueOptions = append(queueOptions, workload.WithPreprocessedDRAResources(draResources))
		}

		if workload.IsActive(&wl) && !workload.HasQuotaReservation(&wl) {
			if err := r.queues.AddOrUpdateWorkload(&wl, queueOptions...); err != nil {
				log.V(2).Info("Failed to add DRA workload to queue", "error", err)
				return ctrl.Result{}, err
			}
		} else {
			if !r.cache.AddOrUpdateWorkload(log, &wl) {
				log.V(2).Info("ClusterQueue for workload didn't exist; ignored for now")
			}
		}
		log.V(3).Info("Successfully pre-processed and queued DRA workload in scheduler")
	}

	if workload.IsActive(&wl) {
		if apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadDeactivationTarget) {
			wl.Spec.Active = ptr.To(false)
			err := r.client.Update(ctx, &wl)
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}

		var updated bool
		var requeueAfter time.Duration
		err := workload.PatchAdmissionStatus(ctx, r.client, &wl, r.clock, func() (*kueue.Workload, bool, error) {
			if cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadRequeued); cond != nil && cond.Status == metav1.ConditionFalse {
				switch cond.Reason {
				case kueue.WorkloadDeactivated:
					workload.SetRequeuedCondition(&wl, kueue.WorkloadReactivated, "The workload was reactivated", true)
					updated = true
				case kueue.WorkloadEvictedByPodsReadyTimeout, kueue.WorkloadEvictedByAdmissionCheck:

					if wl.Status.RequeueState != nil && wl.Status.RequeueState.RequeueAt != nil {
						requeueAfter = wl.Status.RequeueState.RequeueAt.Sub(r.clock.Now())
					}
					if requeueAfter > 0 {
						return nil, updated, nil
					}
					if wl.Status.RequeueState != nil {
						wl.Status.RequeueState.RequeueAt = nil
					}
					workload.SetRequeuedCondition(&wl, kueue.WorkloadBackoffFinished, "The workload backoff was finished", true)
					updated = true
				}
			}
			return &wl, updated, nil
		})

		if requeueAfter > 0 {
			return reconcile.Result{RequeueAfter: requeueAfter}, nil
		}
		if updated {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	} else {
		var updated, evicted bool
		var underlyingCause kueue.EvictionUnderlyingCause
		reason := kueue.WorkloadDeactivated
		message := "The workload is deactivated"
		dtCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadDeactivationTarget)
		if !apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadEvicted) {
			if dtCond != nil {
				underlyingCause = kueue.EvictionUnderlyingCause(dtCond.Reason)
				message = fmt.Sprintf("%s due to %s", message, dtCond.Message)
			}
			updated = true
			evicted = true
		}
		wlOrig := wl.DeepCopy()
		if dtCond != nil {
			apimeta.RemoveStatusCondition(&wl.Status.Conditions, kueue.WorkloadDeactivationTarget)
		}
		if wl.Status.RequeueState != nil {
			wl.Status.RequeueState = nil
			updated = true
		}
		if updated {
			if evicted {
				if err := workload.Evict(ctx, r.client, r.recorder, wlOrig, reason, message, underlyingCause, r.clock, workload.WithCustomPrepare(func() (*kueue.Workload, error) {
					return &wl, nil
				})); err != nil {
					if !apierrors.IsNotFound(err) {
						return ctrl.Result{}, fmt.Errorf("setting eviction: %w", err)
					}
				}
			} else {
				if err := workload.PatchAdmissionStatus(ctx, r.client, wlOrig, r.clock, func() (*kueue.Workload, bool, error) {
					return &wl, true, nil
				}); err != nil {
					if !apierrors.IsNotFound(err) {
						return ctrl.Result{}, fmt.Errorf("updating workload: %w", err)
					}
				}
			}
			return ctrl.Result{}, nil
		}
	}

	lq := kueue.LocalQueue{}
	err := r.client.Get(ctx, types.NamespacedName{Namespace: wl.Namespace, Name: string(wl.Spec.QueueName)}, &lq)
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}

	lqExists := err == nil
	lqActive := ptr.Deref(lq.Spec.StopPolicy, kueue.None) == kueue.None
	if lqExists && lqActive && isDisabledRequeuedByLocalQueueStopped(&wl) {
		return ctrl.Result{}, client.IgnoreNotFound(workload.PatchAdmissionStatus(ctx, r.client, &wl, r.clock, func() (*kueue.Workload, bool, error) {
			return &wl, workload.SetRequeuedCondition(&wl, kueue.WorkloadLocalQueueRestarted, "The LocalQueue was restarted after being stopped", true), nil
		}))
	}

	cqName, cqOk := r.queues.ClusterQueueForWorkload(&wl)
	if cqOk {
		// because we need to react to API cluster cq events, the list of checks from a cache can lead to race conditions
		cq := kueue.ClusterQueue{}
		if err := r.client.Get(ctx, types.NamespacedName{Name: string(cqName)}, &cq); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		// If stopped cluster queue is started we need to set the WorkloadRequeued condition to true.
		if isDisabledRequeuedByClusterQueueStopped(&wl) && ptr.Deref(cq.Spec.StopPolicy, kueue.None) == kueue.None {
			return ctrl.Result{}, client.IgnoreNotFound(workload.PatchAdmissionStatus(ctx, r.client, &wl, r.clock, func() (*kueue.Workload, bool, error) {
				return &wl, workload.SetRequeuedCondition(&wl, kueue.WorkloadClusterQueueRestarted, "The ClusterQueue was restarted after being stopped", true), nil
			}))
		}
		if updated, err := r.reconcileSyncAdmissionChecks(ctx, &wl, &cq); updated || err != nil {
			return ctrl.Result{}, err
		}
	}

	// If the workload is admitted, updating the status here would set the Admitted condition to
	// false before the workloads eviction.
	if !workload.IsAdmitted(&wl) {
		var updated bool
		if err := workload.PatchAdmissionStatus(ctx, r.client, &wl, r.clock, func() (*kueue.Workload, bool, error) {
			updated = workload.SyncAdmittedCondition(&wl, r.clock.Now())
			return &wl, updated, nil
		}); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		if workload.IsAdmitted(&wl) {
			queuedWaitTime := workload.QueuedWaitTime(&wl, r.clock)
			quotaReservedCondition := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			quotaReservedWaitTime := r.clock.Since(quotaReservedCondition.LastTransitionTime.Time)
			r.recorder.Eventf(&wl, corev1.EventTypeNormal, "Admitted", "Admitted by ClusterQueue %v, wait time since reservation was %.0fs", wl.Status.Admission.ClusterQueue, quotaReservedWaitTime.Seconds())
			priorityClassName := workload.PriorityClassName(&wl)
			metrics.AdmittedWorkload(cqName, priorityClassName, queuedWaitTime)
			metrics.ReportAdmissionChecksWaitTime(cqName, priorityClassName, quotaReservedWaitTime)
			if features.Enabled(features.LocalQueueMetrics) {
				metrics.LocalQueueAdmittedWorkload(
					metrics.LQRefFromWorkload(&wl),
					priorityClassName,
					queuedWaitTime,
				)
				metrics.ReportLocalQueueAdmissionChecksWaitTime(
					metrics.LQRefFromWorkload(&wl),
					priorityClassName,
					quotaReservedWaitTime,
				)
			}
		}
		if updated {
			return ctrl.Result{}, nil
		}
	}

	if workload.HasQuotaReservation(&wl) {
		if evictionTriggered, err := r.reconcileCheckBasedEviction(ctx, &wl); evictionTriggered || err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}

		if updated, err := r.reconcileOnLocalQueueActiveState(ctx, &wl, lqExists, &lq); updated || err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}

		if updated, err := r.reconcileOnClusterQueueActiveState(ctx, &wl, cqName); updated || err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}

		podsReadyRecheckAfter, err := r.reconcileNotReadyTimeout(ctx, req, &wl)
		if err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		maxExecRecheckAfter, err := r.reconcileMaxExecutionTime(ctx, &wl)
		if err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}

		// get the minimun non-zero value
		recheckAfter := min(podsReadyRecheckAfter, maxExecRecheckAfter)
		if recheckAfter == 0 {
			recheckAfter = max(podsReadyRecheckAfter, maxExecRecheckAfter)
		}
		return ctrl.Result{RequeueAfter: recheckAfter}, nil
	}

	switch {
	case !lqExists:
		log.V(3).Info("Workload is inadmissible because of missing LocalQueue", "localQueue", klog.KRef(wl.Namespace, string(wl.Spec.QueueName)))
		if err := workload.PatchAdmissionStatus(ctx, r.client, &wl, r.clock, func() (*kueue.Workload, bool, error) {
			return &wl, workload.UnsetQuotaReservationWithCondition(&wl, kueue.WorkloadInadmissible, fmt.Sprintf("LocalQueue %s doesn't exist", wl.Spec.QueueName), r.clock.Now()), nil
		}); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	case !lqActive:
		log.V(3).Info("Workload is inadmissible because of stopped LocalQueue", "localQueue", klog.KRef(wl.Namespace, string(wl.Spec.QueueName)))
		if err := workload.PatchAdmissionStatus(ctx, r.client, &wl, r.clock, func() (*kueue.Workload, bool, error) {
			return &wl, workload.UnsetQuotaReservationWithCondition(&wl, kueue.WorkloadInadmissible, fmt.Sprintf("LocalQueue %s is inactive", wl.Spec.QueueName), r.clock.Now()), nil
		}); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	case !cqOk:
		log.V(3).Info("Workload is inadmissible because of missing ClusterQueue", "clusterQueue", klog.KRef("", string(cqName)))
		if err := workload.PatchAdmissionStatus(ctx, r.client, &wl, r.clock, func() (*kueue.Workload, bool, error) {
			return &wl, workload.UnsetQuotaReservationWithCondition(&wl, kueue.WorkloadInadmissible, fmt.Sprintf("ClusterQueue %s doesn't exist", cqName), r.clock.Now()), nil
		}); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	case !r.cache.ClusterQueueActive(cqName):
		log.V(3).Info("Workload is inadmissible because ClusterQueue is inactive", "clusterQueue", klog.KRef("", string(cqName)))
		if err := workload.PatchAdmissionStatus(ctx, r.client, &wl, r.clock, func() (*kueue.Workload, bool, error) {
			return &wl, workload.UnsetQuotaReservationWithCondition(&wl, kueue.WorkloadInadmissible, fmt.Sprintf("ClusterQueue %s is inactive", cqName), r.clock.Now()), nil
		}); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	}

	return ctrl.Result{}, nil
}

// isDisabledRequeuedByClusterQueueStopped returns true if the workload is unset requeued by cluster queue stopped.
func isDisabledRequeuedByClusterQueueStopped(w *kueue.Workload) bool {
	return isDisabledRequeuedByReason(w, kueue.WorkloadEvictedByClusterQueueStopped)
}

// isDisabledRequeuedByLocalQueueStopped returns true if the workload is unset requeued by local queue stopped.
func isDisabledRequeuedByLocalQueueStopped(w *kueue.Workload) bool {
	return isDisabledRequeuedByReason(w, kueue.WorkloadEvictedByLocalQueueStopped)
}

// isDisabledRequeuedByReason returns true if the workload is unset requeued by reason.
func isDisabledRequeuedByReason(w *kueue.Workload, reason string) bool {
	cond := apimeta.FindStatusCondition(w.Status.Conditions, kueue.WorkloadRequeued)
	return cond != nil && cond.Status == metav1.ConditionFalse && cond.Reason == reason
}

// reconcileMaxExecutionTime deactivates the workload if its MaximumExecutionTimeSeconds is exceeded or returns a retry after value.
func (r *WorkloadReconciler) reconcileMaxExecutionTime(ctx context.Context, wl *kueue.Workload) (time.Duration, error) {
	admittedCondition := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadAdmitted)
	if admittedCondition == nil || admittedCondition.Status != metav1.ConditionTrue || wl.Spec.MaximumExecutionTimeSeconds == nil {
		return 0, nil
	}

	remainingTime := time.Duration(*wl.Spec.MaximumExecutionTimeSeconds-ptr.Deref(wl.Status.AccumulatedPastExecutionTimeSeconds, 0))*time.Second - r.clock.Since(admittedCondition.LastTransitionTime.Time)
	if remainingTime > 0 {
		return remainingTime, nil
	}

	if !apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadDeactivationTarget) {
		err := workload.PatchAdmissionStatus(ctx, r.client, wl, r.clock, func() (*kueue.Workload, bool, error) {
			updated := workload.SetDeactivationTarget(wl, kueue.WorkloadMaximumExecutionTimeExceeded, "exceeding the maximum execution time")
			if wl.Status.AccumulatedPastExecutionTimeSeconds != nil {
				wl.Status.AccumulatedPastExecutionTimeSeconds = nil
				updated = true
			}
			return wl, updated, nil
		})
		if err != nil {
			return 0, err
		}
		r.recorder.Eventf(wl, corev1.EventTypeWarning, kueue.WorkloadMaximumExecutionTimeExceeded, "The maximum execution time (%ds) exceeded", *wl.Spec.MaximumExecutionTimeSeconds)
	}
	return 0, nil
}

// reconcileCheckBasedEviction evicts or deactivates the given Workload if any admission checks have failed.
// Returns true if the Workload was rejected or deactivated, and false otherwise.
func (r *WorkloadReconciler) reconcileCheckBasedEviction(ctx context.Context, wl *kueue.Workload) (bool, error) {
	if apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadEvicted) || (!workload.HasRetryChecks(wl) && !workload.HasRejectedChecks(wl)) {
		return false, nil
	}
	log := ctrl.LoggerFrom(ctx)
	log.V(3).Info("Workload is evicted due to admission checks")
	if workload.HasRejectedChecks(wl) {
		var rejectedCheckNames []kueue.AdmissionCheckReference
		for _, check := range workload.RejectedChecks(wl) {
			rejectedCheckNames = append(rejectedCheckNames, check.Name)
		}
		err := workload.PatchAdmissionStatus(ctx, r.client, wl, r.clock, func() (*kueue.Workload, bool, error) {
			return wl, workload.SetDeactivationTarget(wl, kueue.WorkloadEvictedByAdmissionCheck, fmt.Sprintf("Admission check(s): %v, were rejected", stringsutils.Join(rejectedCheckNames, ","))), nil
		})
		if err != nil {
			return false, err
		}
		log.V(3).Info("Workload is evicted due to rejected admission checks", "workload", klog.KObj(wl), "rejectedChecks", rejectedCheckNames)
		rejectedCheck := workload.RejectedChecks(wl)[0]
		r.recorder.Eventf(wl, corev1.EventTypeWarning, "AdmissionCheckRejected", "Deactivating workload because AdmissionCheck for %v was Rejected: %s", rejectedCheck.Name, rejectedCheck.Message)
		return true, nil
	}
	// at this point we know a Workload has at least one Retry AdmissionCheck
	message := "At least one admission check is false"
	if err := workload.Evict(ctx, r.client, r.recorder, wl, kueue.WorkloadEvictedByAdmissionCheck, message, "", r.clock); err != nil {
		return false, err
	}
	return true, nil
}

func (r *WorkloadReconciler) reconcileSyncAdmissionChecks(ctx context.Context, wl *kueue.Workload, cq *kueue.ClusterQueue) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	admissionChecks := workload.AdmissionChecksForWorkload(log, wl, admissioncheck.NewAdmissionChecks(cq), qutil.AllFlavors(cq.Spec.ResourceGroups))
	newChecks, shouldUpdate := syncAdmissionCheckConditions(wl.Status.AdmissionChecks, admissionChecks, r.clock)
	if shouldUpdate {
		log.V(3).Info("The workload needs admission checks updates", "clusterQueue", klog.KRef("", cq.Name), "admissionChecks", admissionChecks)
		wl.Status.AdmissionChecks = newChecks
		err := r.client.Status().Update(ctx, wl)
		return true, client.IgnoreNotFound(err)
	}
	return false, nil
}

func (r *WorkloadReconciler) reconcileOnLocalQueueActiveState(ctx context.Context, wl *kueue.Workload, lqExists bool, lq *kueue.LocalQueue) (bool, error) {
	queueStopPolicy := ptr.Deref(lq.Spec.StopPolicy, kueue.None)

	log := ctrl.LoggerFrom(ctx)

	if workload.IsAdmitted(wl) {
		if queueStopPolicy != kueue.HoldAndDrain {
			return false, nil
		}
		if apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadEvicted) {
			log.V(3).Info("Workload is already evicted.")
			return false, nil
		}
		log.V(3).Info("Workload is evicted because the LocalQueue is stopped", "localQueue", klog.KRef(wl.Namespace, string(wl.Spec.QueueName)))
		err := workload.Evict(ctx, r.client, r.recorder, wl, kueue.WorkloadEvictedByLocalQueueStopped, "The LocalQueue is stopped", "", r.clock)
		return true, err
	}

	if !lqExists || !lq.DeletionTimestamp.IsZero() {
		log.V(3).Info("Workload is inadmissible because the LocalQueue is terminating or missing", "localQueue", klog.KRef("", string(wl.Spec.QueueName)))
		return true, workload.PatchAdmissionStatus(ctx, r.client, wl, r.clock, func() (*kueue.Workload, bool, error) {
			return wl, workload.UnsetQuotaReservationWithCondition(wl, kueue.WorkloadInadmissible, fmt.Sprintf("LocalQueue %s is terminating or missing", wl.Spec.QueueName), r.clock.Now()), nil
		})
	}

	if queueStopPolicy != kueue.None {
		log.V(3).Info("Workload is inadmissible because the LocalQueue is stopped", "localQueue", klog.KRef("", string(wl.Spec.QueueName)))
		return true, workload.PatchAdmissionStatus(ctx, r.client, wl, r.clock, func() (*kueue.Workload, bool, error) {
			return wl, workload.UnsetQuotaReservationWithCondition(wl, kueue.WorkloadInadmissible, fmt.Sprintf("LocalQueue %s is stopped", wl.Spec.QueueName), r.clock.Now()), nil
		})
	}

	return false, nil
}

func (r *WorkloadReconciler) reconcileOnClusterQueueActiveState(ctx context.Context, wl *kueue.Workload, cqName kueue.ClusterQueueReference) (bool, error) {
	cq := kueue.ClusterQueue{}
	err := r.client.Get(ctx, types.NamespacedName{Name: string(cqName)}, &cq)
	if client.IgnoreNotFound(err) != nil {
		return false, err
	}
	cqExists := err == nil

	queueStopPolicy := ptr.Deref(cq.Spec.StopPolicy, kueue.None)

	log := ctrl.LoggerFrom(ctx)
	if workload.IsAdmitted(wl) {
		if queueStopPolicy != kueue.HoldAndDrain {
			return false, nil
		}
		if apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadEvicted) {
			log.V(3).Info("Workload is already evicted.")
			return false, nil
		}
		log.V(3).Info("Workload is evicted because the ClusterQueue is stopped", "clusterQueue", klog.KRef("", string(cqName)))
		message := "The ClusterQueue is stopped"
		err := workload.Evict(ctx, r.client, r.recorder, wl, kueue.WorkloadEvictedByClusterQueueStopped, message, "", r.clock)
		return true, err
	}

	if !cqExists || !cq.DeletionTimestamp.IsZero() {
		log.V(3).Info("Workload is inadmissible because the ClusterQueue is terminating or missing", "clusterQueue", klog.KRef("", string(cqName)))
		return true, workload.PatchAdmissionStatus(ctx, r.client, wl, r.clock, func() (*kueue.Workload, bool, error) {
			return wl, workload.UnsetQuotaReservationWithCondition(wl, kueue.WorkloadInadmissible, fmt.Sprintf("ClusterQueue %s is terminating or missing", cqName), r.clock.Now()), nil
		})
	}

	if queueStopPolicy != kueue.None {
		log.V(3).Info("Workload is inadmissible because the ClusterQueue is stopped", "clusterQueue", klog.KRef("", string(cqName)))
		return true, workload.PatchAdmissionStatus(ctx, r.client, wl, r.clock, func() (*kueue.Workload, bool, error) {
			return wl, workload.UnsetQuotaReservationWithCondition(wl, kueue.WorkloadInadmissible, fmt.Sprintf("ClusterQueue %s is stopped", cqName), r.clock.Now()), nil
		})
	}

	return false, nil
}

func syncAdmissionCheckConditions(conds []kueue.AdmissionCheckState, admissionChecks sets.Set[kueue.AdmissionCheckReference], c clock.Clock) ([]kueue.AdmissionCheckState, bool) {
	if len(admissionChecks) == 0 {
		return nil, len(conds) > 0
	}

	shouldUpdate := false
	currentChecks := utilslices.ToRefMap(conds, func(c *kueue.AdmissionCheckState) kueue.AdmissionCheckReference { return c.Name })
	for t := range admissionChecks {
		if _, found := currentChecks[t]; !found {
			workload.SetAdmissionCheckState(&conds, kueue.AdmissionCheckState{
				Name:  t,
				State: kueue.CheckStatePending,
			}, c)
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

func (r *WorkloadReconciler) reconcileNotReadyTimeout(ctx context.Context, req ctrl.Request, wl *kueue.Workload) (time.Duration, error) {
	log := ctrl.LoggerFrom(ctx)

	if !workload.IsActive(wl) || apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadEvicted) {
		// the workload has already been evicted by the PodsReadyTimeout or been deactivated.
		return 0, nil
	}

	underlyingCause, recheckAfter := r.admittedNotReadyWorkload(wl)
	if underlyingCause == "" {
		return 0, nil
	}
	if recheckAfter > 0 {
		log.V(4).Info("Workload not yet ready and did not exceed its timeout", "recheckAfter", recheckAfter)
		return recheckAfter, nil
	}
	log.V(2).Info("Start the eviction of the workload due to exceeding the PodsReady timeout")
	message := fmt.Sprintf("Exceeded the PodsReady timeout %s", req.String())
	err := workload.Evict(ctx, r.client, r.recorder, wl, kueue.WorkloadEvictedByPodsReadyTimeout, message, underlyingCause, r.clock, workload.WithCustomPrepare(func() (*kueue.Workload, error) {
		if deactivated, err := r.triggerDeactivationOrBackoffRequeue(ctx, wl); deactivated || err != nil {
			return nil, err
		}
		return wl, nil
	}))
	return 0, err
}

// triggerDeactivationOrBackoffRequeue trigger deactivation of workload
// if a re-queued number has already exceeded the limit of re-queuing backoff.
// Otherwise, it increments a re-queueing count and update a time to be re-queued.
// It returns true as a first value if a workload triggered deactivation.
func (r *WorkloadReconciler) triggerDeactivationOrBackoffRequeue(ctx context.Context, wl *kueue.Workload) (bool, error) {
	if wl.Status.RequeueState == nil {
		wl.Status.RequeueState = &kueue.RequeueState{}
	}
	// If requeuingBackoffLimitCount equals to null, the workloads is repeatedly and endless re-queued.
	if r.waitForPodsReady.requeuingBackoffLimitCount != nil && ptr.Deref(wl.Status.RequeueState.Count, 0)+1 > *r.waitForPodsReady.requeuingBackoffLimitCount {
		if err := workload.PatchAdmissionStatus(ctx, r.client, wl, r.clock, func() (*kueue.Workload, bool, error) {
			return wl, workload.SetDeactivationTarget(wl, kueue.WorkloadRequeuingLimitExceeded, "exceeding the maximum number of re-queuing retries"), nil
		}); err != nil {
			return false, err
		}
		return true, nil
	}
	workload.UpdateRequeueState(wl, r.waitForPodsReady.requeuingBackoffBaseSeconds, int32(r.waitForPodsReady.requeuingBackoffMaxDuration.Seconds()), r.clock)
	return false, nil
}

func (r *WorkloadReconciler) Create(e event.TypedCreateEvent[*kueue.Workload]) bool {
	defer r.notifyWatchers(nil, e.Object)
	status := workload.Status(e.Object)
	log := r.log.WithValues("workload", klog.KObj(e.Object), "queue", e.Object.Spec.QueueName, "status", status)
	log.V(2).Info("Workload create event")

	if status == workload.StatusFinished {
		return true
	}

	ctx := ctrl.LoggerInto(context.Background(), log)
	wlCopy := e.Object.DeepCopy()
	workload.AdjustResources(ctx, r.client, wlCopy)

	// It is intentional for this code to be not guarded behind a feature gate
	// DRA workloads need to have certain error handling and hence to be handled in Reconcile loop
	if features.Enabled(features.DynamicResourceAllocation) && workload.HasDRA(e.Object) {
		log.V(2).Info("Skipping DRA workload in Create event - will be handled in Reconcile")
		return true
	}

	if workload.IsActive(e.Object) && !workload.HasQuotaReservation(e.Object) {
		if err := r.queues.AddOrUpdateWorkload(wlCopy); err != nil {
			log.V(2).Info("ignored an error for now", "error", err)
		}
		return true
	}
	if !r.cache.AddOrUpdateWorkload(log, wlCopy) {
		log.V(2).Info("ClusterQueue for workload didn't exist; ignored for now")
	}
	r.queues.QueueSecondPassIfNeeded(ctx, e.Object, 0)
	return true
}

func (r *WorkloadReconciler) Delete(e event.TypedDeleteEvent[*kueue.Workload]) bool {
	defer r.notifyWatchers(e.Object, nil)
	status := "unknown"
	if !e.DeleteStateUnknown {
		status = workload.Status(e.Object)
	}
	log := r.log.WithValues("workload", klog.KObj(e.Object), "queue", e.Object.Spec.QueueName, "status", status)
	log.V(2).Info("Workload delete event")
	ctx := ctrl.LoggerInto(context.Background(), log)

	// When assigning a clusterQueue to a workload, we assume it in the cache. If
	// the state is unknown, the workload could have been assumed, and we need
	// to clear it from the cache.
	if workload.HasQuotaReservation(e.Object) || e.DeleteStateUnknown {
		// trigger the move of associated inadmissibleWorkloads if required.
		r.queues.QueueAssociatedInadmissibleWorkloadsAfter(ctx, e.Object, func() {
			// Delete the workload from cache while holding the queues lock
			// to guarantee that requeued workloads are taken into account before
			// the next scheduling cycle.
			if err := r.cache.DeleteWorkload(log, e.Object); err != nil {
				if !e.DeleteStateUnknown {
					log.Error(err, "Failed to delete workload from cache")
				}
			}
		})
	}

	// Even if the state is unknown, the last cached state tells us whether the
	// workload was in the queues and should be cleared from them.
	r.queues.DeleteWorkload(e.Object)

	return true
}

func (r *WorkloadReconciler) Update(e event.TypedUpdateEvent[*kueue.Workload]) bool {
	defer r.notifyWatchers(e.ObjectOld, e.ObjectNew)

	status := workload.Status(e.ObjectNew)
	log := r.log.WithValues("workload", klog.KObj(e.ObjectNew), "queue", e.ObjectNew.Spec.QueueName, "status", status)
	ctx := ctrl.LoggerInto(context.Background(), log)
	active := workload.IsActive(e.ObjectNew)

	prevQueue := e.ObjectOld.Spec.QueueName
	if prevQueue != e.ObjectNew.Spec.QueueName {
		log = log.WithValues("prevQueue", prevQueue)
	}
	prevStatus := workload.Status(e.ObjectOld)
	if prevStatus != status {
		log = log.WithValues("prevStatus", prevStatus)
	}
	if workload.HasQuotaReservation(e.ObjectNew) {
		log = log.WithValues("clusterQueue", e.ObjectNew.Status.Admission.ClusterQueue)
	}
	if workload.HasQuotaReservation(e.ObjectOld) && (!workload.HasQuotaReservation(e.ObjectNew) || e.ObjectNew.Status.Admission.ClusterQueue != e.ObjectOld.Status.Admission.ClusterQueue) {
		log = log.WithValues("prevClusterQueue", e.ObjectOld.Status.Admission.ClusterQueue)
	}
	log.V(2).Info("Workload update event")

	wlCopy := e.ObjectNew.DeepCopy()
	// We do not handle old workload here as it will be deleted or replaced by new one anyway.
	workload.AdjustResources(ctrl.LoggerInto(ctx, log), r.client, wlCopy)

	switch {
	case status == workload.StatusFinished || !active:
		if !active {
			log.V(2).Info("Workload will not be queued because the workload is not active")
		}
		// The workload could have been in the queues if we missed an event.
		r.queues.DeleteWorkload(e.ObjectNew)

		// trigger the move of associated inadmissibleWorkloads, if there are any.
		r.queues.QueueAssociatedInadmissibleWorkloadsAfter(ctx, e.ObjectNew, func() {
			// Delete the workload from cache while holding the queues lock
			// to guarantee that requeued workloads are taken into account before
			// the next scheduling cycle.
			if err := r.cache.DeleteWorkload(log, e.ObjectOld); err != nil && prevStatus == workload.StatusAdmitted {
				log.Error(err, "Failed to delete workload from cache")
			}
		})

	case prevStatus == workload.StatusPending && status == workload.StatusPending:
		// Skip queue operations for DRA workloads - they are handled in Reconcile loop
		if features.Enabled(features.DynamicResourceAllocation) && workload.HasDRA(e.ObjectNew) {
			log.V(2).Info("Skipping queue update for DRA workload - handled in Reconcile")
		} else {
			err := r.queues.UpdateWorkload(e.ObjectOld, wlCopy)
			if err != nil {
				log.V(2).Info("ignored an error for now", "error", err)
			}
		}
	case prevStatus == workload.StatusPending && (status == workload.StatusQuotaReserved || status == workload.StatusAdmitted):
		r.queues.DeleteWorkload(e.ObjectOld)
		if !r.cache.AddOrUpdateWorkload(log, wlCopy) {
			log.V(2).Info("ClusterQueue for workload didn't exist; ignored for now")
		}
	case (prevStatus == workload.StatusQuotaReserved || prevStatus == workload.StatusAdmitted) && status == workload.StatusPending:
		var backoff time.Duration
		if wlCopy.Status.RequeueState != nil && wlCopy.Status.RequeueState.RequeueAt != nil {
			backoff = time.Until(e.ObjectNew.Status.RequeueState.RequeueAt.Time)
		}
		immediate := backoff <= 0
		// trigger the move of associated inadmissibleWorkloads, if there are any.
		r.queues.QueueAssociatedInadmissibleWorkloadsAfter(ctx, e.ObjectNew, func() {
			// Delete the workload from cache while holding the queues lock
			// to guarantee that requeued workloads are taken into account before
			// the next scheduling cycle.
			if err := r.cache.DeleteWorkload(log, e.ObjectNew); err != nil {
				log.Error(err, "Failed to delete workload from cache")
			}
			// Here we don't take the lock as it is already taken by the wrapping
			// function.
			if immediate {
				// Skip queue operations for DRA workloads - they are handled in Reconcile loop
				if features.Enabled(features.DynamicResourceAllocation) && workload.HasDRA(e.ObjectNew) {
					log.V(2).Info("Skipping immediate requeue for DRA workload - handled in Reconcile")
				} else {
					if err := r.queues.AddOrUpdateWorkloadWithoutLock(wlCopy); err != nil {
						log.V(2).Info("ignored an error for now", "error", err)
					}
					r.queues.DeleteSecondPassWithoutLock(wlCopy)
				}
			}
		})

		if !immediate {
			log.V(3).Info("Workload to be requeued after backoff", "backoff", backoff, "requeueAt", e.ObjectNew.Status.RequeueState.RequeueAt.Time)
			// Skip delayed requeue for DRA workloads - they are handled in Reconcile loop
			if features.Enabled(features.DynamicResourceAllocation) && workload.HasDRA(e.ObjectNew) {
				log.V(3).Info("Skipping delayed requeue for DRA workload - handled in Reconcile")
			} else {
				time.AfterFunc(backoff, func() {
					updatedWl := kueue.Workload{}
					err := r.client.Get(ctx, client.ObjectKeyFromObject(e.ObjectNew), &updatedWl)
					if err == nil && workload.Status(&updatedWl) == workload.StatusPending {
						if err = r.queues.AddOrUpdateWorkload(wlCopy); err != nil {
							log.V(2).Info("ignored an error for now", "error", err)
						} else {
							log.V(3).Info("Workload requeued after backoff")
						}
					}
				})
			}
		}
	case prevStatus == workload.StatusAdmitted && status == workload.StatusAdmitted && !equality.Semantic.DeepEqual(e.ObjectOld.Status.ReclaimablePods, e.ObjectNew.Status.ReclaimablePods),
		features.Enabled(features.ElasticJobsViaWorkloadSlices) && workloadslicing.ScaledDown(workload.ExtractPodSetCountsFromWorkload(e.ObjectOld), workload.ExtractPodSetCountsFromWorkload(e.ObjectNew)),
		workloadPriorityClassChanged(e.ObjectOld, e.ObjectNew):
		// trigger the move of associated inadmissibleWorkloads, if there are any.
		r.queues.QueueAssociatedInadmissibleWorkloadsAfter(ctx, e.ObjectNew, func() {
			// Update the workload from cache while holding the queues lock
			// to guarantee that requeued workloads are taken into account before
			// the next scheduling cycle.
			if err := r.cache.UpdateWorkload(log, e.ObjectOld, wlCopy); err != nil {
				log.Error(err, "Failed to delete workload from cache")
			}
		})

	default:
		// Workload update in the cache is handled here; however, some fields are immutable
		// and are not supposed to actually change anything.
		if err := r.cache.UpdateWorkload(log, e.ObjectOld, wlCopy); err != nil {
			log.Error(err, "Updating workload in cache")
		}
	}
	r.queues.QueueSecondPassIfNeeded(ctx, e.ObjectNew, 0)
	return true
}

func workloadPriorityClassChanged(old, new *kueue.Workload) bool {
	return workload.IsWorkloadPriorityClass(old) && workload.IsWorkloadPriorityClass(new) &&
		workload.PriorityClassName(old) != "" && workload.PriorityClassName(new) != "" &&
		workload.PriorityClassName(old) != workload.PriorityClassName(new)
}

func (r *WorkloadReconciler) Generic(e event.TypedGenericEvent[*kueue.Workload]) bool {
	r.log.V(3).Info("Ignore Workload generic event", "workload", klog.KObj(e.Object))
	return false
}

func (r *WorkloadReconciler) notifyWatchers(oldWl, newWl *kueue.Workload) {
	for _, w := range r.watchers {
		w.NotifyWorkloadUpdate(oldWl, newWl)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkloadReconciler) SetupWithManager(mgr ctrl.Manager, cfg *config.Configuration) error {
	ruh := &resourceUpdatesHandler{r: r}
	wqh := &workloadQueueHandler{r: r}
	deh := &draEventHandler{}
	return builder.TypedControllerManagedBy[reconcile.Request](mgr).
		Named("workload_controller").
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&kueue.Workload{},
			&handler.TypedEnqueueRequestForObject[*kueue.Workload]{},
			r,
		)).
		WatchesRawSource(source.Channel(r.draReconcileChannel, deh)).
		WithOptions(controller.Options{
			NeedLeaderElection:      ptr.To(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[kueue.GroupVersion.WithKind("Workload").GroupKind().String()],
		}).
		Watches(&corev1.LimitRange{}, ruh).
		Watches(&nodev1.RuntimeClass{}, ruh).
		Watches(&kueue.ClusterQueue{}, wqh).
		Watches(&kueue.LocalQueue{}, wqh).
		Complete(WithLeadingManager(mgr, r, &kueue.Workload{}, cfg))
}

// admittedNotReadyWorkload checks if a workload counts toward the PodsReady timeout
// and calculates the remaining timeout duration.
//
// It returns two values:
//  1. A underlyingCause that complements informarion carried by kueue.WorkloadEvictedByPodsReadyTimeout
//
// (e.g., WaitForStart, WaitForRecovery, or empty if not applicable).
//
//  2. The remaining time (in seconds) until the timeout is exceeded, based on
//     the maximum of the LastTransitionTime for the Admitted and PodsReady conditions.
//
// If the workload is not admitted, PodsReady is true, or no timeout is configured,
// it returns an empty underlyingCause and zero duration.
func (r *WorkloadReconciler) admittedNotReadyWorkload(wl *kueue.Workload) (kueue.EvictionUnderlyingCause, time.Duration) {
	if r.waitForPodsReady == nil {
		// the timeout is not configured for the workload controller
		return "", 0
	}
	if !workload.IsAdmitted(wl) {
		// the workload is not admitted so there is no need to time it out
		return "", 0
	}

	podsReadyCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadPodsReady)
	if podsReadyCond != nil && podsReadyCond.Status == metav1.ConditionTrue {
		return "", 0
	}

	if podsReadyCond == nil || podsReadyCond.Reason == kueue.WorkloadWaitForStart || podsReadyCond.Reason == "PodsReady" {
		admittedCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadAdmitted)
		elapsedTime := r.clock.Since(admittedCond.LastTransitionTime.Time)
		return kueue.WorkloadWaitForStart, max(r.waitForPodsReady.timeout-elapsedTime, 0)
	} else if podsReadyCond.Reason == kueue.WorkloadWaitForRecovery && r.waitForPodsReady.recoveryTimeout != nil {
		// A pod has failed and the workload is waiting for recovery
		elapsedTime := r.clock.Since(podsReadyCond.LastTransitionTime.Time)
		return kueue.WorkloadWaitForRecovery, max(*r.waitForPodsReady.recoveryTimeout-elapsedTime, 0)
	}
	return "", 0
}

type resourceUpdatesHandler struct {
	r *WorkloadReconciler
}

func (h *resourceUpdatesHandler) Create(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	log := ctrl.LoggerFrom(ctx).WithValues("kind", e.Object.GetObjectKind())
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(5).Info("Create event")
	h.handle(ctx, e.Object, q)
}

func (h *resourceUpdatesHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	log := ctrl.LoggerFrom(ctx).WithValues("kind", e.ObjectNew.GetObjectKind())
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(5).Info("Update event")
	h.handle(ctx, e.ObjectNew, q)
}

func (h *resourceUpdatesHandler) Delete(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	log := ctrl.LoggerFrom(ctx).WithValues("kind", e.Object.GetObjectKind())
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(5).Info("Delete event")
	h.handle(ctx, e.Object, q)
}

func (h *resourceUpdatesHandler) Generic(_ context.Context, _ event.GenericEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *resourceUpdatesHandler) handle(ctx context.Context, obj client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
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

func (h *resourceUpdatesHandler) queueReconcileForPending(ctx context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request], opts ...client.ListOption) {
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

		if features.Enabled(features.DynamicResourceAllocation) && workload.HasDRA(wlCopy) {
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      wlCopy.Name,
					Namespace: wlCopy.Namespace,
				},
			}
			q.Add(req)
			log.V(2).Info("Queued reconcile for DRA workload due to resource update")
			continue
		}

		if err = h.r.queues.AddOrUpdateWorkload(wlCopy); err != nil {
			log.V(2).Info("ignored an error for now", "error", err)
		}
	}
}

type workloadQueueHandler struct {
	r *WorkloadReconciler
}

var _ handler.EventHandler = (*workloadQueueHandler)(nil)

// Create is called in response to a create event.
func (w *workloadQueueHandler) Create(ctx context.Context, ev event.CreateEvent, wq workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if cq, isCq := ev.Object.(*kueue.ClusterQueue); isCq {
		log := ctrl.LoggerFrom(ctx).WithValues("clusterQueue", klog.KObj(cq))
		ctx = ctrl.LoggerInto(ctx, log)
		w.queueReconcileForWorkloadsOfClusterQueue(ctx, cq.Name, wq)
		return
	}
	if lq, isLq := ev.Object.(*kueue.LocalQueue); isLq {
		log := ctrl.LoggerFrom(ctx).WithValues("localQueue", klog.KObj(lq))
		ctx = ctrl.LoggerInto(ctx, log)
		w.queueReconcileForWorkloadsOfLocalQueue(ctx, lq, wq)
	}
}

// Update is called in response to an update event.
func (w *workloadQueueHandler) Update(ctx context.Context, ev event.UpdateEvent, wq workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	oldCq, oldIsCq := ev.ObjectOld.(*kueue.ClusterQueue)
	newCq, newIsCq := ev.ObjectNew.(*kueue.ClusterQueue)
	if oldIsCq && newIsCq {
		log := ctrl.LoggerFrom(ctx).WithValues("clusterQueue", klog.KObj(ev.ObjectNew))
		ctx = ctrl.LoggerInto(ctx, log)
		log.V(5).Info("Workload cluster queue update event")

		if !newCq.DeletionTimestamp.IsZero() ||
			!gocmp.Equal(oldCq.Spec.AdmissionChecksStrategy, newCq.Spec.AdmissionChecksStrategy) ||
			!ptr.Equal(oldCq.Spec.StopPolicy, newCq.Spec.StopPolicy) {
			w.queueReconcileForWorkloadsOfClusterQueue(ctx, newCq.Name, wq)
		}
		return
	}

	oldLq, oldIsLq := ev.ObjectOld.(*kueue.LocalQueue)
	newLq, newIsLq := ev.ObjectNew.(*kueue.LocalQueue)
	if oldIsLq && newIsLq {
		log := ctrl.LoggerFrom(ctx).WithValues("localQueue", klog.KObj(ev.ObjectNew))
		ctx = ctrl.LoggerInto(ctx, log)
		log.V(5).Info("Workload cluster queue update event")

		if !newLq.DeletionTimestamp.IsZero() || !ptr.Equal(oldLq.Spec.StopPolicy, newLq.Spec.StopPolicy) {
			w.queueReconcileForWorkloadsOfLocalQueue(ctx, newLq, wq)
		}
	}
}

// Delete is called in response to a delete event.
func (w *workloadQueueHandler) Delete(ctx context.Context, ev event.DeleteEvent, wq workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if cq, isCq := ev.Object.(*kueue.ClusterQueue); isCq {
		w.queueReconcileForWorkloadsOfClusterQueue(ctx, cq.Name, wq)
		return
	}
	if lq, isLq := ev.Object.(*kueue.LocalQueue); isLq {
		log := ctrl.LoggerFrom(ctx).WithValues("localQueue", klog.KObj(lq))
		ctx = ctrl.LoggerInto(ctx, log)
		w.queueReconcileForWorkloadsOfLocalQueue(ctx, lq, wq)
	}
}

// Generic is called in response to an event of an unknown type or a synthetic event triggered as a cron or
// external trigger request.
func (w *workloadQueueHandler) Generic(_ context.Context, _ event.GenericEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	// nothing to do here
}

func (w *workloadQueueHandler) queueReconcileForWorkloadsOfClusterQueue(ctx context.Context, cqName string, wq workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	log := ctrl.LoggerFrom(ctx)
	lst := kueue.LocalQueueList{}
	err := w.r.client.List(ctx, &lst, client.MatchingFields{indexer.QueueClusterQueueKey: cqName})
	if err != nil {
		log.Error(err, "Could not list cluster queues local queues")
	}
	for _, lq := range lst.Items {
		log := log.WithValues("localQueue", klog.KObj(&lq))
		ctx := ctrl.LoggerInto(ctx, log)
		w.queueReconcileForWorkloadsOfLocalQueue(ctx, &lq, wq)
	}
}

func (w *workloadQueueHandler) queueReconcileForWorkloadsOfLocalQueue(ctx context.Context, lq *kueue.LocalQueue, wq workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	log := ctrl.LoggerFrom(ctx)
	lst := kueue.WorkloadList{}
	err := w.r.client.List(ctx, &lst, &client.ListOptions{Namespace: lq.Namespace}, client.MatchingFields{indexer.WorkloadQueueKey: lq.Name})
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

type draEventHandler struct{}

var _ handler.TypedEventHandler[*kueue.Workload, reconcile.Request] = (*draEventHandler)(nil)

func (h *draEventHandler) Create(ctx context.Context, e event.TypedCreateEvent[*kueue.Workload], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *draEventHandler) Update(ctx context.Context, e event.TypedUpdateEvent[*kueue.Workload], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *draEventHandler) Delete(ctx context.Context, e event.TypedDeleteEvent[*kueue.Workload], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *draEventHandler) Generic(ctx context.Context, e event.TypedGenericEvent[*kueue.Workload], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	reconcileReq := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      e.Object.Name,
			Namespace: e.Object.Namespace,
		},
	}
	q.Add(reconcileReq)
	log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(e.Object))
	log.V(4).Info("Queued reconcile for DRA workload from event channel")
}

// GetDRAReconcileChannel returns the DRA reconcile channel for connecting to the queue manager.
func (r *WorkloadReconciler) GetDRAReconcileChannel() chan<- event.TypedGenericEvent[*kueue.Workload] {
	return r.draReconcileChannel
}
